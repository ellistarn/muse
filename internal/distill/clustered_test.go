package distill_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/distill"
	"github.com/ellistarn/muse/internal/embedding"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/internal/testutil"
)

func TestClusteredPipeline_EndToEnd(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddSession("test", "s1", time.Now(), []conversation.Message{
		{Role: "user", Content: "use tabs not spaces"},
		{Role: "assistant", Content: "ok using tabs"},
		{Role: "user", Content: "also prefer explicit error handling"},
		{Role: "assistant", Content: "will do"},
	})
	store.AddSession("test", "s2", time.Now(), []conversation.Message{
		{Role: "user", Content: "always test before shipping"},
		{Role: "assistant", Content: "good practice"},
		{Role: "user", Content: "and keep functions small"},
		{Role: "assistant", Content: "understood"},
	})

	mock := &clusterMockLLM{}
	embedder := embedding.NewMockEmbedder(64)
	root := t.TempDir()

	result, err := distill.RunClustered(
		context.Background(),
		store,
		mock, mock, mock, mock,
		embedder,
		distill.ClusteredOptions{
			ArtifactDir: root,
			Limit:       100,
		},
	)
	if err != nil {
		t.Fatalf("RunClustered: %v", err)
	}

	if result.Muse == "" {
		t.Error("expected non-empty muse")
	}
	if result.Usage.InputTokens == 0 {
		t.Error("expected non-zero input tokens")
	}

	// Verify artifacts were created
	artifacts := distill.NewArtifactStore(root)
	obsList, _ := artifacts.ListObservations()
	if len(obsList) == 0 {
		t.Error("expected observation artifacts")
	}

	clsList, _ := artifacts.ListClassifications()
	if len(clsList) == 0 {
		t.Error("expected classification artifacts")
	}

	embList, _ := artifacts.ListEmbeddings()
	if len(embList) == 0 {
		t.Error("expected embedding artifacts")
	}
}

func TestClusteredPipeline_CacheHit(t *testing.T) {
	store := testutil.NewConversationStore()
	store.AddSession("test", "s1", time.Now(), []conversation.Message{
		{Role: "user", Content: "use tabs"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "no emojis"},
		{Role: "assistant", Content: "sure"},
	})

	mock := &clusterMockLLM{}
	embedder := embedding.NewMockEmbedder(64)
	root := t.TempDir()
	opts := distill.ClusteredOptions{ArtifactDir: root, Limit: 100}

	// First run
	_, err := distill.RunClustered(context.Background(), store, mock, mock, mock, mock, embedder, opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	callsBefore := len(mock.calls)

	// Second run should use cached observations/classifications/embeddings
	_, err = distill.RunClustered(context.Background(), store, mock, mock, mock, mock, embedder, opts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	newCalls := len(mock.calls) - callsBefore
	if newCalls >= callsBefore {
		t.Errorf("expected fewer LLM calls on cache hit: first=%d, second=%d", callsBefore, newCalls)
	}
}

func TestFingerprintCascadeInvalidation(t *testing.T) {
	fp1 := distill.Fingerprint("2024-01-01T00:00:00Z", "prompt-v1")
	fp2 := distill.Fingerprint("2024-01-02T00:00:00Z", "prompt-v1")
	fp3 := distill.Fingerprint("2024-01-01T00:00:00Z", "prompt-v2")

	if fp1 == fp2 {
		t.Error("conversation update should change fingerprint")
	}
	if fp1 == fp3 {
		t.Error("prompt change should change fingerprint")
	}

	obs1FP := distill.Fingerprint("obs text", "classify-prompt-v1")
	obs2FP := distill.Fingerprint("different obs text", "classify-prompt-v1")
	if obs1FP == obs2FP {
		t.Error("different observations should produce different classification fingerprints")
	}
}

// clusterMockLLM dispatches based on system prompt content.
type clusterMockLLM struct {
	calls []string
}

func (m *clusterMockLLM) Converse(_ context.Context, system, user string, _ ...inference.ConverseOption) (string, inference.Usage, error) {
	m.calls = append(m.calls, system[:min(50, len(system))])
	usage := inference.Usage{InputTokens: 100, OutputTokens: 50}

	if strings.Contains(system, "classifying observations") {
		return "Prefers explicit patterns over implicit conventions", usage, nil
	}
	if strings.Contains(system, "synthesizing a cluster") {
		return "I prefer explicit, clear patterns in code.", usage, nil
	}
	if strings.Contains(system, "producing the final muse") {
		return "# How I Think\n\nI value explicitness over cleverness.", usage, nil
	}
	if strings.Contains(system, "distilling observations") {
		return "# Muse\n\nValues clarity.", usage, nil
	}
	if strings.Contains(system, "summarize") || strings.Contains(system, "Summarize") {
		return "Assistant discussed code style preferences", usage, nil
	}

	return "Prefers tabs over spaces\n\nValues explicit error handling\n\nTests before shipping", usage, nil
}

var _ distill.LLM = (*clusterMockLLM)(nil)
var _ storage.Store = (*testutil.ConversationStore)(nil)
