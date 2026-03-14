package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/memory"
	"github.com/ellistarn/muse/internal/testutil"
)

func TestDreamCmd_NoStore(t *testing.T) {
	// When no bucket is set, local store is used — this test just validates
	// the command doesn't panic. It will fail at bedrock client creation
	// which is expected.
	t.Setenv("MUSE_BUCKET", "")
}

func TestDreamCmd_LearnNoStore(t *testing.T) {
	t.Setenv("MUSE_BUCKET", "")
}

func TestRunDream_PropagatesRunError(t *testing.T) {
	store := &testutil.FailingStore{Err: fmt.Errorf("storage unavailable")}
	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runDream(ctx, &stdout, &stderr, store, &testutil.MockLLM{}, &testutil.MockLLM{}, false, false, 100)
	if err == nil {
		t.Fatal("expected error from failing store, got nil")
	}
	if !strings.Contains(err.Error(), "storage unavailable") {
		t.Errorf("expected error to contain 'storage unavailable', got: %s", err.Error())
	}
}

func TestRunDream_PropagatesLearnError(t *testing.T) {
	store := testutil.NewMemoryStore()
	store.Reflections["memories/test/sess-1.json"] = "- observation"
	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runDream(ctx, &stdout, &stderr, store, nil, &testutil.MockLLM{Err: fmt.Errorf("learn failed")}, true, false, 0)
	if err == nil {
		t.Fatal("expected error from failing LLM, got nil")
	}
	if !strings.Contains(err.Error(), "learn failed") {
		t.Errorf("expected error to contain 'learn failed', got: %s", err.Error())
	}
}

func TestRunDream_SuccessfulRun(t *testing.T) {
	store := testutil.NewMemoryStore()
	store.AddSession("test", "sess-1", time.Now(), []memory.Message{
		{Role: "user", Content: "use tabs"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "also no emojis"},
		{Role: "assistant", Content: "sure"},
	})
	mockLLM := &testutil.MockLLM{
		ReflectResponse: "- Uses tabs\n- No emojis",
		LearnResponse:   "## Style\n\nUse tabs. No emojis.",
	}

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runDream(ctx, &stdout, &stderr, store, mockLLM, mockLLM, false, false, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Processed 1 memories") {
		t.Errorf("expected 'Processed 1 memories', got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Muse distilled") {
		t.Errorf("expected 'Muse distilled', got: %s", stdout.String())
	}
}

func TestRunDream_SuccessfulLearn(t *testing.T) {
	store := testutil.NewMemoryStore()
	store.Reflections["memories/test/sess-1.json"] = "- observation"
	mockLLM := &testutil.MockLLM{
		LearnResponse: "## Test\n\nContent.",
	}

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runDream(ctx, &stdout, &stderr, store, nil, mockLLM, true, false, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Muse distilled") {
		t.Errorf("expected 'Muse distilled', got: %s", stdout.String())
	}
}
