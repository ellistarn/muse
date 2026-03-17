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

// clusterPath returns the full path for a cluster artifact.
// pattern: {root}/distill/clusters/{id}.json
func (s *ArtifactStore) clusterPath(id string) string {
	return filepath.Join(s.root, "distill", "clusters", id+".json")
}

// PutObservations writes observations for a conversation.
func (s *ArtifactStore) PutObservations(source, sessionID string, obs *Observations) error {
	return s.putJSON(s.distillPath("observations", source, sessionID), obs)
}

// GetObservations reads observations for a conversation. Returns nil if not found.
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

// GetClassifications reads classifications for a conversation. Returns nil if not found.
func (s *ArtifactStore) GetClassifications(source, sessionID string) (*Classifications, error) {
	var cls Classifications
	if err := s.getJSON(s.distillPath("classifications", source, sessionID), &cls); err != nil {
		return nil, err
	}
	return &cls, nil
}

// PutEmbeddings writes embeddings for a conversation.
func (s *ArtifactStore) PutEmbeddings(source, sessionID string, emb *Embeddings) error {
	return s.putJSON(s.distillPath("embeddings", source, sessionID), emb)
}

// GetEmbeddings reads embeddings for a conversation. Returns nil if not found.
func (s *ArtifactStore) GetEmbeddings(source, sessionID string) (*Embeddings, error) {
	var emb Embeddings
	if err := s.getJSON(s.distillPath("embeddings", source, sessionID), &emb); err != nil {
		return nil, err
	}
	return &emb, nil
}

// PutCluster writes a cluster file (ephemeral, overwritten each run).
func (s *ArtifactStore) PutCluster(id string, cluster *Cluster) error {
	return s.putJSON(s.clusterPath(id), cluster)
}

// ListObservations returns all (source, sessionID) pairs that have observations.
func (s *ArtifactStore) ListObservations() ([]SourceSession, error) {
	return s.listArtifacts("observations")
}

// ListClassifications returns all (source, sessionID) pairs that have classifications.
func (s *ArtifactStore) ListClassifications() ([]SourceSession, error) {
	return s.listArtifacts("classifications")
}

// ListEmbeddings returns all (source, sessionID) pairs that have embeddings.
func (s *ArtifactStore) ListEmbeddings() ([]SourceSession, error) {
	return s.listArtifacts("embeddings")
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

// DeleteEmbeddings removes all embedding artifacts.
func (s *ArtifactStore) DeleteEmbeddings() error {
	return os.RemoveAll(filepath.Join(s.root, "distill", "embeddings"))
}

// DeleteClusters removes all cluster artifacts.
func (s *ArtifactStore) DeleteClusters() error {
	return os.RemoveAll(filepath.Join(s.root, "distill", "clusters"))
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
			return nil
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

func (s *ArtifactStore) putJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal artifact: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func (s *ArtifactStore) getJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return fmt.Errorf("failed to read artifact: %w", err)
	}
	return json.Unmarshal(data, v)
}
