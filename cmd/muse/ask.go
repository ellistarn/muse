package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/muse"
)

func newAskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask your muse a question",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			m, err := muse.New(ctx, bucket)
			if err != nil {
				return err
			}
			question := strings.Join(args, " ")
			answer, err := m.Ask(ctx, question)
			if err != nil {
				return err
			}
			fmt.Println(answer)
			return nil
		},
	}
}
