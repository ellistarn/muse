package openai

import (
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
