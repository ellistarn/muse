package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/distill"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/testutil"
)

// twoSessionStore returns a store with two conversations, each with two
// human turns (the minimum for observation extraction).
func twoSessionStore() *testutil.ConversationStore {
	store := testutil.NewConversationStore()
	store.AddSession("claude-code", "sess-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "use kebab-case for file names"},
		{Role: "assistant", Content: "OK, I'll rename them."},
		{Role: "user", Content: "also use lowercase"},
		{Role: "assistant", Content: "Done."},
	})
	store.AddSession("claude-code", "sess-2", time.Now(), []conversation.Message{
		{Role: "user", Content: "never use emojis in commit messages"},
		{Role: "assistant", Content: "Understood."},
		{Role: "user", Content: "and keep them short"},
		{Role: "assistant", Content: "Will do."},
	})
	return store
}

// ---------------------------------------------------------------------------
// Map-Reduce Pipeline
// ---------------------------------------------------------------------------

func TestMapReduce_EndToEnd(t *testing.T) {
	store := twoSessionStore()
	llm := &testutil.MockLLM{
		ObserveResponse: "- Prefers kebab-case file names\n- No emojis in commits",
		LearnResponse:   "## Naming\n\nI use kebab-case for file names.\n\n## Commits\n\nNo emojis. Keep them short.",
	}

	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if result.Pruned != 0 {
		t.Errorf("Pruned = %d, want 0", result.Pruned)
	}
	if store.Muse == "" {
		t.Error("muse not written to store")
	}
	if !strings.Contains(store.Muse, "kebab-case") {
		t.Error("muse missing expected content")
	}
	// 2 sessions * 2 observe steps (extract + refine) + 1 learn = 5 calls
	if len(llm.Calls) != 5 {
		t.Errorf("LLM calls = %d, want 5", len(llm.Calls))
	}
}

func TestMapReduce_NoConversations(t *testing.T) {
	store := testutil.NewConversationStore()
	llm := &testutil.MockLLM{}

	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Processed != 0 {
		t.Errorf("Processed = %d, want 0", result.Processed)
	}
	if len(llm.Calls) != 0 {
		t.Errorf("LLM calls = %d, want 0", len(llm.Calls))
	}
}

func TestMapReduce_Limit(t *testing.T) {
	store := testutil.NewConversationStore()
	for i := 0; i < 5; i++ {
		store.AddSession("test", fmt.Sprintf("sess-%d", i), time.Now(), []conversation.Message{
			{Role: "user", Content: fmt.Sprintf("message %d", i)},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "follow up"},
			{Role: "assistant", Content: "ok again"},
		})
	}

	llm := &testutil.MockLLM{
		ObserveResponse: "- observation",
		LearnResponse:   "## Test\n\nContent here.",
	}

	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{Limit: 2})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if result.Remaining != 3 {
		t.Errorf("Remaining = %d, want 3", result.Remaining)
	}
}

func TestMapReduce_LimitIncludesPreviousObservations(t *testing.T) {
	store := testutil.NewConversationStore()
	for i := 0; i < 4; i++ {
		store.AddSession("test", fmt.Sprintf("sess-%d", i), time.Now(), []conversation.Message{
			{Role: "user", Content: fmt.Sprintf("message %d", i)},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "follow up"},
			{Role: "assistant", Content: "ok again"},
		})
	}

	llm := &testutil.MockLLM{
		ObserveResponse: "- observation",
		LearnResponse:   "## Test\n\nContent here.",
	}

	// First run: limit to 2
	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{Limit: 2})
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	if result.Processed != 2 {
		t.Errorf("first run Processed = %d, want 2", result.Processed)
	}
	if result.Remaining != 2 {
		t.Errorf("first run Remaining = %d, want 2", result.Remaining)
	}

	// Second run: limit to 2 again, should observe 2 more and learn from all 4
	llm.Calls = nil
	result, err = distill.Run(context.Background(), store, llm, llm, distill.Options{Limit: 2})
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	if result.Processed != 2 {
		t.Errorf("second run Processed = %d, want 2", result.Processed)
	}
	if result.Remaining != 0 {
		t.Errorf("second run Remaining = %d, want 0", result.Remaining)
	}
	if len(store.Observations) != 4 {
		t.Errorf("observations after second run = %d, want 4", len(store.Observations))
	}
	// The learn call (second-to-last, before diff) should contain all 4 observations
	learnInput := llm.Calls[len(llm.Calls)-2].User
	separators := strings.Count(learnInput, "---")
	if separators < 3 {
		t.Errorf("learn input has %d separators, want at least 3 (all 4 observations)", separators)
	}
}

