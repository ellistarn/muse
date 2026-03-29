package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

func newTestLocalStore(t *testing.T) *storage.LocalStore {
	t.Helper()
	return storage.NewLocalStoreWithRoot(t.TempDir())
}

func TestLocalStore_ConversationRoundTrip(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	conv := &conversation.Conversation{
		SchemaVersion:  1,
		Source:         "opencode",
		ConversationID: "conv-001",
		Project:        "/home/user/project",
		Title:          "Fix bug in parser",
		CreatedAt:      time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2025, 1, 1, 11, 0, 0, 0, time.UTC),
		Messages: []conversation.Message{
			{Role: "user", Content: "Fix the parser", Timestamp: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
			{Role: "assistant", Content: "Done.", Timestamp: time.Date(2025, 1, 1, 10, 1, 0, 0, time.UTC), Model: "claude-3"},
		},
	}

	n, err := store.PutConversation(ctx, conv)
	if err != nil {
		t.Fatalf("PutConversation: %v", err)
	}
	if n == 0 {
		t.Fatal("PutConversation returned 0 bytes")
	}

	got, err := store.GetConversation(ctx, "opencode", "conv-001")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.ConversationID != conv.ConversationID {
		t.Errorf("ConversationID = %q, want %q", got.ConversationID, conv.ConversationID)
	}
	if got.Source != conv.Source {
		t.Errorf("Source = %q, want %q", got.Source, conv.Source)
	}
	if got.Title != conv.Title {
		t.Errorf("Title = %q, want %q", got.Title, conv.Title)
	}
	if got.Project != conv.Project {
		t.Errorf("Project = %q, want %q", got.Project, conv.Project)
	}
	if !got.CreatedAt.Equal(conv.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, conv.CreatedAt)
	}
	if !got.UpdatedAt.Equal(conv.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, conv.UpdatedAt)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != "user" || got.Messages[0].Content != "Fix the parser" {
		t.Errorf("Messages[0] = %+v, unexpected", got.Messages[0])
	}
	if got.Messages[1].Model != "claude-3" {
		t.Errorf("Messages[1].Model = %q, want %q", got.Messages[1].Model, "claude-3")
	}

	entries, err := store.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Source != "opencode" {
		t.Errorf("entry.Source = %q, want %q", entries[0].Source, "opencode")
	}
	if entries[0].ConversationID != "conv-001" {
		t.Errorf("entry.ConversationID = %q, want %q", entries[0].ConversationID, "conv-001")
	}
	if entries[0].Key != "conversations/opencode/conv-001.json" {
		t.Errorf("entry.Key = %q, want %q", entries[0].Key, "conversations/opencode/conv-001.json")
	}
}

func TestLocalStore_ConversationNotFound(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	_, err := store.GetConversation(ctx, "opencode", "nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !storage.IsNotFound(err) {
		t.Errorf("expected NotFoundError, got %T: %v", err, err)
	}
}

func TestLocalStore_MuseRoundTrip(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	ts1 := "2025-01-01T10-00-00"
	content1 := "# Muse v1\nFirst version."

	if err := store.PutMuse(ctx, ts1, content1); err != nil {
		t.Fatalf("PutMuse v1: %v", err)
	}

	got, err := store.GetMuse(ctx)
	if err != nil {
		t.Fatalf("GetMuse: %v", err)
	}
	if got != content1 {
		t.Errorf("GetMuse = %q, want %q", got, content1)
	}

	gotVersion, err := store.GetMuseVersion(ctx, ts1)
	if err != nil {
		t.Fatalf("GetMuseVersion: %v", err)
	}
	if gotVersion != content1 {
		t.Errorf("GetMuseVersion = %q, want %q", gotVersion, content1)
	}

	// Put a second version with a later timestamp.
	ts2 := "2025-01-02T10-00-00"
	content2 := "# Muse v2\nSecond version."
	if err := store.PutMuse(ctx, ts2, content2); err != nil {
		t.Fatalf("PutMuse v2: %v", err)
	}

	// GetMuse should return the latest (lexicographically last).
	got, err = store.GetMuse(ctx)
	if err != nil {
		t.Fatalf("GetMuse after v2: %v", err)
	}
	if got != content2 {
		t.Errorf("GetMuse = %q, want %q", got, content2)
	}
}

