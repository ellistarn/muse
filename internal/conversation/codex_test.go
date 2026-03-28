package conversation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodex_BasicConversation(t *testing.T) {
	dir := t.TempDir()
	writeCodexSessionIndex(t, dir,
		`{"id":"sess-1","thread_name":"Review parser changes","updated_at":"2026-03-12T05:16:00Z"}`,
	)
	writeCodexSession(t, filepath.Join(dir, "sessions", "2026", "03", "12", "rollout-2026-03-12T05-13-27-sess-1.jsonl"),
		`{"timestamp":"2026-03-12T05:13:27Z","type":"session_meta","payload":{"id":"sess-1","timestamp":"2026-03-12T05:13:27Z","cwd":"/tmp/project"}}`,
		`{"timestamp":"2026-03-12T05:13:28Z","type":"event_msg","payload":{"type":"user_message","message":"can you review kro-grs.md\n"}}`,
		`{"timestamp":"2026-03-12T05:13:29Z","type":"event_msg","payload":{"type":"agent_message","message":"I'll review the doc and give you prioritized findings.","phase":"commentary"}}`,
	)

	t.Setenv("MUSE_CODEX_DIR", dir)
	c := &Codex{}
	conversations, err := c.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() error: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(conversations))
	}

	s := conversations[0]
	if s.Source != "codex" {
		t.Errorf("Source = %q, want %q", s.Source, "codex")
	}
	if s.ConversationID != "sess-1" {
		t.Errorf("ConversationID = %q, want %q", s.ConversationID, "sess-1")
	}
	if s.Title != "Review parser changes" {
		t.Errorf("Title = %q, want %q", s.Title, "Review parser changes")
	}
	if s.Project != "/tmp/project" {
		t.Errorf("Project = %q, want %q", s.Project, "/tmp/project")
	}
	if got, want := len(s.Messages), 2; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}
	if s.Messages[0].Role != "user" || s.Messages[0].Content != "can you review kro-grs.md" {
		t.Errorf("Messages[0] = %+v", s.Messages[0])
	}
	if s.Messages[1].Role != "assistant" || s.Messages[1].Content != "I'll review the doc and give you prioritized findings." {
		t.Errorf("Messages[1] = %+v", s.Messages[1])
	}
	wantCreated := time.Date(2026, 3, 12, 5, 13, 27, 0, time.UTC)
	if !s.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt = %v, want %v", s.CreatedAt, wantCreated)
	}
	wantUpdated := time.Date(2026, 3, 12, 5, 16, 0, 0, time.UTC)
	if !s.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("UpdatedAt = %v, want %v", s.UpdatedAt, wantUpdated)
	}
}

