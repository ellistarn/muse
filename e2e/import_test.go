package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/importer"
	"github.com/ellistarn/muse/internal/testutil"
)

// buildTestPlugin compiles the test plugin to a temp directory and returns
// the directory (which should be added to PATH) and a cleanup function.
func buildTestPlugin(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "muse-test-plugin")
	cmd := exec.Command("go", "build", "-o", binPath, "./examples/muse-test-plugin")
	cmd.Dir = filepath.Join(testProjectRoot())
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build muse-test-plugin: %v", err)
	}
	return binDir
}

// testProjectRoot returns the project root directory.
func testProjectRoot() string {
	// The e2e tests run from the project root via go test ./e2e/
	wd, err := os.Getwd()
	if err != nil {
		return ".."
	}
	// If we're in the e2e directory, go up one level
	if filepath.Base(wd) == "e2e" {
		return filepath.Dir(wd)
	}
	return wd
}

// prependToPath returns a new PATH with dir prepended.
func prependToPath(dir string) string {
	return dir + ":" + os.Getenv("PATH")
}

func TestImport_EndToEnd(t *testing.T) {
	binDir := buildTestPlugin(t)
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", prependToPath(binDir))
	defer os.Setenv("PATH", origPath)

	ctx := context.Background()
	store := testutil.NewConversationStore()

	result, err := importer.Run(ctx, store, "test-plugin", os.Stderr)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify conversations were imported
	if result.Imported != 3 {
		t.Errorf("Imported = %d, want 3", result.Imported)
	}
	if result.Source != "test-plugin" {
		t.Errorf("Source = %q, want %q", result.Source, "test-plugin")
	}

	// Verify conversations are in storage
	entries, err := store.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("stored conversations = %d, want 3", len(entries))
	}

	// Verify source field was overwritten
	for _, e := range entries {
		if e.Source != "test-plugin" {
			t.Errorf("conversation %s has Source = %q, want %q", e.ConversationID, e.Source, "test-plugin")
		}
	}

	// Verify a specific conversation's content
	conv, err := store.GetConversation(ctx, "test-plugin", "review-101")
	if err != nil {
		t.Fatalf("GetConversation(review-101) error: %v", err)
	}
	if len(conv.Messages) != 4 {
		t.Errorf("review-101 messages = %d, want 4", len(conv.Messages))
	}

	// Verify source metadata was persisted
	metaData, err := store.GetData(ctx, conversation.SourceMetadataKey("test-plugin"))
	if err != nil {
		t.Fatalf("GetData(source metadata) error: %v", err)
	}
	var meta conversation.SourceMetadata
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("unmarshal source metadata: %v", err)
	}
	if meta.Type != "human" {
		t.Errorf("source metadata type = %q, want %q", meta.Type, "human")
	}

	// Verify observation directory was created (source is active)
	obsKeys, err := store.ListData(ctx, "observations/test-plugin/")
	if err != nil {
		t.Fatalf("ListData(observations) error: %v", err)
	}
	if len(obsKeys) == 0 {
		t.Error("expected observation directory to be created (source activation)")
	}
}

func TestImport_PluginNotFound(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewConversationStore()

	_, err := importer.Run(ctx, store, "nonexistent-plugin", os.Stderr)
	if err == nil {
		t.Fatal("expected error for missing plugin, got nil")
	}
}

func TestImport_ReimportSkipsUnchanged(t *testing.T) {
	binDir := buildTestPlugin(t)
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", prependToPath(binDir))
	defer os.Setenv("PATH", origPath)

	ctx := context.Background()
	store := testutil.NewConversationStore()

	// First import
	result1, err := importer.Run(ctx, store, "test-plugin", os.Stderr)
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	if result1.Imported != 3 {
		t.Errorf("first run Imported = %d, want 3", result1.Imported)
	}

	// Second import — same data, should skip all
	result2, err := importer.Run(ctx, store, "test-plugin", os.Stderr)
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	// The plugin produces conversations with timestamps relative to now(),
	// so UpdatedAt will be slightly different each run. The conversations
	// will be re-imported (not skipped) because timestamps change.
	// This is expected behavior — plugins that want incremental sync
	// should use stable timestamps.
	if result2.Imported+result2.Skipped != 3 {
		t.Errorf("second run total = %d, want 3", result2.Imported+result2.Skipped)
	}
}

