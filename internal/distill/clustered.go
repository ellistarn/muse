package distill

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/embedding"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// minClusterSize is the minimum number of observations sharing a label to form
// a cluster. Labels with fewer observations flow through as noise.
const minClusterSize = 3

// ClusteredOptions configures a clustered distill run.
type ClusteredOptions struct {
	Reobserve   bool
	Reclassify  bool
	Limit       int
	Sources     []string
	ArtifactDir string // root for artifact storage (e.g. ~/.muse)
	Verbose     bool   // per-item progress logging

	// Upload results from the load phase, folded into the discover line.
	Uploaded    int // new conversations ingested this run
	UploadBytes int // total bytes of new conversations
}

// RunClustered executes the full clustering distillation pipeline:
// observe → classify → group → sample → synthesize → merge → diff.
//
// The embedder is wired but not currently used — label-match grouping replaced
// HDBSCAN vector clustering. Kept in the signature so we can swap strategies
// without replumbing callers.
func RunClustered(
	ctx context.Context,
	store storage.Store,
	observeLLM, classifyLLM, synthesizeLLM, mergeLLM LLM,
	embedder embedding.Embedder,
	opts ClusteredOptions,
) (*Result, error) {
	artifacts := NewArtifactStore(opts.ArtifactDir)
	pipelineStart := time.Now()
	var totalUsage inference.Usage
	var stages []StageStats

	// ── OBSERVE ─────────────────────────────────────────────────────────
	var observeCounter atomic.Int32
	observeStart := time.Now()
	observeResult, err := runObserve(ctx, store, artifacts, observeLLM, opts, &observeCounter)
	if err != nil {
		return nil, fmt.Errorf("observe: %w", err)
	}
	totalUsage = totalUsage.Add(observeResult.usage)
	stages = append(stages, StageStats{
		Name:     "observe",
		Model:    observeLLM.Model(),
		Duration: time.Since(observeStart),
		Usage:    observeResult.usage,
		DataSize: observeResult.dataSize,
	})

	// Load all observations
	allObs, err := loadAllStructuredObservations(artifacts)
	if err != nil {
		return nil, fmt.Errorf("load observations: %w", err)
	}

	// discover + observe log lines (printed after observe completes since
	// discovery happens inside runObserve before the LLM work)

	// observe result line
	observeNote := ""
	if observeResult.remaining > 0 {
		observeNote = fmt.Sprintf(" [%d remaining]", observeResult.remaining)
	}
	logAfter("%d observations%s",
		len(allObs), observeNote,
	).cost(time.Since(observeStart), observeResult.usage).print()

	if len(allObs) == 0 {
		return &Result{
			Processed: observeResult.processed,
			Pruned:    observeResult.pruned,
			Remaining: observeResult.remaining,
			Stages:    stages,
		}, nil
	}

	obsDataSize := observationBytes(allObs)

	// ── CLASSIFY ────────────────────────────────────────────────────────
	var classifyCounter atomic.Int32
	classifyStart := time.Now()
	// Count sessions for progress total
	classifySessions := map[string]bool{}
	for _, obs := range allObs {
		classifySessions[obs.Source+"/"+obs.SessionID] = true
	}
	classifyTotal := len(classifySessions)
	classifyBeforeNote := ""
	if classifyTotal > 0 {
		classifyBeforeNote = fmt.Sprintf(" (%d sessions)", classifyTotal)
	}
	logBefore("label", "%d observations%s", len(allObs), classifyBeforeNote)
	prog := startProgress(classifyTotal, &classifyCounter)
	classifyUsage, classifyCache, numLabels, err := runClassify(ctx, artifacts, classifyLLM, allObs, opts.Reclassify, opts.Verbose, &classifyCounter)
	prog.stop()
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}
	totalUsage = totalUsage.Add(classifyUsage)
	stages = append(stages, StageStats{
		Name:     "classify",
		Model:    classifyLLM.Model(),
		Duration: time.Since(classifyStart),
		Usage:    classifyUsage,
		DataSize: obsDataSize,
	})
	classifyNote := ""
	if classifyCache.Hit > 0 {
		classifyNote = fmt.Sprintf(" [%d sessions cached]", classifyCache.Hit)
	}
	logAfter("%d labels%s", numLabels, classifyNote).cost(time.Since(classifyStart), classifyUsage).print()

	// ── GROUP ───────────────────────────────────────────────────────────
	groupStart := time.Now()
	clusters, noiseObs, err := runGroup(artifacts, allObs)
	if err != nil {
		return nil, fmt.Errorf("group: %w", err)
	}
	stages = append(stages, StageStats{
		Name:     "cluster",
		Model:    "label-match",
		Duration: time.Since(groupStart),
		DataSize: obsDataSize,
	})
	logStage("cluster", "%d observations → %d clusters + %d outliers",
		len(allObs), len(clusters), len(noiseObs)).print()

	// ── SAMPLE ──────────────────────────────────────────────────────────
	sampleStart := time.Now()
	samples := runSampleWithObs(clusters, allObs, artifacts)
	totalSampled := 0
	sampleDataSize := 0
	for _, s := range samples {
		totalSampled += len(s.Observations)
		for _, obs := range s.Observations {
			sampleDataSize += len(obs)
		}
	}
	totalClusterObs := 0
	for _, cl := range clusters {
		totalClusterObs += len(cl.ObservationIdxs)
	}
	stages = append(stages, StageStats{
		Name:     "sample",
		Model:    "deterministic",
		Duration: time.Since(sampleStart),
		DataSize: sampleDataSize,
	})
	logStage("sample", "%d clusters → %d observations sampled",
		len(samples), totalSampled).print()

	// ── SYNTHESIZE ──────────────────────────────────────────────────────
	var synthCounter atomic.Int32
	synthStart := time.Now()
	logBefore("summarize", "%d clusters", len(samples))
	prog = startProgress(len(samples), &synthCounter)
	summaries, synthUsage, err := runSynthesize(ctx, synthesizeLLM, samples, &synthCounter)
	prog.stop()
	if err != nil {
		return nil, fmt.Errorf("synthesize: %w", err)
	}
	totalUsage = totalUsage.Add(synthUsage)
	synthDataSize := 0
	for _, s := range summaries {
		synthDataSize += len(s)
	}
	stages = append(stages, StageStats{
		Name:     "summarize",
		Model:    synthesizeLLM.Model(),
		Duration: time.Since(synthStart),
		Usage:    synthUsage,
		DataSize: sampleDataSize,
	})
	logAfter("%d summaries", len(summaries)).cost(time.Since(synthStart), synthUsage).print()

	// ── MERGE ───────────────────────────────────────────────────────────
	mergeStart := time.Now()
	mergeInput := fmt.Sprintf("%d summaries", len(summaries))
	if len(noiseObs) > 0 {
		mergeInput += fmt.Sprintf(" + %d outliers", len(noiseObs))
	}
	logBefore("merge", "%s", mergeInput)
	previousMuse, _ := store.GetMuse(ctx)
	muse, timestamp, mergeUsage, err := runMerge(ctx, mergeLLM, store, summaries, noiseObs)
	if err != nil {
		return nil, fmt.Errorf("merge: %w", err)
	}
	totalUsage = totalUsage.Add(mergeUsage)
	mergeDataSize := synthDataSize
	for _, obs := range noiseObs {
		mergeDataSize += len(obs)
	}
	stages = append(stages, StageStats{
		Name:     "merge",
		Model:    mergeLLM.Model(),
		Duration: time.Since(mergeStart),
		Usage:    mergeUsage,
		DataSize: mergeDataSize,
	})
	logAfter("muse.md").cost(time.Since(mergeStart), mergeUsage).print()

	// ── DIFF ────────────────────────────────────────────────────────────
	diffStart := time.Now()
	d, diffUsage, derr := computeDiff(ctx, classifyLLM, store, timestamp, previousMuse, muse)
	if derr != nil {
		return nil, fmt.Errorf("diff: %w", derr)
	}
	totalUsage = totalUsage.Add(diffUsage)
	stages = append(stages, StageStats{
		Name:     "diff",
		Model:    classifyLLM.Model(),
		Duration: time.Since(diffStart),
		Usage:    diffUsage,
		DataSize: len(previousMuse) + len(muse),
	})

	// ── DONE ────────────────────────────────────────────────────────────
	logStage("done", "%d patterns → muse.md", len(clusters)).
		cost(time.Since(pipelineStart), totalUsage).print()

	processed := observeResult.processed
	return &Result{
		Processed:    processed,
		Pruned:       observeResult.pruned,
		Remaining:    observeResult.remaining,
		Observations: len(allObs),
		Clusters:     len(clusters),
		Noise:        len(noiseObs),
		Cache: CacheStats{
			Observe:  HitMiss{Hit: observeResult.pruned, Miss: observeResult.processed},
			Classify: classifyCache,
		},
		Stages: stages,
		Usage:  totalUsage,
		Muse:   muse,
		Diff:   d,
	}, nil
}

