package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/spf13/cobra"
)

const judgePrompt = `You are evaluating observations extracted from a conversation. For each observation, rate it against the source conversation:

GROUNDED — The observation is well-supported by the conversation. It captures something distinctive about how the person thinks, not just what they said.

GENERIC — The observation is true but not distinctive. Any thoughtful person might think this way. It carries no information about this specific person.

MISLEADING — The observation overstates, mischaracterizes, or fabricates. The conversation doesn't support the claim being made.

For each numbered observation, respond with just the number and the rating. Example:
1. GROUNDED
2. GENERIC
3. MISLEADING`

func newJudgeCmd() *cobra.Command {
	var mode string
	var limit int
	cmd := &cobra.Command{
		Use:   "judge",
		Short: "Rate observation quality against source conversations",
		Long: `Runs an LLM-as-judge on observations from a given observe mode,
rating each as GROUNDED, GENERIC, or MISLEADING against the source
conversation. Outputs a JSON summary.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJudge(cmd.Context(), compose.ObserveMode(mode), limit)
		},
	}
	cmd.Flags().StringVar(&mode, "observe-mode", "woo", "which observation set to judge")
	cmd.Flags().IntVar(&limit, "limit", 0, "max conversations to judge (0 = all)")
	return cmd
}

type judgeResult struct {
	Source         string `json:"source"`
	ConversationID string `json:"conversation_id"`
	Method         string `json:"method"`
	Total          int    `json:"total"`
	Grounded       int    `json:"grounded"`
	Generic        int    `json:"generic"`
	Misleading     int    `json:"misleading"`
	Unparsed       int    `json:"unparsed"`
}

func runJudge(ctx context.Context, mode compose.ObserveMode, limit int) error {
	store, err := newStore(ctx)
	if err != nil {
		return err
	}

	// Use Opus for judging
	judge, err := newLLMClient(ctx, TierStrong)
	if err != nil {
		return err
	}

	entries, err := compose.ListObservations(ctx, store, mode)
	if err != nil {
		return err
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	fmt.Fprintf(os.Stderr, "judge: %d conversations, mode=%s\n", len(entries), string(mode))

	var mu sync.Mutex
	var results []judgeResult
	var counter atomic.Int32

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(5) // Opus is expensive, keep concurrency low

	for _, entry := range entries {
		g.Go(func() error {
			obs, err := compose.GetObservations(ctx, store, entry.Source, entry.ConversationID, mode)
			if err != nil || len(obs.Items) == 0 {
				counter.Add(1)
				return nil
			}

			// Load the source conversation
			conv, err := store.GetConversation(ctx, entry.Source, entry.ConversationID)
			if err != nil {
				counter.Add(1)
				return nil // skip conversations we can't load
			}

			// Build conversation text (compressed)
			var convText strings.Builder
			for _, msg := range conv.Messages {
				if msg.Content == "" {
					continue
				}
				role := msg.Role
				text := msg.Content
				if len(text) > 500 {
					text = text[:500] + "..."
				}
				fmt.Fprintf(&convText, "[%s]: %s\n\n", role, text)
			}

			// Build observation list
			var obsList strings.Builder
			for i, item := range obs.Items {
				text := item.Text
				fmt.Fprintf(&obsList, "%d. %s\n", i+1, text)
			}

			input := fmt.Sprintf("## Source conversation\n\n%s\n\n## Observations to evaluate\n\n%s",
				convText.String(), obsList.String())

			resp, _, err := inference.Converse(ctx, judge, judgePrompt, input, inference.WithMaxTokens(4096))
			n := counter.Add(1)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [%d/%d] %s/%s: judge error: %v\n", n, len(entries), entry.Source, entry.ConversationID, err)
				return nil
			}

			// Parse ratings
			r := judgeResult{
				Source:         entry.Source,
				ConversationID: entry.ConversationID,
				Method:         string(mode),
				Total:          len(obs.Items),
			}
			for _, line := range strings.Split(resp, "\n") {
				line = strings.TrimSpace(line)
				upper := strings.ToUpper(line)
				if strings.Contains(upper, "GROUNDED") {
					r.Grounded++
				} else if strings.Contains(upper, "GENERIC") {
					r.Generic++
				} else if strings.Contains(upper, "MISLEADING") {
					r.Misleading++
				}
			}
			r.Unparsed = r.Total - r.Grounded - r.Generic - r.Misleading

			fmt.Fprintf(os.Stderr, "  [%d/%d] %s/%s: %d grounded, %d generic, %d misleading\n",
				n, len(entries), entry.Source, entry.ConversationID, r.Grounded, r.Generic, r.Misleading)

			mu.Lock()
			results = append(results, r)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// Print summary
	var totalObs, totalGrounded, totalGeneric, totalMisleading int
	for _, r := range results {
		totalObs += r.Total
		totalGrounded += r.Grounded
		totalGeneric += r.Generic
		totalMisleading += r.Misleading
	}
	fmt.Fprintf(os.Stderr, "\n%s: %d observations, %d grounded (%.0f%%), %d generic (%.0f%%), %d misleading (%.0f%%)\n",
		string(mode), totalObs,
		totalGrounded, pct(totalGrounded, totalObs),
		totalGeneric, pct(totalGeneric, totalObs),
		totalMisleading, pct(totalMisleading, totalObs))

	// Write JSON to stdout
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}
