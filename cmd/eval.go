package cmd

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
	"github.com/ellistarn/muse/prompts"
)

//go:embed evals/questions.json
var defaultQuestions []byte

// evalQuestion is a single evaluation question with metadata.
type evalQuestion struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Tags     []string `json:"tags,omitempty"`
	Prompt   string   `json:"prompt"`
}

// evalScores holds dimension scores from a judge call.
type evalScores struct {
	Values map[string]int
}

// evalResult holds the complete evaluation for one question.
type evalResult struct {
	Question       evalQuestion
	BaseResponse   string
	MuseResponse   string
	BaseIsA        bool // true if base was randomly assigned to "Response A"
	BaseObservable evalScores
	MuseObservable evalScores
	BaseEpistemic  evalScores
	MuseEpistemic  evalScores
	Preferred      string // "base", "muse", "neither"
	Rationale      string
	Error          error
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
		Short: "Evaluate how the muse changes response quality across dimensions",
		Long: `Runs each question twice — once with the muse, once without — then blind
judges score both responses independently on six dimensions plus an overall
preference. The output is a profile showing where the muse adds value and
where it doesn't.

Dimensions (scored 1-5, where 3 = strong base model):
  What it does:    positional clarity, completeness, specificity
  How it reasons:  calibration, reasoning transparency, intellectual honesty

A separate preference judge picks which response demonstrates better overall
judgment, capturing signal the dimension scores may miss.

Questions include universal judgment probes plus domain-specific questions
generated from the muse.md to measure transferability.`,
		Example: `  muse eval
  muse eval -v
  muse eval --dir ./my-questions`,
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

			// Load universal questions
			questions, err := loadUniversalQuestions()
			if err != nil {
				return fmt.Errorf("load questions: %w", err)
			}

			// Generate domain questions from the muse
			domainQuestions, err := generateDomainQuestions(ctx, llm, document, store)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not generate domain questions: %v\n", err)
			} else {
				questions = append(questions, domainQuestions...)
			}

			// Load custom questions from --dir
			if evalDir != "" {
				custom, err := loadCustomQuestions(evalDir)
				if err != nil {
					return fmt.Errorf("load custom questions: %w", err)
				}
				questions = append(questions, custom...)
			}

			if len(questions) == 0 {
				return fmt.Errorf("no questions found")
			}

			withMuse := muse.New(llm, document)
			withoutMuse := muse.New(llm, "")
			model := shortModel(llm.Model())
			museHash := compose.Fingerprint(document)[:12]

			fmt.Fprintf(os.Stderr, "eval  %d cases  %s\n\n", len(questions), model)

			// Run all cases with bounded concurrency.
			results := make([]evalResult, len(questions))
			g, ctx := errgroup.WithContext(ctx)
			g.SetLimit(10)
			for i, q := range questions {
				g.Go(func() error {
					results[i] = runEvalCase(ctx, q, llm, withMuse, withoutMuse, store, museHash)
					return nil
				})
			}
			g.Wait()

			// Print profile
			fmt.Fprintln(os.Stderr)
			printEvalProfile(results)

			// Verbose: per-case detail
			if verbose {
				fmt.Fprintln(os.Stderr)
				printEvalDetail(results)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&evalDir, "dir", "", "directory of additional .md question files")
	return cmd
}

