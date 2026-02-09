package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxUploadSize = 10 << 20 // 10MB

type ImageInfo struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	URL      string `json:"url"`
	ServedBy string `json:"served_by"`
}

func handleImageUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "file too large or invalid form (max 10MB)"})
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "missing 'image' field"})
		return
	}
	defer file.Close()

	filename := sanitizeFilename(header.Filename)
	if filename == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid filename"})
		return
	}

	// prefix with unix timestamp to avoid collisions
	filename = fmt.Sprintf("%d-%s", time.Now().Unix(), filename)

	dst, err := os.Create(filepath.Join(imagesPath, filename))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": fmt.Sprintf("create file: %v", err)})
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": fmt.Sprintf("write file: %v", err)})
		return
	}

	writeJSON(w, map[string]interface{}{
		"filename":  filename,
		"size":      written,
		"served_by": hostname,
	})
}

func handleImageList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(imagesPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": fmt.Sprintf("read dir: %v", err)})
		return
	}

	images := make([]ImageInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		images = append(images, ImageInfo{
			Name:     e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339),
			URL:      "/api/v1/images/" + e.Name(),
			ServedBy: hostname,
		})
	}

	writeJSON(w, map[string]interface{}{
		"images":    images,
		"count":     len(images),
		"served_by": hostname,
	})
}

// handleImageRouter dispatches /api/v1/images/ to list or serve based on path
func handleImageRouter(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/images/")
	if name == "" {
		handleImageList(w, r)
		return
	}
	handleImageServe(w, r)
}

func handleImageServe(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimPrefix(r.URL.Path, "/api/v1/images/")
	filename = sanitizeFilename(filename)
	if filename == "" {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, filepath.Join(imagesPath, filename))
}

func handleImageDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	filename := strings.TrimPrefix(r.URL.Path, "/api/v1/images/delete/")
	filename = sanitizeFilename(filename)
	if filename == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid filename"})
		return
	}

	path := filepath.Join(imagesPath, filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "file not found"})
		return
	}

	if err := os.Remove(path); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": fmt.Sprintf("delete: %v", err)})
		return
	}

	writeJSON(w, map[string]interface{}{
		"status":    "deleted",
		"filename":  filename,
		"served_by": hostname,
	})
}

// sanitizeFilename strips path components and rejects traversal attempts
func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "/" || name == "" {
		return ""
	}
	// reject anything with path separators after Base (shouldn't happen, but be safe)
	if strings.ContainsAny(name, "/\\") {
		return ""
	}
	return name
}
