package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Kiro reads conversations from Kiro's workspace session files, enriched with
// assistant completions from .chat files. Kiro's session JSON only stores
// placeholder assistant messages ("On it."); the actual completions live in
// separate .chat files linked by executionId.
type Kiro struct{}

func (k *Kiro) Name() string { return "Kiro" }

func (k *Kiro) Conversations(_ context.Context, _ func(SyncProgress)) ([]Conversation, error) {
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

	// The kiro agent dir is the parent of workspace-sessions. It contains
	// hash-named directories with .chat files that hold the actual completions.
	agentDir := kiroDir
	sessionsDir := filepath.Join(agentDir, "workspace-sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return nil, nil
	}

	// Build an executionId -> .chat file path index from all .chat files
	// in the agent dir (excluding workspace-sessions).
	chatIndex := buildChatIndex(agentDir)

	workspaceDirs, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, nil
	}
	var conversations []Conversation
	for _, wd := range workspaceDirs {
		if !wd.IsDir() {
			continue
		}
		wsPath := filepath.Join(sessionsDir, wd.Name())
		index, err := loadKiroIndex(filepath.Join(wsPath, "sessions.json"))
		if err != nil {
			continue
		}
		for _, entry := range index {
			conversationPath := filepath.Join(wsPath, entry.SessionID+".json")
			conv, err := parseKiroConversation(conversationPath, entry, chatIndex)
			if err != nil || conv == nil {
				continue
			}
			conversations = append(conversations, *conv)
		}
	}
	return conversations, nil
}

func defaultKiroDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	// Return the agent dir (parent of workspace-sessions), since we need
	// access to both workspace-sessions/ and the hash dirs with .chat files.
	return filepath.Join(configDir, "Kiro", "User",
		"globalStorage", "kiro.kiroagent"), nil
}

// buildChatIndex scans agentDir for .chat files in hash-named subdirectories
// and builds an executionId -> file path index.
//
// NOTE: There are typically orphan .chat files (~hundreds) that aren't linked
// to any conversation. These represent older conversations whose session JSON was cleaned
// up. We skip them for now but they could be recovered in the future.
func buildChatIndex(agentDir string) map[string]string {
	index := make(map[string]string)
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		return index
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "workspace-sessions" {
			continue
		}
		dirPath := filepath.Join(agentDir, entry.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".chat") {
				continue
			}
			chatPath := filepath.Join(dirPath, f.Name())
			data, err := os.ReadFile(chatPath)
			if err != nil {
				continue
			}
			// Only unmarshal the executionId field for efficiency.
			var stub struct {
				ExecutionID string `json:"executionId"`
			}
			if err := json.Unmarshal(data, &stub); err != nil || stub.ExecutionID == "" {
				continue
			}
			index[stub.ExecutionID] = chatPath
		}
	}
	return index
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
	History []kiroHistoryEntry `json:"history"`
}

type kiroHistoryEntry struct {
	Message     kiroMessage `json:"message"`
	ExecutionID string      `json:"executionId"`
}

type kiroMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func parseKiroConversation(path string, indexEntry kiroIndexEntry, chatIndex map[string]string) (*Conversation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file kiroSessionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}

	conv := &Conversation{
		SchemaVersion:  1,
		Source:         "kiro",
		ConversationID: indexEntry.SessionID,
		Project:        indexEntry.WorkspaceDirectory,
		Title:          indexEntry.Title,
	}

	if ms, err := strconv.ParseInt(indexEntry.DateCreated, 10, 64); err == nil {
		conv.CreatedAt = time.UnixMilli(ms)
	}
	if info, err := os.Stat(path); err == nil {
		conv.UpdatedAt = info.ModTime()
	}

	// Find the last execution's .chat file. Each .chat file contains the
	// cumulative conversation, so the last one has the full history.
	if chatFile, chatPath := findLastChatFile(file.History, chatIndex); chatFile != nil {
		conv.Messages = parseChatFileMessages(chatFile)
		if chatFile.Metadata.ModelID != "" {
			for i := range conv.Messages {
				if conv.Messages[i].Role == "assistant" {
					conv.Messages[i].Model = chatFile.Metadata.ModelID
				}
			}
		}
		// Use .chat file mod time as UpdatedAt since it better reflects
		// when the conversation content last changed.
		if info, err := os.Stat(chatPath); err == nil {
			conv.UpdatedAt = info.ModTime()
		}
	}

	// Fall back to session JSON if no .chat file found or it yielded no messages.
	if len(conv.Messages) == 0 {
		conv.Messages = parseSessionMessages(file.History)
	}

	if len(conv.Messages) == 0 {
		return nil, nil
	}
	return conv, nil
}

