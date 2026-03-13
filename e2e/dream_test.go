package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/dream"
	"github.com/ellistarn/muse/internal/llm"
	"github.com/ellistarn/muse/internal/memory"
	"github.com/ellistarn/muse/internal/storage"
)

// mockStore implements storage.Store with in-memory state.
type mockStore struct {
	sessions    []storage.SessionEntry
	data        map[string]*memory.Session
	soul        string
	reflections map[string]string // memoryKey -> content
	deleted     []string
}

func newMockStore() *mockStore {
	return &mockStore{
		data:        map[string]*memory.Session{},
		reflections: map[string]string{},
	}
}

func (m *mockStore) addSession(src, id string, modified time.Time, messages []memory.Message) {
	key := fmt.Sprintf("memories/%s/%s.json", src, id)
	m.sessions = append(m.sessions, storage.SessionEntry{
		Source:       src,
		SessionID:    id,
		Key:          key,
		LastModified: modified,
	})
	m.data[src+"/"+id] = &memory.Session{
		Source:    src,
		SessionID: id,
		Messages:  messages,
	}
}

func (m *mockStore) ListSessions(_ context.Context) ([]storage.SessionEntry, error) {
	return m.sessions, nil
}

func (m *mockStore) GetSession(_ context.Context, src, sessionID string) (*memory.Session, error) {
	s, ok := m.data[src+"/"+sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found: %s/%s", src, sessionID)
	}
	return s, nil
}

func (m *mockStore) PutSession(_ context.Context, session *memory.Session) (int, error) {
	key := fmt.Sprintf("memories/%s/%s.json", session.Source, session.SessionID)
	m.data[session.Source+"/"+session.SessionID] = session
	m.sessions = append(m.sessions, storage.SessionEntry{
		Source: session.Source, SessionID: session.SessionID, Key: key, LastModified: time.Now(),
	})
	return 0, nil
}

func (m *mockStore) GetSoul(_ context.Context) (string, error) {
	if m.soul == "" {
		return "", &storage.NotFoundError{Key: "soul.md"}
	}
	return m.soul, nil
}

func (m *mockStore) ListReflections(_ context.Context) (map[string]time.Time, error) {
	result := map[string]time.Time{}
	for key := range m.reflections {
		result[key] = time.Now()
	}
	return result, nil
}

func (m *mockStore) GetReflection(_ context.Context, memoryKey string) (string, error) {
	content, ok := m.reflections[memoryKey]
	if !ok {
		return "", fmt.Errorf("reflection not found: %s", memoryKey)
	}
	return content, nil
}

func (m *mockStore) PutReflection(_ context.Context, key, content string) error {
	m.reflections[key] = content
	return nil
}

func (m *mockStore) DeletePrefix(_ context.Context, prefix string) error {
	m.deleted = append(m.deleted, prefix)
	if prefix == "dreams/reflections/" {
		m.reflections = map[string]string{}
	}
	return nil
}

func (m *mockStore) PutSoul(_ context.Context, content string) error {
	m.soul = content
	return nil
}

func (m *mockStore) SnapshotSoul(_ context.Context, _ string) error {
	return nil
}

func (m *mockStore) ListDreams(_ context.Context) ([]string, error) {
	return nil, nil
}

func (m *mockStore) GetDreamSoul(_ context.Context, _ string) (string, error) {
	return "", &storage.NotFoundError{Key: "dream"}
}

// mockLLM implements dream.LLM with canned responses.
type mockLLM struct {
	reflectResponse string
	learnResponse   string
	calls           []llmCall
}

type llmCall struct {
	system string
	user   string
}

func (m *mockLLM) Converse(_ context.Context, system, user string, _ ...llm.ConverseOption) (string, llm.Usage, error) {
	m.calls = append(m.calls, llmCall{system: system, user: user})
	usage := llm.Usage{InputTokens: 100, OutputTokens: 50}
	if strings.Contains(system, "distilling observations") {
		return m.learnResponse, usage, nil
	}
	return m.reflectResponse, usage, nil
}

