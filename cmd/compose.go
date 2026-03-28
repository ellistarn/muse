package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
)

func newComposeCmd() *cobra.Command {
	var reobserve bool
	var relabel bool
	var learn bool
	var limit int
	var method string
	cmd := &cobra.Command{
		Use:   "compose [source...]",
		Short: "Compose a muse from conversations",
		Long: `Discovers new conversations, observes them, and composes a muse.md
that captures how you think. Safe to run repeatedly — only new
conversations are discovered and only unobserved conversations are processed. The
muse is always recomposed.

Two composition methods are available:

  clustering (default) — labels observations, normalizes synonyms, groups by
  label match, summarizes per-cluster, then composes muse.md. Produces
  thematically coherent output.

  map-reduce — observe maps each conversation into observations, then learn
  reduces all observations into a single muse.md. Simpler, sufficient for
  smaller observation sets.

Optionally pass one or more source names to limit discovery and observation to
those sources. Run "muse sources" to see what's available. Network sources like
github and slack are opt-in — they only run when explicitly named. Pass "all" to
include every source.

Use --learn to recompose the muse from existing observations without
reprocessing conversations. Use --reobserve to reprocess conversations from scratch.`,
		Example: `  muse compose                          # default: clustering
  muse compose --method=map-reduce      # simpler pipeline
  muse compose codex                    # only Codex conversations
  muse compose github                   # GitHub PRs and issues (opt-in, requires gh auth)
  muse compose slack                    # Slack (opt-in, set MUSE_SLACK_TOKEN)
  muse compose all                      # all sources including opt-in
  muse compose kiro --reobserve         # re-observe kiro from scratch
  muse compose --learn                  # recompose from existing observations
  muse compose --limit 50              # process at most 50 conversations`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sources := args

			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			// Discover and store new conversations (skip for --learn since it
			// only recomposes from existing observations)
			var uploaded, uploadBytes int
			if !learn {
				result, err := muse.Upload(ctx, store, sources...)
				if err != nil {
					return err
				}
				for _, w := range result.Warnings {
					fmt.Fprintf(os.Stderr, "warning: %s\n", w)
				}
				uploaded = result.Uploaded
				uploadBytes = result.Bytes
			}

			switch method {
			case "clustering":
				return runClusteredCompose(ctx, cmd.OutOrStdout(), store, sources, reobserve, relabel, limit, uploaded, uploadBytes)
			case "map-reduce":
				return runMapReduceCompose(ctx, cmd.OutOrStdout(), store, sources, reobserve, learn, limit)
			default:
				return fmt.Errorf("unknown method %q (use 'clustering' or 'map-reduce')", method)
			}
		},
	}
	cmd.Flags().BoolVar(&reobserve, "reobserve", false, "re-observe all conversations from scratch")
	cmd.Flags().BoolVar(&relabel, "relabel", false, "force re-label all observations")
	cmd.Flags().BoolVar(&learn, "learn", false, "skip observe, recompose muse from existing observations (map-reduce only)")
	cmd.Flags().IntVar(&limit, "limit", 0, "max conversations to observe per run (0 = no limit)")
	cmd.Flags().StringVar(&method, "method", "clustering", "composition method: clustering or map-reduce")
	return cmd
}

func runClusteredCompose(ctx context.Context, stdout io.Writer, store storage.Store, sources []string, reobserve, relabel bool, limit, uploaded, uploadBytes int) error {
	observeLLM, err := newLLMClient(ctx, TierObserve)
	if err != nil {
		return err
	}
	composeLLM, err := newLLMClient(ctx, TierCompose)
	if err != nil {
		return err
	}

	result, err := compose.RunClustered(ctx, store,
		observeLLM, // observe
		observeLLM, // label
		observeLLM, // summarize
		composeLLM, // compose
		compose.ClusteredOptions{
			BaseOptions: compose.BaseOptions{
				Reobserve: reobserve,
				Limit:     limit,
				Sources:   sources,
				Verbose:   verbose,
			},
			Relabel:     relabel,
			Uploaded:    uploaded,
			UploadBytes: uploadBytes,
		},
	)
	if err != nil {
		return err
	}

	return printResult(stdout, result, false)
}