func TestCodex_ToolCallsAttachToAssistantTurn(t *testing.T) {
	dir := t.TempDir()
	writeCodexSession(t, filepath.Join(dir, "sessions", "2026", "03", "12", "rollout-sess-2.jsonl"),
		`{"timestamp":"2026-03-12T05:13:27Z","type":"session_meta","payload":{"id":"sess-2","timestamp":"2026-03-12T05:13:27Z","cwd":"/tmp/project"}}`,
		`{"timestamp":"2026-03-12T05:13:28Z","type":"turn_context","payload":{"cwd":"/tmp/project","model":"gpt-5.2-codex"}}`,
		`{"timestamp":"2026-03-12T05:13:29Z","type":"event_msg","payload":{"type":"user_message","message":"check the repo\n"}}`,
		`{"timestamp":"2026-03-12T05:13:30Z","type":"event_msg","payload":{"type":"agent_message","message":"I'm checking the repo structure first.","phase":"commentary"}}`,
		`{"timestamp":"2026-03-12T05:13:31Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls -la\",\"workdir\":\"/tmp/project\"}","call_id":"call-1"}}`,
		`{"timestamp":"2026-03-12T05:13:32Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"Exit code: 0\nOutput:\nREADME.md\n"}}`,
		`{"timestamp":"2026-03-12T05:13:33Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","status":"completed","call_id":"call-2","input":"*** Begin Patch\n*** Update File: /tmp/project/README.md\n@@\n-old\n+new\n*** End Patch\n"}}`,
		`{"timestamp":"2026-03-12T05:13:34Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-2","output":"{\"output\":\"Success\",\"metadata\":{\"exit_code\":0}}"}}`,
		`{"timestamp":"2026-03-12T05:13:35Z","type":"event_msg","payload":{"type":"agent_message","message":"I found a couple of issues.","phase":"final_answer"}}`,
	)

	t.Setenv("MUSE_CODEX_DIR", dir)
	c := &Codex{}
	conversations, err := c.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() error: %v", err)
	}
	s := findConversation(conversations, "sess-2")
	if s == nil {
		t.Fatal("sess-2 not found")
	}
	if got, want := len(s.Messages), 2; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}

	asst := s.Messages[1]
	if asst.Role != "assistant" {
		t.Fatalf("assistant message role = %q, want assistant", asst.Role)
	}
	if !strings.Contains(asst.Content, "I'm checking the repo structure first.") || !strings.Contains(asst.Content, "I found a couple of issues.") {
		t.Errorf("assistant content = %q, want merged commentary/final text", asst.Content)
	}
	if asst.Model != "gpt-5.2-codex" {
		t.Errorf("assistant model = %q, want %q", asst.Model, "gpt-5.2-codex")
	}
	if got, want := len(asst.ToolCalls), 2; got != want {
		t.Fatalf("expected %d tool calls, got %d", want, got)
	}
	if asst.ToolCalls[0].Name != "exec_command" {
		t.Errorf("ToolCalls[0].Name = %q, want %q", asst.ToolCalls[0].Name, "exec_command")
	}
	if string(asst.ToolCalls[0].Input) != `{"cmd":"ls -la","workdir":"/tmp/project"}` {
		t.Errorf("ToolCalls[0].Input = %s", string(asst.ToolCalls[0].Input))
	}
	if string(asst.ToolCalls[0].Output) != `"Exit code: 0\nOutput:\nREADME.md"` {
		t.Errorf("ToolCalls[0].Output = %s", string(asst.ToolCalls[0].Output))
	}
	if asst.ToolCalls[1].Name != "apply_patch" {
		t.Errorf("ToolCalls[1].Name = %q, want %q", asst.ToolCalls[1].Name, "apply_patch")
	}
	if string(asst.ToolCalls[1].Input) != `"*** Begin Patch\n*** Update File: /tmp/project/README.md\n@@\n-old\n+new\n*** End Patch"` {
		t.Errorf("ToolCalls[1].Input = %s", string(asst.ToolCalls[1].Input))
	}
	if string(asst.ToolCalls[1].Output) != `{"output":"Success","metadata":{"exit_code":0}}` {
		t.Errorf("ToolCalls[1].Output = %s", string(asst.ToolCalls[1].Output))
	}
}

func TestCodex_FallbackToResponseItemMessages(t *testing.T) {
	dir := t.TempDir()
	writeCodexSession(t, filepath.Join(dir, "archived_sessions", "rollout-sess-3.jsonl"),
		`{"timestamp":"2026-03-12T05:13:27Z","type":"session_meta","payload":{"id":"sess-3","timestamp":"2026-03-12T05:13:27Z","cwd":"/tmp/project"}}`,
		`{"timestamp":"2026-03-12T05:13:28Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>..."},{"type":"input_text","text":"<app-context>..."},{"type":"input_text","text":"<collaboration_mode>...</collaboration_mode>"}]}}`,
		`{"timestamp":"2026-03-12T05:13:29Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /tmp/project\n\n<INSTRUCTIONS>...</INSTRUCTIONS>"}]}}`,
		`{"timestamp":"2026-03-12T05:13:30Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/tmp/project</cwd>\n</environment_context>"}]}}`,
		`{"timestamp":"2026-03-12T05:13:31Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"implement codex ingestion\n"}]}}`,
		`{"timestamp":"2026-03-12T05:13:32Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'm adding a Codex provider now."}],"phase":"commentary"}}`,
	)

	t.Setenv("MUSE_CODEX_DIR", dir)
	c := &Codex{}
	conversations, err := c.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() error: %v", err)
	}
	s := findConversation(conversations, "sess-3")
	if s == nil {
		t.Fatal("sess-3 not found")
	}
	if got, want := len(s.Messages), 2; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}
	if s.Messages[0].Content != "implement codex ingestion" {
		t.Errorf("Messages[0].Content = %q, want %q", s.Messages[0].Content, "implement codex ingestion")
	}
	if s.Messages[1].Content != "I'm adding a Codex provider now." {
		t.Errorf("Messages[1].Content = %q, want %q", s.Messages[1].Content, "I'm adding a Codex provider now.")
	}
}

