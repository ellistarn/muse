package cmd

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
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
	Verdict  string
	Summary  string
	Analysis string
}

// cachedResponse is the on-disk format for a cached eval response.
type cachedResponse struct {
	Fingerprint string `json:"fingerprint"`
	Response    string `json:"response"`
}

func newEvalCmd() *cobra.Command {
	var evalDir string

	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Evaluate how the muse changes responses to design questions",
		Long: `Runs each case twice — once with the muse, once without — then an LLM judge
characterizes the difference. This shows where the muse steers the model's
judgment and whether that steering adds real value.

Cases are single-question markdown files. By default, the built-in cases
are used. Use --dir to point at a custom directory.`,
		Example: `  muse eval
  muse eval --dir ./my-cases`,
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
			llm, err := newLLMClient(ctx, TierStrong)
			if err != nil {
				return err
			}
			if err != nil {
				return err
			}

			cases, err := loadEvalCases(evalDir)
			if err != nil {
				return fmt.Errorf("load cases: %w", err)
			}
			if len(cases) == 0 {
				return fmt.Errorf("no cases found")
			}

			withMuse := muse.New(llm, document)
			withoutMuse := muse.New(llm, "")
			model := shortModel(llm.Model())
			museHash := compose.Fingerprint(document)[:12]

			fmt.Fprintf(os.Stderr, "eval  %d cases  %s\n", len(cases), model)

			// Run all cases in parallel; within each case, baseline and muse
			// calls also run in parallel since they are independent.
			results := make([]evalResult, len(cases))
			var wg sync.WaitGroup
			for i, tc := range cases {
				wg.Add(1)
				go func(i int, tc evalCase) {
					defer wg.Done()

					// Cache keys: baseline depends on (case, model), muse on (case, muse, model)
					baseFP := compose.Fingerprint(tc.Prompt, llm.Model())
					museFP := compose.Fingerprint(tc.Prompt, llm.Model(), museHash)
					baseKey := fmt.Sprintf("eval/baseline/%s.json", baseFP[:16])
					museKey := fmt.Sprintf("eval/muse/%s.json", museFP[:16])

					var baseResp, museResp string
					var baseErr, museErr error
					var inner sync.WaitGroup
					inner.Add(2)
					go func() {
						defer inner.Done()
						if cached, err := loadCachedResponse(ctx, store, baseKey, baseFP); err == nil {
							baseResp = cached
							return
						}
						r, err := withoutMuse.Ask(ctx, muse.AskInput{Question: tc.Prompt})
						if err != nil {
							baseErr = err
							return
						}
						baseResp = r.Response
						saveCachedResponse(ctx, store, baseKey, baseFP, baseResp)
					}()
					go func() {
						defer inner.Done()
						if cached, err := loadCachedResponse(ctx, store, museKey, museFP); err == nil {
							museResp = cached
							return
						}
						r, err := withMuse.Ask(ctx, muse.AskInput{Question: tc.Prompt})
						if err != nil {
							museErr = err
							return
						}
						museResp = r.Response
						saveCachedResponse(ctx, store, museKey, museFP, museResp)
					}()
					inner.Wait()

					if baseErr != nil {
						fmt.Fprintf(os.Stderr, "  %-24s error  %v\n", tc.Name, baseErr)
						return
					}
					if museErr != nil {
						fmt.Fprintf(os.Stderr, "  %-24s error  %v\n", tc.Name, museErr)
						return
					}

					// Judge the difference (never cached — cheap and benefits from prompt iteration)
					judgeInput := fmt.Sprintf("## Question\n%s\n\n## Base Response\n%s\n\n## Muse Response\n%s",
						strings.TrimSpace(tc.Prompt), baseResp, museResp)
					judgeResp, _, judgeErr := inference.Converse(ctx, llm, prompts.Judge, judgeInput)
					if judgeErr != nil {
						fmt.Fprintf(os.Stderr, "  %-24s error  %v\n", tc.Name, judgeErr)
						return
					}

					verdict, summary, analysis := parseJudge(judgeResp)
					results[i] = evalResult{
						Case:     tc,
						Baseline: baseResp,
						WithMuse: museResp,
						Verdict:  verdict,
						Summary:  summary,
						Analysis: analysis,
					}
				}(i, tc)
			}
			wg.Wait()

			// Print results
			fmt.Fprintln(os.Stderr)
			for _, r := range results {
				if r.Case.Name == "" {
					continue // skipped due to error
				}
				icon := "~"
				switch r.Verdict {
				case "better":
					icon = "✓"
				case "worse":
					icon = "✗"
				}
				fmt.Fprintf(os.Stderr, "  %s %-24s %s\n", icon, r.Case.Name, r.Summary)

				if verbose {
					fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("─", 80))
					fmt.Fprintf(os.Stderr, "Q: %s\n", strings.TrimSpace(r.Case.Prompt))
					fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 80))
					fmt.Fprintf(os.Stderr, "WITHOUT MUSE:\n%s\n", r.Baseline)
					fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 80))
					fmt.Fprintf(os.Stderr, "WITH MUSE:\n%s\n", r.WithMuse)
					if r.Analysis != "" {
						fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 80))
						fmt.Fprintf(os.Stderr, "ANALYSIS:\n%s\n", r.Analysis)
					}
					fmt.Fprintf(os.Stderr, "%s\n\n", strings.Repeat("─", 80))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&evalDir, "dir", "", "directory of case .md files (default: built-in)")
	return cmd
}

