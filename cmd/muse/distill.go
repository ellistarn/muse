package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/distill"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
)

func newDistillCmd() *cobra.Command {
	var reflect bool
	var learn bool
	var limit int
	cmd := &cobra.Command{
		Use:   "distill [source...]",
		Short: "Distill a muse from conversations",
		Long: `Discovers new conversations, reflects on them, and distills a muse.md
that captures how you think. Safe to run repeatedly — only new
conversations are discovered and only unreflected conversations are processed. The
muse is always re-distilled.

The pipeline is a map-reduce: reflect maps each conversation into observations,
then learn reduces all observations into a single muse.md.

Optionally pass one or more source names (kiro, kiro-cli, claude-code, opencode) to limit
discovery and reflection to those sources. The learn phase always uses all reflections.

Use --learn to re-distill the muse from existing reflections without
reprocessing conversations. Use --reflect to reprocess conversations from scratch.`,
		Example: `  muse distill                        # all sources
  muse distill kiro                   # only kiro conversations
  muse distill kiro opencode          # kiro and opencode
  muse distill kiro --reflect         # re-reflect kiro from scratch
  muse distill --learn                # re-distill muse from existing reflections
  muse distill --limit 50             # process at most 50 conversations`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sources := args

			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			// Discover and store new conversations (skip for --learn since it
			// only re-distills from existing reflections)
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

			opts := distill.Options{
				Reflect: reflect,
				Limit:   limit,
				Sources: sources,
			}

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
				return runDistill(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, nil, learnClient, diffClient, opts)
			}
			reflectClient, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
			if err != nil {
				return err
			}
			learnClient, err := bedrock.NewClient(ctx, bedrock.ModelOpus)
			if err != nil {
				return err
			}
			return runDistill(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, reflectClient, learnClient, nil, opts)
		},
	}
	cmd.Flags().BoolVar(&reflect, "reflect", false, "re-reflect on all conversations from scratch")
	cmd.Flags().BoolVar(&learn, "learn", false, "skip reflect, re-distill muse from existing reflections")
	cmd.Flags().IntVar(&limit, "limit", 100, "max conversations to process (0 = no limit)")
	return cmd
}

// runDistill executes the distill pipeline and prints results. Extracted from the
// command handler so it can be tested with mock dependencies.
func runDistill(ctx context.Context, stdout, stderr io.Writer, store storage.Store, reflectLLM, learnLLM, diffLLM distill.LLM, opts distill.Options) error {
	var (
		result *distill.Result
		err    error
	)
	if opts.Learn {
		result, err = distill.LearnOnly(ctx, store, learnLLM, diffLLM)
	} else {
		result, err = distill.Run(ctx, store, reflectLLM, learnLLM, opts)
	}
	if err != nil {
		return err
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if !opts.Learn {
		fmt.Fprintf(stdout, "Processed %d conversations (%d pruned)\n", result.Processed, result.Pruned)
		if result.Remaining > 0 {
			fmt.Fprintf(stdout, "%d conversations still pending reflection (run distill again)\n", result.Remaining)
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
