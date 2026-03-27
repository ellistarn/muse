package storage

import (
	"context"
	"errors"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
)

// Store is the composite interface for all storage operations.
// Implementations include S3 (for hosted/remote mode) and local filesystem
// (for zero-config local use).
//
// Storage layout:
//
//	conversations/{source}/{conversation_id}.json   — raw conversations
//	observations/{source}/{conversation_id}.md      — per-conversation observations
//	versions/{timestamp}/muse.md                — timestamped muse versions (latest = current)
//	versions/{timestamp}/diff.md               — what changed from the previous version
type Store interface {
	ConversationStore
	MuseStore
	ObservationStore
	DataStore
}

// ConversationStore manages raw conversation data.
type ConversationStore interface {
	ListConversations(ctx context.Context) ([]ConversationEntry, error)
	GetConversation(ctx context.Context, src, conversationID string) (*conversation.Conversation, error)
	PutConversation(ctx context.Context, conv *conversation.Conversation) (int, error)
}

// MuseStore manages muse versions and diffs.
type MuseStore interface {
	GetMuse(ctx context.Context) (string, error)                          // latest version
	PutMuse(ctx context.Context, timestamp, content string) error         // write muse.md at timestamp
	GetMuseDiff(ctx context.Context, timestamp string) (string, error)    // read diff.md at timestamp
	PutMuseDiff(ctx context.Context, timestamp, content string) error     // write diff.md at timestamp
	ListMuses(ctx context.Context) ([]string, error)                      // all timestamps, sorted asc
	GetMuseVersion(ctx context.Context, timestamp string) (string, error) // specific version
}

// ObservationStore manages per-conversation observations.
type ObservationStore interface {
	ListObservations(ctx context.Context) (map[string]time.Time, error)
	GetObservation(ctx context.Context, conversationKey string) (string, error)
	PutObservation(ctx context.Context, key, content string) error
}

// DataStore provides generic key/value operations for pipeline artifacts and
// strategy-specific state. Keys are slash-delimited paths (e.g.
// "compose/observations/opencode/ses_001.json").
type DataStore interface {
	PutData(ctx context.Context, key string, data []byte) error
	GetData(ctx context.Context, key string) ([]byte, error)
	ListData(ctx context.Context, prefix string) ([]string, error) // returns keys under prefix
	DeletePrefix(ctx context.Context, prefix string) error
}

// ConversationEntry is the metadata returned by ListConversations without downloading full content.
type ConversationEntry struct {
	Source         string
	ConversationID string
	Key            string
	LastModified   time.Time
}

// NotFoundError is returned when a requested resource does not exist.
type NotFoundError struct {
	Key string
}

func (e *NotFoundError) Error() string {
	return "not found: " + e.Key
}

// IsNotFound reports whether the error indicates a missing resource.
func IsNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

// FilterEntriesBySource returns only entries matching the allowed sources.
// If sources is empty, all entries are returned.
func FilterEntriesBySource(entries []ConversationEntry, sources []string) []ConversationEntry {
	if len(sources) == 0 {
		return entries
	}
	allowed := make(map[string]bool, len(sources))
	for _, s := range sources {
		allowed[s] = true
	}
	var filtered []ConversationEntry
	for _, e := range entries {
		if allowed[e.Source] {
			filtered = append(filtered, e)
		}
	}
	return filtered
}
