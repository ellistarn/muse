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
	"github.com/ellistarn/muse/internal/hdbscan"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// ClusteredOptions configures a clustered distill run.
type ClusteredOptions struct {
	Reobserve   bool
	Reclassify  bool
	Limit       int
	Sources     []string
	ArtifactDir string // root for artifact storage (e.g. ~/.muse)
}

// RunClustered executes the full clustering distillation pipeline:
// observe → classify → embed → group → sample → synthesize → merge → diff.
func RunClustered(
	ctx context.Context,
	store storage.Store,
	observeLLM, classifyLLM, synthesizeLLM, mergeLLM LLM,
	embedder embedding.Embedder,
	opts ClusteredOptions,
) (*Result, error) {
	artifacts := NewArtifactStore(opts.ArtifactDir)
	var totalUsage inference.Usage

	// ── OBSERVE ─────────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "Stage: observe")
	observeResult, err := runObserve(ctx, store, artifacts, observeLLM, opts)
	if err != nil {
		return nil, fmt.Errorf("observe: %w", err)
	}
	totalUsage = totalUsage.Add(observeResult.usage)

	// Load all observations
	allObs, err := loadAllStructuredObservations(artifacts)
	if err != nil {
		return nil, fmt.Errorf("load observations: %w", err)
	}
	if len(allObs) == 0 {
		return &Result{
			Processed: observeResult.processed,
			Pruned:    observeResult.pruned,
			Remaining: observeResult.remaining,
			Warnings:  observeResult.warnings,
		}, nil
	}

	// ── CLASSIFY ────────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "Stage: classify")
	classifyUsage, err := runClassify(ctx, artifacts, classifyLLM, allObs, opts.Reclassify)
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}
	totalUsage = totalUsage.Add(classifyUsage)

	// ── EMBED ───────────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "Stage: embed")
	if err := runEmbed(ctx, artifacts, embedder, allObs); err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}

	// ── GROUP ───────────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "Stage: group")
	clusters, noiseObs, err := runGroup(artifacts, allObs)
	if err != nil {
		return nil, fmt.Errorf("group: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  %d clusters, %d noise observations\n", len(clusters), len(noiseObs))

	// ── SAMPLE ──────────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "Stage: sample")
	samples := runSampleWithObs(clusters, allObs, artifacts)

	// ── SYNTHESIZE ──────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "Stage: synthesize")
	summaries, synthUsage, err := runSynthesize(ctx, synthesizeLLM, samples)
	if err != nil {
		return nil, fmt.Errorf("synthesize: %w", err)
	}
	totalUsage = totalUsage.Add(synthUsage)

	// ── MERGE ───────────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "Stage: merge")
	previousMuse, _ := store.GetMuse(ctx)
	muse, timestamp, mergeUsage, err := runMerge(ctx, mergeLLM, store, summaries, noiseObs)
	if err != nil {
		return nil, fmt.Errorf("merge: %w", err)
	}
	totalUsage = totalUsage.Add(mergeUsage)

	// ── DIFF ────────────────────────────────────────────────────────────
	d, diffUsage, derr := computeDiff(ctx, classifyLLM, store, timestamp, previousMuse, muse)
	if derr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to compute diff: %v\n", derr)
	}
	totalUsage = totalUsage.Add(diffUsage)

	processed := observeResult.processed
	return &Result{
		Processed: processed,
		Pruned:    observeResult.pruned,
		Remaining: observeResult.remaining,
		Usage:     totalUsage,
		Muse:      muse,
		Diff:      d,
		Warnings:  observeResult.warnings,
	}, nil
}

// observeResult holds intermediate observe stage results.
type observeResult struct {
	processed int
	pruned    int
	remaining int
	usage     inference.Usage
	warnings  []string
}