// observationBytes returns the total byte size of all observation texts.
func observationBytes(obs []observationEntry) int {
	n := 0
	for _, o := range obs {
		n += len(o.Text)
	}
	return n
}

// observeResult holds intermediate observe stage results.
type observeResult struct {
	discovered   int            // total conversations found
	sources      []string       // unique source names
	sourceCounts map[string]int // conversations per source
	processed    int
	pending      int // total items that needed processing (for progress)
	pruned       int
	remaining    int
	usage        inference.Usage
	dataSize     int // bytes of conversation data processed
}

// runObserve discovers and observes conversations, producing structured
// observations ([]string items) stored as JSON artifacts.
// The counter is incremented atomically as each conversation completes,
// allowing the caller to render a progress bar.
func runObserve(
	ctx context.Context,
	store storage.Store,
	artifacts *ArtifactStore,
	llm LLM,
	opts ClusteredOptions,
	counter *atomic.Int32,
) (*observeResult, error) {
	entries, err := store.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	// Handle reobserve
	if opts.Reobserve {
		if len(opts.Sources) > 0 {
			for _, src := range opts.Sources {
				artifacts.DeleteObservationsForSource(src)
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "  Cleared observations for %s\n", src)
				}
			}
		} else {
			artifacts.DeleteObservations()
			fmt.Fprintln(os.Stderr, "  Cleared all observations")
		}
	}

	// Filter by sources
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

	// Count per-source conversations
	sourceCounts := map[string]int{}
	for _, e := range entries {
		sourceCounts[e.Source]++
	}
	var sources []string
	for s := range sourceCounts {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	discovered := len(entries)

	// Compute prompt chain hash for fingerprinting
	promptHash := Fingerprint(prompts.Extract, prompts.Refine)

	// Determine which conversations need (re)observation
	var pending []storage.SessionEntry
	var pruned int
	for _, e := range entries {
		session, err := store.GetSession(ctx, e.Source, e.SessionID)
		if err != nil {
			return nil, fmt.Errorf("get session %s/%s: %w", e.Source, e.SessionID, err)
		}
		fp := Fingerprint(session.UpdatedAt.Format(time.RFC3339Nano), promptHash)

		existing, err := artifacts.GetObservations(e.Source, e.SessionID)
		if err == nil && existing.Fingerprint == fp {
			pruned++
			continue
		}
		pending = append(pending, e)
	}

	// Sort newest first, apply limit
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].LastModified.After(pending[j].LastModified)
	})
	totalPending := len(pending)
	if opts.Limit > 0 && len(pending) > opts.Limit {
		pending = pending[:opts.Limit]
	}

	var mu sync.Mutex
	var firstErr error
	var usage inference.Usage
	var dataSize int

	// Print discover line now that we know totals
	discoverLine := logStage("discover", "%d sources → %d conversations %s",
		len(sources), discovered,
		formatSourceBreakdown(sourceCounts))
	if opts.Uploaded > 0 {
		noun := "conversations"
		if opts.Uploaded == 1 {
			noun = "conversation"
		}
		discoverLine.tail = fmt.Sprintf("(%d new %s, %s)", opts.Uploaded, noun, formatBytes(opts.UploadBytes))
	}
	discoverLine.print()

	if len(pending) > 0 {
		// Print observe before-line: pending count + cached note
		cacheNote := ""
		if pruned > 0 {
			cacheNote = fmt.Sprintf(" (%d cached)", pruned)
		}
		noun := "conversations"
		if len(pending) == 1 {
			noun = "conversation"
		}
		logBefore("observe", "%d %s%s", len(pending), noun, cacheNote)
		prog := startProgress(len(pending), counter)

		var wg sync.WaitGroup
		sem := make(chan struct{}, 50)

		for _, entry := range pending {
			wg.Add(1)
			go func(entry storage.SessionEntry) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				session, err := store.GetSession(ctx, entry.Source, entry.SessionID)
				if err != nil {
					counter.Add(1)
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("load session %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}

				sessionBytes := sessionDataSize(session)

				start := time.Now()
				items, u, err := extractObservations(ctx, llm, session, opts.Verbose)
				n := counter.Add(1)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("observe %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}

				fp := Fingerprint(session.UpdatedAt.Format(time.RFC3339Nano), promptHash)
				obs := &Observations{
					Fingerprint: fp,
					Items:       items,
				}
				if err := artifacts.PutObservations(entry.Source, entry.SessionID, obs); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("save observations for %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}

				// Also persist as legacy observation for map-reduce compatibility
				if len(items) > 0 {
					legacy := strings.Join(items, "\n\n")
					if err := store.PutObservation(ctx, entry.Key, legacy); err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("save legacy observation for %s: %w", entry.Key, err)
						}
						mu.Unlock()
						return
					}
				}

				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "  [%d/%d] Observed %s (%d obs, %s, $%.4f)\n",
						n, len(pending), entry.Key, len(items),
						time.Since(start).Round(time.Millisecond), u.Cost())
				}
				mu.Lock()
				usage = usage.Add(u)
				dataSize += sessionBytes
				mu.Unlock()
			}(entry)
		}
		wg.Wait()
		prog.stop()
		if firstErr != nil {
			return nil, firstErr
		}
	}

	return &observeResult{
		discovered:   discovered,
		sources:      sources,
		sourceCounts: sourceCounts,
		processed:    len(pending),
		pending:      len(pending),
		pruned:       pruned,
		remaining:    totalPending - len(pending),
		usage:        usage,
		dataSize:     dataSize,
	}, nil
}

