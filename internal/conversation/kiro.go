package conversation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Kiro reads sessions from Kiro's workspace session files.
type Kiro struct{}

func (k *Kiro) Name() string { return "Kiro" }

func (k *Kiro) Sessions() ([]Session, error) {
	kiroDir := os.Getenv("MUSE_KIRO_DIR")
	if kiroDir == "" {
		dir, err := defaultKiroDir()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve kiro directory: %w", err)
		}
		kiroDir = dir
	}
	if _, err := os.Stat(kiroDir); os.IsNotExist(err) {
		return nil, nil
	}
	workspaceDirs, err := os.ReadDir(kiroDir)
	if err != nil {
		return nil, nil
	}
	var sessions []Session
	for _, wd := range workspaceDirs {
		if !wd.IsDir() {
			continue
		}
		wsPath := filepath.Join(kiroDir, wd.Name())
		index, err := loadKiroIndex(filepath.Join(wsPath, "sessions.json"))
		if err != nil {
			continue
		}
		for _, entry := range index {
			sessionPath := filepath.Join(wsPath, entry.SessionID+".json")
			session, err := parseKiroSession(sessionPath, entry)
			if err != nil || session == nil {
				continue
			}
			sessions = append(sessions, *session)
		}
	}
	return sessions, nil
}

func defaultKiroDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "Kiro", "User",
		"globalStorage", "kiro.kiroagent", "workspace-sessions"), nil
}

type kiroIndexEntry struct {
	SessionID          string `json:"sessionId"`
	Title              string `json:"title"`
	DateCreated        string `json:"dateCreated"`
	WorkspaceDirectory string `json:"workspaceDirectory"`
}

func loadKiroIndex(path string) ([]kiroIndexEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []kiroIndexEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// kiroSessionFile is the top-level structure of a Kiro session JSON file.
type kiroSessionFile struct {
	History   []kiroHistoryEntry `json:"history"`
	Title     string             `json:"title"`
	SessionID string             `json:"sessionId"`
}

type kiroHistoryEntry struct {
	Message kiroMessage `json:"message"`
}

type kiroMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	ID      string          `json:"id"`
}

func parseKiroSession(path string, index kiroIndexEntry) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file kiroSessionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}

	session := &Session{
		SchemaVersion: 1,
		Source:        "kiro",
		SessionID:     index.SessionID,
		Project:       index.WorkspaceDirectory,
		Title:         index.Title,
	}

	// Parse creation time from index (Unix milliseconds as string).
	if ms, err := strconv.ParseInt(index.DateCreated, 10, 64); err == nil {
		session.CreatedAt = time.UnixMilli(ms)
	}

	// Use file modification time as UpdatedAt.
	if info, err := os.Stat(path); err == nil {
		session.UpdatedAt = info.ModTime()
	}

	for _, entry := range file.History {
		if entry.Message.Role != "user" && entry.Message.Role != "assistant" {
			continue
		}
		msg := Message{Role: entry.Message.Role}
		msg.Content = parseKiroContent(entry.Message.Content)
		if msg.Content == "" {
			continue
		}
		session.Messages = append(session.Messages, msg)
	}
	if len(session.Messages) == 0 {
		return nil, nil
	}
	return session, nil
}

// parseKiroContent handles Kiro's two content formats:
// - User messages: array of {type: "text", text: "..."} blocks
// - Assistant messages: plain string
func parseKiroContent(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	// Try as string first (assistant messages).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try as array of content blocks (user messages).
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
