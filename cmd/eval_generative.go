package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
)

// genEvalCase is a single test case for generative evaluation.
type genEvalCase struct {
	ConversationID string
	Context        string // PR diff + prior comments (everything before the target's comment)
	GroundTruth    string // what the person actually said
	ContextType    string // "first-comment", "thread-response", "question-response"
}

// genEvalResult holds scores for one test case.
type genEvalResult struct {
	Case         genEvalCase
	MuseResponse string
	BaseResponse string
	MuseScore    genScore
	BaseScore    genScore
	Error        error
}

// genScore holds alignment scores for one response against ground truth.
type genScore struct {
	Recall        float64 // weighted fraction of ground truth concerns identified
	Precision     float64 // weighted fraction of response concerns matching ground truth
	PriorityMatch bool    // top concern matches
	MuseConcerns  int
	TruthConcerns int
	MatchedWeight float64 // sum of (match_score * severity_weight) for matched truth concerns
	TotalWeight   float64 // sum of severity_weight for all truth concerns
}

// concern is an extracted review concern.
type concern struct {
	Text     string `json:"concern"`
	Severity string `json:"severity"` // "blocking", "advisory", "suggestive"
}

// severityWeight returns the weight for a concern severity level.
func severityWeight(severity string) float64 {
	switch severity {
	case "blocking":
		return 1.0
	case "advisory":
		return 0.5
	case "suggestive":
		return 0.25
	default:
		return 0.5 // default to advisory
	}
}

const extractPrompt = `Extract the distinct review concerns from this code review comment.
Each concern is one actionable item — a distinct thing the reviewer wants changed or flagged.
A broad claim supported by specific instances is one concern, not multiple.

Classify each concern by severity:
- "blocking": the reviewer is blocking on this, it must change ("this does not belong here", "this is wrong", "this will break")
- "advisory": the reviewer wants a change but is not blocking ("consider", "should", "let's")
- "suggestive": the reviewer is floating an idea or preference ("maybe", "might be nice", "could")

Return a JSON array of objects with "concern" and "severity" fields.
If the comment has no substantive review concerns (e.g. "LGTM", "looks good"), return an empty array.

Review comment:
%s`

const alignPrompt = `You are comparing two lists of code review concerns to measure alignment.

Ground truth concerns (what the reviewer actually said):
%s

Predicted concerns (what the muse predicted):
%s

For each ground truth concern, find the best matching predicted concern. Score each match:
- 1.0 if the predicted concern identifies the same issue with similar reasoning
- 0.5 if the predicted concern is in the same area but frames the issue differently
- 0.0 if no predicted concern matches

Also identify which predicted concerns have no match in ground truth.

Return JSON:
{
  "matches": [{"truth": "...", "predicted": "...", "score": 0.0}],
  "unmatched_predicted": ["..."],
  "top_truth_concern": "...",
  "top_predicted_concern": "..."
}`

