package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

func newSourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sources",
		Short: "List available conversation sources",
		Long: `Lists all conversation sources and their status.

Active sources have an observation directory and are included when running
"muse compose" with no arguments. Use "muse add" and "muse remove" to manage sources.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}
			return printSources(ctx, cmd.OutOrStdout(), store)
		},
	}
}

// printSources lists all sources with their active/inactive status and counts.
func printSources(ctx context.Context, w io.Writer, store storage.Store) error {
	active, err := compose.ListObservationSources(ctx, store)
	if err != nil {
		return err
	}
	activeSet := make(map[string]bool, len(active))
	for _, s := range active {
		activeSet[s] = true
	}

	// Count conversations per source.
	convCounts := map[string]int{}
	convEntries, err := store.ListConversations(ctx)
	if err != nil {
		return err
	}
	for _, e := range convEntries {
		convCounts[e.Source]++
	}

	// Count observation items per source.
	obsCounts, err := compose.CountObservationItems(ctx, store)
	if err != nil {
		return err
	}

	for _, s := range conversation.Sources() {
		status := "inactive"
		if activeSet[s.Name] {
			status = "active"
		}
		counts := ""
		if activeSet[s.Name] {
			c, o := convCounts[s.Name], obsCounts[s.Name]
			if c > 0 || o > 0 {
				counts = fmt.Sprintf("%d conversations  %d observations", c, o)
			}
		}
		tag := ""
		if s.OptIn {
			tag = "(opt-in)"
		}
		suffix := counts + tag
		if suffix != "" {
			suffix = " " + suffix
		}
		fmt.Fprintf(w, "  %-16s %-10s%s\n", s.Name, status, suffix)
	}

	return nil
}
