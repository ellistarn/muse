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
