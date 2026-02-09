package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	sessionPath = getEnv("SESSION_PATH", "/data/sessions")
	listenAddr  = getEnv("LISTEN_ADDR", ":8081")
	hostname    = getHostname()
)

type SessionDigest struct {
	Filename  string `json:"filename"`
	Username  string `json:"username"`
	MD5       string `json:"md5"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
}

func main() {
	log.Printf("session-watcher starting on %s", listenAddr)
	log.Printf("watching: %s", sessionPath)
	log.Printf("hostname: %s", hostname)

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/api/v1/digest", handleDigest)

	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

func handleDigest(w http.ResponseWriter, r *http.Request) {
	digests := readSessions()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(map[string]interface{}{
		"sessions":  digests,
		"count":     len(digests),
		"served_by": hostname,
	})
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	digests := readSessions()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
  <title>Session Watcher</title>
  <meta http-equiv="refresh" content="5">
  <style>
    body { font-family: monospace; max-width: 800px; margin: 40px auto; padding: 0 20px; background: #f8f9fa; color: #212529; }
    h1 { color: #0056b3; }
    .info { color: #495057; margin-bottom: 16px; }
    .served-by { color: #0056b3; font-weight: bold; }
    table { width: 100%%; border-collapse: collapse; background: #fff; border: 1px solid #dee2e6; border-radius: 8px; }
    td, th { padding: 8px 12px; text-align: left; border-bottom: 1px solid #dee2e6; font-size: 13px; }
    th { background: #e9ecef; }
    .empty { padding: 20px; text-align: center; color: #868e96; }
    .md5 { font-family: monospace; font-size: 11px; color: #495057; }
    .badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 11px; background: #0056b3; color: #fff; }
  </style>
</head>
<body>
  <h1>Session Watcher</h1>
  <p class="info">
    Read-only view of <code>%s</code><br>
    Served by: <span class="served-by">%s</span> |
    Sessions: <span class="badge">%d</span> |
    Auto-refreshes every 5s
  </p>
`, sessionPath, hostname, len(digests))

	if len(digests) == 0 {
		fmt.Fprint(w, `<div class="empty">no sessions found — log in on the main app to create one</div>`)
	} else {
		fmt.Fprint(w, `<table>
  <tr><th>User</th><th>File</th><th>MD5</th><th>Size</th><th>Age</th></tr>
`)
		for _, d := range digests {
			age := "?"
			if t, err := time.Parse(time.RFC3339, d.CreatedAt); err == nil {
				age = time.Since(t).Truncate(time.Second).String()
			}
			fmt.Fprintf(w, `  <tr><td>%s</td><td>%s</td><td class="md5">%s</td><td>%d B</td><td>%s</td></tr>
`, d.Username, d.Filename, d.MD5, d.Size, age)
		}
		fmt.Fprint(w, `</table>`)
	}

	fmt.Fprint(w, `
  <p style="margin-top:20px; font-size:12px; color:#868e96;">
    this component only mounts <code>/data/sessions</code> — it cannot see <code>/data/images</code>
  </p>
</body>
</html>`)
}

func readSessions() []SessionDigest {
	entries, err := os.ReadDir(sessionPath)
	if err != nil {
		log.Printf("readdir %s: %v", sessionPath, err)
		return nil
	}

	var digests []SessionDigest
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || e.IsDir() {
			continue
		}

		path := filepath.Join(sessionPath, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		info, _ := e.Info()

		// parse username from session json
		var sess struct {
			Username  string `json:"username"`
			CreatedAt string `json:"created_at"`
		}
		json.Unmarshal(data, &sess)

		hash := fmt.Sprintf("%x", md5.Sum(data))

		digests = append(digests, SessionDigest{
			Filename:  e.Name(),
			Username:  sess.Username,
			MD5:       hash,
			Size:      info.Size(),
			CreatedAt: sess.CreatedAt,
		})
	}
	return digests
}

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
