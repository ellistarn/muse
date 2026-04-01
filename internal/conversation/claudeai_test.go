package conversation

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustClaudeAIConversations(t *testing.T) []Conversation {
	t.Helper()
	t.Setenv("MUSE_CLAUDE_AI_DIR", "testdata/claude-ai")
	c := &ClaudeAI{}
	conversations, err := c.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() returned error: %v", err)
	}
	if conversations == nil {
		t.Fatal("Conversations() returned nil")
	}
	return conversations
}

func TestClaudeAI_BasicConversation(t *testing.T) {
	conversations := mustClaudeAIConversations(t)

	conv := findConversation(conversations, "conv-001")
	if conv == nil {
		t.Fatal("conv-001 not found")
	}

	if conv.Source != "claude-ai" {
		t.Errorf("Source = %q, want %q", conv.Source, "claude-ai")
	}
	if conv.Title != "Help with Go interfaces" {
		t.Errorf("Title = %q, want %q", conv.Title, "Help with Go interfaces")
	}
	if len(conv.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(conv.Messages))
	}

	wantRoles := []string{"user", "assistant", "user", "assistant"}
	for i, want := range wantRoles {
		if conv.Messages[i].Role != want {
			t.Errorf("Messages[%d].Role = %q, want %q", i, conv.Messages[i].Role, want)
		}
	}

	if conv.Messages[0].Content != "How should I design an interface for a storage backend?" {
		t.Errorf("Messages[0].Content = %q", conv.Messages[0].Content)
	}

	wantCreated := time.Date(2024, 6, 15, 9, 0, 0, 0, time.UTC)
	if !conv.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt = %v, want %v", conv.CreatedAt, wantCreated)
	}

	wantUpdated := time.Date(2024, 6, 15, 9, 5, 0, 0, time.UTC)
	if !conv.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("UpdatedAt = %v, want %v", conv.UpdatedAt, wantUpdated)
	}
}

func TestClaudeAI_FallbackTitle(t *testing.T) {
	conversations := mustClaudeAIConversations(t)

	conv := findConversation(conversations, "conv-002")
	if conv == nil {
		t.Fatal("conv-002 not found")
	}

	if conv.Title != "What is the capital of France?" {
		t.Errorf("Title = %q, want first message as fallback", conv.Title)
	}
}

func TestClaudeAI_EmptyConversationsSkipped(t *testing.T) {
	conversations := mustClaudeAIConversations(t)

	if findConversation(conversations, "conv-empty") != nil {
		t.Error("conv-empty should be skipped")
	}
	if findConversation(conversations, "conv-blank-messages") != nil {
		t.Error("conv-blank-messages should be skipped")
	}
}

func TestClaudeAI_MissingDirectory(t *testing.T) {
	t.Setenv("MUSE_CLAUDE_AI_DIR", "testdata/nonexistent-path")
	c := &ClaudeAI{}
	conversations, err := c.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() returned error for missing dir: %v", err)
	}
	if conversations != nil {
		t.Errorf("Conversations() = %v, want nil for missing directory", conversations)
	}
}

func TestClaudeAI_SortedByUpdateTime(t *testing.T) {
	conversations := mustClaudeAIConversations(t)

	if len(conversations) < 2 {
		t.Fatalf("expected at least 2 conversations, got %d", len(conversations))
	}

	for i := 1; i < len(conversations); i++ {
		if conversations[i].UpdatedAt.After(conversations[i-1].UpdatedAt) {
			t.Errorf("conversations not sorted by UpdatedAt desc: [%d]=%v > [%d]=%v",
				i, conversations[i].UpdatedAt, i-1, conversations[i-1].UpdatedAt)
		}
	}
}

func TestClaudeAI_ZipExport(t *testing.T) {
	// Create a temp dir with a zip containing conversations.json
	dir := t.TempDir()
	jsonData, err := os.ReadFile("testdata/claude-ai/conversations.json")
	if err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(dir, "claude-export.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(zf)
	f, err := w.Create("conversations.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(jsonData); err != nil {
		t.Fatal(err)
	}
	w.Close()
	zf.Close()

	t.Setenv("MUSE_CLAUDE_AI_DIR", dir)
	c := &ClaudeAI{}
	conversations, err := c.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() from zip returned error: %v", err)
	}

	conv := findConversation(conversations, "conv-001")
	if conv == nil {
		t.Fatal("conv-001 not found from zip export")
	}
	if conv.Title != "Help with Go interfaces" {
		t.Errorf("Title = %q, want %q", conv.Title, "Help with Go interfaces")
	}
	if len(conv.Messages) != 4 {
		t.Errorf("expected 4 messages, got %d", len(conv.Messages))
	}
}
