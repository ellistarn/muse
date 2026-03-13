package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var bucket string

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "muse",
		Short: "The distilled essence of how you think",
		Long: `A muse absorbs your memories from agent interactions, distills them into a
soul document, and approximates your unique thought processes when asked questions.

Workflow:

  1. muse push       Collect local agent sessions and upload them to storage
  2. muse dream      Reflect on memories and distill a soul document
  3. muse inspect    View your soul or see what changed since the last dream
  4. muse ask        Ask your muse a question (stateless, one-shot)
  5. muse listen     Start an MCP server so agents can ask your muse

Getting started:

  export MUSE_BUCKET=$USER-muse    # S3 bucket for storage
  export MUSE_MODEL=<model-id>     # model override (optional)
  muse push && muse dream && muse inspect

To wire up your muse as an MCP server for agents:

  {
    "mcpServers": {
      "<your-name>": {
        "command": "muse",
        "args": ["listen"]
      }
    }
  }

See https://github.com/ellistarn/muse for full documentation.`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.PersistentFlags().StringVar(&bucket, "bucket", os.Getenv("MUSE_BUCKET"), "S3 bucket name (or set MUSE_BUCKET)")
	cmd.AddCommand(newPushCmd())
	cmd.AddCommand(newDreamCmd())
	cmd.AddCommand(newInspectCmd())
	cmd.AddCommand(newListenCmd())
	cmd.AddCommand(newAskCmd())
	return cmd
}

func requireBucket() error {
	if bucket == "" {
		return fmt.Errorf("bucket is required: use --bucket or set MUSE_BUCKET")
	}
	return nil
}
