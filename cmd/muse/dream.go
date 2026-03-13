package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/dream"
	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/storage"
)

func newDreamCmd() *cobra.Command {
	var reflect bool
	var learn bool
	var limit int
	cmd := &cobra.Command{
		Use:   "dream",
		Short: "Distill a soul from memories",
		Long: `Processes your uploaded memories and distills them into a soul document that
captures how you think. Each dream snapshots the previous soul before
overwriting it.

Use --learn to re-distill the soul from existing reflections without
reprocessing memories. Use --reflect to reprocess all memories from scratch.`,
		Example: `  muse dream              # reflect on new memories and distill soul
  muse dream --learn      # re-distill soul from existing reflections
  muse dream --reflect    # re-reflect on all memories from scratch
  muse dream --limit 50   # process at most 50 memories`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
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
	return nil
}