// sessionDataSize returns the total byte size of all message content in a session.
func sessionDataSize(session *conversation.Session) int {
	n := 0
	for _, msg := range session.Messages {
		n += len(msg.Content)
	}
	return n
}

// extractObservations runs the observe pipeline on a session and returns
// discrete observation strings (not a markdown blob).
//
// Raw conversation is sent to the extract prompt by default. When the raw text
// exceeds the context window, mechanical compression is applied as a fallback:
// code blocks are stripped, tool output is collapsed to [tool: name] markers,
// and long assistant messages are truncated.
func extractObservations(ctx context.Context, client LLM, session *conversation.Session, verbose bool) ([]string, inference.Usage, error) {
	turns := extractTurns(session)
	if len(turns) == 0 {
		return nil, inference.Usage{}, nil
	}

	// Mechanically compress the conversation — no LLM calls. Strips code blocks,
	// collapses tool output to [tool: name] markers, truncates long assistant messages.
	// Human messages are preserved in full since they carry the signal.
	chunks := compressConversation(turns)
	if len(chunks) == 0 {
		return nil, inference.Usage{}, nil
	}

	var totalUsage inference.Usage

	// Extract candidates
	var allCandidates []string
	for _, chunk := range chunks {
		obs, usage, err := client.Converse(ctx, prompts.Extract, chunk, inference.WithMaxTokens(4096))
		totalUsage = totalUsage.Add(usage)
		if err != nil && obs == "" {
			return nil, totalUsage, err
		}
		if obs != "" && !isEmpty(obs) {
			allCandidates = append(allCandidates, obs)
		}
	}
	if len(allCandidates) == 0 {
		return nil, totalUsage, nil
	}

	// Refine
	candidates := strings.Join(allCandidates, "\n\n")
	refined, usage, err := client.Converse(ctx, prompts.Refine, candidates, inference.WithMaxTokens(4096))
	totalUsage = totalUsage.Add(usage)
	if err != nil && refined == "" {
		return nil, totalUsage, err
	}
	if isEmpty(refined) {
		return nil, totalUsage, nil
	}

	// Parse refined output into discrete items.
	items := parseObservationItems(refined)

	// Filter irrelevant items: empty responses, LLM meta-commentary, placeholder tokens
	var relevant []string
	var irrelevant []string
	for _, item := range items {
		if isRelevant(item) {
			relevant = append(relevant, item)
		} else {
			irrelevant = append(irrelevant, item)
		}
	}
	if len(irrelevant) > 0 && verbose {
		fmt.Fprintf(os.Stderr, "    filtered %d irrelevant observations:\n", len(irrelevant))
		for _, s := range irrelevant {
			text := s
			if len(text) > 100 {
				text = text[:100] + "..."
			}
			fmt.Fprintf(os.Stderr, "      - %s\n", text)
		}
	}
	return relevant, totalUsage, nil
}