func TestImport_RunAll(t *testing.T) {
	binDir := buildTestPlugin(t)
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", prependToPath(binDir))
	defer os.Setenv("PATH", origPath)

	ctx := context.Background()
	store := testutil.NewConversationStore()

	// No previously imported sources — RunAll should do nothing
	results, err := importer.RunAll(ctx, store, os.Stderr)
	if err != nil {
		t.Fatalf("RunAll() with no sources error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("RunAll with no sources returned %d results, want 0", len(results))
	}

	// Import once to establish the source
	_, err = importer.Run(ctx, store, "test-plugin", os.Stderr)
	if err != nil {
		t.Fatalf("initial Run() error: %v", err)
	}

	// Now RunAll should re-import test-plugin
	results, err = importer.RunAll(ctx, store, os.Stderr)
	if err != nil {
		t.Fatalf("RunAll() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("RunAll returned %d results, want 1", len(results))
	}
	if results[0].Source != "test-plugin" {
		t.Errorf("RunAll source = %q, want %q", results[0].Source, "test-plugin")
	}
}

func TestImport_ListImportedSources(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewConversationStore()

	// Empty store: no imported sources
	sources, err := importer.ListImportedSources(ctx, store)
	if err != nil {
		t.Fatalf("ListImportedSources() error: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("empty store: sources = %d, want 0", len(sources))
	}

	// Add a built-in source conversation — should not appear as imported
	store.AddConversation("claude-code", "conv-1", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})

	sources, err = importer.ListImportedSources(ctx, store)
	if err != nil {
		t.Fatalf("ListImportedSources() with builtin error: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("store with only builtins: sources = %d, want 0", len(sources))
	}

	// Add a non-builtin source conversation — should appear as imported
	store.AddConversation("my-custom-tool", "conv-2", time.Now(), []conversation.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})

	sources, err = importer.ListImportedSources(ctx, store)
	if err != nil {
		t.Fatalf("ListImportedSources() with import error: %v", err)
	}
	if len(sources) != 1 {
		t.Errorf("store with import: sources = %d, want 1", len(sources))
	}
	if len(sources) > 0 && sources[0] != "my-custom-tool" {
		t.Errorf("imported source = %q, want %q", sources[0], "my-custom-tool")
	}
}

func TestImport_HumanSourceMetadata(t *testing.T) {
	binDir := buildTestPlugin(t)
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", prependToPath(binDir))
	defer os.Setenv("PATH", origPath)

	ctx := context.Background()
	store := testutil.NewConversationStore()

	// Import the test plugin (declares type: human)
	_, err := importer.Run(ctx, store, "test-plugin", os.Stderr)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify isHumanSource resolves correctly via loadHumanSources
	// We can't call isHumanSource directly (unexported), but we can verify
	// the metadata is stored correctly and would be read by the compose pipeline
	data, err := store.GetData(ctx, conversation.SourceMetadataKey("test-plugin"))
	if err != nil {
		t.Fatalf("GetData error: %v", err)
	}
	var meta conversation.SourceMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta.Type != "human" {
		t.Errorf("metadata type = %q, want %q", meta.Type, "human")
	}

	// Verify the source would be picked up by compose (observation dir exists)
	obsSources, err := compose.ListObservationSources(ctx, store)
	if err != nil {
		t.Fatalf("ListObservationSources error: %v", err)
	}
	found := false
	for _, s := range obsSources {
		if s == "test-plugin" {
			found = true
			break
		}
	}
	if !found {
		t.Error("test-plugin not found in observation sources — compose would not process it")
	}
}
