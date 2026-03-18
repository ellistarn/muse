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
	Relabel     bool
	Limit       int
	Sources     []string
	ArtifactDir string // root for artifact storage (e.g. ~/.muse)
	Verbose     bool   // per-item progress logging

	// Upload results from the load phase, folded into the discover line.
	Uploaded    int // new conversations ingested this run
	UploadBytes int // total bytes of new conversations
}

// RunClustered executes the full clustering distillation pipeline:
// observe → label → normalize → group → sample → summarize → compose → diff.
//
// Grouping is by exact label match — shared label vocabulary plus normalization
// produces a shared vocabulary, making embedding-based clustering unnecessary.
func RunClustered(
	ctx context.Context,
	store storage.Store,
	observeLLM, labelLLM, summarizeLLM, composeLLM LLM,
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

	// ── LABEL ──────────────────────────────────────────────────────────
	var labelCounter atomic.Int32
	labelStart := time.Now()
	// Count sessions for progress total
	labelSessions := map[string]bool{}
	for _, obs := range allObs {
		labelSessions[obs.Source+"/"+obs.SessionID] = true
	}
	labelTotal := len(labelSessions)
	labelBeforeNote := ""
	if labelTotal > 0 {
		labelBeforeNote = fmt.Sprintf(" (%d sessions)", labelTotal)
	}
	logBefore("label", "%d observations%s", len(allObs), labelBeforeNote)
	prog := startProgress(labelTotal, &labelCounter)
	labelUsage, labelCache, numLabels, err := runLabel(ctx, artifacts, labelLLM, allObs, opts.Relabel, opts.Verbose, &labelCounter)
	prog.stop()
	if err != nil {
		return nil, fmt.Errorf("label: %w", err)
	}
	totalUsage = totalUsage.Add(labelUsage)
	stages = append(stages, StageStats{
		Name:     "label",
		Model:    labelLLM.Model(),
		Duration: time.Since(labelStart),
		Usage:    labelUsage,
		DataSize: obsDataSize,
	})
	labelNote := ""
	if labelCache.Hit > 0 {
		labelNote = fmt.Sprintf(" [%d sessions cached]", labelCache.Hit)
	}
	logAfter("%d labels%s", numLabels, labelNote).cost(time.Since(labelStart), labelUsage).print()

	// ── NORMALIZE ──────────────────────────────────────────────────────
	normalizeStart := time.Now()
	logBefore("normalize", "%d labels", numLabels)
	normalizeUsage, err := runNormalize(ctx, artifacts, labelLLM, opts.Verbose)
	if err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}
	totalUsage = totalUsage.Add(normalizeUsage)
	stages = append(stages, StageStats{
		Name:     "normalize",
		Model:    labelLLM.Model(),
		Duration: time.Since(normalizeStart),
		Usage:    normalizeUsage,
	})
	logAfter("normalized").cost(time.Since(normalizeStart), normalizeUsage).print()

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

	// ── SUMMARIZE ──────────────────────────────────────────────────────
	var synthCounter atomic.Int32
	synthStart := time.Now()
	logBefore("summarize", "%d clusters", len(samples))
	prog = startProgress(len(samples), &synthCounter)
	summaries, synthUsage, err := runSummarize(ctx, summarizeLLM, samples, &synthCounter)
	prog.stop()
	if err != nil {
		return nil, fmt.Errorf("summarize: %w", err)
	}
	totalUsage = totalUsage.Add(synthUsage)
	synthDataSize := 0
	for _, s := range summaries {
		synthDataSize += len(s)
	}
	stages = append(stages, StageStats{
		Name:     "summarize",
		Model:    summarizeLLM.Model(),
		Duration: time.Since(synthStart),
		Usage:    synthUsage,
		DataSize: sampleDataSize,
	})
	logAfter("%d summaries", len(summaries)).cost(time.Since(synthStart), synthUsage).print()

	// ── COMPOSE ────────────────────────────────────────────────────────
	composeStart := time.Now()
	composeInput := fmt.Sprintf("%d summaries", len(summaries))
	if len(noiseObs) > 0 {
		composeInput += fmt.Sprintf(" + %d outliers", len(noiseObs))
	}
	logBefore("compose", "%s", composeInput)
	previousMuse, err := store.GetMuse(ctx)
	if err != nil && !storage.IsNotFound(err) {
		return nil, fmt.Errorf("get previous muse: %w", err)
	}
	muse, timestamp, composeUsage, err := runCompose(ctx, composeLLM, store, summaries, noiseObs)
	if err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	totalUsage = totalUsage.Add(composeUsage)
	composeDataSize := synthDataSize
	for _, obs := range noiseObs {
		composeDataSize += len(obs)
	}
	stages = append(stages, StageStats{
		Name:     "compose",
		Model:    composeLLM.Model(),
		Duration: time.Since(composeStart),
		Usage:    composeUsage,
		DataSize: composeDataSize,
	})
	logAfter("muse.md").cost(time.Since(composeStart), composeUsage).print()

	// ── DIFF ────────────────────────────────────────────────────────────
	diffStart := time.Now()
	d, diffUsage, derr := computeDiff(ctx, labelLLM, store, timestamp, previousMuse, muse)
	if derr != nil {
		return nil, fmt.Errorf("diff: %w", derr)
	}
	totalUsage = totalUsage.Add(diffUsage)
	stages = append(stages, StageStats{
		Name:     "diff",
		Model:    labelLLM.Model(),
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
			Observe: HitMiss{Hit: observeResult.pruned, Miss: observeResult.processed},
			Label:   labelCache,
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fp := Fingerprint(e.LastModified.Format(time.RFC3339Nano), promptHash)

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

	// errCtx is cancelled when the first error occurs, preventing remaining
	// goroutines from starting expensive LLM calls.
	errCtx, cancelOnErr := context.WithCancel(ctx)
	defer cancelOnErr()

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

				// Check if another goroutine already failed
				if errCtx.Err() != nil {
					counter.Add(1)
					return
				}

				session, err := store.GetSession(ctx, entry.Source, entry.SessionID)
				if err != nil {
					counter.Add(1)
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("load session %s: %w", entry.Key, err)
						cancelOnErr()
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
						cancelOnErr()
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
						cancelOnErr()
					}
					mu.Unlock()
					return
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

// buildRawConversation was removed — mechanical compression is always used.

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
		cleaned := stripListPrefix(line)

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
// so downstream stages can track observations across conversations. removes a leading bullet or numbered-list marker from a line.
// Handles "- ...", "• ...", "* ...", "1. ...", "12. ...", etc.
func stripListPrefix(s string) string {
	// Bullet markers: "- ", "• ", "* "
	for _, prefix := range []string{"- ", "• ", "* "} {
		if strings.HasPrefix(s, prefix) {
			return strings.TrimSpace(s[len(prefix):])
		}
	}
	// Numbered list: "N. " where N is one or more digits
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i < len(s) && s[i] == '.' {
		rest := s[i+1:]
		return strings.TrimSpace(rest)
	}
	return s
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
// a flat list of observation entries, loading sessions in parallel.
func loadAllStructuredObservations(artifacts *ArtifactStore) ([]observationEntry, error) {
	sessions, err := artifacts.ListObservations()
	if err != nil {
		return nil, err
	}

	type result struct {
		entries []observationEntry
		err     error
	}
	results := make([]result, len(sessions))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 20)
	for i, ss := range sessions {
		wg.Add(1)
		go func(i int, ss SourceSession) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			obs, err := artifacts.GetObservations(ss.Source, ss.SessionID)
			if err != nil {
				results[i] = result{err: fmt.Errorf("get observations %s/%s: %w", ss.Source, ss.SessionID, err)}
				return
			}
			var entries []observationEntry
			for j, item := range obs.Items {
				entries = append(entries, observationEntry{
					Source:    ss.Source,
					SessionID: ss.SessionID,
					Index:     j,
					Text:      item,
				})
			}
			results[i] = result{entries: entries}
		}(i, ss)
	}
	wg.Wait()

	var all []observationEntry
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		all = append(all, r.entries...)
	}
	return all, nil
}

