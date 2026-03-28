package conversation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultCodexDir          = ".codex"
	maxCodexScanTokenBytes   = 64 * 1024 * 1024
	maxCodexToolPayloadBytes = 16 * 1024
)

// Codex reads conversations from Codex session JSONL files.
type Codex struct{}

func (c *Codex) Name() string { return "Codex" }

func (c *Codex) Conversations(_ context.Context, _ func(SyncProgress)) ([]Conversation, error) {
	codexDir := os.Getenv("MUSE_CODEX_DIR")
	if codexDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		codexDir = filepath.Join(home, defaultCodexDir)
	}
	if _, err := os.Stat(codexDir); os.IsNotExist(err) {
		return nil, nil
	}

	index := loadCodexSessionIndex(filepath.Join(codexDir, "session_index.jsonl"))
	paths, err := listCodexSessionFiles(codexDir)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]Conversation)
	for _, path := range paths {
		conv, err := parseCodexConversation(path, index)
		if err != nil || conv == nil {
			continue
		}
		existing, exists := byID[conv.ConversationID]
		if !exists || codexConversationNewer(*conv, existing) {
			byID[conv.ConversationID] = *conv
		}
	}

	conversations := make([]Conversation, 0, len(byID))
	for _, conv := range byID {
		conversations = append(conversations, conv)
	}
	sort.Slice(conversations, func(i, j int) bool {
		return conversations[i].UpdatedAt.After(conversations[j].UpdatedAt)
	})
	return conversations, nil
}

type codexIndexEntry struct {
	Title     string
	UpdatedAt time.Time
}

type codexIndexLine struct {
	ID        string `json:"id"`
	Thread    string `json:"thread_name"`
	UpdatedAt string `json:"updated_at"`
}

func loadCodexSessionIndex(path string) map[string]codexIndexEntry {
	index := map[string]codexIndexEntry{}
	f, err := os.Open(path)
	if err != nil {
		return index
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), maxCodexScanTokenBytes)
	for scanner.Scan() {
		var line codexIndexLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil || line.ID == "" {
			continue
		}
		entry := codexIndexEntry{Title: line.Thread}
		if line.UpdatedAt != "" {
			if ts, err := time.Parse(time.RFC3339Nano, line.UpdatedAt); err == nil {
				entry.UpdatedAt = ts
			}
		}
		index[line.ID] = entry
	}
	return index
}

func listCodexSessionFiles(codexDir string) ([]string, error) {
	var paths []string
	for _, root := range []string{"sessions", "archived_sessions"} {
		base := filepath.Join(codexDir, root)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return fs.SkipAll
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan %s: %w", base, err)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func codexConversationNewer(a, b Conversation) bool {
	switch {
	case a.UpdatedAt.After(b.UpdatedAt):
		return true
	case b.UpdatedAt.After(a.UpdatedAt):
		return false
	case len(a.Messages) != len(b.Messages):
		return len(a.Messages) > len(b.Messages)
	default:
		return len(a.Title) > len(b.Title)
	}
}

type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
}

type codexTurnContext struct {
	Model string `json:"model"`
}

type codexEventMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type codexResponseMessage struct {
	Role    string              `json:"role"`
	Content []codexContentBlock `json:"content"`
}

type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	CallID    string `json:"call_id"`
}

type codexFunctionCallOutput struct {
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type codexCustomToolCall struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	CallID string `json:"call_id"`
}

