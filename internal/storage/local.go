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

	"github.com/ellistarn/muse/internal/memory"
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
	return &LocalStore{root: filepath.Join(home, ".muse")}, nil
}

// Root returns the filesystem root directory for this store.
func (l *LocalStore) Root() string { return l.root }

// ListSessions returns all session entries under memories/.
func (l *LocalStore) ListSessions(_ context.Context) ([]SessionEntry, error) {
	memoriesDir := filepath.Join(l.root, "memories")
	var entries []SessionEntry
	err := filepath.WalkDir(memoriesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, err := filepath.Rel(memoriesDir, path)
		if err != nil {
			return nil
		}
		// rel = "source/session_id.json"
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
			Key:          "memories/" + filepath.ToSlash(rel),
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
func (l *LocalStore) PutSession(_ context.Context, session *memory.Session) (int, error) {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("failed to marshal session: %w", err)
	}
	path := filepath.Join(l.root, "memories", session.Source, session.SessionID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, fmt.Errorf("failed to write session: %w", err)
	}
	return len(data), nil
}

// GetSession reads and deserializes a session from the filesystem.
func (l *LocalStore) GetSession(_ context.Context, src, sessionID string) (*memory.Session, error) {
	path := filepath.Join(l.root, "memories", src, sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NotFoundError{Key: sessionKey(src, sessionID)}
		}
		return nil, fmt.Errorf("failed to read session %s: %w", sessionID, err)
	}
	var session memory.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session %s: %w", sessionID, err)
	}
	return &session, nil
}

// GetSoul reads the soul document from the filesystem.
func (l *LocalStore) GetSoul(_ context.Context) (string, error) {
	path := filepath.Join(l.root, soulKey)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{Key: soulKey}
		}
		return "", fmt.Errorf("failed to read soul: %w", err)
	}
	return string(data), nil
}

// PutSoul writes the soul document.
func (l *LocalStore) PutSoul(_ context.Context, content string) error {
	path := filepath.Join(l.root, soulKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// SnapshotSoul copies the current soul to dreams/history/{timestamp}/soul.md.
func (l *LocalStore) SnapshotSoul(_ context.Context, timestamp string) error {
	srcPath := filepath.Join(l.root, soulKey)
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("failed to read soul for snapshot: %w", err)
	}
	dstPath := filepath.Join(l.root, "dreams", "history", timestamp, "soul.md")
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(dstPath, data, 0o644)
}

// PutReflection writes a reflection under dreams/reflections/.
func (l *LocalStore) PutReflection(_ context.Context, key, content string) error {
	relPath := fmt.Sprintf("dreams/reflections/%s.md", strings.TrimPrefix(strings.TrimSuffix(key, ".json"), "memories/"))
	path := filepath.Join(l.root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// ListReflections returns all persisted reflections with their modification times.
func (l *LocalStore) ListReflections(_ context.Context) (map[string]time.Time, error) {
	reflDir := filepath.Join(l.root, "dreams", "reflections")
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
		// Convert dreams/reflections/opencode/ses_abc.md -> memories/opencode/ses_abc.json
		memoryKey := "memories/" + strings.TrimSuffix(filepath.ToSlash(rel), ".md") + ".json"
		info, err := d.Info()
		if err != nil {
			return nil
		}
		reflections[memoryKey] = info.ModTime()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list reflections: %w", err)
	}
	return reflections, nil
}

// GetReflection reads a reflection's content.
func (l *LocalStore) GetReflection(_ context.Context, memoryKey string) (string, error) {
	relPath := fmt.Sprintf("dreams/reflections/%s.md", strings.TrimPrefix(strings.TrimSuffix(memoryKey, ".json"), "memories/"))
	path := filepath.Join(l.root, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{Key: memoryKey}
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

// ListDreams returns timestamps of all dream snapshots, sorted ascending.
func (l *LocalStore) ListDreams(_ context.Context) ([]string, error) {
	historyDir := filepath.Join(l.root, "dreams", "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list dreams: %w", err)
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

// GetDreamSoul reads the soul from a specific dream snapshot.
func (l *LocalStore) GetDreamSoul(_ context.Context, timestamp string) (string, error) {
	path := filepath.Join(l.root, "dreams", "history", timestamp, "soul.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{Key: fmt.Sprintf("dreams/history/%s/soul.md", timestamp)}
		}
		return "", fmt.Errorf("failed to read dream soul for %s: %w", timestamp, err)
	}
	return string(data), nil
}
