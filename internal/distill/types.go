package distill

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

// Classification pairs an observation with its pattern classification.
type Classification struct {
	Observation    string `json:"observation"`
	Classification string `json:"classification"`
}

// Classifications stores per-observation classifications for a conversation.
type Classifications struct {
	Fingerprint string           `json:"fingerprint"`
	Items       []Classification `json:"items"`
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
