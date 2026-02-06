package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type TestResult struct {
	Name    string `json:"name"`
	Pass    bool   `json:"pass"`
	Error   string `json:"error,omitempty"`
	Details string `json:"details,omitempty"`
}

type MatrixResult struct {
	Timestamp   string            `json:"timestamp"`
	User        string            `json:"user"`
	UID         string            `json:"uid"`
	GID         string            `json:"gid"`
	MountPath   string            `json:"mount_path"`
	MountInfo   string            `json:"mount_info"`
	Tests       []TestResult      `json:"tests"`
	Summary     map[string]int    `json:"summary"`
}

type ExecRequest struct {
	Cmd string `json:"cmd"`
	Cwd string `json:"cwd"`
}

type ExecResponse struct {
	Success  bool   `json:"success"`
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Cwd      string `json:"cwd"`
}

var (
	nfsPath    = getEnv("NFS_PATH", "/mnt/nfs")
	listenAddr = getEnv("LISTEN_ADDR", ":8080")
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	log.Printf("nfs-tester starting on %s", listenAddr)
	log.Printf("NFS path: %s", nfsPath)

	// print user info
	u, _ := user.Current()
	log.Printf("Running as: %s (uid=%s, gid=%s)", u.Username, u.Uid, u.Gid)

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/api/v1/info", handleInfo)
	http.HandleFunc("/api/v1/matrix", handleMatrix)
	http.HandleFunc("/api/v1/exec", handleExec)

	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	u, _ := user.Current()
	info := map[string]interface{}{
		"name":    "nfs-tester",
		"version": "1.0.0",
		"user":    u.Username,
		"uid":     u.Uid,
		"gid":     u.Gid,
		"nfs_path": nfsPath,
		"endpoints": []map[string]string{
			{"method": "GET", "path": "/", "description": "This info"},
			{"method": "GET", "path": "/health", "description": "Health check"},
			{"method": "GET", "path": "/api/v1/info", "description": "System and mount info"},
			{"method": "GET", "path": "/api/v1/matrix", "description": "Run full NFS test matrix"},
			{"method": "GET", "path": "/api/v1/exec?cmd=<cmd>&cwd=<path>", "description": "Execute shell command"},
		},
	}
	writeJSON(w, info)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	u, _ := user.Current()

	// get mount info
	mountInfo := ""
	if out, err := exec.Command("mount").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, nfsPath) || strings.Contains(line, "nfs") {
				mountInfo += line + "\n"
			}
		}
	}

	// get directory listing
	dirListing := ""
	if entries, err := os.ReadDir(nfsPath); err == nil {
		for _, e := range entries {
			info, _ := e.Info()
			dirListing += fmt.Sprintf("%s %d %s\n", info.Mode(), info.Size(), e.Name())
		}
	}

	info := map[string]interface{}{
		"user":        u.Username,
		"uid":         u.Uid,
		"gid":         u.Gid,
		"nfs_path":    nfsPath,
		"mount_info":  strings.TrimSpace(mountInfo),
		"dir_listing": strings.TrimSpace(dirListing),
	}
	writeJSON(w, info)
}

func handleMatrix(w http.ResponseWriter, r *http.Request) {
	u, _ := user.Current()

	// get mount info
	mountInfo := ""
	if out, err := exec.Command("mount").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, nfsPath) {
				mountInfo = line
				break
			}
		}
	}

	result := MatrixResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		User:      u.Username,
		UID:       u.Uid,
		GID:       u.Gid,
		MountPath: nfsPath,
		MountInfo: mountInfo,
		Tests:     []TestResult{},
		Summary:   map[string]int{"pass": 0, "fail": 0},
	}

	testDir := filepath.Join(nfsPath, fmt.Sprintf("test-matrix-%d", time.Now().Unix()))

	// run all tests
	tests := []struct {
		name string
		fn   func(string) (string, error)
	}{
		{"create_file", testCreateFile},
		{"read_file", testReadFile},
		{"append_file", testAppendFile},
		{"overwrite_file", testOverwriteFile},
		{"mkdir", testMkdir},
		{"create_in_subdir", testCreateInSubdir},
		{"chmod", testChmod},
		{"rename", testRename},
		{"copy", testCopy},
		{"delete_file", testDeleteFile},
		{"rmdir", testRmdir},
		{"large_file_1mb", testLargeFile},
		{"concurrent_writes", testConcurrentWrites},
	}

	for _, t := range tests {
		tr := TestResult{Name: t.name}
		details, err := t.fn(testDir)
		if err != nil {
			tr.Pass = false
			tr.Error = err.Error()
			result.Summary["fail"]++
		} else {
			tr.Pass = true
			result.Summary["pass"]++
		}
		tr.Details = details
		result.Tests = append(result.Tests, tr)
	}

	// cleanup
	os.RemoveAll(testDir)

	writeJSON(w, result)
}

