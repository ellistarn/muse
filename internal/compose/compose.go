package compose

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// StageStats captures telemetry for a single pipeline stage.
type StageStats struct {
	Name     string
	Model    string        // model or tool used (e.g. "us.anthropic.claude-sonnet-4-20250514-v1:0")
	Duration time.Duration // wall-clock time for the stage
	Usage    inference.Usage
	DataSize int // bytes of input data processed
}

// Result summarizes a compose run.
type Result struct {
	Processed    int
	Pruned       int
	Remaining    int // conversations still pending observation
	Observations int // total observations across all conversations
	Clusters     int // clusters discovered by clustering (0 for map-reduce)
	Noise        int // observations that didn't fit any cluster
	Cache        CacheStats
	Stages       []StageStats
	Usage        inference.Usage
	Muse         string // the composed muse.md
}

// CacheStats tracks cache hit/miss counts for each cached pipeline stage.
type CacheStats struct {
	Observe HitMiss
	Label   HitMiss
}

// HitMiss tracks cache hit and miss counts.
type HitMiss struct {
	Hit  int
	Miss int
}

// BaseOptions contains fields shared across all compose strategies.
type BaseOptions struct {
	// Reobserve ignores persisted observations and re-observes all conversations.
	Reobserve bool
	// Limit caps how many conversations to process (0 means no limit).
	Limit int
	// Verbose enables per-item progress logging.
	Verbose bool
}

// Options configures a map-reduce compose run.
type Options struct {
	BaseOptions
	// Learn skips observe and only recomposes from existing observations.
	Learn bool
}

// Run executes the compose pipeline: observe new conversations, then learn a muse
// from all observations. Observations are the source of truth for what has been
// processed; there is no separate state file.
func Run(ctx context.Context, store storage.Store, observeLLM, learnLLM inference.Client, opts Options) (*Result, error) {
	// Preload source metadata for import sources so isHumanSource can resolve them
	humanOverrides := loadHumanSources(ctx, store)

	// List all conversations and existing observations
	entries, err := store.ListConversations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}

	existingObs, err := ListObservations(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to list observations: %w", err)
	}
	existingSet := make(map[string]bool, len(existingObs))
	for _, sc := range existingObs {
		existingSet[sc.Source+"/"+sc.ConversationID] = true
	}

	// If reprocessing, clear all existing observations
	if opts.Reobserve {
		if err := DeleteObservations(ctx, store); err != nil {
			return nil, fmt.Errorf("failed to clear observations: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Cleared observations/")
		// Rebuild observations set after deletion
		existingObs, err = ListObservations(ctx, store)
		if err != nil {
			return nil, fmt.Errorf("failed to re-list observations: %w", err)
		}
		existingSet = make(map[string]bool, len(existingObs))
		for _, sc := range existingObs {
			existingSet[sc.Source+"/"+sc.ConversationID] = true
		}
	}

	// Diff: conversations without corresponding observations are pending
	var pending []storage.ConversationEntry
	var pruned int
	for _, e := range entries {
		if existingSet[e.Source+"/"+e.ConversationID] {
			pruned++
			continue
		}
		pending = append(pending, e)
	}
	// Sort newest first so the limit keeps the most recent conversations.
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].LastModified.After(pending[j].LastModified)
	})
	totalPending := len(pending)
	if opts.Limit > 0 && len(pending) > opts.Limit {
		pending = pending[:opts.Limit]
	}
	// Re-sort largest first so the most expensive conversations start
	// processing immediately rather than landing in the tail.
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].SizeBytes > pending[j].SizeBytes
	})

	var mu sync.Mutex
	var firstErr error
	var observeUsage inference.Usage

	// Observe pending conversations in parallel
	if len(pending) > 0 {
		observeStart := time.Now()
		var completed atomic.Int32
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, entry := range pending {
			wg.Add(1)
			go func(entry storage.ConversationEntry) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				conv, err := store.GetConversation(ctx, entry.Source, entry.ConversationID)
				if err != nil {
					completed.Add(1)
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("load conversation %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}
				start := time.Now()
				obs, usage, err := observeConversation(ctx, observeLLM, conv, humanOverrides)
				n := completed.Add(1)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("observe %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}

				// Persist as structured JSON so both pipelines share a single format
				items := parseObservationItems(obs)
				fp := Fingerprint(entry.LastModified.Format(time.RFC3339Nano), Fingerprint(prompts.Observe, prompts.Refine))
				structured := &Observations{
					Fingerprint: fp,
					Date:        entry.LastModified.Format("2006-01-02"),
					Items:       items,
				}
				if err := PutObservations(ctx, store, entry.Source, entry.ConversationID, structured); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("save observation for %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "  [%d/%d] Observed ~/.muse/%s (%s, $%.4f)\n",
						n, len(pending), observationPath(entry.Source, entry.ConversationID), time.Since(start).Round(time.Millisecond), usage.Cost())
				}
				mu.Lock()
				observeUsage = observeUsage.Add(usage)
				mu.Unlock()
			}(entry)
		}
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
		fmt.Fprintf(os.Stderr, "Observed %d conversations (%s, $%.4f)\n",
			len(pending), time.Since(observeStart).Round(time.Millisecond), observeUsage.Cost())
	}

	remaining := totalPending - len(pending)

	// Learn from ALL observations (not just new ones)
	allObservations, err := loadAllObservations(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load observations: %w", err)
	}
	if len(allObservations) == 0 {
		return &Result{Pruned: pruned, Remaining: remaining}, nil
	}

	learnStart := time.Now()
	muse, _, learnUsage, err := learn(ctx, learnLLM, store, allObservations)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Muse composed (%s, $%.4f)\n", time.Since(learnStart).Round(time.Millisecond), learnUsage.Cost())

	processed := len(pending)
	return &Result{
		Processed: processed,
		Pruned:    pruned,
		Remaining: remaining,
		Usage:     observeUsage.Add(learnUsage),
		Muse:      muse,
	}, nil
}

