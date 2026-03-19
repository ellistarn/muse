package distill_test

import (
	"testing"

	"github.com/ellistarn/muse/internal/distill"
)

func TestFingerprintCascadeInvalidation(t *testing.T) {
	fp1 := distill.Fingerprint("2024-01-01T00:00:00Z", "prompt-v1")
	fp2 := distill.Fingerprint("2024-01-02T00:00:00Z", "prompt-v1")
	fp3 := distill.Fingerprint("2024-01-01T00:00:00Z", "prompt-v2")

	if fp1 == fp2 {
		t.Error("conversation update should change fingerprint")
	}
	if fp1 == fp3 {
		t.Error("prompt change should change fingerprint")
	}

	obs1FP := distill.Fingerprint("obs text", "label-prompt-v1")
	obs2FP := distill.Fingerprint("different obs text", "label-prompt-v1")
	if obs1FP == obs2FP {
		t.Error("different observations should produce different label fingerprints")
	}
}

func TestLabelFingerprintIncludesVocabulary(t *testing.T) {
	promptHash := distill.Fingerprint("label-prompt")
	obsTexts := []string{"obs1", "obs2"}

	// Fingerprint with no existing labels
	emptyVocabHash := distill.Fingerprint()
	fp1 := distill.Fingerprint(append(obsTexts, promptHash, emptyVocabHash)...)

	// Fingerprint with a label vocabulary
	vocabHash := distill.Fingerprint("architecture", "testing")
	fp2 := distill.Fingerprint(append(obsTexts, promptHash, vocabHash)...)

	if fp1 == fp2 {
		t.Error("label vocabulary growth should invalidate cached classification")
	}

	// Fingerprint with an expanded vocabulary
	expandedVocabHash := distill.Fingerprint("architecture", "design-philosophy", "testing")
	fp3 := distill.Fingerprint(append(obsTexts, promptHash, expandedVocabHash)...)

	if fp2 == fp3 {
		t.Error("adding a new label should invalidate cached classification")
	}

	// Same vocabulary produces same fingerprint (deterministic)
	vocabHash2 := distill.Fingerprint("architecture", "testing")
	fp4 := distill.Fingerprint(append(obsTexts, promptHash, vocabHash2)...)

	if fp2 != fp4 {
		t.Error("same vocabulary should produce same fingerprint")
	}
}
