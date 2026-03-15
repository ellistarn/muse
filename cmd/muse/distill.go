package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/distill"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
)

func newDistillCmd() *cobra.Command {
	var reflect bool
	var learn bool
	var limit int
	cmd := &cobra.Command{
		Use:   "distill",
		Short: "Distill a muse from memories",
		Long: `Discovers new memories, reflects on them, and distills a muse.md
that captures how you think. Safe to run repeatedly — only new
memories are discovered and only unreflected memories are processed. The
muse is always re-distilled.

The pipeline is a map-reduce: reflect maps each memory into observations,
then learn reduces all observations into a single muse.md.

Use --learn to re-distill the muse from existing reflections without
reprocessing memories. Use --reflect to reprocess all memories from scratch.`,
		Example: `  muse distill              # discover memories, reflect, and distill muse
  muse distill --learn      # re-distill muse from existing reflections
  muse distill --reflect    # re-reflect on all memories from scratch
  muse distill --limit 50   # process at most 50 memories`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			// Discover and store new memories (skip for --learn since it
			// only re-distills from existing reflections)
			if !learn {
				m, err := muse.New(ctx, store)
				if err != nil {
					return err
				}
				result, err := m.Upload(ctx)
				if err != nil {
					return err
				}
				for _, w := range result.Warnings {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
				}
				if result.Uploaded > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d new memories (%s)\n", result.Uploaded, muse.FormatBytes(result.Bytes))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "No new memories\n")
				}
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
				log.Printf("Learning with %s\n", learnClient.Model())
				return runDistill(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, nil, learnClient, diffClient, true, false, 0)
			}
			reflectClient, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
			if err != nil {
				return err
			}
			learnClient, err := bedrock.NewClient(ctx, bedrock.ModelOpus)
			if err != nil {
				return err
			}
			log.Printf("Reflecting with %s, learning with %s\n", reflectClient.Model(), learnClient.Model())
			return runDistill(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, reflectClient, learnClient, nil, false, reflect, limit)
		},
	}
	cmd.Flags().BoolVar(&reflect, "reflect", false, "re-reflect on all memories from scratch")
	cmd.Flags().BoolVar(&learn, "learn", false, "skip reflect, re-distill muse from existing reflections")
	cmd.Flags().IntVar(&limit, "limit", 100, "max memories to process (0 = no limit)")
	return cmd
}

// runDistill executes the distill pipeline and prints results. Extracted from the
// command handler so it can be tested with mock dependencies.
func runDistill(ctx context.Context, stdout, stderr io.Writer, store storage.Store, reflectLLM, learnLLM, diffLLM distill.LLM, learn, reflect bool, limit int) error {
	var (
		result *distill.Result
		err    error
	)
	if learn {
		result, err = distill.LearnOnly(ctx, store, learnLLM, diffLLM)
	} else {
		result, err = distill.Run(ctx, store, reflectLLM, learnLLM, distill.Options{Reflect: reflect, Limit: limit})
	}
	if err != nil {
		return err
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	if !learn {
		fmt.Fprintf(stdout, "Processed %d memories (%d pruned)\n", result.Processed, result.Pruned)
		if result.Remaining > 0 {
			fmt.Fprintf(stdout, "%d memories still pending reflection (run distill again)\n", result.Remaining)
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
