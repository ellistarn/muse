// Package throttle provides adaptive rate limiting for external dependencies.
//
// The central type is [Limiter], an interface that gates access to a dependency.
// Callers [Acquire] a slot before making a request and report the [Outcome]
// when the request completes. The limiter uses this feedback to converge on
// the dependency's effective throughput ceiling.
//
// [AIMDLimiter] is the primary implementation. It uses Additive Increase /
// Multiplicative Decrease: the rate grows linearly on sustained success and
// halves on throttle signals. Published rate limits seed the initial rate and
// cap the ceiling — AIMD discovers the actual sustainable rate.
package throttle

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// Outcome is the result of a dependency call, reported back to the limiter.
type Outcome int

const (
	// Success indicates the call completed normally. The limiter may increase
	// its rate after sustained success.
	Success Outcome = iota
	// Throttled indicates the dependency rejected the call (e.g. HTTP 429).
	// The limiter reduces its rate.
	Throttled
	// Error indicates a non-rate failure (5xx, timeout, network). The limiter
	// holds its current rate.
	Error
)

// Limiter gates access to an external dependency. Implementations are safe
// for concurrent use.
type Limiter interface {
	// Acquire blocks until a request slot is available or ctx is cancelled.
	// The returned function MUST be called exactly once with the call's
	// outcome. Failing to call it leaks a rate-limiter token.
	Acquire(ctx context.Context) (func(Outcome), error)
}

// Config holds the parameters for an [AIMDLimiter].
type Config struct {
	// SeedRate is the initial requests per second. The limiter starts here
	// and adjusts via feedback.
	SeedRate float64
	// MinRate is the floor. The limiter will not drop below this rate even
	// under sustained throttling.
	MinRate float64
	// MaxRate is the ceiling. The limiter will not exceed this rate even
	// under sustained success. Set from published rate limits.
	MaxRate float64
	// GrowthThreshold is the number of consecutive successes required before
	// the rate increases by one request per second.
	GrowthThreshold int
	// BackoffCooldown prevents multiple rate halves within this duration.
	BackoffCooldown time.Duration
	// RecoveryWindow resets the rate to SeedRate after this duration without
	// any throttle signals, if the current rate is below SeedRate.
	RecoveryWindow time.Duration
	// Label is an optional name for log messages (e.g. "bedrock", "slack-search").
	Label string
}

// DefaultConfig returns a Config with the defaults extracted from the
// original Bedrock adaptive rate limiter.
func DefaultConfig() Config {
	return Config{
		SeedRate:        20,
		MinRate:         1,
		MaxRate:         50,
		GrowthThreshold: 10,
		BackoffCooldown: 5 * time.Second,
		RecoveryWindow:  30 * time.Second,
	}
}

// AIMDLimiter implements [Limiter] with Additive Increase / Multiplicative
// Decrease. The algorithm is extracted from the Bedrock client's embedded
// rate limiter — the behavior is identical.
//
// Token bucket: a background goroutine refills a channel at the current rate.
// Acquire blocks on the channel. Outcome feedback adjusts the refill rate.
type AIMDLimiter struct {
	cfg      Config
	tokens   chan struct{}
	mu       sync.Mutex
	rate     float64
	succ     int
	lastBack time.Time
	done     chan struct{}
}

// NewAIMDLimiter creates and starts an adaptive rate limiter. The background
// refill goroutine runs until ctx is cancelled.
func NewAIMDLimiter(ctx context.Context, cfg Config) *AIMDLimiter {
	if cfg.SeedRate <= 0 {
		cfg.SeedRate = DefaultConfig().SeedRate
	}
	if cfg.MinRate <= 0 {
		cfg.MinRate = DefaultConfig().MinRate
	}
	if cfg.MaxRate <= 0 {
		cfg.MaxRate = DefaultConfig().MaxRate
	}
	if cfg.GrowthThreshold <= 0 {
		cfg.GrowthThreshold = DefaultConfig().GrowthThreshold
	}
	if cfg.BackoffCooldown <= 0 {
		cfg.BackoffCooldown = DefaultConfig().BackoffCooldown
	}
	if cfg.RecoveryWindow <= 0 {
		cfg.RecoveryWindow = DefaultConfig().RecoveryWindow
	}

	l := &AIMDLimiter{
		cfg:    cfg,
		tokens: make(chan struct{}, int(cfg.MaxRate)),
		rate:   cfg.SeedRate,
		done:   make(chan struct{}),
	}
	go l.refill(ctx)
	return l
}

// Acquire blocks until a rate-limit token is available.
func (l *AIMDLimiter) Acquire(ctx context.Context) (func(Outcome), error) {
	select {
	case <-l.tokens:
		return l.report, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *AIMDLimiter) report(o Outcome) {
	switch o {
	case Success:
		l.onSuccess()
	case Throttled:
		l.onThrottle()
	case Error:
		// Hold steady — errors are not rate signals.
	}
}

// Rate returns the current requests-per-second rate. Safe for concurrent use.
func (l *AIMDLimiter) Rate() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

// Close stops the background refill goroutine.
func (l *AIMDLimiter) Close() {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
}

func rateToInterval(rate float64) time.Duration {
	if rate <= 0 {
		return time.Second
	}
	ns := float64(time.Second) / rate
	if ns < float64(time.Millisecond) {
		return time.Millisecond
	}
	return time.Duration(ns)
}

func (l *AIMDLimiter) refill(ctx context.Context) {
	interval := rateToInterval(l.currentRate())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.done:
			return
		case <-ticker.C:
			select {
			case l.tokens <- struct{}{}:
			default: // bucket full
			}
			newInterval := rateToInterval(l.currentRate())
			if newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
			}
		}
	}
}

func (l *AIMDLimiter) currentRate() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

func (l *AIMDLimiter) onThrottle() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.succ = 0
	if time.Since(l.lastBack) < l.cfg.BackoffCooldown {
		return
	}
	l.lastBack = time.Now()
	l.rate = max(l.rate/2, l.cfg.MinRate)
	if l.cfg.Label != "" {
		fmt.Fprintf(os.Stderr, "  %s rate → %.0f req/s\n", l.cfg.Label, l.rate)
	}
}

func (l *AIMDLimiter) onSuccess() {
	l.mu.Lock()
	defer l.mu.Unlock()
	// If no throttling for a sustained period and rate is depressed, jump back
	// to seed rate. The AIMD growth (+1 per N successes) is too slow to recover
	// during batch tail when few goroutines remain.
	if !l.lastBack.IsZero() && time.Since(l.lastBack) > l.cfg.RecoveryWindow && l.rate < l.cfg.SeedRate {
		l.rate = l.cfg.SeedRate
		l.succ = 0
		return
	}
	l.succ++
	if l.succ >= l.cfg.GrowthThreshold {
		l.succ = 0
		l.rate = min(l.rate+1, l.cfg.MaxRate)
	}
}

// Nop is a [Limiter] that imposes no rate limit. Acquire returns immediately.
// Useful for tests and local dependencies that don't need flow control.
type Nop struct{}

// Acquire returns immediately with a no-op release function.
func (Nop) Acquire(context.Context) (func(Outcome), error) {
	return func(Outcome) {}, nil
}
