package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/storage"
)

var bucket string

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "muse",
		Short: "The distilled essence of how you think",
		Long: `A muse absorbs your memories from agent interactions, distills them into a
soul document, and embodies your unique thought processes when asked questions.

Workflow:

  1. muse load       Load memories from agent sessions into storage
  2. muse dream      Reflect on memories and distill a soul document
  3. muse ask        Ask your muse a question (stateless, one-shot)
  4. muse listen     Start an MCP server so agents can ask your muse

Other commands:

  muse inspect       View your soul or see what changed since the last dream

Getting started:

  muse load && muse dream && muse inspect

Data is stored locally at ~/.muse/ by default. Set MUSE_BUCKET to use S3 instead.

Run "muse listen --help" for MCP server configuration.`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.PersistentFlags().StringVar(&bucket, "bucket", os.Getenv("MUSE_BUCKET"), "S3 bucket name (or set MUSE_BUCKET)")
	cmd.AddCommand(newLoadCmd())
	cmd.AddCommand(newDreamCmd())
	cmd.AddCommand(newInspectCmd())
	cmd.AddCommand(newListenCmd())
	cmd.AddCommand(newAskCmd())
	return cmd
}

// newStore returns an S3-backed store when a bucket is configured,
// otherwise a local filesystem store rooted at ~/.muse/.
func newStore(ctx context.Context) (storage.Store, error) {
	if bucket != "" {
		fmt.Fprintf(os.Stderr, "Using S3 storage (bucket: %s)\n", bucket)
		return storage.NewS3Store(ctx, bucket)
	}
	store, err := storage.NewLocalStore()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "Using local storage at %s\n", store.Root())
	return store, nil
}