func handleExec(w http.ResponseWriter, r *http.Request) {
	cmd := r.URL.Query().Get("cmd")
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		// fall back to / if nfs mount doesn't exist
		if _, err := os.Stat(nfsPath); err == nil {
			cwd = nfsPath
		} else {
			cwd = "/"
		}
	}

	if cmd == "" {
		http.Error(w, "cmd parameter required", http.StatusBadRequest)
		return
	}

	resp := ExecResponse{
		Command: cmd,
		Cwd:     cwd,
	}

	c := exec.Command("sh", "-c", cmd)
	c.Dir = cwd

	stdout, _ := c.StdoutPipe()
	stderr, _ := c.StderrPipe()

	if err := c.Start(); err != nil {
		resp.Success = false
		resp.Stderr = err.Error()
		resp.ExitCode = 1
		writeJSON(w, resp)
		return
	}

	stdoutBytes, _ := io.ReadAll(stdout)
	stderrBytes, _ := io.ReadAll(stderr)

	err := c.Wait()
	resp.Stdout = string(stdoutBytes)
	resp.Stderr = string(stderrBytes)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			resp.ExitCode = 1
		}
		resp.Success = false
	} else {
		resp.ExitCode = 0
		resp.Success = true
	}

	writeJSON(w, resp)
}

// test functions

func testCreateFile(testDir string) (string, error) {
	if err := os.MkdirAll(testDir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(testDir, "test.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		return "", err
	}
	info, _ := os.Stat(path)
	return fmt.Sprintf("created %s, size=%d", path, info.Size()), nil
}

func testReadFile(testDir string) (string, error) {
	path := filepath.Join(testDir, "test.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("read %d bytes: %s", len(data), string(data)), nil
}

func testAppendFile(testDir string) (string, error) {
	path := filepath.Join(testDir, "test.txt")
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

func testOverwriteFile(testDir string) (string, error) {
	path := filepath.Join(testDir, "test.txt")
	if err := os.WriteFile(path, []byte("overwritten content"), 0644); err != nil {
		return "", err
	}
	return "file overwritten", nil
}

func testMkdir(testDir string) (string, error) {
	path := filepath.Join(testDir, "subdir")
	if err := os.Mkdir(path, 0755); err != nil {
		return "", err
	}
	return fmt.Sprintf("created directory %s", path), nil
}

func testCreateInSubdir(testDir string) (string, error) {
	path := filepath.Join(testDir, "subdir", "subfile.txt")
	if err := os.WriteFile(path, []byte("subdir content"), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("created %s", path), nil
}

func testChmod(testDir string) (string, error) {
	path := filepath.Join(testDir, "subdir", "subfile.txt")
	if err := os.Chmod(path, 0755); err != nil {
		return "", err
	}
	info, _ := os.Stat(path)
	return fmt.Sprintf("chmod to %s", info.Mode()), nil
}

func testRename(testDir string) (string, error) {
	oldPath := filepath.Join(testDir, "subdir", "subfile.txt")
	newPath := filepath.Join(testDir, "subdir", "renamed.txt")
	if err := os.Rename(oldPath, newPath); err != nil {
		return "", err
	}
	return fmt.Sprintf("renamed to %s", newPath), nil
}

func testCopy(testDir string) (string, error) {
	src := filepath.Join(testDir, "test.txt")
	dst := filepath.Join(testDir, "test-copy.txt")

	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("copied to %s", dst), nil
}

func testDeleteFile(testDir string) (string, error) {
	path := filepath.Join(testDir, "test-copy.txt")
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted %s", path), nil
}

func testRmdir(testDir string) (string, error) {
	// first remove files in subdir
	subdir := filepath.Join(testDir, "subdir")
	entries, _ := os.ReadDir(subdir)
	for _, e := range entries {
		os.Remove(filepath.Join(subdir, e.Name()))
	}
	if err := os.Remove(subdir); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed directory %s", subdir), nil
}

func testLargeFile(testDir string) (string, error) {
	path := filepath.Join(testDir, "large.bin")
	data := make([]byte, 1024*1024) // 1MB
	for i := range data {
		data[i] = byte(i % 256)
	}

	start := time.Now()
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	duration := time.Since(start)
	speed := float64(len(data)) / duration.Seconds() / 1024 / 1024

	os.Remove(path)
	return fmt.Sprintf("wrote 1MB in %v (%.2f MB/s)", duration, speed), nil
}

func testConcurrentWrites(testDir string) (string, error) {
	var wg sync.WaitGroup
	errors := make(chan error, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			path := filepath.Join(testDir, fmt.Sprintf("concurrent-%d.txt", n))
			if err := os.WriteFile(path, []byte(fmt.Sprintf("concurrent write %d", n)), 0644); err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		return "", err
	}

	// cleanup
	for i := 0; i < 5; i++ {
		os.Remove(filepath.Join(testDir, fmt.Sprintf("concurrent-%d.txt", i)))
	}

	return "5 concurrent writes successful", nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