// LearnOnly re-runs only the learn phase using persisted observations.
// Use this to recompose the muse with improved techniques without re-observing.
func LearnOnly(ctx context.Context, store storage.Store, learnLLM inference.Client) (*Result, error) {
	allObservations, err := loadAllObservations(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load observations: %w", err)
	}
	if len(allObservations) == 0 {
		return &Result{}, nil
	}

	start := time.Now()
	muse, _, usage, err := learn(ctx, learnLLM, store, allObservations)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Muse composed (%s, $%.4f)\n", time.Since(start).Round(time.Millisecond), usage.Cost())

	return &Result{
		Usage: usage,
		Muse:  muse,
	}, nil
}

// loadAllObservations fetches every persisted observation from storage and
// returns them as text strings (for the map-reduce learn step).
func loadAllObservations(ctx context.Context, store storage.Store) ([]string, error) {
	convList, err := ListObservations(ctx, store)
	if err != nil {
		return nil, err
	}
	var observations []string
	for _, sc := range convList {
		obs, err := GetObservations(ctx, store, sc.Source, sc.ConversationID)
		if err != nil {
			return nil, fmt.Errorf("get observation %s/%s: %w", sc.Source, sc.ConversationID, err)
		}
		// Format each observation item as text for the learn step
		for _, item := range obs.Items {
			entry := observationEntry{
				Source:         sc.Source,
				ConversationID: sc.ConversationID,
				Quote:          item.Quote,
				Text:           item.Text,
				Date:           obs.Date,
			}
			observations = append(observations, entry.Format())
		}
	}
	return observations, nil
}

// turn represents a human message paired with the assistant message that preceded it.
type turn struct {
	assistantContent string // raw assistant content (may be long)
	humanContent     string // human's message
}

func observeConversation(ctx context.Context, client inference.Client, conv *conversation.Conversation, humanOverrides map[string]bool) (string, inference.Usage, error) {
	refined, usage, err := observeAndRefine(ctx, client, conv, false, humanOverrides)
	if err != nil {
		return "", usage, err
	}
	return refined, usage, nil
}

