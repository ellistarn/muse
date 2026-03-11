package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/shade"
	"github.com/ellistarn/shade/internal/source"
)

func newShowCmd() *cobra.Command {
	var src string
	cmd := &cobra.Command{
		Use:   "show <session-id>",
		Short: "Display a memory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := shade.New(ctx, bucket)
			if err != nil {
				return err
			}
			session, err := s.Show(ctx, args[0], src)
			if err != nil {
				return err
			}
			printSession(cmd, session)
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "source", "", "Source to search (opencode, claude-code)")
	return cmd
}

func printSession(cmd *cobra.Command, s *source.Session) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "# %s\n\n", s.Title)
	fmt.Fprintf(out, "Source:  %s\n", s.Source)
	fmt.Fprintf(out, "Project: %s\n", s.Project)
	fmt.Fprintf(out, "Created: %s\n", s.CreatedAt.Format("2006-01-02 15:04"))
	fmt.Fprintf(out, "Updated: %s\n\n", s.UpdatedAt.Format("2006-01-02 15:04"))
	for _, m := range s.Messages {
		role := strings.ToUpper(m.Role)
		fmt.Fprintf(out, "--- %s [%s] ---\n", role, m.Timestamp.Format("15:04:05"))
		if m.Content != "" {
			fmt.Fprintln(out, m.Content)
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(out, "[tool: %s]\n", tc.Name)
		}
		fmt.Fprintln(out)
	}
}
