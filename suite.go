package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type opResult struct {
	Before  string
	After   string
	Context string
	Details string
}

// op is a single NFS filesystem operation to test.
type op struct {
	Name string
	Fn   func(dir string) (opResult, error)
}

// PhaseSnapshot captures directory state at a point in time.
type PhaseSnapshot struct {
	Timestamp string   `json:"timestamp"`
	DirExists bool     `json:"dir_exists"`
	FileCount int      `json:"file_count"`
	Files     []string `json:"files,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// SuiteResult holds results from running a test suite against a directory.
type SuiteResult struct {
	Dir           string        `json:"dir"`
	Mode          string        `json:"mode"` // "isolated" or "shared"
	Before        PhaseSnapshot `json:"before"`
	Tests         []TestResult  `json:"tests"`
	After         PhaseSnapshot `json:"after"`
	Duration      string        `json:"duration"`
	Summary       SuiteSummary  `json:"summary"`
	ExistingFiles []string      `json:"existing_files,omitempty"`
}

type SuiteSummary struct {
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Total int `json:"total"`
}

// FullSuiteResult holds results from both isolated and shared test runs.
type FullSuiteResult struct {
	Timestamp      string       `json:"timestamp"`
	RunID          string       `json:"run_id"`
	User           string       `json:"user"`
	UID            string       `json:"uid"`
	GID            string       `json:"gid"`
	MountPath      string       `json:"mount_path"`
	MountInfo      string       `json:"mount_info"`
	Isolated       SuiteResult  `json:"isolated"`
	Shared         SuiteResult  `json:"shared"`
	OverallSummary SuiteSummary `json:"overall_summary"`
}

// coreOps returns the list of NFS operations to test.
// these run in order — some depend on artifacts from earlier ops (e.g. read depends on create).
func coreOps() []op {
	return []op{
		{"create_file", opCreateFile},
		{"read_file", opReadFile},
		{"stat_file", opStatFile},
		{"append_file", opAppendFile},
		{"overwrite_file", opOverwriteFile},
		{"chmod_file", opChmodFile},
		{"rename_file", opRenameFile},
		{"copy_file", opCopyFile},
		{"symlink", opSymlink},
		{"mkdir", opMkdir},
		{"nested_mkdir", opNestedMkdir},
		{"create_in_subdir", opCreateInSubdir},
		{"cross_dir_rename", opCrossDirRename},
		{"delete_file", opDeleteFile},
		{"rmdir", opRmdir},
		{"large_file_1mb", opLargeFile},
		{"concurrent_writes", opConcurrentWrites},
		{"file_lock", opFileLock},
		{"truncate_file", opTruncateFile},
		{"hardlink", opHardlink},
		{"mkfifo", opMkfifo},
		{"write_binary", opWriteBinary},
		{"mtime_check", opMtimeCheck},
		{"readdir_many", opReaddirMany},
		{"sparse_write", opSparseWrite},
		{"temp_file", opTempFile},
		{"exclusive_create", opExclusiveCreate},
		{"seek_read_write", opSeekReadWrite},
	}
}

// sharedOps returns additional operations specific to the shared directory.
// these test cross-run / cross-app scenarios.
func sharedOps(runID string) []op {
	return []op{
		{"write_marker", func(dir string) (opResult, error) {
			return opWriteMarker(dir, runID)
		}},
		{"list_existing", opListExisting},
		{"read_cross_run", opReadCrossRun},
	}
}

// runOps executes a list of operations and collects results.
// recovers from panics (e.g. nil stat results when earlier ops fail) so the suite always returns.
func runOps(dir string, ops []op) []TestResult {
	var results []TestResult
	for _, o := range ops {
		tr := TestResult{Name: o.Name}
		start := time.Now()
		func() {
			defer func() {
				if r := recover(); r != nil {
					tr.Pass = false
					tr.Error = fmt.Sprintf("panic: %v", r)
					tr.Duration = time.Since(start).String()
				}
			}()
			res, err := o.Fn(dir)
			tr.Duration = time.Since(start).String()
			if err != nil {
				tr.Pass = false
				tr.Error = err.Error()
			} else {
				tr.Pass = true
			}
			tr.Before = res.Before
			tr.After = res.After
			tr.Context = res.Context
			tr.Details = res.Details
		}()
		results = append(results, tr)
	}
	return results
}

func summarize(results []TestResult) SuiteSummary {
	s := SuiteSummary{Total: len(results)}
	for _, r := range results {
		if r.Pass {
			s.Pass++
		} else {
			s.Fail++
		}
	}
	return s
}

// RunIsolatedSuite creates a unique directory and runs all core ops, then cleans up.
func RunIsolatedSuite(basePath, runID string) SuiteResult {
	dir := filepath.Join(basePath, fmt.Sprintf("test-isolated-%s", runID))
	start := time.Now()

	before := PhaseSnapshot{Timestamp: start.UTC().Format(time.RFC3339)}
	if _, err := os.Stat(dir); err != nil {
		before.DirExists = false
	} else {
		before.DirExists = true
	}

	results := runOps(dir, coreOps())

	os.RemoveAll(dir)

	_, afterErr := os.Stat(dir)
	after := PhaseSnapshot{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		DirExists: afterErr == nil,
	}

	return SuiteResult{
		Dir:      dir,
		Mode:     "isolated",
		Before:   before,
		Tests:    results,
		After:    after,
		Duration: time.Since(start).String(),
		Summary:  summarize(results),
	}
}

// RunSharedSuite runs core ops + shared-specific ops in a persistent shared directory.
// files from previous runs are preserved so cross-run reads work.
func RunSharedSuite(basePath, runID string) SuiteResult {
	sharedDir := filepath.Join(basePath, "shared")
	runDir := filepath.Join(sharedDir, fmt.Sprintf("run-%s", runID))
	start := time.Now()

	// capture existing state before we start
	var existing []string
	before := PhaseSnapshot{Timestamp: start.UTC().Format(time.RFC3339)}
	if entries, err := os.ReadDir(sharedDir); err == nil {
		before.DirExists = true
		before.FileCount = len(entries)
		for _, e := range entries {
			existing = append(existing, e.Name())
			before.Files = append(before.Files, e.Name())
		}
	}

	results := runOps(runDir, coreOps())

	sharedResults := runOps(sharedDir, sharedOps(runID))
	results = append(results, sharedResults...)

	// cleanup only the per-run test artifacts, keep shared marker files
	os.RemoveAll(runDir)

	after := PhaseSnapshot{Timestamp: time.Now().UTC().Format(time.RFC3339)}
	if entries, err := os.ReadDir(sharedDir); err == nil {
		after.DirExists = true
		after.FileCount = len(entries)
		for _, e := range entries {
			after.Files = append(after.Files, e.Name())
		}
	}

	return SuiteResult{
		Dir:           sharedDir,
		Mode:          "shared",
		Before:        before,
		Tests:         results,
		After:         after,
		Duration:      time.Since(start).String(),
		Summary:       summarize(results),
		ExistingFiles: existing,
	}
}

// --- core operations ---

func opCreateFile(dir string) (opResult, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return opResult{Context: "os.WriteFile test.txt on NFS"}, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "test.txt")
	_, statErr := os.Stat(path)
	before := fmt.Sprintf("test.txt exists=%v", statErr == nil)

	if err := os.WriteFile(path, []byte("hello nfs"), 0644); err != nil {
		return opResult{Before: before, Context: "os.WriteFile test.txt on NFS"}, err
	}
	info, _ := os.Stat(path)
	after := fmt.Sprintf("test.txt size=%d mode=%s", info.Size(), info.Mode())
	return opResult{Before: before, After: after, Context: "os.WriteFile test.txt on NFS", Details: fmt.Sprintf("created %s (%d bytes)", filepath.Base(path), info.Size())}, nil
}

func opReadFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")
	info, _ := os.Stat(path)
	before := fmt.Sprintf("test.txt size=%d", info.Size())

	data, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before, Context: "os.ReadFile + content verify"}, err
	}
	if string(data) != "hello nfs" {
		return opResult{Before: before, Context: "os.ReadFile + content verify"}, fmt.Errorf("content mismatch: got %q, want %q", string(data), "hello nfs")
	}
	after := fmt.Sprintf("read %d bytes, match=true", len(data))
	return opResult{Before: before, After: after, Context: "os.ReadFile + content verify", Details: fmt.Sprintf("read %d bytes, content verified", len(data))}, nil
}

func opStatFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")
	info, err := os.Stat(path)
	if err != nil {
		return opResult{Context: "os.Stat metadata check"}, err
	}
	return opResult{
		Context: "os.Stat metadata check",
		Details: fmt.Sprintf("name=%s size=%d mode=%s modtime=%s", info.Name(), info.Size(), info.Mode(), info.ModTime().Format(time.RFC3339)),
	}, nil
}

func opAppendFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")

	beforeData, _ := os.ReadFile(path)
	before := fmt.Sprintf("size=%d content=%q", len(beforeData), string(beforeData))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return opResult{Before: before, Context: "O_APPEND write"}, err
	}
	defer f.Close()
	n, err := f.WriteString("\nappended line")
	if err != nil {
		return opResult{Before: before, Context: "O_APPEND write"}, err
	}

	afterData, _ := os.ReadFile(path)
	after := fmt.Sprintf("size=%d content=%q", len(afterData), string(afterData))

	return opResult{Before: before, After: after, Context: "O_APPEND write", Details: fmt.Sprintf("appended %d bytes", n)}, nil
}

func opOverwriteFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")

	beforeData, _ := os.ReadFile(path)
	before := fmt.Sprintf("content=%q", string(beforeData))

	content := "overwritten content"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return opResult{Before: before, Context: "os.WriteFile overwrite + verify"}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before, Context: "os.WriteFile overwrite + verify"}, fmt.Errorf("read-back failed: %w", err)
	}
	if string(data) != content {
		return opResult{Before: before, Context: "os.WriteFile overwrite + verify"}, fmt.Errorf("content mismatch after overwrite: got %q", string(data))
	}

	return opResult{Before: before, After: fmt.Sprintf("content=%q", string(data)), Context: "os.WriteFile overwrite + verify", Details: "overwritten and verified"}, nil
}

func opChmodFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")

	infoBefore, _ := os.Stat(path)
	before := fmt.Sprintf("mode=%s", infoBefore.Mode())

	if err := os.Chmod(path, 0755); err != nil {
		return opResult{Before: before, Context: "os.Chmod permission change"}, err
	}
	infoAfter, _ := os.Stat(path)
	after := fmt.Sprintf("mode=%s", infoAfter.Mode())

	return opResult{Before: before, After: after, Context: "os.Chmod permission change", Details: fmt.Sprintf("chmod %s -> %s", infoBefore.Mode(), infoAfter.Mode())}, nil
}

func opRenameFile(dir string) (opResult, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "renamed.txt")

	before := fmt.Sprintf("test.txt exists=true, renamed.txt exists=false")

	if err := os.Rename(src, dst); err != nil {
		return opResult{Before: before, Context: "os.Rename within same dir"}, err
	}
	_, srcGone := os.Stat(src)
	_, dstExists := os.Stat(dst)
	after := fmt.Sprintf("test.txt exists=%v, renamed.txt exists=%v", srcGone == nil, dstExists == nil)

	if err := os.Rename(dst, src); err != nil {
		return opResult{Before: before, After: after, Context: "os.Rename within same dir"}, fmt.Errorf("rename-back failed: %w", err)
	}
	return opResult{Before: before, After: after, Context: "os.Rename within same dir", Details: "renamed and renamed back"}, nil
}

func opCopyFile(dir string) (opResult, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "test-copy.txt")

	before := "test-copy.txt exists=false"

	data, err := os.ReadFile(src)
	if err != nil {
		return opResult{Before: before, Context: "read src + write dst copy"}, err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return opResult{Before: before, Context: "read src + write dst copy"}, err
	}
	after := fmt.Sprintf("test-copy.txt exists=true size=%d", len(data))
	return opResult{Before: before, After: after, Context: "read src + write dst copy", Details: fmt.Sprintf("copied %d bytes", len(data))}, nil
}

func opSymlink(dir string) (opResult, error) {
	target := filepath.Join(dir, "test.txt")
	link := filepath.Join(dir, "test-link.txt")

	before := "test-link.txt exists=false"

	if err := os.Symlink(target, link); err != nil {
		return opResult{Before: before, Context: "os.Symlink + os.Readlink"}, err
	}
	resolved, err := os.Readlink(link)
	if err != nil {
		return opResult{Before: before, Context: "os.Symlink + os.Readlink"}, fmt.Errorf("readlink failed: %w", err)
	}
	after := fmt.Sprintf("test-link.txt -> %s", filepath.Base(resolved))
	return opResult{Before: before, After: after, Context: "os.Symlink + os.Readlink", Details: fmt.Sprintf("symlink created and resolved")}, nil
}

func opMkdir(dir string) (opResult, error) {
	path := filepath.Join(dir, "subdir")

	before := "subdir/ exists=false"

	if err := os.Mkdir(path, 0755); err != nil {
		return opResult{Before: before, Context: "os.Mkdir single dir"}, err
	}
	info, _ := os.Stat(path)
	after := fmt.Sprintf("subdir/ exists=true mode=%s", info.Mode())
	return opResult{Before: before, After: after, Context: "os.Mkdir single dir", Details: "created subdir/"}, nil
}

func opNestedMkdir(dir string) (opResult, error) {
	path := filepath.Join(dir, "deep", "nested", "dir")

	before := "deep/ exists=false"

	if err := os.MkdirAll(path, 0755); err != nil {
		return opResult{Before: before, Context: "os.MkdirAll 3-level deep"}, err
	}
	after := "deep/nested/dir/ exists=true"
	return opResult{Before: before, After: after, Context: "os.MkdirAll 3-level deep", Details: "created deep/nested/dir/"}, nil
}

func opCreateInSubdir(dir string) (opResult, error) {
	path := filepath.Join(dir, "subdir", "subfile.txt")

	before := "subdir/subfile.txt exists=false"

	if err := os.WriteFile(path, []byte("subdir content"), 0644); err != nil {
		return opResult{Before: before, Context: "write file inside subdir"}, err
	}
	info, _ := os.Stat(path)
	after := fmt.Sprintf("subdir/subfile.txt size=%d", info.Size())
	return opResult{Before: before, After: after, Context: "write file inside subdir", Details: "created subdir/subfile.txt"}, nil
}

func opCrossDirRename(dir string) (opResult, error) {
	src := filepath.Join(dir, "subdir", "subfile.txt")
	dst := filepath.Join(dir, "deep", "moved.txt")

	before := "subdir/subfile.txt -> (exists)"

	if err := os.Rename(src, dst); err != nil {
		return opResult{Before: before, Context: "os.Rename across directories"}, err
	}
	if _, err := os.Stat(dst); err != nil {
		return opResult{Before: before, Context: "os.Rename across directories"}, fmt.Errorf("file missing after cross-dir rename: %w", err)
	}

	after := "deep/moved.txt -> (exists)"
	return opResult{Before: before, After: after, Context: "os.Rename across directories", Details: "moved subdir/subfile.txt -> deep/moved.txt"}, nil
}

func opDeleteFile(dir string) (opResult, error) {
	targets := []string{"test-copy.txt", "test-link.txt"}

	var existsBefore []string
	for _, name := range targets {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			existsBefore = append(existsBefore, name)
		}
	}
	before := fmt.Sprintf("exist: %s", strings.Join(existsBefore, ", "))

	for _, name := range targets {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return opResult{Before: before, Context: "os.Remove file deletion"}, fmt.Errorf("delete %s: %w", name, err)
		}
	}

	var existsAfter []string
	for _, name := range targets {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			existsAfter = append(existsAfter, name)
		}
	}
	after := "exist: (none)"
	if len(existsAfter) > 0 {
		after = fmt.Sprintf("exist: %s", strings.Join(existsAfter, ", "))
	}

	return opResult{Before: before, After: after, Context: "os.Remove file deletion", Details: "deleted test-copy.txt and test-link.txt"}, nil
}

func opRmdir(dir string) (opResult, error) {
	targets := []string{"deep", "subdir"}

	var existsBefore []string
	for _, d := range targets {
		if _, err := os.Stat(filepath.Join(dir, d)); err == nil {
			existsBefore = append(existsBefore, d+"/")
		}
	}
	before := fmt.Sprintf("dirs: %s", strings.Join(existsBefore, ", "))

	for _, d := range targets {
		path := filepath.Join(dir, d)
		if err := os.RemoveAll(path); err != nil {
			return opResult{Before: before, Context: "os.RemoveAll recursive"}, fmt.Errorf("rmdir %s: %w", d, err)
		}
	}

	var existsAfter []string
	for _, d := range targets {
		if _, err := os.Stat(filepath.Join(dir, d)); err == nil {
			existsAfter = append(existsAfter, d+"/")
		}
	}
	after := "dirs: (none)"
	if len(existsAfter) > 0 {
		after = fmt.Sprintf("dirs: %s", strings.Join(existsAfter, ", "))
	}

	return opResult{Before: before, After: after, Context: "os.RemoveAll recursive", Details: "removed subdir/ and deep/"}, nil
}

func opLargeFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "large.bin")
	data := make([]byte, 1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	before := "large.bin exists=false"

	start := time.Now()
	if err := os.WriteFile(path, data, 0644); err != nil {
		return opResult{Before: before, Context: "1MB write + read-back throughput"}, err
	}
	duration := time.Since(start)
	speed := float64(len(data)) / duration.Seconds() / 1024 / 1024

	readBack, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before, Context: "1MB write + read-back throughput"}, fmt.Errorf("read-back failed: %w", err)
	}
	if len(readBack) != len(data) {
		return opResult{Before: before, Context: "1MB write + read-back throughput"}, fmt.Errorf("size mismatch: wrote %d, read %d", len(data), len(readBack))
	}

	after := fmt.Sprintf("large.bin size=1048576 verified=true")
	os.Remove(path)
	return opResult{Before: before, After: after, Context: "1MB write + read-back throughput", Details: fmt.Sprintf("1MB in %v (%.2f MB/s)", duration, speed)}, nil
}

func opConcurrentWrites(dir string) (opResult, error) {
	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			path := filepath.Join(dir, fmt.Sprintf("concurrent-%d.txt", idx))
			if err := os.WriteFile(path, []byte(fmt.Sprintf("writer %d", idx)), 0644); err != nil {
				errs <- fmt.Errorf("writer %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		return opResult{Context: "5 goroutines writing simultaneously"}, err
	}

	for i := 0; i < n; i++ {
		path := filepath.Join(dir, fmt.Sprintf("concurrent-%d.txt", i))
		data, err := os.ReadFile(path)
		if err != nil {
			return opResult{Context: "5 goroutines writing simultaneously"}, fmt.Errorf("verify writer %d: %w", i, err)
		}
		expected := fmt.Sprintf("writer %d", i)
		if string(data) != expected {
			return opResult{Context: "5 goroutines writing simultaneously"}, fmt.Errorf("writer %d content mismatch: got %q", i, string(data))
		}
		os.Remove(path)
	}

	return opResult{Context: "5 goroutines writing simultaneously", Details: fmt.Sprintf("%d concurrent writes verified", n)}, nil
}

func opFileLock(dir string) (opResult, error) {
	path := filepath.Join(dir, "locktest.txt")
	original := "lock test"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		return opResult{Context: "WriteAt with held fd"}, err
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return opResult{Context: "WriteAt with held fd"}, err
	}
	defer f.Close()

	before := fmt.Sprintf("content=%q", original)

	if _, err := f.WriteAt([]byte("locked"), 0); err != nil {
		return opResult{Before: before, Context: "WriteAt with held fd"}, fmt.Errorf("write-at with open fd: %w", err)
	}

	afterData, _ := os.ReadFile(path)
	after := fmt.Sprintf("content=%q", string(afterData))

	os.Remove(path)
	return opResult{Before: before, After: after, Context: "WriteAt with held fd", Details: "write-at succeeded"}, nil
}

func opTruncateFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "truncate-test.txt")
	original := "this is a long string for truncation"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		return opResult{Context: "os.Truncate shrink file"}, err
	}

	before := fmt.Sprintf("size=%d content=%q", len(original), original)

	if err := os.Truncate(path, 10); err != nil {
		return opResult{Before: before, Context: "os.Truncate shrink file"}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before, Context: "os.Truncate shrink file"}, fmt.Errorf("read after truncate: %w", err)
	}
	os.Remove(path)
	if len(data) != 10 {
		return opResult{Before: before, Context: "os.Truncate shrink file"}, fmt.Errorf("truncate failed: got %d bytes, want 10", len(data))
	}

	after := fmt.Sprintf("size=%d content=%q", len(data), string(data))
	return opResult{Before: before, After: after, Context: "os.Truncate shrink file", Details: fmt.Sprintf("truncated to %d bytes", len(data))}, nil
}

func opHardlink(dir string) (opResult, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "test-hardlink.txt")

	before := "test-hardlink.txt exists=false"

	if err := os.Link(src, dst); err != nil {
		return opResult{Before: before, Context: "os.Link hard link"}, err
	}
	srcData, _ := os.ReadFile(src)
	dstData, _ := os.ReadFile(dst)
	os.Remove(dst)
	if string(srcData) != string(dstData) {
		return opResult{Before: before, Context: "os.Link hard link"}, fmt.Errorf("hardlink content mismatch")
	}
	after := fmt.Sprintf("test-hardlink.txt content matches (%d bytes)", len(srcData))
	return opResult{Before: before, After: after, Context: "os.Link hard link", Details: "hardlink created, content matches"}, nil
}

func opMkfifo(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.fifo")

	before := "test.fifo exists=false"

	if err := syscall.Mkfifo(path, 0644); err != nil {
		return opResult{Before: before, Context: "syscall.Mkfifo named pipe"}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return opResult{Before: before, Context: "syscall.Mkfifo named pipe"}, err
	}
	after := fmt.Sprintf("test.fifo mode=%s", info.Mode())
	os.Remove(path)
	return opResult{Before: before, After: after, Context: "syscall.Mkfifo named pipe", Details: "fifo created"}, nil
}

func opWriteBinary(dir string) (opResult, error) {
	path := filepath.Join(dir, "binary.bin")
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}

	before := "binary.bin exists=false"

	if err := os.WriteFile(path, data, 0644); err != nil {
		return opResult{Before: before, Context: "write all 256 byte values, read back, compare"}, err
	}
	readBack, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before, Context: "write all 256 byte values, read back, compare"}, fmt.Errorf("read-back failed: %w", err)
	}
	if !bytes.Equal(data, readBack) {
		return opResult{Before: before, Context: "write all 256 byte values, read back, compare"}, fmt.Errorf("binary data corruption: wrote %d bytes, read %d bytes", len(data), len(readBack))
	}
	after := "binary.bin size=256 match=true"
	os.Remove(path)
	return opResult{Before: before, After: after, Context: "write all 256 byte values, read back, compare", Details: "256-byte binary round-trip verified"}, nil
}

func opMtimeCheck(dir string) (opResult, error) {
	path := filepath.Join(dir, "mtime-test.txt")
	if err := os.WriteFile(path, []byte("before"), 0644); err != nil {
		return opResult{Context: "verify mtime advances after write"}, err
	}
	info1, _ := os.Stat(path)
	mtime1 := info1.ModTime()
	before := mtime1.Format(time.RFC3339Nano)

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(path, []byte("after modification"), 0644); err != nil {
		return opResult{Before: before, Context: "verify mtime advances after write"}, err
	}
	info2, _ := os.Stat(path)
	mtime2 := info2.ModTime()
	after := mtime2.Format(time.RFC3339Nano)
	os.Remove(path)

	if !mtime2.After(mtime1) {
		return opResult{Before: before, After: after, Context: "verify mtime advances after write"}, fmt.Errorf("mtime did not advance")
	}
	return opResult{Before: before, After: after, Context: "verify mtime advances after write", Details: "mtime advanced"}, nil
}

func opReaddirMany(dir string) (opResult, error) {
	subdir := filepath.Join(dir, "readdir-test")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		return opResult{Context: "create 50 files + os.ReadDir"}, err
	}

	const count = 50
	for i := 0; i < count; i++ {
		path := filepath.Join(subdir, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("file %d", i)), 0644); err != nil {
			return opResult{Context: "create 50 files + os.ReadDir"}, fmt.Errorf("create file %d: %w", i, err)
		}
	}

	before := fmt.Sprintf("readdir-test/ files=%d", count)

	entries, err := os.ReadDir(subdir)
	if err != nil {
		return opResult{Before: before, Context: "create 50 files + os.ReadDir"}, fmt.Errorf("readdir: %w", err)
	}

	after := fmt.Sprintf("readdir returned %d entries", len(entries))
	os.RemoveAll(subdir)

	if len(entries) != count {
		return opResult{Before: before, After: after, Context: "create 50 files + os.ReadDir"}, fmt.Errorf("readdir returned %d entries, want %d", len(entries), count)
	}
	return opResult{Before: before, After: after, Context: "create 50 files + os.ReadDir", Details: fmt.Sprintf("created and listed %d files", count)}, nil
}

func opSparseWrite(dir string) (opResult, error) {
	path := filepath.Join(dir, "sparse.bin")

	before := "sparse.bin exists=false"

	f, err := os.Create(path)
	if err != nil {
		return opResult{Before: before, Context: "seek to 1MB offset, write 16 bytes"}, err
	}

	offset := int64(1024 * 1024)
	if _, err := f.Seek(offset, 0); err != nil {
		f.Close()
		return opResult{Before: before, Context: "seek to 1MB offset, write 16 bytes"}, fmt.Errorf("seek: %w", err)
	}
	payload := []byte("sparse data here")
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return opResult{Before: before, Context: "seek to 1MB offset, write 16 bytes"}, fmt.Errorf("write at offset: %w", err)
	}
	f.Close()

	info, _ := os.Stat(path)
	f2, _ := os.Open(path)
	buf := make([]byte, len(payload))
	f2.ReadAt(buf, offset)
	f2.Close()
	os.Remove(path)

	after := fmt.Sprintf("sparse.bin logical_size=%d", info.Size())

	if string(buf) != string(payload) {
		return opResult{Before: before, After: after, Context: "seek to 1MB offset, write 16 bytes"}, fmt.Errorf("sparse read mismatch: got %q", string(buf))
	}
	return opResult{Before: before, After: after, Context: "seek to 1MB offset, write 16 bytes", Details: fmt.Sprintf("wrote %d bytes at offset %d", len(payload), offset)}, nil
}

func opTempFile(dir string) (opResult, error) {
	// os.CreateTemp creates the file in `dir` (the NFS mount), NOT /tmp
	before := fmt.Sprintf("target dir=%s (NFS mount, not /tmp)", dir)

	f, err := os.CreateTemp(dir, "nfs-test-*.tmp")
	if err != nil {
		return opResult{Before: before, Context: "os.CreateTemp on NFS (not /tmp)"}, err
	}
	name := f.Name()
	if _, err := f.WriteString("temp file content"); err != nil {
		f.Close()
		return opResult{Before: before, Context: "os.CreateTemp on NFS (not /tmp)"}, err
	}
	f.Close()

	data, err := os.ReadFile(name)
	if err != nil {
		return opResult{Before: before, Context: "os.CreateTemp on NFS (not /tmp)"}, fmt.Errorf("read temp file: %w", err)
	}
	after := fmt.Sprintf("created %s (%d bytes)", filepath.Base(name), len(data))
	os.Remove(name)
	return opResult{Before: before, After: after, Context: "os.CreateTemp on NFS (not /tmp)", Details: fmt.Sprintf("temp file %s: %d bytes", filepath.Base(name), len(data))}, nil
}

func opExclusiveCreate(dir string) (opResult, error) {
	path := filepath.Join(dir, "exclusive.txt")
	os.Remove(path)

	before := "exclusive.txt exists=false"

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return opResult{Before: before, Context: "O_CREATE|O_EXCL atomic create"}, err
	}
	f.WriteString("exclusive create")
	f.Close()

	_, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err == nil {
		os.Remove(path)
		return opResult{Before: before, Context: "O_CREATE|O_EXCL atomic create"}, fmt.Errorf("O_EXCL did not reject duplicate create")
	}

	after := "exclusive.txt exists=true, 2nd O_EXCL rejected"
	os.Remove(path)
	return opResult{Before: before, After: after, Context: "O_CREATE|O_EXCL atomic create", Details: "O_EXCL create succeeded, duplicate correctly rejected"}, nil
}

func opSeekReadWrite(dir string) (opResult, error) {
	path := filepath.Join(dir, "seektest.txt")
	original := "AAAAAAAAAA"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		return opResult{Context: "seek to offset 5, overwrite, verify"}, err
	}

	before := fmt.Sprintf("content=%q", original)

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return opResult{Before: before, Context: "seek to offset 5, overwrite, verify"}, err
	}

	if _, err := f.Seek(5, 0); err != nil {
		f.Close()
		return opResult{Before: before, Context: "seek to offset 5, overwrite, verify"}, fmt.Errorf("seek: %w", err)
	}
	if _, err := f.Write([]byte("BBBBB")); err != nil {
		f.Close()
		return opResult{Before: before, Context: "seek to offset 5, overwrite, verify"}, fmt.Errorf("write at offset: %w", err)
	}
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before, Context: "seek to offset 5, overwrite, verify"}, fmt.Errorf("read-back: %w", err)
	}
	os.Remove(path)

	after := fmt.Sprintf("content=%q", string(data))
	expected := "AAAAABBBBB"
	if string(data) != expected {
		return opResult{Before: before, After: after, Context: "seek to offset 5, overwrite, verify"}, fmt.Errorf("seek write mismatch: got %q, want %q", string(data), expected)
	}
	return opResult{Before: before, After: after, Context: "seek to offset 5, overwrite, verify", Details: fmt.Sprintf("seek write verified: %q", string(data))}, nil
}

// --- shared-specific operations ---

func opWriteMarker(dir string, runID string) (opResult, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return opResult{Context: "write marker file for cross-run visibility"}, err
	}
	markerName := fmt.Sprintf("marker-%s.txt", runID)
	path := filepath.Join(dir, markerName)
	content := fmt.Sprintf("run_id=%s\ntimestamp=%s\n", runID, time.Now().UTC().Format(time.RFC3339))

	before := fmt.Sprintf("%s exists=false", markerName)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return opResult{Before: before, Context: "write marker file for cross-run visibility"}, err
	}
	after := fmt.Sprintf("%s exists=true (%d bytes)", markerName, len(content))
	return opResult{Before: before, After: after, Context: "write marker file for cross-run visibility", Details: fmt.Sprintf("wrote marker %s", markerName)}, nil
}

func opListExisting(dir string) (opResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return opResult{Context: "list shared dir entries (all runs)"}, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return opResult{Context: "list shared dir entries (all runs)", Details: fmt.Sprintf("%d entries: %s", len(names), strings.Join(names, ", "))}, nil
}

func opReadCrossRun(dir string) (opResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return opResult{Context: "read markers from previous test runs"}, err
	}

	var markers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "marker-") && strings.HasSuffix(e.Name(), ".txt") {
			markers = append(markers, e.Name())
		}
	}

	if len(markers) == 0 {
		return opResult{Context: "read markers from previous test runs", Details: "no previous markers found (first run)"}, nil
	}

	markerPath := filepath.Join(dir, markers[len(markers)-1])
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return opResult{Context: "read markers from previous test runs"}, fmt.Errorf("read marker %s: %w", markers[len(markers)-1], err)
	}

	return opResult{Context: "read markers from previous test runs", Details: fmt.Sprintf("read marker %s: %s (total markers: %d)", markers[len(markers)-1], strings.TrimSpace(string(data)), len(markers))}, nil
}