// findLastChatFile walks conversation history in order and returns the parsed .chat
// file and its path for the last executionId that has a matching file.
func findLastChatFile(history []kiroHistoryEntry, chatIndex map[string]string) (*kiroChatFile, string) {
	var lastPath string
	for _, entry := range history {
		if entry.ExecutionID == "" {
			continue
		}
		if p, ok := chatIndex[entry.ExecutionID]; ok {
			lastPath = p
		}
	}
	if lastPath == "" {
		return nil, ""
	}
	data, err := os.ReadFile(lastPath)
	if err != nil {
		return nil, ""
	}
	var chat kiroChatFile
	if err := json.Unmarshal(data, &chat); err != nil {
		return nil, ""
	}
	return &chat, lastPath
}

// kiroChatFile represents a .chat file containing the actual conversation.
type kiroChatFile struct {
	ExecutionID string           `json:"executionId"`
	Chat        []kiroChatMsg    `json:"chat"`
	Metadata    kiroChatMetadata `json:"metadata"`
}

type kiroChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type kiroChatMetadata struct {
	ModelID   string `json:"modelId"`
	StartTime int64  `json:"startTime"`
	EndTime   int64  `json:"endTime"`
}

// parseChatFileMessages extracts normalized messages from a .chat file.
// It filters out the system prompt, "I will follow" acknowledgment, IDE
// notifications, and empty messages. Consecutive bot messages between user
// turns are joined into a single assistant message.
func parseChatFileMessages(chat *kiroChatFile) []Message {
	var messages []Message
	var botParts []string

	flushBot := func() {
		if len(botParts) > 0 {
			messages = append(messages, Message{
				Role:    "assistant",
				Content: strings.Join(botParts, "\n"),
			})
			botParts = nil
		}
	}

	for i, msg := range chat.Chat {
		content := strings.TrimSpace(msg.Content)

		switch msg.Role {
		case "human":
			if isKiroSystemPrompt(i, content) {
				continue
			}
			if isKiroIDEMessage(content) {
				continue
			}
			// Strip <EnvironmentContext> blocks from user messages.
			content = stripEnvironmentContext(content)
			content = strings.TrimSpace(content)
			if content == "" {
				continue
			}
			flushBot()
			messages = append(messages, Message{
				Role:    "user",
				Content: content,
			})

		case "bot":
			if isKiroBotPreamble(i, content) {
				continue
			}
			if content == "" {
				continue
			}
			botParts = append(botParts, content)

		case "tool":
			// Tool result messages have empty content in Kiro's format.
			// Skip them — the bot's follow-up message summarizes the result.
			continue
		}
	}
	flushBot()
	return messages
}

// isKiroSystemPrompt returns true for the system prompt at the start of
// the conversation (human message at index 0 starting with identity markers).
func isKiroSystemPrompt(index int, content string) bool {
	if index != 0 {
		return false
	}
	return strings.HasPrefix(content, "# Identity") ||
		strings.HasPrefix(content, "<identity>")
}

// isKiroBotPreamble returns true for the bot's acknowledgment of the system
// prompt (always "I will follow these instructions." at index 1).
func isKiroBotPreamble(index int, content string) bool {
	return index == 1 && content == "I will follow these instructions."
}

// isKiroIDEMessage returns true for automated IDE notifications that are
// injected as human messages (e.g., autofix notifications).
func isKiroIDEMessage(content string) bool {
	return strings.Contains(content, "<kiro-ide-message>")
}

// stripEnvironmentContext removes <EnvironmentContext>...</EnvironmentContext>
// blocks that Kiro appends to user messages. These contain IDE state that
// isn't part of the user's actual input.
func stripEnvironmentContext(content string) string {
	for {
		start := strings.Index(content, "<EnvironmentContext>")
		if start == -1 {
			return content
		}
		end := strings.Index(content, "</EnvironmentContext>")
		if end == -1 {
			// Malformed — strip from start tag to end of string.
			return strings.TrimSpace(content[:start])
		}
		content = content[:start] + content[end+len("</EnvironmentContext>"):]
	}
}

// parseSessionMessages is the fallback parser that extracts messages from the
// session JSON when no .chat file is available. This produces degraded output
// since assistant messages are typically just "On it."
func parseSessionMessages(history []kiroHistoryEntry) []Message {
	var messages []Message
	for _, entry := range history {
		if entry.Message.Role != "user" && entry.Message.Role != "assistant" {
			continue
		}
		msg := Message{Role: entry.Message.Role}
		msg.Content = parseKiroContent(entry.Message.Content)
		if msg.Content == "" {
			continue
		}
		messages = append(messages, msg)
	}
	return messages
}

// parseKiroContent handles Kiro's two session JSON content formats:
// - User messages: array of {type: "text", text: "..."} blocks
// - Assistant messages: plain string
func parseKiroContent(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
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