// maxAssistantChars caps truncated assistant messages. Long assistant output is
// mostly code or tool results — the human's reaction is what matters, not the
// full assistant text.
const maxAssistantChars = 500

// buildRawConversation formats turns into chunked text without any compression.
// This preserves the full conversation for extraction when it fits in the context window.
func buildRawConversation(turns []turn) []string {
	var chunks []string
	var b strings.Builder

	for _, t := range turns {
		var entry string
		if t.assistantContent != "" {
			entry = fmt.Sprintf("[assistant]: %s\n[human]: %s\n\n", t.assistantContent, t.humanContent)
		} else {
			entry = fmt.Sprintf("[human]: %s\n\n", t.humanContent)
		}

		if b.Len()+len(entry) > maxChunkChars && b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
		}
		b.WriteString(entry)
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

// totalChars returns the total character count across all chunks.
func totalChars(chunks []string) int {
	n := 0
	for _, c := range chunks {
		n += len(c)
	}
	return n
}

// compressConversation mechanically compresses turns for extraction: strips
// code blocks, collapses tool output to [tool: name] markers, and truncates
// long assistant messages. Returns chunked text ready for the extract prompt.
func compressConversation(turns []turn) []string {
	var chunks []string
	var b strings.Builder

	for _, t := range turns {
		var entry string
		if t.assistantContent != "" {
			compressed := compressAssistant(t.assistantContent)
			entry = fmt.Sprintf("[assistant]: %s\n[human]: %s\n\n", compressed, t.humanContent)
		} else {
			entry = fmt.Sprintf("[human]: %s\n\n", t.humanContent)
		}

		if b.Len()+len(entry) > maxChunkChars && b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
		}
		b.WriteString(entry)
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

// compressAssistant strips code blocks, collapses tool markers, and truncates.
func compressAssistant(content string) string {
	// Strip fenced code blocks (```...```)
	result := stripCodeBlocks(content)

	// Truncate if still too long
	result = strings.TrimSpace(result)
	if len(result) > maxAssistantChars {
		result = result[:maxAssistantChars] + "..."
	}
	return result
}

// stripCodeBlocks removes fenced code blocks (``` ... ```) from text,
// replacing each with a short marker.
func stripCodeBlocks(s string) string {
	var b strings.Builder
	lines := strings.Split(s, "\n")
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inBlock {
				inBlock = true
				b.WriteString("[code block]\n")
			} else {
				inBlock = false
			}
			continue
		}
		if !inBlock {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// minObservationLen is the minimum character length for an observation to be
// considered substantive. A genuine observation about how someone thinks can't
// be expressed in fewer than a few words.
const minObservationLen = 20

// irrelevantPrefixes are LLM meta-commentary patterns that indicate the model
// had nothing useful to extract. These come from known responses to the
// observe-extract and observe-refine prompts.
var irrelevantPrefixes = []string{
	"i don't see",
	"i couldn't find",
	"i do not see",
	"i did not find",
	"no observations",
	"no candidate observations",
	"no distinctive",
	"there are no",
	"there were no",
	"none of the",
	"nothing distinctive",
	"nothing notable",
	"the conversation",
	"this conversation",
	"after review",
	"after filtering",
	"understood",
	"empty response",
	"ready to",
	"please share",
	"could you share",
	"i notice",
	"it looks like",
	"i'm ready",
	"i need",
	"[some ",    // placeholder bracket tokens from LLM
	"[another ", // placeholder bracket tokens from LLM
	"[an extracted",
}

// irrelevantExact are placeholder tokens that indicate empty/null output.
var irrelevantExact = []string{
	"(empty)",
	"(empty response)",
	"(none)",
	"n/a",
	"none",
	"empty",
}

// isRelevant returns true if an observation is a genuine statement about the
// person's thinking rather than empty output, a placeholder token, or LLM
// meta-commentary about failing to find observations. This is a second line
// of defense — the primary filter is structural (Observation: prefix parsing).
func isRelevant(s string) bool {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) == 0 {
		return false
	}
	if len(trimmed) < minObservationLen {
		return false
	}
	// Strip parentheses — LLMs often wrap meta-commentary in parens
	lower := strings.ToLower(trimmed)
	lower = strings.TrimLeft(lower, "(")
	lower = strings.TrimSpace(lower)
	for _, exact := range irrelevantExact {
		if lower == exact {
			return false
		}
	}
	for _, prefix := range irrelevantPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	// Reject bracket-wrapped placeholder tokens like "[some observation]"
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return false
	}
	return true
}

// observationPrefix is the required prefix for structured observation output.
const observationPrefix = "Observation: "

// parseObservationItems extracts discrete observations from LLM output.
// Lines starting with "Observation: " are extracted and the prefix stripped.
// All other lines (meta-commentary, blank lines, preamble) are discarded.
// This structural parsing replaces heuristic pattern-matching for garbage filtering.
func parseObservationItems(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var items []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		// Strip optional bullet/number prefix before checking for Observation:
		// Handles "- Observation: ...", "1. Observation: ...", "• Observation: ..."
		cleaned := line
		cleaned = strings.TrimLeft(cleaned, "- •*0123456789.")
		cleaned = strings.TrimSpace(cleaned)

		if strings.HasPrefix(cleaned, observationPrefix) {
			obs := strings.TrimSpace(cleaned[len(observationPrefix):])
			if obs != "" {
				items = append(items, obs)
			}
		}
	}
	return items
}

