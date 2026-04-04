package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/storage"
)

// weightedObservation is an observation with its validated weight.
type weightedObservation struct {
	Reviewer string
	Text     string
	Quote    string
	Weight   float64
}

const reinforcePrompt = `You are reviewing an observation about how you think and work.
Be ruthlessly selective. Most observations are generic things any good reviewer does.
Only reinforce observations that capture something DISTINCTIVE about your specific
review style — patterns that distinguish you from other competent reviewers.

Observation: "%s"

Respond with ONLY one of:
+1  if this captures a distinctive pattern specific to how YOU review (not generic good practice)
 0  if this is accurate but generic — any experienced reviewer would do this
-1  if this is wrong, misleading, or describes something you actively avoid`

// validateObservations loads a peer's observations and asks their muse to
// reinforce, ignore, or reject each one. Returns weighted observations.
func validateObservations(ctx context.Context, peer, project string) ([]weightedObservation, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	pd := peerDir(fmt.Sprintf("github/%s", peer), project)
	peerRoot := filepath.Join(home, ".muse", "peers", pd)
	store := storage.NewLocalStoreWithRoot(peerRoot)

	// Load observations
	observations, err := loadAllObservations(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("load observations: %w", err)
	}
	if len(observations) == 0 {
		return nil, fmt.Errorf("no observations found for %s", peer)
	}

	// Load muse
	document, err := store.GetMuse(ctx)
	if err != nil {
		return nil, fmt.Errorf("no muse found for %s", peer)
	}

	llm, err := newLLMClient(ctx, TierStrong)
	if err != nil {
		return nil, err
	}

	museHash := compose.Fingerprint(document)[:12]
	fmt.Fprintf(os.Stderr, "validate  %d observations  peer=github/%s\n", len(observations), peer)

	// Ask the muse to reinforce each observation, with caching
	weights := make([]float64, len(observations))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)

	for i, obs := range observations {
		g.Go(func() error {
			// Cache key: observation text + muse hash
			fp := compose.Fingerprint(obs.Text, museHash)
			cacheKey := fmt.Sprintf("eval/validate/%s.json", fp[:16])

			// Check cache
			if cached, err := loadCachedResponse(gctx, store, cacheKey, fp); err == nil {
				w := parseWeight(cached)
				mu.Lock()
				weights[i] = w
				mu.Unlock()
				return nil
			}

			// Ask the muse
			prompt := fmt.Sprintf(reinforcePrompt, obs.Text)
			resp, _, err := inference.Converse(gctx, llm, document, prompt)
			if err != nil {
				return nil
			}

			w := parseWeight(resp)
			mu.Lock()
			weights[i] = w
			mu.Unlock()

			// Cache the raw response
			saveCachedResponse(gctx, store, cacheKey, fp, resp)
			return nil
		})
	}
	g.Wait()

	// Build weighted observations
	var result []weightedObservation
	var pos, neg, zero int
	for i, obs := range observations {
		if weights[i] > 0 {
			pos++
		} else if weights[i] < 0 {
			neg++
		} else {
			zero++
		}
		result = append(result, weightedObservation{
			Reviewer: peer,
			Text:     obs.Text,
			Quote:    obs.Quote,
			Weight:   weights[i],
		})
	}
	fmt.Fprintf(os.Stderr, "  +1: %d   0: %d  -1: %d\n", pos, zero, neg)

	return result, nil
}

// parseWeight extracts +1, 0, or -1 from a response string.
func parseWeight(s string) float64 {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "+1") {
		return 1
	}
	if strings.HasPrefix(s, "-1") {
		return -1
	}
	// Check for just "1" (without +)
	if strings.HasPrefix(s, "1") {
		return 1
	}
	return 0
}

// loadAllObservations loads all observations for a peer from storage.
func loadAllObservations(ctx context.Context, store storage.Store) ([]compose.Observation, error) {
	keys, err := store.ListData(ctx, "observations/")
	if err != nil {
		return nil, err
	}

	var all []compose.Observation
	for _, key := range keys {
		data, err := store.GetData(ctx, key)
		if err != nil {
			continue
		}
		var obs compose.Observations
		if err := json.Unmarshal(data, &obs); err != nil {
			continue
		}
		all = append(all, obs.Items...)
	}
	return all, nil
}
