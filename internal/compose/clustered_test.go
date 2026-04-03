package compose_test

import (
	"testing"

	"github.com/ellistarn/muse/internal/compose"
)

func TestFingerprintCascadeInvalidation(t *testing.T) {
	fp1 := compose.Fingerprint("2024-01-01T00:00:00Z", "prompt-v1")
	fp2 := compose.Fingerprint("2024-01-02T00:00:00Z", "prompt-v1")
	fp3 := compose.Fingerprint("2024-01-01T00:00:00Z", "prompt-v2")

	if fp1 == fp2 {
		t.Error("conversation update should change fingerprint")
	}
	if fp1 == fp3 {
		t.Error("prompt change should change fingerprint")
	}

	obs1FP := compose.Fingerprint("obs text", "label-prompt-v1")
	obs2FP := compose.Fingerprint("different obs text", "label-prompt-v1")
	if obs1FP == obs2FP {
		t.Error("different observations should produce different label fingerprints")
	}
}

func TestLabelFingerprintIncludesVocabulary(t *testing.T) {
	promptHash := compose.Fingerprint("label-prompt")
	obsTexts := []string{"obs1", "obs2"}

	// Fingerprint with no existing labels
	emptyVocabHash := compose.Fingerprint()
	fp1 := compose.Fingerprint(append(obsTexts, promptHash, emptyVocabHash)...)

	// Fingerprint with a label vocabulary
	vocabHash := compose.Fingerprint("architecture", "testing")
	fp2 := compose.Fingerprint(append(obsTexts, promptHash, vocabHash)...)

	if fp1 == fp2 {
		t.Error("label vocabulary growth should invalidate cached classification")
	}

	// Fingerprint with an expanded vocabulary
	expandedVocabHash := compose.Fingerprint("architecture", "design-philosophy", "testing")
	fp3 := compose.Fingerprint(append(obsTexts, promptHash, expandedVocabHash)...)

	if fp2 == fp3 {
		t.Error("adding a new label should invalidate cached classification")
	}

	// Same vocabulary produces same fingerprint (deterministic)
	vocabHash2 := compose.Fingerprint("architecture", "testing")
	fp4 := compose.Fingerprint(append(obsTexts, promptHash, vocabHash2)...)

	if fp2 != fp4 {
		t.Error("same vocabulary should produce same fingerprint")
	}
}

func TestValidateThesis(t *testing.T) {
	validThesis := `## Identity

Builds infrastructure at the platform layer.

## Thesis

Systems should converge toward declared intent.

## Structure

- Cluster 1: **core** — convergence as architecture
- Cluster 2: **core** — naming as design
- Cluster 3: **supporting** — enriches Cluster 1
`

	// All clusters referenced — should pass
	if err := compose.ValidateThesis(validThesis, 3); err != nil {
		t.Errorf("valid thesis should pass: %v", err)
	}

	// Missing Cluster 2 — should fail
	missingCluster := `## Structure

- Cluster 1: **core** — convergence
- Cluster 3: **supporting** — enriches Cluster 1
`
	err := compose.ValidateThesis(missingCluster, 3)
	if err == nil {
		t.Error("thesis missing Cluster 2 should fail validation")
	}

	// Zero clusters — should pass (vacuous truth)
	if err := compose.ValidateThesis("", 0); err != nil {
		t.Errorf("zero clusters should pass: %v", err)
	}
}