// runGenerativeEval runs the held-out generative evaluation.
// If museOverride is non-empty, it is read as a file path and used as the muse
// document instead of the peer's own muse. This allows testing the team muse
// against an individual reviewer's held-out conversations.
func runGenerativeEval(ctx context.Context, peer, project string, limit int, museOverride string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	pd := fmt.Sprintf("github-%s", peer)
	if project != "" {
		pd = fmt.Sprintf("github-%s/%s", peer, project)
	}
	peerRoot := filepath.Join(home, ".muse", "peers", pd)
	store := storage.NewLocalStoreWithRoot(peerRoot)

	// Load muse: override file or peer's own muse
	var document string
	if museOverride != "" {
		data, err := os.ReadFile(museOverride)
		if err != nil {
			return fmt.Errorf("failed to read muse override %s: %w", museOverride, err)
		}
		document = string(data)
	} else {
		doc, err := store.GetMuse(ctx)
		if err != nil {
			return fmt.Errorf("no muse found for peer %s (run: muse compose github/%s --project %s)", peer, peer, project)
		}
		document = doc
	}

	// Load all conversations
	entries, err := store.ListConversations(ctx)
	if err != nil {
		return fmt.Errorf("list conversations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastModified.After(entries[j].LastModified)
	})

	// Split: 20% holdout, min 5, max 30
	testSize := len(entries) / 5
	if testSize < 5 {
		testSize = 5
	}
	if testSize > 30 {
		testSize = 30
	}
	if testSize > len(entries) {
		return fmt.Errorf("not enough conversations (%d) for evaluation (need at least 5)", len(entries))
	}
	if limit > 0 && testSize > limit {
		testSize = limit
	}

	// Scan more entries than testSize to find enough usable cases,
	// since many conversations won't have context before the target's comment.
	scanSize := testSize * 5
	if scanSize > len(entries) {
		scanSize = len(entries)
	}
	scanEntries := entries[:scanSize]

	// Build test cases from held-out conversations
	var cases []genEvalCase
	for _, entry := range scanEntries {
		conv, err := store.GetConversation(ctx, entry.Source, entry.ConversationID)
		if err != nil {
			continue
		}
		c := buildGenEvalCase(conv, peer)
		if c != nil {
			cases = append(cases, *c)
			if testSize > 0 && len(cases) >= testSize {
				break
			}
		}
	}

	if len(cases) == 0 {
		return fmt.Errorf("no usable test cases found")
	}

	fmt.Fprintf(os.Stderr, "generative eval  %d cases  peer=github/%s/%s\n\n", len(cases), peer, project)

	// Run evaluation
	llm, err := newLLMClient(ctx, TierStrong)
	if err != nil {
		return err
	}

	withMuse := muse.New(llm, document)
	withoutMuse := muse.New(llm, "")
	museHash := compose.Fingerprint(document)[:12]

	results := make([]genEvalResult, len(cases))
	var mu sync.Mutex
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8)

	for i, c := range cases {
		g.Go(func() error {
			r := runGenEvalCase(ctx, llm, withMuse, withoutMuse, c, store, museHash)
			mu.Lock()
			results[i] = r
			mu.Unlock()

			symbol := "  "
			if r.Error != nil {
				symbol = "!"
			} else if r.MuseScore.Recall > r.BaseScore.Recall {
				symbol = "+"
			} else if r.MuseScore.Recall < r.BaseScore.Recall {
				symbol = "-"
			}
			fmt.Fprintf(os.Stderr, "  %s %-40s recall=%.2f/%.2f  precision=%.2f/%.2f\n",
				symbol,
				truncate(c.ConversationID, 40),
				r.MuseScore.Recall, r.BaseScore.Recall,
				r.MuseScore.Precision, r.BaseScore.Precision,
			)
			return nil
		})
	}
	g.Wait()

	// Print summary
	printGenEvalSummary(os.Stdout, results)
	return nil
}

// buildGenEvalCase extracts context and ground truth from a conversation.
// The context is everything before the target's first substantive message.
// The ground truth is the target's first substantive message.
func buildGenEvalCase(conv *conversation.Conversation, targetUser string) *genEvalCase {
	var contextParts []string
	var groundTruth string
	contextType := "first-comment"

	for _, msg := range conv.Messages {
		if msg.Role == "user" && groundTruth == "" {
			// This is the target's first message — it's the ground truth
			if len(strings.Fields(msg.Content)) < 10 {
				return nil // too short to be a substantive review
			}
			groundTruth = msg.Content
			if len(contextParts) > 0 {
				contextType = "thread-response"
			}
		} else if groundTruth == "" {
			// Context: everything before the target's first message
			contextParts = append(contextParts, msg.Content)
		}
	}

	if groundTruth == "" || len(contextParts) == 0 {
		return nil
	}

	return &genEvalCase{
		ConversationID: conv.ConversationID,
		Context:        strings.Join(contextParts, "\n\n---\n\n"),
		GroundTruth:    groundTruth,
		ContextType:    contextType,
	}
}

