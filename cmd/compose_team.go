package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
)

const teamComposePrompt = `You are synthesizing validated reviewer observations into a team review guide.

Each observation has been scored by how well it predicts what the reviewer actually says in
code reviews. Higher weights mean stronger predictive value. Negative weights mean the
observation led the muse astray.

RULES:

1. Observations with weight >= 2 are validated signals. Preserve at full strength. If
   distinctive to one reviewer, give prominent treatment.

2. Observations with weight 0-2 are mild or unvalidated. Include only if they reinforce
   higher-weighted patterns.

3. Observations with negative weight are harmful. Cut them.

4. Observations that score high for one reviewer but zero or negative for others are
   DISTINCTIVE. These are the team muse's primary value over a base model. Give them
   dedicated sections, not bullets in shared lists.

5. Observations that score moderately across all reviewers are SHARED. The base model
   catches most of these. Compress them.

6. Order by signal strength: distinctive patterns first, shared patterns second.

7. Write in third person, attributing patterns to specific reviewers. Every sentence
   should change how the muse reviews code.`

func newComposeTeamCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "compose-team",
		Short: "Compose a team muse from validated observations",
		Long: `Validates observations from multiple peer muses by asking each reviewer's muse
to reinforce (+1), ignore (0), or reject (-1) each observation. Then composes a
team muse weighted by the muse's own judgment of what matters.`,
		Example: `  muse compose-team --project karpenter github/ellistarn github/jmdeal github/tzneal github/DerekFrank`,
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Validate observations for each peer
			var allWeighted []weightedObservation
			for _, peerFlag := range args {
				_, username, err := parsePeerFlag(peerFlag)
				if err != nil {
					return err
				}
				weighted, err := validateObservations(ctx, username, project)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", peerFlag, err)
					continue
				}
				allWeighted = append(allWeighted, weighted...)
			}

			if len(allWeighted) == 0 {
				return fmt.Errorf("no validated observations from any peer")
			}

			// Compose team muse from weighted observations
			fmt.Fprintf(os.Stderr, "\ncomposing team muse from %d weighted observations...\n", len(allWeighted))
			teamMuse, err := composeTeamMuse(ctx, allWeighted)
			if err != nil {
				return fmt.Errorf("compose team muse: %w", err)
			}

			// Save
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			teamDir := filepath.Join(home, ".muse", "peers", "github-karpenter-team", project)
			teamStore := storage.NewLocalStoreWithRoot(teamDir)
			ts := time.Now().UTC().Format(time.RFC3339)
			if err := teamStore.PutMuse(ctx, ts, teamMuse); err != nil {
				return fmt.Errorf("save team muse: %w", err)
			}

			fmt.Fprintf(os.Stderr, "team muse saved to %s\n", filepath.Join(teamDir, "muse.md"))
			fmt.Print(teamMuse)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project scope")
	return cmd
}

// composeTeamMuse calls the LLM with weighted observations to produce a team muse.
func composeTeamMuse(ctx context.Context, weighted []weightedObservation) (string, error) {
	// Group by reviewer
	byReviewer := make(map[string][]weightedObservation)
	for _, w := range weighted {
		byReviewer[w.Reviewer] = append(byReviewer[w.Reviewer], w)
	}

	// Build input: observations grouped by reviewer, sorted by weight descending
	var parts []string
	for reviewer, obs := range byReviewer {
		sort.Slice(obs, func(i, j int) bool {
			return obs[i].Weight > obs[j].Weight
		})

		var lines []string
		lines = append(lines, fmt.Sprintf("## %s", reviewer))
		lines = append(lines, "")
		for _, o := range obs {
			lines = append(lines, fmt.Sprintf("[%+.1f] %s", o.Weight, o.Text))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	input := strings.Join(parts, "\n\n---\n\n")

	llm, err := newLLMClient(ctx, TierStrong)
	if err != nil {
		return "", err
	}

	resp, usage, err := inference.Converse(ctx, llm, teamComposePrompt, input, inference.WithMaxTokens(16384))
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "  tokens: input=%d output=%d\n", usage.InputTokens, usage.OutputTokens)
	return resp, nil
}
