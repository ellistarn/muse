package compose

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
)

// scriptedLLM is a minimal inference.Client that returns a sequence of
// (text, error) responses across calls. Used to script truncation behavior
// in retry tests.
type scriptedLLM struct {
	mu        sync.Mutex
	responses []scriptedResponse
	calls     atomic.Int32
}

type scriptedResponse struct {
	text string
	err  error
}

func (m *scriptedLLM) ConverseMessages(_ context.Context, _ string, _ []inference.Message, _ ...inference.ConverseOption) (*inference.Response, error) {
	idx := int(m.calls.Add(1)) - 1
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx >= len(m.responses) {
		return &inference.Response{}, nil
	}
	r := m.responses[idx]
	return &inference.Response{Text: r.text, Usage: inference.Usage{}}, r.err
}

func (m *scriptedLLM) ConverseMessagesStream(ctx context.Context, system string, messages []inference.Message, _ inference.StreamFunc, opts ...inference.ConverseOption) (*inference.Response, error) {
	return m.ConverseMessages(ctx, system, messages, opts...)
}

func (m *scriptedLLM) Model() string { return "scripted" }

// twoTurnConversation builds a minimal conversation with two user turns
// (the minimum for AI sources to pass extractTurns) so observeWoo produces a
// single window with one observe call (plus refine when observations exist).
func twoTurnConversation() *conversation.Conversation {
	return &conversation.Conversation{
		Source: "test",
		Messages: []conversation.Message{
			{Role: "user", Content: "the owner says something distinctive"},
			{Role: "assistant", Content: "ack"},
			{Role: "user", Content: "and follows up with another distinctive thing"},
			{Role: "assistant", Content: "ack"},
		},
	}
}

func TestObserveWindowRetriesOnTruncation(t *testing.T) {
	mock := &scriptedLLM{
		responses: []scriptedResponse{
			{text: "", err: &inference.TruncatedError{OutputTokens: windowObserveBudget}},
			{text: "Observation: distinctive thinking pattern\n", err: nil},
		},
	}

	obs, _, err := observeWindow(context.Background(), mock, "prompt", "input")
	if err != nil {
		t.Fatalf("observeWindow returned error: %v", err)
	}
	if obs == "" {
		t.Errorf("expected observations after retry, got empty")
	}
	if got, want := mock.calls.Load(), int32(2); got != want {
		t.Errorf("call count = %d, want %d (initial + retry)", got, want)
	}
}

func TestObserveWindowReturnsTruncationAfterRetryFails(t *testing.T) {
	mock := &scriptedLLM{
		responses: []scriptedResponse{
			{text: "", err: &inference.TruncatedError{OutputTokens: windowObserveBudget}},
			{text: "", err: &inference.TruncatedError{OutputTokens: windowObserveRetryBudget}},
		},
	}

	_, _, err := observeWindow(context.Background(), mock, "prompt", "input")
	if !inference.IsTruncated(err) {
		t.Errorf("expected TruncatedError after persistent truncation, got %v", err)
	}
	if got, want := mock.calls.Load(), int32(2); got != want {
		t.Errorf("call count = %d, want %d (no third attempt)", got, want)
	}
}

func TestObserveWindowDoesNotRetryOnNonTruncationError(t *testing.T) {
	mock := &scriptedLLM{
		responses: []scriptedResponse{
			{text: "", err: &nonTruncationError{}},
		},
	}

	_, _, err := observeWindow(context.Background(), mock, "prompt", "input")
	if err == nil {
		t.Fatal("expected non-truncation error to propagate, got nil")
	}
	if got, want := mock.calls.Load(), int32(1); got != want {
		t.Errorf("call count = %d, want %d (no retry for non-truncation errors)", got, want)
	}
}

func TestObserveWooSkipsWindowOnPersistentTruncation(t *testing.T) {
	// Verifies the integration: observeWoo treats a persistently-truncated
	// window as a skip rather than aborting the conversation. Mock script:
	// initial truncates, retry truncates → window skipped → no candidates
	// → observeWoo returns nil with no error and no refine call.
	mock := &scriptedLLM{
		responses: []scriptedResponse{
			{text: "", err: &inference.TruncatedError{OutputTokens: windowObserveBudget}},
			{text: "", err: &inference.TruncatedError{OutputTokens: windowObserveRetryBudget}},
		},
	}

	obs, _, err := observeWoo(context.Background(), mock, twoTurnConversation(), false, nil)
	if err != nil {
		t.Fatalf("observeWoo returned error on skip-after-retry: %v", err)
	}
	if got, want := mock.calls.Load(), int32(2); got != want {
		t.Errorf("call count = %d, want %d (initial + retry, no refine because no candidates)", got, want)
	}
	if len(obs) != 0 {
		t.Errorf("expected no observations when sole window skipped, got %d", len(obs))
	}
}

type nonTruncationError struct{}

func (e *nonTruncationError) Error() string { return "non-truncation network error" }