// runObserve discovers and observes conversations, producing structured
// observations ([]string items) stored as JSON artifacts.
func runObserve(
	ctx context.Context,
	store storage.Store,
	artifacts *ArtifactStore,
	llm LLM,
	opts ClusteredOptions,
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
				fmt.Fprintf(os.Stderr, "  Cleared observations for %s\n", src)
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

	// Compute prompt chain hash for fingerprinting
	promptHash := Fingerprint(prompts.ObserveExtract, prompts.ObserveRefine, prompts.ObserveSummarize)

	// Determine which conversations need (re)observation
	var pending []storage.SessionEntry
	var pruned int
	for _, e := range entries {
		session, err := store.GetSession(ctx, e.Source, e.SessionID)
		if err != nil {
			continue
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
	var warnings []string
	var usage inference.Usage

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
					warnings = append(warnings, fmt.Sprintf("failed to load %s: %v", entry.Key, err))
					mu.Unlock()
					return
				}

				start := time.Now()
				items, u, err := extractObservations(ctx, llm, session)
				n := completed.Add(1)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  [%d/%d] error: %v %s\n", n, len(pending), err, entry.Key)
					mu.Lock()
					warnings = append(warnings, fmt.Sprintf("failed to observe %s: %v", entry.Key, err))
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
					warnings = append(warnings, fmt.Sprintf("failed to save observations for %s: %v", entry.Key, err))
					mu.Unlock()
					return
				}

				// Also persist as legacy observation for map-reduce compatibility
				if len(items) > 0 {
					legacy := strings.Join(items, "\n\n")
					_ = store.PutObservation(ctx, entry.Key, legacy)
				}

				fmt.Fprintf(os.Stderr, "  [%d/%d] Observed %s (%d obs, %s, $%.4f)\n",
					n, len(pending), entry.Key, len(items),
					time.Since(start).Round(time.Millisecond), u.Cost())
				mu.Lock()
				usage = usage.Add(u)
				mu.Unlock()
			}(entry)
		}
		wg.Wait()
		fmt.Fprintf(os.Stderr, "Observed %d conversations (%s, $%.4f)\n",
			len(pending)-len(warnings), time.Since(observeStart).Round(time.Millisecond), usage.Cost())
	}

	return &observeResult{
		processed: len(pending) - len(warnings),
		pruned:    pruned,
		remaining: totalPending - len(pending),
		usage:     usage,
		warnings:  warnings,
	}, nil
}

