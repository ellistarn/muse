package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "modernc.org/sqlite"
)

// KiroCLI reads conversations from the kiro-cli SQLite database.
type KiroCLI struct{}

func (k *KiroCLI) Name() string { return "Kiro CLI" }

func (k *KiroCLI) Conversations(_ context.Context, _ func(SyncProgress)) ([]Conversation, error) {
	dbPath := os.Getenv("MUSE_KIRO_CLI_DB")
	if dbPath == "" {
		p, err := defaultKiroCLIDB()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve kiro-cli database: %w", err)
		}
		dbPath = p
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("failed to open kiro-cli database: %w", err)
	}
	defer db.Close()
	return queryKiroCLIConversations(db)
}

func defaultKiroCLIDB() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "kiro-cli", "data.sqlite3"), nil
	}
	// Linux / other: XDG data dir.
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "kiro-cli", "data.sqlite3"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3"), nil
}

func queryKiroCLIConversations(db *sql.DB) ([]Conversation, error) {
	rows, err := db.Query(`
		SELECT key, conversation_id, value, created_at, updated_at
		FROM conversations_v2
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query conversations: %w", err)
	}
	defer rows.Close()

	var conversations []Conversation
	var parseErrs int
	for rows.Next() {
		var project, convID, value string
		var createdMs, updatedMs int64
		if err := rows.Scan(&project, &convID, &value, &createdMs, &updatedMs); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		s, err := parseKiroCLIConversation(project, convID, value, createdMs, updatedMs)
		if err != nil {
			parseErrs++
			continue
		}
		if s == nil {
			continue
		}
		conversations = append(conversations, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate conversations: %w", err)
	}
	if parseErrs > 0 && len(conversations) == 0 {
		return nil, fmt.Errorf("all %d conversations failed to parse", parseErrs)
	}
	return conversations, nil
}

// kiroCLIConversation is the JSON structure stored in conversations_v2.value.
type kiroCLIConversation struct {
	History []kiroCLITurn `json:"history"`
}

type kiroCLITurn struct {
	User     kiroCLIUser     `json:"user"`
	Asst     json.RawMessage `json:"assistant"`
	Metadata kiroCLIMeta     `json:"request_metadata"`
}

type kiroCLIUser struct {
	Content   json.RawMessage `json:"content"`
	Timestamp string          `json:"timestamp"`
}

type kiroCLIMeta struct {
	ModelID string `json:"model_id"`
}

// kiroCLIAssistantResponse is the "Response" variant.
type kiroCLIAssistantResponse struct {
	Content string `json:"content"`
}

// kiroCLIAssistantToolUse is the "ToolUse" variant.
type kiroCLIAssistantToolUse struct {
	Content  string              `json:"content"`
	ToolUses []kiroCLIToolUseRef `json:"tool_uses"`
}

type kiroCLIToolUseRef struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	OrigArgs json.RawMessage `json:"orig_args"`
}

func parseKiroCLIConversation(project, convID, value string, createdMs, updatedMs int64) (*Conversation, error) {
	var conv kiroCLIConversation
	if err := json.Unmarshal([]byte(value), &conv); err != nil {
		return nil, err
	}

	s := &Conversation{
		SchemaVersion:  1,
		Source:         "kiro-cli",
		ConversationID: convID,
		Project:        project,
		CreatedAt:      time.UnixMilli(createdMs),
		UpdatedAt:      time.UnixMilli(updatedMs),
	}

	for _, turn := range conv.History {
		// Parse user message.
		var contentMap map[string]json.RawMessage
		if err := json.Unmarshal(turn.User.Content, &contentMap); err != nil {
			continue
		}
		if raw, ok := contentMap["Prompt"]; ok {
			var prompt struct {
				Prompt string `json:"prompt"`
			}
			if json.Unmarshal(raw, &prompt) == nil && prompt.Prompt != "" {
				msg := Message{Role: "user", Content: prompt.Prompt}
				if ts, err := time.Parse(time.RFC3339Nano, turn.User.Timestamp); err == nil {
					msg.Timestamp = ts
				}
				s.Messages = append(s.Messages, msg)
			}
		}
		// ToolUseResults and CancelledToolUses are internal plumbing — skip.

		// Parse assistant message.
		var asstMap map[string]json.RawMessage
		if err := json.Unmarshal(turn.Asst, &asstMap); err != nil {
			continue
		}
		if raw, ok := asstMap["Response"]; ok {
			var resp kiroCLIAssistantResponse
			if json.Unmarshal(raw, &resp) == nil && resp.Content != "" {
				s.Messages = append(s.Messages, Message{
					Role:    "assistant",
					Content: resp.Content,
					Model:   turn.Metadata.ModelID,
				})
			}
		} else if raw, ok := asstMap["ToolUse"]; ok {
			var tu kiroCLIAssistantToolUse
			if json.Unmarshal(raw, &tu) == nil {
				msg := Message{
					Role:    "assistant",
					Content: tu.Content,
					Model:   turn.Metadata.ModelID,
				}
				for _, ref := range tu.ToolUses {
					msg.ToolCalls = append(msg.ToolCalls, ToolCall{
						Name:  ref.Name,
						Input: ref.OrigArgs,
					})
				}
				if msg.Content != "" || len(msg.ToolCalls) > 0 {
					s.Messages = append(s.Messages, msg)
				}
			}
		}
	}

	if len(s.Messages) == 0 {
		return nil, nil
	}
	return s, nil
}
