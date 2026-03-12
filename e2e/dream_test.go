package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ellistarn/shade/internal/dream"
	"github.com/ellistarn/shade/internal/llm"
	"github.com/ellistarn/shade/internal/source"
	"github.com/ellistarn/shade/internal/storage"
)

// mockStore implements dream.Store with in-memory state.
type mockStore struct {
	sessions []storage.SessionEntry
	data     map[string]*source.Session
	json     map[string][]byte
	skills   map[string]string
	deleted  []string
}

func newMockStore() *mockStore {
	return &mockStore{
		data:   map[string]*source.Session{},
		json:   map[string][]byte{},
		skills: map[string]string{},
	}
}

func (m *mockStore) addSession(src, id string, modified time.Time, messages []source.Message) {
	key := fmt.Sprintf("memories/%s/%s.json", src, id)
	m.sessions = append(m.sessions, storage.SessionEntry{
		Source:       src,
		SessionID:    id,
		Key:          key,
		LastModified: modified,
	})
	m.data[src+"/"+id] = &source.Session{
		Source:    src,
		SessionID: id,
		Messages:  messages,
	}
}

func (m *mockStore) ListSessions(_ context.Context) ([]storage.SessionEntry, error) {
	return m.sessions, nil
}

func (m *mockStore) GetSession(_ context.Context, src, sessionID string) (*source.Session, error) {
	s, ok := m.data[src+"/"+sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found: %s/%s", src, sessionID)
	}
	return s, nil
}

func (m *mockStore) GetJSON(_ context.Context, key string, v any) error {
	return fmt.Errorf("not found: %s", key) // always fresh start
}

func (m *mockStore) PutJSON(_ context.Context, key string, v any) error {
	return nil
}

func (m *mockStore) DeletePrefix(_ context.Context, prefix string) error {
	m.deleted = append(m.deleted, prefix)
	return nil
}

func (m *mockStore) PutSkill(_ context.Context, name, content string) error {
	m.skills[name] = content
	return nil
}

// mockLLM implements dream.LLM with canned responses.
type mockLLM struct {
	reflectResponse string
	learnResponse   string
	calls           []llmCall
}

type llmCall struct {
	system string
	user   string
}

func (m *mockLLM) Converse(_ context.Context, system, user string) (string, llm.Usage, error) {
	m.calls = append(m.calls, llmCall{system: system, user: user})
	usage := llm.Usage{InputTokens: 100, OutputTokens: 50}
	if strings.Contains(system, "compressing observations") {
		return m.learnResponse, usage, nil
	}
	return m.reflectResponse, usage, nil
}

