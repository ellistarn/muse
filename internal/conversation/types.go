package conversation

import (
	"encoding/json"
	"fmt"
	"time"
)

// Provider is the interface for conversation sources. Each provider knows how to
// discover and normalize conversations from a specific agent or platform.
type Provider interface {
	// Name returns a human-readable name for this source (e.g. "OpenCode").
	Name() string
	// Conversations returns all conversations available from this source.
	// Returns (nil, nil) if the source data doesn't exist on this machine.
	Conversations() ([]Conversation, error)
}

// Providers returns the default set of conversation providers.
func Providers() []Provider {
	return []Provider{
		&OpenCode{},
		&ClaudeCode{},
		&Codex{},
		&Kiro{},
		&KiroCLI{},
		&GitHub{},
	}
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
