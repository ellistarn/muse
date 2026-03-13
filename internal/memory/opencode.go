package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const defaultOpenCodeDB = ".local/share/opencode/opencode.db"

// OpenCode reads sessions from the OpenCode SQLite database.
type OpenCode struct{}

func (o *OpenCode) Name() string { return "OpenCode" }

func (o *OpenCode) Sessions() ([]Session, error) {
	dbPath := os.Getenv("MUSE_OPENCODE_DB")
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		dbPath = filepath.Join(home, defaultOpenCodeDB)
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil // no database, no sessions
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open OpenCode database: %w", err)
	}
	defer db.Close()
	return queryOpenCodeSessions(db)
}

func queryOpenCodeSessions(db *sql.DB) ([]Session, error) {
	rows, err := db.Query(`
		SELECT s.id, s.title, s.parent_id, s.time_created, s.time_updated, p.worktree
		FROM session s
		JOIN project p ON s.project_id = p.id
		ORDER BY s.time_updated DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer rows.Close()

	// Build sessions and track parent-child relationships.
	var sessions []Session
	children := map[string][]string{} // parent_id -> []child_id
	for rows.Next() {
		var id, title, worktree string
		var parentID sql.NullString
		var created, updated int64
		if err := rows.Scan(&id, &title, &parentID, &created, &updated, &worktree); err != nil {
			return nil, fmt.Errorf("failed to scan session row: %w", err)
		}
		s := Session{
			SchemaVersion: 1,
			Source:        "opencode",
			SessionID:     id,
			Title:         title,
			Project:       worktree,
			CreatedAt:     time.UnixMilli(created),
			UpdatedAt:     time.UnixMilli(updated),
		}
		if parentID.Valid {
			s.ParentID = parentID.String
			children[parentID.String] = append(children[parentID.String], id)
		}
		messages, err := queryOpenCodeMessages(db, id)
		if err != nil {
			return nil, fmt.Errorf("failed to query messages for session %s: %w", id, err)
		}
		s.Messages = messages
		sessions = append(sessions, s)
	}
	// Wire up subagent IDs on parent sessions.
	for i := range sessions {
		if ids, ok := children[sessions[i].SessionID]; ok {
			sessions[i].SubagentIDs = ids
		}
	}
	return sessions, nil
}

func queryOpenCodeMessages(db *sql.DB, sessionID string) ([]Message, error) {
	rows, err := db.Query(`
		SELECT m.id, m.data, m.time_created
		FROM message m
		WHERE m.session_id = ?
		ORDER BY m.time_created ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msgID, data string
		var created int64
		if err := rows.Scan(&msgID, &data, &created); err != nil {
			return nil, fmt.Errorf("failed to scan message row: %w", err)
		}
		msg, err := parseOpenCodeMessage(db, msgID, data, created)
		if err != nil {
			continue // skip unparseable messages
		}
		if msg != nil {
			messages = append(messages, *msg)
		}
	}
	return messages, nil
}

// openCodeMessageData is the JSON structure in the message.data column.
type openCodeMessageData struct {
	Role       string `json:"role"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
}

// openCodePartData is the JSON structure in the part.data column.
type openCodePartData struct {
	Type   string          `json:"type"`
	Text   string          `json:"text"`
	Tool   string          `json:"tool"`
	CallID string          `json:"callID"`
	State  json.RawMessage `json:"state"`
}

type openCodeToolState struct {
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
}

func parseOpenCodeMessage(db *sql.DB, msgID, data string, created int64) (*Message, error) {
	var md openCodeMessageData
	if err := json.Unmarshal([]byte(data), &md); err != nil {
		return nil, err
	}
	if md.Role != "user" && md.Role != "assistant" {
		return nil, nil
	}

	// Read parts to build content and tool calls.
	partRows, err := db.Query(`
		SELECT data FROM part WHERE message_id = ? ORDER BY time_created ASC
	`, msgID)
	if err != nil {
		return nil, err
	}
	defer partRows.Close()

	msg := &Message{
		Role:      md.Role,
		Timestamp: time.UnixMilli(created),
		Model:     md.ModelID,
	}

	var textParts []byte
	for partRows.Next() {
		var partData string
		if err := partRows.Scan(&partData); err != nil {
			continue
		}
		var pd openCodePartData
		if err := json.Unmarshal([]byte(partData), &pd); err != nil {
			continue
		}
		switch pd.Type {
		case "text":
			if len(textParts) > 0 {
				textParts = append(textParts, '\n')
			}
			textParts = append(textParts, pd.Text...)
		case "tool":
			tc := ToolCall{Name: pd.Tool}
			if pd.State != nil {
				var state openCodeToolState
				if err := json.Unmarshal(pd.State, &state); err == nil {
					tc.Input = state.Input
					// Output can be a string or JSON. Store as raw JSON.
					if state.Output != nil {
						tc.Output = state.Output
					}
				}
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
	}
	msg.Content = string(textParts)
	return msg, nil
}
