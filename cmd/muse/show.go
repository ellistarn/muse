package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/storage"
)

func newShowCmd() *cobra.Command {
	var diff bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print muse.md",
		Long: `Prints your current muse.md to stdout. If no muse exists yet, prompts
you to run 'muse distill'.

Use --diff to summarize what changed since the last distill.`,
		Example: `  muse show          # print the muse
  muse show --diff   # summarize what changed since the last distill`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			if diff {
				return runDiff(cmd, store)
			}

			log.Println("Loading muse...")
			soul, err := store.GetMuse(ctx)
			if err != nil {
				if !storage.IsNotFound(err) {
					return fmt.Errorf("failed to load muse: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "No muse found. Run 'muse distill' to generate one from memories.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(soul))
			fmt.Fprintf(cmd.ErrOrStderr(), "muse.md: ~%d tokens\n", inference.EstimateTokens(soul))
			return nil
		},
	}
	cmd.Flags().BoolVar(&diff, "diff", false, "summarize what changed since the last distill")
	return cmd
}

func runDiff(cmd *cobra.Command, store storage.Store) error {
	ctx := cmd.Context()

	log.Println("Loading muse history...")
	muses, err := store.ListMuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to list muse history: %w", err)
	}
	if len(muses) < 2 {
		return fmt.Errorf("need at least 2 muse versions to diff; only found %d", len(muses))
	}
	// Compare the second-to-last with the latest
	prevTimestamp := muses[len(muses)-2]
	log.Printf("Comparing snapshot %s with current muse\n", prevTimestamp)

	prev, err := store.GetMuseVersion(ctx, prevTimestamp)
	if err != nil {
		return fmt.Errorf("failed to load muse version %s: %w", prevTimestamp, err)
	}
	current, err := store.GetMuseVersion(ctx, muses[len(muses)-1])
	if err != nil {
		if !storage.IsNotFound(err) {
			return fmt.Errorf("failed to load current muse: %w", err)
		}
		current = ""
	}
	log.Printf("Previous: %d bytes, Current: %d bytes\n", len(prev), len(current))

	if prev == "" && current == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "No muse in either snapshot.")
		return nil
	}

	log.Println("Generating diff summary...")
	llm, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
	if err != nil {
		return err
	}

	prompt := `Compare the previous and current versions of this muse. Summarize what changed in a few concise bullet points: which sections were added, removed, or meaningfully revised. Focus on substance, not formatting.`

	summary, usage, err := llm.Converse(ctx, prompt, "Previous:\n\n"+prev+"\n\n---\n\nCurrent:\n\n"+current)
	if err != nil {
		return fmt.Errorf("failed to generate diff summary: %w", err)
	}
	log.Printf("Diff complete ($%.4f)\n", usage.Cost())
	fmt.Fprintf(cmd.OutOrStdout(), "Changes since %s:\n\n%s\n", prevTimestamp, strings.TrimSpace(summary))
	fmt.Fprintf(cmd.ErrOrStderr(), "tokens: %d in / %d out · $%.4f\n",
		usage.InputTokens, usage.OutputTokens, usage.Cost())
	return nil
}