// extractObservations runs the observe pipeline on a session and returns
// discrete observation strings (not a markdown blob).
func extractObservations(ctx context.Context, client LLM, session *conversation.Session) ([]string, inference.Usage, error) {
	turns := extractTurns(session)
	if len(turns) == 0 {
		return nil, inference.Usage{}, nil
	}

	var totalUsage inference.Usage

	// Build human-focused view
	chunks, usage, err := buildHumanFocusedView(ctx, client, turns)
	totalUsage = totalUsage.Add(usage)
	if err != nil {
		return nil, totalUsage, err
	}
	if len(chunks) == 0 {
		return nil, totalUsage, nil
	}

	// Extract candidates
	var allCandidates []string
	for _, chunk := range chunks {
		obs, usage, err := client.Converse(ctx, prompts.ObserveExtract, chunk, inference.WithMaxTokens(4096))
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
	refined, usage, err := client.Converse(ctx, prompts.ObserveRefine, candidates, inference.WithMaxTokens(4096))
	totalUsage = totalUsage.Add(usage)
	if err != nil {
		return nil, totalUsage, err
	}
	if isEmpty(refined) {
		return nil, totalUsage, nil
	}

	// Parse refined output into discrete items.
	// Each observation is typically separated by blank lines or bullet points.
	items := parseObservationItems(refined)
	return items, totalUsage, nil
}

// parseObservationItems splits a refined observation output into discrete items.
func parseObservationItems(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Split on double newlines (paragraph boundaries)
	paragraphs := strings.Split(text, "\n\n")
	var items []string
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		// Remove leading bullet markers
		p = strings.TrimLeft(p, "- •*")
		p = strings.TrimSpace(p)
		if p != "" {
			items = append(items, p)
		}
	}
	if len(items) == 0 && text != "" {
		// Fallback: treat entire text as one observation
		items = []string{text}
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
			continue
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

// runClassify classifies each observation using an LLM.
func runClassify(
	ctx context.Context,
	artifacts *ArtifactStore,
	llm LLM,
	allObs []observationEntry,
	forceReclassify bool,
) (inference.Usage, error) {
	if forceReclassify {
		artifacts.DeleteClassifications()
	}

	classifyPromptHash := Fingerprint(prompts.Classify)
	start := time.Now()

	// Group observations by (source, sessionID)
	type sessionKey struct{ source, sessionID string }
	groups := map[sessionKey][]observationEntry{}
	for _, obs := range allObs {
		key := sessionKey{obs.Source, obs.SessionID}
		groups[key] = append(groups[key], obs)
	}

	var mu sync.Mutex
	var totalUsage inference.Usage
	var completed atomic.Int32
	total := len(groups)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for key, entries := range groups {
		wg.Add(1)
		go func(key sessionKey, entries []observationEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Check cache: fingerprint based on observation content + prompt hash
			var obsTexts []string
			for _, e := range entries {
				obsTexts = append(obsTexts, e.Text)
			}
			fp := Fingerprint(append(obsTexts, classifyPromptHash)...)

			existing, err := artifacts.GetClassifications(key.source, key.sessionID)
			if err == nil && existing.Fingerprint == fp {
				n := completed.Add(1)
				fmt.Fprintf(os.Stderr, "  [%d/%d] Cached classifications for %s/%s\n", n, total, key.source, key.sessionID)
				return
			}

			// Classify each observation
			var items []Classification
			var usage inference.Usage
			for _, e := range entries {
				resp, u, err := llm.Converse(ctx, prompts.Classify, e.Text, inference.WithMaxTokens(256))
				usage = usage.Add(u)
				if err != nil {
					continue
				}
				items = append(items, Classification{
					Observation:    e.Text,
					Classification: strings.TrimSpace(resp),
				})
			}

			cls := &Classifications{
				Fingerprint: fp,
				Items:       items,
			}
			artifacts.PutClassifications(key.source, key.sessionID, cls)

			n := completed.Add(1)
			fmt.Fprintf(os.Stderr, "  [%d/%d] Classified %s/%s (%d items, $%.4f)\n",
				n, total, key.source, key.sessionID, len(items), usage.Cost())

			mu.Lock()
			totalUsage = totalUsage.Add(usage)
			mu.Unlock()
		}(key, entries)
	}
	wg.Wait()

	fmt.Fprintf(os.Stderr, "Classified %d sessions (%s, $%.4f)\n",
		total, time.Since(start).Round(time.Millisecond), totalUsage.Cost())
	return totalUsage, nil
}

// ── EMBED ───────────────────────────────────────────────────────────────

// runEmbed computes embeddings for all classifications.
func runEmbed(
	ctx context.Context,
	artifacts *ArtifactStore,
	embedder embedding.Embedder,
	allObs []observationEntry,
) error {
	model := embedder.Model()
	start := time.Now()

	// Group by session
	type sessionKey struct{ source, sessionID string }
	groups := map[sessionKey]bool{}
	for _, obs := range allObs {
		groups[sessionKey{obs.Source, obs.SessionID}] = true
	}

	var completed atomic.Int32
	total := len(groups)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

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
				n := completed.Add(1)
				fmt.Fprintf(os.Stderr, "  [%d/%d] Cached embeddings for %s/%s\n", n, total, key.source, key.sessionID)
				return
			}

			// Compute embeddings
			vectors, err := embedder.Embed(ctx, clsTexts)
			if err != nil {
				n := completed.Add(1)
				fmt.Fprintf(os.Stderr, "  [%d/%d] Error embedding %s/%s: %v\n", n, total, key.source, key.sessionID, err)
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
			fmt.Fprintf(os.Stderr, "  [%d/%d] Embedded %s/%s (%d vectors)\n",
				n, total, key.source, key.sessionID, len(items))
		}(key)
	}
	wg.Wait()

	fmt.Fprintf(os.Stderr, "Embedded %d sessions (%s)\n",
		total, time.Since(start).Round(time.Millisecond))
	return nil
}

