package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/muse"
)

func newPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Push memories to storage",
		Long: `Finds local agent sessions (OpenCode, Claude Code, Kiro, etc.) and uploads
them to your S3 bucket. Uploads are incremental — sessions already in storage
are skipped. Run this before dreaming to give your muse new material.`,
		Example: `  muse push`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			m, err := muse.New(ctx, bucket)
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
			fmt.Fprintf(cmd.OutOrStdout(), "Found %d local sessions\n", result.Total)
			if result.Uploaded > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Uploaded %d sessions (%s), %d unchanged\n", result.Uploaded, muse.FormatBytes(result.Bytes), result.Skipped)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "All %d sessions unchanged\n", result.Skipped)
			}
			return nil
		},
	}
}
