package storage

import (
	"context"
	"errors"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
)

// Store is the interface for all storage operations. Implementations include
// S3 (for hosted/remote mode) and local filesystem (for zero-config local use).
//
// Storage layout:
//
//	conversations/{source}/{session_id}.json   — raw conversation sessions
//	observations/{source}/{session_id}.md      — per-session observations
//	muse/versions/{timestamp}/muse.md          — timestamped muse versions (latest = current)
//	muse/versions/{timestamp}/diff.md          — what changed from the previous version
type Store interface {
	// Conversations
	ListSessions(ctx context.Context) ([]SessionEntry, error)
	GetSession(ctx context.Context, src, sessionID string) (*conversation.Session, error)
	PutSession(ctx context.Context, session *conversation.Session) (int, error)

	// Muses
	GetMuse(ctx context.Context) (string, error)                          // latest version
	PutMuse(ctx context.Context, timestamp, content string) error         // write muse.md at timestamp
	GetMuseDiff(ctx context.Context, timestamp string) (string, error)    // read diff.md at timestamp
	PutMuseDiff(ctx context.Context, timestamp, content string) error     // write diff.md at timestamp
	ListMuses(ctx context.Context) ([]string, error)                      // all timestamps, sorted asc
	GetMuseVersion(ctx context.Context, timestamp string) (string, error) // specific version

	// Observations
	ListObservations(ctx context.Context) (map[string]time.Time, error)
	GetObservation(ctx context.Context, conversationKey string) (string, error)
	PutObservation(ctx context.Context, key, content string) error

	// Maintenance
	DeletePrefix(ctx context.Context, prefix string) error
}

// SessionEntry is the metadata returned by ListSessions without downloading full content.
type SessionEntry struct {
	Source       string
	SessionID    string
	Key          string
	LastModified time.Time
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
