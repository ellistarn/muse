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
}

// Result summarizes a distill run.
type Result struct {
	Processed int
	Pruned    int
	Remaining int // conversations still pending reflection
	Usage     inference.Usage
	Muse      string // the distilled muse.md
	Diff      string // what changed from the previous muse version
	Warnings  []string
}

// Options configures a distill run.
type Options struct {
	// Reflect ignores persisted reflections and re-reflects all conversations.
	Reflect bool
	// Learn skips reflect and only re-distills from existing reflections.
	Learn bool
	// Limit caps how many conversations to process (0 means no limit).
	Limit int
	// Sources filters to conversations from specific sources (e.g. "kiro").
	// Empty means all sources.
	Sources []string
}

// estimateTokens is a convenience alias for inference.EstimateTokens.
var estimateTokens = inference.EstimateTokens

// Run executes the distill pipeline: reflect on new conversations, then learn a muse
// from all reflections. Reflections are the source of truth for what has been
// processed; there is no separate state file.
func Run(ctx context.Context, store storage.Store, reflectLLM, learnLLM LLM, opts Options) (*Result, error) {
	// List all conversations and existing reflections
	fmt.Fprintln(os.Stderr, "Listing conversations...")
	entries, err := store.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}

	reflections, err := store.ListReflections(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list reflections: %w", err)
	}

	// If reprocessing, clear existing reflections (scoped to sources if set)
	if opts.Reflect {
		if len(opts.Sources) > 0 {
			for _, src := range opts.Sources {
				prefix := "reflections/" + src + "/"
				fmt.Fprintf(os.Stderr, "Re-reflecting conversations (clearing %s)\n", prefix)
				if err := store.DeletePrefix(ctx, prefix); err != nil {
					return nil, fmt.Errorf("failed to clear reflections: %w", err)
				}
			}
		} else {
			fmt.Fprintln(os.Stderr, "Re-reflecting all conversations (clearing reflections/)")
			if err := store.DeletePrefix(ctx, "reflections/"); err != nil {
				return nil, fmt.Errorf("failed to clear reflections: %w", err)
			}
		}
		// Rebuild reflections map after deletion
		reflections, err = store.ListReflections(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to re-list reflections: %w", err)
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

	// Diff: conversations without a corresponding reflection (or stale ones) are pending
	var pending []storage.SessionEntry
	var pruned int
	for _, e := range entries {
		if reflected, ok := reflections[e.Key]; ok && !e.LastModified.After(reflected) {
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
		fmt.Fprintf(os.Stderr, "Found %d new conversations, limiting to %d\n", len(pending), opts.Limit)
		pending = pending[:opts.Limit]
	}
	fmt.Fprintf(os.Stderr, "Found %d conversations (%d new, %d already reflected)\n", len(entries), totalPending, pruned)

	var mu sync.Mutex
	var warnings []string
	var reflectUsage inference.Usage

	// Reflect on pending conversations in parallel
	if len(pending) > 0 {
		fmt.Fprintln(os.Stderr, "Estimating token usage...")
		var totalEstimate int
		for _, entry := range pending {
			session, err := store.GetSession(ctx, entry.Source, entry.SessionID)
			if err != nil {
				continue
			}
			turns := extractTurns(session)
			for _, t := range turns {
				totalEstimate += estimateTokens(prompts.ReflectSummarize) + estimateTokens(t.assistantContent)
				totalEstimate += estimateTokens(t.humanContent)
			}
			// Add estimate for extract + refine passes
			totalEstimate += estimateTokens(prompts.ReflectExtract) + estimateTokens(prompts.ReflectRefine)
		}
		fmt.Fprintf(os.Stderr, "Estimated ~%dk input tokens for reflect phase\n", totalEstimate/1000)

		fmt.Fprintf(os.Stderr, "Reflecting on %d conversations...\n", len(pending))
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
					n := completed.Add(1)
					fmt.Fprintf(os.Stderr, "  [%d/%d] (error) %s\n", n, len(pending), entry.Key)
					mu.Lock()
					warnings = append(warnings, fmt.Sprintf("failed to process %s: %v", entry.Key, err))
					mu.Unlock()
					return
				}
				msgs := len(session.Messages)
				obs, usage, err := reflectOnSession(ctx, reflectLLM, session)
				n := completed.Add(1)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  [%d/%d] (%d msgs) error: %v %s\n", n, len(pending), msgs, err, entry.Key)
					mu.Lock()
					warnings = append(warnings, fmt.Sprintf("failed to process %s: %v", entry.Key, err))
					mu.Unlock()
					return
				}
				if obs == "" {
					fmt.Fprintf(os.Stderr, "  [%d/%d] (skipped) %s\n", n, len(pending), entry.Key)
				} else {
					fmt.Fprintf(os.Stderr, "  [%d/%d] (%d msgs, $%.4f) %s\n",
						n, len(pending), msgs, usage.Cost(), entry.Key)
				}
				// Persist immediately so progress survives cancellation
				if err := store.PutReflection(ctx, entry.Key, obs); err != nil {
					mu.Lock()
					warnings = append(warnings, fmt.Sprintf("failed to save reflection for %s: %v", entry.Key, err))
					mu.Unlock()
					return
				}
				mu.Lock()
				reflectUsage = reflectUsage.Add(usage)
				mu.Unlock()
			}(entry)
		}
		wg.Wait()
		fmt.Fprintf(os.Stderr, "Reflected on %d conversations ($%.4f)\n", len(pending)-len(warnings), reflectUsage.Cost())
	}

	remaining := totalPending - len(pending)
	if remaining > 0 {
		fmt.Fprintf(os.Stderr, "%d conversations still pending reflection (run distill again to continue)\n", remaining)
	}

	// Learn from ALL reflections (not just new ones)
	allReflections, err := loadAllReflections(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load reflections: %w", err)
	}
	if len(allReflections) == 0 {
		return &Result{Pruned: pruned, Remaining: remaining, Warnings: warnings}, nil
	}

	// Load previous muse before learning so we can diff afterward.
	previousMuse, _ := store.GetMuse(ctx) // ok if not found (first run)

	muse, timestamp, learnUsage, err := learn(ctx, learnLLM, store, allReflections)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Muse distilled ($%.4f)\n", learnUsage.Cost())

	// Diff is a post-processing step, not part of learning.
	d, diffUsage, derr := computeDiff(ctx, reflectLLM, store, timestamp, previousMuse, muse)
	if derr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to compute diff: %v\n", derr)
	}

	processed := len(pending) - len(warnings)
	if processed < 0 {
		processed = 0
	}
	return &Result{
		Processed: processed,
		Pruned:    pruned,
		Remaining: remaining,
		Usage:     reflectUsage.Add(learnUsage).Add(diffUsage),
		Muse:      muse,
		Diff:      d,
		Warnings:  warnings,
	}, nil
}

