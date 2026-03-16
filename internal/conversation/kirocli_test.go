package conversation

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func createKiroCLIDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE conversations_v2 (
		key TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		value TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (key, conversation_id)
	)`)
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	return db
}

func insertKiroCLIConv(t *testing.T, db *sql.DB, key, convID, value string, created, updated int64) {
	t.Helper()
	mustExecKiroCLI(t, db, `INSERT INTO conversations_v2 (key, conversation_id, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		key, convID, value, created, updated)
}

func mustExecKiroCLI(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestKiroCLI_BasicSession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.sqlite3")
	db := createKiroCLIDB(t, dbPath)

	conv := kiroCLIConversation{
		History: []kiroCLITurn{
			{
				User: kiroCLIUser{
					Content:   json.RawMessage(`{"Prompt":{"prompt":"fix the bug"}}`),
					Timestamp: "2026-01-15T10:00:00Z",
				},
				Asst:     json.RawMessage(`{"Response":{"message_id":"m1","content":"I fixed the bug in main.go."}}`),
				Metadata: kiroCLIMeta{ModelID: "claude-opus-4.6"},
			},
		},
	}
	value, _ := json.Marshal(conv)
	insertKiroCLIConv(t, db, "/home/user/myapp", "sess-1", string(value), 1705312800000, 1705312900000)
	db.Close()

	t.Setenv("MUSE_KIRO_CLI_DB", dbPath)
	k := &KiroCLI{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]
	if s.Source != "kiro-cli" {
		t.Errorf("Source = %q, want %q", s.Source, "kiro-cli")
	}
	if s.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", s.SessionID, "sess-1")
	}
	if s.Project != "/home/user/myapp" {
		t.Errorf("Project = %q, want %q", s.Project, "/home/user/myapp")
	}
	wantCreated := time.UnixMilli(1705312800000)
	if !s.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt = %v, want %v", s.CreatedAt, wantCreated)
	}
	wantUpdated := time.UnixMilli(1705312900000)
	if !s.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("UpdatedAt = %v, want %v", s.UpdatedAt, wantUpdated)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(s.Messages))
	}
	if s.Messages[0].Role != "user" || s.Messages[0].Content != "fix the bug" {
		t.Errorf("Messages[0] = %+v", s.Messages[0])
	}
	if s.Messages[1].Role != "assistant" || s.Messages[1].Content != "I fixed the bug in main.go." {
		t.Errorf("Messages[1] = %+v", s.Messages[1])
	}
	if s.Messages[1].Model != "claude-opus-4.6" {
		t.Errorf("Messages[1].Model = %q, want %q", s.Messages[1].Model, "claude-opus-4.6")
	}
}

func TestKiroCLI_ToolUseSession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.sqlite3")
	db := createKiroCLIDB(t, dbPath)

	conv := kiroCLIConversation{
		History: []kiroCLITurn{
			{
				User: kiroCLIUser{
					Content:   json.RawMessage(`{"Prompt":{"prompt":"list files"}}`),
					Timestamp: "2026-01-15T10:00:00Z",
				},
				Asst: json.RawMessage(`{"ToolUse":{"message_id":"m1","content":"Let me check.","tool_uses":[{"id":"tu1","name":"execute_bash","orig_name":"execute_bash","args":{},"orig_args":{"command":"ls"}}]}}`),
				Metadata: kiroCLIMeta{ModelID: "claude-opus-4.6"},
			},
			{
				User: kiroCLIUser{
					Content:   json.RawMessage(`{"ToolUseResults":{"tool_use_results":[{"tool_use_id":"tu1","content":[{"Text":"file1\nfile2"}]}]}}`),
					Timestamp: "2026-01-15T10:00:01Z",
				},
				Asst:     json.RawMessage(`{"Response":{"message_id":"m2","content":"There are 2 files."}}`),
				Metadata: kiroCLIMeta{ModelID: "claude-opus-4.6"},
			},
		},
	}
	value, _ := json.Marshal(conv)
	insertKiroCLIConv(t, db, "/home/user/myapp", "sess-2", string(value), 1705312800000, 1705312900000)
	db.Close()

	t.Setenv("MUSE_KIRO_CLI_DB", dbPath)
	k := &KiroCLI{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]
	// Expect: user prompt, assistant tool use, assistant response.
	// ToolUseResults turn produces no user message.
	if len(s.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(s.Messages))
	}
	if s.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want user", s.Messages[0].Role)
	}
	if s.Messages[1].Role != "assistant" || s.Messages[1].Content != "Let me check." {
		t.Errorf("Messages[1] = %+v", s.Messages[1])
	}
	if len(s.Messages[1].ToolCalls) != 1 || s.Messages[1].ToolCalls[0].Name != "execute_bash" {
		t.Errorf("Messages[1].ToolCalls = %+v", s.Messages[1].ToolCalls)
	}
	if s.Messages[2].Role != "assistant" || s.Messages[2].Content != "There are 2 files." {
		t.Errorf("Messages[2] = %+v", s.Messages[2])
	}
}

func TestKiroCLI_EmptyConversationSkipped(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.sqlite3")
	db := createKiroCLIDB(t, dbPath)

	// Conversation with only cancelled tool uses — no real messages.
	conv := kiroCLIConversation{
		History: []kiroCLITurn{
			{
				User: kiroCLIUser{
					Content:   json.RawMessage(`{"CancelledToolUses":{}}`),
					Timestamp: "2026-01-15T10:00:00Z",
				},
				Asst:     json.RawMessage(`{"Response":{"message_id":"m1","content":""}}`),
				Metadata: kiroCLIMeta{ModelID: "claude-opus-4.6"},
			},
		},
	}
	value, _ := json.Marshal(conv)
	insertKiroCLIConv(t, db, "/tmp", "sess-empty", string(value), 1705312800000, 1705312900000)
	db.Close()

	t.Setenv("MUSE_KIRO_CLI_DB", dbPath)
	k := &KiroCLI{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestKiroCLI_MissingDatabase(t *testing.T) {
	t.Setenv("MUSE_KIRO_CLI_DB", filepath.Join(t.TempDir(), "nonexistent.db"))
	k := &KiroCLI{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if sessions != nil {
		t.Errorf("expected nil sessions, got %d", len(sessions))
	}
}

func TestKiroCLI_EmptyDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.sqlite3")
	db := createKiroCLIDB(t, dbPath)
	db.Close()

	t.Setenv("MUSE_KIRO_CLI_DB", dbPath)
	k := &KiroCLI{}
	sessions, err := k.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}