// observationEntry flattens source/session/index into a single record
// so downstream stages can track observations across conversations.
type observationEntry struct {
	Source    string
	SessionID string
	Index     int
	Text      string
}

// loadAllStructuredObservations loads all observation artifacts and returns
// a flat list of observation entries.
func loadAllStructuredObservations(artifacts *ArtifactStore) ([]observationEntry, error) {
	sessions, err := artifacts.ListObservations()
	if err != nil {
		return nil, err
	}
	var all []observationEntry
	for _, ss := range sessions {
		obs, err := artifacts.GetObservations(ss.Source, ss.SessionID)
		if err != nil {
			return nil, fmt.Errorf("get observations %s/%s: %w", ss.Source, ss.SessionID, err)
		}
		for i, item := range obs.Items {
			all = append(all, observationEntry{
				Source:    ss.Source,
				SessionID: ss.SessionID,
				Index:     i,
				Text:      item,
			})
		}
	}
	return all, nil
}

// ── CLASSIFY ────────────────────────────────────────────────────────────

// labelSet tracks assigned classification labels across parallel goroutines.
// It provides the existing label vocabulary to each classify call so the LLM
// converges on reusing labels instead of paraphrasing.
type labelSet struct {
	mu     sync.Mutex
	labels map[string]bool
}

func newLabelSet() *labelSet {
	return &labelSet{labels: map[string]bool{}}
}

func (ls *labelSet) add(label string) {
	ls.mu.Lock()
	ls.labels[label] = true
	ls.mu.Unlock()
}

func (ls *labelSet) addAll(labels []string) {
	ls.mu.Lock()
	for _, l := range labels {
		ls.labels[l] = true
	}
	ls.mu.Unlock()
}

func (ls *labelSet) list() []string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	result := make([]string, 0, len(ls.labels))
	for l := range ls.labels {
		result = append(result, l)
	}
	sort.Strings(result)
	return result
}

