package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/runlog"
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
			result, err := m.Ask(ctx, muse.AskInput{
				Question: question,
				StreamFunc: bedrock.StreamFunc(func(delta string) {
					fmt.Fprint(os.Stdout, delta)
					wroteOutput = true
				}),
			})
			if wroteOutput {
				fmt.Fprintln(os.Stdout) // trailing newline after stream completes
			}
			if err != nil {
				return err
			}

			// Write run record (best-effort, don't fail the command)
			rl := newRunLog()
			if err := rl.Log(runlog.Record{
				Timestamp:    time.Now().UTC(),
				Command:      "ask",
				InputTokens:  result.Usage.InputTokens,
				OutputTokens: result.Usage.OutputTokens,
				Cost:         result.Usage.Cost(),
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to record usage: %v\n", err)
			}
			return nil
		},
	}
}
