package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ellistarn/muse/internal/storage"
)

// Artifact path conventions. Observations are shared across all strategies and
// stored at the top level. Strategy-specific derived artifacts live under
// "compose/". All paths use the Store's generic PutData/GetData/ListData methods.
//
// Shared:
//   observations/{source}/{conversationID}.json
//
// Clustering-specific:
//   compose/labels/{source}/{conversationID}.json
//   compose/themes.json

// SourceConversation identifies a conversation by its source and conversation ID.
type SourceConversation struct {
	Source         string
	ConversationID string
}

// composePath returns the key for a compose-specific (strategy-derived) artifact.
func composePath(kind, source, conversationID string) string {
	return fmt.Sprintf("compose/%s/%s/%s.json", kind, source, conversationID)
}

// observationPath returns the key for a shared observation artifact.
func observationPath(source, conversationID string) string {
	return fmt.Sprintf("observations/%s/%s.json", source, conversationID)
}

// PutObservations writes observations for a conversation.
func PutObservations(ctx context.Context, store storage.Store, source, conversationID string, obs *Observations) error {
	return putJSON(ctx, store, observationPath(source, conversationID), obs)
}

// GetObservations reads observations for a conversation.
// Returns storage.NotFoundError when no artifact exists.
func GetObservations(ctx context.Context, store storage.Store, source, conversationID string) (*Observations, error) {
	var obs Observations
	if err := getJSON(ctx, store, observationPath(source, conversationID), &obs); err != nil {
		return nil, err
	}
	return &obs, nil
}

// PutLabels writes labels for a conversation.
func PutLabels(ctx context.Context, store storage.Store, source, conversationID string, lbl *Labels) error {
	return putJSON(ctx, store, composePath("labels", source, conversationID), lbl)
}

// GetLabels reads labels for a conversation.
func GetLabels(ctx context.Context, store storage.Store, source, conversationID string) (*Labels, error) {
	var lbl Labels
	if err := getJSON(ctx, store, composePath("labels", source, conversationID), &lbl); err != nil {
		return nil, err
	}
	return &lbl, nil
}

// PutThemes writes the theme mapping.
func PutThemes(ctx context.Context, store storage.Store, themes *LabelMapping) error {
	return putJSON(ctx, store, "compose/themes.json", themes)
}

// GetThemes reads the theme mapping.
func GetThemes(ctx context.Context, store storage.Store) (*LabelMapping, error) {
	var themes LabelMapping
	if err := getJSON(ctx, store, "compose/themes.json", &themes); err != nil {
		return nil, err
	}
	return &themes, nil
}

// DeleteThemes removes the theme mapping artifact.
func DeleteThemes(ctx context.Context, store storage.Store) error {
	return store.DeletePrefix(ctx, "compose/themes.json")
}

// ListObservations returns all (source, conversationID) pairs that have observations.
func ListObservations(ctx context.Context, store storage.Store) ([]SourceConversation, error) {
	return listArtifacts(ctx, store, "observations/")
}

// CountObservationItems returns the total number of discrete observation items
// per source by reading each observation file and summing its Items.
func CountObservationItems(ctx context.Context, store storage.Store) (map[string]int, error) {
	entries, err := ListObservations(ctx, store)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		obs, err := GetObservations(ctx, store, e.Source, e.ConversationID)
		if err != nil {
			return nil, err
		}
		counts[e.Source] += len(obs.Items)
	}
	return counts, nil
}

// ListLabels returns all (source, conversationID) pairs that have labels.
func ListLabels(ctx context.Context, store storage.Store) ([]SourceConversation, error) {
	return listArtifacts(ctx, store, "compose/labels/")
}

// DeleteObservations removes all observation artifacts.
func DeleteObservations(ctx context.Context, store storage.Store) error {
	return store.DeletePrefix(ctx, "observations/")
}

// DeleteObservationsForSource removes observation artifacts for a specific source.
func DeleteObservationsForSource(ctx context.Context, store storage.Store, source string) error {
	return store.DeletePrefix(ctx, fmt.Sprintf("observations/%s/", source))
}

// DeleteLabels removes all label artifacts.
func DeleteLabels(ctx context.Context, store storage.Store) error {
	return store.DeletePrefix(ctx, "compose/labels/")
}

// ListObservations returns all (source, conversationID) pairs that have observations.
// Keys are expected to follow the pattern: {prefix}{source}/{conversationID}.json
func listArtifacts(ctx context.Context, store storage.Store, prefix string) ([]SourceConversation, error) {
	keys, err := store.ListData(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list artifacts under %s: %w", prefix, err)
	}
	var results []SourceConversation
	for _, key := range keys {
		rel := strings.TrimPrefix(key, prefix)
		if !strings.HasSuffix(rel, ".json") {
			continue
		}
		parts := strings.SplitN(rel, "/", 2)
		if len(parts) != 2 {
			continue
		}
		results = append(results, SourceConversation{
			Source:         parts[0],
			ConversationID: strings.TrimSuffix(parts[1], ".json"),
		})
	}
	return results, nil
}

func putJSON(ctx context.Context, store storage.Store, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal artifact: %w", err)
	}
	return store.PutData(ctx, key, data)
}

func getJSON(ctx context.Context, store storage.Store, key string, v any) error {
	data, err := store.GetData(ctx, key)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("failed to parse artifact %s: %w", key, err)
	}
	return nil
}
