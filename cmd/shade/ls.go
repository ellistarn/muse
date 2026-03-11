package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/shade"
)

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List uploaded memories",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := shade.New(ctx, bucket)
			if err != nil {
				return err
			}
			entries, err := s.Ls(ctx)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SOURCE\tSESSION ID\tLAST MODIFIED")
			for _, e := range entries {
				fmt.Fprintf(w, "%s\t%s\t%s\n", e.Source, e.SessionID, e.LastModified.Format("2006-01-02 15:04"))
			}
			return w.Flush()
		},
	}
}
