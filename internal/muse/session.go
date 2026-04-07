package muse

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/google/uuid"
)

// Session holds a conversation's message history so it can be continued.
// The mutex serializes concurrent Ask calls that target the same session.
type Session struct {
	mu       sync.Mutex
	ID       string              `json:"id"`
	System   string              `json:"system"`   // system prompt at creation time (metadata)
	Messages []inference.Message `json:"messages"` // full conversation history
}

// sessionStore is an in-memory map of active sessions.
// When dir is set, sessions are also persisted to disk so they survive restarts.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	dir      string // if non-empty, persist sessions here
}

func newSessionStore(dir string) *sessionStore {
	return &sessionStore{sessions: make(map[string]*Session), dir: dir}
}

// save stores or updates a session and returns its ID.
// If a directory is configured, the session is also written to disk.
func (s *sessionStore) save(session *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session.ID == "" {
		session.ID = uuid.New().String()
	}
	s.sessions[session.ID] = session
	if s.dir != "" {
		if err := persistSession(s.dir, session); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to persist session: %v\n", err)
		}
	}
	return session.ID
}

// get retrieves a session from memory first, then falls back to disk.
func (s *sessionStore) get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[id]; ok {
		return session, nil
	}
	// Fall back to disk if persistence is configured.
	if s.dir != "" {
		session, err := loadSession(s.dir, id)
		if err == nil {
			s.sessions[id] = session
			return session, nil
		}
	}
	return nil, fmt.Errorf("session %q not found", id)
}

// latestID returns the most recent session ID from disk, or "" if none exists.
func (s *sessionStore) latestID() string {
	if s.dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "latest"))
	if err != nil {
		return ""
	}
	return string(data)
}

// setLatest updates the latest pointer on disk so the CLI can resume.
// Only callers that own the "resume last session" UX should call this.
// Fire-and-forget: the latest pointer is a convenience shortcut, not
// a correctness requirement — a failure here means the next `muse ask`
// starts a new session instead of resuming, which is acceptable.
func (s *sessionStore) setLatest(id string) {
	if s.dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(s.dir, "latest"), []byte(id), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to update latest pointer: %v\n", err)
	}
}

// persistSession writes a session to disk.
func persistSession(dir string, session *Session) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, session.ID+".json"), data, 0o644)
}

// loadSession reads a session from disk.
func loadSession(dir, id string) (*Session, error) {
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}
