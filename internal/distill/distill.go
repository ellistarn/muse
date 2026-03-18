package distill

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

// LLM is the subset of an LLM client used by the distill pipeline.
type LLM interface {
	Converse(ctx context.Context, system, user string, opts ...inference.ConverseOption) (string, inference.Usage, error)
	ConverseStream(ctx context.Context, system, user string, fn inference.StreamFunc, opts ...inference.ConverseOption) (string, inference.Usage, error)
	Model() string
}

// StageStats captures telemetry for a single pipeline stage.
type StageStats struct {
	Name     string
	Model    string        // model or tool used (e.g. "us.anthropic.claude-sonnet-4-20250514-v1:0")
	Duration time.Duration // wall-clock time for the stage
	Usage    inference.Usage
	DataSize int // bytes of input data processed
}

// Result summarizes a distill run.
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
	Muse         string // the distilled muse.md
	Diff         string // what changed from the previous muse version
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

// Options configures a distill run.
type Options struct {
	// Reobserve ignores persisted observations and re-observes all conversations.
	Reobserve bool
	// Learn skips observe and only re-distills from existing observations.
	Learn bool
	// Limit caps how many conversations to process (0 means no limit).
	Limit int
	// Sources filters to conversations from specific sources (e.g. "kiro").
	// Empty means all sources.
	Sources []string
	// Verbose enables per-item progress logging.
	Verbose bool
}

