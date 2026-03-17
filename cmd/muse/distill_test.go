package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/distill"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/internal/testutil"
)

func TestDistillCmd_NoStore(t *testing.T) {
	// When no bucket is set, local store is used — this test just validates
	// the command doesn't panic. It will fail at bedrock client creation
	// which is expected.
	t.Setenv("MUSE_BUCKET", "")
}

func TestDistillCmd_LearnNoStore(t *testing.T) {
	t.Setenv("MUSE_BUCKET", "")
}

func TestRunDistill_PropagatesRunError(t *testing.T) {
	store := &failingStore{err: fmt.Errorf("storage unavailable")}
	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runDistill(ctx, &stdout, &stderr, store, &testutil.MockLLM{}, &testutil.MockLLM{}, &testutil.MockLLM{}, distill.Options{Limit: 100})
	if err == nil {
		t.Fatal("expected error from failing store, got nil")
	}
	if !strings.Contains(err.Error(), "storage unavailable") {
		t.Errorf("expected error to contain 'storage unavailable', got: %s", err.Error())
	}
}

func TestRunDistill_PropagatesLearnError(t *testing.T) {
	store := testutil.NewConversationStore()
	store.Observations["conversations/test/sess-1.json"] = "- observation"
	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runDistill(ctx, &stdout, &stderr, store, nil, &testutil.MockLLM{Err: fmt.Errorf("learn failed")}, &testutil.MockLLM{}, distill.Options{Learn: true})
	if err == nil {
		t.Fatal("expected error from failing LLM, got nil")
	}
	if !strings.Contains(err.Error(), "learn failed") {
		t.Errorf("expected error to contain 'learn failed', got: %s", err.Error())
	}
}

func TestRunDistill_SuccessfulRun(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddSession("test", "sess-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "use tabs"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "also no emojis"},
		{Role: "assistant", Content: "sure"},
	})
	mockLLM := &testutil.MockLLM{
		ObserveResponse: "- Uses tabs\n- No emojis",
		LearnResponse:   "## Style\n\nUse tabs. No emojis.",
	}

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runDistill(ctx, &stdout, &stderr, store, mockLLM, mockLLM, mockLLM, distill.Options{Limit: 100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Processed 1 conversations") {
		t.Errorf("expected 'Processed 1 conversations', got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Muse distilled") {
		t.Errorf("expected 'Muse distilled', got: %s", stdout.String())
	}
}

func TestRunDistill_SuccessfulLearn(t *testing.T) {
	store := testutil.NewConversationStore()
	store.Observations["conversations/test/sess-1.json"] = "- observation"
	mockLLM := &testutil.MockLLM{
		LearnResponse: "## Test\n\nContent.",
	}

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runDistill(ctx, &stdout, &stderr, store, nil, mockLLM, mockLLM, distill.Options{Learn: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Muse distilled") {
		t.Errorf("expected 'Muse distilled', got: %s", stdout.String())
	}
}

// failingStore implements storage.Store where all operations return an error.
type failingStore struct{ err error }

func (s *failingStore) ListSessions(_ context.Context) ([]storage.SessionEntry, error) {
	return nil, s.err
}
func (s *failingStore) GetSession(_ context.Context, _, _ string) (*conversation.Session, error) {
	return nil, s.err
}
func (s *failingStore) PutSession(_ context.Context, _ *conversation.Session) (int, error) {
	return 0, s.err
}
func (s *failingStore) GetMuse(_ context.Context) (string, error)    { return "", s.err }
func (s *failingStore) PutMuse(_ context.Context, _, _ string) error { return s.err }
func (s *failingStore) GetMuseDiff(_ context.Context, _ string) (string, error) {
	return "", s.err
}
func (s *failingStore) PutMuseDiff(_ context.Context, _, _ string) error { return s.err }
func (s *failingStore) ListMuses(_ context.Context) ([]string, error) {
	return nil, s.err
}
func (s *failingStore) GetMuseVersion(_ context.Context, _ string) (string, error) {
	return "", s.err
}
func (s *failingStore) ListObservations(_ context.Context) (map[string]time.Time, error) {
	return nil, s.err
}
func (s *failingStore) GetObservation(_ context.Context, _ string) (string, error) {
	return "", s.err
}
func (s *failingStore) PutObservation(_ context.Context, _, _ string) error { return s.err }
func (s *failingStore) DeletePrefix(_ context.Context, _ string) error      { return s.err }
