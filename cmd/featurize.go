package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/spf13/cobra"
)

func newFeaturizeCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "featurize",
		Short: "Build a feature dataset from observation strategies",
		Long: `Runs both default (windowed-with-assistant) and woo (windowed-owner-only)
observation on each window of each conversation. Writes a JSON array of
per-window records with input features and observation counts from each
strategy. Use the output to find the threshold for when to use which method.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFeaturize(cmd.Context(), limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "max conversations to process (0 = no limit)")
	return cmd
}

func runFeaturize(ctx context.Context, limit int) error {
	store, err := newStore(ctx)
	if err != nil {
		return err
	}

	entries, err := store.ListConversations(ctx)
	if err != nil {
		return fmt.Errorf("list conversations: %w", err)
	}

	// Sort largest first
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].SizeBytes > entries[j].SizeBytes
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	llm, err := newLLMClient(ctx, TierFast)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "featurize: %d conversations\n", len(entries))

	var mu sync.Mutex
	var allFeatures []compose.WindowFeatures
	var counter atomic.Int32

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(10) // lower concurrency — 2 LLM calls per window

	for _, entry := range entries {
		g.Go(func() error {
			conv, err := store.GetConversation(ctx, entry.Source, entry.ConversationID)
			if err != nil {
				counter.Add(1)
				return fmt.Errorf("load %s: %w", entry.Key, err)
			}

			features, _, err := compose.FeaturizeConversation(ctx, llm, conv, verbose)
			n := counter.Add(1)
			if err != nil {
				return fmt.Errorf("featurize %s: %w", entry.Key, err)
			}

			mu.Lock()
			allFeatures = append(allFeatures, features...)
			mu.Unlock()

			windows := len(features)
			var defTotal, wooTotal int
			for _, f := range features {
				defTotal += f.DefaultObs
				wooTotal += f.WooObs
			}
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s: %d windows, default:%d woo:%d\n",
				n, len(entries), entry.Key, windows, defTotal, wooTotal)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// Write JSON to stdout
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(allFeatures)
}

func registerFeaturize(entries []storage.ConversationEntry) {
	_ = entries // satisfy linter if needed
}
