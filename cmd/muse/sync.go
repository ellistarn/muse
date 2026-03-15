package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/storage"
)

var validCategories = map[string]bool{
	"conversations": true,
	"reflections":   true,
	"muse":          true,
}

func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync <src> <dst> [category...]",
		Short: "Sync data between local disk and S3",
		Long: `Copies muse data between your local filesystem (~/.muse/) and S3.
Additive only — existing data at the destination is never modified or
deleted. Items already present are skipped.

The typical workflow is pull from S3 on a new machine, push to S3 to back up.
By default all data is synced. You can limit to a category (conversations,
reflections, muse) but you rarely need to.`,
		Example: `  muse sync s3 local                  # pull from S3 to local
  muse sync local s3                  # push from local to S3
  muse sync s3 local conversations    # pull only conversations`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			srcName, dstName := args[0], args[1]
			categories := args[2:]

			for _, c := range categories {
				if !validCategories[c] {
					return fmt.Errorf("unknown category %q (valid: conversations, reflections, muse)", c)
				}
			}

			src, err := resolveEndpoint(ctx, srcName)
			if err != nil {
				return fmt.Errorf("source %q: %w", srcName, err)
			}
			dst, err := resolveEndpoint(ctx, dstName)
			if err != nil {
				return fmt.Errorf("destination %q: %w", dstName, err)
			}

			return storage.Sync(ctx, src, dst, categories, os.Stdout)
		},
	}
	return cmd
}

func resolveEndpoint(ctx context.Context, name string) (storage.Store, error) {
	switch name {
	case "s3":
		if bucket == "" {
			return nil, fmt.Errorf("MUSE_BUCKET must be set to use s3 endpoint")
		}
		return storage.NewS3Store(ctx, bucket)
	case "local":
		return storage.NewLocalStore()
	default:
		return nil, fmt.Errorf("unknown endpoint %q (valid: s3, local)", name)
	}
}