// isEmpty checks if the LLM output has no substantive content.
// isEmpty returns true if the LLM response is empty or a common null marker.
// This prevents trivial responses like "None" or "N/A" from triggering
// downstream LLM calls (e.g. a refine pass on empty observe output).
func isEmpty(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return true
	}
	switch strings.ToLower(s) {
	case "none", "none.", "n/a", "empty", "(none)", "(empty)", "(empty response)":
		return true
	}
	return false
}

func learn(ctx context.Context, client inference.Client, store storage.Store, observations []string) (string, string, inference.Usage, error) {
	if len(observations) == 0 {
		return "", "", inference.Usage{}, nil
	}
	input := strings.Join(observations, "\n\n---\n\n")
	muse, usage, err := inference.Converse(ctx, client, prompts.Compose, input, inference.WithThinking(16000))
	if err != nil {
		return "", "", usage, err
	}
	// Strip markdown code fences the LLM sometimes wraps output in
	muse = stripCodeFences(muse)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	if err := store.PutMuse(ctx, timestamp, muse); err != nil {
		return "", "", usage, fmt.Errorf("failed to write muse: %w", err)
	}
	return muse, timestamp, usage, nil
}

// ComputeDiff summarizes what changed between two muse versions. On first run
// (no previous version), writes a static message without an LLM call.
func ComputeDiff(ctx context.Context, client inference.Client, store storage.Store, timestamp, previous, current string) (string, inference.Usage, error) {
	var d string
	var usage inference.Usage

	if previous == "" {
		d = "Initial version."
	} else {
		input := fmt.Sprintf("Previous muse:\n%s\n\n---\n\nNew muse:\n%s", previous, current)
		stream := newStageStream(0, 4096) // no thinking, writing bar against 4k budget
		var err error
		d, usage, err = inference.ConverseStream(ctx, client, prompts.Diff, input, stream.callback(), inference.WithMaxTokens(4096))
		stream.finish()
		if err != nil {
			return "", usage, err
		}
		d = strings.TrimSpace(d)
	}

	if werr := store.PutMuseDiff(ctx, timestamp, d); werr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write diff: %v\n", werr)
	}
	return d, usage, nil
}

// stripCodeFences removes wrapping ```markdown ... ``` from LLM output.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if strings.HasSuffix(s, "```") {
			s = s[:len(s)-3]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

// maxChunkChars caps each conversation chunk to ~50k tokens of input,
// leaving headroom for the system prompt and output.
const maxChunkChars = 200_000

// extractTurns extracts human/assistant pairs from a conversation. Each turn pairs
// the assistant message that preceded a human response with that human message.
// For AI conversations, requires at least 2 human turns (corrections/preferences).
// For peer conversations (e.g. Slack), a single human turn suffices since even
// one substantive statement reveals reasoning, awareness, and voice.
func extractTurns(conv *conversation.Conversation, humanOverrides map[string]bool) []turn {
	var userTurns int
	for _, msg := range conv.Messages {
		if msg.Role == "user" && len(msg.Content) > 0 {
			userTurns++
		}
	}
	minTurns := 2
	if isHumanSource(conv.Source, humanOverrides) {
		minTurns = 1
	}
	if userTurns < minTurns {
		return nil
	}

	var turns []turn
	var lastAssistant string
	for _, msg := range conv.Messages {
		switch msg.Role {
		case "assistant":
			// Accumulate assistant content (may include tool call names)
			var parts []string
			if msg.Content != "" {
				parts = append(parts, msg.Content)
			}
			for _, tc := range msg.ToolCalls {
				parts = append(parts, fmt.Sprintf("[tool: %s]", tc.Name))
			}
			if len(parts) > 0 {
				lastAssistant = strings.Join(parts, "\n")
			}
		case "user":
			if msg.Content == "" {
				continue
			}
			turns = append(turns, turn{
				assistantContent: lastAssistant,
				humanContent:     msg.Content,
			})
			lastAssistant = ""
		}
	}
	return turns
}
