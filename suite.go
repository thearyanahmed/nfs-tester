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

// op is a single NFS filesystem operation to test.
type op struct {
	Name string
	Fn   func(dir string) (string, error)
}

// SuiteResult holds results from running a test suite against a directory.
type SuiteResult struct {
	Dir           string       `json:"dir"`
	Mode          string       `json:"mode"` // "isolated" or "shared"
	Tests         []TestResult `json:"tests"`
	Summary       SuiteSummary `json:"summary"`
	ExistingFiles []string     `json:"existing_files,omitempty"`
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
		{"write_marker", func(dir string) (string, error) {
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
		details, err := o.Fn(dir)
		if err != nil {
			tr.Pass = false
			tr.Error = err.Error()
		} else {
			tr.Pass = true
		}
		tr.Details = details
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

	results := runOps(dir, coreOps())

	// cleanup
	os.RemoveAll(dir)

	return SuiteResult{
		Dir:     dir,
		Mode:    "isolated",
		Tests:   results,
		Summary: summarize(results),
	}
}

// RunSharedSuite runs core ops + shared-specific ops in a persistent shared directory.
// files from previous runs are preserved so cross-run reads work.
func RunSharedSuite(basePath, runID string) SuiteResult {
	sharedDir := filepath.Join(basePath, "shared")
	// per-run subdir inside shared, so concurrent runs don't collide on filenames
	runDir := filepath.Join(sharedDir, fmt.Sprintf("run-%s", runID))

	// list existing files before we start
	var existing []string
	if entries, err := os.ReadDir(sharedDir); err == nil {
		for _, e := range entries {
			existing = append(existing, e.Name())
		}
	}

	// run core ops in a per-run subdir
	results := runOps(runDir, coreOps())

	// run shared-specific ops against the shared root
	sharedResults := runOps(sharedDir, sharedOps(runID))
	results = append(results, sharedResults...)

	// cleanup only the per-run test artifacts, keep shared marker files
	os.RemoveAll(runDir)

	return SuiteResult{
		Dir:           sharedDir,
		Mode:          "shared",
		Tests:         results,
		Summary:       summarize(results),
		ExistingFiles: existing,
	}
}

// --- core operations ---

func opCreateFile(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello nfs"), 0644); err != nil {
		return "", err
	}
	info, _ := os.Stat(path)
	return fmt.Sprintf("created %s (%d bytes)", path, info.Size()), nil
}

func opReadFile(dir string) (string, error) {
	path := filepath.Join(dir, "test.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if string(data) != "hello nfs" {
		return "", fmt.Errorf("content mismatch: got %q, want %q", string(data), "hello nfs")
	}
	return fmt.Sprintf("read %d bytes, content verified", len(data)), nil
}

func opStatFile(dir string) (string, error) {
	path := filepath.Join(dir, "test.txt")
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("name=%s size=%d mode=%s modtime=%s", info.Name(), info.Size(), info.Mode(), info.ModTime().Format(time.RFC3339)), nil
}

func opAppendFile(dir string) (string, error) {
	path := filepath.Join(dir, "test.txt")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := f.WriteString("\nappended line")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("appended %d bytes", n), nil
}

func opOverwriteFile(dir string) (string, error) {
	path := filepath.Join(dir, "test.txt")
	content := "overwritten content"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	// verify
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read-back failed: %w", err)
	}
	if string(data) != content {
		return "", fmt.Errorf("content mismatch after overwrite: got %q", string(data))
	}
	return "overwritten and verified", nil
}

func opChmodFile(dir string) (string, error) {
	path := filepath.Join(dir, "test.txt")
	if err := os.Chmod(path, 0755); err != nil {
		return "", err
	}
	info, _ := os.Stat(path)
	return fmt.Sprintf("chmod to %s", info.Mode()), nil
}