// buildClassifyPrompt constructs the classify system prompt, injecting existing
// labels when available so the LLM converges on a shared vocabulary.
func buildClassifyPrompt(existingLabels []string) string {
	if len(existingLabels) == 0 {
		return prompts.Classify
	}
	var b strings.Builder
	b.WriteString(prompts.Classify)
	b.WriteString("\n\nExisting labels already assigned to other observations (reuse one if it fits; only create a new label when none of these match):\n")
	for _, l := range existingLabels {
		b.WriteString("- ")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

// classifyBatchSize is how many observations to send per LLM call.
const classifyBatchSize = 10

// buildClassifyInput formats a numbered list of observations for batch classify.
func buildClassifyInput(observations []string) string {
	var b strings.Builder
	for i, obs := range observations {
		fmt.Fprintf(&b, "%d. %s\n", i+1, obs)
	}
	return b.String()
}

// parseClassifyResponse parses numbered labels from the LLM response.
// Returns labels aligned to input indices. Missing/unparseable lines get "".
func parseClassifyResponse(resp string, count int) []string {
	labels := make([]string, count)
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Parse "N. label" or "N: label"
		var idx int
		var label string
		if n, _ := fmt.Sscanf(line, "%d.", &idx); n == 1 {
			// Find the label after "N. "
			dot := strings.Index(line, ".")
			if dot >= 0 && dot+1 < len(line) {
				label = strings.TrimSpace(line[dot+1:])
			}
		}
		if idx >= 1 && idx <= count && label != "" {
			labels[idx-1] = label
		}
	}
	return labels
}

// runClassify classifies observations using batched LLM calls.
// Sessions are processed sequentially so each batch sees all prior labels,
// ensuring convergence. Within each session, observations are sent in batches
// of classifyBatchSize.
func runClassify(
	ctx context.Context,
	artifacts *ArtifactStore,
	llm LLM,
	allObs []observationEntry,
	forceReclassify bool,
	verbose bool,
	counter *atomic.Int32,
) (inference.Usage, HitMiss, int, error) {
	if forceReclassify {
		artifacts.DeleteClassifications()
	}

	classifyPromptHash := Fingerprint(prompts.Classify)

	// Seed the label set from cached classifications
	labels := newLabelSet()
	existingCls, err := artifacts.ListClassifications()
	if err != nil {
		return inference.Usage{}, HitMiss{}, 0, fmt.Errorf("list classifications: %w", err)
	}
	for _, ss := range existingCls {
		cls, err := artifacts.GetClassifications(ss.Source, ss.SessionID)
		if err != nil {
			return inference.Usage{}, HitMiss{}, 0, fmt.Errorf("get classifications %s/%s: %w", ss.Source, ss.SessionID, err)
		}
		for _, item := range cls.Items {
			if item.Classification != "" {
				labels.add(item.Classification)
			}
		}
	}

	// Group observations by (source, sessionID)
	type sessionKey struct{ source, sessionID string }
	groups := map[sessionKey][]observationEntry{}
	for _, obs := range allObs {
		key := sessionKey{obs.Source, obs.SessionID}
		groups[key] = append(groups[key], obs)
	}

	var totalUsage inference.Usage
	var hits, misses atomic.Int32
	total := len(groups)

	// Process sessions sequentially for label convergence.
	for key, entries := range groups {
		// Check cache
		var obsTexts []string
		for _, e := range entries {
			obsTexts = append(obsTexts, e.Text)
		}
		fp := Fingerprint(append(obsTexts, classifyPromptHash)...)

		existing, err := artifacts.GetClassifications(key.source, key.sessionID)
		if err == nil && existing.Fingerprint == fp {
			hits.Add(1)
			for _, item := range existing.Items {
				if item.Classification != "" {
					labels.add(item.Classification)
				}
			}
			n := counter.Add(1)
			if verbose {
				fmt.Fprintf(os.Stderr, "  [%d/%d] Cached classifications for %s/%s\n", n, total, key.source, key.sessionID)
			}
			continue
		}
		misses.Add(1)

		// Batch classify: send up to classifyBatchSize observations per call
		var items []Classification
		var usage inference.Usage
		for i := 0; i < len(entries); i += classifyBatchSize {
			end := i + classifyBatchSize
			if end > len(entries) {
				end = len(entries)
			}
			batch := entries[i:end]

			var batchTexts []string
			for _, e := range batch {
				batchTexts = append(batchTexts, e.Text)
			}

			prompt := buildClassifyPrompt(labels.list())
			input := buildClassifyInput(batchTexts)
			resp, u, err := llm.Converse(ctx, prompt, input, inference.WithMaxTokens(1024))
			usage = usage.Add(u)
			if err != nil {
				return inference.Usage{}, HitMiss{}, 0, fmt.Errorf("classify batch for %s/%s: %w", key.source, key.sessionID, err)
			}

			batchLabels := parseClassifyResponse(resp, len(batch))
			for j, e := range batch {
				cls := batchLabels[j]
				if cls == "" {
					cls = "uncategorized"
				}
				items = append(items, Classification{
					Observation:    e.Text,
					Classification: cls,
				})
				labels.add(cls)
			}
		}

		clsResult := &Classifications{
			Fingerprint: fp,
			Items:       items,
		}
		if err := artifacts.PutClassifications(key.source, key.sessionID, clsResult); err != nil {
			return inference.Usage{}, HitMiss{}, 0, fmt.Errorf("save classifications for %s/%s: %w", key.source, key.sessionID, err)
		}

		n := counter.Add(1)
		if verbose {
			fmt.Fprintf(os.Stderr, "  [%d/%d] Classified %s/%s (%d items, $%.4f)\n",
				n, total, key.source, key.sessionID, len(items), usage.Cost())
		}

		totalUsage = totalUsage.Add(usage)
	}

	return totalUsage, HitMiss{Hit: int(hits.Load()), Miss: int(misses.Load())}, len(labels.list()), nil
}

// ── EMBED ───────────────────────────────────────────────────────────────

// runEmbed computes embeddings for all classifications.
func runEmbed(
	ctx context.Context,
	artifacts *ArtifactStore,
	embedder embedding.Embedder,
	allObs []observationEntry,
	verbose bool,
) (HitMiss, error) {
	model := embedder.Model()
	start := time.Now()

	// Group by session
	type sessionKey struct{ source, sessionID string }
	groups := map[sessionKey]bool{}
	for _, obs := range allObs {
		groups[sessionKey{obs.Source, obs.SessionID}] = true
	}

	var hits, misses atomic.Int32
	var completed atomic.Int32
	total := len(groups)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 50)

	for key := range groups {
		wg.Add(1)
		go func(key sessionKey) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cls, err := artifacts.GetClassifications(key.source, key.sessionID)
			if err != nil || len(cls.Items) == 0 {
				completed.Add(1)
				return
			}

			// Fingerprint: hash of classification content + model
			var clsTexts []string
			for _, c := range cls.Items {
				clsTexts = append(clsTexts, c.Classification)
			}
			fp := Fingerprint(append(clsTexts, model)...)

			existing, err := artifacts.GetEmbeddings(key.source, key.sessionID)
			if err == nil && existing.Fingerprint == fp {
				hits.Add(1)
				n := completed.Add(1)
				if verbose {
					fmt.Fprintf(os.Stderr, "  [%d/%d] Cached embeddings for %s/%s\n", n, total, key.source, key.sessionID)
				}
				return
			}
			misses.Add(1)

			// Compute embeddings
			vectors, err := embedder.Embed(ctx, clsTexts)
			if err != nil {
				n := completed.Add(1)
				if verbose {
					fmt.Fprintf(os.Stderr, "  [%d/%d] Error embedding %s/%s: %v\n", n, total, key.source, key.sessionID, err)
				}
				return
			}

			var items []Embedding
			for i, c := range cls.Items {
				items = append(items, Embedding{
					Classification: c.Classification,
					Vector:         vectors[i],
				})
			}

			emb := &Embeddings{
				Fingerprint: fp,
				Items:       items,
			}
			artifacts.PutEmbeddings(key.source, key.sessionID, emb)

			n := completed.Add(1)
			if verbose {
				fmt.Fprintf(os.Stderr, "  [%d/%d] Embedded %s/%s (%d vectors)\n",
					n, total, key.source, key.sessionID, len(items))
			}
		}(key)
	}
	wg.Wait()

	fmt.Fprintf(os.Stderr, "Embedded %d sessions (%s)\n",
		total, time.Since(start).Round(time.Millisecond))
	return HitMiss{Hit: int(hits.Load()), Miss: int(misses.Load())}, nil
}

