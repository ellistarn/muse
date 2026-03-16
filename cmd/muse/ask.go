package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/muse"
)

func newAskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask your muse a question",
		Long: `Sends a question to your muse and streams the response. Each call is
stateless — your muse has no recall of previous questions. Ask opinionated
questions ("Is X a good approach for Y?") rather than factual lookups.`,
		Example: `  muse ask "Is a monorepo the right call for this project?"
  muse ask "How should I structure error handling in Go?"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}
			m, err := muse.New(ctx, store)
			if err != nil {
				return err
			}
			question := strings.Join(args, " ")
			var wroteOutput bool
			_, err = m.Ask(ctx, muse.AskInput{
				Question: question,
				StreamFunc: bedrock.StreamFunc(func(delta string) {
					fmt.Fprint(os.Stdout, delta)
					wroteOutput = true
				}),
			})
			if wroteOutput {
				fmt.Fprintln(os.Stdout) // trailing newline after stream completes
			}
			return err
		},
	}
}
