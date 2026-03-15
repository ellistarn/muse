// Package testutil provides shared test doubles for the muse project.
package testutil

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/distill"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
)

// Compile-time interface checks.
var (
	_ storage.Store = (*ConversationStore)(nil)
	_ distill.LLM   = (*MockLLM)(nil)
)

// ---------------------------------------------------------------------------
// ConversationStore
// ---------------------------------------------------------------------------

// ConversationStore is an in-memory implementation of storage.Store for tests.
type ConversationStore struct {
	Sessions    []storage.SessionEntry
	Data        map[string]*conversation.Session
	Muse        string
	Reflections map[string]string
	Deleted     []string
	Muses       map[string]string // timestamp -> content
}

// NewConversationStore returns a ready-to-use ConversationStore.
func NewConversationStore() *ConversationStore {
	return &ConversationStore{
		Data:        map[string]*conversation.Session{},
		Reflections: map[string]string{},
		Muses:       map[string]string{},
	}
}

// AddSession is a helper that registers a session in the store.
func (s *ConversationStore) AddSession(src, id string, modified time.Time, messages []conversation.Message) {
	key := fmt.Sprintf("conversations/%s/%s.json", src, id)
	s.Sessions = append(s.Sessions, storage.SessionEntry{
		Source:       src,
		SessionID:    id,
		Key:          key,
		LastModified: modified,
	})
	s.Data[src+"/"+id] = &conversation.Session{
		Source:    src,
		SessionID: id,
		Messages:  messages,
	}
}

func (s *ConversationStore) ListSessions(_ context.Context) ([]storage.SessionEntry, error) {
	return s.Sessions, nil
}

func (s *ConversationStore) GetSession(_ context.Context, src, sessionID string) (*conversation.Session, error) {
	sess, ok := s.Data[src+"/"+sessionID]
	if !ok {
		return nil, &storage.NotFoundError{Key: fmt.Sprintf("conversations/%s/%s.json", src, sessionID)}
	}
	return sess, nil
}

func (s *ConversationStore) PutSession(_ context.Context, session *conversation.Session) (int, error) {
	key := fmt.Sprintf("conversations/%s/%s.json", session.Source, session.SessionID)
	s.Data[session.Source+"/"+session.SessionID] = session
	s.Sessions = append(s.Sessions, storage.SessionEntry{
		Source:       session.Source,
		SessionID:    session.SessionID,
		Key:          key,
		LastModified: time.Now(),
	})
	return 0, nil
}

func (s *ConversationStore) GetMuse(_ context.Context) (string, error) {
	if s.Muse == "" {
		return "", &storage.NotFoundError{Key: "muse.md"}
	}
	return s.Muse, nil
}

func (s *ConversationStore) PutMuse(_ context.Context, timestamp, content string) error {
	s.Muses[timestamp] = content
	s.Muse = content
	return nil
}

func (s *ConversationStore) PutMuseDiff(_ context.Context, _, _ string) error {
	return nil
}

func (s *ConversationStore) GetMuseDiff(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (s *ConversationStore) ListMuses(_ context.Context) ([]string, error) {
	timestamps := make([]string, 0, len(s.Muses))
	for ts := range s.Muses {
		timestamps = append(timestamps, ts)
	}
	sort.Strings(timestamps)
	return timestamps, nil
}

func (s *ConversationStore) GetMuseVersion(_ context.Context, timestamp string) (string, error) {
	content, ok := s.Muses[timestamp]
	if !ok {
		return "", &storage.NotFoundError{Key: "muse/versions/" + timestamp}
	}
	return content, nil
}

func (s *ConversationStore) ListReflections(_ context.Context) (map[string]time.Time, error) {
	result := map[string]time.Time{}
	for key := range s.Reflections {
		result[key] = time.Now()
	}
	return result, nil
}

func (s *ConversationStore) GetReflection(_ context.Context, conversationKey string) (string, error) {
	content, ok := s.Reflections[conversationKey]
	if !ok {
		return "", &storage.NotFoundError{Key: conversationKey}
	}
	return content, nil
}

func (s *ConversationStore) PutReflection(_ context.Context, key, content string) error {
	s.Reflections[key] = content
	return nil
}

func (s *ConversationStore) DeletePrefix(_ context.Context, prefix string) error {
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

// MockLLM is a test double for distill.LLM that returns canned responses.
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
