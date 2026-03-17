package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/distill"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/runlog"
	"github.com/ellistarn/muse/internal/storage"
)

func newDistillCmd() *cobra.Command {
	var reobserve bool
	var learn bool
	var limit int
	cmd := &cobra.Command{
		Use:   "distill [source...]",
		Short: "Distill a muse from conversations",
		Long: `Discovers new conversations, observes them, and distills a muse.md
that captures how you think. Safe to run repeatedly — only new
conversations are discovered and only unobserved conversations are processed. The
muse is always re-distilled.

The pipeline is a map-reduce: observe maps each conversation into observations,
then learn reduces all observations into a single muse.md.

Optionally pass one or more source names (kiro, kiro-cli, claude-code, opencode) to limit
discovery and observation to those sources. The learn phase always uses all observations.

Use --learn to re-distill the muse from existing observations without
reprocessing conversations. Use --reobserve to reprocess conversations from scratch.`,
		Example: `  muse distill                        # all sources
  muse distill kiro                   # only kiro conversations
  muse distill kiro opencode          # kiro and opencode
  muse distill kiro --reobserve       # re-observe kiro from scratch
  muse distill --learn                # re-distill muse from existing observations
  muse distill --limit 50             # process at most 50 conversations`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sources := args

			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			var discovered int
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
				discovered = result.Uploaded
				if result.Uploaded > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d new conversations (%s)\n", result.Uploaded, muse.FormatBytes(result.Bytes))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "No new conversations\n")
				}
			}

			opts := distill.Options{
				Reobserve: reobserve,
				Limit:     limit,
				Sources:   sources,
			}

			var result *distill.Result
			if learn {
				learnClient, cerr := bedrock.NewClient(ctx, bedrock.ModelOpus)
				if cerr != nil {
					return cerr
				}
				diffClient, cerr := bedrock.NewClient(ctx, bedrock.ModelSonnet)
				if cerr != nil {
					return cerr
				}
				opts.Learn = true
				result, err = runDistill(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, nil, learnClient, diffClient, opts)
			} else {
				observeClient, err2 := bedrock.NewClient(ctx, bedrock.ModelSonnet)
				if err2 != nil {
					return err2
				}
				learnClient, err2 := bedrock.NewClient(ctx, bedrock.ModelOpus)
				if err2 != nil {
					return err2
				}
				result, err = runDistill(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, observeClient, learnClient, nil, opts)
			}
			if err != nil {
				return err
			}

			// Write run record (best-effort, don't fail the command)
			rl := newRunLog()
			if err := rl.Log(runlog.Record{
				Timestamp:    time.Now().UTC(),
				Command:      "distill",
				InputTokens:  result.Usage.InputTokens,
				OutputTokens: result.Usage.OutputTokens,
				Cost:         result.Usage.Cost(),
				Discovered:   discovered,
				Observed:     result.Processed,
				Pruned:       result.Pruned,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to record usage: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reobserve, "reobserve", false, "re-observe all conversations from scratch")
	cmd.Flags().BoolVar(&learn, "learn", false, "skip observe, re-distill muse from existing observations")
	cmd.Flags().IntVar(&limit, "limit", 100, "max conversations to process (0 = no limit)")
	return cmd
}

// runDistill executes the distill pipeline and prints results. Extracted from the
// command handler so it can be tested with mock dependencies.
func runDistill(ctx context.Context, stdout, stderr io.Writer, store storage.Store, observeLLM, learnLLM, diffLLM distill.LLM, opts distill.Options) (*distill.Result, error) {
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
		return nil, err
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if !opts.Learn {
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
	return result, nil
}
