package muse

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// UploadResult summarizes what happened during an upload sync.
type UploadResult struct {
	Sources      int
	SourceCounts map[string]int // per-source upload counts (new conversations)
	SourceTotals map[string]int // per-source total conversation counts (discovered)
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
	New        bool                 // if true, forces a new session (ignores latest)
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

// Option configures a Muse instance.
type Option func(*Muse)

// WithSessionsDir enables session persistence under the given directory.
// Sessions are saved to disk after each turn and the latest session is
// automatically resumed when no SessionID is provided.
func WithSessionsDir(dir string) Option {
	return func(m *Muse) {
		m.sessions = newSessionStore(dir)
	}
}

// New creates a Muse with the given LLM client and document (muse.md content).
// Pass an empty document for first-run before any composes.
func New(llm inference.Client, document string, opts ...Option) *Muse {
	m := &Muse{
		llm:      llm,
		document: document,
		sessions: newSessionStore(""),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

var systemPrompt = prompts.System

// Ask handles a conversation turn. If SessionID is set, continues that session.
// If SessionID is empty and session persistence is configured, resumes the
// latest session from disk. Otherwise starts a new conversation.
// The system prompt is always refreshed from the current muse.md on resume.
func (m *Muse) Ask(ctx context.Context, input AskInput) (*AskResult, error) {
	var session *Session

	// Resolve which session to use: explicit ID > latest from disk > new.
	sessionID := input.SessionID
	if sessionID == "" && !input.New {
		sessionID = m.sessions.latestID()
	}

	if sessionID != "" {
		s, err := m.sessions.get(sessionID)
		if err != nil {
			return nil, err
		}
		session = s
	} else {
		session = &Session{}
	}

	// Always use a fresh system prompt so the current muse.md governs,
	// even when resuming a session created under an older version.
	doc := m.document
	if doc == "" {
		doc = "No muse available yet. Run 'muse compose' to generate one from conversations."
	}
	session.System = fmt.Sprintf(systemPrompt, doc)

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

	sessionID = m.sessions.save(session)
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

// SetLatest updates the "latest" session pointer on disk so a subsequent
// `muse ask` (without --new) resumes this session. Only the CLI path should
// call this — MCP sessions persist but don't compete for the latest pointer.
func (m *Muse) SetLatest(sessionID string) {
	m.sessions.setLatest(sessionID)
}

// SyncProgressFunc receives sync progress from source providers during Upload.
// The source parameter identifies which provider sent the update.
type SyncProgressFunc func(source string, p conversation.SyncProgress)

// Upload scans local sources, diffs against storage, and uploads changed conversations.
// If sources are specified, only those providers are scanned. The optional progress
// callback receives structured sync updates from network sources.
func Upload(ctx context.Context, store storage.Store, progress SyncProgressFunc, sources ...string) (*UploadResult, error) {
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
	providers := conversation.ProvidersFor(sources)
	// Sort providers so API sources (slower, network-bound) start first.
	apiSources := map[string]bool{"GitHub Issues": true, "GitHub PRs": true, "Slack": true}
	sort.SliceStable(providers, func(i, j int) bool {
		iAPI := apiSources[providers[i].Name()]
		jAPI := apiSources[providers[j].Name()]
		if iAPI != jAPI {
			return iAPI
		}
		return false
	})
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, provider := range providers {
		wg.Add(1)
		go func(i int, p conversation.Provider) {
			defer wg.Done()
			progressFn := func(conversation.SyncProgress) {} // no-op default
			if progress != nil {
				name := p.Name()
				progressFn = func(sp conversation.SyncProgress) {
					progress(name, sp)
				}
			}
			convs, err := p.Conversations(ctx, progressFn)
			results[i] = result{name: p.Name(), conversations: convs, err: err}
			// Signal completion so the renderer can print the summary line.
			if progress != nil {
				progress(p.Name(), conversation.SyncProgress{
					Phase:  "done",
					Detail: fmt.Sprintf("%d conversations", len(convs)),
				})
			}
		}(i, provider)
	}
	wg.Wait()

	var local []conversation.Conversation
	var warnings []string
	for _, r := range results {
		if r.err != nil {
			// If the user explicitly requested this source, fail hard.
			if len(sources) > 0 {
				return nil, fmt.Errorf("%s: %w", r.name, r.err)
			}
			warnings = append(warnings, fmt.Sprintf("failed to read %s conversations: %v", r.name, r.err))
			continue
		}
		local = append(local, r.conversations...)
	}

	// Filter by source if specified.
	if len(sources) > 0 {
		local = filterConversationsBySource(local, sources)
	}

	// Count per-source totals
	sourceTotals := map[string]int{}
	for _, sess := range local {
		sourceTotals[sess.Source]++
	}

	var uploaded, skipped int
	var totalBytes int
	var mu sync.Mutex
	uploadCounts := map[string]int{}

	// Separate conversations that need uploading from those that can be skipped.
	var toUpload []*conversation.Conversation
	for i := range local {
		sess := &local[i]
		key := fmt.Sprintf("conversations/%s/%s.json", sess.Source, sess.ConversationID)
		if entry, exists := remote[key]; exists {
			if !sess.UpdatedAt.After(entry.LastModified) {
				skipped++
				continue
			}
		}
		toUpload = append(toUpload, sess)
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(20)
	for _, sess := range toUpload {
		g.Go(func() error {
			n, err := store.PutConversation(ctx, sess)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to upload %s: %v", sess.ConversationID, err))
				return nil
			}
			uploaded++
			uploadCounts[sess.Source]++
			totalBytes += n
			return nil
		})
	}
	g.Wait()
	return &UploadResult{
		Sources:      len(sourceTotals),
		SourceCounts: uploadCounts,
		SourceTotals: sourceTotals,
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
