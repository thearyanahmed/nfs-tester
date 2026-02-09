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
func runOps(dir string, ops []op) []TestResult {
	var results []TestResult
	for _, o := range ops {
		tr := TestResult{Name: o.Name}
		start := time.Now()
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
		tr.Details = res.Details
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
		return opResult{}, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello nfs"), 0644); err != nil {
		return opResult{}, err
	}
	info, _ := os.Stat(path)
	return opResult{Details: fmt.Sprintf("created %s (%d bytes)", path, info.Size())}, nil
}

func opReadFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return opResult{}, err
	}
	if string(data) != "hello nfs" {
		return opResult{}, fmt.Errorf("content mismatch: got %q, want %q", string(data), "hello nfs")
	}
	return opResult{Details: fmt.Sprintf("read %d bytes, content verified", len(data))}, nil
}

func opStatFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")
	info, err := os.Stat(path)
	if err != nil {
		return opResult{}, err
	}
	return opResult{Details: fmt.Sprintf("name=%s size=%d mode=%s modtime=%s", info.Name(), info.Size(), info.Mode(), info.ModTime().Format(time.RFC3339))}, nil
}

func opAppendFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")

	beforeData, _ := os.ReadFile(path)
	before := fmt.Sprintf("size=%d content=%q", len(beforeData), string(beforeData))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return opResult{Before: before}, err
	}
	defer f.Close()
	n, err := f.WriteString("\nappended line")
	if err != nil {
		return opResult{Before: before}, err
	}

	afterData, _ := os.ReadFile(path)
	after := fmt.Sprintf("size=%d content=%q", len(afterData), string(afterData))

	return opResult{Before: before, After: after, Details: fmt.Sprintf("appended %d bytes", n)}, nil
}

func opOverwriteFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")

	beforeData, _ := os.ReadFile(path)
	before := fmt.Sprintf("content=%q", string(beforeData))

	content := "overwritten content"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return opResult{Before: before}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before}, fmt.Errorf("read-back failed: %w", err)
	}
	if string(data) != content {
		return opResult{Before: before}, fmt.Errorf("content mismatch after overwrite: got %q", string(data))
	}

	return opResult{Before: before, After: fmt.Sprintf("content=%q", string(data)), Details: "overwritten and verified"}, nil
}

func opChmodFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.txt")

	infoBefore, _ := os.Stat(path)
	before := fmt.Sprintf("mode=%s", infoBefore.Mode())

	if err := os.Chmod(path, 0755); err != nil {
		return opResult{Before: before}, err
	}
	infoAfter, _ := os.Stat(path)
	after := fmt.Sprintf("mode=%s", infoAfter.Mode())

	return opResult{Before: before, After: after, Details: fmt.Sprintf("chmod %s -> %s", infoBefore.Mode(), infoAfter.Mode())}, nil
}

func opRenameFile(dir string) (opResult, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "renamed.txt")

	before := fmt.Sprintf("src=%s exists=true", filepath.Base(src))

	if err := os.Rename(src, dst); err != nil {
		return opResult{Before: before}, err
	}
	_, srcGone := os.Stat(src)
	_, dstExists := os.Stat(dst)
	after := fmt.Sprintf("src exists=%v, dst=%s exists=%v", srcGone == nil, filepath.Base(dst), dstExists == nil)

	if err := os.Rename(dst, src); err != nil {
		return opResult{Before: before, After: after}, fmt.Errorf("rename-back failed: %w", err)
	}
	return opResult{Before: before, After: after, Details: "renamed and renamed back"}, nil
}

func opCopyFile(dir string) (opResult, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "test-copy.txt")
	data, err := os.ReadFile(src)
	if err != nil {
		return opResult{}, err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return opResult{}, err
	}
	return opResult{Details: fmt.Sprintf("copied %d bytes to %s", len(data), dst)}, nil
}

func opSymlink(dir string) (opResult, error) {
	target := filepath.Join(dir, "test.txt")
	link := filepath.Join(dir, "test-link.txt")
	if err := os.Symlink(target, link); err != nil {
		return opResult{}, err
	}
	resolved, err := os.Readlink(link)
	if err != nil {
		return opResult{}, fmt.Errorf("readlink failed: %w", err)
	}
	return opResult{Details: fmt.Sprintf("symlink %s -> %s", link, resolved)}, nil
}

func opMkdir(dir string) (opResult, error) {
	path := filepath.Join(dir, "subdir")
	if err := os.Mkdir(path, 0755); err != nil {
		return opResult{}, err
	}
	return opResult{Details: fmt.Sprintf("created %s", path)}, nil
}