// Run executes the distill pipeline: observe new conversations, then learn a muse
// from all observations. Observations are the source of truth for what has been
// processed; there is no separate state file.
func Run(ctx context.Context, store storage.Store, observeLLM, learnLLM LLM, opts Options) (*Result, error) {
	// List all conversations and existing observations
	entries, err := store.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}

	observations, err := store.ListObservations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list observations: %w", err)
	}

	// If reprocessing, clear existing observations (scoped to sources if set)
	if opts.Reobserve {
		if len(opts.Sources) > 0 {
			for _, src := range opts.Sources {
				prefix := "observations/" + src + "/"
				if err := store.DeletePrefix(ctx, prefix); err != nil {
					return nil, fmt.Errorf("failed to clear observations: %w", err)
				}
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "Cleared %s\n", prefix)
				}
			}
		} else {
			if err := store.DeletePrefix(ctx, "observations/"); err != nil {
				return nil, fmt.Errorf("failed to clear observations: %w", err)
			}
			fmt.Fprintln(os.Stderr, "Cleared observations/")
		}
		// Rebuild observations map after deletion
		observations, err = store.ListObservations(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to re-list observations: %w", err)
		}
	}

	// Filter by sources if specified
	if len(opts.Sources) > 0 {
		allowed := make(map[string]bool, len(opts.Sources))
		for _, s := range opts.Sources {
			allowed[s] = true
		}
		var filtered []storage.SessionEntry
		for _, e := range entries {
			if allowed[e.Source] {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	// Diff: conversations without corresponding observations (or stale ones) are pending
	var pending []storage.SessionEntry
	var pruned int
	for _, e := range entries {
		if observed, ok := observations[e.Key]; ok && !e.LastModified.After(observed) {
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
			go func(entry storage.SessionEntry) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				session, err := store.GetSession(ctx, entry.Source, entry.SessionID)
				if err != nil {
					completed.Add(1)
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("load session %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}
				start := time.Now()
				obs, usage, err := observeSession(ctx, observeLLM, session)
				n := completed.Add(1)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("observe %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}

				// Persist immediately so progress survives cancellation
				if err := store.PutObservation(ctx, entry.Key, obs); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("save observation for %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "  [%d/%d] Observed %s (%s, $%.4f)\n",
						n, len(pending), entry.Key, time.Since(start).Round(time.Millisecond), usage.Cost())
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

	// Load previous muse before learning so we can diff afterward.
	previousMuse, _ := store.GetMuse(ctx) // ok if not found (first run)

	learnStart := time.Now()
	muse, timestamp, learnUsage, err := learn(ctx, learnLLM, store, allObservations)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Muse distilled (%s, $%.4f)\n", time.Since(learnStart).Round(time.Millisecond), learnUsage.Cost())

	// Diff is a post-processing step, not part of learning.
	d, diffUsage, derr := computeDiff(ctx, observeLLM, store, timestamp, previousMuse, muse)
	if derr != nil {
		return nil, fmt.Errorf("diff: %w", derr)
	}

	processed := len(pending)
	return &Result{
		Processed: processed,
		Pruned:    pruned,
		Remaining: remaining,
		Usage:     observeUsage.Add(learnUsage).Add(diffUsage),
		Muse:      muse,
		Diff:      d,
	}, nil
}

// LearnOnly re-runs only the learn phase using persisted observations.
// Use this to re-compose the muse with improved techniques without re-observing.
func LearnOnly(ctx context.Context, store storage.Store, learnLLM, diffLLM LLM) (*Result, error) {
	allObservations, err := loadAllObservations(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load observations: %w", err)
	}
	if len(allObservations) == 0 {
		return &Result{}, nil
	}

	// Load previous muse before learning so we can diff afterward.
	previousMuse, _ := store.GetMuse(ctx)

	start := time.Now()
	muse, timestamp, usage, err := learn(ctx, learnLLM, store, allObservations)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Muse distilled (%s, $%.4f)\n", time.Since(start).Round(time.Millisecond), usage.Cost())

	d, diffUsage, derr := computeDiff(ctx, diffLLM, store, timestamp, previousMuse, muse)
	if derr != nil {
		return nil, fmt.Errorf("diff: %w", derr)
	}

	return &Result{
		Usage: usage.Add(diffUsage),
		Muse:  muse,
		Diff:  d,
	}, nil
}

// loadAllObservations fetches every persisted observation from storage.
func loadAllObservations(ctx context.Context, store storage.Store) ([]string, error) {
	index, err := store.ListObservations(ctx)
	if err != nil {
		return nil, err
	}
	var observations []string
	for conversationKey := range index {
		content, err := store.GetObservation(ctx, conversationKey)
		if err != nil {
			return nil, fmt.Errorf("get observation %s: %w", conversationKey, err)
		}
		if content != "" {
			observations = append(observations, content)
		}
	}
	return observations, nil
}

// turn represents a human message paired with the assistant message that preceded it.
type turn struct {
	assistantContent string // raw assistant content (may be long)
	humanContent     string // human's message
}

func observeSession(ctx context.Context, client LLM, session *conversation.Session) (string, inference.Usage, error) {
	turns := extractTurns(session)
	if len(turns) == 0 {
		return "", inference.Usage{}, nil
	}

	var totalUsage inference.Usage

	// Mechanically compress the conversation — no LLM calls.
	chunks := compressConversation(turns)
	if len(chunks) == 0 {
		return "", totalUsage, nil
	}

	// Extract candidate observations (Pass 1)
	var allCandidates []string
	for _, chunk := range chunks {
		obs, usage, err := client.Converse(ctx, prompts.Extract, chunk, inference.WithMaxTokens(4096))
		totalUsage = totalUsage.Add(usage)
		if err != nil && obs == "" {
			return "", totalUsage, err
		}
		if obs != "" && !isEmpty(obs) {
			allCandidates = append(allCandidates, obs)
		}
	}
	if len(allCandidates) == 0 {
		return "", totalUsage, nil
	}

	// Refine observations (Pass 2)
	candidates := strings.Join(allCandidates, "\n\n")
	refined, usage, err := client.Converse(ctx, prompts.Refine, candidates, inference.WithMaxTokens(4096))
	totalUsage = totalUsage.Add(usage)
	if err != nil {
		return "", totalUsage, err
	}
	if isEmpty(refined) {
		return "", totalUsage, nil
	}
	return refined, totalUsage, nil
}

// isEmpty checks if the LLM output has no substantive content.
func isEmpty(s string) bool {
	return len(strings.TrimSpace(s)) == 0
}

func learn(ctx context.Context, client LLM, store storage.Store, observations []string) (string, string, inference.Usage, error) {
	if len(observations) == 0 {
		return "", "", inference.Usage{}, nil
	}
	input := strings.Join(observations, "\n\n---\n\n")
	muse, usage, err := client.Converse(ctx, prompts.Compose, input, inference.WithThinking(16000))
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

// computeDiff summarizes what changed between two muse versions. On first run
// (no previous version), writes a static message without an LLM call.
func computeDiff(ctx context.Context, client LLM, store storage.Store, timestamp, previous, current string) (string, inference.Usage, error) {
	var d string
	var usage inference.Usage

	if previous == "" {
		d = "Initial version."
	} else {
		input := fmt.Sprintf("Previous muse:\n%s\n\n---\n\nNew muse:\n%s", previous, current)
		stream := newStageStream(0) // no thinking for diff
		var err error
		d, usage, err = client.ConverseStream(ctx, prompts.Diff, input, stream.callback(), inference.WithMaxTokens(4096))
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

// extractTurns extracts human/assistant pairs from a session. Each turn pairs
// the assistant message that preceded a human response with that human message.
// Sessions with fewer than 2 human turns are skipped (no corrections or
// preferences were expressed).
func extractTurns(session *conversation.Session) []turn {
	var userTurns int
	for _, msg := range session.Messages {
		if msg.Role == "user" && len(msg.Content) > 0 {
			userTurns++
		}
	}
	if userTurns < 2 {
		return nil
	}

	var turns []turn
	var lastAssistant string
	for _, msg := range session.Messages {
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