// ── GROUP ───────────────────────────────────────────────────────────────

type clusterResult struct {
	ID              int
	ObservationIdxs []int // indices into the flat allObs slice
}

// runGroup collects all embedding vectors and runs HDBSCAN clustering.
// Returns clusters (groups of observation indices) and noise observations.
func runGroup(artifacts *ArtifactStore, allObs []observationEntry) ([]clusterResult, []string, error) {
	// Load classifications to get label for each observation
	type sessionKey struct{ source, sessionID string }
	clsBySession := map[sessionKey]*Classifications{}
	sessions, err := artifacts.ListClassifications()
	if err != nil {
		return nil, nil, err
	}
	for _, ss := range sessions {
		cls, err := artifacts.GetClassifications(ss.Source, ss.SessionID)
		if err != nil {
			return nil, nil, fmt.Errorf("get classifications %s/%s: %w", ss.Source, ss.SessionID, err)
		}
		clsBySession[sessionKey{ss.Source, ss.SessionID}] = cls
	}

	// Build observation → label mapping
	obsLabels := make([]string, len(allObs))
	for i, obs := range allObs {
		key := sessionKey{obs.Source, obs.SessionID}
		cls, ok := clsBySession[key]
		if ok && obs.Index < len(cls.Items) {
			obsLabels[i] = strings.ToLower(strings.TrimSpace(cls.Items[obs.Index].Classification))
			// Strip surrounding quotes from labels
			obsLabels[i] = strings.Trim(obsLabels[i], "\"'")
		}
	}

	// Group by exact label match: labels with 2+ observations form clusters
	labelMembers := map[string][]int{} // label → list of allObs indices
	for i, label := range obsLabels {
		if label != "" {
			labelMembers[label] = append(labelMembers[label], i)
		}
	}

	var clusters []clusterResult
	clusteredObs := map[int]bool{}
	clusterID := 0
	for _, members := range labelMembers {
		if len(members) >= minClusterSize {
			clusters = append(clusters, clusterResult{
				ID:              clusterID,
				ObservationIdxs: members,
			})
			clusterID++
			for _, idx := range members {
				clusteredObs[idx] = true
			}
		}
	}

	// Everything not clustered is noise
	var noiseObs []string
	for i, obs := range allObs {
		if !clusteredObs[i] {
			noiseObs = append(noiseObs, obs.Text)
		}
	}

	// Sort clusters by ID for determinism
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].ID < clusters[j].ID
	})

	return clusters, noiseObs, nil
}

