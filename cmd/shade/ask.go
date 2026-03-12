package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/shade/internal/shade"
)

func newAskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask the shade a question",
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
			defer s.Close()
			question := strings.Join(args, " ")
			answer, err := s.Ask(ctx, question)
			if err != nil {
				return err
			}
			fmt.Println(answer)
			return nil
		},
	}
}