// parseJudge extracts verdict, summary, and analysis from structured judge output.
func parseJudge(raw string) (verdict, summary, analysis string) {
	verdict = "?"
	lines := strings.Split(raw, "\n")
	var analysisLines []string
	inAnalysis := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "VERDICT:"):
			verdict = strings.TrimSpace(strings.TrimPrefix(trimmed, "VERDICT:"))
		case strings.HasPrefix(trimmed, "SUMMARY:"):
			summary = strings.TrimSpace(strings.TrimPrefix(trimmed, "SUMMARY:"))
		case strings.HasPrefix(trimmed, "ANALYSIS:"):
			inAnalysis = true
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "ANALYSIS:"))
			if rest != "" {
				analysisLines = append(analysisLines, rest)
			}
		case inAnalysis:
			analysisLines = append(analysisLines, line)
		}
	}
	analysis = strings.TrimSpace(strings.Join(analysisLines, "\n"))
	return
}

// shortModel strips common provider prefixes from model identifiers.
func shortModel(model string) string {
	// Strip "us." or region prefix from bedrock ARN-style names
	if idx := strings.LastIndex(model, "."); idx != -1 {
		parts := strings.Split(model, ".")
		if len(parts) > 2 {
			// e.g. "us.anthropic.claude-sonnet-4-6" → "claude-sonnet-4-6"
			return parts[len(parts)-1]
		}
	}
	return model
}

func loadCachedResponse(ctx context.Context, store storage.Store, key, fingerprint string) (string, error) {
	data, err := store.GetData(ctx, key)
	if err != nil {
		return "", err
	}
	var cached cachedResponse
	if err := json.Unmarshal(data, &cached); err != nil {
		return "", err
	}
	if cached.Fingerprint != fingerprint {
		return "", fmt.Errorf("fingerprint mismatch")
	}
	return cached.Response, nil
}

func saveCachedResponse(ctx context.Context, store storage.Store, key, fingerprint, response string) {
	data, err := json.Marshal(cachedResponse{Fingerprint: fingerprint, Response: response})
	if err != nil {
		return
	}
	store.PutData(ctx, key, data)
}

func loadEvalCases(dir string) ([]evalCase, error) {
	if dir != "" {
		return loadCasesFromDir(dir)
	}
	return loadCasesFromEmbed()
}

func loadCasesFromEmbed() ([]evalCase, error) {
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

func loadCasesFromDir(dir string) ([]evalCase, error) {
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
