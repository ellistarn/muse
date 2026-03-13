package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultClaudeDir = ".claude"

// ClaudeCode reads sessions from Claude Code's JSONL files.
type ClaudeCode struct{}

func (c *ClaudeCode) Name() string { return "Claude Code" }

func (c *ClaudeCode) Sessions() ([]Session, error) {
	claudeDir := os.Getenv("MUSE_CLAUDE_DIR")
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		claudeDir = filepath.Join(home, defaultClaudeDir)
	}
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		return nil, nil
	}
	titles := loadClaudeHistory(filepath.Join(claudeDir, "history.jsonl"))
	projectsDir := filepath.Join(claudeDir, "projects")
	projectDirs, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, nil // no projects directory
	}

	var sessions []Session
	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}
		projectPath := filepath.Join(projectsDir, pd.Name())
		entries, err := os.ReadDir(projectPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			sessionID := strings.TrimSuffix(name, ".jsonl")
			sessionPath := filepath.Join(projectPath, name)
			session, err := parseClaudeSession(sessionPath, sessionID, titles)
			if err != nil || session == nil {
				continue
			}
			// Look for subagent files.
			subagentDir := filepath.Join(projectPath, sessionID, "subagents")
			if subEntries, err := os.ReadDir(subagentDir); err == nil {
				for _, se := range subEntries {
					if !strings.HasSuffix(se.Name(), ".jsonl") {
						continue
					}
					agentID := strings.TrimSuffix(se.Name(), ".jsonl")
					subPath := filepath.Join(subagentDir, se.Name())
					sub, err := parseClaudeSession(subPath, agentID, titles)
					if err != nil || sub == nil {
						continue
					}
					sub.ParentID = session.SessionID
					session.SubagentIDs = append(session.SubagentIDs, agentID)
					sessions = append(sessions, *sub)
				}
			}
			sessions = append(sessions, *session)
		}
	}
	return sessions, nil
}

type claudeHistoryEntry struct {
	Display   string `json:"display"`
	SessionID string `json:"sessionId"`
}

// loadClaudeHistory builds a map of sessionID -> first user prompt for titles.
func loadClaudeHistory(path string) map[string]string {
	titles := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return titles
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var entry claudeHistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		// Keep the first entry per session as the title.
		if _, exists := titles[entry.SessionID]; !exists && entry.Display != "" {
			titles[entry.SessionID] = truncate(entry.Display, 100)
		}
	}
	return titles
}

type claudeEvent struct {
	Type       string          `json:"type"`
	UUID       string          `json:"uuid"`
	ParentUUID string          `json:"parentUuid"`
	SessionID  string          `json:"sessionId"`
	Timestamp  string          `json:"timestamp"`
	CWD        string          `json:"cwd"`
	Message    json.RawMessage `json:"message"`
}

type claudeMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Model      string          `json:"model"`
	StopReason *string         `json:"stop_reason"`
}

// claudeContentBlock represents one block in an assistant message's content array.
type claudeContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	ID    string          `json:"id"`
}

func parseClaudeSession(path, sessionID string, titles map[string]string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	session := &Session{
		SchemaVersion: 1,
		Source:        "claude-code",
		SessionID:     sessionID,
	}

	var firstTime, lastTime time.Time
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024) // 10MB buffer for large lines
	for scanner.Scan() {
		var event claudeEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		// Track timestamps.
		if event.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, event.Timestamp); err == nil {
				if firstTime.IsZero() || t.Before(firstTime) {
					firstTime = t
				}
				if t.After(lastTime) {
					lastTime = t
				}
			}
		}
		// Extract project from cwd.
		if session.Project == "" && event.CWD != "" {
			session.Project = event.CWD
		}
		if event.Type != "user" && event.Type != "assistant" {
			continue
		}
		if event.Message == nil {
			continue
		}
		var cm claudeMessage
		if err := json.Unmarshal(event.Message, &cm); err != nil {
			continue
		}
		// For assistant messages, skip streaming partials (which have no stop reason).
		if event.Type == "assistant" && cm.StopReason == nil {
			continue
		}
		msg := Message{
			Role:  cm.Role,
			Model: cm.Model,
		}
		if event.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, event.Timestamp); err == nil {
				msg.Timestamp = t
			}
		}
		// Parse content: string for user, array of blocks for assistant.
		msg.Content, msg.ToolCalls = parseClaudeContent(cm.Content)
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		session.Messages = append(session.Messages, msg)
	}

	if len(session.Messages) == 0 {
		return nil, nil
	}
	session.CreatedAt = firstTime
	session.UpdatedAt = lastTime
	if title, ok := titles[sessionID]; ok {
		session.Title = title
	} else if len(session.Messages) > 0 {
		session.Title = truncate(session.Messages[0].Content, 100)
	}
	return session, nil
}

func parseClaudeContent(raw json.RawMessage) (string, []ToolCall) {
	if raw == nil {
		return "", nil
	}
	// Try as string first (user messages).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	// Try as array of content blocks (assistant messages).
	var blocks []claudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return string(raw), nil
	}
	var text []string
	var tools []ToolCall
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			tools = append(tools, ToolCall{
				Name:  b.Name,
				Input: b.Input,
			})
		}
	}
	return strings.Join(text, "\n"), tools
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