func TestLocalStore_GetMuse_SkipsDiffOnly(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	// Write a real muse at ts1
	ts1 := "2025-01-01T10-00-00"
	content1 := "# Muse v1"
	if err := store.PutMuse(ctx, ts1, content1); err != nil {
		t.Fatalf("PutMuse: %v", err)
	}

	// Write only a diff at ts2 (simulating the old bug where timestamps diverged)
	ts2 := "2025-01-02T10-00-00"
	if err := store.PutMuseDiff(ctx, ts2, "some diff"); err != nil {
		t.Fatalf("PutMuseDiff: %v", err)
	}

	// GetMuse should skip ts2 (no muse.md) and return ts1's content
	got, err := store.GetMuse(ctx)
	if err != nil {
		t.Fatalf("GetMuse: %v", err)
	}
	if got != content1 {
		t.Errorf("GetMuse = %q, want %q", got, content1)
	}
}

func TestLocalStore_ListMuses(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	timestamps := []string{
		"2025-01-03T00-00-00",
		"2025-01-01T00-00-00",
		"2025-01-02T00-00-00",
	}
	for _, ts := range timestamps {
		if err := store.PutMuse(ctx, ts, "content-"+ts); err != nil {
			t.Fatalf("PutMuse(%s): %v", ts, err)
		}
	}

	got, err := store.ListMuses(ctx)
	if err != nil {
		t.Fatalf("ListMuses: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(ListMuses) = %d, want 3", len(got))
	}

	// Should be sorted ascending.
	want := []string{
		"2025-01-01T00-00-00",
		"2025-01-02T00-00-00",
		"2025-01-03T00-00-00",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListMuses[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLocalStore_MuseNotFound(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	_, err := store.GetMuse(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !storage.IsNotFound(err) {
		t.Errorf("expected NotFoundError, got %T: %v", err, err)
	}
}

func TestLocalStore_DataRoundTrip(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	key := "observations/opencode/conv-1.json"
	content := []byte(`{"fingerprint":"abc","items":[]}`)

	if err := store.PutData(ctx, key, content); err != nil {
		t.Fatalf("PutData: %v", err)
	}

	got, err := store.GetData(ctx, key)
	if err != nil {
		t.Fatalf("GetData: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("GetData = %q, want %q", got, content)
	}

	keys, err := store.ListData(ctx, "observations/")
	if err != nil {
		t.Fatalf("ListData: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("len(ListData) = %d, want 1", len(keys))
	}
	if keys[0] != key {
		t.Errorf("ListData[0] = %q, want %q", keys[0], key)
	}
}

func TestLocalStore_DataNotFound(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	_, err := store.GetData(ctx, "observations/opencode/nonexistent.json")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !storage.IsNotFound(err) {
		t.Errorf("expected NotFoundError, got %T: %v", err, err)
	}
}

func TestLocalStore_DeletePrefix(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	keys := []string{
		"observations/opencode/conv-1.json",
		"observations/opencode/conv-2.json",
		"observations/claude/conv-3.json",
	}
	for _, key := range keys {
		if err := store.PutData(ctx, key, []byte("data for "+key)); err != nil {
			t.Fatalf("PutData(%s): %v", key, err)
		}
	}

	// Verify they exist.
	listed, err := store.ListData(ctx, "observations/")
	if err != nil {
		t.Fatalf("ListData before delete: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("len(ListData) = %d, want 3", len(listed))
	}

	// Delete all observations.
	if err := store.DeletePrefix(ctx, "observations/"); err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}

	listed, err = store.ListData(ctx, "observations/")
	if err != nil {
		t.Fatalf("ListData after delete: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("len(ListData) = %d after delete, want 0", len(listed))
	}
}

func TestLocalStore_ListConversationsEmpty(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	entries, err := store.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("len(ListConversations) = %d, want 0", len(entries))
	}
}

func TestLocalStore_GetConversation_RejectsStaleSchema(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	// Write a file with the old "session_id" JSON key (pre-rename schema).
	// GetConversation must reject this rather than silently returning an
	// empty ConversationID.
	staleJSON := `{
		"schema_version": 1,
		"source": "opencode",
		"session_id": "old-conv-001",
		"messages": [{"role": "user", "content": "hello"}]
	}`
	dir := filepath.Join(store.Root(), "conversations", "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old-conv-001.json"), []byte(staleJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := store.GetConversation(ctx, "opencode", "old-conv-001")
	if err == nil {
		t.Fatal("expected error for stale session_id schema, got nil")
	}
}