// runGenEvalCase evaluates a single test case with response caching.
func runGenEvalCase(ctx context.Context, llm inference.Client, withMuse, withoutMuse *muse.Muse, c genEvalCase, store storage.Store, museHash string) genEvalResult {
	result := genEvalResult{Case: c}
	question := fmt.Sprintf("Review this code/discussion. What would you flag?\n\n%s", c.Context)

	// Cache keys: conversation + model + muse hash
	baseFP := compose.Fingerprint(c.ConversationID, llm.Model())
	museFP := compose.Fingerprint(c.ConversationID, llm.Model(), museHash)
	baseKey := fmt.Sprintf("eval/generative/baseline/%s.json", baseFP[:16])
	museKey := fmt.Sprintf("eval/generative/muse/%s.json", museFP[:16])

	// Generate muse response (cached)
	if cached, err := loadCachedResponse(ctx, store, museKey, museFP); err == nil {
		result.MuseResponse = cached
	} else {
		museResult, err := withMuse.Ask(ctx, muse.AskInput{Question: question, New: true})
		if err != nil {
			result.Error = fmt.Errorf("muse ask: %w", err)
			return result
		}
		result.MuseResponse = museResult.Response
		saveCachedResponse(ctx, store, museKey, museFP, result.MuseResponse)
	}

	// Generate baseline response (cached)
	if cached, err := loadCachedResponse(ctx, store, baseKey, baseFP); err == nil {
		result.BaseResponse = cached
	} else {
		baseResult, err := withoutMuse.Ask(ctx, muse.AskInput{Question: question, New: true})
		if err != nil {
			result.Error = fmt.Errorf("base ask: %w", err)
			return result
		}
		result.BaseResponse = baseResult.Response
		saveCachedResponse(ctx, store, baseKey, baseFP, result.BaseResponse)
	}

	// Score both against ground truth
	result.MuseScore = scoreAlignment(ctx, llm, c.GroundTruth, result.MuseResponse)
	result.BaseScore = scoreAlignment(ctx, llm, c.GroundTruth, result.BaseResponse)

	return result
}

// scoreAlignment runs the three-step judge pipeline.
func scoreAlignment(ctx context.Context, llm inference.Client, groundTruth, response string) genScore {
	// Step 1: Extract concerns from ground truth
	truthConcerns := extractConcerns(ctx, llm, groundTruth)
	// Step 2: Extract concerns from response (same prompt)
	responseConcerns := extractConcerns(ctx, llm, response)

	totalWeight := 0.0
	for _, c := range truthConcerns {
		totalWeight += severityWeight(c.Severity)
	}

	if len(truthConcerns) == 0 {
		return genScore{Recall: 1.0, Precision: 0.0, TruthConcerns: 0, MuseConcerns: len(responseConcerns), TotalWeight: 0}
	}
	if len(responseConcerns) == 0 {
		return genScore{Recall: 0.0, Precision: 0.0, TruthConcerns: len(truthConcerns), MuseConcerns: 0, TotalWeight: totalWeight}
	}

	// Step 3: Align and score
	alignment := alignConcerns(ctx, llm, truthConcerns, responseConcerns)
	return alignment
}

// extractConcerns calls the LLM to extract review concerns with severity.
func extractConcerns(ctx context.Context, llm inference.Client, text string) []concern {
	prompt := fmt.Sprintf(extractPrompt, text)
	resp, _, err := inference.Converse(ctx, llm, "You extract structured review concerns.", prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "    extractConcerns error: %v\n", err)
		return nil
	}
	text = resp

	// Parse JSON array from response
	raw := strings.TrimSpace(text)
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end < 0 || end <= start {
		return nil
	}
	raw = raw[start : end+1]

	var concerns []concern
	if err := json.Unmarshal([]byte(raw), &concerns); err != nil {
		return nil
	}

	var result []concern
	for _, c := range concerns {
		if c.Text != "" {
			if c.Severity == "" {
				c.Severity = "advisory"
			}
			result = append(result, c)
		}
	}
	return result
}

// alignResult is the parsed output of the alignment judge.
type alignResult struct {
	Matches             []alignMatch `json:"matches"`
	UnmatchedPredicted  []string     `json:"unmatched_predicted"`
	TopTruthConcern     string       `json:"top_truth_concern"`
	TopPredictedConcern string       `json:"top_predicted_concern"`
}

type alignMatch struct {
	Truth     string  `json:"truth"`
	Predicted string  `json:"predicted"`
	Score     float64 `json:"score"`
}

