package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/solworktech/md2pdf/v2"
	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/storage"
)

func newShowCmd() *cobra.Command {
	var diff bool
	var output string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print muse.md",
		Long: `Prints your current muse.md to stdout. If no muse exists yet, prompts
you to run 'muse compose'.

Use --diff to print the changelog from the latest compose. If no diff has
been computed yet, one is generated on the fly and cached for future use.

Use -o to write output to a file. Files ending in .pdf are rendered as PDF.`,
		Example: `  muse show              # print the muse
  muse show --diff       # print what changed in the latest compose
  muse show -o muse.pdf  # export as PDF`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			if diff {
				return runShowDiff(cmd, store)
			}

			document, err := store.GetMuse(ctx)
			if err != nil {
				if !storage.IsNotFound(err) {
					return fmt.Errorf("failed to load muse: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "No muse found. Run 'muse compose' to generate one from conversations.")
				return nil
			}

			if output != "" {
				if filepath.Ext(output) == ".pdf" {
					return renderPDF(document, output)
				}
				return os.WriteFile(output, []byte(strings.TrimSpace(document)+"\n"), 0644)
			}

			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(document))
			return nil
		},
	}
	cmd.Flags().BoolVar(&diff, "diff", false, "print what changed in the latest compose")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to file (use .pdf extension for PDF)")
	return cmd
}

func renderPDF(markdown, path string) error {
	renderer := mdtopdf.NewPdfRenderer(mdtopdf.PdfRendererParams{
		PdfFile: path,
		Theme:   mdtopdf.LIGHT,
	})
	return renderer.Process([]byte(markdown))
}

func runShowDiff(cmd *cobra.Command, store storage.Store) error {
	ctx := cmd.Context()
	muses, err := store.ListMuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to list muse history: %w", err)
	}
	if len(muses) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No muse found. Run 'muse compose' to generate one from conversations.")
		return nil
	}
	latest := muses[len(muses)-1]

	// Try cached diff first.
	d, err := store.GetMuseDiff(ctx, latest)
	if err == nil {
		fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(d))
		return nil
	}
	if !storage.IsNotFound(err) {
		return fmt.Errorf("failed to load diff: %w", err)
	}

	// No cached diff — compute it lazily.
	current, err := store.GetMuseVersion(ctx, latest)
	if err != nil {
		return fmt.Errorf("failed to load latest muse: %w", err)
	}

	var previous string
	if len(muses) >= 2 {
		previous, _ = store.GetMuseVersion(ctx, muses[len(muses)-2])
	}

	fmt.Fprintln(os.Stderr, "Computing diff...")
	client, err := newLLMClient(ctx, TierObserve)
	if err != nil {
		return fmt.Errorf("llm client: %w", err)
	}
	d, _, err = compose.ComputeDiff(ctx, client, store, latest, previous, current)
	if err != nil {
		return fmt.Errorf("compute diff: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(d))
	return nil
}
