package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
)

func newAskCmd() *cobra.Command {
	var newSession bool
	cmd := &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask your muse a question",
		Long: `Sends a question to your muse and streams the response. By default, continues
the most recent conversation so your muse remembers prior context. Use --new
to start a fresh session.`,
		Example: `  muse ask "Is a monorepo the right call for this project?"
  muse ask "Tell me more about that"
  muse ask --new "How should I structure error handling in Go?"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}
			document := loadDocument(ctx, store)
			llm, err := newLLMClient(ctx, TierStrong)
			if err != nil {
				return err
			}

			// Session persistence lives under ~/.muse/sessions/.
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			sessionsDir := filepath.Join(home, ".muse", "sessions")
			m := muse.New(llm, document, muse.WithSessionsDir(sessionsDir))

			question := strings.Join(args, " ")
			result, err := m.Ask(ctx, muse.AskInput{
				Question: question,
				New:      newSession,
				StreamFunc: inference.StreamFunc(func(delta inference.StreamDelta) {
					fmt.Fprint(os.Stdout, delta.Text)
				}),
			})
			if result != nil && result.Response != "" {
				fmt.Fprintln(os.Stdout) // trailing newline after stream completes
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&newSession, "new", false, "start a fresh session instead of continuing the last one")
	return cmd
}
