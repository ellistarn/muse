package compose

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

// IncrementalOptions configures an incremental compose run.
type IncrementalOptions struct {
	BaseOptions
}

// RunIncremental executes the incremental composition pipeline: observe new
// conversations, then fold only the new observations into the existing muse
// via a single Opus call. On first run (no existing muse), bootstraps from
// all available observations.
func RunIncremental(ctx context.Context, store storage.Store, observeLLM, composeLLM LLM, opts IncrementalOptions) (*Result, error) {
	entries, err := store.ListConversations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}

	observations, err := store.ListObservations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list observations: %w", err)
	}

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
		observations, err = store.ListObservations(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to re-list observations: %w", err)
		}
	}

	if len(opts.Sources) > 0 {
		allowed := make(map[string]bool, len(opts.Sources))
		for _, s := range opts.Sources {
			allowed[s] = true
		}
		var filtered []storage.ConversationEntry
		for _, e := range entries {
			if allowed[e.Source] {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	var pending []storage.ConversationEntry
	var pruned int
	for _, e := range entries {
		if observed, ok := observations[e.Key]; ok && !e.LastModified.After(observed) {
			pruned++
			continue
		}
		pending = append(pending, e)
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].LastModified.After(pending[j].LastModified)
	})
	totalPending := len(pending)
	if opts.Limit > 0 && len(pending) > opts.Limit {
		pending = pending[:opts.Limit]
	}

	pendingKeys := make(map[string]bool, len(pending))
	for _, e := range pending {
		pendingKeys[e.Key] = true
	}

	var mu sync.Mutex
	var firstErr error
	var observeUsage inference.Usage

	if len(pending) > 0 {
		observeStart := time.Now()
		var completed atomic.Int32
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, entry := range pending {
			wg.Add(1)
			go func(entry storage.ConversationEntry) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				conv, err := store.GetConversation(ctx, entry.Source, entry.ConversationID)
				if err != nil {
					completed.Add(1)
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("load conversation %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}
				start := time.Now()
				obs, usage, err := observeConversation(ctx, observeLLM, conv)
				n := completed.Add(1)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("observe %s: %w", entry.Key, err)
					}
					mu.Unlock()
					return
				}

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

	currentMuse, err := store.GetMuse(ctx)
	if err != nil {
		if !storage.IsNotFound(err) {
			return nil, fmt.Errorf("failed to load current muse: %w", err)
		}
		currentMuse = ""
	}

	var composeObservations []string
	if currentMuse == "" {
		allObs, err := loadAllObservations(ctx, store)
		if err != nil {
			return nil, fmt.Errorf("failed to load observations: %w", err)
		}
		composeObservations = allObs
	} else {
		for key := range pendingKeys {
			content, err := store.GetObservation(ctx, key)
			if err != nil {
				continue
			}
			if content != "" {
				composeObservations = append(composeObservations, content)
			}
		}
	}

	if len(composeObservations) == 0 {
		return &Result{
			Processed: len(pending),
			Pruned:    pruned,
			Remaining: remaining,
			Usage:     observeUsage,
		}, nil
	}

	observationsText := strings.Join(composeObservations, "\n\n---\n\n")
	input := fmt.Sprintf("Current muse:\n%s\n\n---\n\nNew observations:\n%s", currentMuse, observationsText)

	composeStart := time.Now()
	stream := newStageStream(16000, 4096)
	muse, composeUsage, err := inference.ConverseStream(ctx, composeLLM, prompts.ComposeIncremental, input, stream.callback(), inference.WithThinking(16000))
	stream.finish()
	if err != nil {
		return nil, fmt.Errorf("compose failed: %w", err)
	}
	muse = stripCodeFences(muse)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	if err := store.PutMuse(ctx, timestamp, muse); err != nil {
		return nil, fmt.Errorf("failed to write muse: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Muse updated (%s, $%.4f)\n", time.Since(composeStart).Round(time.Millisecond), composeUsage.Cost())

	return &Result{
		Processed: len(pending),
		Pruned:    pruned,
		Remaining: remaining,
		Usage:     observeUsage.Add(composeUsage),
		Muse:      muse,
	}, nil
}
