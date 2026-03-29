package e2e

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/internal/testutil"
)

func TestSyncAll(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewConversationStore()
	dst := testutil.NewConversationStore()

	// 2 conversations
	src.AddConversation("test", "conv-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})
	src.AddConversation("test", "conv-2", time.Now(), []conversation.Message{
		{Role: "user", Content: "bye"},
		{Role: "assistant", Content: "see ya"},
	})

	// 2 observations (via the unified JSON artifact path)
	compose.PutObservations(ctx, src, "test", "conv-1", &compose.Observations{
		Items: []compose.Observation{{Observation: "observation 1"}},
	})
	compose.PutObservations(ctx, src, "test", "conv-2", &compose.Observations{
		Items: []compose.Observation{{Observation: "observation 2"}},
	})

	// 1 muse version
	src.Muses["2024-01-15T10:00:00Z"] = "# My Muse\ncontent"

	var buf bytes.Buffer
	if err := storage.Sync(ctx, src, dst, nil, &buf); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// Verify conversations
	conversations, err := dst.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if len(conversations) != 2 {
		t.Errorf("dst conversations = %d, want 2", len(conversations))
	}

	// Verify observations synced via DataStore
	obs1, err := compose.GetObservations(ctx, dst, "test", "conv-1")
	if err != nil {
		t.Fatalf("GetObservations(conv-1) error: %v", err)
	}
	if len(obs1.Items) != 1 || obs1.Items[0].Observation != "observation 1" {
		t.Errorf("observation 1 = %+v, want 'observation 1'", obs1.Items)
	}
	obs2, err := compose.GetObservations(ctx, dst, "test", "conv-2")
	if err != nil {
		t.Fatalf("GetObservations(conv-2) error: %v", err)
	}
	if len(obs2.Items) != 1 || obs2.Items[0].Observation != "observation 2" {
		t.Errorf("observation 2 = %+v, want 'observation 2'", obs2.Items)
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
		t.Errorf("output missing 'Synced 2 conversations', got: %s", output)
	}
	if !strings.Contains(output, "Synced 2 observations") {
		t.Errorf("output missing 'Synced 2 observations', got: %s", output)
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
	src.AddConversation("test", "conv-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
	})
	compose.PutObservations(ctx, src, "test", "conv-1", &compose.Observations{
		Items: []compose.Observation{{Observation: "observation 1"}},
	})
	src.Muses["2024-01-15T10:00:00Z"] = "# Muse"

	var buf bytes.Buffer
	if err := storage.Sync(ctx, src, dst, []string{"conversations"}, &buf); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// Verify conversations synced
	conversations, err := dst.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if len(conversations) != 1 {
		t.Errorf("dst conversations = %d, want 1", len(conversations))
	}

	// Verify observations NOT synced
	obsList, _ := dst.ListData(ctx, "observations/")
	if len(obsList) != 0 {
		t.Errorf("dst observations = %d, want 0", len(obsList))
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

	src.AddConversation("test", "conv-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
	})
	compose.PutObservations(ctx, src, "test", "conv-1", &compose.Observations{
		Items: []compose.Observation{{Observation: "observation 1"}},
	})
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
	obs, err := compose.GetObservations(ctx, dst, "test", "conv-1")
	if err != nil {
		t.Fatalf("GetObservations error: %v", err)
	}
	if len(obs.Items) != 1 || obs.Items[0].Observation != "observation 1" {
		t.Errorf("observation = %+v, want 'observation 1'", obs.Items)
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
	conversations, err := dst.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if len(conversations) != 0 {
		t.Errorf("dst conversations = %d, want 0", len(conversations))
	}
	obsList, _ := dst.ListData(ctx, "observations/")
	if len(obsList) != 0 {
		t.Errorf("dst observations = %d, want 0", len(obsList))
	}
	if len(dst.Muses) != 0 {
		t.Errorf("dst muses = %d, want 0", len(dst.Muses))
	}

	// Verify output
	output := buf.String()
	if !strings.Contains(output, "Synced 0 conversations") {
		t.Errorf("output missing 'Synced 0 conversations', got: %s", output)
	}
	if !strings.Contains(output, "Synced 0 observations") {
		t.Errorf("output missing 'Synced 0 observations', got: %s", output)
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
	dst.AddConversation("existing", "dst-conv-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "existing message"},
	})
	compose.PutObservations(ctx, dst, "existing", "dst-conv-1", &compose.Observations{
		Items: []compose.Observation{{Observation: "existing observation"}},
	})
	dst.Muses["2024-01-01T00:00:00Z"] = "# Existing Muse"

	// Populate src with different data
	src.AddConversation("test", "src-conv-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "new message"},
	})
	compose.PutObservations(ctx, src, "test", "src-conv-1", &compose.Observations{
		Items: []compose.Observation{{Observation: "new observation"}},
	})
	src.Muses["2024-02-01T00:00:00Z"] = "# New Muse"

	var buf bytes.Buffer
	if err := storage.Sync(ctx, src, dst, nil, &buf); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// Verify dst has original conversation + synced conversation
	conversations, err := dst.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if len(conversations) != 2 {
		t.Errorf("dst conversations = %d, want 2", len(conversations))
	}

	// Verify original dst data preserved
	obs, err := compose.GetObservations(ctx, dst, "existing", "dst-conv-1")
	if err != nil {
		t.Fatalf("GetObservations(existing) error: %v", err)
	}
	if obs.Items[0].Observation != "existing observation" {
		t.Errorf("existing observation lost")
	}
	existingMuse, err := dst.GetMuseVersion(ctx, "2024-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("GetMuseVersion(existing) error: %v", err)
	}
	if existingMuse != "# Existing Muse" {
		t.Errorf("existing muse = %q, want %q", existingMuse, "# Existing Muse")
	}

	// Verify new synced data present
	newObs, err := compose.GetObservations(ctx, dst, "test", "src-conv-1")
	if err != nil {
		t.Fatalf("GetObservations(new) error: %v", err)
	}
	if newObs.Items[0].Observation != "new observation" {
		t.Errorf("synced observation missing")
	}
	newMuse, err := dst.GetMuseVersion(ctx, "2024-02-01T00:00:00Z")
	if err != nil {
		t.Fatalf("GetMuseVersion(new) error: %v", err)
	}
	if newMuse != "# New Muse" {
		t.Errorf("synced muse = %q, want %q", newMuse, "# New Muse")
	}
}