// ── LABEL ───────────────────────────────────────────────────────────────

// labelSet tracks assigned labels across parallel goroutines.
// It provides the existing label vocabulary to each label call so the LLM
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

// buildLabelPrompt constructs the label system prompt, injecting existing
// labels when available so the LLM converges on a shared vocabulary.
func buildLabelPrompt(existingLabels []string) string {
	if len(existingLabels) == 0 {
		return prompts.Label
	}
	var b strings.Builder
	b.WriteString(prompts.Label)
	b.WriteString("\n\nExisting labels already assigned to other observations (reuse one if it fits; only create a new label when none of these match):\n")
	for _, l := range existingLabels {
		b.WriteString("- ")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

// labelBatchSize is how many observations to send per LLM call.
const labelBatchSize = 10

// buildLabelInput formats a numbered list of observations for batch labeling.
func buildLabelInput(observations []string) string {
	var b strings.Builder
	for i, obs := range observations {
		fmt.Fprintf(&b, "%d. %s\n", i+1, obs)
	}
	return b.String()
}

// parseLabelResponse parses numbered labels from the LLM response.
// Returns labels aligned to input indices. Missing/unparseable lines get "".
func parseLabelResponse(resp string, count int) []string {
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

// runLabel labels observations using batched LLM calls.
// Sessions are processed in parallel with a shared label vocabulary.
// A normalization step downstream merges synonymous labels.
func runLabel(
	ctx context.Context,
	artifacts *ArtifactStore,
	llm LLM,
	allObs []observationEntry,
	forceRelabel bool,
	verbose bool,
	counter *atomic.Int32,
) (inference.Usage, HitMiss, int, error) {
	if forceRelabel {
		artifacts.DeleteLabels()
	}

	labelPromptHash := Fingerprint(prompts.Label)

	// Seed the label set from cached labels
	labels := newLabelSet()
	existingLabels, err := artifacts.ListLabels()
	if err != nil {
		return inference.Usage{}, HitMiss{}, 0, fmt.Errorf("list labels: %w", err)
	}
	for _, ss := range existingLabels {
		lbl, err := artifacts.GetLabels(ss.Source, ss.SessionID)
		if err != nil {
			return inference.Usage{}, HitMiss{}, 0, fmt.Errorf("get labels %s/%s: %w", ss.Source, ss.SessionID, err)
		}
		for _, item := range lbl.Items {
			if item.Label != "" {
				labels.add(item.Label)
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

	var mu sync.Mutex
	var totalUsage inference.Usage
	var firstErr error
	var hits, misses atomic.Int32
	total := len(groups)

	errCtx, cancelOnErr := context.WithCancel(ctx)
	defer cancelOnErr()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 50)

	for key, entries := range groups {
		wg.Add(1)
		go func(key sessionKey, entries []observationEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if errCtx.Err() != nil {
				counter.Add(1)
				return
			}

			// Check cache
			var obsTexts []string
			for _, e := range entries {
				obsTexts = append(obsTexts, e.Text)
			}
			fp := Fingerprint(append(obsTexts, labelPromptHash)...)

			existing, err := artifacts.GetLabels(key.source, key.sessionID)
			if err == nil && existing.Fingerprint == fp {
				hits.Add(1)
				for _, item := range existing.Items {
					if item.Label != "" {
						labels.add(item.Label)
					}
				}
				n := counter.Add(1)
				if verbose {
					fmt.Fprintf(os.Stderr, "  [%d/%d] Cached labels for %s/%s\n", n, total, key.source, key.sessionID)
				}
				return
			}
			misses.Add(1)

			// Batch label: send up to labelBatchSize observations per call
			var items []Label
			var usage inference.Usage
			for i := 0; i < len(entries); i += labelBatchSize {
				end := i + labelBatchSize
				if end > len(entries) {
					end = len(entries)
				}
				batch := entries[i:end]

				var batchTexts []string
				for _, e := range batch {
					batchTexts = append(batchTexts, e.Text)
				}

				prompt := buildLabelPrompt(labels.list())
				input := buildLabelInput(batchTexts)
				resp, u, err := llm.Converse(errCtx, prompt, input, inference.WithMaxTokens(1024))
				usage = usage.Add(u)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("label batch for %s/%s: %w", key.source, key.sessionID, err)
						cancelOnErr()
					}
					mu.Unlock()
					return
				}

				batchLabels := parseLabelResponse(resp, len(batch))
				for j, e := range batch {
					lbl := batchLabels[j]
					if lbl == "" {
						lbl = "uncategorized"
					}
					items = append(items, Label{
						Observation: e.Text,
						Label:       lbl,
					})
					labels.add(lbl)
				}
			}

			lblResult := &Labels{
				Fingerprint: fp,
				Items:       items,
			}
			if err := artifacts.PutLabels(key.source, key.sessionID, lblResult); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("save labels for %s/%s: %w", key.source, key.sessionID, err)
					cancelOnErr()
				}
				mu.Unlock()
				return
			}

			n := counter.Add(1)
			if verbose {
				fmt.Fprintf(os.Stderr, "  [%d/%d] Labeled %s/%s (%d items, $%.4f)\n",
					n, total, key.source, key.sessionID, len(items), usage.Cost())
			}

			mu.Lock()
			totalUsage = totalUsage.Add(usage)
			mu.Unlock()
		}(key, entries)
	}
	wg.Wait()

	if firstErr != nil {
		return inference.Usage{}, HitMiss{}, 0, firstErr
	}

	return totalUsage, HitMiss{Hit: int(hits.Load()), Miss: int(misses.Load())}, len(labels.list()), nil
}

// ── NORMALIZE ───────────────────────────────────────────────────────────

// runNormalize merges synonymous labels via a single LLM call.
// The mapping is cached by label vocabulary hash — it only reruns when
// the set of labels changes.
func runNormalize(
	ctx context.Context,
	artifacts *ArtifactStore,
	llm LLM,
	verbose bool,
) (inference.Usage, error) {
	// Collect all unique labels
	sessions, err := artifacts.ListLabels()
	if err != nil {
		return inference.Usage{}, fmt.Errorf("list labels: %w", err)
	}

	uniqueLabels := map[string]bool{}
	for _, ss := range sessions {
		lbl, err := artifacts.GetLabels(ss.Source, ss.SessionID)
		if err != nil {
			return inference.Usage{}, fmt.Errorf("get labels %s/%s: %w", ss.Source, ss.SessionID, err)
		}
		for _, item := range lbl.Items {
			if item.Label != "" {
				uniqueLabels[item.Label] = true
			}
		}
	}

	if len(uniqueLabels) == 0 {
		return inference.Usage{}, nil
	}

	// Sort labels for deterministic fingerprinting
	sorted := make([]string, 0, len(uniqueLabels))
	for l := range uniqueLabels {
		sorted = append(sorted, l)
	}
	sort.Strings(sorted)

	fp := Fingerprint(append(sorted, Fingerprint(prompts.Normalize))...)

	// Check cache
	existing, err := artifacts.GetNormalization()
	if err == nil && existing.Fingerprint == fp {
		// Cache hit — still need to apply mapping to ensure consistency
		if len(existing.Mapping) > 0 {
			applyNormalization(artifacts, sessions, existing.Mapping)
		}
		return inference.Usage{}, nil
	}

	// Build input: one label per line
	var input strings.Builder
	for _, l := range sorted {
		input.WriteString("- ")
		input.WriteString(l)
		input.WriteString("\n")
	}

	resp, usage, err := llm.Converse(ctx, prompts.Normalize, input.String(), inference.WithMaxTokens(4096))
	if err != nil {
		return usage, fmt.Errorf("normalize: %w", err)
	}

	// Parse "original → canonical" lines
	mapping := parseNormalizationResponse(resp)

	if verbose && len(mapping) > 0 {
		fmt.Fprintf(os.Stderr, "  Normalized %d labels:\n", len(mapping))
		for from, to := range mapping {
			fmt.Fprintf(os.Stderr, "    %s → %s\n", from, to)
		}
	}

	// Save normalization mapping
	norm := &Normalization{
		Fingerprint: fp,
		Mapping:     mapping,
	}
	if err := artifacts.PutNormalization(norm); err != nil {
		return usage, fmt.Errorf("save normalization: %w", err)
	}

	// Apply mapping to label artifacts
	if len(mapping) > 0 {
		applyNormalization(artifacts, sessions, mapping)
	}

	return usage, nil
}

// parseNormalizationResponse parses "original → canonical" lines from the LLM response.
func parseNormalizationResponse(resp string) map[string]string {
	mapping := map[string]string{}
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip leading "- " bullet if present
		line = strings.TrimPrefix(line, "- ")

		// Try unicode arrow first, then ASCII
		parts := strings.SplitN(line, "→", 2)
		if len(parts) != 2 {
			parts = strings.SplitN(line, "->", 2)
		}
		if len(parts) != 2 {
			continue
		}
		from := strings.TrimSpace(parts[0])
		to := strings.TrimSpace(parts[1])
		// Strip surrounding quotes
		from = strings.Trim(from, "\"'")
		to = strings.Trim(to, "\"'")
		if from != "" && to != "" && from != to {
			mapping[from] = to
		}
	}
	return mapping
}

