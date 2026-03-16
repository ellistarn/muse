package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
)

// LocalStore implements Store backed by the local filesystem, rooted at ~/.muse/.
type LocalStore struct {
	root string
}

// Verify LocalStore implements Store at compile time.
var _ Store = (*LocalStore)(nil)

// NewLocalStore creates a new LocalStore rooted at ~/.muse/.
// The directory is created on first write, not here.
func NewLocalStore() (*LocalStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine home directory: %w", err)
	}
	return NewLocalStoreWithRoot(filepath.Join(home, ".muse")), nil
}

// NewLocalStoreWithRoot creates a LocalStore rooted at the given directory.
func NewLocalStoreWithRoot(root string) *LocalStore {
	return &LocalStore{root: root}
}

// Root returns the filesystem root directory for this store.
func (l *LocalStore) Root() string { return l.root }

// ListSessions returns all session entries under conversations/.
func (l *LocalStore) ListSessions(_ context.Context) ([]SessionEntry, error) {
	conversationsDir := filepath.Join(l.root, "conversations")
	var entries []SessionEntry
	err := filepath.WalkDir(conversationsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, err := filepath.Rel(conversationsDir, path)
		if err != nil {
			return nil
		}
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		if len(parts) != 2 {
			return nil
		}
		src := parts[0]
		sessionID := strings.TrimSuffix(parts[1], ".json")
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, SessionEntry{
			Source:       src,
			SessionID:    sessionID,
			Key:          "conversations/" + filepath.ToSlash(rel),
			LastModified: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	return entries, nil
}

// PutSession writes a session as JSON and returns the number of bytes written.
func (l *LocalStore) PutSession(_ context.Context, session *conversation.Session) (int, error) {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("failed to marshal session: %w", err)
	}
	path := filepath.Join(l.root, "conversations", session.Source, session.SessionID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, fmt.Errorf("failed to write session: %w", err)
	}
	return len(data), nil
}

// GetSession reads and deserializes a session from the filesystem.
func (l *LocalStore) GetSession(_ context.Context, src, sessionID string) (*conversation.Session, error) {
	path := filepath.Join(l.root, "conversations", src, sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NotFoundError{Key: sessionKey(src, sessionID)}
		}
		return nil, fmt.Errorf("failed to read session %s: %w", sessionID, err)
	}
	var session conversation.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session %s: %w", sessionID, err)
	}
	return &session, nil
}

// GetMuse returns the latest muse version by finding the most recent timestamp
// that contains a muse.md file. Directories with only a diff.md are skipped.
func (l *LocalStore) GetMuse(_ context.Context) (string, error) {
	timestamps, err := l.ListMuses(context.Background())
	if err != nil {
		return "", err
	}
	// Walk backwards to find the latest timestamp that has a muse.md
	for i := len(timestamps) - 1; i >= 0; i-- {
		content, err := l.GetMuseVersion(context.Background(), timestamps[i])
		if err == nil {
			return content, nil
		}
	}
	return "", &NotFoundError{Key: "muse/versions/"}
}

// PutMuse writes a muse version at the given timestamp.
func (l *LocalStore) PutMuse(_ context.Context, timestamp, content string) error {
	path := filepath.Join(l.root, "muse", "versions", timestamp, "muse.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// PutMuseDiff writes a diff summary at the given timestamp.
func (l *LocalStore) PutMuseDiff(_ context.Context, timestamp, content string) error {
	path := filepath.Join(l.root, "muse", "versions", timestamp, "diff.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// GetMuseDiff reads the diff summary for the given timestamp.
func (l *LocalStore) GetMuseDiff(_ context.Context, timestamp string) (string, error) {
	path := filepath.Join(l.root, "muse", "versions", timestamp, "diff.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{Key: museDiffKey(timestamp)}
		}
		return "", fmt.Errorf("failed to read muse diff %s: %w", timestamp, err)
	}
	return string(data), nil
}

// ListMuses returns timestamps of all muse versions, sorted ascending.
func (l *LocalStore) ListMuses(_ context.Context) ([]string, error) {
	musesDir := filepath.Join(l.root, "muse", "versions")
	entries, err := os.ReadDir(musesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list muse versions: %w", err)
	}
	var timestamps []string
	for _, e := range entries {
		if e.IsDir() {
			timestamps = append(timestamps, e.Name())
		}
	}
	sort.Strings(timestamps)
	return timestamps, nil
}

// GetMuseVersion reads a specific muse version.
func (l *LocalStore) GetMuseVersion(_ context.Context, timestamp string) (string, error) {
	path := filepath.Join(l.root, "muse", "versions", timestamp, "muse.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{Key: museVersionKey(timestamp)}
		}
		return "", fmt.Errorf("failed to read muse version %s: %w", timestamp, err)
	}
	return string(data), nil
}

// PutReflection writes a reflection under reflections/.
func (l *LocalStore) PutReflection(_ context.Context, key, content string) error {
	relPath := reflectionKey(key)
	path := filepath.Join(l.root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// ListReflections returns all persisted reflections with their modification times.
func (l *LocalStore) ListReflections(_ context.Context) (map[string]time.Time, error) {
	reflDir := filepath.Join(l.root, "reflections")
	reflections := map[string]time.Time{}
	err := filepath.WalkDir(reflDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, err := filepath.Rel(reflDir, path)
		if err != nil {
			return nil
		}
		conversationKey := "conversations/" + strings.TrimSuffix(filepath.ToSlash(rel), ".md") + ".json"
		info, err := d.Info()
		if err != nil {
			return nil
		}
		reflections[conversationKey] = info.ModTime()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list reflections: %w", err)
	}
	return reflections, nil
}

// GetReflection reads a reflection's content.
func (l *LocalStore) GetReflection(_ context.Context, conversationKey string) (string, error) {
	relPath := reflectionKey(conversationKey)
	path := filepath.Join(l.root, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{Key: conversationKey}
		}
		return "", fmt.Errorf("failed to read reflection: %w", err)
	}
	return string(data), nil
}

// DeletePrefix removes all files under the given prefix.
func (l *LocalStore) DeletePrefix(_ context.Context, prefix string) error {
	path := filepath.Join(l.root, filepath.FromSlash(prefix))
	if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete %s: %w", prefix, err)
	}
	return nil
}
