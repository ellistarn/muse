package dream

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellistarn/shade/internal/llm"
	"github.com/ellistarn/shade/internal/log"
	"github.com/ellistarn/shade/internal/source"
	"github.com/ellistarn/shade/internal/storage"
)

// Store is the subset of storage.Client used by the dream pipeline.
type Store interface {
	ListSessions(ctx context.Context) ([]storage.SessionEntry, error)
	GetSession(ctx context.Context, src, sessionID string) (*source.Session, error)
	ListReflections(ctx context.Context) (map[string]time.Time, error)
	GetReflection(ctx context.Context, memoryKey string) (string, error)
	PutReflection(ctx context.Context, key, content string) error
	DeletePrefix(ctx context.Context, prefix string) error
	PutSkill(ctx context.Context, name, content string) error
	SnapshotSkills(ctx context.Context, timestamp string) error
}

// LLM is the subset of an LLM client used by the dream pipeline.
type LLM interface {
	Converse(ctx context.Context, system, user string) (string, llm.Usage, error)
}

// Result summarizes a dream run.
type Result struct {
	Processed int
	Pruned    int
	Skills    int
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

// Run executes the dream pipeline: reflect on new memories, then learn skills
// from all reflections. Reflections are the source of truth for what has been
// processed; there is no separate state file.
func Run(ctx context.Context, store Store, client LLM, opts Options) (*Result, error) {
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
	if opts.Limit > 0 && len(pending) > opts.Limit {
		log.Printf("Found %d new memories, limiting to %d\n", len(pending), opts.Limit)
		pending = pending[:opts.Limit]
	}
	log.Printf("Found %d memories (%d new, %d already reflected)\n", len(entries), len(pending), pruned)

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
			for _, chunk := range formatSession(session) {
				totalEstimate += estimateTokens(reflectPrompt) + estimateTokens(chunk)
			}
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
				obs, usage, err := reflectOnSession(ctx, client, session)
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
			if r.observations != "" {
				if err := store.PutReflection(ctx, r.key, r.observations); err != nil {
					warnings = append(warnings, fmt.Sprintf("failed to save reflection for %s: %v", r.key, err))
				}
			}
		}
		log.Printf("Reflected on %d memories ($%.4f)\n", len(pending)-len(warnings), reflectUsage.Cost())
	}

	// Learn from ALL reflections (not just new ones)
	allReflections, err := loadAllReflections(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load reflections: %w", err)
	}
	if len(allReflections) == 0 {
		return &Result{Pruned: pruned, Skills: 0, Warnings: warnings}, nil
	}

	log.Printf("Learning skills from %d reflections...\n", len(allReflections))
	skills, learnUsage, err := learn(ctx, client, allReflections)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	log.Printf("Produced %d skills ($%.4f)\n", len(skills), learnUsage.Cost())

	// Write skills (snapshot old skills first, then clear and replace)
	writeWarnings, err := writeSkills(ctx, store, skills)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, writeWarnings...)

	processed := len(pending) - len(warnings)
	if processed < 0 {
		processed = 0
	}
	return &Result{
		Processed: processed,
		Pruned:    pruned,
		Skills:    len(skills),
		Usage:     reflectUsage.Add(learnUsage),
		Warnings:  warnings,
	}, nil
}

// LearnOnly re-runs only the learn phase using persisted reflections.
// Use this to re-synthesize skills with improved techniques without re-reflecting.
func LearnOnly(ctx context.Context, store Store, client LLM) (*Result, error) {
	allReflections, err := loadAllReflections(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to load reflections: %w", err)
	}
	if len(allReflections) == 0 {
		return &Result{}, nil
	}

	log.Printf("Re-learning skills from %d reflections...\n", len(allReflections))
	skills, usage, err := learn(ctx, client, allReflections)
	if err != nil {
		return nil, fmt.Errorf("learn failed: %w", err)
	}
	log.Printf("Produced %d skills ($%.4f)\n", len(skills), usage.Cost())

	log.Println("Writing skills to storage...")
	writeWarnings, err := writeSkills(ctx, store, skills)
	if err != nil {
		return nil, err
	}

	return &Result{
		Skills:   len(skills),
		Usage:    usage,
		Warnings: writeWarnings,
	}, nil
}

// writeSkills snapshots existing skills, clears them, and writes the new set.
func writeSkills(ctx context.Context, store Store, skills map[string]string) (warnings []string, err error) {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	log.Printf("Snapshotting previous skills to dreams/history/%s/...\n", timestamp)
	if err := store.SnapshotSkills(ctx, timestamp); err != nil {
		log.Printf("Snapshot skipped (expected on first dream): %v\n", err)
	} else {
		log.Printf("Snapshot saved\n")
	}
	log.Printf("Writing %d skills to storage...\n", len(skills))
	if err := store.DeletePrefix(ctx, "skills/"); err != nil {
		return nil, fmt.Errorf("failed to clear old skills: %w", err)
	}
	for name, content := range skills {
		if err := store.PutSkill(ctx, name, content); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to write skill %s: %v", name, err))
		}
	}
	return warnings, nil
}

// loadAllReflections fetches every persisted reflection from storage.
func loadAllReflections(ctx context.Context, store Store) ([]string, error) {
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

func reflectOnSession(ctx context.Context, client LLM, session *source.Session) (string, llm.Usage, error) {
	chunks := formatSession(session)
	if len(chunks) == 0 {
		return "", llm.Usage{}, nil
	}
	var allObs []string
	var totalUsage llm.Usage
	for _, chunk := range chunks {
		obs, usage, err := client.Converse(ctx, reflectPrompt, chunk)
		if err != nil {
			return "", totalUsage, err
		}
		totalUsage = totalUsage.Add(usage)
		if obs != "" {
			allObs = append(allObs, obs)
		}
	}
	return strings.Join(allObs, "\n\n"), totalUsage, nil
}

func learn(ctx context.Context, client LLM, observations []string) (map[string]string, llm.Usage, error) {
	if len(observations) == 0 {
		return nil, llm.Usage{}, nil
	}
	input := strings.Join(observations, "\n\n---\n\n")
	raw, usage, err := client.Converse(ctx, learnPrompt, input)
	if err != nil {
		return nil, usage, err
	}
	skills, err := ParseSkillsResponse(raw)
	return skills, usage, err
}

// maxChunkChars caps each conversation chunk to ~50k tokens of input,
// leaving headroom for the system prompt and output.
const maxChunkChars = 200_000

// formatSession splits a session into chunks that fit within the token budget.
// Each chunk breaks at message boundaries.
func formatSession(session *source.Session) []string {
	var chunks []string
	var b strings.Builder
	for _, msg := range session.Messages {
		if msg.Content == "" {
			continue
		}
		line := fmt.Sprintf("[%s]: %s\n\n", msg.Role, msg.Content)
		if b.Len()+len(line) > maxChunkChars && b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
		}
		b.WriteString(line)
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

// ParseSkillsResponse splits the LLM's reduce output into individual skill files.
// Expected format: multiple blocks delimited by "=== SKILL: skill-name ===" headers,
// where each block contains the complete SKILL.md content (frontmatter + body).
func ParseSkillsResponse(raw string) (map[string]string, error) {
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