func TestDreamPipeline(t *testing.T) {
	store := newMockStore()
	store.addSession("claude-code", "sess-1", time.Now(), []memory.Message{
		{Role: "user", Content: "use kebab-case for file names"},
		{Role: "assistant", Content: "OK, I'll rename them."},
		{Role: "user", Content: "also use lowercase"},
		{Role: "assistant", Content: "Done."},
	})
	store.addSession("claude-code", "sess-2", time.Now(), []memory.Message{
		{Role: "user", Content: "never use emojis in commit messages"},
		{Role: "assistant", Content: "Understood."},
		{Role: "user", Content: "and keep them short"},
		{Role: "assistant", Content: "Will do."},
	})

	llm := &mockLLM{
		reflectResponse: "- Prefers kebab-case file names\n- No emojis in commits",
		learnResponse:   "## Naming\n\nI use kebab-case for file names.\n\n## Commits\n\nNo emojis. Keep them short.",
	}

	result, err := dream.Run(context.Background(), store, llm, llm, dream.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if result.Pruned != 0 {
		t.Errorf("Pruned = %d, want 0", result.Pruned)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", result.Warnings)
	}

	// Verify soul was written
	if store.soul == "" {
		t.Error("soul not written to store")
	}
	if !strings.Contains(store.soul, "kebab-case") {
		t.Error("soul missing expected content")
	}

	// Verify LLM was called: 2 sessions * 3 reflect steps (summarize + extract + refine) + 1 learn = 7 calls
	if len(llm.calls) != 7 {
		t.Errorf("LLM calls = %d, want 7", len(llm.calls))
	}
}

func TestDreamPipelineNoMemories(t *testing.T) {
	store := newMockStore()
	llm := &mockLLM{}

	result, err := dream.Run(context.Background(), store, llm, llm, dream.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Processed != 0 {
		t.Errorf("Processed = %d, want 0", result.Processed)
	}
	if len(llm.calls) != 0 {
		t.Errorf("LLM calls = %d, want 0", len(llm.calls))
	}
}

func TestDreamPipelineLimit(t *testing.T) {
	store := newMockStore()
	for i := 0; i < 5; i++ {
		store.addSession("test", fmt.Sprintf("sess-%d", i), time.Now(), []memory.Message{
			{Role: "user", Content: fmt.Sprintf("message %d", i)},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "follow up"},
			{Role: "assistant", Content: "ok again"},
		})
	}

	llm := &mockLLM{
		reflectResponse: "- observation",
		learnResponse:   "## Test\n\nContent here.",
	}

	result, err := dream.Run(context.Background(), store, llm, llm, dream.Options{Limit: 2})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if result.Remaining != 3 {
		t.Errorf("Remaining = %d, want 3", result.Remaining)
	}
	// 2 sessions * 3 reflect steps + 1 learn = 7
	if len(llm.calls) != 7 {
		t.Errorf("LLM calls = %d, want 7 (2 sessions * 3 reflect steps + 1 learn)", len(llm.calls))
	}
}

func TestDreamPipelineLimitIncludesPreviousReflections(t *testing.T) {
	store := newMockStore()
	for i := 0; i < 4; i++ {
		store.addSession("test", fmt.Sprintf("sess-%d", i), time.Now(), []memory.Message{
			{Role: "user", Content: fmt.Sprintf("message %d", i)},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "follow up"},
			{Role: "assistant", Content: "ok again"},
		})
	}

	llm := &mockLLM{
		reflectResponse: "- observation",
		learnResponse:   "## Test\n\nContent here.",
	}

	// First run: limit to 2, should reflect 2 and learn from 2
	result, err := dream.Run(context.Background(), store, llm, llm, dream.Options{Limit: 2})
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	if result.Processed != 2 {
		t.Errorf("first run Processed = %d, want 2", result.Processed)
	}
	if result.Remaining != 2 {
		t.Errorf("first run Remaining = %d, want 2", result.Remaining)
	}
	firstRunReflections := len(store.reflections)
	if firstRunReflections != 2 {
		t.Errorf("reflections after first run = %d, want 2", firstRunReflections)
	}

	// Second run: limit to 2 again, should reflect 2 more and learn from all 4
	llm.calls = nil
	result, err = dream.Run(context.Background(), store, llm, llm, dream.Options{Limit: 2})
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	if result.Processed != 2 {
		t.Errorf("second run Processed = %d, want 2", result.Processed)
	}
	if result.Remaining != 0 {
		t.Errorf("second run Remaining = %d, want 0", result.Remaining)
	}
	if len(store.reflections) != 4 {
		t.Errorf("reflections after second run = %d, want 4", len(store.reflections))
	}
	// 2 sessions * 3 reflect steps + 1 learn = 7, and learn should have received all 4 reflections
	if len(llm.calls) != 7 {
		t.Errorf("second run LLM calls = %d, want 7", len(llm.calls))
	}
	// The learn call (last one) should contain all 4 observations joined by ---
	learnInput := llm.calls[len(llm.calls)-1].user
	separators := strings.Count(learnInput, "---")
	// 4 observations joined by "---" = 3 separators (in the join delimiters)
	if separators < 3 {
		t.Errorf("learn input has %d separators, want at least 3 (all 4 reflections)", separators)
	}
}

func TestDreamPipelineEmptyConversation(t *testing.T) {
	store := newMockStore()
	// Session with only empty messages produces no observations
	store.addSession("test", "empty", time.Now(), []memory.Message{
		{Role: "user", Content: ""},
		{Role: "assistant", Content: ""},
	})

	llm := &mockLLM{}

	result, err := dream.Run(context.Background(), store, llm, llm, dream.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// Empty conversation produces no reflect call, but still shows up in pending
	if result.Processed != 0 {
		t.Errorf("Processed = %d, want 0", result.Processed)
	}
}

func TestDreamPipelineReflect(t *testing.T) {
	store := newMockStore()
	store.addSession("test", "sess-1", time.Now(), []memory.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "one more thing"},
		{Role: "assistant", Content: "sure"},
	})

	llm := &mockLLM{
		reflectResponse: "- observation",
		learnResponse:   "## Test\n\nContent.",
	}

	// First run
	_, err := dream.Run(context.Background(), store, llm, llm, dream.Options{})
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	// With Reprocess, it should process again even though state would normally prune it
	llm.calls = nil
	result, err := dream.Run(context.Background(), store, llm, llm, dream.Options{Reflect: true})
	if err != nil {
		t.Fatalf("reprocess Run() error: %v", err)
	}
	if result.Processed != 1 {
		t.Errorf("Processed = %d, want 1", result.Processed)
	}
}