// runEvalCase generates responses, blinds them, and runs both judge calls.
func runEvalCase(ctx context.Context, q evalQuestion, llm inference.Client, withMuse, withoutMuse *muse.Muse, store storage.Store, museHash string) evalResult {
	result := evalResult{Question: q}

	// Cache keys
	baseFP := compose.Fingerprint(q.Prompt, llm.Model())
	museFP := compose.Fingerprint(q.Prompt, llm.Model(), museHash)
	baseKey := fmt.Sprintf("eval/baseline/%s.json", baseFP[:16])
	museKey := fmt.Sprintf("eval/muse/%s.json", museFP[:16])

	// Generate responses in parallel
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
		r, err := withoutMuse.Ask(ctx, muse.AskInput{Question: q.Prompt})
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
		r, err := withMuse.Ask(ctx, muse.AskInput{Question: q.Prompt})
		if err != nil {
			museErr = err
			return
		}
		museResp = r.Response
		saveCachedResponse(ctx, store, museKey, museFP, museResp)
	}()
	inner.Wait()

	if baseErr != nil {
		result.Error = baseErr
		fmt.Fprintf(os.Stderr, "  ! %-28s error: %v\n", q.ID, baseErr)
		return result
	}
	if museErr != nil {
		result.Error = museErr
		fmt.Fprintf(os.Stderr, "  ! %-28s error: %v\n", q.ID, museErr)
		return result
	}

	result.BaseResponse = baseResp
	result.MuseResponse = museResp

	// Blind: randomly assign to A/B
	result.BaseIsA = rand.IntN(2) == 0
	var respA, respB string
	if result.BaseIsA {
		respA, respB = baseResp, museResp
	} else {
		respA, respB = museResp, baseResp
	}

	blindedInput := fmt.Sprintf("## Question\n%s\n\n## Response A\n%s\n\n## Response B\n%s",
		strings.TrimSpace(q.Prompt), respA, respB)

	// Run all three judge calls in parallel (never cached — cheap, benefits from prompt iteration)
	var obsResp, epiResp, prefResp string
	var obsErr, epiErr, prefErr error
	var judgeWg sync.WaitGroup
	judgeWg.Add(3)
	go func() {
		defer judgeWg.Done()
		obsResp, _, obsErr = inference.Converse(ctx, llm, prompts.JudgeObservable, blindedInput)
	}()
	go func() {
		defer judgeWg.Done()
		epiResp, _, epiErr = inference.Converse(ctx, llm, prompts.JudgeEpistemic, blindedInput)
	}()
	go func() {
		defer judgeWg.Done()
		prefResp, _, prefErr = inference.Converse(ctx, llm, prompts.JudgePreference, blindedInput)
	}()
	judgeWg.Wait()

	if obsErr != nil {
		result.Error = obsErr
		fmt.Fprintf(os.Stderr, "  ! %-28s judge error: %v\n", q.ID, obsErr)
		return result
	}
	if epiErr != nil {
		result.Error = epiErr
		fmt.Fprintf(os.Stderr, "  ! %-28s judge error: %v\n", q.ID, epiErr)
		return result
	}
	if prefErr != nil {
		result.Error = prefErr
		fmt.Fprintf(os.Stderr, "  ! %-28s judge error: %v\n", q.ID, prefErr)
		return result
	}

	// Parse scores
	aObs, bObs := parseJudgeScores(obsResp)
	aEpi, bEpi := parseJudgeScores(epiResp)
	preferred, rationale := parsePreference(prefResp)

	// De-blind
	if result.BaseIsA {
		result.BaseObservable = aObs
		result.MuseObservable = bObs
		result.BaseEpistemic = aEpi
		result.MuseEpistemic = bEpi
	} else {
		result.BaseObservable = bObs
		result.MuseObservable = aObs
		result.BaseEpistemic = bEpi
		result.MuseEpistemic = aEpi
	}

	// De-blind preference
	switch preferred {
	case "A":
		if result.BaseIsA {
			result.Preferred = "base"
		} else {
			result.Preferred = "muse"
		}
	case "B":
		if result.BaseIsA {
			result.Preferred = "muse"
		} else {
			result.Preferred = "base"
		}
	default:
		result.Preferred = "neither"
	}
	result.Rationale = rationale

	// Progress
	icon := "~"
	switch result.Preferred {
	case "muse":
		icon = "✓"
	case "base":
		icon = "✗"
	}
	fmt.Fprintf(os.Stderr, "  %s %-28s %s\n", icon, q.ID, truncate(rationale, 60))

	return result
}

// --- Scoring ---

var scorePattern = regexp.MustCompile(`(\w+)=(\d)`)

func parseScoreMap(line string) evalScores {
	s := evalScores{Values: map[string]int{}}
	for _, match := range scorePattern.FindAllStringSubmatch(line, -1) {
		if n, err := strconv.Atoi(match[2]); err == nil {
			s.Values[match[1]] = n
		}
	}
	return s
}

// parseJudgeScores extracts RESPONSE_A and RESPONSE_B score lines.
func parseJudgeScores(raw string) (a, b evalScores) {
	a = evalScores{Values: map[string]int{}}
	b = evalScores{Values: map[string]int{}}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "RESPONSE_A:") {
			a = parseScoreMap(trimmed)
		} else if strings.HasPrefix(trimmed, "RESPONSE_B:") {
			b = parseScoreMap(trimmed)
		}
	}
	return
}

// parsePreference extracts PREFERRED and RATIONALE from epistemic judge output.
func parsePreference(raw string) (preferred, rationale string) {
	preferred = "neither"
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "PREFERRED:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "PREFERRED:"))
			val = strings.ToUpper(val)
			if strings.HasPrefix(val, "A") {
				preferred = "A"
			} else if strings.HasPrefix(val, "B") {
				preferred = "B"
			} else {
				preferred = "neither"
			}
		} else if strings.HasPrefix(trimmed, "RATIONALE:") {
			rationale = strings.TrimSpace(strings.TrimPrefix(trimmed, "RATIONALE:"))
		}
	}
	return
}

// --- Output ---

var (
	observableDims = []string{"positional_clarity", "completeness", "specificity"}
	epistemicDims  = []string{"calibration", "reasoning", "honesty"}
	allDims        = append(append([]string{}, observableDims...), epistemicDims...)
)

