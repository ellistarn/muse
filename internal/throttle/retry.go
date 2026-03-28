package throttle

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

const (
	defaultBaseBackoff = 2 * time.Second
	defaultMaxBackoff  = 60 * time.Second
	defaultMaxRetries  = 5
)

// RetryConfig configures retry behavior for throttled calls.
type RetryConfig struct {
	MaxRetries  int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// DefaultRetryConfig returns sensible retry defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:  defaultMaxRetries,
		BaseBackoff: defaultBaseBackoff,
		MaxBackoff:  defaultMaxBackoff,
	}
}

// IsThrottled is a predicate that classifies an error as a throttle signal.
type IsThrottled func(error) bool

// Retry executes fn with rate-limited retry. On each attempt it acquires a
// token from the limiter, calls fn, and reports the outcome. Throttled errors
// are retried with exponential backoff. Non-throttle errors are returned
// immediately.
func Retry(ctx context.Context, limiter Limiter, cfg RetryConfig, isThrottled IsThrottled, fn func() error) error {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = defaultMaxRetries
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = defaultBaseBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = defaultMaxBackoff
	}

	var lastErr error
	for attempt := range cfg.MaxRetries {
		release, err := limiter.Acquire(ctx)
		if err != nil {
			return err
		}

		err = fn()
		if err == nil {
			release(Success)
			return nil
		}

		if !isThrottled(err) {
			release(Error)
			return err
		}

		release(Throttled)
		lastErr = err

		backoff := BackoffDuration(attempt, cfg.BaseBackoff, cfg.MaxBackoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return lastErr
}

// BackoffDuration computes exponential backoff with jitter for the given attempt.
func BackoffDuration(attempt int, base, maximum time.Duration) time.Duration {
	backoff := float64(base) * math.Pow(2, float64(attempt))
	if backoff > float64(maximum) {
		backoff = float64(maximum)
	}
	jitter := 0.5 + rand.Float64()*0.5
	return time.Duration(backoff * jitter)
}
