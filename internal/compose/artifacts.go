package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ellistarn/muse/internal/storage"
)

// Artifact path conventions. Each strategy builds paths under "compose/" using
// the Store's generic PutData/GetData/ListData/DeleteData methods.
//
// Clustering artifacts:
//   compose/observations/{source}/{conversationID}.json
//   compose/labels/{source}/{conversationID}.json
//   compose/normalization.json

// SourceConversation identifies a conversation by its source and conversation ID.
type SourceConversation struct {
	Source         string
	ConversationID string
}

// composePath returns the key for a compose artifact.
func composePath(kind, source, conversationID string) string {
	return fmt.Sprintf("compose/%s/%s/%s.json", kind, source, conversationID)
}

// PutObservations writes observations for a conversation.
func PutObservations(ctx context.Context, store storage.Store, source, conversationID string, obs *Observations) error {
	return putJSON(ctx, store, composePath("observations", source, conversationID), obs)
}

// GetObservations reads observations for a conversation.
// Returns storage.NotFoundError when no artifact exists.
func GetObservations(ctx context.Context, store storage.Store, source, conversationID string) (*Observations, error) {
	var obs Observations
	if err := getJSON(ctx, store, composePath("observations", source, conversationID), &obs); err != nil {
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
	return listArtifacts(ctx, store, "compose/observations/")
}

// ListLabels returns all (source, conversationID) pairs that have labels.
func ListLabels(ctx context.Context, store storage.Store) ([]SourceConversation, error) {
	return listArtifacts(ctx, store, "compose/labels/")
}

// DeleteObservations removes all observation artifacts.
func DeleteObservations(ctx context.Context, store storage.Store) error {
	return store.DeletePrefix(ctx, "compose/observations/")
}

// DeleteObservationsForSource removes observation artifacts for a specific source.
func DeleteObservationsForSource(ctx context.Context, store storage.Store, source string) error {
	return store.DeletePrefix(ctx, fmt.Sprintf("compose/observations/%s/", source))
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
