package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/distill"
	"github.com/ellistarn/muse/internal/embedding"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
)

func newDistillCmd() *cobra.Command {
	var reobserve bool
	var reclassify bool
	var learn bool
	var limit int
	var method string
	cmd := &cobra.Command{
		Use:   "distill [source...]",
		Short: "Distill a muse from conversations",
		Long: `Discovers new conversations, observes them, and distills a muse.md
that captures how you think. Safe to run repeatedly — only new
conversations are discovered and only unobserved conversations are processed. The
muse is always re-distilled.

Two distillation methods are available:

  clustering (default) — classifies observations, embeds them, clusters with
  HDBSCAN, synthesizes per-cluster, then merges into muse.md. Produces
  thematically coherent output.

  map-reduce — observe maps each conversation into observations, then learn
  reduces all observations into a single muse.md. Simpler, sufficient for
  smaller observation sets.

Optionally pass one or more source names (kiro, kiro-cli, claude-code, opencode) to limit
discovery and observation to those sources.

Use --learn to re-distill the muse from existing observations without
reprocessing conversations. Use --reobserve to reprocess conversations from scratch.`,
		Example: `  muse distill                          # default: clustering
  muse distill --method=map-reduce      # simpler pipeline
  muse distill kiro                     # only kiro conversations
  muse distill kiro opencode            # kiro and opencode
  muse distill kiro --reobserve         # re-observe kiro from scratch
  muse distill --learn                  # re-distill muse from existing observations
  muse distill --limit 50              # process at most 50 conversations
  muse distill --reclassify             # force re-classify all observations`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sources := args

			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			// Discover and store new conversations (skip for --learn since it
			// only re-distills from existing observations)
			if !learn {
				m, err := muse.New(ctx, store)
				if err != nil {
					return err
				}
				result, err := m.Upload(ctx, sources...)
				if err != nil {
					return err
				}
				for _, w := range result.Warnings {
					fmt.Fprintf(os.Stderr, "warning: %s\n", w)
				}
				if result.Uploaded > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d new conversations (%s)\n", result.Uploaded, muse.FormatBytes(result.Bytes))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "No new conversations\n")
				}
			}

			switch method {
			case "clustering":
				return runClusteredDistill(ctx, cmd.OutOrStdout(), store, sources, reobserve, reclassify, limit)
			case "map-reduce":
				return runMapReduceDistill(ctx, cmd.OutOrStdout(), store, sources, reobserve, learn, limit)
			default:
				return fmt.Errorf("unknown method %q (use 'clustering' or 'map-reduce')", method)
			}
		},
	}
	cmd.Flags().BoolVar(&reobserve, "reobserve", false, "re-observe all conversations from scratch")
	cmd.Flags().BoolVar(&reclassify, "reclassify", false, "force re-classify all observations")
	cmd.Flags().BoolVar(&learn, "learn", false, "skip observe, re-distill muse from existing observations (map-reduce only)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max conversations to process (0 = no limit)")
	cmd.Flags().StringVar(&method, "method", "clustering", "distillation method: clustering or map-reduce")
	return cmd
}

func runClusteredDistill(ctx context.Context, stdout io.Writer, store storage.Store, sources []string, reobserve, reclassify bool, limit int) error {
	sonnet, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
	if err != nil {
		return fmt.Errorf("sonnet client: %w", err)
	}
	opus, err := bedrock.NewClient(ctx, bedrock.ModelOpus)
	if err != nil {
		return fmt.Errorf("opus client: %w", err)
	}
	embedClient, err := embedding.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("embedding client: %w", err)
	}

	// Determine artifact directory from store root
	artifactDir := artifactDirFromStore(store)

	result, err := distill.RunClustered(ctx, store,
		sonnet, // observe
		sonnet, // classify
		sonnet, // synthesize
		opus,   // merge
		embedClient,
		distill.ClusteredOptions{
			Reobserve:   reobserve,
			Reclassify:  reclassify,
			Limit:       limit,
			Sources:     sources,
			ArtifactDir: artifactDir,
		},
	)
	if err != nil {
		return err
	}

	return printResult(stdout, result, false)
}

func runMapReduceDistill(ctx context.Context, stdout io.Writer, store storage.Store, sources []string, reobserve, learn bool, limit int) error {
	opts := distill.Options{
		Reobserve: reobserve,
		Learn:     learn,
		Limit:     limit,
		Sources:   sources,
	}

	if learn {
		learnClient, err := bedrock.NewClient(ctx, bedrock.ModelOpus)
		if err != nil {
			return err
		}
		diffClient, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
		if err != nil {
			return err
		}
		opts.Learn = true
		result, err := distill.LearnOnly(ctx, store, learnClient, diffClient)
		if err != nil {
			return err
		}
		return printResult(stdout, result, true)
	}

	observeClient, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
	if err != nil {
		return err
	}
	learnClient, err := bedrock.NewClient(ctx, bedrock.ModelOpus)
	if err != nil {
		return err
	}
	result, err := distill.Run(ctx, store, observeClient, learnClient, opts)
	if err != nil {
		return err
	}
	return printResult(stdout, result, false)
}

func printResult(stdout io.Writer, result *distill.Result, learnOnly bool) error {
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if !learnOnly {
		fmt.Fprintf(stdout, "Processed %d conversations (%d pruned)\n", result.Processed, result.Pruned)
		if result.Remaining > 0 {
			fmt.Fprintf(stdout, "%d conversations still pending observation (run distill again)\n", result.Remaining)
		}
	}
	fmt.Fprintf(stdout, "Muse distilled (%dk input, %dk output tokens, $%.2f)\n",
		result.Usage.InputTokens/1000, result.Usage.OutputTokens/1000, result.Usage.Cost())
	if result.Muse != "" {
		fmt.Fprintf(stdout, "muse.md: ~%d tokens\n", inference.EstimateTokens(result.Muse))
	}
	if result.Diff != "" {
		fmt.Fprintf(stdout, "\n%s\n", result.Diff)
	}
	return nil
}

// artifactDirFromStore extracts the root directory from a store for artifact storage.
func artifactDirFromStore(store storage.Store) string {
	if ls, ok := store.(*storage.LocalStore); ok {
		return ls.Root()
	}
	// Fallback to ~/.muse for S3 stores (artifacts are local cache)
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".muse")
}

// runDistill executes the map-reduce distill pipeline and prints results.
// Preserved for backward compatibility with existing tests.
func runDistill(ctx context.Context, stdout, stderr io.Writer, store storage.Store, observeLLM, learnLLM, diffLLM distill.LLM, opts distill.Options) error {
	var (
		result *distill.Result
		err    error
	)
	if opts.Learn {
		result, err = distill.LearnOnly(ctx, store, learnLLM, diffLLM)
	} else {
		result, err = distill.Run(ctx, store, observeLLM, learnLLM, opts)
	}
	if err != nil {
		return err
	}
	return printResult(stdout, result, opts.Learn)
}