func TestMapReduce_EmptyConversation(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddSession("test", "empty", time.Now(), []conversation.Message{
		{Role: "user", Content: ""},
		{Role: "assistant", Content: ""},
	})

	llm := &testutil.MockLLM{}

	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Processed != 0 {
		t.Errorf("Processed = %d, want 0", result.Processed)
	}
}

func TestMapReduce_ObserveError(t *testing.T) {
	store := twoSessionStore()
	llm := &contentFailLLM{
		failOn:          "use spaces",
		observeResponse: "- observation from good session",
		learnResponse:   "## Muse\n\nContent.",
	}

	// Inject a session that will trigger the LLM failure
	store.AddSession("test", "bad", time.Now(), []conversation.Message{
		{Role: "user", Content: "use spaces"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "always"},
		{Role: "assistant", Content: "sure"},
	})

	_, err := distill.Run(context.Background(), store, llm, llm, distill.Options{})
	if err == nil {
		t.Fatal("expected error from LLM failure, got nil")
	}
}

func TestMapReduce_Reobserve(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddSession("test", "sess-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "one more thing"},
		{Role: "assistant", Content: "sure"},
	})

	llm := &testutil.MockLLM{
		ObserveResponse: "- observation",
		LearnResponse:   "## Test\n\nContent.",
	}

	// First run
	_, err := distill.Run(context.Background(), store, llm, llm, distill.Options{})
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	// With Reobserve, it should process again
	llm.Calls = nil
	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{Reobserve: true})
	if err != nil {
		t.Fatalf("reprocess Run() error: %v", err)
	}
	if result.Processed != 1 {
		t.Errorf("Processed = %d, want 1", result.Processed)
	}
}

func TestMapReduce_IncrementalPersist(t *testing.T) {
	store := twoSessionStore()
	llm := &testutil.MockLLM{
		ObserveResponse: "- observation",
		LearnResponse:   "## Test\n\nContent.",
	}

	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if len(store.Observations) != 2 {
		t.Errorf("Observations = %d, want 2", len(store.Observations))
	}
}

// ---------------------------------------------------------------------------
// Clustered Pipeline
// ---------------------------------------------------------------------------

func TestClustered_EndToEnd(t *testing.T) {
	store := twoSessionStore()
	mock := &clusterMockLLM{}
	root := t.TempDir()

	result, err := distill.RunClustered(
		context.Background(), store,
		mock, mock, mock, mock,
		distill.ClusteredOptions{ArtifactDir: root, Limit: 100},
	)
	if err != nil {
		t.Fatalf("RunClustered: %v", err)
	}

	if result.Muse == "" {
		t.Error("expected non-empty muse")
	}
	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if result.Usage.InputTokens == 0 {
		t.Error("expected non-zero input tokens")
	}

	// Verify artifacts
	artifacts := distill.NewArtifactStore(root)
	obsList, err := artifacts.ListObservations()
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(obsList) == 0 {
		t.Error("expected observation artifacts")
	}
	clsList, err := artifacts.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(clsList) == 0 {
		t.Error("expected label artifacts")
	}
}

