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

func TestDistillPipeline(t *testing.T) {
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
	if len(result.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", result.Warnings)
	}

	// Verify muse was written
	if store.Muse == "" {
		t.Error("muse not written to store")
	}
	if !strings.Contains(store.Muse, "kebab-case") {
		t.Error("muse missing expected content")
	}

	// Verify LLM was called: 2 sessions * 3 observe steps (summarize + extract + refine) + 1 learn = 7 calls
	// No diff call on first run (no previous muse to compare against).
	if len(llm.Calls) != 7 {
		t.Errorf("LLM calls = %d, want 7", len(llm.Calls))
	}
}

func TestDistillPipelineNoMemories(t *testing.T) {
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

func TestDistillPipelineLimit(t *testing.T) {
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
	// 2 sessions * 3 observe steps + 1 learn = 7 (no diff on first run)
	if len(llm.Calls) != 7 {
		t.Errorf("LLM calls = %d, want 7 (2 sessions * 3 observe steps + 1 learn)", len(llm.Calls))
	}
}

func TestDistillPipelineLimitIncludesPreviousObservations(t *testing.T) {
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

	// First run: limit to 2, should observe 2 and learn from 2
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
	firstRunObservations := len(store.Observations)
	if firstRunObservations != 2 {
		t.Errorf("observations after first run = %d, want 2", firstRunObservations)
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
	// 2 sessions * 3 observe steps + 1 learn + 1 diff = 8 (diff runs because previous muse exists)
	if len(llm.Calls) != 8 {
		t.Errorf("second run LLM calls = %d, want 8", len(llm.Calls))
	}
	// The learn call (second-to-last, before diff) should contain all 4 observations joined by ---
	learnInput := llm.Calls[len(llm.Calls)-2].User
	separators := strings.Count(learnInput, "---")
	// 4 observations joined by "---" = 3 separators (in the join delimiters)
	if separators < 3 {
		t.Errorf("learn input has %d separators, want at least 3 (all 4 observations)", separators)
	}
}

func TestDistillPipelineEmptyConversation(t *testing.T) {
	store := testutil.NewConversationStore()
	// Session with only empty messages produces no observations
	store.AddSession("test", "empty", time.Now(), []conversation.Message{
		{Role: "user", Content: ""},
		{Role: "assistant", Content: ""},
	})

	llm := &testutil.MockLLM{}

	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// Empty conversation produces no observe call, but still shows up in pending
	if result.Processed != 0 {
		t.Errorf("Processed = %d, want 0", result.Processed)
	}
}

func TestDistillPipelineIncrementalPersist(t *testing.T) {
	// Verify that successful observations are persisted even when other sessions
	// fail during the observe phase. Before incremental persist, all observations
	// were batched after wg.Wait(), so a cancelled context would lose everything.
	store := testutil.NewConversationStore()
	store.AddSession("test", "good-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "use tabs"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "always"},
		{Role: "assistant", Content: "sure"},
	})
	store.AddSession("test", "good-2", time.Now(), []conversation.Message{
		{Role: "user", Content: "use spaces"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "always"},
		{Role: "assistant", Content: "sure"},
	})

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
	// Both observations should be persisted individually
	if len(store.Observations) != 2 {
		t.Errorf("Observations = %d, want 2", len(store.Observations))
	}
	for key, content := range store.Observations {
		if content == "" {
			t.Errorf("Observation %s is empty", key)
		}
	}
}

func TestDistillPipelinePartialFailurePersistsSuccesses(t *testing.T) {
	// When the LLM fails for one session, already-completed observations should
	// still be persisted (not lost due to batching).
	store := testutil.NewConversationStore()
	store.AddSession("test", "good", time.Now(), []conversation.Message{
		{Role: "user", Content: "use tabs"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "always"},
		{Role: "assistant", Content: "sure"},
	})
	store.AddSession("test", "bad", time.Now(), []conversation.Message{
		{Role: "user", Content: "use spaces"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "always"},
		{Role: "assistant", Content: "sure"},
	})

	// Use an LLM that fails when processing the "bad" session.
	llm := &contentFailLLM{
		failOn:          "use spaces",
		observeResponse: "- observation from good session",
		learnResponse:   "## Muse\n\nContent.",
	}

	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// One session succeeded, one failed
	if result.Processed != 1 {
		t.Errorf("Processed = %d, want 1", result.Processed)
	}
	if len(result.Warnings) != 1 {
		t.Errorf("Warnings = %d, want 1", len(result.Warnings))
	}
	// The successful session's observation should be persisted
	if len(store.Observations) != 1 {
		t.Errorf("Observations = %d, want 1", len(store.Observations))
	}
}

func TestDistillPipelineCancelPreservesCompleted(t *testing.T) {
	// Verify that observations completed before context cancellation are persisted.
	store := testutil.NewConversationStore()
	store.AddSession("test", "fast", time.Now().Add(-time.Second), []conversation.Message{
		{Role: "user", Content: "use tabs"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "always"},
		{Role: "assistant", Content: "sure"},
	})
	store.AddSession("test", "slow", time.Now(), []conversation.Message{
		{Role: "user", Content: "use spaces"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "always"},
		{Role: "assistant", Content: "sure"},
	})

	// LLM that cancels context once the first session is fully observed
	ctx, cancel := context.WithCancel(context.Background())
	llm := &cancelAfterNObserveLLM{
		succeedN:        3, // let first session complete (summarize + extract + refine)
		observeResponse: "- observation",
		learnResponse:   "## Muse",
		cancel:          cancel,
	}

	// Run will likely error because context is cancelled during second session
	result, _ := distill.Run(ctx, store, llm, llm, distill.Options{})

	// The first session's observation should be persisted even though context was cancelled
	if len(store.Observations) < 1 {
		t.Errorf("Observations = %d, want at least 1 (completed before cancel)", len(store.Observations))
	}
	// Should have at least some warnings from the cancelled session
	if result != nil && result.Processed < 1 {
		t.Errorf("Processed = %d, want at least 1", result.Processed)
	}
}

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

// cancelAfterNObserveLLM succeeds for the first N observe calls, then cancels context.
type cancelAfterNObserveLLM struct {
	succeedN        int
	observeResponse string
	learnResponse   string
	cancel          context.CancelFunc
	mu              sync.Mutex
	observeCalls    int
}

func (m *cancelAfterNObserveLLM) Converse(ctx context.Context, system, user string, _ ...inference.ConverseOption) (string, inference.Usage, error) {
	usage := inference.Usage{InputTokens: 100, OutputTokens: 50}
	if strings.Contains(system, "distilling observations") {
		return m.learnResponse, usage, nil
	}
	m.mu.Lock()
	m.observeCalls++
	n := m.observeCalls
	m.mu.Unlock()
	if n > m.succeedN {
		m.cancel()
		return "", inference.Usage{}, ctx.Err()
	}
	return m.observeResponse, usage, nil
}

func TestDistillPipelineReobserve(t *testing.T) {
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

	// With Reobserve, it should process again even though state would normally prune it
	llm.Calls = nil
	result, err := distill.Run(context.Background(), store, llm, llm, distill.Options{Reobserve: true})
	if err != nil {
		t.Fatalf("reprocess Run() error: %v", err)
	}
	if result.Processed != 1 {
		t.Errorf("Processed = %d, want 1", result.Processed)
	}
}