func opNestedMkdir(dir string) (opResult, error) {
	path := filepath.Join(dir, "deep", "nested", "dir")
	if err := os.MkdirAll(path, 0755); err != nil {
		return opResult{}, err
	}
	return opResult{Details: fmt.Sprintf("created %s", path)}, nil
}

func opCreateInSubdir(dir string) (opResult, error) {
	path := filepath.Join(dir, "subdir", "subfile.txt")
	if err := os.WriteFile(path, []byte("subdir content"), 0644); err != nil {
		return opResult{}, err
	}
	return opResult{Details: fmt.Sprintf("created %s", path)}, nil
}

func opCrossDirRename(dir string) (opResult, error) {
	src := filepath.Join(dir, "subdir", "subfile.txt")
	dst := filepath.Join(dir, "deep", "moved.txt")

	before := fmt.Sprintf("file at %s", filepath.Base(src))

	if err := os.Rename(src, dst); err != nil {
		return opResult{Before: before}, err
	}
	if _, err := os.Stat(dst); err != nil {
		return opResult{Before: before}, fmt.Errorf("file missing after cross-dir rename: %w", err)
	}

	after := fmt.Sprintf("file at %s", filepath.Base(dst))
	return opResult{Before: before, After: after, Details: fmt.Sprintf("moved %s -> %s", src, dst)}, nil
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
			return opResult{Before: before}, fmt.Errorf("delete %s: %w", name, err)
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

	return opResult{Before: before, After: after, Details: "deleted test-copy.txt and test-link.txt"}, nil
}

func opRmdir(dir string) (opResult, error) {
	targets := []string{"deep", "subdir"}

	var existsBefore []string
	for _, d := range targets {
		if _, err := os.Stat(filepath.Join(dir, d)); err == nil {
			existsBefore = append(existsBefore, d)
		}
	}
	before := fmt.Sprintf("dirs: %s", strings.Join(existsBefore, ", "))

	for _, d := range targets {
		path := filepath.Join(dir, d)
		if err := os.RemoveAll(path); err != nil {
			return opResult{Before: before}, fmt.Errorf("rmdir %s: %w", d, err)
		}
	}

	var existsAfter []string
	for _, d := range targets {
		if _, err := os.Stat(filepath.Join(dir, d)); err == nil {
			existsAfter = append(existsAfter, d)
		}
	}
	after := "dirs: (none)"
	if len(existsAfter) > 0 {
		after = fmt.Sprintf("dirs: %s", strings.Join(existsAfter, ", "))
	}

	return opResult{Before: before, After: after, Details: "removed subdir and deep directories"}, nil
}

func opLargeFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "large.bin")
	data := make([]byte, 1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	start := time.Now()
	if err := os.WriteFile(path, data, 0644); err != nil {
		return opResult{}, err
	}
	duration := time.Since(start)
	speed := float64(len(data)) / duration.Seconds() / 1024 / 1024

	readBack, err := os.ReadFile(path)
	if err != nil {
		return opResult{}, fmt.Errorf("read-back failed: %w", err)
	}
	if len(readBack) != len(data) {
		return opResult{}, fmt.Errorf("size mismatch: wrote %d, read %d", len(data), len(readBack))
	}

	os.Remove(path)
	return opResult{Details: fmt.Sprintf("1MB write in %v (%.2f MB/s), read-back verified", duration, speed)}, nil
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
		return opResult{}, err
	}

	for i := 0; i < n; i++ {
		path := filepath.Join(dir, fmt.Sprintf("concurrent-%d.txt", i))
		data, err := os.ReadFile(path)
		if err != nil {
			return opResult{}, fmt.Errorf("verify writer %d: %w", i, err)
		}
		expected := fmt.Sprintf("writer %d", i)
		if string(data) != expected {
			return opResult{}, fmt.Errorf("writer %d content mismatch: got %q", i, string(data))
		}
		os.Remove(path)
	}

	return opResult{Details: fmt.Sprintf("%d concurrent writes verified", n)}, nil
}

