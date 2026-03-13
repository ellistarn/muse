package muse

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/source"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// UploadResult summarizes what happened during an upload sync.
type UploadResult struct {
	Total    int
	Uploaded int
	Skipped  int
	Bytes    int
	Warnings []string
}

// AskInput contains the parameters for an Ask call.
type AskInput struct {
	Question   string             // the user's message
	SessionID  string             // if set, continues an existing conversation
	StreamFunc bedrock.StreamFunc // if set, text deltas are streamed through this callback
}

// AskResult contains the output from an Ask call.
type AskResult struct {
	Response  string // the muse's response text
	SessionID string // session ID for continuing the conversation
}

// Muse holds the state needed for all operations.
type Muse struct {
	storage  storage.Store
	bedrock  *bedrock.Client
	soul     string // the full soul document, loaded at init
	sessions *sessionStore
}

func New(ctx context.Context, store storage.Store) (*Muse, error) {
	bedrockClient, err := bedrock.NewClient(ctx, bedrock.ModelOpus)
	if err != nil {
		return nil, fmt.Errorf("failed to create bedrock client: %w", err)
	}
	soul, err := store.GetSoul(ctx)
	if err != nil {
		if !storage.IsNotFound(err) {
			return nil, fmt.Errorf("failed to load soul: %w", err)
		}
		soul = "" // no soul yet — first run before any dreams
	}
	if soul != "" {
		log.Printf("Loaded soul (%d bytes)\n", len(soul))
	} else {
		log.Println("No soul found (run 'muse dream' to generate one)")
	}
	return &Muse{
		storage:  store,
		bedrock:  bedrockClient,
		soul:     soul,
		sessions: newSessionStore(),
	}, nil
}

// NewForTest creates a Muse with caller-provided dependencies.
func NewForTest(bedrockClient *bedrock.Client, soul string) *Muse {
	return &Muse{
		bedrock:  bedrockClient,
		soul:     soul,
		sessions: newSessionStore(),
	}
}

var systemPrompt = prompts.Muse

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
		session.Messages = append(session.Messages, types.Message{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: input.Question}},
		})
	} else {
		// New conversation
		soul := m.soul
		if soul == "" {
			soul = "No soul document available yet. Run 'muse dream' to generate one from memories."
		}
		session = &Session{
			System: fmt.Sprintf(systemPrompt, soul),
			Messages: []types.Message{
				{
					Role:    types.ConversationRoleUser,
					Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: input.Question}},
				},
			},
		}
	}

	var result *bedrock.ConverseResult
	var err error
	if input.StreamFunc != nil {
		result, err = m.bedrock.ConverseMessagesStream(ctx, session.System, session.Messages, input.StreamFunc)
	} else {
		result, err = m.bedrock.ConverseMessages(ctx, session.System, session.Messages, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("converse failed: %w", err)
	}

	// Append the assistant's response to the session history
	session.Messages = append(session.Messages, types.Message{
		Role:    types.ConversationRoleAssistant,
		Content: result.Content,
	})

	sessionID := m.sessions.save(session)
	return &AskResult{
		Response:  result.Text,
		SessionID: sessionID,
	}, nil
}

// Upload scans local sources, diffs against storage, and uploads changed sessions.
func (m *Muse) Upload(ctx context.Context) (*UploadResult, error) {
	log.Println("Listing remote sessions...")
	existing, err := m.storage.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list remote sessions: %w", err)
	}
	log.Printf("Found %d remote sessions\n", len(existing))
	remote := map[string]storage.SessionEntry{}
	for _, e := range existing {
		remote[e.Key] = e
	}

	log.Println("Scanning local sessions...")
	type result struct {
		name     string
		sessions []source.Session
		err      error
	}
	providers := source.Providers()
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, provider := range providers {
		wg.Add(1)
		go func(i int, p source.Provider) {
			defer wg.Done()
			sessions, err := p.Sessions()
			results[i] = result{name: p.Name(), sessions: sessions, err: err}
		}(i, provider)
	}
	wg.Wait()

	var local []source.Session
	var warnings []string
	for _, r := range results {
		if r.err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to read %s sessions: %v", r.name, r.err))
			continue
		}
		log.Printf("Found %d %s sessions\n", len(r.sessions), r.name)
		local = append(local, r.sessions...)
	}

	log.Printf("Diffing %d local sessions against remote...\n", len(local))
	var uploaded, skipped int
	var totalBytes int
	for i := range local {
		sess := &local[i]
		key := fmt.Sprintf("memories/%s/%s.json", sess.Source, sess.SessionID)
		if entry, exists := remote[key]; exists {
			if !sess.UpdatedAt.After(entry.LastModified) {
				log.Printf("  skip (unchanged) %s\n", key)
				skipped++
				continue
			}
		}
		n, err := m.storage.PutSession(ctx, sess)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to upload %s: %v", sess.SessionID, err))
			continue
		}
		log.Printf("  upload (%s) %s\n", FormatBytes(n), key)
		uploaded++
		totalBytes += n
	}
	return &UploadResult{
		Total:    len(local),
		Uploaded: uploaded,
		Skipped:  skipped,
		Bytes:    totalBytes,
		Warnings: warnings,
	}, nil
}

func FormatBytes(b int) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// Soul returns the current soul document for use by the MCP handler.
func (m *Muse) Soul() string {
	return m.soul
}