type codexCustomToolCallOutput struct {
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type codexBuilder struct {
	messages  []Message
	callIndex map[string]codexToolRef
}

type codexToolRef struct {
	Message int
	Tool    int
}

func newCodexBuilder() *codexBuilder {
	return &codexBuilder{
		callIndex: make(map[string]codexToolRef),
	}
}

func (b *codexBuilder) addText(role, text string, ts time.Time, model string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if n := len(b.messages); n > 0 && b.messages[n-1].Role == role {
		if b.messages[n-1].Content != "" {
			b.messages[n-1].Content += "\n" + text
		} else {
			b.messages[n-1].Content = text
		}
		if role == "assistant" && b.messages[n-1].Model == "" {
			b.messages[n-1].Model = model
		}
		return
	}
	msg := Message{
		Role:      role,
		Content:   text,
		Timestamp: ts,
	}
	if role == "assistant" {
		msg.Model = model
	}
	b.messages = append(b.messages, msg)
}

func (b *codexBuilder) ensureAssistant(ts time.Time, model string) int {
	if n := len(b.messages); n > 0 && b.messages[n-1].Role == "assistant" {
		if b.messages[n-1].Model == "" {
			b.messages[n-1].Model = model
		}
		if b.messages[n-1].Timestamp.IsZero() {
			b.messages[n-1].Timestamp = ts
		}
		return n - 1
	}
	b.messages = append(b.messages, Message{
		Role:      "assistant",
		Timestamp: ts,
		Model:     model,
	})
	return len(b.messages) - 1
}

func (b *codexBuilder) addToolCall(name string, input json.RawMessage, ts time.Time, model, callID string) {
	idx := b.ensureAssistant(ts, model)
	msg := &b.messages[idx]
	msg.ToolCalls = append(msg.ToolCalls, ToolCall{
		Name:  name,
		Input: input,
	})
	if callID != "" {
		b.callIndex[callID] = codexToolRef{Message: idx, Tool: len(msg.ToolCalls) - 1}
	}
}

func (b *codexBuilder) addToolOutput(callID string, output json.RawMessage) {
	ref, ok := b.callIndex[callID]
	if !ok || ref.Message >= len(b.messages) || ref.Tool >= len(b.messages[ref.Message].ToolCalls) {
		return
	}
	b.messages[ref.Message].ToolCalls[ref.Tool].Output = output
}

func parseCodexConversation(path string, titles map[string]codexIndexEntry) (*Conversation, error) {
	conv, err := parseCodexConversationPass(path, titles, true)
	if err != nil {
		return conv, err
	}
	if conv != nil && len(conv.Messages) > 0 {
		return conv, nil
	}
	return parseCodexConversationPass(path, titles, false)
}

func parseCodexConversationPass(path string, titles map[string]codexIndexEntry, useEventMessages bool) (*Conversation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	conv := &Conversation{
		SchemaVersion: 1,
		Source:        "codex",
	}
	builder := newCodexBuilder()

	var firstTime time.Time
	var lastTime time.Time
	var currentModel string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), maxCodexScanTokenBytes)
	for scanner.Scan() {
		var env codexEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, env.Timestamp)
		if !ts.IsZero() {
			if firstTime.IsZero() || ts.Before(firstTime) {
				firstTime = ts
			}
			if ts.After(lastTime) {
				lastTime = ts
			}
		}

		switch env.Type {
		case "session_meta":
			var meta codexSessionMeta
			if err := json.Unmarshal(env.Payload, &meta); err != nil {
				continue
			}
			if conv.ConversationID == "" {
				conv.ConversationID = meta.ID
			}
			if conv.Project == "" {
				conv.Project = meta.CWD
			}
			if meta.Timestamp != "" {
				if sessionTS, err := time.Parse(time.RFC3339Nano, meta.Timestamp); err == nil {
					if firstTime.IsZero() || sessionTS.Before(firstTime) {
						firstTime = sessionTS
					}
					if sessionTS.After(lastTime) {
						lastTime = sessionTS
					}
				}
			}
		case "turn_context":
			var ctx codexTurnContext
			if err := json.Unmarshal(env.Payload, &ctx); err == nil && ctx.Model != "" {
				currentModel = ctx.Model
			}
		case "event_msg":
			if !useEventMessages {
				continue
			}
			var msg codexEventMsg
			if err := json.Unmarshal(env.Payload, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "user_message":
				builder.addText("user", msg.Message, ts, "")
			case "agent_message":
				builder.addText("assistant", msg.Message, ts, currentModel)
			}
		case "response_item":
			var kind struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(env.Payload, &kind); err != nil {
				continue
			}
			switch kind.Type {
			case "message":
				if useEventMessages {
					continue
				}
				var msg codexResponseMessage
				if err := json.Unmarshal(env.Payload, &msg); err != nil {
					continue
				}
				text := extractCodexResponseText(msg.Content)
				switch msg.Role {
				case "user":
					if isCodexSyntheticUserMessage(text) {
						continue
					}
					builder.addText("user", text, ts, "")
				case "assistant":
					builder.addText("assistant", text, ts, currentModel)
				}
			case "function_call":
				var call codexFunctionCall
				if err := json.Unmarshal(env.Payload, &call); err != nil {
					continue
				}
				builder.addToolCall(call.Name, codexToolPayload(call.Arguments), ts, currentModel, call.CallID)
			case "function_call_output":
				var out codexFunctionCallOutput
				if err := json.Unmarshal(env.Payload, &out); err != nil {
					continue
				}
				builder.addToolOutput(out.CallID, codexToolPayload(out.Output))
			case "custom_tool_call":
				var call codexCustomToolCall
				if err := json.Unmarshal(env.Payload, &call); err != nil {
					continue
				}
				builder.addToolCall(call.Name, codexToolPayload(call.Input), ts, currentModel, call.CallID)
			case "custom_tool_call_output":
				var out codexCustomToolCallOutput
				if err := json.Unmarshal(env.Payload, &out); err != nil {
					continue
				}
				builder.addToolOutput(out.CallID, codexToolPayload(out.Output))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if conv.ConversationID == "" {
		return nil, fmt.Errorf("missing session id in %s", path)
	}

	conv.Messages = builder.messages
	if len(conv.Messages) == 0 {
		return nil, nil
	}

	conv.CreatedAt = firstTime
	conv.UpdatedAt = lastTime

	if entry, ok := titles[conv.ConversationID]; ok {
		if entry.Title != "" {
			conv.Title = entry.Title
		}
		if entry.UpdatedAt.After(conv.UpdatedAt) {
			conv.UpdatedAt = entry.UpdatedAt
		}
	}
	if conv.Title == "" {
		conv.Title = codexFallbackTitle(conv.Messages)
	}
	if conv.CreatedAt.IsZero() {
		conv.CreatedAt = conv.UpdatedAt
	}
	if conv.UpdatedAt.IsZero() {
		if info, err := os.Stat(path); err == nil {
			conv.UpdatedAt = info.ModTime()
		}
		if conv.CreatedAt.IsZero() {
			conv.CreatedAt = conv.UpdatedAt
		}
	}

	return conv, nil
}

func extractCodexResponseText(blocks []codexContentBlock) string {
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case "input_text", "output_text":
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func isCodexSyntheticUserMessage(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	return strings.HasPrefix(text, "# AGENTS.md instructions") ||
		strings.HasPrefix(text, "<environment_context>") ||
		strings.HasPrefix(text, "<INSTRUCTIONS>")
}

func codexFallbackTitle(messages []Message) string {
	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) != "" {
			return truncate(msg.Content, 100)
		}
	}
	return ""
}

func codexToolPayload(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxCodexToolPayloadBytes {
		return nil
	}
	raw := []byte(s)
	if json.Valid(raw) {
		return raw
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return data
}
