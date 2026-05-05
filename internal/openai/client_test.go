package openai

import (
	"errors"
	"testing"

	sdkopenai "github.com/openai/openai-go"

	"github.com/ellistarn/muse/internal/inference"
)

func TestBuildParamsIgnoresThinkingBudgetForNonReasoningModels(t *testing.T) {
	client := &Client{model: ModelFull}
	opts := inference.Apply([]inference.ConverseOption{inference.WithThinking(16000)})

	params := client.buildParams("system", []inference.Message{{Role: "user", Content: "hello"}}, opts)

	if !params.MaxCompletionTokens.Valid() {
		t.Fatal("MaxCompletionTokens should be set")
	}
	// Non-reasoning models don't get thinking budget added.
	if got, want := params.MaxCompletionTokens.Value, int64(inference.DefaultMaxTokens); got != want {
		t.Fatalf("MaxCompletionTokens = %d, want %d", got, want)
	}
	if params.ReasoningEffort != "" {
		t.Fatalf("ReasoningEffort = %q, want empty for non-reasoning model", params.ReasoningEffort)
	}
}

func TestBuildParamsSetsReasoningEffortForReasoningModels(t *testing.T) {
	client := &Client{model: "o3"}
	opts := inference.Apply([]inference.ConverseOption{inference.WithThinking(8000)})

	params := client.buildParams("system", []inference.Message{{Role: "user", Content: "hello"}}, opts)

	if got, want := params.ReasoningEffort, sdkopenai.ReasoningEffortMedium; got != want {
		t.Fatalf("ReasoningEffort = %q, want %q", got, want)
	}
	if got, want := params.MaxCompletionTokens.Value, int64(inference.DefaultMaxTokens+8000); got != want {
		t.Fatalf("MaxCompletionTokens = %d, want %d", got, want)
	}
}

func TestClassifyContextSizeErrorLlamaCpp(t *testing.T) {
	// llama.cpp's HTTP 400 body shape (the actual error the user hit).
	raw := errors.New(`POST "http://127.0.0.1:8080/v1/chat/completions": 400 Bad Request {"code":400,"message":"request (23956 tokens) exceeds the available context size (16384 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":23956,"n_ctx":16384}`)
	cse, ok := classifyContextSizeError(raw)
	if !ok {
		t.Fatalf("expected llama.cpp context-size error to classify; raw: %v", raw)
	}
	if got, want := cse.PromptTokens, 23956; got != want {
		t.Errorf("PromptTokens = %d, want %d", got, want)
	}
	if got, want := cse.ContextSize, 16384; got != want {
		t.Errorf("ContextSize = %d, want %d", got, want)
	}
}

func TestClassifyContextSizeErrorOpenAI(t *testing.T) {
	raw := errors.New(`This model's maximum context length is 8192 tokens, however you requested 12000 tokens (context_length_exceeded)`)
	cse, ok := classifyContextSizeError(raw)
	if !ok {
		t.Fatalf("expected OpenAI-style context-size error to classify; raw: %v", raw)
	}
	// OpenAI's text doesn't carry the JSON fields, so token counts default to 0.
	if cse.PromptTokens != 0 || cse.ContextSize != 0 {
		t.Errorf("expected zero token fields when not parseable; got PromptTokens=%d ContextSize=%d", cse.PromptTokens, cse.ContextSize)
	}
}

func TestClassifyContextSizeErrorIgnoresUnrelated(t *testing.T) {
	for _, msg := range []string{
		"connection refused",
		"401 Unauthorized",
		"response truncated: hit max token limit (4096 output tokens)",
	} {
		if _, ok := classifyContextSizeError(errors.New(msg)); ok {
			t.Errorf("classifyContextSizeError matched unrelated error: %q", msg)
		}
	}
}
