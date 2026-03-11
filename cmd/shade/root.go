package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var bucket string

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "shade",
		Short:         "A projection of how you work",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.PersistentFlags().StringVar(&bucket, "bucket", os.Getenv("SHADE_BUCKET"), "S3 bucket name (or set SHADE_BUCKET)")
	cmd.AddCommand(newUploadCmd())
	cmd.AddCommand(newDreamCmd())
	cmd.AddCommand(newLsCmd())
	cmd.AddCommand(newShowCmd())
	cmd.AddCommand(newListenCmd())
	cmd.AddCommand(newAskCmd())
	return cmd
}

func requireBucket() error {
	if bucket == "" {
		return fmt.Errorf("bucket is required: use --bucket or set SHADE_BUCKET")
	}
	return nil
}
