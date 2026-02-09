package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Session struct {
	Username  string `json:"username"`
	SessionID string `json:"session_id"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by"`
}

// hardcoded users for the demo
var validUsers = map[string]string{
	"alice": "password123",
	"bob":   "password456",
}

type SessionStore struct {
	dir string
}

func NewSessionStore(dir string) *SessionStore {
	os.MkdirAll(dir, 0755)
	return &SessionStore{dir: dir}
}

func (s *SessionStore) Create(username, hostname string) (*Session, error) {
	id, err := generateSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}

	sess := &Session{
		Username:  username,
		SessionID: id,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		CreatedBy: hostname,
	}

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal session: %w", err)
	}

	path := filepath.Join(s.dir, id+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return nil, fmt.Errorf("write session file: %w", err)
	}

	return sess, nil
}

func (s *SessionStore) Get(sessionID string) (*Session, error) {
	// prevent directory traversal
	if strings.Contains(sessionID, "/") || strings.Contains(sessionID, "..") {
		return nil, fmt.Errorf("invalid session id")
	}

	path := filepath.Join(s.dir, sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &sess, nil
}

func (s *SessionStore) Delete(sessionID string) error {
	if strings.Contains(sessionID, "/") || strings.Contains(sessionID, "..") {
		return fmt.Errorf("invalid session id")
	}
	return os.Remove(filepath.Join(s.dir, sessionID+".json"))
}

func (s *SessionStore) List() ([]Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var sessions []Session
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			continue
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

func generateSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