// ── GROUP ───────────────────────────────────────────────────────────────

type clusterResult struct {
	ID              int
	ObservationIdxs []int // indices into the flat allObs slice
}

// runGroup collects all embedding vectors and runs HDBSCAN clustering.
// Returns clusters (groups of observation indices) and noise observations.
func runGroup(artifacts *ArtifactStore, allObs []observationEntry) ([]clusterResult, []string, error) {
	// Collect all vectors in order matching allObs.
	// We need to match observations to their embeddings.
	type sessionKey struct{ source, sessionID string }
	embeddingsBySession := map[sessionKey]*Embeddings{}

	sessions, err := artifacts.ListEmbeddings()
	if err != nil {
		return nil, nil, err
	}
	for _, ss := range sessions {
		emb, err := artifacts.GetEmbeddings(ss.Source, ss.SessionID)
		if err != nil {
			continue
		}
		embeddingsBySession[sessionKey{ss.Source, ss.SessionID}] = emb
	}

	// Build vector list matching allObs order.
	// For observations without embeddings, we'll mark them as noise.
	var vectors [][]float64
	var vectorIdxToObs []int // maps vector index → allObs index
	var noEmbeddingObs []string

	for i, obs := range allObs {
		key := sessionKey{obs.Source, obs.SessionID}
		emb, ok := embeddingsBySession[key]
		if !ok || obs.Index >= len(emb.Items) {
			noEmbeddingObs = append(noEmbeddingObs, obs.Text)
			continue
		}
		vectors = append(vectors, emb.Items[obs.Index].Vector)
		vectorIdxToObs = append(vectorIdxToObs, i)
	}

	if len(vectors) < 3 {
		// Not enough for HDBSCAN — treat all as noise
		var noise []string
		for _, obs := range allObs {
			noise = append(noise, obs.Text)
		}
		return nil, noise, nil
	}

	// Run HDBSCAN
	distMatrix := hdbscan.CosineDistanceMatrix(vectors)
	labels := hdbscan.Cluster(distMatrix, 3)

	// Group by cluster label
	clusterMembers := map[int][]int{} // label → list of allObs indices
	var noiseObs []string

	for vi, label := range labels {
		obsIdx := vectorIdxToObs[vi]
		if label == -1 {
			noiseObs = append(noiseObs, allObs[obsIdx].Text)
		} else {
			clusterMembers[label] = append(clusterMembers[label], obsIdx)
		}
	}
	noiseObs = append(noiseObs, noEmbeddingObs...)

	// Build cluster results
	var clusters []clusterResult
	for label, members := range clusterMembers {
		clusters = append(clusters, clusterResult{
			ID:              label,
			ObservationIdxs: members,
		})
	}
	// Sort by cluster ID for determinism
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
) ([]string, inference.Usage, error) {
	summaries := make([]string, len(samples))
	errs := make([]error, len(samples))
	usages := make([]inference.Usage, len(samples))

	start := time.Now()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

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

			resp, usage, err := llm.Converse(ctx, prompts.Synthesize, input, inference.WithMaxTokens(4096))
			summaries[i] = strings.TrimSpace(resp)
			usages[i] = usage
			errs[i] = err
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

	fmt.Fprintf(os.Stderr, "Synthesized %d clusters (%s, $%.4f)\n",
		len(samples), time.Since(start).Round(time.Millisecond), totalUsage.Cost())
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

	start := time.Now()
	muse, usage, err := llm.Converse(ctx, prompts.Merge, input.String(), inference.WithThinking(16000))
	if err != nil {
		return "", "", usage, err
	}
	muse = stripCodeFences(muse)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	if err := store.PutMuse(ctx, timestamp, muse); err != nil {
		return "", "", usage, fmt.Errorf("failed to write muse: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Merged into muse.md (%s, $%.4f)\n",
		time.Since(start).Round(time.Millisecond), usage.Cost())
	return muse, timestamp, usage, nil
}
