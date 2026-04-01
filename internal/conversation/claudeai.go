package conversation

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ClaudeAI reads conversations from a claude.ai data export.
//
// To export: claude.ai → Settings → Export Data. Place the downloaded zip
// (or extracted conversations.json) at ~/.muse/imports/claude-ai/.
// Set MUSE_CLAUDE_AI_DIR to override the directory.
type ClaudeAI struct{}

func (c *ClaudeAI) Name() string { return "Claude AI" }

func (c *ClaudeAI) Conversations(_ context.Context, _ func(SyncProgress)) ([]Conversation, error) {
	dir := os.Getenv("MUSE_CLAUDE_AI_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		dir = filepath.Join(home, ".muse", "imports", "claude-ai")
	}

	data, err := loadClaudeAIExport(dir)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	var raw []claudeAIConversation
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse claude-ai export: %w", err)
	}

	var conversations []Conversation
	for _, r := range raw {
		conv := parseClaudeAIConversation(r)
		if conv != nil {
			conversations = append(conversations, *conv)
		}
	}

	sort.Slice(conversations, func(i, j int) bool {
		return conversations[i].UpdatedAt.After(conversations[j].UpdatedAt)
	})
	return conversations, nil
}

// loadClaudeAIExport finds and reads conversations.json from dir. It checks
// for a bare conversations.json first, then looks for any .zip file containing
// conversations.json. Returns nil, nil if nothing is found.
func loadClaudeAIExport(dir string) ([]byte, error) {
	// Try bare JSON first.
	jsonPath := filepath.Join(dir, "conversations.json")
	if _, err := os.Stat(jsonPath); err == nil {
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			return nil, fmt.Errorf("read claude-ai export: %w", err)
		}
		return data, nil
	}

	// Look for a zip file containing conversations.json.
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read claude-ai dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".zip") {
			continue
		}
		data, err := readJSONFromZip(filepath.Join(dir, entry.Name()), "conversations.json")
		if err != nil {
			return nil, err
		}
		if data != nil {
			return data, nil
		}
	}

	return nil, nil
}

// readJSONFromZip reads a named file from a zip archive. Returns nil, nil if
// the target file is not found in the archive.
func readJSONFromZip(zipPath, target string) ([]byte, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	for _, f := range r.File {
		// Match by basename so it works whether the file is at the root or nested.
		if filepath.Base(f.Name) != target {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read %s from zip: %w", target, err)
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, nil
}

type claudeAIConversation struct {
	UUID         string                `json:"uuid"`
	Name         string                `json:"name"`
	CreatedAt    string                `json:"created_at"`
	UpdatedAt    string                `json:"updated_at"`
	ChatMessages []claudeAIChatMessage `json:"chat_messages"`
}

type claudeAIChatMessage struct {
	UUID      string `json:"uuid"`
	Text      string `json:"text"`
	Sender    string `json:"sender"`
	CreatedAt string `json:"created_at"`
}

func parseClaudeAIConversation(raw claudeAIConversation) *Conversation {
	if raw.UUID == "" || len(raw.ChatMessages) == 0 {
		return nil
	}

	conv := &Conversation{
		SchemaVersion:  1,
		Source:         "claude-ai",
		ConversationID: raw.UUID,
		Title:          raw.Name,
	}

	if t, err := time.Parse(time.RFC3339Nano, raw.CreatedAt); err == nil {
		conv.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, raw.UpdatedAt); err == nil {
		conv.UpdatedAt = t
	}

	for _, m := range raw.ChatMessages {
		if m.Text == "" {
			continue
		}
		role := "user"
		if m.Sender == "assistant" {
			role = "assistant"
		}
		msg := Message{
			Role:    role,
			Content: m.Text,
		}
		if t, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err == nil {
			msg.Timestamp = t
		}
		conv.Messages = append(conv.Messages, msg)
	}

	if len(conv.Messages) == 0 {
		return nil
	}

	if conv.Title == "" {
		conv.Title = truncate(conv.Messages[0].Content, 100)
	}

	return conv
}
