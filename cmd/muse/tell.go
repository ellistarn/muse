package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/muse"
)

func newTellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tell [message]",
		Short: "Tell your muse something directly",
		Long: `Stores a message directly as a conversation session so it gets picked up
by the next distill. Use this to feed your muse observations, preferences,
or corrections without going through an agent conversation.`,
		Example: `  muse tell "I prefer table-driven tests over sequential assertions"
  muse tell "Always use structured logging, never fmt.Println in production"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			sessionID := now.Format("20060102T150405Z")
			message := strings.Join(args, " ")
			session := &conversation.Session{
				SchemaVersion: 1,
				Source:        "muse",
				SessionID:     sessionID,
				Title:         message,
				CreatedAt:     now,
				UpdatedAt:     now,
				Messages: []conversation.Message{
					{
						Role:      "user",
						Content:   message,
						Timestamp: now,
					},
				},
			}
			n, err := store.PutSession(ctx, session)
			if err != nil {
				return fmt.Errorf("failed to store session: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stored conversations/muse/%s.json (%s)\n", sessionID, muse.FormatBytes(n))
			return nil
		},
	}
}
