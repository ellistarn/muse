package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/anthropic"
	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/inference"
	museOpenAI "github.com/ellistarn/muse/internal/openai"
	"github.com/ellistarn/muse/internal/storage"
)

var bucket string
var verbose bool

// Execute runs the root command.
func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "muse",
		Short: "The composed essence of how you think",
		Long: `A muse absorbs your conversations from agent interactions, composes them into
muse.md, and embodies your unique thought processes when asked questions.

Workflow:

  1. muse compose    Discover conversations, observe, and compose muse.md
  2. muse show       Print muse.md
  3. muse ask        Ask your muse a question (stateless, one-shot)
  4. muse listen     Start an MCP server so agents can ask your muse

Getting started:

  muse compose && muse show

Data is stored locally at ~/.muse/ by default. Set MUSE_BUCKET to use S3 instead.

Provider is auto-detected from API keys (ANTHROPIC_API_KEY, OPENAI_API_KEY) or
falls back to Bedrock. Override with MUSE_PROVIDER=anthropic|openai|bedrock.

Run "muse listen --help" for MCP server configuration.`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.PersistentFlags().StringVar(&bucket, "bucket", os.Getenv("MUSE_BUCKET"), "S3 bucket name (or set MUSE_BUCKET)")
	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "show per-item progress during pipeline stages")
	cmd.AddCommand(newComposeCmd())
	cmd.AddCommand(newShowCmd())
	cmd.AddCommand(newListenCmd())
	cmd.AddCommand(newAskCmd())
	cmd.AddCommand(newSyncCmd())
	return cmd
}

// newStore returns an S3-backed store when a bucket is configured,
// otherwise a local filesystem store rooted at ~/.muse/.
func newStore(ctx context.Context) (storage.Store, error) {
	if bucket != "" {
		return storage.NewS3Store(ctx, bucket)
	}
	store, err := storage.NewLocalStore()
	if err != nil {
		return nil, err
	}
	return store, nil
}

// loadDocument loads the muse.md content from storage. Returns empty string on first run.
func loadDocument(ctx context.Context, store storage.Store) string {
	document, err := store.GetMuse(ctx)
	if err != nil {
		return ""
	}
	return document
}

// Model tiers used by the pipeline. Compose is for editorial work (final
// muse composition, ask). Observe handles bulk work (observation, labeling,
// summarization).
const (
	TierCompose = "compose"
	TierObserve = "observe"
)

// newLLMClient creates an inference.Client based on MUSE_PROVIDER and tier.
// When MUSE_PROVIDER is unset, the provider is auto-detected from environment
// variables: ANTHROPIC_API_KEY → anthropic, OPENAI_API_KEY → openai,
// otherwise bedrock (uses standard AWS credential chain).
func newLLMClient(ctx context.Context, tier string) (inference.Client, error) {
	provider := detectProvider()
	switch provider {
	case "anthropic":
		return anthropic.NewClient(anthropicModel(tier))
	case "openai":
		return museOpenAI.NewClient(openaiModel(tier))
	case "bedrock":
		return bedrock.NewClient(ctx, bedrockModel(tier))
	default:
		return nil, fmt.Errorf("unknown MUSE_PROVIDER %q (use 'anthropic', 'openai', or 'bedrock')", provider)
	}
}

// detectProvider returns the provider from MUSE_PROVIDER, or auto-detects
// based on which API key is present in the environment.
func detectProvider() string {
	if p := os.Getenv("MUSE_PROVIDER"); p != "" {
		return p
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "anthropic"
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		return "openai"
	}
	return "bedrock"
}

func bedrockModel(tier string) string {
	if tier == TierObserve {
		return bedrock.ModelSonnet
	}
	return bedrock.ModelOpus
}

func anthropicModel(tier string) string {
	if tier == TierObserve {
		return anthropic.ModelSonnet
	}
	return anthropic.ModelOpus
}

func openaiModel(tier string) string {
	if tier == TierObserve {
		return museOpenAI.ModelMini
	}
	return museOpenAI.ModelReasoning
}
