package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

func newTestLocalStore(t *testing.T) *storage.LocalStore {
	t.Helper()
	return storage.NewLocalStoreWithRoot(t.TempDir())
}

func TestLocalStore_SessionRoundTrip(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	session := &conversation.Session{
		SchemaVersion: 1,
		Source:        "opencode",
		SessionID:     "sess-001",
		Project:       "/home/user/project",
		Title:         "Fix bug in parser",
		CreatedAt:     time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2025, 1, 1, 11, 0, 0, 0, time.UTC),
		Messages: []conversation.Message{
			{Role: "user", Content: "Fix the parser", Timestamp: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
			{Role: "assistant", Content: "Done.", Timestamp: time.Date(2025, 1, 1, 10, 1, 0, 0, time.UTC), Model: "claude-3"},
		},
	}

	n, err := store.PutSession(ctx, session)
	if err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if n == 0 {
		t.Fatal("PutSession returned 0 bytes")
	}

	got, err := store.GetSession(ctx, "opencode", "sess-001")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.SessionID != session.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, session.SessionID)
	}
	if got.Source != session.Source {
		t.Errorf("Source = %q, want %q", got.Source, session.Source)
	}
	if got.Title != session.Title {
		t.Errorf("Title = %q, want %q", got.Title, session.Title)
	}
	if got.Project != session.Project {
		t.Errorf("Project = %q, want %q", got.Project, session.Project)
	}
	if !got.CreatedAt.Equal(session.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, session.CreatedAt)
	}
	if !got.UpdatedAt.Equal(session.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, session.UpdatedAt)
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

	entries, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Source != "opencode" {
		t.Errorf("entry.Source = %q, want %q", entries[0].Source, "opencode")
	}
	if entries[0].SessionID != "sess-001" {
		t.Errorf("entry.SessionID = %q, want %q", entries[0].SessionID, "sess-001")
	}
	if entries[0].Key != "conversations/opencode/sess-001.json" {
		t.Errorf("entry.Key = %q, want %q", entries[0].Key, "conversations/opencode/sess-001.json")
	}
}

func TestLocalStore_SessionNotFound(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	_, err := store.GetSession(ctx, "opencode", "nonexistent")
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

func TestLocalStore_ObservationRoundTrip(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	memoryKey := "conversations/opencode/sess-1.json"
	content := "## Observations\n- User prefers concise code."

	if err := store.PutObservation(ctx, memoryKey, content); err != nil {
		t.Fatalf("PutObservation: %v", err)
	}

	got, err := store.GetObservation(ctx, memoryKey)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if got != content {
		t.Errorf("GetObservation = %q, want %q", got, content)
	}

	observations, err := store.ListObservations(ctx)
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("len(ListObservations) = %d, want 1", len(observations))
	}
	modTime, ok := observations[memoryKey]
	if !ok {
		t.Fatalf("ListObservations missing key %q, got %v", memoryKey, observations)
	}
	if modTime.IsZero() {
		t.Error("ListObservations returned zero mod time")
	}
}

func TestLocalStore_ObservationNotFound(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	_, err := store.GetObservation(ctx, "conversations/opencode/nonexistent.json")
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
		"conversations/opencode/sess-1.json",
		"conversations/opencode/sess-2.json",
		"conversations/claude/sess-3.json",
	}
	for _, key := range keys {
		if err := store.PutObservation(ctx, key, "observation for "+key); err != nil {
			t.Fatalf("PutObservation(%s): %v", key, err)
		}
	}

	// Verify they exist.
	observations, err := store.ListObservations(ctx)
	if err != nil {
		t.Fatalf("ListObservations before delete: %v", err)
	}
	if len(observations) != 3 {
		t.Fatalf("len(ListObservations) = %d, want 3", len(observations))
	}

	// Delete all observations.
	if err := store.DeletePrefix(ctx, "observations/"); err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}

	observations, err = store.ListObservations(ctx)
	if err != nil {
		t.Fatalf("ListObservations after delete: %v", err)
	}
	if len(observations) != 0 {
		t.Errorf("len(ListObservations) = %d after delete, want 0", len(observations))
	}
}

func TestLocalStore_ListSessionsEmpty(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	entries, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("len(ListSessions) = %d, want 0", len(entries))
	}
}