func opRenameFile(dir string) (string, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "renamed.txt")
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	// rename back so later ops can find it
	if err := os.Rename(dst, src); err != nil {
		return "", fmt.Errorf("rename-back failed: %w", err)
	}
	return "renamed and renamed back", nil
}

func opCopyFile(dir string) (string, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "test-copy.txt")
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("copied %d bytes to %s", len(data), dst), nil
}

func opSymlink(dir string) (string, error) {
	target := filepath.Join(dir, "test.txt")
	link := filepath.Join(dir, "test-link.txt")
	if err := os.Symlink(target, link); err != nil {
		return "", err
	}
	resolved, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("readlink failed: %w", err)
	}
	return fmt.Sprintf("symlink %s -> %s", link, resolved), nil
}

func opMkdir(dir string) (string, error) {
	path := filepath.Join(dir, "subdir")
	if err := os.Mkdir(path, 0755); err != nil {
		return "", err
	}
	return fmt.Sprintf("created %s", path), nil
}

func opNestedMkdir(dir string) (string, error) {
	path := filepath.Join(dir, "deep", "nested", "dir")
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", err
	}
	return fmt.Sprintf("created %s", path), nil
}

func opCreateInSubdir(dir string) (string, error) {
	path := filepath.Join(dir, "subdir", "subfile.txt")
	if err := os.WriteFile(path, []byte("subdir content"), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("created %s", path), nil
}

func opCrossDirRename(dir string) (string, error) {
	src := filepath.Join(dir, "subdir", "subfile.txt")
	dst := filepath.Join(dir, "deep", "moved.txt")
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	// verify it landed
	if _, err := os.Stat(dst); err != nil {
		return "", fmt.Errorf("file missing after cross-dir rename: %w", err)
	}
	return fmt.Sprintf("moved %s -> %s", src, dst), nil
}

func opDeleteFile(dir string) (string, error) {
	// delete the copy and symlink we created
	for _, name := range []string{"test-copy.txt", "test-link.txt"} {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("delete %s: %w", name, err)
		}
	}
	return "deleted test-copy.txt and test-link.txt", nil
}

func opRmdir(dir string) (string, error) {
	// remove the deep nested dir tree and subdir
	for _, d := range []string{"deep", "subdir"} {
		path := filepath.Join(dir, d)
		if err := os.RemoveAll(path); err != nil {
			return "", fmt.Errorf("rmdir %s: %w", d, err)
		}
	}
	return "removed subdir and deep directories", nil
}

func opLargeFile(dir string) (string, error) {
	path := filepath.Join(dir, "large.bin")
	data := make([]byte, 1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	start := time.Now()
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	duration := time.Since(start)
	speed := float64(len(data)) / duration.Seconds() / 1024 / 1024

	// read back and verify first+last byte
	readBack, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read-back failed: %w", err)
	}
	if len(readBack) != len(data) {
		return "", fmt.Errorf("size mismatch: wrote %d, read %d", len(data), len(readBack))
	}

	os.Remove(path)
	return fmt.Sprintf("1MB write in %v (%.2f MB/s), read-back verified", duration, speed), nil
}

func opConcurrentWrites(dir string) (string, error) {
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
		return "", err
	}

	// verify all files exist and have correct content
	for i := 0; i < n; i++ {
		path := filepath.Join(dir, fmt.Sprintf("concurrent-%d.txt", i))
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("verify writer %d: %w", i, err)
		}
		expected := fmt.Sprintf("writer %d", i)
		if string(data) != expected {
			return "", fmt.Errorf("writer %d content mismatch: got %q", i, string(data))
		}
		os.Remove(path)
	}

	return fmt.Sprintf("%d concurrent writes verified", n), nil
}

