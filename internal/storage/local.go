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

// ListConversations returns all conversation entries under conversations/.
func (l *LocalStore) ListConversations(_ context.Context) ([]ConversationEntry, error) {
	conversationsDir := filepath.Join(l.root, "conversations")
	var entries []ConversationEntry
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
		conversationID := strings.TrimSuffix(parts[1], ".json")
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, ConversationEntry{
			Source:         src,
			ConversationID: conversationID,
			Key:            "conversations/" + filepath.ToSlash(rel),
			LastModified:   info.ModTime(),
			SizeBytes:      info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}
	return entries, nil
}

// PutConversation writes a conversation as JSON and returns the number of bytes written.
func (l *LocalStore) PutConversation(_ context.Context, conv *conversation.Conversation) (int, error) {
	data, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("failed to marshal conversation: %w", err)
	}
	path := filepath.Join(l.root, "conversations", conv.Source, conv.ConversationID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, fmt.Errorf("failed to write conversation: %w", err)
	}
	return len(data), nil
}

// GetConversation reads and deserializes a conversation from the filesystem.
func (l *LocalStore) GetConversation(_ context.Context, src, conversationID string) (*conversation.Conversation, error) {
	path := filepath.Join(l.root, "conversations", src, conversationID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NotFoundError{Key: conversationKey(src, conversationID)}
		}
		return nil, fmt.Errorf("failed to read conversation %s: %w", conversationID, err)
	}
	var conv conversation.Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("failed to unmarshal conversation %s: %w", conversationID, err)
	}
	if err := conv.Validate(); err != nil {
		return nil, fmt.Errorf("invalid conversation %s: %w", conversationID, err)
	}
	return &conv, nil
}

// GetMuse returns the latest muse version by finding the most recent timestamp
// that contains a muse.md file. Directories with only a diff.md are skipped.
func (l *LocalStore) GetMuse(ctx context.Context) (string, error) {
	timestamps, err := l.ListMuses(ctx)
	if err != nil {
		return "", err
	}
	// Walk backwards to find the latest timestamp that has a muse.md
	for i := len(timestamps) - 1; i >= 0; i-- {
		content, err := l.GetMuseVersion(ctx, timestamps[i])
		if err == nil {
			return content, nil
		}
	}
	return "", &NotFoundError{Key: "versions/"}
}

// PutMuse writes a muse version at the given timestamp and updates the stable
// latest file at {root}/muse.md for external consumers (e.g. agent instructions).
func (l *LocalStore) PutMuse(_ context.Context, timestamp, content string) error {
	path := filepath.Join(l.root, "versions", timestamp, "muse.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(l.root, "muse.md"), []byte(content), 0o644)
}

// PutMuseDiff writes a diff summary at the given timestamp.
func (l *LocalStore) PutMuseDiff(_ context.Context, timestamp, content string) error {
	path := filepath.Join(l.root, "versions", timestamp, "diff.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// GetMuseDiff reads the diff summary for the given timestamp.
func (l *LocalStore) GetMuseDiff(_ context.Context, timestamp string) (string, error) {
	path := filepath.Join(l.root, "versions", timestamp, "diff.md")
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
	musesDir := filepath.Join(l.root, "versions")
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
	path := filepath.Join(l.root, "versions", timestamp, "muse.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{Key: museVersionKey(timestamp)}
		}
		return "", fmt.Errorf("failed to read muse version %s: %w", timestamp, err)
	}
	return string(data), nil
}

// PutObservation writes observations under observations/.
func (l *LocalStore) PutObservation(_ context.Context, key, content string) error {
	relPath := ObservationKey(key)
	path := filepath.Join(l.root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// ListObservations returns all persisted observations with their modification times.
func (l *LocalStore) ListObservations(_ context.Context) (map[string]time.Time, error) {
	obsDir := filepath.Join(l.root, "observations")
	observations := map[string]time.Time{}
	err := filepath.WalkDir(obsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, err := filepath.Rel(obsDir, path)
		if err != nil {
			return nil
		}
		conversationKey := "conversations/" + strings.TrimSuffix(filepath.ToSlash(rel), ".md") + ".json"
		info, err := d.Info()
		if err != nil {
			return nil
		}
		observations[conversationKey] = info.ModTime()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list observations: %w", err)
	}
	return observations, nil
}

// GetObservation reads an observation's content.
func (l *LocalStore) GetObservation(_ context.Context, conversationKey string) (string, error) {
	relPath := ObservationKey(conversationKey)
	path := filepath.Join(l.root, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{Key: conversationKey}
		}
		return "", fmt.Errorf("failed to read observation: %w", err)
	}
	return string(data), nil
}

// PutData writes raw bytes at the given key path using atomic write-to-temp + rename.
func (l *LocalStore) PutData(_ context.Context, key string, data []byte) error {
	path := filepath.Join(l.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", key, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", key, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("failed to rename %s: %w", key, err)
	}
	return nil
}

// GetData reads raw bytes at the given key path.
func (l *LocalStore) GetData(_ context.Context, key string) ([]byte, error) {
	path := filepath.Join(l.root, filepath.FromSlash(key))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NotFoundError{Key: key}
		}
		return nil, fmt.Errorf("failed to read %s: %w", key, err)
	}
	return data, nil
}

// ListData returns all keys under the given prefix.
func (l *LocalStore) ListData(_ context.Context, prefix string) ([]string, error) {
	dir := filepath.Join(l.root, filepath.FromSlash(prefix))
	var keys []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(l.root, path)
		if err != nil {
			return nil
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", prefix, err)
	}
	return keys, nil
}

// DeletePrefix removes all files under the given prefix.
func (l *LocalStore) DeletePrefix(_ context.Context, prefix string) error {
	path := filepath.Join(l.root, filepath.FromSlash(prefix))
	if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete %s: %w", prefix, err)
	}
	return nil
}
