package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/shade"
)

func newUploadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upload",
		Short: "Sync memories to storage",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := shade.New(ctx, bucket)
			if err != nil {
				return err
			}
			defer s.Close()
			result, err := s.Upload(ctx)
			if err != nil {
				return err
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Found %d local sessions\n", result.Total)
			fmt.Fprintf(cmd.OutOrStdout(), "Uploaded %d sessions (%d unchanged, %s transferred)\n", result.Uploaded, result.Skipped, shade.FormatBytes(result.Bytes))
			return nil
		},
	}
}
