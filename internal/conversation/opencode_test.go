package conversation

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// createOpenCodeDB creates a SQLite database with the OpenCode schema at the given path.
func createOpenCodeDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE project (id TEXT PRIMARY KEY, worktree TEXT)`,
		`CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, parent_id TEXT, project_id TEXT, time_created INTEGER, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, data TEXT, time_created INTEGER)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, data TEXT, time_created INTEGER)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create schema: %v", err)
		}
	}
	return db
}

func TestOpenCode_BasicSession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	db := createOpenCodeDB(t, dbPath)

	// Insert project.
	mustExec(t, db, `INSERT INTO project (id, worktree) VALUES (?, ?)`, "proj-1", "/home/user/myapp")

	// Insert session.
	mustExec(t, db, `INSERT INTO session (id, title, parent_id, project_id, time_created, time_updated) VALUES (?, ?, NULL, ?, ?, ?)`,
		"sess-1", "Fix bug", "proj-1", 1705312800000, 1705312900000)

	// Insert user message with 1 text part.
	mustExec(t, db, `INSERT INTO message (id, session_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"msg-1", "sess-1", `{"role":"user","modelID":"","providerID":""}`, 1705312800000)
	mustExec(t, db, `INSERT INTO part (id, message_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"part-1", "msg-1", `{"type":"text","text":"fix the login bug"}`, 1705312800000)

	// Insert assistant message with 1 text part and 1 tool part.
	mustExec(t, db, `INSERT INTO message (id, session_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"msg-2", "sess-1", `{"role":"assistant","modelID":"claude-sonnet","providerID":"anthropic"}`, 1705312801000)
	mustExec(t, db, `INSERT INTO part (id, message_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"part-2", "msg-2", `{"type":"text","text":"I'll fix the login handler."}`, 1705312801000)
	mustExec(t, db, `INSERT INTO part (id, message_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"part-3", "msg-2", `{"type":"tool","tool":"Edit","callID":"call-1","state":{"input":{"file":"login.go"},"output":"done"}}`, 1705312802000)

	db.Close()

	t.Setenv("MUSE_OPENCODE_DB", dbPath)
	oc := &OpenCode{}
	sessions, err := oc.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]

	// Source.
	if s.Source != "opencode" {
		t.Errorf("Source = %q, want %q", s.Source, "opencode")
	}
	// Title.
	if s.Title != "Fix bug" {
		t.Errorf("Title = %q, want %q", s.Title, "Fix bug")
	}
	// Project.
	if s.Project != "/home/user/myapp" {
		t.Errorf("Project = %q, want %q", s.Project, "/home/user/myapp")
	}
	// CreatedAt.
	wantCreated := time.UnixMilli(1705312800000)
	if !s.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt = %v, want %v", s.CreatedAt, wantCreated)
	}
	// UpdatedAt.
	wantUpdated := time.UnixMilli(1705312900000)
	if !s.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("UpdatedAt = %v, want %v", s.UpdatedAt, wantUpdated)
	}

	// Messages.
	if len(s.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(s.Messages))
	}

	// User message.
	m0 := s.Messages[0]
	if m0.Role != "user" {
		t.Errorf("Messages[0].Role = %q, want %q", m0.Role, "user")
	}
	if m0.Content != "fix the login bug" {
		t.Errorf("Messages[0].Content = %q, want %q", m0.Content, "fix the login bug")
	}

	// Assistant message.
	m1 := s.Messages[1]
	if m1.Role != "assistant" {
		t.Errorf("Messages[1].Role = %q, want %q", m1.Role, "assistant")
	}
	if m1.Content != "I'll fix the login handler." {
		t.Errorf("Messages[1].Content = %q, want %q", m1.Content, "I'll fix the login handler.")
	}
	if m1.Model != "claude-sonnet" {
		t.Errorf("Messages[1].Model = %q, want %q", m1.Model, "claude-sonnet")
	}
	if len(m1.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(m1.ToolCalls))
	}
	if m1.ToolCalls[0].Name != "Edit" {
		t.Errorf("ToolCalls[0].Name = %q, want %q", m1.ToolCalls[0].Name, "Edit")
	}
}

func TestOpenCode_ParentChildRelationship(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	db := createOpenCodeDB(t, dbPath)

	mustExec(t, db, `INSERT INTO project (id, worktree) VALUES (?, ?)`, "proj-1", "/home/user/myapp")

	// Parent session.
	mustExec(t, db, `INSERT INTO session (id, title, parent_id, project_id, time_created, time_updated) VALUES (?, ?, NULL, ?, ?, ?)`,
		"parent-1", "Parent task", "proj-1", 1705312800000, 1705312900000)
	mustExec(t, db, `INSERT INTO message (id, session_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"msg-p1", "parent-1", `{"role":"user","modelID":"","providerID":""}`, 1705312800000)
	mustExec(t, db, `INSERT INTO part (id, message_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"part-p1", "msg-p1", `{"type":"text","text":"do the thing"}`, 1705312800000)

	// Child session.
	mustExec(t, db, `INSERT INTO session (id, title, parent_id, project_id, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)`,
		"child-1", "Child task", "parent-1", "proj-1", 1705312850000, 1705312950000)
	mustExec(t, db, `INSERT INTO message (id, session_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"msg-c1", "child-1", `{"role":"assistant","modelID":"claude-sonnet","providerID":"anthropic"}`, 1705312850000)
	mustExec(t, db, `INSERT INTO part (id, message_id, data, time_created) VALUES (?, ?, ?, ?)`,
		"part-c1", "msg-c1", `{"type":"text","text":"on it"}`, 1705312850000)

	db.Close()

	t.Setenv("MUSE_OPENCODE_DB", dbPath)
	oc := &OpenCode{}
	sessions, err := oc.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Build a map for easy lookup.
	byID := map[string]Session{}
	for _, s := range sessions {
		byID[s.SessionID] = s
	}

	parent := byID["parent-1"]
	child := byID["child-1"]

	// Parent should list child in SubagentIDs.
	if len(parent.SubagentIDs) != 1 || parent.SubagentIDs[0] != "child-1" {
		t.Errorf("parent SubagentIDs = %v, want [child-1]", parent.SubagentIDs)
	}

	// Child should reference parent.
	if child.ParentID != "parent-1" {
		t.Errorf("child ParentID = %q, want %q", child.ParentID, "parent-1")
	}
}

func TestOpenCode_EmptyDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	db := createOpenCodeDB(t, dbPath)
	db.Close()

	t.Setenv("MUSE_OPENCODE_DB", dbPath)
	oc := &OpenCode{}
	sessions, err := oc.Sessions()
	if err != nil {
		t.Fatalf("Sessions() error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestOpenCode_MissingDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "does-not-exist.db")

	t.Setenv("MUSE_OPENCODE_DB", dbPath)
	oc := &OpenCode{}
	sessions, err := oc.Sessions()
	if err != nil {
		t.Fatalf("expected nil error for missing db, got: %v", err)
	}
	if sessions != nil {
		t.Errorf("expected nil sessions, got %v", sessions)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