// LearnOnly re-runs only the learn phase using persisted reflections.
// Use this to re-synthesize the muse with improved techniques without re-reflecting.
func LearnOnly(ctx context.Context, store storage.Store, learnLLM, diffLLM LLM) (*Result, error) {
	allReflections, err := loadAllReflections(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load reflections: %w", err)
	}
	if len(allReflections) == 0 {
		return &Result{}, nil
	}

	// Load previous muse before learning so we can diff afterward.
	previousMuse, _ := store.GetMuse(ctx)

	muse, timestamp, usage, err := learn(ctx, learnLLM, store, allReflections)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Muse distilled ($%.4f)\n", usage.Cost())

	d, diffUsage, derr := computeDiff(ctx, diffLLM, store, timestamp, previousMuse, muse)
	if derr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to compute diff: %v\n", derr)
	}

	return &Result{
		Usage:    usage.Add(diffUsage),
		Muse:     muse,
		Diff:     d,
		Warnings: nil,
	}, nil
}

// loadAllReflections fetches every persisted reflection from storage.
func loadAllReflections(ctx context.Context, store storage.Store) ([]string, error) {
	index, err := store.ListReflections(ctx)
	if err != nil {
		return nil, err
	}
	var reflections []string
	for conversationKey := range index {
		content, err := store.GetReflection(ctx, conversationKey)
		if err != nil {
			continue
		}
		if content != "" {
			reflections = append(reflections, content)
		}
	}
	return reflections, nil
}

// turn represents a human message paired with the assistant message that preceded it.
type turn struct {
	assistantContent string // raw assistant content (may be long)
	humanContent     string // human's message
}

func reflectOnSession(ctx context.Context, client LLM, session *conversation.Session) (string, inference.Usage, error) {
	turns := extractTurns(session)
	if len(turns) == 0 {
		return "", inference.Usage{}, nil
	}

	var totalUsage inference.Usage

	// Step 1: Summarize assistant context for each turn, then build human-focused view
	chunks, usage, err := buildHumanFocusedView(ctx, client, turns)
	totalUsage = totalUsage.Add(usage)
	if err != nil {
		return "", totalUsage, err
	}
	if len(chunks) == 0 {
		return "", totalUsage, nil
	}

	// Step 2: Extract candidate observations (Pass 1)
	var allCandidates []string
	for _, chunk := range chunks {
		obs, usage, err := client.Converse(ctx, prompts.ReflectExtract, chunk, inference.WithMaxTokens(4096))
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

	// Step 3: Refine observations (Pass 2)
	candidates := strings.Join(allCandidates, "\n\n")
	refined, usage, err := client.Converse(ctx, prompts.ReflectRefine, candidates, inference.WithMaxTokens(4096))
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

// buildHumanFocusedView summarizes assistant messages and formats the conversation
// as [context]/[human] pairs, chunked to fit within the token budget.
func buildHumanFocusedView(ctx context.Context, client LLM, turns []turn) ([]string, inference.Usage, error) {
	var totalUsage inference.Usage
	var chunks []string
	var b strings.Builder

	for _, t := range turns {
		// Summarize the assistant's message into 1-2 structural sentences
		var contextLine string
		if t.assistantContent != "" {
			summary, usage, err := client.Converse(ctx, prompts.ReflectSummarize, t.assistantContent, inference.WithMaxTokens(256))
			totalUsage = totalUsage.Add(usage)
			if err != nil {
				// On error, fall back to a generic context marker
				contextLine = "[context]: (assistant message)\n"
			} else {
				contextLine = fmt.Sprintf("[context]: %s\n", strings.TrimSpace(summary))
			}
		}

		humanLine := fmt.Sprintf("[human]: %s\n\n", t.humanContent)
		entry := contextLine + humanLine

		if b.Len()+len(entry) > maxChunkChars && b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
		}
		b.WriteString(entry)
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks, totalUsage, nil
}

func learn(ctx context.Context, client LLM, store storage.Store, observations []string) (string, string, inference.Usage, error) {
	if len(observations) == 0 {
		return "", "", inference.Usage{}, nil
	}
	input := strings.Join(observations, "\n\n---\n\n")
	muse, usage, err := client.Converse(ctx, prompts.Learn, input, inference.WithThinking(16000))
	if err != nil {
		return "", "", usage, err
	}
	// Strip markdown code fences the LLM sometimes wraps output in
	muse = stripCodeFences(muse)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(os.Stderr, "Writing muse to muse/versions/%s/...\n", timestamp)
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
		var err error
		d, usage, err = client.Converse(ctx, prompts.Diff, input, inference.WithMaxTokens(4096))
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