func runMapReduceCompose(ctx context.Context, stdout io.Writer, store storage.Store, sources []string, reobserve, learn bool, limit int) error {
	opts := compose.Options{
		BaseOptions: compose.BaseOptions{
			Reobserve: reobserve,
			Limit:     limit,
			Sources:   sources,
			Verbose:   verbose,
		},
		Learn: learn,
	}

	observeLLM, err := newLLMClient(ctx, TierObserve)
	if err != nil {
		return err
	}
	composeLLM, err := newLLMClient(ctx, TierCompose)
	if err != nil {
		return err
	}

	if learn {
		opts.Learn = true
		result, err := compose.LearnOnly(ctx, store, composeLLM)
		if err != nil {
			return err
		}
		return printResult(stdout, result, true)
	}

	result, err := compose.Run(ctx, store, observeLLM, composeLLM, opts)
	if err != nil {
		return err
	}
	return printResult(stdout, result, false)
}

func printResult(stdout io.Writer, result *compose.Result, learnOnly bool) error {
	if !learnOnly {
		fmt.Fprintf(stdout, "Processed %d conversations (%d pruned)\n", result.Processed, result.Pruned)
		if result.Remaining > 0 {
			fmt.Fprintf(stdout, "%d conversations still pending observation (run compose again)\n", result.Remaining)
		}
	}
	// Print per-stage telemetry
	if len(result.Stages) > 0 {
		fmt.Fprintf(stdout, "\n%-12s %-45s %8s %8s %8s %8s\n", "STAGE", "MODEL", "TIME", "IN TOK", "OUT TOK", "DATA")
		fmt.Fprintf(stdout, "%-12s %-45s %8s %8s %8s %8s\n", "─────", "─────", "────", "──────", "───────", "────")
		for _, s := range result.Stages {
			model := s.Model
			if len(model) > 45 {
				model = "…" + model[len(model)-44:]
			}
			cost := ""
			if s.Usage.Cost() > 0 {
				cost = fmt.Sprintf("$%.4f", s.Usage.Cost())
			}
			fmt.Fprintf(stdout, "%-12s %-45s %8s %8s %8s %8s %s\n",
				s.Name,
				model,
				compose.FormatDuration(s.Duration),
				formatTokens(s.Usage.InputTokens),
				formatTokens(s.Usage.OutputTokens),
				formatDataSize(s.DataSize),
				cost,
			)
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintf(stdout, "Muse composed (%dk input, %dk output tokens, $%.2f)\n",
		result.Usage.InputTokens/1000, result.Usage.OutputTokens/1000, result.Usage.Cost())
	if result.Muse != "" {
		fmt.Fprintf(stdout, "muse.md: ~%d tokens\n", inference.EstimateTokens(result.Muse))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  muse show          # view muse.md")
	fmt.Fprintln(stdout, "  muse show --diff   # view what changed")
	return nil
}

func formatTokens(n int) string {
	if n == 0 {
		return "—"
	}
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

func formatDataSize(n int) string {
	if n == 0 {
		return "—"
	}
	return compose.FormatBytes(n)
}

// runCompose executes the map-reduce compose pipeline and prints results.
// Preserved for backward compatibility with existing tests.
func runCompose(ctx context.Context, stdout, stderr io.Writer, store storage.Store, observeLLM, learnLLM inference.Client, opts compose.Options) error {
	var (
		result *compose.Result
		err    error
	)
	if opts.Learn {
		result, err = compose.LearnOnly(ctx, store, learnLLM)
	} else {
		result, err = compose.Run(ctx, store, observeLLM, learnLLM, opts)
	}
	if err != nil {
		return err
	}
	return printResult(stdout, result, opts.Learn)
}
