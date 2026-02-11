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

var (
	sessionPath = getEnv("SESSION_PATH", "/data/sessions")
	imagesPath  = getEnv("IMAGES_PATH", "/data/images")
	hostname    = getHostname()
)

type TestResult struct {
	Name     string `json:"name"`
	Pass     bool   `json:"pass"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
	Context  string `json:"context,omitempty"`
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

func getHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

var sessions *SessionStore

func main() {
	log.Printf("nfs-tester starting on %s", listenAddr)
	log.Printf("NFS path: %s", nfsPath)
	log.Printf("Session path: %s", sessionPath)
	log.Printf("Images path: %s", imagesPath)
	log.Printf("Hostname: %s", hostname)

	sessions = NewSessionStore(sessionPath)
	os.MkdirAll(imagesPath, 0755)
	// gvisor gofer ignores mode on mkdir over NFS, force correct perms
	os.Chmod(imagesPath, 0755)

	u, _ := user.Current()
	log.Printf("Running as: %s (uid=%s, gid=%s)", u.Username, u.Uid, u.Gid)

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/api/v1/info", handleInfo)
	http.HandleFunc("/api/v1/matrix", handleMatrix)
	http.HandleFunc("/api/v1/exec", handleExec)
	http.HandleFunc("/api/v1/test-suite", handleTestSuite)

	http.HandleFunc("/api/v1/login", handleLogin)
	http.HandleFunc("/api/v1/me", handleMe)
	http.HandleFunc("/api/v1/logout", handleLogout)
	http.HandleFunc("/api/v1/sessions", handleSessions)

	http.HandleFunc("/api/v1/images/upload", handleImageUpload)
	http.HandleFunc("/api/v1/images/delete/", handleImageDelete)
	http.HandleFunc("/api/v1/images/", handleImageRouter)
	http.HandleFunc("/api/v1/images", handleImageList)

	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
  <title>NFS Session Demo</title>
  <style>
    body { font-family: monospace; max-width: 800px; margin: 40px auto; padding: 0 20px; background: #f8f9fa; color: #212529; }
    h1 { color: #0056b3; }
    h2 { color: #495057; margin-top: 30px; }
    .card { background: #fff; border: 1px solid #dee2e6; border-radius: 8px; padding: 20px; margin: 16px 0; box-shadow: 0 1px 3px rgba(0,0,0,0.08); }
    input { padding: 8px 12px; margin: 4px; background: #fff; border: 1px solid #ced4da; color: #212529; border-radius: 4px; }
    button { padding: 8px 16px; margin: 4px; background: #0056b3; color: #fff; border: none; border-radius: 4px; cursor: pointer; font-weight: bold; }
    button:hover { background: #004094; }
    button.danger { background: #dc3545; color: white; }
    #result { white-space: pre-wrap; margin-top: 12px; padding: 12px; background: #f1f3f5; border: 1px solid #dee2e6; border-radius: 4px; min-height: 40px; }
    .served-by { color: #0056b3; font-weight: bold; }
    table { width: 100%%; border-collapse: collapse; }
    td, th { padding: 6px 12px; text-align: left; border-bottom: 1px solid #dee2e6; }
    a { color: #0056b3; }
  </style>
</head>
<body>
  <h1>NFS Session Demo</h1>
  <p>Served by: <span class="served-by">%s</span></p>

  <div class="card">
    <h2>Login</h2>
    <p>Hardcoded users: alice/password123, bob/password456</p>
    <input id="username" placeholder="username" value="alice">
    <input id="password" type="password" placeholder="password" value="password123">
    <button onclick="doLogin()">Login</button>
  </div>

  <div class="card">
    <h2>Session</h2>
    <button onclick="doMe()">Who am I? (GET /api/v1/me)</button>
    <button onclick="doLogout()" class="danger">Logout</button>
    <button onclick="doSessions()">List all sessions</button>
    <button onclick="doLoop()">Loop 200x (test all instances)</button>
    <div id="result">click a button...</div>
  </div>

  <div class="card">
    <h2>Image Gallery (shared NFS)</h2>
    <p>Upload images on any instance, view from all instances.</p>
    <input type="file" id="imageFile" accept="image/*">
    <button onclick="doUpload()">Upload</button>
    <button onclick="doGallery()">Refresh Gallery</button>
    <div id="gallery" style="margin-top:12px; display:flex; flex-wrap:wrap; gap:12px;"></div>
    <div id="imgResult" style="margin-top:8px; color:#495057;"></div>
  </div>

  <div class="card">
    <h2>NFS Test Endpoints</h2>
    <table>
      <tr><td>GET</td><td><a href="/health">/health</a></td><td>Health check</td></tr>
      <tr><td>GET</td><td><a href="/api/v1/info">/api/v1/info</a></td><td>System and mount info</td></tr>
      <tr><td>GET</td><td><a href="/api/v1/matrix">/api/v1/matrix</a></td><td>Run NFS test matrix</td></tr>
      <tr><td>GET</td><td><a href="/api/v1/test-suite">/api/v1/test-suite</a></td><td>Full NFS test suite</td></tr>
      <tr><td>GET</td><td>/api/v1/exec?cmd=&lt;cmd&gt;</td><td>Execute shell command</td></tr>
    </table>
  </div>

<script>
const out = document.getElementById('result');

async function doLogin() {
  const resp = await fetch('/api/v1/login', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      username: document.getElementById('username').value,
      password: document.getElementById('password').value,
    }),
  });
  out.textContent = resp.status + ' ' + resp.statusText + '\n' + await resp.text();
}

async function doMe() {
  const resp = await fetch('/api/v1/me');
  out.textContent = resp.status + ' ' + resp.statusText + '\n' + await resp.text();
}

async function doLogout() {
  const resp = await fetch('/api/v1/logout', {method: 'POST'});
  out.textContent = resp.status + ' ' + resp.statusText + '\n' + await resp.text();
}

async function doSessions() {
  const resp = await fetch('/api/v1/sessions');
  out.textContent = resp.status + ' ' + resp.statusText + '\n' + await resp.text();
}

async function doLoop() {
  out.textContent = 'running 200 requests...\n';
  const instances = {};
  let failures = 0;
  for (let i = 0; i < 200; i++) {
    const resp = await fetch('/api/v1/me');
    if (resp.ok) {
      const data = await resp.json();
      instances[data.served_by] = (instances[data.served_by] || 0) + 1;
      out.textContent += '#' + (i+1) + ' 200 served_by=' + data.served_by + '\n';
    } else {
      failures++;
      out.textContent += '#' + (i+1) + ' ' + resp.status + ' FAIL\n';
    }
  }
  const keys = Object.keys(instances);
  out.textContent += '\n--- summary ---\n';
  out.textContent += 'total: 200, ok: ' + (200-failures) + ', fail: ' + failures + '\n';
  out.textContent += 'instances hit: ' + keys.length + '\n';
  for (const [k,v] of Object.entries(instances)) {
    out.textContent += '  ' + k + ': ' + v + ' requests\n';
  }
  if (keys.length >= 2) {
    out.textContent += '\nSHARED SESSION WORKS - requests served by multiple instances\n';
  } else if (keys.length === 1 && failures === 0) {
    out.textContent += '\nall requests hit same instance (try more requests or check instance_count)\n';
  }
}

const gallery = document.getElementById('gallery');
const imgResult = document.getElementById('imgResult');

async function doUpload() {
  const input = document.getElementById('imageFile');
  if (!input.files.length) { imgResult.textContent = 'select a file first'; return; }
  const form = new FormData();
  form.append('image', input.files[0]);
  imgResult.textContent = 'uploading...';
  const resp = await fetch('/api/v1/images/upload', {method:'POST', body: form});
  const data = await resp.json();
  imgResult.textContent = resp.ok
    ? 'uploaded ' + data.filename + ' (' + data.size + ' bytes) via ' + data.served_by
    : 'error: ' + (data.error || resp.statusText);
  if (resp.ok) doGallery();
}

async function doGallery() {
  const resp = await fetch('/api/v1/images');
  const data = await resp.json();
  gallery.innerHTML = '';
  if (!data.images || data.images.length === 0) {
    gallery.innerHTML = '<p style="color:#868e96">no images yet</p>';
    return;
  }
  data.images.forEach(img => {
    const card = document.createElement('div');
    card.style.cssText = 'border:1px solid #dee2e6; border-radius:4px; padding:8px; text-align:center; width:160px; background:#fff;';
    card.innerHTML = '<a href="' + img.url + '" target="_blank">'
      + '<img src="' + img.url + '" style="max-width:140px; max-height:140px; display:block; margin:0 auto 6px;">'
      + '</a>'
      + '<div style="font-size:11px; word-break:break-all;">' + img.name + '</div>'
      + '<div style="font-size:10px; color:#868e96;">' + (img.size/1024).toFixed(1) + ' KB</div>'
      + '<button class="danger" style="font-size:11px; padding:2px 8px; margin-top:4px;" onclick="doDeleteImage(\'' + img.name + '\')">delete</button>';
    gallery.appendChild(card);
  });
  imgResult.textContent = data.count + ' image(s), served by ' + data.served_by;
}

async function doDeleteImage(name) {
  const resp = await fetch('/api/v1/images/delete/' + encodeURIComponent(name), {method:'POST'});
  const data = await resp.json();
  imgResult.textContent = resp.ok ? 'deleted ' + name : 'error: ' + (data.error || resp.statusText);
  doGallery();
}

doGallery();
</script>
</body>
</html>`, hostname)
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

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "invalid json"})
		return
	}

	expected, ok := validUsers[req.Username]
	if !ok || expected != req.Password {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "invalid credentials"})
		return
	}

	sess, err := sessions.Create(req.Username, hostname)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  "session",
		Value: sess.SessionID,
		Path:  "/",
	})

	writeJSON(w, map[string]string{
		"status":     "ok",
		"username":   sess.Username,
		"session_id": sess.SessionID,
		"served_by":  hostname,
	})
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]string{"error": "no session cookie"})
		return
	}

	sess, err := sessions.Get(cookie.Value)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]string{"error": "session not found"})
		return
	}

	writeJSON(w, map[string]string{
		"username":   sess.Username,
		"session_id": sess.SessionID,
		"created_by": sess.CreatedBy,
		"served_by":  hostname,
	})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("session")
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]string{"error": "no session cookie"})
		return
	}

	sessions.Delete(cookie.Value)

	http.SetCookie(w, &http.Cookie{
		Name:   "session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

	writeJSON(w, map[string]string{
		"status":    "ok",
		"served_by": hostname,
	})
}

func handleSessions(w http.ResponseWriter, r *http.Request) {
	list, err := sessions.List()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, map[string]interface{}{
		"sessions":  list,
		"count":     len(list),
		"served_by": hostname,
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