// alignConcerns calls the LLM to align two concern lists, weighting by severity.
func alignConcerns(ctx context.Context, llm inference.Client, truthConcerns, responseConcerns []concern) genScore {
	// Build severity lookup by concern text
	severityMap := make(map[string]string)
	for _, c := range truthConcerns {
		severityMap[c.Text] = c.Severity
	}

	// Extract text for alignment prompt
	truthTexts := make([]string, len(truthConcerns))
	for i, c := range truthConcerns {
		truthTexts[i] = c.Text
	}
	responseTexts := make([]string, len(responseConcerns))
	for i, c := range responseConcerns {
		responseTexts[i] = c.Text
	}

	truthJSON, _ := json.Marshal(truthTexts)
	responseJSON, _ := json.Marshal(responseTexts)

	prompt := fmt.Sprintf(alignPrompt, string(truthJSON), string(responseJSON))
	text, _, err := inference.Converse(ctx, llm, "You align code review concerns.", prompt)
	if err != nil {
		return genScore{TruthConcerns: len(truthConcerns), MuseConcerns: len(responseConcerns)}
	}

	raw := strings.TrimSpace(text)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end < 0 || end <= start {
		return genScore{TruthConcerns: len(truthConcerns), MuseConcerns: len(responseConcerns)}
	}

	var result alignResult
	if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err != nil {
		return genScore{TruthConcerns: len(truthConcerns), MuseConcerns: len(responseConcerns)}
	}

	// Compute weighted recall: sum(match_score * severity_weight) / sum(severity_weight)
	totalWeight := 0.0
	for _, c := range truthConcerns {
		totalWeight += severityWeight(c.Severity)
	}

	matchedWeight := 0.0
	for _, m := range result.Matches {
		w := severityWeight(severityMap[m.Truth])
		matchedWeight += m.Score * w
	}
	recall := matchedWeight / totalWeight

	// Precision: unweighted (muse concerns don't have ground-truth severity)
	totalMatch := 0.0
	for _, m := range result.Matches {
		totalMatch += m.Score
	}
	precision := totalMatch / float64(len(responseConcerns))

	// Priority alignment
	priorityMatch := result.TopTruthConcern != "" &&
		result.TopPredictedConcern != "" &&
		result.TopTruthConcern == result.TopPredictedConcern

	return genScore{
		Recall:        math.Min(recall, 1.0),
		Precision:     math.Min(precision, 1.0),
		PriorityMatch: priorityMatch,
		TruthConcerns: len(truthConcerns),
		MuseConcerns:  len(responseConcerns),
		MatchedWeight: matchedWeight,
		TotalWeight:   totalWeight,
	}
}

func printGenEvalSummary(w io.Writer, results []genEvalResult) {
	var museMatchedWeight, baseMatchedWeight, totalWeight float64
	var musePrecision, basePrecision float64
	var museWins, baseWins, ties int
	var validCount int

	for _, r := range results {
		if r.Error != nil {
			continue
		}
		validCount++

		// Pool globally: accumulate matched weight and total weight across all cases
		museMatchedWeight += r.MuseScore.MatchedWeight
		baseMatchedWeight += r.BaseScore.MatchedWeight
		totalWeight += r.MuseScore.TotalWeight // same ground truth for both

		musePrecision += r.MuseScore.Precision
		basePrecision += r.BaseScore.Precision

		// Win/loss by weighted recall per case
		if r.MuseScore.Recall > r.BaseScore.Recall {
			museWins++
		} else if r.BaseScore.Recall > r.MuseScore.Recall {
			baseWins++
		} else {
			ties++
		}
	}

	if validCount == 0 {
		fmt.Fprintln(w, "No valid results.")
		return
	}

	n := float64(validCount)
	museRecall := museMatchedWeight / totalWeight
	baseRecall := baseMatchedWeight / totalWeight

	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "                    Muse    Base    Delta\n")
	fmt.Fprintf(w, "  ────────────────────────────────────────\n")
	fmt.Fprintf(w, "  Recall (wt)       %.2f    %.2f    %+.2f\n", museRecall, baseRecall, museRecall-baseRecall)
	fmt.Fprintf(w, "  Precision         %.2f    %.2f    %+.2f\n", musePrecision/n, basePrecision/n, (musePrecision-basePrecision)/n)
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "  Muse better: %d/%d  Base better: %d/%d  Tied: %d/%d\n",
		museWins, validCount, baseWins, validCount, ties, validCount)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// parsePeerFlag validates a "source/username" peer flag and returns the
// source and username. Currently only "github" is supported as a source.
func parsePeerFlag(peer string) (source, username string, err error) {
	parts := strings.SplitN(peer, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", "", fmt.Errorf("invalid peer format %q (use source/username, e.g. github/ellistarn)", peer)
	}
	if parts[0] != "github" {
		return "", "", fmt.Errorf("unsupported peer source %q (only github is supported)", parts[0])
	}
	return parts[0], parts[1], nil
}

func peerDir(peer, project string) string {
	parts := strings.SplitN(peer, "/", 2)
	dir := fmt.Sprintf("github-%s", parts[1])
	if project != "" {
		dir = fmt.Sprintf("github-%s/%s", parts[1], project)
	}
	return dir
}
