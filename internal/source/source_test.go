package source

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockProvider is a test Provider with configurable behavior.
type mockProvider struct {
	name     string
	sessions []Session
	err      error
	delay    time.Duration
	called   atomic.Bool
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Sessions() ([]Session, error) {
	m.called.Store(true)
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.sessions, m.err
}

func TestProviders_ReturnsAllDefaults(t *testing.T) {
	providers := Providers()
	if len(providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(providers))
	}
	names := map[string]bool{}
	for _, p := range providers {
		names[p.Name()] = true
	}
	for _, want := range []string{"OpenCode", "Claude Code", "Kiro"} {
		if !names[want] {
			t.Errorf("missing provider %q", want)
		}
	}
}

func TestProviders_ImplementInterface(t *testing.T) {
	// Verify each default provider satisfies the Provider interface and
	// returns gracefully when data doesn't exist on this machine.
	for _, p := range Providers() {
		t.Run(p.Name(), func(t *testing.T) {
			sessions, err := p.Sessions()
			// Either returns sessions or nil — should not error when
			// the source simply doesn't exist on this machine.
			if err != nil {
				t.Logf("warning: %s returned error (may be expected in CI): %v", p.Name(), err)
			}
			t.Logf("%s: %d sessions", p.Name(), len(sessions))
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
			sessions: []Session{
				{Source: "slow-a", SessionID: "1"},
				{Source: "slow-a", SessionID: "2"},
			},
		},
		&mockProvider{
			name:  "slow-b",
			delay: 100 * time.Millisecond,
			sessions: []Session{
				{Source: "slow-b", SessionID: "3"},
			},
		},
		&mockProvider{
			name:  "slow-c",
			delay: 100 * time.Millisecond,
			sessions: []Session{
				{Source: "slow-c", SessionID: "4"},
				{Source: "slow-c", SessionID: "5"},
				{Source: "slow-c", SessionID: "6"},
			},
		},
	}

	type result struct {
		name     string
		sessions []Session
		err      error
	}

	start := time.Now()
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			sessions, err := p.Sessions()
			results[i] = result{name: p.Name(), sessions: sessions, err: err}
		}(i, p)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Should complete in ~100ms (parallel), not ~300ms (sequential).
	if elapsed > 250*time.Millisecond {
		t.Errorf("parallel loading took %v, expected <250ms (providers ran sequentially?)", elapsed)
	}

	// Verify all providers were called and results are correct.
	totalSessions := 0
	for i, r := range results {
		if r.err != nil {
			t.Errorf("provider %d (%s) returned error: %v", i, r.name, r.err)
		}
		if !providers[i].(*mockProvider).called.Load() {
			t.Errorf("provider %d (%s) was never called", i, r.name)
		}
		totalSessions += len(r.sessions)
	}
	if totalSessions != 6 {
		t.Errorf("expected 6 total sessions, got %d", totalSessions)
	}

	// Verify order is preserved (results[i] matches providers[i]).
	if results[0].name != "slow-a" || results[1].name != "slow-b" || results[2].name != "slow-c" {
		t.Errorf("result order mismatch: got %s, %s, %s", results[0].name, results[1].name, results[2].name)
	}
}

func TestParallelProviderLoading_ErrorHandling(t *testing.T) {
	providers := []Provider{
		&mockProvider{
			name:     "good",
			sessions: []Session{{Source: "good", SessionID: "1"}},
		},
		&mockProvider{
			name: "bad",
			err:  fmt.Errorf("disk on fire"),
		},
		&mockProvider{
			name:     "also-good",
			sessions: []Session{{Source: "also-good", SessionID: "2"}},
		},
	}

	type result struct {
		name     string
		sessions []Session
		err      error
	}
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			sessions, err := p.Sessions()
			results[i] = result{name: p.Name(), sessions: sessions, err: err}
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

	// Collect sessions from non-errored providers.
	var sessions []Session
	for _, r := range results {
		if r.err == nil {
			sessions = append(sessions, r.sessions...)
		}
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions from good providers, got %d", len(sessions))
	}
}