var dimLabels = map[string]string{
	"positional_clarity": "Positional clarity",
	"completeness":       "Completeness",
	"specificity":        "Specificity",
	"calibration":        "Calibration",
	"reasoning":          "Reasoning",
	"honesty":            "Intellectual honesty",
}

func printEvalProfile(results []evalResult) {
	var valid []evalResult
	for _, r := range results {
		if r.Error == nil {
			valid = append(valid, r)
		}
	}
	if len(valid) == 0 {
		fmt.Fprintln(os.Stderr, "No valid results.")
		return
	}

	printDimensionTable("What it does", observableDims, valid)
	fmt.Fprintln(os.Stderr)
	printDimensionTable("How it reasons", epistemicDims, valid)
	fmt.Fprintln(os.Stderr)
	printTransferability(valid)
	fmt.Fprintln(os.Stderr)

	var museN, baseN, neitherN int
	for _, r := range valid {
		switch r.Preferred {
		case "muse":
			museN++
		case "base":
			baseN++
		default:
			neitherN++
		}
	}
	fmt.Fprintf(os.Stderr, "Preferred: Muse %d/%d, Base %d/%d, Neither %d/%d\n",
		museN, len(valid), baseN, len(valid), neitherN, len(valid))
}

func printDimensionTable(title string, dims []string, results []evalResult) {
	fmt.Fprintf(os.Stderr, "%-24s  Base  Muse  Delta\n", title)
	fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 50))

	for _, dim := range dims {
		var baseSum, museSum, count int
		for _, r := range results {
			bs, bOk := dimScore(r.BaseObservable, r.BaseEpistemic, dim)
			ms, mOk := dimScore(r.MuseObservable, r.MuseEpistemic, dim)
			if bOk && mOk {
				baseSum += bs
				museSum += ms
				count++
			}
		}
		if count == 0 {
			continue
		}
		baseAvg := float64(baseSum) / float64(count)
		museAvg := float64(museSum) / float64(count)
		delta := museAvg - baseAvg
		sign := "+"
		if delta < 0 {
			sign = ""
		}
		label := dimLabels[dim]
		if label == "" {
			label = dim
		}
		fmt.Fprintf(os.Stderr, "  %-22s  %4.1f  %4.1f  %s%.1f\n",
			label, baseAvg, museAvg, sign, delta)
	}
}

func printTransferability(results []evalResult) {
	type group struct {
		count int
		delta float64
	}
	displayOrder := []string{"domain", "universal"}
	displayLabels := map[string]string{
		"domain":    "Domain",
		"universal": "Universal",
	}
	groups := map[string]*group{}
	for _, name := range displayOrder {
		groups[name] = &group{}
	}

	for _, r := range results {
		gName := transferGroup(r.Question.Category)
		g := groups[gName]

		var deltaSum float64
		var dimCount int
		for _, dim := range allDims {
			bs, bOk := dimScore(r.BaseObservable, r.BaseEpistemic, dim)
			ms, mOk := dimScore(r.MuseObservable, r.MuseEpistemic, dim)
			if bOk && mOk {
				deltaSum += float64(ms - bs)
				dimCount++
			}
		}
		if dimCount > 0 {
			g.count++
			g.delta += deltaSum / float64(dimCount)
		}
	}

	fmt.Fprintf(os.Stderr, "%-24s  Cases  Avg Δ\n", "Transferability")
	fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 42))
	for _, name := range displayOrder {
		g := groups[name]
		if g.count == 0 {
			continue
		}
		avg := g.delta / float64(g.count)
		sign := "+"
		if avg < 0 {
			sign = ""
		}
		label := displayLabels[name]
		if label == "" {
			label = name
		}
		fmt.Fprintf(os.Stderr, "  %-22s  %5d  %s%.1f\n", label, g.count, sign, avg)
	}
}

