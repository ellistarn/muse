package conversation

import (
	"path/filepath"
	"testing"
	"time"
)

func TestKiro_BasicSession(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", "testdata/kiro")

	k := &Kiro{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) < 1 {
		t.Fatal("expected at least 1 session, got 0")
	}

	// Find sess-1.
	var s *Session
	for i := range sessions {
		if sessions[i].SessionID == "sess-1" {
			s = &sessions[i]
			break
		}
	}
	if s == nil {
		t.Fatal("session sess-1 not found")
	}

	if s.Source != "kiro" {
		t.Errorf("Source = %q, want %q", s.Source, "kiro")
	}
	if s.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", s.SessionID, "sess-1")
	}
	if s.Title != "Debugging API" {
		t.Errorf("Title = %q, want %q", s.Title, "Debugging API")
	}
	if s.Project != "/home/user/api-project" {
		t.Errorf("Project = %q, want %q", s.Project, "/home/user/api-project")
	}
	if len(s.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(s.Messages))
	}

	// Verify roles alternate user/assistant.
	wantRoles := []string{"user", "assistant", "user", "assistant"}
	for i, want := range wantRoles {
		if s.Messages[i].Role != want {
			t.Errorf("Messages[%d].Role = %q, want %q", i, s.Messages[i].Role, want)
		}
	}

	// User content should be parsed from array blocks.
	if s.Messages[0].Content != "help me debug this API endpoint" {
		t.Errorf("Messages[0].Content = %q, want %q", s.Messages[0].Content, "help me debug this API endpoint")
	}
	if s.Messages[2].Content != "getting a 500 error on POST /users" {
		t.Errorf("Messages[2].Content = %q, want %q", s.Messages[2].Content, "getting a 500 error on POST /users")
	}

	// Assistant content should be parsed from plain string.
	if s.Messages[1].Content != "I can help with that. Can you share the error?" {
		t.Errorf("Messages[1].Content = %q, want %q", s.Messages[1].Content, "I can help with that. Can you share the error?")
	}
}

func TestKiro_EmptySessionSkipped(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", "testdata/kiro")

	k := &Kiro{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, s := range sessions {
		if s.SessionID == "sess-empty" {
			t.Error("sess-empty should not be in returned sessions (no user/assistant messages)")
		}
	}
}

func TestKiro_MissingDirectory(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", filepath.Join(t.TempDir(), "nonexistent"))

	k := &Kiro{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("expected nil error for missing directory, got: %v", err)
	}
	if sessions != nil {
		t.Errorf("expected nil sessions for missing directory, got %d", len(sessions))
	}
}

func TestKiro_SessionMetadata(t *testing.T) {
	t.Setenv("MUSE_KIRO_DIR", "testdata/kiro")

	k := &Kiro{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var s *Session
	for i := range sessions {
		if sessions[i].SessionID == "sess-1" {
			s = &sessions[i]
			break
		}
	}
	if s == nil {
		t.Fatal("session sess-1 not found")
	}

	// dateCreated "1705312800000" = 2024-01-15 10:00:00 UTC.
	want := time.UnixMilli(1705312800000)
	if !s.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", s.CreatedAt, want)
	}

	if s.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", s.SchemaVersion)
	}

	// UpdatedAt should be set (file mod time).
	if s.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
}
