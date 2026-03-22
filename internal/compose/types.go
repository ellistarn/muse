package compose

import (
	"crypto/sha256"
	"fmt"
)

// Observations stores discrete observations extracted from a single conversation.
// Each observation is a standalone insight about how the owner thinks or works.
type Observations struct {
	Fingerprint string   `json:"fingerprint"`
	Items       []string `json:"items"`
}

// Label pairs an observation with its pattern label.
type Label struct {
	Observation string `json:"observation"`
	Label       string `json:"label"`
}

// Labels stores per-observation labels for a conversation.
type Labels struct {
	Fingerprint string  `json:"fingerprint"`
	Items       []Label `json:"items"`
}

// Normalization stores the canonical label mapping produced by the normalize step.
type Normalization struct {
	Fingerprint string            `json:"fingerprint"`
	Mapping     map[string]string `json:"mapping"` // original → canonical
}

// Fingerprint computes a hex SHA-256 hash of the given inputs concatenated
// with a null separator. This is used to detect when cached artifacts are stale.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
