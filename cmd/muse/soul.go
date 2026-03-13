package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/storage"
)

func newSoulCmd() *cobra.Command {
	var diff bool
	cmd := &cobra.Command{
		Use:   "soul",
		Short: "Print the soul document",
		Long: `Prints your current soul document to stdout. If no soul exists yet, prompts
you to run 'muse dream'.

Use --diff to summarize what changed since the last dream.`,
		Example: `  muse soul          # print the soul
  muse soul --diff   # summarize what changed since the last dream`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			if diff {
				return runDiff(cmd, store)
			}

			log.Println("Loading soul...")
			soul, err := store.GetSoul(ctx)
			if err != nil {
				if !storage.IsNotFound(err) {
					return fmt.Errorf("failed to load soul: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "No soul found. Run 'muse dream' to generate one from memories.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(soul))
			return nil
		},
	}
	cmd.Flags().BoolVar(&diff, "diff", false, "summarize what changed since the last dream")
	return cmd
}

func runDiff(cmd *cobra.Command, store storage.Store) error {
	ctx := cmd.Context()

	log.Println("Loading dream history...")
	dreams, err := store.ListDreams(ctx)
	if err != nil {
		return fmt.Errorf("failed to list dream history: %w", err)
	}
	if len(dreams) == 0 {
		return fmt.Errorf("no dream history found; run 'muse dream' to create a snapshot")
	}
	latest := dreams[len(dreams)-1]
	log.Printf("Comparing snapshot %s with current soul\n", latest)

	prev, err := store.GetDreamSoul(ctx, latest)
	if err != nil {
		return fmt.Errorf("failed to load dream snapshot %s: %w", latest, err)
	}
	current, err := store.GetSoul(ctx)
	if err != nil {
		if !storage.IsNotFound(err) {
			return fmt.Errorf("failed to load current soul: %w", err)
		}
		current = ""
	}
	log.Printf("Previous: %d bytes, Current: %d bytes\n", len(prev), len(current))

	if prev == "" && current == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "No soul in either snapshot.")
		return nil
	}

	log.Println("Generating diff summary...")
	llm, err := bedrock.NewClient(ctx, bedrock.ModelSonnet)
	if err != nil {
		return err
	}

	prompt := `Compare the previous and current soul documents. Summarize what changed in a few concise bullet points: which sections were added, removed, or meaningfully revised. Focus on substance, not formatting.`

	summary, usage, err := llm.Converse(ctx, prompt, "Previous:\n\n"+prev+"\n\n---\n\nCurrent:\n\n"+current)
	if err != nil {
		return fmt.Errorf("failed to generate diff summary: %w", err)
	}
	log.Printf("Diff complete ($%.4f)\n", usage.Cost())
	fmt.Fprintf(cmd.OutOrStdout(), "Changes since %s:\n\n%s\n", latest, strings.TrimSpace(summary))
	return nil
}
