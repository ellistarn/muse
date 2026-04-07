package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/importer"
)

func newImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import [name]",
		Short: "Import conversations from an external plugin",
		Long: `Runs an external plugin to import conversations into muse. Plugins are
executables named muse-{name} on $PATH that produce conversations in muse's
standard JSON format.

With a name argument, runs the named plugin (resolving it to muse-{name} on
$PATH). First use of a plugin is always explicit — bare "muse import" only
re-imports previously imported sources.

With no arguments, re-imports all previously imported sources. If a plugin is
no longer on $PATH, its source is skipped with a warning.

Plugin configuration is the plugin's concern — muse passes MUSE_OUTPUT_DIR
and expects conversation JSON files plus a .muse-source.json metadata file.`,
		Example: `  muse import code-reviews      # run muse-code-reviews plugin
  muse import internal-chat     # run muse-internal-chat plugin
  muse import                   # re-import all previously imported sources`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			if len(args) == 1 {
				// Run a single named plugin
				name := args[0]
				result, err := importer.Run(ctx, store, name, os.Stderr)
				if err != nil {
					return err
				}
				printImportResult(cmd, result)
				return nil
			}

			// No args: re-import all previously imported sources
			results, err := importer.RunAll(ctx, store, os.Stderr)
			if err != nil {
				return err
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No previously imported sources found. Run \"muse import <name>\" to import a plugin.")
				return nil
			}
			for _, result := range results {
				printImportResult(cmd, result)
			}
			return nil
		},
	}
}

func printImportResult(cmd *cobra.Command, result *importer.Result) {
	w := cmd.OutOrStdout()
	if result.Imported > 0 || result.Skipped > 0 {
		fmt.Fprintf(w, "  %-20s %d imported, %d unchanged", result.Source, result.Imported, result.Skipped)
		if result.Rejected > 0 {
			fmt.Fprintf(w, ", %d rejected", result.Rejected)
		}
		fmt.Fprintln(w)
	} else if result.Rejected > 0 {
		fmt.Fprintf(w, "  %-20s %d rejected\n", result.Source, result.Rejected)
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s: %s\n", result.Source, w)
	}
}
