package bedrock

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ellistarn/muse/internal/inference"
)

type stubRuntime struct {
	out *bedrockruntime.ConverseOutput
	err error
}

func (s stubRuntime) Converse(_ context.Context, _ *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return s.out, s.err
}

func TestConverseMessagesPreservesPartialResponseOnTruncation(t *testing.T) {
	client := NewClientWithRuntime(context.Background(), stubRuntime{
		out: &bedrockruntime.ConverseOutput{
			StopReason: types.StopReasonMaxTokens,
			Output: &types.ConverseOutputMemberMessage{
				Value: types.Message{
					Role: types.ConversationRoleAssistant,
					Content: []types.ContentBlock{
						&types.ContentBlockMemberText{Value: "part one "},
						&types.ContentBlockMemberText{Value: "part two"},
					},
				},
			},
			Usage: &types.TokenUsage{
				InputTokens:  aws.Int32(123),
				OutputTokens: aws.Int32(456),
			},
		},
	})

	resp, err := client.ConverseMessages(context.Background(), "system", []inference.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if resp == nil {
		t.Fatal("expected partial response")
	}
	if got, want := resp.Text, "part one part two"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	if got, want := resp.Usage.InputTokens, 123; got != want {
		t.Fatalf("InputTokens = %d, want %d", got, want)
	}
	if got, want := resp.Usage.OutputTokens, 456; got != want {
		t.Fatalf("OutputTokens = %d, want %d", got, want)
	}
	if !strings.Contains(err.Error(), "response truncated") {
		t.Fatalf("err = %v, want truncation error", err)
	}
}

// newTestClient creates a Client with a pre-filled token bucket and the given
// initial rate. No background refill goroutine is started.
func newTestClient(rate float64) *Client {
	throttle := make(chan struct{}, 100)
	for range 100 {
		throttle <- struct{}{}
	}
	return &Client{
		runtime:    stubRuntime{},
		model:      "test-model",
		throttle:   throttle,
		ratePerSec: rate,
	}
}

func TestOnSuccess_GradualGrowth(t *testing.T) {
	c := newTestClient(10)

	// 9 successes: no rate change yet
	for range growthThreshold - 1 {
		c.onSuccess()
	}
	if got := c.currentRate(); got != 10 {
		t.Fatalf("rate after %d successes = %v, want 10", growthThreshold-1, got)
	}

	// 10th success triggers +1
	c.onSuccess()
	if got := c.currentRate(); got != 11 {
		t.Fatalf("rate after %d successes = %v, want 11", growthThreshold, got)
	}
}

func TestOnSuccess_RecoveryAfterWindow(t *testing.T) {
	c := newTestClient(initialRate)

	// Simulate throttling that depressed the rate
	c.onThrottle()
	depressedRate := c.currentRate()
	if depressedRate >= initialRate {
		t.Fatalf("expected rate below %v after throttle, got %v", initialRate, depressedRate)
	}

	// Successes within the recovery window should NOT reset rate
	for range growthThreshold {
		c.onSuccess()
	}
	if got := c.currentRate(); got == initialRate {
		t.Fatalf("rate should not have jumped to initial within recovery window, got %v", got)
	}

	// Simulate that lastBackoff happened long enough ago to exceed the recovery window
	c.rateMu.Lock()
	c.lastBackoff = time.Now().Add(-recoveryWindow - time.Second)
	c.rateMu.Unlock()

	// Next success should reset to initial rate
	c.onSuccess()
	if got := c.currentRate(); got != initialRate {
		t.Fatalf("rate after recovery window = %v, want %v", got, initialRate)
	}
}

func TestOnSuccess_NoRecoveryWhenAlreadyAtOrAboveInitial(t *testing.T) {
	c := newTestClient(initialRate)

	// Set lastBackoff well in the past
	c.rateMu.Lock()
	c.lastBackoff = time.Now().Add(-recoveryWindow - time.Second)
	c.rateMu.Unlock()

	// Success should use normal growth path, not reset
	// (rate is already at initialRate, so the recovery branch is skipped)
	for range growthThreshold {
		c.onSuccess()
	}
	if got := c.currentRate(); got != initialRate+1 {
		t.Fatalf("rate = %v, want %v (normal growth, not reset)", got, initialRate+1)
	}
}

func TestOnSuccess_NoRecoveryBeforeFirstThrottle(t *testing.T) {
	c := newTestClient(5) // start below initial but never throttled

	// lastBackoff is zero (never throttled) — recovery should not trigger
	for range growthThreshold {
		c.onSuccess()
	}
	if got := c.currentRate(); got != 6 {
		t.Fatalf("rate = %v, want 6 (normal growth, no recovery)", got)
	}
}

func TestOnThrottle_HalvesRate(t *testing.T) {
	c := newTestClient(20)
	c.onThrottle()
	if got := c.currentRate(); got != 10 {
		t.Fatalf("rate after throttle = %v, want 10", got)
	}
}

func TestOnThrottle_RespectsMinRate(t *testing.T) {
	c := newTestClient(minRate)
	c.onThrottle()
	if got := c.currentRate(); got != minRate {
		t.Fatalf("rate after throttle at min = %v, want %v", got, minRate)
	}
}

func TestOnThrottle_CooldownPreventsRepeatedHalving(t *testing.T) {
	c := newTestClient(20)
	c.onThrottle() // 20 → 10
	c.onThrottle() // should be ignored (within cooldown)
	if got := c.currentRate(); got != 10 {
		t.Fatalf("rate after double throttle = %v, want 10 (cooldown should prevent second halving)", got)
	}
}

// TestRateLimiter_BatchTailRecovery is an integration test that reproduces the
// "stuck at 97/100" problem. It simulates a batch of work items processed
// through a live token bucket with the refill goroutine running, where the rate
// has been depressed by earlier throttling and the recovery window has elapsed.
//
// Without the recovery fix, 30 items at 2 req/s = 15s minimum.
// With recovery, the rate resets to initialRate on first success, and the items
// flow through in ~30/initialRate ≈ 1.5s.
func TestRateLimiter_BatchTailRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a client with a live refill goroutine at a depressed rate,
	// simulating the state after throttling during a batch.
	c := &Client{
		runtime:     stubRuntime{},
		model:       "test-model",
		throttle:    make(chan struct{}, int(maxRate)),
		ratePerSec:  2,                                             // depressed from earlier throttling
		lastBackoff: time.Now().Add(-recoveryWindow - time.Second), // recovery window elapsed
	}
	go c.refillTokens(ctx)

	// Simulate batch tail: 30 items, 10 concurrent goroutines, each making
	// one call through retryThrottled. This mirrors the end of the observe
	// phase where a few large conversations are still processing.
	const items = 30
	const concurrency = 10

	var completed atomic.Int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	start := time.Now()
	for range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			err := c.retryThrottled(ctx, func() error {
				return nil // all calls succeed — no more throttling
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			completed.Add(1)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if got := completed.Load(); got != items {
		t.Fatalf("completed %d/%d items", got, items)
	}

	// Without recovery: 30 items at 2 req/s = 15s.
	// With recovery: rate resets to 20 req/s on first success, so 30 items
	// completes in ~1.5s plus goroutine overhead.
	// Use 5s as a generous upper bound.
	if elapsed > 5*time.Second {
		t.Fatalf("batch took %s — rate limiter recovery likely didn't trigger (rate=%v)",
			elapsed.Round(time.Millisecond), c.currentRate())
	}

	// Verify the rate actually recovered
	if got := c.currentRate(); got < initialRate {
		t.Fatalf("rate after batch = %v, want >= %v (recovery should have triggered)", got, initialRate)
	}
	t.Logf("batch completed in %s, final rate=%.0f req/s", elapsed.Round(time.Millisecond), c.currentRate())
}
