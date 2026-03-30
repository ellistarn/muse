package compose

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

	"golang.org/x/sync/errgroup"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// minClusterSize is the minimum number of observations sharing a label to form
// a cluster. Labels with fewer observations flow through as noise.
const minClusterSize = 3

// themeThreshold triggers vocabulary theming when the number of unique
// labels exceeds this count. Cold-start parallel labeling produces fragmented
// vocabularies; the theme step consolidates them into canonical themes.
const themeThreshold = 50

// ClusteredOptions configures a clustered compose run.
type ClusteredOptions struct {
	BaseOptions
	Relabel bool // force re-label all observations

	// Upload results from the load phase, folded into the discover line.
	Uploaded    int // new conversations ingested this run
	UploadBytes int // total bytes of new conversations
}

// RunClustered executes the full clustering composition pipeline:
// observe → label → theme → group → sample → summarize → compose → diff.
//
// Labeling happens per-conversation in parallel, producing fragmented vocabulary.
// The theme step consolidates labels into canonical themes when the vocabulary
// exceeds the threshold. Grouping is by exact label match after theming.
func RunClustered(
	ctx context.Context,
	store storage.Store,
	observeLLM, labelLLM, summarizeLLM, composeLLM inference.Client,
	opts ClusteredOptions,
) (*Result, error) {
	pipelineStart := time.Now()
	var totalUsage inference.Usage
	var stages []StageStats

	// ── OBSERVE ─────────────────────────────────────────────────────────
	var observeCounter atomic.Int32
	observeStart := time.Now()
	observeResult, err := runObserve(ctx, store, observeLLM, opts, &observeCounter)
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
	allObs, err := loadAllStructuredObservations(ctx, store)
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
	).Cost(time.Since(observeStart), observeResult.usage.Cost()).Print()

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
	// Count conversations for progress total
	labelConversations := map[string]bool{}
	for _, obs := range allObs {
		labelConversations[obs.Source+"/"+obs.ConversationID] = true
	}
	labelTotal := len(labelConversations)
	labelBeforeNote := ""
	if labelTotal > 0 {
		labelBeforeNote = fmt.Sprintf(" (%d conversations)", labelTotal)
	}
	logBefore("label", "%d observations%s", len(allObs), labelBeforeNote)
	prog := startProgress(labelTotal, &labelCounter)
	labelUsage, labelCache, numLabels, err := runLabel(ctx, store, labelLLM, allObs, opts.Relabel, opts.Verbose, &labelCounter)
	prog.Stop()
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
		labelNote = fmt.Sprintf(" [%d conversations cached]", labelCache.Hit)
	}
	logAfter("%d labels%s", numLabels, labelNote).Cost(time.Since(labelStart), labelUsage.Cost()).Print()

	// ── THEME (when label set is fragmented) ──────────────────────────────
	var themeMapping map[string]string
	if numLabels > themeThreshold {
		themeStart := time.Now()
		logBefore("theme", "%d labels", numLabels)
		var themeUsage inference.Usage
		themeMapping, themeUsage, err = runTheme(ctx, store, labelLLM, opts.Verbose)
		if err != nil {
			return nil, fmt.Errorf("theme: %w", err)
		}
		totalUsage = totalUsage.Add(themeUsage)
		stages = append(stages, StageStats{
			Name:     "theme",
			Model:    labelLLM.Model(),
			Duration: time.Since(themeStart),
			Usage:    themeUsage,
		})
		logAfter("themed").Cost(time.Since(themeStart), themeUsage.Cost()).Print()
	}

	// ── GROUP ───────────────────────────────────────────────────────────
	groupStart := time.Now()
	clusters, noiseObs, err := runGroup(ctx, store, allObs, themeMapping)
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
		len(allObs), len(clusters), len(noiseObs)).Print()

	// ── SAMPLE ──────────────────────────────────────────────────────────
	sampleStart := time.Now()
	samples := runSampleWithObs(ctx, clusters, allObs, store)
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
		len(samples), totalSampled).Print()

	// ── SUMMARIZE ──────────────────────────────────────────────────────
	var synthCounter atomic.Int32
	synthStart := time.Now()
	logBefore("summarize", "%d clusters", len(samples))
	prog = startProgress(len(samples), &synthCounter)
	summaries, synthUsage, err := runSummarize(ctx, summarizeLLM, samples, &synthCounter)
	prog.Stop()
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
	logAfter("%d summaries", len(summaries)).Cost(time.Since(synthStart), synthUsage.Cost()).Print()

	// ── COMPOSE ────────────────────────────────────────────────────────
	composeStart := time.Now()
	composeInput := fmt.Sprintf("%d summaries", len(summaries))
	if len(noiseObs) > 0 {
		composeInput += fmt.Sprintf(" + %d outliers", len(noiseObs))
	}
	logBefore("compose", "%s", composeInput)
	muse, _, composeUsage, err := runCompose(ctx, composeLLM, store, summaries, noiseObs)
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
	logAfter("muse.md").Cost(time.Since(composeStart), composeUsage.Cost()).Print()

	// ── DONE ────────────────────────────────────────────────────────────
	logStage("done", "%d patterns → muse.md", len(clusters)).
		Cost(time.Since(pipelineStart), totalUsage.Cost()).Print()

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
	}, nil
}

