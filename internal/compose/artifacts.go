package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ellistarn/muse/internal/storage"
)

// Artifact path conventions. Observations are stored per observe-mode so
// different strategies can coexist without clobbering each other. The default
// mode ("") stores at the top level for backward compatibility. Named modes
// store under observations/{mode}/. Strategy-specific derived artifacts live
// under "compose/".
//
// Observations:
//   observations/{source}/{conversationID}.json              (default mode)
//   observations/{mode}/{source}/{conversationID}.json       (named modes)
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

// observationPath returns the storage key for an observation artifact.
// Named modes are namespaced under observations/{mode}/.
func observationPath(source, conversationID string, mode ...ObserveMode) string {
	m := ObserveDefault
	if len(mode) > 0 {
		m = mode[0]
	}
	if m == "" || m == ObserveDefault {
		return fmt.Sprintf("observations/%s/%s.json", source, conversationID)
	}
	return fmt.Sprintf("observations/%s/%s/%s.json", string(m), source, conversationID)
}

// observationDirPrefix returns the storage prefix for listing/deleting observations.
func observationDirPrefix(mode ObserveMode) string {
	if mode == "" || mode == ObserveDefault {
		return "observations/"
	}
	return fmt.Sprintf("observations/%s/", string(mode))
}

// PutObservations writes observations for a conversation.
func PutObservations(ctx context.Context, store storage.Store, source, conversationID string, obs *Observations, mode ...ObserveMode) error {
	return putJSON(ctx, store, observationPath(source, conversationID, mode...), obs)
}

// GetObservations reads observations for a conversation.
// Returns storage.NotFoundError when no artifact exists.
func GetObservations(ctx context.Context, store storage.Store, source, conversationID string, mode ...ObserveMode) (*Observations, error) {
	var obs Observations
	if err := getJSON(ctx, store, observationPath(source, conversationID, mode...), &obs); err != nil {
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
func ListObservations(ctx context.Context, store storage.Store, mode ...ObserveMode) ([]SourceConversation, error) {
	m := ObserveDefault
	if len(mode) > 0 {
		m = mode[0]
	}
	return listArtifacts(ctx, store, observationDirPrefix(m))
}

// CountObservationItems returns the total number of discrete observation items
// per source by reading each observation file and summing its Items.
func CountObservationItems(ctx context.Context, store storage.Store, mode ...ObserveMode) (map[string]int, error) {
	entries, err := ListObservations(ctx, store, mode...)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		obs, err := GetObservations(ctx, store, e.Source, e.ConversationID, mode...)
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

// DeleteObservations removes observation artifacts for the given mode only.
// Default mode uses depth filtering to avoid deleting named-mode observations
// that share the "observations/" prefix.
func DeleteObservations(ctx context.Context, store storage.Store, mode ...ObserveMode) error {
	m := ObserveDefault
	if len(mode) > 0 {
		m = mode[0]
	}
	if m != "" && m != ObserveDefault {
		// Named modes have a unique prefix; safe to delete by prefix.
		return store.DeletePrefix(ctx, observationDirPrefix(m))
	}
	// Default mode: observations/{source}/{id}.json lives at depth 2 under
	// "observations/". Named modes add a third level. List and delete only
	// depth-2 keys to avoid clobbering named-mode data.
	keys, err := store.ListData(ctx, "observations/")
	if err != nil {
		return err
	}
	for _, key := range keys {
		rel := strings.TrimPrefix(key, "observations/")
		// Default-mode keys have exactly one slash: {source}/{id}.json
		if strings.Count(rel, "/") == 1 {
			if err := store.DeletePrefix(ctx, key); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteObservationsForSource removes default-mode observation artifacts for a
// specific source. Only deletes keys at the expected depth to avoid colliding
// with named-mode prefixes (e.g., a source named "woo" vs the woo mode).
func DeleteObservationsForSource(ctx context.Context, store storage.Store, source string) error {
	prefix := fmt.Sprintf("observations/%s/", source)
	keys, err := store.ListData(ctx, prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		rel := strings.TrimPrefix(key, prefix)
		// Default-mode source keys have no further slashes: just {id}.json
		if !strings.Contains(rel, "/") {
			if err := store.DeletePrefix(ctx, key); err != nil {
				return err
			}
		}
	}
	return nil
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
