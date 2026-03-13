package dream

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellistarn/muse/internal/llm"
	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/source"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// LLM is the subset of an LLM client used by the dream pipeline.
type LLM interface {
	Converse(ctx context.Context, system, user string, opts ...llm.ConverseOption) (string, llm.Usage, error)
}

// Result summarizes a dream run.
type Result struct {
	Processed int
	Pruned    int
	Remaining int // memories still pending reflection
	Usage     llm.Usage
	Warnings  []string
}

// Options configures a dream run.
type Options struct {
	// Reflect ignores persisted reflections and re-reflects all memories.
	Reflect bool
	// Limit caps how many memories to process (0 means no limit).
	Limit int
}

// estimateTokens returns a rough token count for a string (~4 chars per token).
func estimateTokens(s string) int {
	return len(s) / 4
}

// Run executes the dream pipeline: reflect on new memories, then learn a soul
// from all reflections. Reflections are the source of truth for what has been
// processed; there is no separate state file.
func Run(ctx context.Context, store storage.Store, reflectLLM, learnLLM LLM, opts Options) (*Result, error) {
	// List all memories and existing reflections
	log.Println("Listing memories...")
	entries, err := store.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list memories: %w", err)
	}

	reflections, err := store.ListReflections(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list reflections: %w", err)
	}

	// If reprocessing, clear all existing reflections
	if opts.Reflect {
		log.Println("Re-reflecting all memories (clearing existing reflections)")
		if err := store.DeletePrefix(ctx, "dreams/reflections/"); err != nil {
			return nil, fmt.Errorf("failed to clear reflections: %w", err)
		}
		reflections = map[string]time.Time{}
	}

	// Diff: memories without a corresponding reflection (or stale ones) are pending
	var pending []storage.SessionEntry
	var pruned int
	for _, e := range entries {
		if reflected, ok := reflections[e.Key]; ok && !e.LastModified.After(reflected) {
			pruned++
			continue
		}
		pending = append(pending, e)
	}
	// Sort newest first so the limit keeps the most recent memories.
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].LastModified.After(pending[j].LastModified)
	})
	totalPending := len(pending)
	if opts.Limit > 0 && len(pending) > opts.Limit {
		log.Printf("Found %d new memories, limiting to %d\n", len(pending), opts.Limit)
		pending = pending[:opts.Limit]
	}
	log.Printf("Found %d memories (%d new, %d already reflected)\n", len(entries), totalPending, pruned)

	var warnings []string
	var reflectUsage llm.Usage

	// Reflect on pending memories in parallel
	if len(pending) > 0 {
		log.Println("Estimating token usage...")
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
		log.Printf("Estimated ~%dk input tokens for reflect phase\n", totalEstimate/1000)

		log.Printf("Reflecting on %d memories...\n", len(pending))
		type mapResult struct {
			key          string
			observations string
			usage        llm.Usage
			err          error
		}
		results := make([]mapResult, len(pending))
		var completed atomic.Int32
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for i, entry := range pending {
			wg.Add(1)
			go func(i int, entry storage.SessionEntry) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				session, err := store.GetSession(ctx, entry.Source, entry.SessionID)
				if err != nil {
					results[i] = mapResult{key: entry.Key, err: err}
					n := completed.Add(1)
					log.Printf("  [%d/%d] (error) %s\n", n, len(pending), entry.Key)
					return
				}
				msgs := len(session.Messages)
				obs, usage, err := reflectOnSession(ctx, reflectLLM, session)
				results[i] = mapResult{key: entry.Key, observations: obs, usage: usage, err: err}
				n := completed.Add(1)
				if err != nil {
					log.Printf("  [%d/%d] (%d msgs) error: %v %s\n", n, len(pending), msgs, err, entry.Key)
				} else {
					log.Printf("  [%d/%d] (%d msgs, %d in / %d out tokens, $%.4f) %s\n",
						n, len(pending), msgs, usage.InputTokens, usage.OutputTokens, usage.Cost(), entry.Key)
				}
			}(i, entry)
		}
		wg.Wait()

		// Persist reflections and collect warnings
		for _, r := range results {
			if r.err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to process %s: %v", r.key, r.err))
				continue
			}
			reflectUsage = reflectUsage.Add(r.usage)
			if err := store.PutReflection(ctx, r.key, r.observations); err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to save reflection for %s: %v", r.key, err))
			}
		}
		log.Printf("Reflected on %d memories ($%.4f)\n", len(pending)-len(warnings), reflectUsage.Cost())
	}

	remaining := totalPending - len(pending)
	if remaining > 0 {
		log.Printf("%d memories still pending reflection (run dream again to continue)\n", remaining)
	}

	// Learn from ALL reflections (not just new ones)
	allReflections, err := loadAllReflections(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load reflections: %w", err)
	}
	if len(allReflections) == 0 {
		return &Result{Pruned: pruned, Remaining: remaining, Warnings: warnings}, nil
	}

	log.Printf("Distilling soul from %d reflections...\n", len(allReflections))
	learnUsage, err := learn(ctx, learnLLM, store, allReflections)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	log.Printf("Soul distilled ($%.4f)\n", learnUsage.Cost())

	processed := len(pending) - len(warnings)
	if processed < 0 {
		processed = 0
	}
	return &Result{
		Processed: processed,
		Pruned:    pruned,
		Remaining: remaining,
		Usage:     reflectUsage.Add(learnUsage),
		Warnings:  warnings,
	}, nil
}

