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
//   compose/normalization.json

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

// PutNormalization writes the normalization mapping.
func PutNormalization(ctx context.Context, store storage.Store, norm *Normalization) error {
	return putJSON(ctx, store, "compose/normalization.json", norm)
}

// GetNormalization reads the normalization mapping.
func GetNormalization(ctx context.Context, store storage.Store) (*Normalization, error) {
	var norm Normalization
	if err := getJSON(ctx, store, "compose/normalization.json", &norm); err != nil {
		return nil, err
	}
	return &norm, nil
}

// ListObservations returns all (source, conversationID) pairs that have observations.
func ListObservations(ctx context.Context, store storage.Store) ([]SourceConversation, error) {
	return listArtifacts(ctx, store, "observations/")
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

// DeleteNormalization removes the normalization mapping artifact.
func DeleteNormalization(ctx context.Context, store storage.Store) error {
	return store.DeletePrefix(ctx, "compose/normalization.json")
}

// listArtifacts returns (source, conversationID) pairs from keys under the given prefix.
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

// MigrateObservations moves observations from the legacy compose/observations/
// path to the canonical observations/ path. This is a one-time migration for
// users upgrading from the old storage layout. Returns the number of
// observations migrated. Already-migrated observations (present at the new path)
// are skipped.
func MigrateObservations(ctx context.Context, store storage.Store) (int, error) {
	const legacyPrefix = "compose/observations/"
	legacyKeys, err := store.ListData(ctx, legacyPrefix)
	if err != nil {
		return 0, nil // no legacy data
	}
	if len(legacyKeys) == 0 {
		return 0, nil
	}

	var migrated int
	for _, oldKey := range legacyKeys {
		// compose/observations/{source}/{id}.json → observations/{source}/{id}.json
		newKey := strings.TrimPrefix(oldKey, "compose/")

		// Skip if already exists at new path
		if _, err := store.GetData(ctx, newKey); err == nil {
			continue
		}

		data, err := store.GetData(ctx, oldKey)
		if err != nil {
			continue
		}
		if err := store.PutData(ctx, newKey, data); err != nil {
			return migrated, fmt.Errorf("migrate %s: %w", oldKey, err)
		}
		migrated++
	}

	// Clean up legacy directory after successful migration
	if migrated > 0 {
		store.DeletePrefix(ctx, legacyPrefix)
	}

	return migrated, nil
}
