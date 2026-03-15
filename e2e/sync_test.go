package e2e

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/internal/testutil"
)

func TestSyncAll(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewConversationStore()
	dst := testutil.NewConversationStore()

	// 2 sessions
	src.AddSession("test", "sess-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})
	src.AddSession("test", "sess-2", time.Now(), []conversation.Message{
		{Role: "user", Content: "bye"},
		{Role: "assistant", Content: "see ya"},
	})

	// 2 reflections
	src.Reflections["conversations/test/sess-1.json"] = "observation 1"
	src.Reflections["conversations/test/sess-2.json"] = "observation 2"

	// 1 muse version
	src.Muses["2024-01-15T10:00:00Z"] = "# My Muse\ncontent"

	var buf bytes.Buffer
	if err := storage.Sync(ctx, src, dst, nil, &buf); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// Verify sessions
	sessions, err := dst.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("dst sessions = %d, want 2", len(sessions))
	}

	// Verify reflections
	if dst.Reflections["conversations/test/sess-1.json"] != "observation 1" {
		t.Errorf("dst reflection 1 = %q, want %q", dst.Reflections["conversations/test/sess-1.json"], "observation 1")
	}
	if dst.Reflections["conversations/test/sess-2.json"] != "observation 2" {
		t.Errorf("dst reflection 2 = %q, want %q", dst.Reflections["conversations/test/sess-2.json"], "observation 2")
	}

	// Verify muse version
	content, err := dst.GetMuseVersion(ctx, "2024-01-15T10:00:00Z")
	if err != nil {
		t.Fatalf("GetMuseVersion() error: %v", err)
	}
	if content != "# My Muse\ncontent" {
		t.Errorf("dst muse = %q, want %q", content, "# My Muse\ncontent")
	}

	// Verify output
	output := buf.String()
	if !strings.Contains(output, "Synced 2 conversations") {
		t.Errorf("output missing 'Synced 2 memories', got: %s", output)
	}
	if !strings.Contains(output, "Synced 2 reflections") {
		t.Errorf("output missing 'Synced 2 reflections', got: %s", output)
	}
	if !strings.Contains(output, "Synced 1 muse versions") {
		t.Errorf("output missing 'Synced 1 muse versions', got: %s", output)
	}
}

func TestSyncSelectiveCategories(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewConversationStore()
	dst := testutil.NewConversationStore()

	// Populate all categories in src
	src.AddSession("test", "sess-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
	})
	src.Reflections["conversations/test/sess-1.json"] = "observation 1"
	src.Muses["2024-01-15T10:00:00Z"] = "# Muse"

	var buf bytes.Buffer
	if err := storage.Sync(ctx, src, dst, []string{"conversations"}, &buf); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// Verify sessions synced
	sessions, err := dst.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("dst sessions = %d, want 1", len(sessions))
	}

	// Verify reflections NOT synced
	if len(dst.Reflections) != 0 {
		t.Errorf("dst reflections = %d, want 0", len(dst.Reflections))
	}

	// Verify muse NOT synced
	if len(dst.Muses) != 0 {
		t.Errorf("dst muses = %d, want 0", len(dst.Muses))
	}
}

func TestSyncIdempotent(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewConversationStore()
	dst := testutil.NewConversationStore()

	src.AddSession("test", "sess-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
	})
	src.Reflections["conversations/test/sess-1.json"] = "observation 1"
	src.Muses["2024-01-15T10:00:00Z"] = "# Muse"

	// First sync
	var buf1 bytes.Buffer
	if err := storage.Sync(ctx, src, dst, nil, &buf1); err != nil {
		t.Fatalf("first Sync() error: %v", err)
	}

	// Second sync
	var buf2 bytes.Buffer
	if err := storage.Sync(ctx, src, dst, nil, &buf2); err != nil {
		t.Fatalf("second Sync() error: %v", err)
	}

	// Verify dst still has correct data
	if dst.Reflections["conversations/test/sess-1.json"] != "observation 1" {
		t.Errorf("reflection = %q, want %q", dst.Reflections["conversations/test/sess-1.json"], "observation 1")
	}
	content, err := dst.GetMuseVersion(ctx, "2024-01-15T10:00:00Z")
	if err != nil {
		t.Fatalf("GetMuseVersion() error: %v", err)
	}
	if content != "# Muse" {
		t.Errorf("muse = %q, want %q", content, "# Muse")
	}
}

func TestSyncEmptySource(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewConversationStore()
	dst := testutil.NewConversationStore()

	var buf bytes.Buffer
	if err := storage.Sync(ctx, src, dst, nil, &buf); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// Verify dst is empty
	sessions, err := dst.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("dst sessions = %d, want 0", len(sessions))
	}
	if len(dst.Reflections) != 0 {
		t.Errorf("dst reflections = %d, want 0", len(dst.Reflections))
	}
	if len(dst.Muses) != 0 {
		t.Errorf("dst muses = %d, want 0", len(dst.Muses))
	}

	// Verify output
	output := buf.String()
	if !strings.Contains(output, "Synced 0 conversations") {
		t.Errorf("output missing 'Synced 0 memories', got: %s", output)
	}
	if !strings.Contains(output, "Synced 0 reflections") {
		t.Errorf("output missing 'Synced 0 reflections', got: %s", output)
	}
	if !strings.Contains(output, "Synced 0 muse versions") {
		t.Errorf("output missing 'Synced 0 muse versions', got: %s", output)
	}
}

func TestSyncPreservesExistingDstData(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewConversationStore()
	dst := testutil.NewConversationStore()

	// Pre-populate dst
	dst.AddSession("existing", "dst-sess-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "existing message"},
	})
	dst.Reflections["conversations/existing/dst-sess-1.json"] = "existing observation"
	dst.Muses["2024-01-01T00:00:00Z"] = "# Existing Muse"

	// Populate src with different data
	src.AddSession("test", "src-sess-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "new message"},
	})
	src.Reflections["conversations/test/src-sess-1.json"] = "new observation"
	src.Muses["2024-02-01T00:00:00Z"] = "# New Muse"

	var buf bytes.Buffer
	if err := storage.Sync(ctx, src, dst, nil, &buf); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// Verify dst has original session + synced session
	sessions, err := dst.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("dst sessions = %d, want 2", len(sessions))
	}

	// Verify original dst data preserved
	if dst.Reflections["conversations/existing/dst-sess-1.json"] != "existing observation" {
		t.Errorf("existing reflection lost")
	}
	existingMuse, err := dst.GetMuseVersion(ctx, "2024-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("GetMuseVersion(existing) error: %v", err)
	}
	if existingMuse != "# Existing Muse" {
		t.Errorf("existing muse = %q, want %q", existingMuse, "# Existing Muse")
	}

	// Verify new synced data present
	if dst.Reflections["conversations/test/src-sess-1.json"] != "new observation" {
		t.Errorf("synced reflection missing")
	}
	newMuse, err := dst.GetMuseVersion(ctx, "2024-02-01T00:00:00Z")
	if err != nil {
		t.Fatalf("GetMuseVersion(new) error: %v", err)
	}
	if newMuse != "# New Muse" {
		t.Errorf("synced muse = %q, want %q", newMuse, "# New Muse")
	}
}