// LearnOnly re-runs only the learn phase using persisted reflections.
// Use this to re-synthesize the soul with improved techniques without re-reflecting.
func LearnOnly(ctx context.Context, store storage.Store, learnLLM LLM) (*Result, error) {
	allReflections, err := loadAllReflections(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load reflections: %w", err)
	}
	if len(allReflections) == 0 {
		return &Result{}, nil
	}

	log.Printf("Re-distilling soul from %d reflections...\n", len(allReflections))
	usage, err := learn(ctx, learnLLM, store, allReflections)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	log.Printf("Soul distilled ($%.4f)\n", usage.Cost())

	return &Result{
		Usage:    usage,
		Warnings: nil,
	}, nil
}

// writeSoul snapshots the existing soul and writes the new one.
func writeSoul(ctx context.Context, store storage.Store, soul string) error {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	log.Printf("Snapshotting previous soul to dreams/history/%s/...\n", timestamp)
	if err := store.SnapshotSoul(ctx, timestamp); err != nil {
		log.Printf("Snapshot skipped (expected on first dream): %v\n", err)
	} else {
		log.Printf("Snapshot saved\n")
	}
	log.Println("Writing soul to storage...")
	return store.PutSoul(ctx, soul)
}

// loadAllReflections fetches every persisted reflection from storage.
func loadAllReflections(ctx context.Context, store storage.Store) ([]string, error) {
	index, err := store.ListReflections(ctx)
	if err != nil {
		return nil, err
	}
	var reflections []string
	for memoryKey := range index {
		content, err := store.GetReflection(ctx, memoryKey)
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

func reflectOnSession(ctx context.Context, client LLM, session *source.Session) (string, llm.Usage, error) {
	turns := extractTurns(session)
	if len(turns) == 0 {
		return "", llm.Usage{}, nil
	}

	var totalUsage llm.Usage

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
		obs, usage, err := client.Converse(ctx, prompts.ReflectExtract, chunk, llm.WithMaxTokens(4096))
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
	refined, usage, err := client.Converse(ctx, prompts.ReflectRefine, candidates, llm.WithMaxTokens(4096))
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
func buildHumanFocusedView(ctx context.Context, client LLM, turns []turn) ([]string, llm.Usage, error) {
	var totalUsage llm.Usage
	var chunks []string
	var b strings.Builder

	for _, t := range turns {
		// Summarize the assistant's message into 1-2 structural sentences
		var contextLine string
		if t.assistantContent != "" {
			summary, usage, err := client.Converse(ctx, prompts.ReflectSummarize, t.assistantContent, llm.WithMaxTokens(256))
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

func learn(ctx context.Context, client LLM, store storage.Store, observations []string) (llm.Usage, error) {
	if len(observations) == 0 {
		return llm.Usage{}, nil
	}
	input := strings.Join(observations, "\n\n---\n\n")
	soul, usage, err := client.Converse(ctx, prompts.Learn, input, llm.WithThinking(16000))
	if err != nil {
		return usage, err
	}
	// Strip markdown code fences the LLM sometimes wraps output in
	soul = stripCodeFences(soul)
	if err := writeSoul(ctx, store, soul); err != nil {
		return usage, fmt.Errorf("failed to write soul: %w", err)
	}
	return usage, nil
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
func extractTurns(session *source.Session) []turn {
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
