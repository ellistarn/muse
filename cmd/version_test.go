package cmd

import "testing"

func TestVersion_ReturnsNonEmpty(t *testing.T) {
	v := version()
	if v == "" {
		t.Fatal("version() returned empty string")
	}
}