// observationBytes returns the total byte size of all formatted observation
// content. This reflects what downstream LLM stages actually receive via
// Format(), including date, quote prefix, and observation prefix overhead.
func observationBytes(obs []observationEntry) int {
	n := 0
	for _, o := range obs {
		n += len(o.Format())
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
	llm inference.Client,
	opts ClusteredOptions,
	counter *atomic.Int32,
) (*observeResult, error) {
	entries, err := store.ListConversations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}

	// Handle reobserve
	if opts.Reobserve {
		if len(opts.Sources) > 0 {
			for _, src := range opts.Sources {
				DeleteObservationsForSource(ctx, store, src)
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "  Cleared observations for %s\n", src)
				}
			}
		} else {
			DeleteObservations(ctx, store)
			fmt.Fprintln(os.Stderr, "  Cleared all observations")
		}
	}

	// Filter by sources
	entries = storage.FilterEntriesBySource(entries, opts.Sources)

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
	promptHash := Fingerprint(prompts.Extract, prompts.ObserveHuman, prompts.Refine)

	// Determine which conversations need (re)observation
	var pending []storage.ConversationEntry
	var pruned int
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fp := Fingerprint(e.LastModified.Format(time.RFC3339Nano), promptHash)

		existing, err := GetObservations(ctx, store, e.Source, e.ConversationID)
		if err == nil && existing.Fingerprint == fp {
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
		discoverLine.Tail = fmt.Sprintf("(%d new %s, %s)", opts.Uploaded, noun, FormatBytes(opts.UploadBytes))
	}
	discoverLine.Print()

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

		g, ctx := errgroup.WithContext(ctx)
		g.SetLimit(50)

		for _, entry := range pending {
			g.Go(func() error {
				conv, err := store.GetConversation(ctx, entry.Source, entry.ConversationID)
				if err != nil {
					counter.Add(1)
					return fmt.Errorf("load conversation %s: %w", entry.Key, err)
				}

				convBytes := conversationDataSize(conv)

				start := time.Now()
				items, u, err := extractObservations(ctx, llm, conv, opts.Verbose)
				n := counter.Add(1)
				if err != nil {
					return fmt.Errorf("observe %s: %w", entry.Key, err)
				}

				fp := Fingerprint(entry.LastModified.Format(time.RFC3339Nano), promptHash)
				obs := &Observations{
					Fingerprint: fp,
					Date:        entry.LastModified.Format("2006-01-02"),
					Items:       items,
				}
				if err := PutObservations(ctx, store, entry.Source, entry.ConversationID, obs); err != nil {
					return fmt.Errorf("save observations for %s: %w", entry.Key, err)
				}

				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "  [%d/%d] Observed ~/.muse/%s (%d obs, %s, $%.4f)\n",
						n, len(pending), observationPath(entry.Source, entry.ConversationID), len(items),
						time.Since(start).Round(time.Millisecond), u.Cost())
				}
				mu.Lock()
				usage = usage.Add(u)
				dataSize += convBytes
				mu.Unlock()
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			prog.Stop()
			return nil, err
		}
		prog.Stop()
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