func TestCodex_DeduplicatesSessionsByConversationID(t *testing.T) {
	dir := t.TempDir()
	writeCodexSessionIndex(t, dir,
		`{"id":"sess-4","thread_name":"Dedup test","updated_at":"2026-03-12T05:30:00Z"}`,
	)
	writeCodexSession(t, filepath.Join(dir, "sessions", "2026", "03", "12", "rollout-sess-4.jsonl"),
		`{"timestamp":"2026-03-12T05:13:27Z","type":"session_meta","payload":{"id":"sess-4","timestamp":"2026-03-12T05:13:27Z","cwd":"/tmp/project"}}`,
		`{"timestamp":"2026-03-12T05:13:28Z","type":"event_msg","payload":{"type":"user_message","message":"old copy\n"}}`,
		`{"timestamp":"2026-03-12T05:13:29Z","type":"event_msg","payload":{"type":"agent_message","message":"old answer","phase":"final_answer"}}`,
	)
	writeCodexSession(t, filepath.Join(dir, "archived_sessions", "rollout-sess-4.jsonl"),
		`{"timestamp":"2026-03-12T05:20:27Z","type":"session_meta","payload":{"id":"sess-4","timestamp":"2026-03-12T05:20:27Z","cwd":"/tmp/project"}}`,
		`{"timestamp":"2026-03-12T05:20:28Z","type":"event_msg","payload":{"type":"user_message","message":"new copy\n"}}`,
		`{"timestamp":"2026-03-12T05:20:29Z","type":"event_msg","payload":{"type":"agent_message","message":"new answer","phase":"final_answer"}}`,
	)

	t.Setenv("MUSE_CODEX_DIR", dir)
	c := &Codex{}
	conversations, err := c.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("Conversations() error: %v", err)
	}
	if got, want := len(conversations), 1; got != want {
		t.Fatalf("expected %d conversation after dedup, got %d", want, got)
	}
	if conversations[0].Messages[0].Content != "new copy" {
		t.Errorf("dedup kept wrong copy: %#v", conversations[0].Messages)
	}
}

func TestCodex_MissingDirectory(t *testing.T) {
	t.Setenv("MUSE_CODEX_DIR", filepath.Join(t.TempDir(), "missing"))
	c := &Codex{}
	conversations, err := c.Conversations(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if conversations != nil {
		t.Errorf("expected nil conversations, got %d", len(conversations))
	}
}

func writeCodexSessionIndex(t *testing.T, dir string, lines ...string) {
	t.Helper()
	path := filepath.Join(dir, "session_index.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeCodexSession(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCodexToolPayload_JSONAndSizeHandling(t *testing.T) {
	if got := string(codexToolPayload(`{"cmd":"ls"}`)); got != `{"cmd":"ls"}` {
		t.Errorf("codexToolPayload(JSON) = %q", got)
	}
	if got := string(codexToolPayload("plain output")); got != `"plain output"` {
		t.Errorf("codexToolPayload(string) = %q", got)
	}
	huge := fmt.Sprintf("%0*s", maxCodexToolPayloadBytes+1, "")
	if got := codexToolPayload(huge); got != nil {
		t.Errorf("codexToolPayload(huge) = %s, want nil", string(got))
	}
}
