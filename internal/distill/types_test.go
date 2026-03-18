package distill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprint(t *testing.T) {
	// Deterministic
	fp1 := Fingerprint("hello", "world")
	fp2 := Fingerprint("hello", "world")
	if fp1 != fp2 {
		t.Errorf("same inputs should produce same fingerprint: %s != %s", fp1, fp2)
	}

	// Different inputs -> different fingerprint
	fp3 := Fingerprint("hello", "earth")
	if fp1 == fp3 {
		t.Errorf("different inputs should produce different fingerprints")
	}

	// Order matters
	fp4 := Fingerprint("world", "hello")
	if fp1 == fp4 {
		t.Errorf("order should matter: %s == %s", fp1, fp4)
	}

	// Single part vs concatenated — separator prevents collision
	fp5 := Fingerprint("helloworld")
	fp6 := Fingerprint("hello", "world")
	if fp5 == fp6 {
		t.Errorf("single-part vs multi-part should differ")
	}

	// Hex format, 64 chars for SHA-256
	if len(fp1) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(fp1))
	}
}

func TestArtifactStoreObservations(t *testing.T) {
	root := t.TempDir()
	store := NewArtifactStore(root)

	obs := &Observations{
		Fingerprint: "abc123",
		Items:       []string{"obs1", "obs2", "obs3"},
	}

	// Write
	if err := store.PutObservations("opencode", "ses_001", obs); err != nil {
		t.Fatalf("PutObservations: %v", err)
	}

	// File exists
	path := filepath.Join(root, "distill", "observations", "opencode", "ses_001.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}

	// Read back
	got, err := store.GetObservations("opencode", "ses_001")
	if err != nil {
		t.Fatalf("GetObservations: %v", err)
	}
	if got.Fingerprint != "abc123" {
		t.Errorf("fingerprint: got %q, want %q", got.Fingerprint, "abc123")
	}
	if len(got.Items) != 3 {
		t.Errorf("items: got %d, want 3", len(got.Items))
	}

	// List
	list, err := store.ListObservations()
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(list))
	}
	if list[0].Source != "opencode" || list[0].SessionID != "ses_001" {
		t.Errorf("unexpected source/session: %+v", list[0])
	}
}

func TestArtifactStoreLabels(t *testing.T) {
	root := t.TempDir()
	store := NewArtifactStore(root)

	lbl := &Labels{
		Fingerprint: "def456",
		Items: []Label{
			{Observation: "obs1", Label: "pattern of X"},
			{Observation: "obs2", Label: "pattern of Y"},
		},
	}

	if err := store.PutLabels("kiro", "ses_002", lbl); err != nil {
		t.Fatalf("PutLabels: %v", err)
	}

	got, err := store.GetLabels("kiro", "ses_002")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if got.Fingerprint != "def456" {
		t.Errorf("fingerprint: got %q, want %q", got.Fingerprint, "def456")
	}
	if len(got.Items) != 2 {
		t.Errorf("items: got %d, want 2", len(got.Items))
	}
}

func TestArtifactStoreCacheMiss(t *testing.T) {
	root := t.TempDir()
	store := NewArtifactStore(root)

	// Not found returns os.ErrNotExist-wrapped error
	_, err := store.GetObservations("opencode", "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing artifact")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist error, got: %v", err)
	}
}

func TestArtifactStoreDelete(t *testing.T) {
	root := t.TempDir()
	store := NewArtifactStore(root)

	// Create some artifacts
	store.PutObservations("src1", "s1", &Observations{Items: []string{"a"}})
	store.PutObservations("src2", "s2", &Observations{Items: []string{"b"}})
	store.PutLabels("src1", "s1", &Labels{Items: []Label{{Observation: "a", Label: "x"}}})

	// Delete observations for one source
	if err := store.DeleteObservationsForSource("src1"); err != nil {
		t.Fatalf("DeleteObservationsForSource: %v", err)
	}

	// src1 gone, src2 remains
	list, _ := store.ListObservations()
	if len(list) != 1 || list[0].Source != "src2" {
		t.Errorf("expected only src2: %+v", list)
	}

	// Labels untouched
	lblList, _ := store.ListLabels()
	if len(lblList) != 1 {
		t.Errorf("expected 1 label, got %d", len(lblList))
	}

	// Delete all observations
	store.PutObservations("src3", "s3", &Observations{Items: []string{"c"}})
	if err := store.DeleteObservations(); err != nil {
		t.Fatalf("DeleteObservations: %v", err)
	}
	list, _ = store.ListObservations()
	if len(list) != 0 {
		t.Errorf("expected 0 observations after delete all, got %d", len(list))
	}
}

func TestFingerprintCacheValidation(t *testing.T) {
	root := t.TempDir()
	store := NewArtifactStore(root)

	// Simulate: conversation updated at t1 with prompt hash p1
	fp1 := Fingerprint("2024-01-01T00:00:00Z", "prompt-hash-v1")
	obs := &Observations{
		Fingerprint: fp1,
		Items:       []string{"obs1"},
	}
	store.PutObservations("test", "s1", obs)

	// Read back and validate fingerprint matches (cache hit)
	got, _ := store.GetObservations("test", "s1")
	currentFP := Fingerprint("2024-01-01T00:00:00Z", "prompt-hash-v1")
	if got.Fingerprint != currentFP {
		t.Error("expected cache hit")
	}

	// Conversation updated (cache miss)
	newFP := Fingerprint("2024-01-02T00:00:00Z", "prompt-hash-v1")
	if got.Fingerprint == newFP {
		t.Error("expected cache miss when conversation updated")
	}

	// Prompt changed (cache miss)
	promptChangeFP := Fingerprint("2024-01-01T00:00:00Z", "prompt-hash-v2")
	if got.Fingerprint == promptChangeFP {
		t.Error("expected cache miss when prompt changed")
	}
}