// conversationDataSize returns the total byte size of all message content in a conversation.
func conversationDataSize(conv *conversation.Conversation) int {
	n := 0
	for _, msg := range conv.Messages {
		n += len(msg.Content)
	}
	return n
}

// extractObservations runs the observe pipeline on a conversation and returns
// discrete observations (not a markdown blob).
//
// Raw conversation is sent to the extract prompt by default. When the raw text
// exceeds the context window, mechanical compression is applied as a fallback:
// code blocks are stripped, tool output is collapsed to [tool: name] markers,
// and long assistant messages are truncated.
func extractObservations(ctx context.Context, client inference.Client, conv *conversation.Conversation, verbose bool) ([]Observation, inference.Usage, error) {
	refined, usage, err := extractAndRefine(ctx, client, conv, verbose)
	if err != nil {
		return nil, usage, err
	}
	if refined == "" {
		return nil, usage, nil
	}

	// Parse refined output into discrete items.
	items := parseObservationItems(refined)

	// Filter irrelevant items: empty responses, LLM meta-commentary, placeholder tokens
	var relevant []Observation
	var irrelevant []Observation
	for _, item := range items {
		if isRelevant(item.Text) {
			relevant = append(relevant, item)
		} else {
			irrelevant = append(irrelevant, item)
		}
	}
	if len(irrelevant) > 0 && verbose {
		fmt.Fprintf(os.Stderr, "    filtered %d irrelevant observations:\n", len(irrelevant))
		for _, item := range irrelevant {
			text := item.Text
			if len(text) > 100 {
				text = text[:100] + "..."
			}
			fmt.Fprintf(os.Stderr, "      - %s\n", text)
		}
	}
	return relevant, usage, nil
}

