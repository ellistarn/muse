package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/internal/testutil"
)

func TestComposeCmd_NoStore(t *testing.T) {
	// When no bucket is set, local store is used — this test just validates
	// the command doesn't panic. It will fail at bedrock client creation
	// which is expected.
	t.Setenv("MUSE_BUCKET", "")
}

func TestComposeCmd_LearnNoStore(t *testing.T) {
	t.Setenv("MUSE_BUCKET", "")
}

func TestRunCompose_PropagatesRunError(t *testing.T) {
	store := &failingStore{err: fmt.Errorf("storage unavailable")}
	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runCompose(ctx, &stdout, &stderr, store, &testutil.MockLLM{}, &testutil.MockLLM{}, compose.Options{BaseOptions: compose.BaseOptions{Limit: 100}})
	if err == nil {
		t.Fatal("expected error from failing store, got nil")
	}
	if !strings.Contains(err.Error(), "storage unavailable") {
		t.Errorf("expected error to contain 'storage unavailable', got: %s", err.Error())
	}
}

func TestRunCompose_PropagatesLearnError(t *testing.T) {
	store := testutil.NewConversationStore()
	// Seed an observation via the shared JSON artifact path
	compose.PutObservations(context.Background(), store, "test", "conv-1", &compose.Observations{
		Items: []compose.Observation{{Text: "observation"}},
	})
	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runCompose(ctx, &stdout, &stderr, store, nil, &testutil.MockLLM{Err: fmt.Errorf("learn failed")}, compose.Options{Learn: true})
	if err == nil {
		t.Fatal("expected error from failing LLM, got nil")
	}
	if !strings.Contains(err.Error(), "learn failed") {
		t.Errorf("expected error to contain 'learn failed', got: %s", err.Error())
	}
}

func TestRunCompose_SuccessfulRun(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddConversation("test", "conv-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "use tabs"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "also no emojis"},
		{Role: "assistant", Content: "sure"},
	})
	mockLLM := &testutil.MockLLM{
		ObserveResponse: "Observation: Uses tabs\nObservation: No emojis",
		LearnResponse:   "## Style\n\nUse tabs. No emojis.",
	}

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runCompose(ctx, &stdout, &stderr, store, mockLLM, mockLLM, compose.Options{BaseOptions: compose.BaseOptions{Limit: 100}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Processed 1 conversations") {
		t.Errorf("expected 'Processed 1 conversations', got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Muse composed") {
		t.Errorf("expected 'Muse composed', got: %s", stdout.String())
	}
}

func TestRunCompose_SuccessfulLearn(t *testing.T) {
	store := testutil.NewConversationStore()
	compose.PutObservations(context.Background(), store, "test", "conv-1", &compose.Observations{
		Items: []compose.Observation{{Text: "observation"}},
	})
	mockLLM := &testutil.MockLLM{
		LearnResponse: "## Test\n\nContent.",
	}

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	err := runCompose(ctx, &stdout, &stderr, store, nil, mockLLM, compose.Options{Learn: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Muse composed") {
		t.Errorf("expected 'Muse composed', got: %s", stdout.String())
	}
}

// failingStore implements storage.Store where all operations return an error.
type failingStore struct{ err error }

func (s *failingStore) ListConversations(_ context.Context) ([]storage.ConversationEntry, error) {
	return nil, s.err
}
func (s *failingStore) GetConversation(_ context.Context, _, _ string) (*conversation.Conversation, error) {
	return nil, s.err
}
func (s *failingStore) PutConversation(_ context.Context, _ *conversation.Conversation) (int, error) {
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
func (s *failingStore) DeletePrefix(_ context.Context, _ string) error      { return s.err }
func (s *failingStore) PutData(_ context.Context, _ string, _ []byte) error { return s.err }
func (s *failingStore) GetData(_ context.Context, _ string) ([]byte, error) { return nil, s.err }
func (s *failingStore) ListData(_ context.Context, _ string) ([]string, error) {
	return nil, s.err
}