func printEvalDetail(results []evalResult) {
	for _, r := range results {
		if r.Error != nil {
			continue
		}
		fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 80))
		fmt.Fprintf(os.Stderr, "Q [%s] (%s): %s\n", r.Question.ID, r.Question.Category,
			strings.TrimSpace(r.Question.Prompt))
		if len(r.Question.Tags) > 0 {
			fmt.Fprintf(os.Stderr, "Tags: %s\n", strings.Join(r.Question.Tags, ", "))
		}
		fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 80))

		fmt.Fprintf(os.Stderr, "BASE:\n%s\n", r.BaseResponse)
		fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 40))
		fmt.Fprintf(os.Stderr, "MUSE:\n%s\n", r.MuseResponse)
		fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("─", 40))

		fmt.Fprintf(os.Stderr, "What it does:    ")
		for _, dim := range observableDims {
			bs, _ := dimScore(r.BaseObservable, r.BaseEpistemic, dim)
			ms, _ := dimScore(r.MuseObservable, r.MuseEpistemic, dim)
			label := dimLabels[dim]
			if label == "" {
				label = dim
			}
			fmt.Fprintf(os.Stderr, "%s: %d→%d  ", label, bs, ms)
		}
		fmt.Fprintln(os.Stderr)

		fmt.Fprintf(os.Stderr, "How it reasons:  ")
		for _, dim := range epistemicDims {
			bs, _ := dimScore(r.BaseObservable, r.BaseEpistemic, dim)
			ms, _ := dimScore(r.MuseObservable, r.MuseEpistemic, dim)
			label := dimLabels[dim]
			if label == "" {
				label = dim
			}
			fmt.Fprintf(os.Stderr, "%s: %d→%d  ", label, bs, ms)
		}
		fmt.Fprintln(os.Stderr)

		fmt.Fprintf(os.Stderr, "Preferred: %s\n", r.Preferred)
		if r.Rationale != "" {
			fmt.Fprintf(os.Stderr, "Rationale: %s\n", r.Rationale)
		}
		fmt.Fprintln(os.Stderr)
	}
}

// --- Helpers ---

// dimScore looks up a dimension score across observable and epistemic scores.
func dimScore(obs, epi evalScores, dim string) (int, bool) {
	if obs.Values != nil {
		if v, ok := obs.Values[dim]; ok {
			return v, true
		}
	}
	if epi.Values != nil {
		if v, ok := epi.Values[dim]; ok {
			return v, true
		}
	}
	return 0, false
}

// transferGroup maps a question category to a transferability group.
func transferGroup(category string) string {
	switch category {
	case "in-domain", "adjacent-domain", "out-of-domain":
		return "domain"
	default:
		return "universal"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// shortModel strips common provider prefixes from model identifiers.
func shortModel(model string) string {
	if idx := strings.LastIndex(model, "."); idx != -1 {
		parts := strings.Split(model, ".")
		if len(parts) > 2 {
			return parts[len(parts)-1]
		}
	}
	return model
}

// --- Question loading ---

func loadUniversalQuestions() ([]evalQuestion, error) {
	var questions []evalQuestion
	if err := json.Unmarshal(defaultQuestions, &questions); err != nil {
		return nil, fmt.Errorf("parse built-in questions: %w", err)
	}
	return questions, nil
}

func generateDomainQuestions(ctx context.Context, llm inference.Client, document string, store storage.Store) ([]evalQuestion, error) {
	// Check cache (keyed on muse content)
	museHash := compose.Fingerprint(document)[:16]
	cacheKey := fmt.Sprintf("eval/domain-questions/%s.json", museHash)
	if data, err := store.GetData(ctx, cacheKey); err == nil {
		var questions []evalQuestion
		if err := json.Unmarshal(data, &questions); err == nil {
			return questions, nil
		}
	}

	resp, _, err := inference.Converse(ctx, llm, prompts.GenerateEval, document)
	if err != nil {
		return nil, err
	}

	jsonStr := extractJSON(resp)
	var questions []evalQuestion
	if err := json.Unmarshal([]byte(jsonStr), &questions); err != nil {
		return nil, fmt.Errorf("parse generated questions (raw response length %d): %w", len(resp), err)
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("generated question set is empty")
	}

	// Cache for next run
	if data, err := json.Marshal(questions); err == nil {
		store.PutData(ctx, cacheKey, data)
	}
	return questions, nil
}

// extractJSON finds a JSON array in text that may be wrapped in markdown code blocks.
// Returns the extracted substring, which the caller must validate by unmarshaling.
func extractJSON(s string) string {
	// Try the whole string first (model followed instructions)
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return trimmed
	}
	// Strip markdown code fences
	if idx := strings.Index(s, "```"); idx != -1 {
		// Find content between first and last fence
		start := strings.Index(s[idx+3:], "\n")
		if start != -1 {
			inner := s[idx+3+start+1:]
			if end := strings.Index(inner, "```"); end != -1 {
				inner = strings.TrimSpace(inner[:end])
				if strings.HasPrefix(inner, "[") && strings.HasSuffix(inner, "]") {
					return inner
				}
			}
		}
	}
	// Last resort: first [ to last ]
	if idx := strings.Index(s, "["); idx != -1 {
		if end := strings.LastIndex(s, "]"); end > idx {
			return s[idx : end+1]
		}
	}
	return s
}

func loadCustomQuestions(dir string) ([]evalQuestion, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var questions []evalQuestion
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		questions = append(questions, evalQuestion{
			ID:       strings.TrimSuffix(e.Name(), ".md"),
			Category: "custom",
			Prompt:   string(data),
		})
	}
	return questions, nil
}

// --- Caching ---

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
