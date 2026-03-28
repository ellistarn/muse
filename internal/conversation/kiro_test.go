package conversation

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

const kiroTestDir = "testdata/kiro/kiro.kiroagent"

func TestKiro_ChatFileContent(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", kiroTestDir)

	k := &Kiro{}
	conversations, err := k.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := findKiroConversation(t, conversations, "sess-1")

	if s.Source != "kiro" {
		t.Errorf("Source = %q, want %q", s.Source, "kiro")
	}
	if s.Title != "Debugging API" {
		t.Errorf("Title = %q, want %q", s.Title, "Debugging API")
	}
	if s.Project != "/home/user/api-project" {
		t.Errorf("Project = %q, want %q", s.Project, "/home/user/api-project")
	}

	// The .chat file (exec-2) has the full conversation with 2 user + 2 assistant turns.
	if len(s.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(s.Messages))
	}

	wantRoles := []string{"user", "assistant", "user", "assistant"}
	for i, want := range wantRoles {
		if s.Messages[i].Role != want {
			t.Errorf("Messages[%d].Role = %q, want %q", i, s.Messages[i].Role, want)
		}
	}

	// User content should come from the .chat file with EnvironmentContext stripped.
	if s.Messages[0].Content != "help me debug this API endpoint" {
		t.Errorf("Messages[0].Content = %q, want %q", s.Messages[0].Content, "help me debug this API endpoint")
	}
	if s.Messages[2].Content != "getting a 500 error on POST /users" {
		t.Errorf("Messages[2].Content = %q, want %q", s.Messages[2].Content, "getting a 500 error on POST /users")
	}

	// Assistant content should be the real response from the .chat file, NOT "On it."
	// Consecutive bot messages between user turns are joined.
	wantAssistant1 := "I'll look into the API endpoint issue. Let me check the handler code first.\nI can see the handler. Can you share the specific error you're getting?"
	if s.Messages[1].Content != wantAssistant1 {
		t.Errorf("Messages[1].Content = %q, want %q", s.Messages[1].Content, wantAssistant1)
	}
}

func TestKiro_ChatFileModel(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", kiroTestDir)

	k := &Kiro{}
	conversations, err := k.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := findKiroConversation(t, conversations, "sess-1")

	// Model should be populated from .chat metadata on assistant messages.
	for i, msg := range s.Messages {
		if msg.Role == "assistant" && msg.Model != "claude-sonnet-4-20250514" {
			t.Errorf("Messages[%d].Model = %q, want %q", i, msg.Model, "claude-sonnet-4-20250514")
		}
		if msg.Role == "user" && msg.Model != "" {
			t.Errorf("Messages[%d].Model = %q, want empty for user", i, msg.Model)
		}
	}
}

func TestKiro_ChatFileFiltering(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", kiroTestDir)

	k := &Kiro{}
	conversations, err := k.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := findKiroConversation(t, conversations, "sess-1")

	for i, msg := range s.Messages {
		if msg.Content == "I will follow these instructions." {
			t.Errorf("Messages[%d] should not contain bot preamble", i)
		}
		if msg.Role == "user" && (msg.Content == "" || msg.Content[0] == '#') {
			t.Errorf("Messages[%d] should not contain system prompt", i)
		}
		if containsString(msg.Content, "<kiro-ide-message>") {
			t.Errorf("Messages[%d] should not contain IDE message", i)
		}
		if containsString(msg.Content, "<EnvironmentContext>") {
			t.Errorf("Messages[%d] should not contain EnvironmentContext", i)
		}
	}
}

func TestKiro_FallbackToSessionJSON(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", kiroTestDir)

	k := &Kiro{}
	conversations, err := k.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := findKiroConversation(t, conversations, "sess-fallback")

	// Fallback conversation has no matching .chat files (exec-orphan-1, exec-orphan-2),
	// so it should fall back to conversation JSON content ("On it." for assistant).
	if len(s.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(s.Messages))
	}
	if s.Messages[1].Content != "On it." {
		t.Errorf("Fallback Messages[1].Content = %q, want %q", s.Messages[1].Content, "On it.")
	}
}

func TestKiro_EmptyConversationSkipped(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", kiroTestDir)

	k := &Kiro{}
	conversations, err := k.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, s := range conversations {
		if s.ConversationID == "sess-empty" {
			t.Error("sess-empty should not be in returned conversations")
		}
	}
}

func TestKiro_MissingDirectory(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", filepath.Join(t.TempDir(), "nonexistent"))

	k := &Kiro{}
	conversations, err := k.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil error for missing directory, got: %v", err)
	}
	if conversations != nil {
		t.Errorf("expected nil conversations for missing directory, got %d", len(conversations))
	}
}

func TestKiro_ConversationMetadata(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", kiroTestDir)

	k := &Kiro{}
	conversations, err := k.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := findKiroConversation(t, conversations, "sess-1")

	want := time.UnixMilli(1705312800000)
	if !s.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", s.CreatedAt, want)
	}
	if s.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", s.SchemaVersion)
	}
	if s.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
}

func TestKiro_StripEnvironmentContext(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no context",
			input: "just a question",
			want:  "just a question",
		},
		{
			name:  "context at end",
			input: "my question\n\n<EnvironmentContext>\nstuff\n</EnvironmentContext>",
			want:  "my question\n\n",
		},
		{
			name:  "context in middle",
			input: "before<EnvironmentContext>stuff</EnvironmentContext>after",
			want:  "beforeafter",
		},
		{
			name:  "unclosed context",
			input: "question<EnvironmentContext>dangling",
			want:  "question",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripEnvironmentContext(tt.input)
			if got != tt.want {
				t.Errorf("stripEnvironmentContext(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func findKiroConversation(t *testing.T, conversations []Conversation, id string) *Conversation {
	t.Helper()
	for i := range conversations {
		if conversations[i].ConversationID == id {
			return &conversations[i]
		}
	}
	t.Fatalf("conversation %q not found", id)
	return nil
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
