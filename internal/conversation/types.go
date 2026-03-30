package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SyncProgress reports progress from a source sync operation. Sources call
// the progress callback to report what's happening; the caller renders it.
//
// Phases:
//   - "discovering" — transient status, overwritten in place on TTY
//   - "fetching"    — transient progress bar, overwritten in place on TTY
//   - "log"         — persistent one-line event (e.g. auth success), printed with newline
//   - "done"        — final summary, printed with newline
type SyncProgress struct {
	Phase   string // "discovering", "fetching", "log", "done"
	Total   int    // total items to process (0 if unknown yet)
	Current int    // items processed so far
	Detail  string // human-readable detail, e.g. "533 PRs"
}

// Provider is the interface for conversation sources. Each provider knows how to
// discover and normalize conversations from a specific agent or platform.
type Provider interface {
	// Name returns a human-readable name for this source (e.g. "OpenCode").
	Name() string
	// Conversations returns all conversations available from this source.
	// The progress callback reports sync progress for slow (network) sources.
	// Local sources may ignore it. Returns (nil, nil) if the source data
	// doesn't exist on this machine.
	Conversations(ctx context.Context, progress func(SyncProgress)) ([]Conversation, error)
}

// SourceInfo describes a registered conversation source.
type SourceInfo struct {
	Name     string   // source name used in CLI args and conversation.Source
	Provider Provider // the provider implementation
	OptIn    bool     // true if the source requires explicit selection (e.g. network calls)
}

// Sources returns all registered conversation sources.
func Sources() []SourceInfo {
	return []SourceInfo{
		{Name: "opencode", Provider: &OpenCode{}},
		{Name: "claude-code", Provider: &ClaudeCode{}},
		{Name: "codex", Provider: &Codex{}},
		{Name: "kiro", Provider: &Kiro{}},
		{Name: "kiro-cli", Provider: &KiroCLI{}},
		{Name: "github", Provider: &GitHub{}, OptIn: true},
		{Name: "slack", Provider: &Slack{}, OptIn: true},
	}
}

// Providers returns the default set of conversation providers (local sources).
// These are safe to invoke unconditionally — they read local files and databases.
func Providers() []Provider {
	var providers []Provider
	for _, s := range Sources() {
		if !s.OptIn {
			providers = append(providers, s.Provider)
		}
	}
	return providers
}

// DefaultSourceNames returns the names of all non-opt-in sources.
func DefaultSourceNames() []string {
	var names []string
	for _, s := range Sources() {
		if !s.OptIn {
			names = append(names, s.Name)
		}
	}
	return names
}

// ProvidersFor returns providers matching the given source names. Includes
// opt-in providers when explicitly named. Returns all default providers
// when sources is empty.
func ProvidersFor(sources []string) []Provider {
	if len(sources) == 0 {
		return Providers()
	}
	wanted := make(map[string]bool, len(sources))
	for _, s := range sources {
		wanted[s] = true
	}
	var providers []Provider
	for _, s := range Sources() {
		if wanted[s.Name] {
			providers = append(providers, s.Provider)
		}
	}
	return providers
}

// Conversation is the normalized representation of a conversation from any agent.
type Conversation struct {
	SchemaVersion  int       `json:"schema_version"`
	Source         string    `json:"source"`
	ConversationID string    `json:"conversation_id"`
	Project        string    `json:"project"`
	Title          string    `json:"title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ParentID       string    `json:"parent_id,omitempty"`
	SubagentIDs    []string  `json:"subagent_ids,omitempty"`
	Messages       []Message `json:"messages"`
}

// UnmarshalJSON implements backward-compatible deserialization for Conversation.
// Upstream tools rename fields without notice (e.g. session_id → conversation_id).
// Per designs/sources.md "Format compatibility", the parser accepts both names.
func (c *Conversation) UnmarshalJSON(data []byte) error {
	// Alias avoids infinite recursion on UnmarshalJSON.
	type Alias Conversation
	aux := &struct {
		*Alias
		SessionID string `json:"session_id"`
	}{Alias: (*Alias)(c)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if c.ConversationID == "" && aux.SessionID != "" {
		c.ConversationID = aux.SessionID
	}
	return nil
}

// Validate checks that required fields are present. This catches silent
// deserialization failures (e.g. renamed JSON tags, corrupt data) that would
// otherwise propagate empty strings through the system.
func (c *Conversation) Validate() error {
	if c.ConversationID == "" {
		return fmt.Errorf("conversation missing required field: conversation_id")
	}
	if c.Source == "" {
		return fmt.Errorf("conversation %s missing required field: source", c.ConversationID)
	}
	return nil
}

type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Timestamp time.Time  `json:"timestamp"`
	Model     string     `json:"model,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}
