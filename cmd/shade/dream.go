package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/bedrock"
	"github.com/ellistarn/shade/internal/dream"
	"github.com/ellistarn/shade/internal/storage"
)

func newDreamCmd() *cobra.Command {
	var reprocess bool
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
			llm, err := bedrock.NewClient(ctx)
			if err != nil {
				return err
			}
			result, err := dream.Run(ctx, store, llm, dream.Options{Reprocess: reprocess, Limit: limit})
			if err != nil {
				return err
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			totalUsage := bedrock.Usage{InputTokens: result.InputTokens, OutputTokens: result.OutputTokens}
			fmt.Fprintf(cmd.OutOrStdout(), "Processed %d memories (%d pruned)\n", result.Processed, result.Pruned)
			fmt.Fprintf(cmd.OutOrStdout(), "Produced %d skills (%dk input, %dk output tokens, $%.2f)\n",
				result.Skills, result.InputTokens/1000, result.OutputTokens/1000, totalUsage.Cost())
			return nil
		},
	}
	cmd.Flags().BoolVar(&reprocess, "reprocess", false, "ignore saved state and reprocess all memories")
	cmd.Flags().IntVar(&limit, "limit", 100, "max memories to process (0 = no limit)")
	return cmd
}