func TestClustered_CacheHit(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddSession("test", "s1", time.Now(), []conversation.Message{
		{Role: "user", Content: "use tabs"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "no emojis"},
		{Role: "assistant", Content: "sure"},
	})

	mock := &clusterMockLLM{}
	root := t.TempDir()
	opts := distill.ClusteredOptions{ArtifactDir: root, Limit: 100}

	// First run
	_, err := distill.RunClustered(context.Background(), store, mock, mock, mock, mock, opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	callsBefore := len(mock.calls)

	// Second run should use cache
	_, err = distill.RunClustered(context.Background(), store, mock, mock, mock, mock, opts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	newCalls := len(mock.calls) - callsBefore
	if newCalls >= callsBefore {
		t.Errorf("expected fewer LLM calls on cache hit: first=%d, second=%d", callsBefore, newCalls)
	}
}

func TestClustered_NoConversations(t *testing.T) {
	store := testutil.NewConversationStore()
	mock := &clusterMockLLM{}
	root := t.TempDir()

	result, err := distill.RunClustered(
		context.Background(), store,
		mock, mock, mock, mock,
		distill.ClusteredOptions{ArtifactDir: root, Limit: 100},
	)
	if err != nil {
		t.Fatalf("RunClustered: %v", err)
	}
	if result.Processed != 0 {
		t.Errorf("Processed = %d, want 0", result.Processed)
	}
	if len(mock.calls) != 0 {
		t.Errorf("LLM calls = %d, want 0", len(mock.calls))
	}
}

func TestClustered_ObserveError(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddSession("test", "bad", time.Now(), []conversation.Message{
		{Role: "user", Content: "trigger failure"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "more input"},
		{Role: "assistant", Content: "sure"},
	})

	mock := &clusterMockLLM{failOnExtract: true}
	root := t.TempDir()

	_, err := distill.RunClustered(
		context.Background(), store,
		mock, mock, mock, mock,
		distill.ClusteredOptions{ArtifactDir: root, Limit: 100},
	)
	if err == nil {
		t.Fatal("expected error from LLM failure, got nil")
	}
}

func TestClustered_Limit(t *testing.T) {
	store := testutil.NewConversationStore()
	for i := 0; i < 5; i++ {
		store.AddSession("test", fmt.Sprintf("sess-%d", i), time.Now(), []conversation.Message{
			{Role: "user", Content: fmt.Sprintf("message %d", i)},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "follow up"},
			{Role: "assistant", Content: "ok again"},
		})
	}

	mock := &clusterMockLLM{}
	root := t.TempDir()

	result, err := distill.RunClustered(
		context.Background(), store,
		mock, mock, mock, mock,
		distill.ClusteredOptions{ArtifactDir: root, Limit: 2},
	)
	if err != nil {
		t.Fatalf("RunClustered: %v", err)
	}
	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if result.Remaining != 3 {
		t.Errorf("Remaining = %d, want 3", result.Remaining)
	}
}

// ---------------------------------------------------------------------------
// Test Doubles
// ---------------------------------------------------------------------------

// contentFailLLM fails observe calls when the user content contains failOn.
type contentFailLLM struct {
	failOn          string
	observeResponse string
	learnResponse   string
}

func (m *contentFailLLM) Converse(_ context.Context, system, user string, _ ...inference.ConverseOption) (string, inference.Usage, error) {
	usage := inference.Usage{InputTokens: 100, OutputTokens: 50}
	if strings.Contains(system, "distilling observations") {
		return m.learnResponse, usage, nil
	}
	if strings.Contains(user, m.failOn) {
		return "", inference.Usage{}, fmt.Errorf("simulated LLM failure")
	}
	return m.observeResponse, usage, nil
}

func (m *contentFailLLM) Model() string { return "content-fail-mock" }

func (m *contentFailLLM) ConverseStream(ctx context.Context, system, user string, fn inference.StreamFunc, opts ...inference.ConverseOption) (string, inference.Usage, error) {
	text, usage, err := m.Converse(ctx, system, user, opts...)
	if fn != nil && err == nil {
		fn(inference.StreamDelta{Text: text})
	}
	return text, usage, err
}

// clusterMockLLM dispatches based on system prompt content.
type clusterMockLLM struct {
	mu            sync.Mutex
	calls         []string
	failOnExtract bool
}

func (m *clusterMockLLM) Converse(_ context.Context, system, user string, _ ...inference.ConverseOption) (string, inference.Usage, error) {
	m.mu.Lock()
	m.calls = append(m.calls, system[:min(50, len(system))])
	m.mu.Unlock()
	usage := inference.Usage{InputTokens: 100, OutputTokens: 50}

	if m.failOnExtract && strings.Contains(system, "Extract observations") {
		return "", inference.Usage{}, fmt.Errorf("simulated extract failure")
	}
	if strings.Contains(system, "Label each") {
		return "explicit patterns over implicit conventions", usage, nil
	}
	if strings.Contains(system, "Summarize these") {
		return "I prefer explicit, clear patterns in code.", usage, nil
	}
	if strings.Contains(system, "producing muse.md") {
		return "# How I Think\n\nI value explicitness over cleverness.", usage, nil
	}
	if strings.Contains(system, "distilling observations") {
		return "# Muse\n\nValues clarity.", usage, nil
	}
	// Refine: pass through observations as-is
	if strings.Contains(system, "Filter candidate") {
		return user, usage, nil
	}

	return "Observation: Prefers tabs over spaces\nObservation: Values explicit error handling\nObservation: Tests before shipping", usage, nil
}

func (m *clusterMockLLM) Model() string { return "cluster-mock-model" }

func (m *clusterMockLLM) ConverseStream(ctx context.Context, system, user string, fn inference.StreamFunc, opts ...inference.ConverseOption) (string, inference.Usage, error) {
	text, usage, err := m.Converse(ctx, system, user, opts...)
	if fn != nil && err == nil {
		fn(inference.StreamDelta{Text: text})
	}
	return text, usage, err
}
