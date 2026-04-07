package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/prompts"
	"github.com/spf13/cobra"
)

func newEvalContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval-context [conversation-id]",
		Short: "Compare observation strategies on a single conversation",
		Long: `Loads a conversation snapshot, runs the observe pipeline under each
context strategy (default, adaptive), and prints observations side by side.
Uses a snapshot to ensure repeatable comparisons.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEvalContext(cmd.Context(), args[0])
		},
	}
	return cmd
}

func runEvalContext(ctx context.Context, convID string) error {
	// Find the conversation file
	convPath, err := findConversation(convID)
	if err != nil {
		return err
	}

	// Load and snapshot the conversation
	data, err := os.ReadFile(convPath)
	if err != nil {
		return fmt.Errorf("read conversation: %w", err)
	}
	var conv conversation.Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return fmt.Errorf("parse conversation: %w", err)
	}
	if conv.Source == "" {
		conv.Source = sourceFromPath(convPath)
	}

	fmt.Fprintf(os.Stderr, "conversation: %s\n", convPath)
	fmt.Fprintf(os.Stderr, "messages:     %d\n", len(conv.Messages))
	fmt.Fprintf(os.Stderr, "source:       %s\n\n", conv.Source)

	llm, err := newLLMClient(ctx, TierFast)
	if err != nil {
		return err
	}

	strategies := []struct {
		name    string
		ctx     compose.ContextStrategy
	}{
		{"default (500 chars)", compose.ContextDefault},
		{"adaptive (500-2000)", compose.ContextAdaptive},
	}

	results := make([][]compose.Observation, len(strategies))

	for i, s := range strategies {
		fmt.Fprintf(os.Stderr, "--- strategy: %s ---\n", s.name)

		raw, obs, usage, err := compose.ObserveForTestVerbose(ctx, llm, &conv, s.ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			continue
		}
		results[i] = obs
		fmt.Fprintf(os.Stderr, "  observations: %d  tokens: %dk in / %dk out  cost: $%.4f\n",
			len(obs), usage.InputTokens/1000, usage.OutputTokens/1000, usage.Cost())
		fmt.Fprintf(os.Stderr, "  raw output (%d chars): %s\n", len(raw), raw[:min(len(raw), 500)])
		fmt.Fprintln(os.Stderr)
	}

	// Print side-by-side comparison
	fmt.Println()
	for i, s := range strategies {
		fmt.Printf("=== %s: %d observations ===\n\n", s.name, len(results[i]))
		for j, obs := range results[i] {
			if obs.Quote != "" {
				fmt.Printf("  [%d] Quote: %s\n", j+1, truncateStr(obs.Quote, 120))
			}
			fmt.Printf("  [%d] %s\n\n", j+1, truncateStr(obs.Text, 300))
		}
	}

	// Print prompt used (first 200 chars for identification)
	observePrompt := prompts.Observe
	if isHumanSource(conv.Source) {
		observePrompt = prompts.ObserveHuman
	}
	fmt.Printf("--- prompt (first 200 chars) ---\n%s\n", truncateStr(observePrompt, 200))

	return nil
}

func findConversation(id string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Search across all sources
	sources := []string{"claude-code", "kiro-cli", "kiro", "opencode", "codex",
		"github-issues", "github-prs", "slack"}
	for _, src := range sources {
		pattern := filepath.Join(home, ".muse", "conversations", src, id+"*")
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			return matches[0], nil
		}
	}
	// Try as a direct path
	if _, err := os.Stat(id); err == nil {
		return id, nil
	}
	return "", fmt.Errorf("conversation %q not found", id)
}

func sourceFromPath(path string) string {
	parts := strings.Split(path, "/conversations/")
	if len(parts) < 2 {
		return "unknown"
	}
	return strings.Split(parts[1], "/")[0]
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func isHumanSource(source string) bool {
	switch source {
	case "slack", "github-issues", "github-prs":
		return true
	default:
		return false
	}
}
