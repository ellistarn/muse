// Package testutil provides shared test doubles for the muse project.
package testutil

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ellistarn/muse/internal/dream"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/memory"
	"github.com/ellistarn/muse/internal/storage"
)

// Compile-time interface checks.
var (
	_ storage.Store = (*MemoryStore)(nil)
	_ storage.Store = (*FailingStore)(nil)
	_ dream.LLM     = (*MockLLM)(nil)
)

// ---------------------------------------------------------------------------
// MemoryStore
// ---------------------------------------------------------------------------

// MemoryStore is an in-memory implementation of storage.Store for tests.
type MemoryStore struct {
	Sessions    []storage.SessionEntry
	Data        map[string]*memory.Session
	Muse        string
	Reflections map[string]string
	Deleted     []string
	Muses       map[string]string // timestamp -> content
}

// NewMemoryStore returns a ready-to-use MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		Data:        map[string]*memory.Session{},
		Reflections: map[string]string{},
		Muses:       map[string]string{},
	}
}

// AddSession is a helper that registers a session in the store.
func (s *MemoryStore) AddSession(src, id string, modified time.Time, messages []memory.Message) {
	key := fmt.Sprintf("memories/%s/%s.json", src, id)
	s.Sessions = append(s.Sessions, storage.SessionEntry{
		Source:       src,
		SessionID:    id,
		Key:          key,
		LastModified: modified,
	})
	s.Data[src+"/"+id] = &memory.Session{
		Source:    src,
		SessionID: id,
		Messages:  messages,
	}
}

func (s *MemoryStore) ListSessions(_ context.Context) ([]storage.SessionEntry, error) {
	return s.Sessions, nil
}

func (s *MemoryStore) GetSession(_ context.Context, src, sessionID string) (*memory.Session, error) {
	sess, ok := s.Data[src+"/"+sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found: %s/%s", src, sessionID)
	}
	return sess, nil
}

func (s *MemoryStore) PutSession(_ context.Context, session *memory.Session) (int, error) {
	key := fmt.Sprintf("memories/%s/%s.json", session.Source, session.SessionID)
	s.Data[session.Source+"/"+session.SessionID] = session
	s.Sessions = append(s.Sessions, storage.SessionEntry{
		Source:       session.Source,
		SessionID:    session.SessionID,
		Key:          key,
		LastModified: time.Now(),
	})
	return 0, nil
}

func (s *MemoryStore) GetMuse(_ context.Context) (string, error) {
	if s.Muse == "" {
		return "", &storage.NotFoundError{Key: "muse.md"}
	}
	return s.Muse, nil
}

func (s *MemoryStore) PutMuse(_ context.Context, timestamp, content string) error {
	s.Muses[timestamp] = content
	s.Muse = content
	return nil
}

func (s *MemoryStore) ListMuses(_ context.Context) ([]string, error) {
	timestamps := make([]string, 0, len(s.Muses))
	for ts := range s.Muses {
		timestamps = append(timestamps, ts)
	}
	sort.Strings(timestamps)
	return timestamps, nil
}

func (s *MemoryStore) GetMuseVersion(_ context.Context, timestamp string) (string, error) {
	content, ok := s.Muses[timestamp]
	if !ok {
		return "", &storage.NotFoundError{Key: "muse/versions/" + timestamp}
	}
	return content, nil
}

func (s *MemoryStore) ListReflections(_ context.Context) (map[string]time.Time, error) {
	result := map[string]time.Time{}
	for key := range s.Reflections {
		result[key] = time.Now()
	}
	return result, nil
}

func (s *MemoryStore) GetReflection(_ context.Context, memoryKey string) (string, error) {
	content, ok := s.Reflections[memoryKey]
	if !ok {
		return "", fmt.Errorf("reflection not found: %s", memoryKey)
	}
	return content, nil
}

func (s *MemoryStore) PutReflection(_ context.Context, key, content string) error {
	s.Reflections[key] = content
	return nil
}

func (s *MemoryStore) DeletePrefix(_ context.Context, prefix string) error {
	s.Deleted = append(s.Deleted, prefix)
	if prefix == "reflections/" {
		s.Reflections = map[string]string{}
	}
	return nil
}

// ---------------------------------------------------------------------------
// MockLLM
// ---------------------------------------------------------------------------

// LLMCall records the arguments of a single Converse call.
type LLMCall struct {
	System string
	User   string
}

// MockLLM is a test double for dream.LLM that returns canned responses.
// It dispatches based on whether the system prompt contains
// "distilling observations" (learn phase) or not (reflect phase).
type MockLLM struct {
	ReflectResponse string
	LearnResponse   string
	Err             error
	Calls           []LLMCall
}

func (m *MockLLM) Converse(_ context.Context, system, user string, _ ...inference.ConverseOption) (string, inference.Usage, error) {
	m.Calls = append(m.Calls, LLMCall{System: system, User: user})
	if m.Err != nil {
		return "", inference.Usage{}, m.Err
	}
	usage := inference.Usage{InputTokens: 100, OutputTokens: 50}
	if strings.Contains(system, "distilling observations") {
		return m.LearnResponse, usage, nil
	}
	return m.ReflectResponse, usage, nil
}

// ---------------------------------------------------------------------------
// FailingStore
// ---------------------------------------------------------------------------

// FailingStore is a storage.Store where every operation returns Err.
type FailingStore struct {
	Err error
}

func (s *FailingStore) ListSessions(_ context.Context) ([]storage.SessionEntry, error) {
	return nil, s.Err
}

func (s *FailingStore) GetSession(_ context.Context, _, _ string) (*memory.Session, error) {
	return nil, s.Err
}

func (s *FailingStore) PutSession(_ context.Context, _ *memory.Session) (int, error) {
	return 0, s.Err
}

func (s *FailingStore) GetMuse(_ context.Context) (string, error) {
	return "", s.Err
}

func (s *FailingStore) PutMuse(_ context.Context, _, _ string) error {
	return s.Err
}

func (s *FailingStore) ListMuses(_ context.Context) ([]string, error) {
	return nil, s.Err
}

func (s *FailingStore) GetMuseVersion(_ context.Context, _ string) (string, error) {
	return "", s.Err
}

func (s *FailingStore) ListReflections(_ context.Context) (map[string]time.Time, error) {
	return nil, s.Err
}

func (s *FailingStore) GetReflection(_ context.Context, _ string) (string, error) {
	return "", s.Err
}

func (s *FailingStore) PutReflection(_ context.Context, _, _ string) error {
	return s.Err
}

func (s *FailingStore) DeletePrefix(_ context.Context, _ string) error {
	return s.Err
}
