package compose

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/prompts"
)

// WindowFeatures records observation results and input features for a single
// window of a single conversation. Used to build a dataset for predicting
// which observation strategy works best.
type WindowFeatures struct {
	Source         string `json:"source"`
	ConversationID string `json:"conversation_id"`
	Window         int    `json:"window"`
	TurnCount      int    `json:"turns"`
	AvgOwnerChars  int    `json:"avg_owner_chars"`
	MaxOwnerChars  int    `json:"max_owner_chars"`
	TotalOwnerChars int   `json:"total_owner_chars"`
	AvgAssistChars int    `json:"avg_assist_chars"`
	TotalChars     int    `json:"total_chars"`
	DefaultObs     int    `json:"default_obs"`
	WooObs         int    `json:"woo_obs"`
	DefaultCost    float64 `json:"default_cost"`
	WooCost        float64 `json:"woo_cost"`
}

// FeaturizeConversation runs both default (windowed-with-assistant) and woo
// (windowed-owner-only) on each window of a conversation, returning per-window
// features and observation counts.
func FeaturizeConversation(ctx context.Context, client inference.Client, conv *conversation.Conversation, verbose bool) ([]WindowFeatures, inference.Usage, error) {
	turns := extractTurns(conv, nil)
	if len(turns) == 0 {
		return nil, inference.Usage{}, nil
	}

	windows := buildWindows(turns, windowSize, windowStride)

	observePrompt := prompts.Observe
	if isHumanSource(conv.Source, nil) {
		observePrompt = prompts.ObserveHuman
	}

	var totalUsage inference.Usage
	var features []WindowFeatures

	for i, w := range windows {
		f := WindowFeatures{
			Source:         conv.Source,
			ConversationID: conv.ConversationID,
			Window:         i,
			TurnCount:      len(w),
		}

		// Compute input features
		var maxOwner, totalOwner, totalAssist int
		for _, t := range w {
			n := len(t.humanContent)
			totalOwner += n
			if n > maxOwner {
				maxOwner = n
			}
			totalAssist += len(t.assistantContent)
		}
		f.AvgOwnerChars = totalOwner / len(w)
		f.MaxOwnerChars = maxOwner
		f.TotalOwnerChars = totalOwner
		f.AvgAssistChars = totalAssist / len(w)
		f.TotalChars = totalOwner + totalAssist

		// Default: compressed with assistant context
		defaultInput := compressWindow(w, conv.Source)
		start := time.Now()
		defaultRaw, defaultUsage, err := inference.Converse(ctx, client, observePrompt, defaultInput, inference.WithMaxTokens(4096))
		totalUsage = totalUsage.Add(defaultUsage)
		f.DefaultCost = defaultUsage.Cost()
		if err == nil && defaultRaw != "" && !isEmpty(defaultRaw) {
			items := parseObservationItems(defaultRaw)
			for _, item := range items {
				if isRelevant(item.Text) {
					f.DefaultObs++
				}
			}
		}

		// Woo: owner-only
		var b strings.Builder
		for _, t := range w {
			fmt.Fprintf(&b, "[owner]: %s\n\n", t.humanContent)
		}
		wooInput := b.String()
		wooRaw, wooUsage, err := inference.Converse(ctx, client, observePrompt, wooInput, inference.WithMaxTokens(4096))
		totalUsage = totalUsage.Add(wooUsage)
		f.WooCost = wooUsage.Cost()
		if err == nil && wooRaw != "" && !isEmpty(wooRaw) {
			items := parseObservationItems(wooRaw)
			for _, item := range items {
				if isRelevant(item.Text) {
					f.WooObs++
				}
			}
		}

		if verbose {
			fmt.Fprintf(os.Stderr, "      window[%d/%d] %d turns, avg %d chars → default:%d woo:%d (%s)\n",
				i+1, len(windows), len(w), f.AvgOwnerChars, f.DefaultObs, f.WooObs,
				time.Since(start).Round(time.Millisecond))
		}

		features = append(features, f)
	}

	return features, totalUsage, nil
}

// compressWindow compresses a single window of turns into text for the
// default observe prompt. Uses the same compression as compressConversation
// but returns a single string.
func compressWindow(turns []turn, source string) string {
	chunks := compressConversation(turns, source, nil)
	return strings.Join(chunks, "\n")
}
