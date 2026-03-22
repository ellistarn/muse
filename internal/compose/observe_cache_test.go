package compose

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/testutil"
)

func TestRunObserve_CacheHitOnSecondPass(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddConversation("codex", "conv-1", time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), []conversation.Message{
		{Role: "user", Content: "Use smaller diffs."},
		{Role: "assistant", Content: "Will do."},
		{Role: "user", Content: "Also add a rollback plan for risky changes."},
		{Role: "assistant", Content: "Got it."},
	})

	llm := &testutil.MockLLM{
		ObserveResponse: "Observation: Prefers smaller diffs.\nObservation: Wants rollback plans for risky changes.",
	}

	var counter atomic.Int32
	first, err := runObserve(context.Background(), store, llm, ClusteredOptions{}, &counter)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.processed != 1 {
		t.Fatalf("first.processed = %d, want 1", first.processed)
	}
	if len(llm.Calls) == 0 {
		t.Fatal("first pass made no LLM calls")
	}
	callsAfterFirst := len(llm.Calls)

	counter.Store(0)
	second, err := runObserve(context.Background(), store, llm, ClusteredOptions{}, &counter)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.processed != 0 {
		t.Fatalf("second.processed = %d, want 0", second.processed)
	}
	if second.pruned != 1 {
		t.Fatalf("second.pruned = %d, want 1", second.pruned)
	}
	if len(llm.Calls) != callsAfterFirst {
		t.Fatalf("second pass made %d new LLM calls, want 0", len(llm.Calls)-callsAfterFirst)
	}
}