func opFileLock(dir string) (opResult, error) {
	path := filepath.Join(dir, "locktest.txt")
	original := "lock test"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		return opResult{}, err
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return opResult{}, err
	}
	defer f.Close()

	before := fmt.Sprintf("content=%q", original)

	if _, err := f.WriteAt([]byte("locked"), 0); err != nil {
		return opResult{Before: before}, fmt.Errorf("write-at with open fd: %w", err)
	}

	afterData, _ := os.ReadFile(path)
	after := fmt.Sprintf("content=%q", string(afterData))

	os.Remove(path)
	return opResult{Before: before, After: after, Details: "file lock (write-at with open fd) succeeded"}, nil
}

func opTruncateFile(dir string) (opResult, error) {
	path := filepath.Join(dir, "truncate-test.txt")
	original := "this is a long string for truncation"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		return opResult{}, err
	}

	before := fmt.Sprintf("size=%d content=%q", len(original), original)

	if err := os.Truncate(path, 10); err != nil {
		return opResult{Before: before}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before}, fmt.Errorf("read after truncate: %w", err)
	}
	os.Remove(path)
	if len(data) != 10 {
		return opResult{Before: before}, fmt.Errorf("truncate failed: got %d bytes, want 10", len(data))
	}

	after := fmt.Sprintf("size=%d content=%q", len(data), string(data))
	return opResult{Before: before, After: after, Details: fmt.Sprintf("truncated to %d bytes", len(data))}, nil
}

func opHardlink(dir string) (opResult, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "test-hardlink.txt")
	if err := os.Link(src, dst); err != nil {
		return opResult{}, err
	}
	srcData, _ := os.ReadFile(src)
	dstData, _ := os.ReadFile(dst)
	os.Remove(dst)
	if string(srcData) != string(dstData) {
		return opResult{}, fmt.Errorf("hardlink content mismatch")
	}
	srcInfo, _ := os.Stat(src)
	return opResult{Details: fmt.Sprintf("hardlink created, content matches (%d bytes)", srcInfo.Size())}, nil
}

func opMkfifo(dir string) (opResult, error) {
	path := filepath.Join(dir, "test.fifo")
	if err := syscall.Mkfifo(path, 0644); err != nil {
		return opResult{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return opResult{}, err
	}
	os.Remove(path)
	return opResult{Details: fmt.Sprintf("created fifo %s (mode=%s)", path, info.Mode())}, nil
}

func opWriteBinary(dir string) (opResult, error) {
	path := filepath.Join(dir, "binary.bin")
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return opResult{}, err
	}
	readBack, err := os.ReadFile(path)
	if err != nil {
		return opResult{}, fmt.Errorf("read-back failed: %w", err)
	}
	if !bytes.Equal(data, readBack) {
		return opResult{}, fmt.Errorf("binary data corruption: wrote %d bytes, read %d bytes", len(data), len(readBack))
	}
	os.Remove(path)
	return opResult{Details: "256-byte binary round-trip verified"}, nil
}

func opMtimeCheck(dir string) (opResult, error) {
	path := filepath.Join(dir, "mtime-test.txt")
	if err := os.WriteFile(path, []byte("before"), 0644); err != nil {
		return opResult{}, err
	}
	info1, _ := os.Stat(path)
	mtime1 := info1.ModTime()
	before := mtime1.Format(time.RFC3339Nano)

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(path, []byte("after modification"), 0644); err != nil {
		return opResult{Before: before}, err
	}
	info2, _ := os.Stat(path)
	mtime2 := info2.ModTime()
	after := mtime2.Format(time.RFC3339Nano)
	os.Remove(path)

	if !mtime2.After(mtime1) {
		return opResult{Before: before, After: after}, fmt.Errorf("mtime did not advance: before=%s after=%s", mtime1, mtime2)
	}
	return opResult{Before: before, After: after, Details: fmt.Sprintf("mtime advanced: %s -> %s", before, after)}, nil
}

func opReaddirMany(dir string) (opResult, error) {
	subdir := filepath.Join(dir, "readdir-test")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		return opResult{}, err
	}

	const count = 50
	for i := 0; i < count; i++ {
		path := filepath.Join(subdir, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("file %d", i)), 0644); err != nil {
			return opResult{}, fmt.Errorf("create file %d: %w", i, err)
		}
	}

	entries, err := os.ReadDir(subdir)
	if err != nil {
		return opResult{}, fmt.Errorf("readdir: %w", err)
	}

	os.RemoveAll(subdir)

	if len(entries) != count {
		return opResult{}, fmt.Errorf("readdir returned %d entries, want %d", len(entries), count)
	}
	return opResult{Details: fmt.Sprintf("created and listed %d files", count)}, nil
}

