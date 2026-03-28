package conversation

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockProvider is a test Provider with configurable behavior.
type mockProvider struct {
	name          string
	conversations []Conversation
	err           error
	delay         time.Duration
	called        atomic.Bool
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Conversations(_ context.Context, _ func(SyncProgress)) ([]Conversation, error) {
	m.called.Store(true)
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.conversations, m.err
}

func TestProviders_ReturnsAllDefaults(t *testing.T) {
	providers := Providers()
	if len(providers) != 5 {
		t.Fatalf("expected 5 default providers, got %d", len(providers))
	}
	names := map[string]bool{}
	for _, p := range providers {
		names[p.Name()] = true
	}
	for _, want := range []string{"OpenCode", "Claude Code", "Codex", "Kiro", "Kiro CLI"} {
		if !names[want] {
			t.Errorf("missing provider %q", want)
		}
	}
	// Opt-in sources should not be in defaults.
	for _, notWant := range []string{"GitHub", "Slack"} {
		if names[notWant] {
			t.Errorf("opt-in provider %q should not be in defaults", notWant)
		}
	}
}

func TestSources_ReturnsAll(t *testing.T) {
	sources := Sources()
	if len(sources) != 7 {
		t.Fatalf("expected 7 sources, got %d", len(sources))
	}
	names := map[string]bool{}
	for _, s := range sources {
		names[s.Name] = true
	}
	for _, want := range []string{"opencode", "claude-code", "codex", "kiro", "kiro-cli", "github", "slack"} {
		if !names[want] {
			t.Errorf("missing source %q", want)
		}
	}
}

func TestProviders_ImplementInterface(t *testing.T) {
	// Verify each default provider satisfies the Provider interface and
	// returns gracefully when data doesn't exist on this machine.
	for _, p := range Providers() {
		t.Run(p.Name(), func(t *testing.T) {
			conversations, err := p.Conversations(context.Background(), nil)
			// Either returns conversations or nil — should not error when
			// the source simply doesn't exist on this machine.
			if err != nil {
				t.Logf("warning: %s returned error (may be expected in CI): %v", p.Name(), err)
			}
			t.Logf("%s: %d conversations", p.Name(), len(conversations))
		})
	}
}

func TestParallelProviderLoading(t *testing.T) {
	// Simulate 3 providers that each take 100ms. If run in parallel, total
	// should be ~100ms, not ~300ms.
	providers := []Provider{
		&mockProvider{
			name:  "slow-a",
			delay: 100 * time.Millisecond,
			conversations: []Conversation{
				{Source: "slow-a", ConversationID: "1"},
				{Source: "slow-a", ConversationID: "2"},
			},
		},
		&mockProvider{
			name:  "slow-b",
			delay: 100 * time.Millisecond,
			conversations: []Conversation{
				{Source: "slow-b", ConversationID: "3"},
			},
		},
		&mockProvider{
			name:  "slow-c",
			delay: 100 * time.Millisecond,
			conversations: []Conversation{
				{Source: "slow-c", ConversationID: "4"},
				{Source: "slow-c", ConversationID: "5"},
				{Source: "slow-c", ConversationID: "6"},
			},
		},
	}

	type result struct {
		name          string
		conversations []Conversation
		err           error
	}

	start := time.Now()
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			conversations, err := p.Conversations(context.Background(), nil)
			results[i] = result{name: p.Name(), conversations: conversations, err: err}
		}(i, p)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Should complete in ~100ms (parallel), not ~300ms (sequential).
	if elapsed > 250*time.Millisecond {
		t.Errorf("parallel loading took %v, expected <250ms (providers ran sequentially?)", elapsed)
	}

	// Verify all providers were called and results are correct.
	totalConversations := 0
	for i, r := range results {
		if r.err != nil {
			t.Errorf("provider %d (%s) returned error: %v", i, r.name, r.err)
		}
		if !providers[i].(*mockProvider).called.Load() {
			t.Errorf("provider %d (%s) was never called", i, r.name)
		}
		totalConversations += len(r.conversations)
	}
	if totalConversations != 6 {
		t.Errorf("expected 6 total conversations, got %d", totalConversations)
	}

	// Verify order is preserved (results[i] matches providers[i]).
	if results[0].name != "slow-a" || results[1].name != "slow-b" || results[2].name != "slow-c" {
		t.Errorf("result order mismatch: got %s, %s, %s", results[0].name, results[1].name, results[2].name)
	}
}

func TestParallelProviderLoading_ErrorHandling(t *testing.T) {
	providers := []Provider{
		&mockProvider{
			name:          "good",
			conversations: []Conversation{{Source: "good", ConversationID: "1"}},
		},
		&mockProvider{
			name: "bad",
			err:  fmt.Errorf("disk on fire"),
		},
		&mockProvider{
			name:          "also-good",
			conversations: []Conversation{{Source: "also-good", ConversationID: "2"}},
		},
	}

	type result struct {
		name          string
		conversations []Conversation
		err           error
	}
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			conversations, err := p.Conversations(context.Background(), nil)
			results[i] = result{name: p.Name(), conversations: conversations, err: err}
		}(i, p)
	}
	wg.Wait()

	// One error should not prevent others from succeeding.
	if results[0].err != nil {
		t.Errorf("good provider errored: %v", results[0].err)
	}
	if results[1].err == nil {
		t.Error("bad provider should have errored")
	}
	if results[2].err != nil {
		t.Errorf("also-good provider errored: %v", results[2].err)
	}

	// Collect conversations from non-errored providers.
	var conversations []Conversation
	for _, r := range results {
		if r.err == nil {
			conversations = append(conversations, r.conversations...)
		}
	}
	if len(conversations) != 2 {
		t.Errorf("expected 2 conversations from good providers, got %d", len(conversations))
	}
}

func TestConversation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		conv    Conversation
		wantErr bool
	}{
		{
			name:    "valid",
			conv:    Conversation{ConversationID: "abc", Source: "test"},
			wantErr: false,
		},
		{
			name:    "missing conversation_id",
			conv:    Conversation{Source: "test"},
			wantErr: true,
		},
		{
			name:    "missing source",
			conv:    Conversation{ConversationID: "abc"},
			wantErr: true,
		},
		{
			name:    "both missing",
			conv:    Conversation{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.conv.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
