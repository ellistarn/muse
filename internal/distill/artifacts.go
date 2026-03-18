package distill

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ArtifactStore handles reading and writing distill pipeline artifacts.
// Artifacts live under {root}/distill/ and are organized by type, source,
// and session ID. This is separate from the main storage.Store interface
// because distill artifacts are pipeline internals, not domain objects.
type ArtifactStore struct {
	root string // e.g. ~/.muse
}

// NewArtifactStore creates an artifact store rooted at the given directory.
func NewArtifactStore(root string) *ArtifactStore {
	return &ArtifactStore{root: root}
}

// distillPath returns the full path for a distill artifact.
// pattern: {root}/distill/{kind}/{source}/{sessionID}.json
func (s *ArtifactStore) distillPath(kind, source, sessionID string) string {
	return filepath.Join(s.root, "distill", kind, source, sessionID+".json")
}

// PutObservations writes observations for a conversation.
func (s *ArtifactStore) PutObservations(source, sessionID string, obs *Observations) error {
	return s.putJSON(s.distillPath("observations", source, sessionID), obs)
}

// GetObservations reads observations for a conversation.
// Returns os.ErrNotExist (via os.IsNotExist) when no artifact is found.
func (s *ArtifactStore) GetObservations(source, sessionID string) (*Observations, error) {
	var obs Observations
	if err := s.getJSON(s.distillPath("observations", source, sessionID), &obs); err != nil {
		return nil, err
	}
	return &obs, nil
}

// PutClassifications writes classifications for a conversation.
func (s *ArtifactStore) PutClassifications(source, sessionID string, cls *Classifications) error {
	return s.putJSON(s.distillPath("classifications", source, sessionID), cls)
}

// GetClassifications reads classifications for a conversation.
// Returns os.ErrNotExist (via os.IsNotExist) when no artifact is found.
func (s *ArtifactStore) GetClassifications(source, sessionID string) (*Classifications, error) {
	var cls Classifications
	if err := s.getJSON(s.distillPath("classifications", source, sessionID), &cls); err != nil {
		return nil, err
	}
	return &cls, nil
}

// ListObservations returns all (source, sessionID) pairs that have observations.
func (s *ArtifactStore) ListObservations() ([]SourceSession, error) {
	return s.listArtifacts("observations")
}

// ListClassifications returns all (source, sessionID) pairs that have classifications.
func (s *ArtifactStore) ListClassifications() ([]SourceSession, error) {
	return s.listArtifacts("classifications")
}

// DeleteObservations removes all observation artifacts.
func (s *ArtifactStore) DeleteObservations() error {
	return os.RemoveAll(filepath.Join(s.root, "distill", "observations"))
}

// DeleteObservationsForSource removes observation artifacts for a specific source.
func (s *ArtifactStore) DeleteObservationsForSource(source string) error {
	return os.RemoveAll(filepath.Join(s.root, "distill", "observations", source))
}

// DeleteClassifications removes all classification artifacts.
func (s *ArtifactStore) DeleteClassifications() error {
	return os.RemoveAll(filepath.Join(s.root, "distill", "classifications"))
}

// SourceSession identifies a conversation by its source and session ID.
type SourceSession struct {
	Source    string
	SessionID string
}

func (s *ArtifactStore) listArtifacts(kind string) ([]SourceSession, error) {
	dir := filepath.Join(s.root, "distill", kind)
	var results []SourceSession
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("filepath.Rel(%s, %s): %w", dir, path, err)
		}
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		if len(parts) != 2 {
			return nil
		}
		results = append(results, SourceSession{
			Source:    parts[0],
			SessionID: strings.TrimSuffix(parts[1], ".json"),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list %s artifacts: %w", kind, err)
	}
	return results, nil
}

// putJSON atomically writes JSON to path using write-to-temp + rename.
func (s *ArtifactStore) putJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal artifact: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("failed to write artifact: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("failed to rename artifact: %w", err)
	}
	return nil
}

func (s *ArtifactStore) getJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return fmt.Errorf("failed to read artifact %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("failed to parse artifact %s: %w", path, err)
	}
	return nil
}
