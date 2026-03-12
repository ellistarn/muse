package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/shade"
)

func newAdviseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "advise [question]",
		Short: "Ask the shade for advice",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := shade.New(ctx, bucket)
			if err != nil {
				return err
			}
			question := strings.Join(args, " ")
			answer, err := s.Advise(ctx, question)
			if err != nil {
				return err
			}
			fmt.Println(answer)
			return nil
		},
	}
}
