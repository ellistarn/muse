package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/bedrock"
	"github.com/ellistarn/shade/internal/dream"
	"github.com/ellistarn/shade/internal/log"
	"github.com/ellistarn/shade/internal/storage"
)

func newDreamCmd() *cobra.Command {
	var reprocess bool
	var relearn bool
	var limit int
	cmd := &cobra.Command{
		Use:   "dream",
		Short: "Distill skills from memories",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			store, err := storage.NewClient(ctx, bucket)
			if err != nil {
				return err
			}
			llm, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
			if err != nil {
				return err
			}
			log.Printf("Using model %s\n", llm.Model())
			var result *dream.Result
			if relearn {
				result, err = dream.Relearn(ctx, store, llm)
			} else {
				result, err = dream.Run(ctx, store, llm, dream.Options{Reprocess: reprocess, Limit: limit})
			}
			if err != nil {
				return err
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			if !relearn {
				fmt.Fprintf(cmd.OutOrStdout(), "Processed %d memories (%d pruned)\n", result.Processed, result.Pruned)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Produced %d skills (%dk input, %dk output tokens, $%.2f)\n",
				result.Skills, result.Usage.InputTokens/1000, result.Usage.OutputTokens/1000, result.Usage.Cost())
			return nil
		},
	}
	cmd.Flags().BoolVar(&reprocess, "reprocess", false, "re-reflect on all memories from scratch")
	cmd.Flags().BoolVar(&relearn, "relearn", false, "skip reflect, re-synthesize skills from existing reflections")
	cmd.Flags().IntVar(&limit, "limit", 100, "max memories to process (0 = no limit)")
	return cmd
}