// extractAndRefine is the shared core of the observation pipeline. It extracts
// turns from a conversation, mechanically compresses them, runs the extract
// prompt in chunks, and refines the candidates into a single text.
// Both the map-reduce path (which wants raw text) and the clustering path
// (which further parses into discrete items) call this function.
func extractAndRefine(ctx context.Context, client inference.Client, conv *conversation.Conversation, verbose bool) (string, inference.Usage, error) {
	turns := extractTurns(conv)
	if len(turns) == 0 {
		return "", inference.Usage{}, nil
	}

	chunks := compressConversation(turns, conv.Source)
	if len(chunks) == 0 {
		return "", inference.Usage{}, nil
	}

	// Select the appropriate extract prompt based on source type
	extractPrompt := prompts.Extract
	if isHumanSource(conv.Source) {
		extractPrompt = prompts.ObserveHuman
	}

	var totalUsage inference.Usage

	// Extract candidates (Pass 1)
	var allCandidates []string
	for i, chunk := range chunks {
		start := time.Now()
		obs, usage, err := inference.Converse(ctx, client, extractPrompt, chunk, inference.WithMaxTokens(4096))
		totalUsage = totalUsage.Add(usage)
		if verbose {
			fmt.Fprintf(os.Stderr, "      extract[%d/%d] %d chars → %d chars (%s, $%.4f)\n",
				i+1, len(chunks), len(chunk), len(obs), time.Since(start).Round(time.Millisecond), usage.Cost())
		}
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

	// Refine (Pass 2)
	candidates := strings.Join(allCandidates, "\n\n")
	start := time.Now()
	refined, usage, err := inference.Converse(ctx, client, prompts.Refine, candidates, inference.WithMaxTokens(4096))
	totalUsage = totalUsage.Add(usage)
	if verbose {
		fmt.Fprintf(os.Stderr, "      refine %d chars → %d chars (%s, $%.4f)\n",
			len(candidates), len(refined), time.Since(start).Round(time.Millisecond), usage.Cost())
	}
	if err != nil && refined == "" {
		return "", totalUsage, err
	}
	if isEmpty(refined) {
		return "", totalUsage, nil
	}
	return refined, totalUsage, nil
}

// maxAssistantChars caps truncated assistant messages. Long assistant output is
// mostly code or tool results — the human's reaction is what matters, not the
// full assistant text.
const maxAssistantChars = 500

// isHumanSource returns true for sources where conversations are between
// the owner and other people (not AI assistants).
func isHumanSource(source string) bool {
	switch source {
	case "slack", "github":
		return true
	default:
		return false
	}
}

// compressConversation mechanically compresses turns for extraction: strips
// code blocks, collapses tool output to [tool: name] markers, and truncates
// long assistant/peer messages. Returns chunked text ready for the extract prompt.
func compressConversation(turns []turn, source string) []string {
	var chunks []string
	var b strings.Builder

	human := isHumanSource(source)
	ownerLabel := "[human]"
	peerLabel := "[assistant]"
	if human {
		ownerLabel = "[owner]"
		peerLabel = "[peer]"
	}

	for _, t := range turns {
		var entry string
		if t.assistantContent != "" {
			compressed := compressAssistant(t.assistantContent)
			entry = fmt.Sprintf("%s: %s\n%s: %s\n\n", peerLabel, compressed, ownerLabel, t.humanContent)
		} else {
			entry = fmt.Sprintf("%s: %s\n\n", ownerLabel, t.humanContent)
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
	"[some ",    // placeholder bracket tokens from inference.Client
	"[another ", // placeholder bracket tokens from inference.Client
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
// person's thinking rather than empty output, a placeholder token, or inference.Client
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

// quotePrefix is the optional prefix for verbatim voice-carrying quotes.
const quotePrefix = "Quote: "

// parseObservationItems extracts discrete observations from LLM output.
// Lines starting with "Observation: " are extracted. An optional "Quote: " line
// preceding an "Observation: " line is paired with that observation — intervening
// blank lines do not break the pairing, but any non-empty non-prefix line does.
// All other lines (meta-commentary, blank lines, preamble) are discarded.
func parseObservationItems(text string) []Observation {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var items []Observation
	var pendingQuote string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		// Strip optional bullet/number prefix before checking for prefixes.
		// Handles "- Observation: ...", "1. Observation: ...", "• Observation: ..."
		cleaned := stripListPrefix(line)

		if strings.HasPrefix(cleaned, quotePrefix) {
			q := strings.TrimSpace(cleaned[len(quotePrefix):])
			// Strip surrounding quotes if present
			q = strings.Trim(q, "\"")
			q = strings.Trim(q, "\u201c\u201d") // smart quotes
			pendingQuote = q
			continue
		}

		if strings.HasPrefix(cleaned, observationPrefix) {
			obs := strings.TrimSpace(cleaned[len(observationPrefix):])
			if obs != "" {
				items = append(items, Observation{
					Quote: pendingQuote,
					Text:  obs,
				})
			}
			pendingQuote = ""
			continue
		}

		// Any non-Quote, non-Observation line resets the pending quote
		if cleaned != "" {
			pendingQuote = ""
		}
	}
	return items
}

// stripListPrefix removes a leading bullet or numbered-list marker from a line.
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

// observationEntry flattens source/conversation/index into a single record
// so downstream stages can track observations across conversations.
type observationEntry struct {
	Source         string
	ConversationID string
	Index          int
	Quote          string // optional verbatim quote carrying voice signal
	Text           string // the analytical observation text
	Date           string // conversation date (YYYY-MM-DD)
}

// Format returns the observation as text for LLM consumption. When a quote
// is present, it's included as a voice exemplar preceding the observation.
// The date is included so downstream stages can reason about recency.
func (e observationEntry) Format() string {
	var parts []string
	if e.Date != "" {
		parts = append(parts, fmt.Sprintf("[%s]", e.Date))
	}
	if e.Quote != "" {
		parts = append(parts, fmt.Sprintf("Quote: \"%s\"", e.Quote))
	}
	parts = append(parts, fmt.Sprintf("Observation: %s", e.Text))
	return strings.Join(parts, "\n")
}

// FormatTokens estimates the token count of the formatted observation.
func (e observationEntry) FormatTokens() int {
	return inference.EstimateTokens(e.Format())
}

// loadAllStructuredObservations loads all observation artifacts and returns
// a flat list of observation entries, loading conversations in parallel.
func loadAllStructuredObservations(ctx context.Context, store storage.Store) ([]observationEntry, error) {
	convList, err := ListObservations(ctx, store)
	if err != nil {
		return nil, err
	}

	type result struct {
		entries []observationEntry
		err     error
	}
	results := make([]result, len(convList))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(20)
	for i, ss := range convList {
		g.Go(func() error {
			obs, err := GetObservations(ctx, store, ss.Source, ss.ConversationID)
			if err != nil {
				results[i] = result{err: fmt.Errorf("get observations %s/%s: %w", ss.Source, ss.ConversationID, err)}
				return results[i].err
			}
			var entries []observationEntry
			for j, item := range obs.Items {
				entries = append(entries, observationEntry{
					Source:         ss.Source,
					ConversationID: ss.ConversationID,
					Index:          j,
					Quote:          item.Quote,
					Text:           item.Text,
					Date:           obs.Date,
				})
			}
			results[i] = result{entries: entries}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

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
// Conversations are processed in parallel with a shared label vocabulary.
// A normalization step downstream merges synonymous labels.
func runLabel(
	ctx context.Context,
	store storage.Store,
	llm inference.Client,
	allObs []observationEntry,
	forceRelabel bool,
	verbose bool,
	counter *atomic.Int32,
) (inference.Usage, HitMiss, int, error) {
	if forceRelabel {
		DeleteLabels(ctx, store)
	}

	labelPromptHash := Fingerprint(prompts.Label)

	// Seed the label set from cached labels
	labels := newLabelSet()
	existingLabels, err := ListLabels(ctx, store)
	if err != nil {
		return inference.Usage{}, HitMiss{}, 0, fmt.Errorf("list labels: %w", err)
	}
	for _, ss := range existingLabels {
		lbl, err := GetLabels(ctx, store, ss.Source, ss.ConversationID)
		if err != nil {
			return inference.Usage{}, HitMiss{}, 0, fmt.Errorf("get labels %s/%s: %w", ss.Source, ss.ConversationID, err)
		}
		for _, item := range lbl.Items {
			if item.Label != "" {
				labels.add(item.Label)
			}
		}
	}

	// Group observations by (source, conversationID)
	type conversationKey struct{ source, conversationID string }
	groups := map[conversationKey][]observationEntry{}
	for _, obs := range allObs {
		key := conversationKey{obs.Source, obs.ConversationID}
		groups[key] = append(groups[key], obs)
	}

	// Sort conversation keys by observation count descending (longest pole first)
	type conversationWork struct {
		key     conversationKey
		entries []observationEntry
	}
	work := make([]conversationWork, 0, len(groups))
	for key, entries := range groups {
		work = append(work, conversationWork{key, entries})
	}
	sort.Slice(work, func(i, j int) bool {
		return len(work[i].entries) > len(work[j].entries)
	})

	var mu sync.Mutex
	var totalUsage inference.Usage
	var hits, misses atomic.Int32
	total := len(groups)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(50)

	for _, w := range work {
		g.Go(func() error {
			key := w.key
			entries := w.entries

			// Check cache
			var obsTexts []string
			for _, e := range entries {
				obsTexts = append(obsTexts, e.Text)
			}
			// Cache key: observation texts + prompt hash. The label vocabulary is
			// intentionally excluded — it grows across runs as new labels emerge,
			// which caused spurious cache invalidation on warm runs. The vocabulary
			// is an output of labeling, not an input: the LLM assigns labels based
			// on the prompt and observation text, not the current vocabulary. Stale
			// cached labels are acceptable because compose is re-run frequently and
			// the vocabulary stabilizes quickly.
			fp := Fingerprint(append(obsTexts, labelPromptHash)...)

			existing, err := GetLabels(ctx, store, key.source, key.conversationID)
			if err == nil && existing.Fingerprint == fp {
				hits.Add(1)
				for _, item := range existing.Items {
					if item.Label != "" {
						labels.add(item.Label)
					}
				}
				n := counter.Add(1)
				if verbose {
					fmt.Fprintf(os.Stderr, "  [%d/%d] Cached labels for %s/%s\n", n, total, key.source, key.conversationID)
				}
				return nil
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
				resp, u, err := inference.Converse(ctx, llm, prompt, input, inference.WithMaxTokens(1024))
				usage = usage.Add(u)
				if err != nil {
					return fmt.Errorf("label batch for %s/%s: %w", key.source, key.conversationID, err)
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
			if err := PutLabels(ctx, store, key.source, key.conversationID, lblResult); err != nil {
				return fmt.Errorf("save labels for %s/%s: %w", key.source, key.conversationID, err)
			}

			n := counter.Add(1)
			if verbose {
				fmt.Fprintf(os.Stderr, "  [%d/%d] Labeled %s/%s (%d items, $%.4f)\n",
					n, total, key.source, key.conversationID, len(items), usage.Cost())
			}

			mu.Lock()
			totalUsage = totalUsage.Add(usage)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return inference.Usage{}, HitMiss{}, 0, err
	}

	return totalUsage, HitMiss{Hit: int(hits.Load()), Miss: int(misses.Load())}, len(labels.list()), nil
}

// ── THEME ─────────────────────────────────────────────────────────────

// runTheme consolidates a fragmented label set into canonical themes.
// This handles the cold-start case where parallel labeling produces hundreds of
// unique labels because conversations are labeled without shared vocabulary.
// The mapping is applied at group time (labels on disk stay pristine).
func runTheme(
	ctx context.Context,
	store storage.Store,
	llm inference.Client,
	verbose bool,
) (map[string]string, inference.Usage, error) {
	// Collect all unique labels
	convList, err := ListLabels(ctx, store)
	if err != nil {
		return nil, inference.Usage{}, fmt.Errorf("list labels: %w", err)
	}

	uniqueLabels := map[string]bool{}
	for _, ss := range convList {
		lbl, err := GetLabels(ctx, store, ss.Source, ss.ConversationID)
		if err != nil {
			return nil, inference.Usage{}, fmt.Errorf("get labels %s/%s: %w", ss.Source, ss.ConversationID, err)
		}
		for _, item := range lbl.Items {
			if item.Label != "" {
				uniqueLabels[item.Label] = true
			}
		}
	}

	sorted := make([]string, 0, len(uniqueLabels))
	for l := range uniqueLabels {
		sorted = append(sorted, l)
	}
	sort.Strings(sorted)

	fp := Fingerprint(append(sorted, Fingerprint(prompts.Theme))...)

	// Check cache
	existing, err := GetThemes(ctx, store)
	if err == nil && existing.Fingerprint == fp {
		return existing.Mapping, inference.Usage{}, nil
	}

	// Build input: one label per line
	var input strings.Builder
	for _, l := range sorted {
		input.WriteString("- ")
		input.WriteString(l)
		input.WriteString("\n")
	}

	resp, usage, err := inference.Converse(ctx, llm, prompts.Theme, input.String(), inference.WithMaxTokens(16384))
	if err != nil {
		return nil, usage, fmt.Errorf("theme: %w", err)
	}

	mapping := parseThemeResponse(resp)

	if verbose && len(mapping) > 0 {
		fmt.Fprintf(os.Stderr, "  Themed %d labels:\n", len(mapping))
	}

	// Save
	themes := &LabelMapping{
		Fingerprint: fp,
		Mapping:     mapping,
	}
	if err := PutThemes(ctx, store, themes); err != nil {
		return nil, usage, fmt.Errorf("save themes: %w", err)
	}

	return mapping, usage, nil
}

// parseThemeResponse parses "original → canonical" lines from the LLM response.
func parseThemeResponse(resp string) map[string]string {
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

// ── GROUP ───────────────────────────────────────────────────────────────

type clusterResult struct {
	ID              int
	ObservationIdxs []int // indices into the flat allObs slice
}

// runGroup groups observations by exact label match, applying normalization at read time.
// Labels on disk stay pristine; the normalization mapping is applied in-memory only.
// Labels with minClusterSize+ observations form clusters; the rest is noise.
func runGroup(ctx context.Context, store storage.Store, allObs []observationEntry, themeMapping map[string]string) ([]clusterResult, []string, error) {
	// Load labels to get label for each observation
	type conversationKey struct{ source, conversationID string }
	lblByConversation := map[conversationKey]*Labels{}
	convList, err := ListLabels(ctx, store)
	if err != nil {
		return nil, nil, err
	}
	for _, ss := range convList {
		lbl, err := GetLabels(ctx, store, ss.Source, ss.ConversationID)
		if err != nil {
			return nil, nil, fmt.Errorf("get labels %s/%s: %w", ss.Source, ss.ConversationID, err)
		}
		lblByConversation[conversationKey{ss.Source, ss.ConversationID}] = lbl
	}

	// Build observation → label mapping, applying normalization at read time
	obsLabels := make([]string, len(allObs))
	for i, obs := range allObs {
		key := conversationKey{obs.Source, obs.ConversationID}
		lbl, ok := lblByConversation[key]
		if ok && obs.Index < len(lbl.Items) {
			label := strings.ToLower(strings.TrimSpace(lbl.Items[obs.Index].Label))
			// Strip surrounding quotes from labels
			label = strings.Trim(label, "\"'")
			// Apply normalization mapping in-memory (labels stay pristine on disk)
			if canonical, ok := themeMapping[label]; ok {
				label = strings.ToLower(strings.TrimSpace(canonical))
			}
			obsLabels[i] = label
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
			noiseObs = append(noiseObs, obs.Format())
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
func runSampleWithObs(ctx context.Context, clusters []clusterResult, allObs []observationEntry, store storage.Store) []clusterSample {
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
			t := obs.FormatTokens()
			if tokens+t > maxSampleTokens && len(selected) > 0 {
				break
			}
			selected = append(selected, obs.Format())
			tokens += t
		}

		// Determine cluster theme from labels
		theme := ""
		if len(indices) > 0 {
			obs := allObs[indices[0]]
			lbl, err := GetLabels(ctx, store, obs.Source, obs.ConversationID)
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
	llm inference.Client,
	samples []clusterSample,
	counter *atomic.Int32,
) ([]string, inference.Usage, error) {
	// Sort clusters by observation count descending (longest pole first)
	sort.Slice(samples, func(i, j int) bool {
		return len(samples[i].Observations) > len(samples[j].Observations)
	})

	summaries := make([]string, len(samples))
	usages := make([]inference.Usage, len(samples))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(50)

	for i, sample := range samples {
		g.Go(func() error {
			var input strings.Builder
			fmt.Fprintf(&input, "Cluster theme: %s\n\nObservations:\n", sample.Theme)
			for _, obs := range sample.Observations {
				input.WriteString("\n---\n")
				input.WriteString(obs)
			}

			resp, usage, err := inference.Converse(ctx, llm, prompts.Summarize, input.String(), inference.WithMaxTokens(4096))
			summaries[i] = strings.TrimSpace(resp)
			usages[i] = usage
			counter.Add(1)
			if err != nil {
				return fmt.Errorf("summarize cluster %d: %w", i, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, inference.Usage{}, err
	}

	var totalUsage inference.Usage
	for _, u := range usages {
		totalUsage = totalUsage.Add(u)
	}

	return summaries, totalUsage, nil
}

// ── COMPOSE ────────────────────────────────────────────────────────────

// runCompose combines cluster summaries and noise observations into muse.md.
func runCompose(
	ctx context.Context,
	llm inference.Client,
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

	stream := newStageStream(16000, 4096)
	muse, usage, err := inference.ConverseStream(ctx, llm, prompts.ComposeClustered, input.String(), stream.callback(), inference.WithThinking(16000))
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
