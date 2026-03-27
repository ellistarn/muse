package inference

import (
	"context"
	"errors"
	"testing"
)

type stubClient struct {
	resp *Response
	err  error
}

func (s stubClient) ConverseMessages(_ context.Context, _ string, _ []Message, _ ...ConverseOption) (*Response, error) {
	return s.resp, s.err
}

func (s stubClient) ConverseMessagesStream(_ context.Context, _ string, _ []Message, _ StreamFunc, _ ...ConverseOption) (*Response, error) {
	return s.resp, s.err
}

func (s stubClient) Model() string { return "stub" }

func TestConversePreservesPartialResponseOnError(t *testing.T) {
	wantErr := errors.New("response truncated")
	wantUsage := NewUsage(12, 34, 0.56)

	text, usage, err := Converse(context.Background(), stubClient{
		resp: &Response{Text: "partial output", Usage: wantUsage},
		err:  wantErr,
	}, "system", "user")

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if text != "partial output" {
		t.Fatalf("text = %q, want %q", text, "partial output")
	}
	if usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", usage, wantUsage)
	}
}

func TestConverseStreamPreservesPartialResponseOnError(t *testing.T) {
	wantErr := errors.New("response truncated")
	wantUsage := NewUsage(21, 43, 0.65)

	text, usage, err := ConverseStream(context.Background(), stubClient{
		resp: &Response{Text: "partial stream", Usage: wantUsage},
		err:  wantErr,
	}, "system", "user", nil)

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if text != "partial stream" {
		t.Fatalf("text = %q, want %q", text, "partial stream")
	}
	if usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", usage, wantUsage)
	}
}