// ── SAMPLE ──────────────────────────────────────────────────────────────

const maxSampleTokens = 10_000

type clusterSample struct {
	ID           int
	Theme        string // classification theme for the cluster
	Observations []string
}

// runSampleWithObs selects representative observations from each cluster.
func runSampleWithObs(clusters []clusterResult, allObs []observationEntry, artifacts *ArtifactStore) []clusterSample {
	var samples []clusterSample
	for _, cl := range clusters {
		indices := cl.ObservationIdxs

		// Shuffle for random selection
		shuffled := make([]int, len(indices))
		copy(shuffled, indices)
		rand.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})

		var selected []string
		tokens := 0
		for _, idx := range shuffled {
			obs := allObs[idx]
			t := inference.EstimateTokens(obs.Text)
			if tokens+t > maxSampleTokens && len(selected) > 0 {
				break
			}
			selected = append(selected, obs.Text)
			tokens += t
		}

		// Determine cluster theme from classifications
		theme := ""
		if len(indices) > 0 {
			obs := allObs[indices[0]]
			cls, err := artifacts.GetClassifications(obs.Source, obs.SessionID)
			if err == nil && obs.Index < len(cls.Items) {
				theme = cls.Items[obs.Index].Classification
			}
		}

		samples = append(samples, clusterSample{
			ID:           cl.ID,
			Theme:        theme,
			Observations: selected,
		})
	}
	return samples
}

// ── SYNTHESIZE ──────────────────────────────────────────────────────────

// runSynthesize runs parallel per-cluster synthesis.
func runSynthesize(
	ctx context.Context,
	llm LLM,
	samples []clusterSample,
	counter *atomic.Int32,
) ([]string, inference.Usage, error) {
	summaries := make([]string, len(samples))
	errs := make([]error, len(samples))
	usages := make([]inference.Usage, len(samples))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 50)

	for i, sample := range samples {
		wg.Add(1)
		go func(i int, sample clusterSample) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			input := fmt.Sprintf("Cluster theme: %s\n\nObservations:\n", sample.Theme)
			for _, obs := range sample.Observations {
				input += "\n---\n" + obs
			}

			resp, usage, err := llm.Converse(ctx, prompts.Summarize, input, inference.WithMaxTokens(4096))
			summaries[i] = strings.TrimSpace(resp)
			usages[i] = usage
			errs[i] = err
			counter.Add(1)
		}(i, sample)
	}
	wg.Wait()

	var totalUsage inference.Usage
	for i, err := range errs {
		if err != nil {
			return nil, totalUsage, fmt.Errorf("synthesize cluster %d: %w", i, err)
		}
		totalUsage = totalUsage.Add(usages[i])
	}

	return summaries, totalUsage, nil
}

// ── MERGE ───────────────────────────────────────────────────────────────

// runMerge combines cluster summaries and noise observations into muse.md.
func runMerge(
	ctx context.Context,
	llm LLM,
	store storage.Store,
	summaries []string,
	noiseObs []string,
) (string, string, inference.Usage, error) {
	var input strings.Builder
	input.WriteString("## Cluster Summaries\n\n")
	for i, summary := range summaries {
		fmt.Fprintf(&input, "### Cluster %d\n\n%s\n\n", i+1, summary)
	}

	if len(noiseObs) > 0 {
		input.WriteString("## Unclustered Observations\n\n")
		input.WriteString("These observations didn't fit any theme. Preserve what's distinctive, ignore what's redundant with the cluster summaries.\n\n")
		// Budget noise to ~10k tokens
		tokens := 0
		for _, obs := range noiseObs {
			t := inference.EstimateTokens(obs)
			if tokens+t > maxSampleTokens {
				break
			}
			fmt.Fprintf(&input, "- %s\n\n", obs)
			tokens += t
		}
	}

	muse, usage, err := llm.Converse(ctx, prompts.Merge, input.String(), inference.WithThinking(8000))
	if err != nil {
		return "", "", usage, err
	}
	muse = stripCodeFences(muse)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	if err := store.PutMuse(ctx, timestamp, muse); err != nil {
		return "", "", usage, fmt.Errorf("failed to write muse: %w", err)
	}

	return muse, timestamp, usage, nil
}
