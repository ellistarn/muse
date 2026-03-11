package dream

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	Processed int
	Pruned    int
	Skills    int
	Warnings  []string
}

// Run executes the dream pipeline: load state, map new memories to observations,
// reduce observations into skills, and persist the results.
func Run(ctx context.Context, store *storage.Client, llm *bedrock.Client) (*Result, error) {
	// Load prior dream state (missing state means first run)
	var state State
	if err := store.GetJSON(ctx, stateKey, &state); err != nil {
		state = State{Memories: map[string]time.Time{}}
	}

	// List all memories and filter to ones we haven't processed since their last modification
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
	if len(pending) == 0 {
		return &Result{Pruned: pruned, Skills: 0}, nil
	}

	// Map: extract observations from each new/updated memory in parallel
	type mapResult struct {
		key          string
		observations string
		err          error
	}
	results := make([]mapResult, len(pending))
	var wg sync.WaitGroup
	// Limit concurrency to avoid throttling
	sem := make(chan struct{}, 5)
	for i, entry := range pending {
		wg.Add(1)
		go func(i int, entry storage.SessionEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			session, err := store.GetSession(ctx, entry.Source, entry.SessionID)
			if err != nil {
				results[i] = mapResult{key: entry.Key, err: err}
				return
			}
			obs, err := extractObservations(ctx, llm, session)
			results[i] = mapResult{key: entry.Key, observations: obs, err: err}
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

	// Reduce: compress observations into skills
	skills, err := reduceToSkills(ctx, llm, allObservations)
	if err != nil {
		return nil, fmt.Errorf("reduce failed: %w", err)
	}

	// Write skills to S3 (clear old skills first, dream produces a complete set)
	if err := store.DeletePrefix(ctx, "skills/"); err != nil {
		return nil, fmt.Errorf("failed to clear old skills: %w", err)
	}
	for name, content := range skills {
		if err := store.PutSkill(ctx, name, content); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to write skill %s: %v", name, err))
		}
	}

	// Update state with newly processed memories
	for k, v := range processedKeys {
		state.Memories[k] = v
	}
	state.LastDream = time.Now()
	if err := store.PutJSON(ctx, stateKey, &state); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to save dream state: %v", err))
	}

	return &Result{
		Processed: len(allObservations),
		Pruned:    pruned,
		Skills:    len(skills),
		Warnings:  warnings,
	}, nil
}

func extractObservations(ctx context.Context, llm *bedrock.Client, session *source.Session) (string, error) {
	conversation := formatSession(session)
	if conversation == "" {
		return "", nil
	}
	return llm.Converse(ctx, extractPrompt, conversation)
}

func reduceToSkills(ctx context.Context, llm *bedrock.Client, observations []string) (map[string]string, error) {
	if len(observations) == 0 {
		return nil, nil
	}
	input := strings.Join(observations, "\n\n---\n\n")
	raw, err := llm.Converse(ctx, reducePrompt, input)
	if err != nil {
		return nil, err
	}
	return parseSkillsResponse(raw)
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
	skills := map[string]string{}
	sections := strings.Split(raw, "=== SKILL:")
	for _, section := range sections[1:] { // skip content before first delimiter
		endHeader := strings.Index(section, "===\n")
		if endHeader == -1 {
			continue
		}
		name := strings.TrimSpace(section[:endHeader])
		content := strings.TrimSpace(section[endHeader+4:])
		if name != "" && content != "" {
			skills[name] = content
		}
	}
	if len(skills) == 0 {
		return nil, fmt.Errorf("no skills found in reduce output")
	}
	return skills, nil
}
