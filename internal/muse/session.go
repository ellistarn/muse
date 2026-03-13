package muse

import (
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/google/uuid"
)

// Session holds a Bedrock conversation's message history so it can be continued.
type Session struct {
	ID       string
	System   string          // system prompt for the conversation
	Messages []types.Message // full conversation history
}

// sessionStore is an in-memory map of active sessions.
// Sessions die with the process — no persistence, no TTL.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*Session)}
}

// save stores or updates a session and returns its ID.
func (s *sessionStore) save(session *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session.ID == "" {
		session.ID = uuid.New().String()
	}
	s.sessions[session.ID] = session
	return session.ID
}

// get retrieves a session. Returns an error if not found.
func (s *sessionStore) get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found (sessions are ephemeral and lost on restart)", id)
	}
	return session, nil
}
