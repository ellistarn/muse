package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/output"
	"github.com/ellistarn/muse/internal/storage"
)

func newComposeCmd() *cobra.Command {
	var reobserve bool
	var relabel bool
	var learn bool
	var limit int
	var method string
	var context string
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Compose a muse from conversations",
		Long: `Discovers new conversations, observes them, and composes a muse.md
that captures how you think. Safe to run repeatedly — only new
conversations are discovered and only unobserved conversations are processed. The
muse is always recomposed.

Two composition methods are available:

  clustering (default) — labels observations, themes them into canonical
  patterns, groups by theme, summarizes per-cluster, then composes muse.md.
  Produces thematically coherent output.

  map-reduce — observe maps each conversation into observations, then learn
  reduces all observations into a single muse.md. Simpler, sufficient for
  smaller observation sets.

Sources are remembered automatically. On first run, default (local) sources are
activated. Use "muse add" and "muse remove" to manage sources. Run "muse sources"
to see what's active.

Use --learn to recompose the muse from existing observations without
reprocessing conversations. Use --reobserve to reprocess conversations from scratch.`,
		Example: `  muse compose                          # default: clustering
  muse compose --method=map-reduce      # simpler pipeline
  muse compose --reobserve              # re-observe all from scratch
  muse compose --learn                  # recompose from existing observations
  muse compose --limit 50              # process at most 50 conversations`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			// Resolve active sources from observation directories.
			// On first run, defaults are bootstrapped.
			sources, err := compose.ResolveSources(ctx, store)
			if err != nil {
				return err
			}

			// Discover and store new conversations (skip for --learn since it
			// only recomposes from existing observations)
			var uploaded, uploadBytes int
			if !learn {
				result, err := muse.Upload(ctx, store, syncProgressRenderer(), sources...)
				if err != nil {
					return err
				}
				for _, w := range result.Warnings {
					fmt.Fprintf(os.Stderr, "warning: %s\n", w)
				}
				printSyncSummary(result)
				uploaded = result.Uploaded
				uploadBytes = result.Bytes
			}

			switch method {
			case "clustering":
				return runClusteredCompose(ctx, cmd.OutOrStdout(), store, reobserve, relabel, compose.ContextStrategy(context), limit, uploaded, uploadBytes)
			case "map-reduce":
				return runMapReduceCompose(ctx, cmd.OutOrStdout(), store, reobserve, learn, limit)
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
	cmd.Flags().StringVar(&context, "context", "", "compression context strategy: '' (default 500 chars) or 'adaptive' (scale by owner message length)")
	return cmd
}

func runClusteredCompose(ctx context.Context, stdout io.Writer, store storage.Store, reobserve, relabel bool, ctxStrategy compose.ContextStrategy, limit, uploaded, uploadBytes int) error {
	observeLLM, err := newLLMClient(ctx, TierFast)
	if err != nil {
		return err
	}
	composeLLM, err := newLLMClient(ctx, TierStrong)
	if err != nil {
		return err
	}

	result, err := compose.RunClustered(ctx, store,
		observeLLM, // observe
		observeLLM, // label
		composeLLM, // summarize
		composeLLM, // compose
		compose.ClusteredOptions{
			BaseOptions: compose.BaseOptions{
				Reobserve: reobserve,
				Limit:     limit,
				Verbose:   verbose,
				Context:   ctxStrategy,
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

func runMapReduceCompose(ctx context.Context, stdout io.Writer, store storage.Store, reobserve, learn bool, limit int) error {
	opts := compose.Options{
		BaseOptions: compose.BaseOptions{
			Reobserve: reobserve,
			Limit:     limit,
			Verbose:   verbose,
		},
		Learn: learn,
	}

	observeLLM, err := newLLMClient(ctx, TierFast)
	if err != nil {
		return err
	}
	composeLLM, err := newLLMClient(ctx, TierStrong)
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

// syncProgressRenderer returns a SyncProgressFunc that renders source sync
// progress using the stage log system. Each source gets a "sync" stage line
// that updates as discovery and fetching progress. The renderer is safe for
// concurrent use by multiple providers.
//
// The "done" phase only clears the transient bar — summary lines are printed
// after Upload returns via printSyncSummary, which has access to per-source
// cache information (new vs cached counts).
func syncProgressRenderer() muse.SyncProgressFunc {
	var mu sync.Mutex
	tty := output.IsTTY()
	done := map[string]bool{}

	return func(source string, p conversation.SyncProgress) {
		mu.Lock()
		defer mu.Unlock()

		name := strings.ToLower(source)

		switch p.Phase {
		case "discovering":
			if tty {
				fmt.Fprintf(os.Stderr, "\r%-*ssync %s: discovering...", output.StageWidth, "", name)
			}
		case "fetching":
			if p.Total > 0 && p.Current > 0 && tty {
				bar := output.RenderBar(p.Current, p.Total, output.BarWidth)
				fmt.Fprintf(os.Stderr, "\r%-*s%s %s %d/%d", output.StageWidth, "", bar, name, p.Current, p.Total)
			}
		case "log":
			// Persistent one-line event (e.g. auth success). Clear any
			// transient status, print on its own line.
			if tty {
				output.ClearLine()
			}
			output.LogStage("sync", "%s: %s", name, p.Detail).Print()
		case "done":
			if !done[name] {
				done[name] = true
				// Just clear the transient bar; summary printed by printSyncSummary.
				if tty {
					output.ClearLine()
				}
			}
		}
	}
}

// printSyncSummary prints per-source sync lines with cache information.
// Each source shows total conversations and how many were new (uploaded).
func printSyncSummary(result *muse.UploadResult) {
	sources := make([]string, 0, len(result.SourceTotals))
	for s := range result.SourceTotals {
		sources = append(sources, s)
	}
	sort.Strings(sources)

	for _, source := range sources {
		total := result.SourceTotals[source]
		newCount := result.SourceCounts[source] // 0 if not in map
		displayName := strings.ReplaceAll(source, "-", " ")

		detail := fmt.Sprintf("%d conversations (%d new)", total, newCount)

		output.LogStage("sync", "%s: %s", displayName, detail).Print()
	}
}
