package dream

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellistarn/shade/internal/bedrock"
	"github.com/ellistarn/shade/internal/source"
	"github.com/ellistarn/shade/internal/storage"
)

// State tracks which memories have been processed so we can prune on subsequent runs.
type State struct {
	// LastDream is when the last dream completed.
	LastDream time.Time `json:"last_dream"`
	// Memories maps each memory key to when it was last processed.
	Memories map[string]time.Time `json:"memories"`
}

const stateKey = "dream/state.json"

// Result summarizes a dream run.
type Result struct {
	Processed    int
	Pruned       int
	Skills       int
	InputTokens  int
	OutputTokens int
	Warnings     []string
}

// Options configures a dream run.
type Options struct {
	// Reprocess ignores saved state and reprocesses all memories.
	Reprocess bool
	// Limit caps how many memories to process (0 means no limit).
	Limit int
}

// estimateTokens returns a rough token count for a string (~4 chars per token).
func estimateTokens(s string) int {
	return len(s) / 4
}

// Run executes the dream pipeline: load state, map new memories to observations,
// reduce observations into skills, and persist the results.
func Run(ctx context.Context, store *storage.Client, llm *bedrock.Client, opts Options) (*Result, error) {
	// Load prior dream state (missing state means first run)
	var state State
	if opts.Reprocess {
		fmt.Println("Reprocessing all memories (ignoring saved state)")
		state = State{Memories: map[string]time.Time{}}
	} else {
		fmt.Println("Loading dream state...")
		if err := store.GetJSON(ctx, stateKey, &state); err != nil {
			fmt.Println("No prior dream state found, starting fresh")
			state = State{Memories: map[string]time.Time{}}
		}
	}

	// List all memories and filter to ones we haven't processed since their last modification
	fmt.Println("Listing memories...")
	entries, err := store.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list memories: %w", err)
	}
	var pending []storage.SessionEntry
	var pruned int
	for _, e := range entries {
		if processed, ok := state.Memories[e.Key]; ok && !e.LastModified.After(processed) {
			pruned++
			continue
		}
		pending = append(pending, e)
	}
	if opts.Limit > 0 && len(pending) > opts.Limit {
		fmt.Printf("Found %d new memories, limiting to %d\n", len(pending), opts.Limit)
		pending = pending[:opts.Limit]
	}
	fmt.Printf("Found %d memories (%d new, %d already processed)\n", len(entries), len(pending), pruned)
	if len(pending) == 0 {
		return &Result{Pruned: pruned, Skills: 0}, nil
	}

	// Estimate tokens: load each session and estimate the input size
	fmt.Println("Estimating token usage...")
	var totalEstimate int
	for _, entry := range pending {
		session, err := store.GetSession(ctx, entry.Source, entry.SessionID)
		if err != nil {
			continue
		}
		conversation := formatSession(session)
		totalEstimate += estimateTokens(reflectPrompt) + estimateTokens(conversation)
	}
	fmt.Printf("Estimated ~%dk input tokens for reflect phase\n", totalEstimate/1000)

	// Reflect: extract observations from each new/updated memory in parallel
	fmt.Printf("Reflecting on %d memories...\n", len(pending))
	type mapResult struct {
		key          string
		observations string
		err          error
	}
	results := make([]mapResult, len(pending))
	var inputTokens, outputTokens atomic.Int64
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
				fmt.Printf("  [%d/%d] %s (error)\n", n, len(pending), entry.Key)
				return
			}
			msgs := len(session.Messages)
			obs, usage, err := reflect(ctx, llm, session)
			inputTokens.Add(int64(usage.InputTokens))
			outputTokens.Add(int64(usage.OutputTokens))
			results[i] = mapResult{key: entry.Key, observations: obs, err: err}
			n := completed.Add(1)
			if err != nil {
				fmt.Printf("  [%d/%d] %s (%d msgs) error: %v\n", n, len(pending), entry.Key, msgs, err)
			} else {
				fmt.Printf("  [%d/%d] %s (%d msgs, %d in / %d out tokens, $%.4f)\n",
					n, len(pending), entry.Key, msgs, usage.InputTokens, usage.OutputTokens, usage.Cost())
			}
		}(i, entry)
	}
	wg.Wait()

	// Collect observations, record warnings for failures
	var allObservations []string
	var warnings []string
	processedKeys := map[string]time.Time{}
	for _, r := range results {
		if r.err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to process %s: %v", r.key, r.err))
			continue
		}
		if r.observations != "" {
			allObservations = append(allObservations, r.observations)
		}
		processedKeys[r.key] = time.Now()
	}
	reflectUsage := bedrock.Usage{InputTokens: int(inputTokens.Load()), OutputTokens: int(outputTokens.Load())}
	fmt.Printf("Reflected on %d memories ($%.4f)\n", len(allObservations), reflectUsage.Cost())

	// Learn: compress observations into skills
	fmt.Printf("Learning %d observations into skills...\n", len(allObservations))
	skills, learnUsage, err := learn(ctx, llm, allObservations)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	inputTokens.Add(int64(learnUsage.InputTokens))
	outputTokens.Add(int64(learnUsage.OutputTokens))
	fmt.Printf("Produced %d skills ($%.4f)\n", len(skills), learnUsage.Cost())

	// Write skills to S3 (clear old skills first, dream produces a complete set)
	fmt.Println("Writing skills to storage...")
	if err := store.DeletePrefix(ctx, "skills/"); err != nil {
		return nil, fmt.Errorf("failed to clear old skills: %w", err)
	}
	for name, content := range skills {
		if err := store.PutSkill(ctx, name, content); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to write skill %s: %v", name, err))
		}
	}

	// Update state with newly processed memories
	fmt.Println("Saving dream state...")
	for k, v := range processedKeys {
		state.Memories[k] = v
	}
	state.LastDream = time.Now()
	if err := store.PutJSON(ctx, stateKey, &state); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to save dream state: %v", err))
	}

	return &Result{
		Processed:    len(allObservations),
		Pruned:       pruned,
		Skills:       len(skills),
		InputTokens:  int(inputTokens.Load()),
		OutputTokens: int(outputTokens.Load()),
		Warnings:     warnings,
	}, nil
}

