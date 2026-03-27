package muse

import (
	"context"
	"fmt"
	"sync"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// UploadResult summarizes what happened during an upload sync.
type UploadResult struct {
	Sources      int
	SourceCounts map[string]int // per-source upload counts
	Total        int
	Uploaded     int
	Skipped      int
	Bytes        int
	Warnings     []string
}

// AskInput contains the parameters for an Ask call.
type AskInput struct {
	Question   string               // the user's message
	SessionID  string               // if set, continues an existing conversation
	StreamFunc inference.StreamFunc // if set, text deltas are streamed through this callback
}

// AskResult contains the output from an Ask call.
type AskResult struct {
	Response  string          // the muse's response text
	SessionID string          // session ID for continuing the conversation
	Usage     inference.Usage // token usage and cost for this call
}

// Muse holds the state needed for conversational Ask operations.
type Muse struct {
	llm      inference.Client
	document string // the full muse.md
	sessions *sessionStore
}

// New creates a Muse with the given LLM client and document (muse.md content).
// Pass an empty document for first-run before any composes.
func New(llm inference.Client, document string) *Muse {
	return &Muse{
		llm:      llm,
		document: document,
		sessions: newSessionStore(),
	}
}

var systemPrompt = prompts.System

// Ask handles a conversation turn. If SessionID is set, continues an existing
// conversation. Otherwise starts a new one.
func (m *Muse) Ask(ctx context.Context, input AskInput) (*AskResult, error) {
	var session *Session

	if input.SessionID != "" {
		// Resume existing conversation
		s, err := m.sessions.get(input.SessionID)
		if err != nil {
			return nil, err
		}
		session = s
	} else {
		// New conversation
		doc := m.document
		if doc == "" {
			doc = "No muse available yet. Run 'muse compose' to generate one from conversations."
		}
		session = &Session{
			System: fmt.Sprintf(systemPrompt, doc),
		}
	}

	// Hold the session lock for the entire turn so concurrent calls on the
	// same session are serialized and Messages is never mutated concurrently.
	session.mu.Lock()
	defer session.mu.Unlock()

	session.Messages = append(session.Messages, inference.Message{
		Role:    "user",
		Content: input.Question,
	})

	var result *inference.Response
	var err error
	if input.StreamFunc != nil {
		result, err = m.llm.ConverseMessagesStream(ctx, session.System, session.Messages, input.StreamFunc)
	} else {
		result, err = m.llm.ConverseMessages(ctx, session.System, session.Messages)
	}
	if err != nil {
		return nil, fmt.Errorf("converse failed: %w", err)
	}

	// Append the assistant's response to the session history
	session.Messages = append(session.Messages, inference.Message{
		Role:    "assistant",
		Content: result.Text,
	})

	sessionID := m.sessions.save(session)
	return &AskResult{
		Response:  result.Text,
		SessionID: sessionID,
		Usage:     result.Usage,
	}, nil
}

// Document returns the current muse.md for use by the MCP handler.
func (m *Muse) Document() string {
	return m.document
}

// Upload scans local sources, diffs against storage, and uploads changed conversations.
// If sources are specified, only those providers are scanned.
func Upload(ctx context.Context, store storage.Store, sources ...string) (*UploadResult, error) {
	existing, err := store.ListConversations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list remote conversations: %w", err)
	}
	remote := map[string]storage.ConversationEntry{}
	for _, e := range existing {
		remote[e.Key] = e
	}

	type result struct {
		name          string
		conversations []conversation.Conversation
		err           error
	}
	providers := conversation.Providers()
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, provider := range providers {
		wg.Add(1)
		go func(i int, p conversation.Provider) {
			defer wg.Done()
			convs, err := p.Conversations()
			results[i] = result{name: p.Name(), conversations: convs, err: err}
		}(i, provider)
	}
	wg.Wait()

	var local []conversation.Conversation
	var warnings []string
	for _, r := range results {
		if r.err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to read %s conversations: %v", r.name, r.err))
			continue
		}
		local = append(local, r.conversations...)
	}

	// Filter by source if specified.
	if len(sources) > 0 {
		local = filterConversationsBySource(local, sources)
	}

	// Count unique sources
	sourceSet := map[string]bool{}
	for _, sess := range local {
		sourceSet[sess.Source] = true
	}

	var uploaded, skipped int
	var totalBytes int
	uploadCounts := map[string]int{}
	for i := range local {
		sess := &local[i]
		key := fmt.Sprintf("conversations/%s/%s.json", sess.Source, sess.ConversationID)
		if entry, exists := remote[key]; exists {
			if !sess.UpdatedAt.After(entry.LastModified) {
				skipped++
				continue
			}
		}
		n, err := store.PutConversation(ctx, sess)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to upload %s: %v", sess.ConversationID, err))
			continue
		}
		uploaded++
		uploadCounts[sess.Source]++
		totalBytes += n
	}
	return &UploadResult{
		Sources:      len(sourceSet),
		SourceCounts: uploadCounts,
		Total:        len(local),
		Uploaded:     uploaded,
		Skipped:      skipped,
		Bytes:        totalBytes,
		Warnings:     warnings,
	}, nil
}

func filterConversationsBySource(convs []conversation.Conversation, sources []string) []conversation.Conversation {
	allowed := make(map[string]bool, len(sources))
	for _, s := range sources {
		allowed[s] = true
	}
	var filtered []conversation.Conversation
	for _, c := range convs {
		if allowed[c.Source] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