func TestDreamPipeline(t *testing.T) {
	store := newMockStore()
	store.addSession("claude-code", "sess-1", time.Now(), []source.Message{
		{Role: "user", Content: "use kebab-case for file names"},
		{Role: "assistant", Content: "OK, I'll rename them."},
	})
	store.addSession("claude-code", "sess-2", time.Now(), []source.Message{
		{Role: "user", Content: "never use emojis in commit messages"},
		{Role: "assistant", Content: "Understood."},
	})

	llm := &mockLLM{
		reflectResponse: "- Prefers kebab-case file names\n- No emojis in commits",
		learnResponse: `=== SKILL: naming-conventions ===
---
name: Naming Conventions
description: File and commit naming preferences.
---

Use kebab-case for file names. Never use emojis in commit messages.

=== SKILL: commit-style ===
---
name: Commit Style
description: How to write commits.
---

Keep commit messages plain text, no emojis.`,
	}

	result, err := dream.Run(context.Background(), store, llm, dream.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if result.Skills != 2 {
		t.Errorf("Skills = %d, want 2", result.Skills)
	}
	if result.Pruned != 0 {
		t.Errorf("Pruned = %d, want 0", result.Pruned)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", result.Warnings)
	}

	// Verify skills were written
	if _, ok := store.skills["naming-conventions"]; !ok {
		t.Error("skill 'naming-conventions' not written to store")
	}
	if _, ok := store.skills["commit-style"]; !ok {
		t.Error("skill 'commit-style' not written to store")
	}

	// Verify old skills were cleared first
	if len(store.deleted) == 0 || store.deleted[0] != "skills/" {
		t.Errorf("expected DeletePrefix(\"skills/\"), got %v", store.deleted)
	}

	// Verify LLM was called: 2 reflect + 1 learn = 3 calls
	if len(llm.calls) != 3 {
		t.Errorf("LLM calls = %d, want 3", len(llm.calls))
	}
}

func TestDreamPipelineNoMemories(t *testing.T) {
	store := newMockStore()
	llm := &mockLLM{}

	result, err := dream.Run(context.Background(), store, llm, dream.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Processed != 0 {
		t.Errorf("Processed = %d, want 0", result.Processed)
	}
	if result.Skills != 0 {
		t.Errorf("Skills = %d, want 0", result.Skills)
	}
	if len(llm.calls) != 0 {
		t.Errorf("LLM calls = %d, want 0", len(llm.calls))
	}
}

func TestDreamPipelineLimit(t *testing.T) {
	store := newMockStore()
	for i := 0; i < 5; i++ {
		store.addSession("test", fmt.Sprintf("sess-%d", i), time.Now(), []source.Message{
			{Role: "user", Content: fmt.Sprintf("message %d", i)},
			{Role: "assistant", Content: "ok"},
		})
	}

	llm := &mockLLM{
		reflectResponse: "- observation",
		learnResponse: `=== SKILL: test-skill ===
---
name: Test
description: Test skill.
---

Content here.`,
	}

	result, err := dream.Run(context.Background(), store, llm, dream.Options{Limit: 2})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// 2 reflect calls + 1 learn call
	if result.Processed != 2 {
		t.Errorf("Processed = %d, want 2", result.Processed)
	}
	if len(llm.calls) != 3 {
		t.Errorf("LLM calls = %d, want 3 (2 reflect + 1 learn)", len(llm.calls))
	}
}

func TestDreamPipelineEmptyConversation(t *testing.T) {
	store := newMockStore()
	// Session with only empty messages produces no observations
	store.addSession("test", "empty", time.Now(), []source.Message{
		{Role: "user", Content: ""},
		{Role: "assistant", Content: ""},
	})

	llm := &mockLLM{}

	result, err := dream.Run(context.Background(), store, llm, dream.Options{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// Empty conversation produces no reflect call, but still shows up in pending
	if result.Processed != 0 {
		t.Errorf("Processed = %d, want 0", result.Processed)
	}
}

func TestDreamPipelineReprocess(t *testing.T) {
	store := newMockStore()
	store.addSession("test", "sess-1", time.Now(), []source.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})

	llm := &mockLLM{
		reflectResponse: "- observation",
		learnResponse: `=== SKILL: test ===
---
name: Test
description: A test skill.
---

Content.`,
	}

	// First run
	_, err := dream.Run(context.Background(), store, llm, dream.Options{})
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	// With Reprocess, it should process again even though state would normally prune it
	llm.calls = nil
	result, err := dream.Run(context.Background(), store, llm, dream.Options{Reprocess: true})
	if err != nil {
		t.Fatalf("reprocess Run() error: %v", err)
	}
	if result.Processed != 1 {
		t.Errorf("Processed = %d, want 1", result.Processed)
	}
}

func TestParseSkillsResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "basic",
			input: `=== SKILL: code-style ===
---
name: Code Style
description: Coding preferences.
---

Use tabs not spaces.`,
			want: map[string]string{
				"code-style": "---\nname: Code Style\ndescription: Coding preferences.\n---\n\nUse tabs not spaces.",
			},
		},
		{
			name:  "wrapped in code fences",
			input: "```\n=== SKILL: test ===\n---\nname: Test\ndescription: Test.\n---\n\nContent.\n```",
			want: map[string]string{
				"test": "---\nname: Test\ndescription: Test.\n---\n\nContent.",
			},
		},
		{
			name: "multiple skills",
			input: `=== SKILL: a ===
Content A.

=== SKILL: b ===
Content B.`,
			want: map[string]string{
				"a": "Content A.",
				"b": "Content B.",
			},
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no skills delimiter",
			input:   "just some random text without any skills",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dream.ParseSkillsResponse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSkillsResponse() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d skills, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("skill %q:\n  got:  %q\n  want: %q", k, got[k], v)
				}
			}
		})
	}
}