func reflect(ctx context.Context, llm *bedrock.Client, session *source.Session) (string, bedrock.Usage, error) {
	conversation := formatSession(session)
	if conversation == "" {
		return "", bedrock.Usage{}, nil
	}
	return llm.Converse(ctx, reflectPrompt, conversation)
}

func learn(ctx context.Context, llm *bedrock.Client, observations []string) (map[string]string, bedrock.Usage, error) {
	if len(observations) == 0 {
		return nil, bedrock.Usage{}, nil
	}
	input := strings.Join(observations, "\n\n---\n\n")
	raw, usage, err := llm.Converse(ctx, learnPrompt, input)
	if err != nil {
		return nil, usage, err
	}
	skills, err := parseSkillsResponse(raw)
	return skills, usage, err
}

func formatSession(session *source.Session) string {
	var b strings.Builder
	for _, msg := range session.Messages {
		if msg.Content == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s]: %s\n\n", msg.Role, msg.Content)
	}
	return b.String()
}

// parseSkillsResponse splits the LLM's reduce output into individual skill files.
// Expected format: multiple blocks delimited by "=== SKILL: skill-name ===" headers,
// where each block contains the complete SKILL.md content (frontmatter + body).
func parseSkillsResponse(raw string) (map[string]string, error) {
	// Strip markdown code fences the LLM sometimes wraps output in
	cleaned := strings.TrimSpace(raw)
	if strings.HasPrefix(cleaned, "```") {
		if idx := strings.Index(cleaned, "\n"); idx != -1 {
			cleaned = cleaned[idx+1:]
		}
		if strings.HasSuffix(cleaned, "```") {
			cleaned = cleaned[:len(cleaned)-3]
		}
		cleaned = strings.TrimSpace(cleaned)
	}

	skills := map[string]string{}
	sections := strings.Split(cleaned, "=== SKILL:")
	for _, section := range sections[1:] { // skip content before first delimiter
		// Find the closing "===" with flexible whitespace handling
		endHeader := -1
		for i := 0; i < len(section); i++ {
			if i+3 <= len(section) && section[i:i+3] == "===" && (i == 0 || section[i-1] == ' ') {
				// Check it's the closing delimiter, not part of content
				rest := section[i+3:]
				trimmed := strings.TrimLeft(rest, " \t")
				if len(trimmed) == 0 || trimmed[0] == '\n' || trimmed[0] == '\r' {
					endHeader = i
					// Advance past the === and the newline
					skipTo := i + 3 + (len(rest) - len(trimmed))
					if skipTo < len(section) && (section[skipTo] == '\n' || section[skipTo] == '\r') {
						skipTo++
					}
					name := strings.TrimSpace(section[:endHeader])
					content := strings.TrimSpace(section[skipTo:])
					if name != "" && content != "" {
						skills[name] = content
					}
					break
				}
			}
		}
	}
	if len(skills) == 0 {
		// Log a snippet of the raw output to aid debugging
		snippet := raw
		if len(snippet) > 500 {
			snippet = snippet[:500] + "..."
		}
		return nil, fmt.Errorf("no skills found in learn output. Raw response starts with:\n%s", snippet)
	}
	return skills, nil
}