func opSparseWrite(dir string) (opResult, error) {
	path := filepath.Join(dir, "sparse.bin")
	f, err := os.Create(path)
	if err != nil {
		return opResult{}, err
	}

	offset := int64(1024 * 1024)
	if _, err := f.Seek(offset, 0); err != nil {
		f.Close()
		return opResult{}, fmt.Errorf("seek: %w", err)
	}
	payload := []byte("sparse data here")
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return opResult{}, fmt.Errorf("write at offset: %w", err)
	}
	f.Close()

	info, _ := os.Stat(path)
	f2, _ := os.Open(path)
	buf := make([]byte, len(payload))
	f2.ReadAt(buf, offset)
	f2.Close()
	os.Remove(path)

	if string(buf) != string(payload) {
		return opResult{}, fmt.Errorf("sparse read mismatch: got %q", string(buf))
	}
	return opResult{Details: fmt.Sprintf("sparse file: logical size=%d, wrote %d bytes at offset %d", info.Size(), len(payload), offset)}, nil
}

func opTempFile(dir string) (opResult, error) {
	f, err := os.CreateTemp(dir, "nfs-test-*.tmp")
	if err != nil {
		return opResult{}, err
	}
	name := f.Name()
	if _, err := f.WriteString("temp file content"); err != nil {
		f.Close()
		return opResult{}, err
	}
	f.Close()

	data, err := os.ReadFile(name)
	if err != nil {
		return opResult{}, fmt.Errorf("read temp file: %w", err)
	}
	os.Remove(name)
	return opResult{Details: fmt.Sprintf("temp file %s: %d bytes", filepath.Base(name), len(data))}, nil
}

func opExclusiveCreate(dir string) (opResult, error) {
	path := filepath.Join(dir, "exclusive.txt")
	os.Remove(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return opResult{}, err
	}
	f.WriteString("exclusive create")
	f.Close()

	_, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err == nil {
		os.Remove(path)
		return opResult{}, fmt.Errorf("O_EXCL did not reject duplicate create")
	}

	os.Remove(path)
	return opResult{Details: "O_EXCL create succeeded, duplicate correctly rejected"}, nil
}

func opSeekReadWrite(dir string) (opResult, error) {
	path := filepath.Join(dir, "seektest.txt")
	original := "AAAAAAAAAA"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		return opResult{}, err
	}

	before := fmt.Sprintf("content=%q", original)

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return opResult{Before: before}, err
	}

	if _, err := f.Seek(5, 0); err != nil {
		f.Close()
		return opResult{Before: before}, fmt.Errorf("seek: %w", err)
	}
	if _, err := f.Write([]byte("BBBBB")); err != nil {
		f.Close()
		return opResult{Before: before}, fmt.Errorf("write at offset: %w", err)
	}
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		return opResult{Before: before}, fmt.Errorf("read-back: %w", err)
	}
	os.Remove(path)

	after := fmt.Sprintf("content=%q", string(data))
	expected := "AAAAABBBBB"
	if string(data) != expected {
		return opResult{Before: before, After: after}, fmt.Errorf("seek write mismatch: got %q, want %q", string(data), expected)
	}
	return opResult{Before: before, After: after, Details: fmt.Sprintf("seek write verified: %q", string(data))}, nil
}

// --- shared-specific operations ---

func opWriteMarker(dir string, runID string) (opResult, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return opResult{}, err
	}
	path := filepath.Join(dir, fmt.Sprintf("marker-%s.txt", runID))
	content := fmt.Sprintf("run_id=%s\ntimestamp=%s\n", runID, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return opResult{}, err
	}
	return opResult{Details: fmt.Sprintf("wrote marker %s", path)}, nil
}

func opListExisting(dir string) (opResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return opResult{}, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return opResult{Details: fmt.Sprintf("%d entries: %s", len(names), strings.Join(names, ", "))}, nil
}

func opReadCrossRun(dir string) (opResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return opResult{}, err
	}

	var markers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "marker-") && strings.HasSuffix(e.Name(), ".txt") {
			markers = append(markers, e.Name())
		}
	}

	if len(markers) == 0 {
		return opResult{Details: "no previous markers found (first run)"}, nil
	}

	markerPath := filepath.Join(dir, markers[len(markers)-1])
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return opResult{}, fmt.Errorf("read marker %s: %w", markers[len(markers)-1], err)
	}

	return opResult{Details: fmt.Sprintf("read marker %s: %s (total markers: %d)", markers[len(markers)-1], strings.TrimSpace(string(data)), len(markers))}, nil
}