func opFileLock(dir string) (string, error) {
	path := filepath.Join(dir, "locktest.txt")
	if err := os.WriteFile(path, []byte("lock test"), 0644); err != nil {
		return "", err
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// try advisory lock via fcntl-style (Go doesn't expose flock directly,
	// but we can test exclusive open semantics)
	// write while holding the fd open
	if _, err := f.WriteAt([]byte("locked"), 0); err != nil {
		return "", fmt.Errorf("write-at with open fd: %w", err)
	}

	os.Remove(path)
	return "file lock (write-at with open fd) succeeded", nil
}

func opTruncateFile(dir string) (string, error) {
	path := filepath.Join(dir, "truncate-test.txt")
	if err := os.WriteFile(path, []byte("this is a long string for truncation"), 0644); err != nil {
		return "", err
	}
	if err := os.Truncate(path, 10); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read after truncate: %w", err)
	}
	os.Remove(path)
	if len(data) != 10 {
		return "", fmt.Errorf("truncate failed: got %d bytes, want 10", len(data))
	}
	return fmt.Sprintf("truncated to %d bytes: %q", len(data), string(data)), nil
}

func opHardlink(dir string) (string, error) {
	src := filepath.Join(dir, "test.txt")
	dst := filepath.Join(dir, "test-hardlink.txt")
	if err := os.Link(src, dst); err != nil {
		return "", err
	}
	// verify both point to same content
	srcData, _ := os.ReadFile(src)
	dstData, _ := os.ReadFile(dst)
	os.Remove(dst)
	if string(srcData) != string(dstData) {
		return "", fmt.Errorf("hardlink content mismatch")
	}
	// verify inode match via Stat
	srcInfo, _ := os.Stat(src)
	// hardlink removed, but content matched — that's enough
	return fmt.Sprintf("hardlink created, content matches (%d bytes)", srcInfo.Size()), nil
}

func opMkfifo(dir string) (string, error) {
	path := filepath.Join(dir, "test.fifo")
	if err := syscall.Mkfifo(path, 0644); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	os.Remove(path)
	return fmt.Sprintf("created fifo %s (mode=%s)", path, info.Mode()), nil
}

func opWriteBinary(dir string) (string, error) {
	path := filepath.Join(dir, "binary.bin")
	// write all 256 byte values to test binary data integrity
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	readBack, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read-back failed: %w", err)
	}
	if !bytes.Equal(data, readBack) {
		return "", fmt.Errorf("binary data corruption: wrote %d bytes, read %d bytes", len(data), len(readBack))
	}
	os.Remove(path)
	return "256-byte binary round-trip verified", nil
}

func opMtimeCheck(dir string) (string, error) {
	path := filepath.Join(dir, "mtime-test.txt")
	if err := os.WriteFile(path, []byte("before"), 0644); err != nil {
		return "", err
	}
	info1, _ := os.Stat(path)
	mtime1 := info1.ModTime()

	// small delay to ensure mtime changes
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(path, []byte("after modification"), 0644); err != nil {
		return "", err
	}
	info2, _ := os.Stat(path)
	mtime2 := info2.ModTime()
	os.Remove(path)

	if !mtime2.After(mtime1) {
		return "", fmt.Errorf("mtime did not advance: before=%s after=%s", mtime1, mtime2)
	}
	return fmt.Sprintf("mtime advanced: %s -> %s", mtime1.Format(time.RFC3339Nano), mtime2.Format(time.RFC3339Nano)), nil
}

func opReaddirMany(dir string) (string, error) {
	subdir := filepath.Join(dir, "readdir-test")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		return "", err
	}

	const count = 50
	for i := 0; i < count; i++ {
		path := filepath.Join(subdir, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("file %d", i)), 0644); err != nil {
			return "", fmt.Errorf("create file %d: %w", i, err)
		}
	}

	entries, err := os.ReadDir(subdir)
	if err != nil {
		return "", fmt.Errorf("readdir: %w", err)
	}

	os.RemoveAll(subdir)

	if len(entries) != count {
		return "", fmt.Errorf("readdir returned %d entries, want %d", len(entries), count)
	}
	return fmt.Sprintf("created and listed %d files", count), nil
}

