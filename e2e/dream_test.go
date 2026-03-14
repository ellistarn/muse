package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/dream"
	"github.com/ellistarn/muse/internal/memory"
	"github.com/ellistarn/muse/internal/testutil"
)

func TestDreamPipeline(t *testing.T) {
	store := testutil.NewMemoryStore()
	store.AddSession("claude-code", "sess-1", time.Now(), []memory.Message{
		{Role: "user", Content: "use kebab-case for file names"},
		{Role: "assistant", Content: "OK, I'll rename them."},
		{Role: "user", Content: "also use lowercase"},
		{Role: "assistant", Content: "Done."},
	})
	store.AddSession("claude-code", "sess-2", time.Now(), []memory.Message{
		{Role: "user", Content: "never use emojis in commit messages"},
		{Role: "assistant", Content: "Understood."},
		{Role: "user", Content: "and keep them short"},
		{Role: "assistant", Content: "Will do."},
	})

	llm := &testutil.MockLLM{
		ReflectResponse: "- Prefers kebab-case file names\n- No emojis in commits",
		LearnResponse:   "## Naming\n\nI use kebab-case for file names.\n\n## Commits\n\nNo emojis. Keep them short.",
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

	// Verify muse was written
	if store.Muse == "" {
		t.Error("muse not written to store")
	}
	if !strings.Contains(store.Muse, "kebab-case") {
		t.Error("muse missing expected content")
	}

	// Verify LLM was called: 2 sessions * 3 reflect steps (summarize + extract + refine) + 1 learn = 7 calls
	if len(llm.Calls) != 7 {
		t.Errorf("LLM calls = %d, want 7", len(llm.Calls))
	}
}

func TestDreamPipelineNoMemories(t *testing.T) {
	store := testutil.NewMemoryStore()
	llm := &testutil.MockLLM{}

	result, err := dream.Run(context.Background(), store, llm, llm, dream.Options{})
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

func TestDreamPipelineLimit(t *testing.T) {
	store := testutil.NewMemoryStore()
	for i := 0; i < 5; i++ {
		store.AddSession("test", fmt.Sprintf("sess-%d", i), time.Now(), []memory.Message{
			{Role: "user", Content: fmt.Sprintf("message %d", i)},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "follow up"},
			{Role: "assistant", Content: "ok again"},
		})
	}

	llm := &testutil.MockLLM{
		ReflectResponse: "- observation",
		LearnResponse:   "## Test\n\nContent here.",
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
	if len(llm.Calls) != 7 {
		t.Errorf("LLM calls = %d, want 7 (2 sessions * 3 reflect steps + 1 learn)", len(llm.Calls))
	}
}

func TestDreamPipelineLimitIncludesPreviousReflections(t *testing.T) {
	store := testutil.NewMemoryStore()
	for i := 0; i < 4; i++ {
		store.AddSession("test", fmt.Sprintf("sess-%d", i), time.Now(), []memory.Message{
			{Role: "user", Content: fmt.Sprintf("message %d", i)},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "follow up"},
			{Role: "assistant", Content: "ok again"},
		})
	}

	llm := &testutil.MockLLM{
		ReflectResponse: "- observation",
		LearnResponse:   "## Test\n\nContent here.",
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
	firstRunReflections := len(store.Reflections)
	if firstRunReflections != 2 {
		t.Errorf("reflections after first run = %d, want 2", firstRunReflections)
	}

	// Second run: limit to 2 again, should reflect 2 more and learn from all 4
	llm.Calls = nil
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
	if len(store.Reflections) != 4 {
		t.Errorf("reflections after second run = %d, want 4", len(store.Reflections))
	}
	// 2 sessions * 3 reflect steps + 1 learn = 7, and learn should have received all 4 reflections
	if len(llm.Calls) != 7 {
		t.Errorf("second run LLM calls = %d, want 7", len(llm.Calls))
	}
	// The learn call (last one) should contain all 4 observations joined by ---
	learnInput := llm.Calls[len(llm.Calls)-1].User
	separators := strings.Count(learnInput, "---")
	// 4 observations joined by "---" = 3 separators (in the join delimiters)
	if separators < 3 {
		t.Errorf("learn input has %d separators, want at least 3 (all 4 reflections)", separators)
	}
}

func TestDreamPipelineEmptyConversation(t *testing.T) {
	store := testutil.NewMemoryStore()
	// Session with only empty messages produces no observations
	store.AddSession("test", "empty", time.Now(), []memory.Message{
		{Role: "user", Content: ""},
		{Role: "assistant", Content: ""},
	})

	llm := &testutil.MockLLM{}

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
	store := testutil.NewMemoryStore()
	store.AddSession("test", "sess-1", time.Now(), []memory.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "one more thing"},
		{Role: "assistant", Content: "sure"},
	})

	llm := &testutil.MockLLM{
		ReflectResponse: "- observation",
		LearnResponse:   "## Test\n\nContent.",
	}

	// First run
	_, err := dream.Run(context.Background(), store, llm, llm, dream.Options{})
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	// With Reprocess, it should process again even though state would normally prune it
	llm.Calls = nil
	result, err := dream.Run(context.Background(), store, llm, llm, dream.Options{Reflect: true})
	if err != nil {
		t.Fatalf("reprocess Run() error: %v", err)
	}
	if result.Processed != 1 {
		t.Errorf("Processed = %d, want 1", result.Processed)
	}
}