// applyNormalization rewrites label artifacts, replacing labels according to the mapping.
func applyNormalization(artifacts *ArtifactStore, sessions []SourceSession, mapping map[string]string) {
	for _, ss := range sessions {
		lbl, err := artifacts.GetLabels(ss.Source, ss.SessionID)
		if err != nil {
			continue
		}
		changed := false
		for i, item := range lbl.Items {
			if canonical, ok := mapping[item.Label]; ok {
				lbl.Items[i].Label = canonical
				changed = true
			}
		}
		if changed {
			artifacts.PutLabels(ss.Source, ss.SessionID, lbl)
		}
	}
}

// ── GROUP ───────────────────────────────────────────────────────────────

type clusterResult struct {
	ID              int
	ObservationIdxs []int // indices into the flat allObs slice
}

// runGroup groups observations by exact label match.
// Labels with minClusterSize+ observations form clusters; the rest is noise.
func runGroup(artifacts *ArtifactStore, allObs []observationEntry) ([]clusterResult, []string, error) {
	// Load labels to get label for each observation
	type sessionKey struct{ source, sessionID string }
	lblBySession := map[sessionKey]*Labels{}
	sessions, err := artifacts.ListLabels()
	if err != nil {
		return nil, nil, err
	}
	for _, ss := range sessions {
		lbl, err := artifacts.GetLabels(ss.Source, ss.SessionID)
		if err != nil {
			return nil, nil, fmt.Errorf("get labels %s/%s: %w", ss.Source, ss.SessionID, err)
		}
		lblBySession[sessionKey{ss.Source, ss.SessionID}] = lbl
	}

	// Build observation → label mapping
	obsLabels := make([]string, len(allObs))
	for i, obs := range allObs {
		key := sessionKey{obs.Source, obs.SessionID}
		lbl, ok := lblBySession[key]
		if ok && obs.Index < len(lbl.Items) {
			obsLabels[i] = strings.ToLower(strings.TrimSpace(lbl.Items[obs.Index].Label))
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
	Theme        string // label theme for the cluster
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

		// Determine cluster theme from labels
		theme := ""
		if len(indices) > 0 {
			obs := allObs[indices[0]]
			lbl, err := artifacts.GetLabels(obs.Source, obs.SessionID)
			if err == nil && obs.Index < len(lbl.Items) {
				theme = lbl.Items[obs.Index].Label
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

// ── SUMMARIZE ──────────────────────────────────────────────────────────

// runSummarize runs parallel per-cluster synthesis.
func runSummarize(
	ctx context.Context,
	llm LLM,
	samples []clusterSample,
	counter *atomic.Int32,
) ([]string, inference.Usage, error) {
	summaries := make([]string, len(samples))
	errs := make([]error, len(samples))
	usages := make([]inference.Usage, len(samples))

	// errCtx is cancelled on first error to short-circuit remaining goroutines.
	errCtx, cancelOnErr := context.WithCancel(ctx)
	defer cancelOnErr()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 50)

	for i, sample := range samples {
		wg.Add(1)
		go func(i int, sample clusterSample) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if errCtx.Err() != nil {
				counter.Add(1)
				return
			}

			var input strings.Builder
			fmt.Fprintf(&input, "Cluster theme: %s\n\nObservations:\n", sample.Theme)
			for _, obs := range sample.Observations {
				input.WriteString("\n---\n")
				input.WriteString(obs)
			}

			resp, usage, err := llm.Converse(ctx, prompts.Summarize, input.String(), inference.WithMaxTokens(4096))
			summaries[i] = strings.TrimSpace(resp)
			usages[i] = usage
			if err != nil {
				errs[i] = err
				cancelOnErr()
			}
			counter.Add(1)
		}(i, sample)
	}
	wg.Wait()

	var totalUsage inference.Usage
	for i, err := range errs {
		if err != nil {
			return nil, totalUsage, fmt.Errorf("summarize cluster %d: %w", i, err)
		}
		totalUsage = totalUsage.Add(usages[i])
	}

	return summaries, totalUsage, nil
}

// ── COMPOSE ────────────────────────────────────────────────────────────

// runCompose combines cluster summaries and noise observations into muse.md.
func runCompose(
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

	stream := newStageStream(16000)
	muse, usage, err := llm.ConverseStream(ctx, prompts.ComposeClustered, input.String(), stream.callback(), inference.WithThinking(16000))
	stream.finish()
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