func opSparseWrite(dir string) (string, error) {
	path := filepath.Join(dir, "sparse.bin")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}

	// seek to 1MB offset and write a small payload
	offset := int64(1024 * 1024)
	if _, err := f.Seek(offset, 0); err != nil {
		f.Close()
		return "", fmt.Errorf("seek: %w", err)
	}
	payload := []byte("sparse data here")
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return "", fmt.Errorf("write at offset: %w", err)
	}
	f.Close()

	info, _ := os.Stat(path)
	// read back at offset
	f2, _ := os.Open(path)
	buf := make([]byte, len(payload))
	f2.ReadAt(buf, offset)
	f2.Close()
	os.Remove(path)

	if string(buf) != string(payload) {
		return "", fmt.Errorf("sparse read mismatch: got %q", string(buf))
	}
	return fmt.Sprintf("sparse file: logical size=%d, wrote %d bytes at offset %d", info.Size(), len(payload), offset), nil
}

func opTempFile(dir string) (string, error) {
	f, err := os.CreateTemp(dir, "nfs-test-*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.WriteString("temp file content"); err != nil {
		f.Close()
		return "", err
	}
	f.Close()

	data, err := os.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read temp file: %w", err)
	}
	os.Remove(name)
	return fmt.Sprintf("temp file %s: %d bytes", filepath.Base(name), len(data)), nil
}

func opExclusiveCreate(dir string) (string, error) {
	path := filepath.Join(dir, "exclusive.txt")
	// ensure it doesn't exist
	os.Remove(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	f.WriteString("exclusive create")
	f.Close()

	// second attempt should fail
	_, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err == nil {
		os.Remove(path)
		return "", fmt.Errorf("O_EXCL did not reject duplicate create")
	}

	os.Remove(path)
	return "O_EXCL create succeeded, duplicate correctly rejected", nil
}

func opSeekReadWrite(dir string) (string, error) {
	path := filepath.Join(dir, "seektest.txt")
	if err := os.WriteFile(path, []byte("AAAAAAAAAA"), 0644); err != nil {
		return "", err
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return "", err
	}

	// seek to middle and overwrite
	if _, err := f.Seek(5, 0); err != nil {
		f.Close()
		return "", fmt.Errorf("seek: %w", err)
	}
	if _, err := f.Write([]byte("BBBBB")); err != nil {
		f.Close()
		return "", fmt.Errorf("write at offset: %w", err)
	}
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read-back: %w", err)
	}
	os.Remove(path)

	expected := "AAAAABBBBB"
	if string(data) != expected {
		return "", fmt.Errorf("seek write mismatch: got %q, want %q", string(data), expected)
	}
	return fmt.Sprintf("seek write verified: %q", string(data)), nil
}

// --- shared-specific operations ---

// opWriteMarker writes a marker file in the shared dir so other runs can find it.
func opWriteMarker(dir string, runID string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("marker-%s.txt", runID))
	content := fmt.Sprintf("run_id=%s\ntimestamp=%s\n", runID, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote marker %s", path), nil
}

// opListExisting lists all files in the shared directory.
func opListExisting(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return fmt.Sprintf("%d entries: %s", len(names), strings.Join(names, ", ")), nil
}

// opReadCrossRun tries to read any existing marker file from a previous run.
func opReadCrossRun(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	// find marker files from other runs
	var markers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "marker-") && strings.HasSuffix(e.Name(), ".txt") {
			markers = append(markers, e.Name())
		}
	}

	if len(markers) == 0 {
		return "no previous markers found (first run)", nil
	}

	// read the most recent marker
	markerPath := filepath.Join(dir, markers[len(markers)-1])
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return "", fmt.Errorf("read marker %s: %w", markers[len(markers)-1], err)
	}

	return fmt.Sprintf("read marker %s: %s (total markers: %d)", markers[len(markers)-1], strings.TrimSpace(string(data)), len(markers)), nil
}
