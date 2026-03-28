package conversation

import (
	"context"
	"strings"
	"testing"
	"time"
)

// helper to find a conversation by ID in the returned slice.
func findConversation(conversations []Conversation, id string) *Conversation {
	for i := range conversations {
		if conversations[i].ConversationID == id {
			return &conversations[i]
		}
	}
	return nil
}

func mustConversations(t *testing.T) []Conversation {
	t.Helper()
	t.Setenv("MUSE_CLAUDE_DIR", "testdata/claude")
	cc := &ClaudeCode{}
	conversations, err := cc.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() returned error: %v", err)
	}
	if conversations == nil {
		t.Fatal("Conversations() returned nil")
	}
	return conversations
}

func TestClaudeCode_BasicSession(t *testing.T) {
	conversations := mustConversations(t)

	s := findConversation(conversations, "basic-session")
	if s == nil {
		t.Fatal("basic-session not found in returned conversations")
	}

	// Source
	if s.Source != "claude-code" {
		t.Errorf("Source = %q, want %q", s.Source, "claude-code")
	}

	// Message count
	if len(s.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(s.Messages))
	}

	// Roles alternate user/assistant
	wantRoles := []string{"user", "assistant", "user", "assistant"}
	for i, want := range wantRoles {
		if s.Messages[i].Role != want {
			t.Errorf("Messages[%d].Role = %q, want %q", i, s.Messages[i].Role, want)
		}
	}

	// First user message content
	if s.Messages[0].Content != "use kebab-case for file names" {
		t.Errorf("Messages[0].Content = %q, want %q", s.Messages[0].Content, "use kebab-case for file names")
	}

	// Title from history.jsonl
	if s.Title != "Basic session" {
		t.Errorf("Title = %q, want %q", s.Title, "Basic session")
	}
}

func TestClaudeCode_ToolHeavySession(t *testing.T) {
	conversations := mustConversations(t)

	s := findConversation(conversations, "tool-heavy-session")
	if s == nil {
		t.Fatal("tool-heavy-session not found in returned conversations")
	}

	// Should have 4 messages: user, assistant (tool_use), user, assistant
	if len(s.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(s.Messages))
	}

	// First assistant message (index 1) should have text content AND a tool call
	assistant1 := s.Messages[1]
	if assistant1.Role != "assistant" {
		t.Fatalf("Messages[1].Role = %q, want %q", assistant1.Role, "assistant")
	}
	if assistant1.Content == "" {
		t.Error("Messages[1].Content is empty, expected text from tool_use assistant message")
	}
	if !strings.Contains(assistant1.Content, "refactor the handler") {
		// The text block says "I'll refactor the handler now."
		if !strings.Contains(assistant1.Content, "refactor") {
			t.Errorf("Messages[1].Content = %q, expected it to contain refactor text", assistant1.Content)
		}
	}

	// Tool call present
	if len(assistant1.ToolCalls) != 1 {
		t.Fatalf("Messages[1].ToolCalls length = %d, want 1", len(assistant1.ToolCalls))
	}
	if assistant1.ToolCalls[0].Name != "Edit" {
		t.Errorf("ToolCalls[0].Name = %q, want %q", assistant1.ToolCalls[0].Name, "Edit")
	}

	// Second assistant message (index 3) must be present — regression #34
	assistant2 := s.Messages[3]
	if assistant2.Role != "assistant" {
		t.Errorf("Messages[3].Role = %q, want %q", assistant2.Role, "assistant")
	}
	if assistant2.Content != "I've added the tests." {
		t.Errorf("Messages[3].Content = %q, want %q", assistant2.Content, "I've added the tests.")
	}

	// Title from history.jsonl
	if s.Title != "Refactoring the handler" {
		t.Errorf("Title = %q, want %q", s.Title, "Refactoring the handler")
	}
}

func TestClaudeCode_StreamingPartialsSkipped(t *testing.T) {
	conversations := mustConversations(t)

	s := findConversation(conversations, "streaming-partials")
	if s == nil {
		t.Fatal("streaming-partials not found in returned conversations")
	}

	// Partial should be skipped: user, assistant, user, assistant = 4 messages
	if len(s.Messages) != 4 {
		t.Fatalf("expected 4 messages (partial skipped), got %d", len(s.Messages))
	}

	// No message should contain the partial text
	for i, m := range s.Messages {
		if strings.Contains(m.Content, "I'm thinking...") {
			t.Errorf("Messages[%d] contains partial text %q which should have been skipped", i, m.Content)
		}
	}

	// Verify the kept messages
	if s.Messages[0].Content != "hello" {
		t.Errorf("Messages[0].Content = %q, want %q", s.Messages[0].Content, "hello")
	}
	if s.Messages[1].Content != "Hello! How can I help?" {
		t.Errorf("Messages[1].Content = %q, want %q", s.Messages[1].Content, "Hello! How can I help?")
	}
	if s.Messages[2].Content != "thanks" {
		t.Errorf("Messages[2].Content = %q, want %q", s.Messages[2].Content, "thanks")
	}
	if s.Messages[3].Content != "You're welcome!" {
		t.Errorf("Messages[3].Content = %q, want %q", s.Messages[3].Content, "You're welcome!")
	}
}

func TestClaudeCode_EmptySessionSkipped(t *testing.T) {
	conversations := mustConversations(t)

	s := findConversation(conversations, "empty-session")
	if s != nil {
		t.Errorf("empty-session should not appear in results, but found conversation with %d messages", len(s.Messages))
	}
}

func TestClaudeCode_MissingDirectory(t *testing.T) {
	t.Setenv("MUSE_CLAUDE_DIR", "testdata/nonexistent-path")
	cc := &ClaudeCode{}
	conversations, err := cc.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() returned error for missing dir: %v", err)
	}
	if conversations != nil {
		t.Errorf("Conversations() = %v, want nil for missing directory", conversations)
	}
}

func TestClaudeCode_SessionMetadata(t *testing.T) {
	conversations := mustConversations(t)

	s := findConversation(conversations, "basic-session")
	if s == nil {
		t.Fatal("basic-session not found")
	}

	// ConversationID
	if s.ConversationID != "basic-session" {
		t.Errorf("ConversationID = %q, want %q", s.ConversationID, "basic-session")
	}

	// Project comes from the cwd field
	if s.Project != "/home/user/project" {
		t.Errorf("Project = %q, want %q", s.Project, "/home/user/project")
	}

	// CreatedAt should be the earliest timestamp
	wantCreated := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	if !s.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt = %v, want %v", s.CreatedAt, wantCreated)
	}

	// UpdatedAt should be the latest timestamp
	wantUpdated := time.Date(2024, 1, 15, 10, 1, 1, 0, time.UTC)
	if !s.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("UpdatedAt = %v, want %v", s.UpdatedAt, wantUpdated)
	}

	// Message timestamps should be populated
	for i, m := range s.Messages {
		if m.Timestamp.IsZero() {
			t.Errorf("Messages[%d].Timestamp is zero", i)
		}
	}

	// Assistant messages should have a model
	for i, m := range s.Messages {
		if m.Role == "assistant" && m.Model == "" {
			t.Errorf("Messages[%d] (assistant) has empty Model", i)
		}
	}

	// SchemaVersion
	if s.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", s.SchemaVersion)
	}
}
