package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/bedrock"
	"github.com/ellistarn/shade/internal/dream"
	"github.com/ellistarn/shade/internal/storage"
)

func newDreamCmd() *cobra.Command {
	return &cobra.Command{
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
			result, err := dream.Run(ctx, store, llm)
			if err != nil {
				return err
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Processed %d memories (%d pruned)\n", result.Processed, result.Pruned)
			fmt.Fprintf(cmd.OutOrStdout(), "Produced %d skills\n", result.Skills)
			return nil
		},
	}
}
