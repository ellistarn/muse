package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/dream"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
)

func newDreamCmd() *cobra.Command {
	var reflect bool
	var learn bool
	var limit int
	cmd := &cobra.Command{
		Use:   "dream",
		Short: "Distill a soul from memories",
		Long: `Discovers new memories, reflects on them, and distills a soul document
(soul.md) that captures how you think. Safe to run repeatedly — only new
memories are discovered and only unreflected memories are processed. The
soul is always re-distilled.

The pipeline is a map-reduce: reflect maps each memory into observations,
then learn reduces all observations into a single soul document.

Use --learn to re-distill the soul from existing reflections without
reprocessing memories. Use --reflect to reprocess all memories from scratch.`,
		Example: `  muse dream              # discover memories, reflect, and distill soul
  muse dream --learn      # re-distill soul from existing reflections
  muse dream --reflect    # re-reflect on all memories from scratch
  muse dream --limit 50   # process at most 50 memories`,
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
				client, cerr := bedrock.NewClient(ctx, bedrock.ModelOpus)
				if cerr != nil {
					return cerr
				}
				log.Printf("Learning with %s\n", client.Model())
				return runDream(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, nil, client, true, false, 0)
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
			return runDream(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, reflectClient, learnClient, false, reflect, limit)
		},
	}
	cmd.Flags().BoolVar(&reflect, "reflect", false, "re-reflect on all memories from scratch")
	cmd.Flags().BoolVar(&learn, "learn", false, "skip reflect, re-distill soul from existing reflections")
	cmd.Flags().IntVar(&limit, "limit", 100, "max memories to process (0 = no limit)")
	return cmd
}

// runDream executes the dream pipeline and prints results. Extracted from the
// command handler so it can be tested with mock dependencies.
func runDream(ctx context.Context, stdout, stderr io.Writer, store storage.Store, reflectLLM, learnLLM dream.LLM, learn, reflect bool, limit int) error {
	var (
		result *dream.Result
		err    error
	)
	if learn {
		result, err = dream.LearnOnly(ctx, store, learnLLM)
	} else {
		result, err = dream.Run(ctx, store, reflectLLM, learnLLM, dream.Options{Reflect: reflect, Limit: limit})
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
			fmt.Fprintf(stdout, "%d memories still pending reflection (run dream again)\n", result.Remaining)
		}
	}
	fmt.Fprintf(stdout, "Soul distilled (%dk input, %dk output tokens, $%.2f)\n",
		result.Usage.InputTokens/1000, result.Usage.OutputTokens/1000, result.Usage.Cost())
	if result.Soul != "" {
		fmt.Fprintf(stdout, "soul.md: ~%d tokens\n", inference.EstimateTokens(result.Soul))
	}
	return nil
}
