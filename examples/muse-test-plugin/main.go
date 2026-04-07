// muse-test-plugin is a reference implementation of a muse import plugin.
// It writes sample conversations and source metadata to MUSE_OUTPUT_DIR.
//
// This binary serves two purposes:
//   - Reference for plugin authors showing the output contract
//   - Test fixture for e2e/import_test.go
//
// Usage:
//
//	MUSE_OUTPUT_DIR=/tmp/out muse-test-plugin
//
// The plugin writes conversations as JSON files and a .muse-source.json
// metadata file to the directory specified by MUSE_OUTPUT_DIR.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// conversation mirrors the muse Conversation type. Plugin authors can define
// their own struct or use muse's type directly if they import the module.
type conversation struct {
	SchemaVersion  int       `json:"schema_version"`
	ConversationID string    `json:"conversation_id"`
	Project        string    `json:"project"`
	Title          string    `json:"title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Messages       []message `json:"messages"`
}

type message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type sourceMetadata struct {
	Type string `json:"type"`
}

func main() {
	outputDir := os.Getenv("MUSE_OUTPUT_DIR")
	if outputDir == "" {
		fmt.Fprintln(os.Stderr, "MUSE_OUTPUT_DIR not set")
		os.Exit(1)
	}

	now := time.Now()

	conversations := []conversation{
		{
			SchemaVersion:  1,
			ConversationID: "review-101",
			Project:        "my-service",
			Title:          "Code review: Fix auth bypass",
			CreatedAt:      now.Add(-2 * time.Hour),
			UpdatedAt:      now.Add(-1 * time.Hour),
			Messages: []message{
				{Role: "user", Content: "The auth check should happen before the rate limiter, not after. If we rate-limit first, unauthenticated requests consume budget.", Timestamp: now.Add(-2 * time.Hour)},
				{Role: "assistant", Content: "[GitHub comment by @alice] Good catch. I had it after because the rate limiter was originally per-IP, but now that it's per-token that ordering matters.", Timestamp: now.Add(-110 * time.Minute)},
				{Role: "user", Content: "Exactly. The invariant is: authentication is the first thing that touches a request. Everything downstream can assume identity is established.", Timestamp: now.Add(-100 * time.Minute)},
				{Role: "assistant", Content: "[GitHub comment by @alice] Updated. Auth → rate limit → handler now.", Timestamp: now.Add(-90 * time.Minute)},
			},
		},
		{
			SchemaVersion:  1,
			ConversationID: "review-102",
			Project:        "my-service",
			Title:          "Code review: Add retry logic",
			CreatedAt:      now.Add(-4 * time.Hour),
			UpdatedAt:      now.Add(-3 * time.Hour),
			Messages: []message{
				{Role: "user", Content: "This retry logic needs a backoff strategy. Fixed delays under load just create thundering herds.", Timestamp: now.Add(-4 * time.Hour)},
				{Role: "assistant", Content: "[GitHub comment by @bob] Would exponential backoff with jitter work here?", Timestamp: now.Add(-230 * time.Minute)},
				{Role: "user", Content: "Yes, exponential with full jitter. And cap it — unbounded backoff is just a slow failure.", Timestamp: now.Add(-220 * time.Minute)},
			},
		},
		{
			SchemaVersion:  1,
			ConversationID: "review-103",
			Project:        "infra-tools",
			Title:          "Code review: Refactor config loading",
			CreatedAt:      now.Add(-6 * time.Hour),
			UpdatedAt:      now.Add(-5 * time.Hour),
			Messages: []message{
				{Role: "user", Content: "Config should be validated at load time, not at first use. If the config is invalid, I want to know immediately, not when the first request hits a code path that reads it.", Timestamp: now.Add(-6 * time.Hour)},
				{Role: "assistant", Content: "[GitHub comment by @carol] Makes sense. I'll add a Validate() call in the constructor.", Timestamp: now.Add(-350 * time.Minute)},
				{Role: "user", Content: "Good. And the error message should include the field name and what was wrong with it. 'invalid config' tells you nothing.", Timestamp: now.Add(-340 * time.Minute)},
			},
		},
	}

	// Write source metadata
	meta := sourceMetadata{Type: "human"}
	if err := writeJSON(filepath.Join(outputDir, ".muse-source.json"), meta); err != nil {
		fmt.Fprintf(os.Stderr, "write metadata: %v\n", err)
		os.Exit(1)
	}

	// Write conversations
	for _, conv := range conversations {
		filename := conv.ConversationID + ".json"
		if err := writeJSON(filepath.Join(outputDir, filename), conv); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", filename, err)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "wrote %d conversations\n", len(conversations))
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
