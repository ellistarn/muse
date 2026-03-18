// Package testutil provides shared test doubles for the muse project.
package testutil

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
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
	Conversations []storage.ConversationEntry
	Data          map[string]*conversation.Conversation
	Muse          string
	Observations  map[string]string
	RawData       map[string][]byte // generic key/value for PutData/GetData
	Deleted       []string
	Muses         map[string]string // timestamp -> content
	mu            sync.Mutex
}

// NewConversationStore returns a ready-to-use ConversationStore.
func NewConversationStore() *ConversationStore {
	return &ConversationStore{
		Data:         map[string]*conversation.Conversation{},
		Observations: map[string]string{},
		RawData:      map[string][]byte{},
		Muses:        map[string]string{},
	}
}

// AddConversation is a helper that registers a conversation in the store.
func (s *ConversationStore) AddConversation(src, id string, modified time.Time, messages []conversation.Message) {
	key := fmt.Sprintf("conversations/%s/%s.json", src, id)
	s.Conversations = append(s.Conversations, storage.ConversationEntry{
		Source:         src,
		ConversationID: id,
		Key:            key,
		LastModified:   modified,
	})
	s.Data[src+"/"+id] = &conversation.Conversation{
		Source:         src,
		ConversationID: id,
		Messages:       messages,
	}
}

func (s *ConversationStore) ListConversations(_ context.Context) ([]storage.ConversationEntry, error) {
	return s.Conversations, nil
}

func (s *ConversationStore) GetConversation(_ context.Context, src, conversationID string) (*conversation.Conversation, error) {
	conv, ok := s.Data[src+"/"+conversationID]
	if !ok {
		return nil, &storage.NotFoundError{Key: fmt.Sprintf("conversations/%s/%s.json", src, conversationID)}
	}
	return conv, nil
}

func (s *ConversationStore) PutConversation(_ context.Context, conv *conversation.Conversation) (int, error) {
	key := fmt.Sprintf("conversations/%s/%s.json", conv.Source, conv.ConversationID)
	s.Data[conv.Source+"/"+conv.ConversationID] = conv
	s.Conversations = append(s.Conversations, storage.ConversationEntry{
		Source:         conv.Source,
		ConversationID: conv.ConversationID,
		Key:            key,
		LastModified:   time.Now(),
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
		return "", &storage.NotFoundError{Key: "versions/" + timestamp}
	}
	return content, nil
}

func (s *ConversationStore) ListObservations(_ context.Context) (map[string]time.Time, error) {
	result := map[string]time.Time{}
	for key := range s.Observations {
		result[key] = time.Now()
	}
	return result, nil
}

func (s *ConversationStore) GetObservation(_ context.Context, conversationKey string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.Observations[conversationKey]
	if !ok {
		return "", &storage.NotFoundError{Key: conversationKey}
	}
	return content, nil
}

func (s *ConversationStore) PutObservation(_ context.Context, key, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Observations[key] = content
	return nil
}

func (s *ConversationStore) DeletePrefix(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Deleted = append(s.Deleted, prefix)
	if prefix == "observations/" {
		s.Observations = map[string]string{}
	}
	return nil
}

func (s *ConversationStore) PutData(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RawData[key] = append([]byte(nil), data...) // defensive copy
	return nil
}

func (s *ConversationStore) GetData(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.RawData[key]
	if !ok {
		return nil, &storage.NotFoundError{Key: key}
	}
	return data, nil
}

func (s *ConversationStore) ListData(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.RawData {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *ConversationStore) DeleteData(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.RawData {
		if strings.HasPrefix(k, prefix) {
			delete(s.RawData, k)
		}
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
// "distilling observations" (learn phase) or not (observe phase).
type MockLLM struct {
	ObserveResponse string
	LearnResponse   string
	Err             error
	mu              sync.Mutex
	Calls           []LLMCall
}

func (m *MockLLM) Converse(_ context.Context, system, user string, _ ...inference.ConverseOption) (string, inference.Usage, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, LLMCall{System: system, User: user})
	m.mu.Unlock()
	if m.Err != nil {
		return "", inference.Usage{}, m.Err
	}
	usage := inference.Usage{InputTokens: 100, OutputTokens: 50}
	if strings.Contains(system, "distilling observations") {
		return m.LearnResponse, usage, nil
	}
	return m.ObserveResponse, usage, nil
}

func (m *MockLLM) Model() string { return "mock-model" }

func (m *MockLLM) ConverseStream(ctx context.Context, system, user string, fn inference.StreamFunc, opts ...inference.ConverseOption) (string, inference.Usage, error) {
	text, usage, err := m.Converse(ctx, system, user, opts...)
	if fn != nil && err == nil {
		fn(inference.StreamDelta{Text: text})
	}
	return text, usage, err
}
