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
	"strings"
	"time"
)

type TestResult struct {
	Name     string `json:"name"`
	Pass     bool   `json:"pass"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
	Error    string `json:"error,omitempty"`
	Details  string `json:"details,omitempty"`
	Duration string `json:"duration"`
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
	http.HandleFunc("/api/v1/test-suite", handleTestSuite)

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
			{"method": "GET", "path": "/api/v1/test-suite", "description": "Run full NFS test suite (isolated + shared)"},
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

	mountInfo := ""
	if out, err := exec.Command("mount").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, nfsPath) {
				mountInfo = line
				break
			}
		}
	}

	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	isolated := RunIsolatedSuite(nfsPath, runID)

	result := MatrixResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		User:      u.Username,
		UID:       u.Uid,
		GID:       u.Gid,
		MountPath: nfsPath,
		MountInfo: mountInfo,
		Tests:     isolated.Tests,
		Summary:   map[string]int{"pass": isolated.Summary.Pass, "fail": isolated.Summary.Fail},
	}

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

func handleTestSuite(w http.ResponseWriter, r *http.Request) {
	u, _ := user.Current()
	runID := fmt.Sprintf("%d", time.Now().UnixNano())

	mountInfo := ""
	if out, err := exec.Command("mount").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, nfsPath) {
				mountInfo = line
				break
			}
		}
	}

	isolated := RunIsolatedSuite(nfsPath, runID)
	shared := RunSharedSuite(nfsPath, runID)

	result := FullSuiteResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		RunID:     runID,
		User:      u.Username,
		UID:       u.Uid,
		GID:       u.Gid,
		MountPath: nfsPath,
		MountInfo: mountInfo,
		Isolated:  isolated,
		Shared:    shared,
		OverallSummary: SuiteSummary{
			Pass:  isolated.Summary.Pass + shared.Summary.Pass,
			Fail:  isolated.Summary.Fail + shared.Summary.Fail,
			Total: isolated.Summary.Total + shared.Summary.Total,
		},
	}

	writeJSON(w, result)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
