// Package importer executes external muse plugins and imports their output as
// conversations. See designs/010-import.md for the full design.
package importer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

const pluginPrefix = "muse-"

// Result describes the outcome of a single plugin import.
type Result struct {
	Source   string
	Imported int
	Skipped  int
	Rejected int
	Warnings []string
}

// Run executes a single import plugin by source name. The source name is resolved
// to muse-{name} on $PATH. Returns an error if the plugin is not found, fails to
// execute, or does not write valid source metadata.
func Run(ctx context.Context, store storage.Store, name string, stderr io.Writer) (*Result, error) {
	binName := pluginPrefix + name
	binPath, err := exec.LookPath(binName)
	if err != nil {
		return nil, fmt.Errorf("plugin %q not found on $PATH (looking for %q): %w", name, binName, err)
	}

	// Create temp output directory
	tmpDir, err := os.MkdirTemp("", "muse-import-"+name+"-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Execute the plugin
	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = append(os.Environ(), "MUSE_OUTPUT_DIR="+tmpDir)

	// Capture stderr and stream it with a prefix
	cmdStderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin %q: %w", binName, err)
	}

	// Stream stderr with prefix
	prefix := fmt.Sprintf("[%s] ", name)
	scanner := bufio.NewScanner(cmdStderr)
	for scanner.Scan() {
		fmt.Fprintf(stderr, "%s%s\n", prefix, scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("plugin %q failed: %w", binName, err)
	}

	// Read and validate source metadata
	meta, err := readSourceMetadata(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("plugin %q: %w", binName, err)
	}

	// Read, validate, and store conversations
	result := &Result{Source: name}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("read output dir: %w", err)
	}

	// Build a set of existing conversations for this source so we can diff
	existing, err := store.ListConversations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list existing conversations: %w", err)
	}
	remote := make(map[string]storage.ConversationEntry)
	for _, e := range existing {
		if e.Source == name {
			remote[e.ConversationID] = e
		}
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fname := entry.Name()
		// Skip the metadata file
		if fname == ".muse-source.json" {
			continue
		}
		if !strings.HasSuffix(fname, ".json") {
			continue
		}

		fpath := filepath.Join(tmpDir, fname)
		conv, err := readAndValidateConversation(fpath)
		if err != nil {
			result.Rejected++
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", fname, err))
			continue
		}

		// Overwrite source to match import name — muse is the system of record
		conv.Source = name

		// Diff: skip if unchanged
		if entry, exists := remote[conv.ConversationID]; exists {
			if !conv.UpdatedAt.After(entry.LastModified) {
				result.Skipped++
				continue
			}
		}

		if _, err := store.PutConversation(ctx, conv); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("store %s: %v", conv.ConversationID, err))
			continue
		}
		result.Imported++
	}

	// Persist source metadata so compose can read it
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal source metadata: %w", err)
	}
	if err := store.PutData(ctx, conversation.SourceMetadataKey(name), metaBytes); err != nil {
		return nil, fmt.Errorf("store source metadata: %w", err)
	}

	// Activate the source for compose
	if err := compose.EnsureSourceDir(ctx, store, name); err != nil {
		return nil, fmt.Errorf("activate source: %w", err)
	}

	return result, nil
}

// RunAll re-imports all previously imported sources. A source is considered
// previously imported if it has a conversations directory and is not a built-in
// source. Returns results for each source attempted. Missing plugins produce a
// warning, not an error.
func RunAll(ctx context.Context, store storage.Store, stderr io.Writer) ([]*Result, error) {
	sources, err := ListImportedSources(ctx, store)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}

	var results []*Result
	for _, name := range sources {
		result, err := Run(ctx, store, name, stderr)
		if err != nil {
			// Check if it's a PATH resolution failure — warn and continue
			binName := pluginPrefix + name
			if _, lookErr := exec.LookPath(binName); lookErr != nil {
				results = append(results, &Result{
					Source:   name,
					Warnings: []string{fmt.Sprintf("plugin %q no longer on $PATH, skipping (previously imported data preserved)", binName)},
				})
				continue
			}
			return nil, fmt.Errorf("import %s: %w", name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

// ListImportedSources returns source names that have conversations in storage
// but are not built-in sources. These are sources created by prior plugin imports.
func ListImportedSources(ctx context.Context, store storage.Store) ([]string, error) {
	entries, err := store.ListConversations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}

	builtin := conversation.BuiltinSourceNames()
	seen := make(map[string]bool)
	for _, e := range entries {
		if !builtin[e.Source] {
			seen[e.Source] = true
		}
	}

	var sources []string
	for s := range seen {
		sources = append(sources, s)
	}
	return sources, nil
}

// readSourceMetadata reads and validates .muse-source.json from the output directory.
func readSourceMetadata(dir string) (*conversation.SourceMetadata, error) {
	path := filepath.Join(dir, ".muse-source.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("missing required .muse-source.json (plugin must declare source type)")
		}
		return nil, fmt.Errorf("read .muse-source.json: %w", err)
	}

	var meta conversation.SourceMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse .muse-source.json: %w", err)
	}

	switch meta.Type {
	case "human", "ai":
		// valid
	case "":
		return nil, fmt.Errorf(".muse-source.json missing required field \"type\" (must be \"human\" or \"ai\")")
	default:
		return nil, fmt.Errorf(".muse-source.json has invalid type %q (must be \"human\" or \"ai\")", meta.Type)
	}

	return &meta, nil
}

// readAndValidateConversation reads a JSON file and validates it as a Conversation.
func readAndValidateConversation(path string) (*conversation.Conversation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var conv conversation.Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// ConversationID is the only hard requirement — Source will be overwritten
	if conv.ConversationID == "" {
		return nil, fmt.Errorf("missing required field \"conversation_id\"")
	}

	return &conv, nil
}
