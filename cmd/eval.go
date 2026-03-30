package cmd

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/muse"
)

//go:embed evals/*.md
var defaultEvals embed.FS

type evalCase struct {
	Name   string
	Prompt string
}

type evalResult struct {
	Case     evalCase
	Baseline string
	WithMuse string
}

func newEvalCmd() *cobra.Command {
	var evalDir string

	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run eval cases with and without the muse",
		Long: `Runs each eval case twice — once with the muse, once without — and prints
both responses side by side. This shows where the muse steers the model's
judgment on design questions. No scoring, no judge — the human reads the delta.

Eval cases are single-question markdown files. By default, the built-in cases
are used. Use --dir to point at a custom directory.`,
		Example: `  muse eval
  muse eval --dir ./my-evals`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}
			document := loadDocument(ctx, store)
			if document == "" {
				return fmt.Errorf("no muse.md found — run 'muse compose' first")
			}
			llm, err := newLLMClient(ctx, TierObserve)
			if err != nil {
				return err
			}

			// Load eval cases
			cases, err := loadEvals(evalDir)
			if err != nil {
				return fmt.Errorf("load evals: %w", err)
			}
			if len(cases) == 0 {
				return fmt.Errorf("no eval cases found")
			}

			withMuse := muse.New(llm, document)
			withoutMuse := muse.New(llm, "")

			fmt.Fprintf(os.Stderr, "eval     %d cases, model=%s\n", len(cases), llm.Model())

			// Run all cases in parallel
			results := make([]evalResult, len(cases))
			var wg sync.WaitGroup
			for i, tc := range cases {
				wg.Add(1)
				go func(i int, tc evalCase) {
					defer wg.Done()
					baseResp, err := withoutMuse.Ask(ctx, muse.AskInput{Question: tc.Prompt})
					if err != nil {
						fmt.Fprintf(os.Stderr, "  %s baseline error: %v\n", tc.Name, err)
						return
					}
					museResp, err := withMuse.Ask(ctx, muse.AskInput{Question: tc.Prompt})
					if err != nil {
						fmt.Fprintf(os.Stderr, "  %s muse error: %v\n", tc.Name, err)
						return
					}
					results[i] = evalResult{
						Case:     tc,
						Baseline: baseResp.Response,
						WithMuse: museResp.Response,
					}
				}(i, tc)
			}
			wg.Wait()

			// Print results
			for _, r := range results {
				if r.Case.Name == "" {
					continue // skipped due to error
				}
				fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("═", 80))
				fmt.Fprintf(os.Stderr, "%s\n", r.Case.Name)
				fmt.Fprintf(os.Stderr, "Q: %s\n", strings.TrimSpace(r.Case.Prompt))
				fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 80))
				fmt.Fprintf(os.Stderr, "WITHOUT MUSE:\n%s\n", r.Baseline)
				fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 80))
				fmt.Fprintf(os.Stderr, "WITH MUSE:\n%s\n", r.WithMuse)
				fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("═", 80))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&evalDir, "dir", "", "directory of eval .md files (default: built-in)")
	return cmd
}

func loadEvals(dir string) ([]evalCase, error) {
	if dir != "" {
		return loadEvalsFromDir(dir)
	}
	return loadEvalsFromEmbed()
}

func loadEvalsFromEmbed() ([]evalCase, error) {
	entries, err := defaultEvals.ReadDir("evals")
	if err != nil {
		return nil, err
	}
	var cases []evalCase
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := defaultEvals.ReadFile("evals/" + e.Name())
		if err != nil {
			return nil, err
		}
		cases = append(cases, evalCase{
			Name:   strings.TrimSuffix(e.Name(), ".md"),
			Prompt: string(data),
		})
	}
	return cases, nil
}

func loadEvalsFromDir(dir string) ([]evalCase, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var cases []evalCase
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		cases = append(cases, evalCase{
			Name:   strings.TrimSuffix(e.Name(), ".md"),
			Prompt: string(data),
		})
	}
	return cases, nil
}
