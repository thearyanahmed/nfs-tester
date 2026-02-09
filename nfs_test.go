package main

import (
	"os"
	"path/filepath"
	"testing"
)

func testBasePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("NFS_PATH"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("NFS_PATH=%s not accessible: %v", p, err)
		}
		return p
	}
	return t.TempDir()
}

func TestIsolatedSuite(t *testing.T) {
	base := testBasePath(t)
	runID := "test-unit"
	dir := filepath.Join(base, "test-isolated-"+runID)
	t.Cleanup(func() { os.RemoveAll(dir) })

	for _, op := range coreOps() {
		t.Run(op.Name, func(t *testing.T) {
			res, err := op.Fn(dir)
			if err != nil {
				t.Fatalf("%s failed: %v", op.Name, err)
			}
			if res.Context != "" {
				t.Logf("context: %s", res.Context)
			}
			if res.Before != "" {
				t.Logf("before: %s", res.Before)
			}
			if res.After != "" {
				t.Logf("after:  %s", res.After)
			}
			t.Logf("details: %s", res.Details)
		})
	}
}

func TestSharedSuite(t *testing.T) {
	base := testBasePath(t)
	sharedDir := filepath.Join(base, "shared")
	runID := "test-unit-shared"
	runDir := filepath.Join(sharedDir, "run-"+runID)
	t.Cleanup(func() { os.RemoveAll(runDir) })

	for _, op := range coreOps() {
		t.Run("core/"+op.Name, func(t *testing.T) {
			res, err := op.Fn(runDir)
			if err != nil {
				t.Fatalf("%s failed: %v", op.Name, err)
			}
			if res.Context != "" {
				t.Logf("context: %s", res.Context)
			}
			if res.Before != "" {
				t.Logf("before: %s", res.Before)
			}
			if res.After != "" {
				t.Logf("after:  %s", res.After)
			}
			t.Logf("details: %s", res.Details)
		})
	}

	for _, op := range sharedOps(runID) {
		t.Run("shared/"+op.Name, func(t *testing.T) {
			res, err := op.Fn(sharedDir)
			if err != nil {
				t.Fatalf("%s failed: %v", op.Name, err)
			}
			if res.Context != "" {
				t.Logf("context: %s", res.Context)
			}
			t.Logf("details: %s", res.Details)
		})
	}
}

func TestIndividualOps(t *testing.T) {
	independent := map[string]bool{
		"create_file":       true,
		"mkdir":             true,
		"nested_mkdir":      true,
		"large_file_1mb":    true,
		"concurrent_writes": true,
		"file_lock":         true,
		"truncate_file":     true,
		"mkfifo":            true,
		"write_binary":      true,
		"mtime_check":       true,
		"readdir_many":      true,
		"sparse_write":      true,
		"temp_file":         true,
		"exclusive_create":  true,
		"seek_read_write":   true,
	}

	for _, op := range coreOps() {
		if !independent[op.Name] {
			continue
		}
		t.Run(op.Name, func(t *testing.T) {
			dir := t.TempDir()
			res, err := op.Fn(dir)
			if err != nil {
				t.Fatalf("%s failed: %v", op.Name, err)
			}
			if res.Context != "" {
				t.Logf("context: %s", res.Context)
			}
			if res.Before != "" {
				t.Logf("before: %s", res.Before)
			}
			if res.After != "" {
				t.Logf("after:  %s", res.After)
			}
			t.Logf("details: %s", res.Details)
		})
	}
}
