package throttle

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAIMDLimiter_Acquire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := NewAIMDLimiter(ctx, Config{
		SeedRate: 100, // high rate so test is fast
		MinRate:  1,
		MaxRate:  200,
	})
	defer l.Close()

	// Should be able to acquire and release
	release, err := l.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release(Success)
}

func TestAIMDLimiter_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a moderate rate but drain the bucket so Acquire must wait for a refill.
	l := NewAIMDLimiter(ctx, Config{
		SeedRate: 5,
		MinRate:  1,
		MaxRate:  5,
	})
	defer l.Close()

	// Drain all pre-filled tokens
	for {
		drainCtx, drainCancel := context.WithTimeout(ctx, 5*time.Millisecond)
		_, err := l.Acquire(drainCtx)
		drainCancel()
		if err != nil {
			break
		}
	}

	// Cancel context — next Acquire should fail fast
	cancelCtx, cancelFn := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelFn()

	_, err := l.Acquire(cancelCtx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestAIMDLimiter_ThrottleReducesRate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := NewAIMDLimiter(ctx, Config{
		SeedRate:        100,
		MinRate:         1,
		MaxRate:         200,
		BackoffCooldown: time.Nanosecond, // no cooldown for test
	})
	defer l.Close()

	initial := l.Rate()

	release, _ := l.Acquire(ctx)
	release(Throttled)

	after := l.Rate()
	if after >= initial {
		t.Errorf("rate should decrease after throttle: was %.0f, now %.0f", initial, after)
	}
	if after != initial/2 {
		t.Errorf("expected rate to halve: was %.0f, expected %.0f, got %.0f", initial, initial/2, after)
	}
}

func TestAIMDLimiter_SuccessIncreasesRate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	threshold := 5
	l := NewAIMDLimiter(ctx, Config{
		SeedRate:        10,
		MinRate:         1,
		MaxRate:         200,
		GrowthThreshold: threshold,
	})
	defer l.Close()

	initial := l.Rate()

	// Feed exactly threshold successes
	for range threshold {
		release, _ := l.Acquire(ctx)
		release(Success)
	}

	after := l.Rate()
	if after <= initial {
		t.Errorf("rate should increase after %d successes: was %.0f, now %.0f", threshold, initial, after)
	}
}

func TestAIMDLimiter_ErrorHoldsSteady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := NewAIMDLimiter(ctx, Config{
		SeedRate:        100,
		MinRate:         1,
		MaxRate:         200,
		BackoffCooldown: 0,
	})
	defer l.Close()

	initial := l.Rate()

	release, _ := l.Acquire(ctx)
	release(Error)

	after := l.Rate()
	if after != initial {
		t.Errorf("rate should not change on error: was %.0f, now %.0f", initial, after)
	}
}

func TestAIMDLimiter_MinRateFloor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := NewAIMDLimiter(ctx, Config{
		SeedRate:        100,
		MinRate:         5,
		MaxRate:         200,
		BackoffCooldown: time.Nanosecond,
	})
	defer l.Close()

	// Throttle repeatedly — rate should not go below MinRate
	for range 20 {
		release, _ := l.Acquire(ctx)
		release(Throttled)
	}

	rate := l.Rate()
	if rate < 5 {
		t.Errorf("rate below MinRate: %.0f", rate)
	}
	if rate != 5 {
		t.Errorf("expected rate to settle at MinRate (5), got %.0f", rate)
	}
}

func TestAIMDLimiter_MaxRateCeiling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := NewAIMDLimiter(ctx, Config{
		SeedRate:        98,
		MinRate:         1,
		MaxRate:         100,
		GrowthThreshold: 1,
		BackoffCooldown: time.Nanosecond,
	})
	defer l.Close()

	// Feed many successes — rate should not exceed MaxRate
	for range 20 {
		release, _ := l.Acquire(ctx)
		release(Success)
	}

	rate := l.Rate()
	if rate > 100 {
		t.Errorf("rate above MaxRate: %.0f", rate)
	}
}

func TestAIMDLimiter_RecoveryWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := NewAIMDLimiter(ctx, Config{
		SeedRate:        100,
		MinRate:         1,
		MaxRate:         200,
		BackoffCooldown: time.Nanosecond,
		RecoveryWindow:  10 * time.Millisecond,
	})
	defer l.Close()

	// Throttle to reduce rate
	release, _ := l.Acquire(ctx)
	release(Throttled)

	depressed := l.Rate()
	if depressed >= 100 {
		t.Fatalf("rate should be depressed after throttle: %.0f", depressed)
	}

	// Wait for recovery window
	time.Sleep(20 * time.Millisecond)

	// A success after recovery window should reset to seed rate
	release, _ = l.Acquire(ctx)
	release(Success)

	recovered := l.Rate()
	if recovered != 100 {
		t.Errorf("expected rate to recover to seed (100), got %.0f", recovered)
	}
}

func TestAIMDLimiter_ConcurrentAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := NewAIMDLimiter(ctx, Config{
		SeedRate: 200,
		MinRate:  1,
		MaxRate:  200,
	})
	defer l.Close()

	var wg sync.WaitGroup
	var acquired atomic.Int64

	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				release, err := l.Acquire(ctx)
				if err != nil {
					return
				}
				acquired.Add(1)
				release(Success)
			}
		}()
	}
	wg.Wait()

	if acquired.Load() != 500 {
		t.Errorf("expected 500 acquires, got %d", acquired.Load())
	}
}

func TestAIMDLimiter_BackoffCooldown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := NewAIMDLimiter(ctx, Config{
		SeedRate:        100,
		MinRate:         1,
		MaxRate:         200,
		BackoffCooldown: time.Hour, // very long — second throttle should be ignored
	})
	defer l.Close()

	// First throttle halves
	release, _ := l.Acquire(ctx)
	release(Throttled)
	afterFirst := l.Rate()

	// Second throttle within cooldown — no change
	release, _ = l.Acquire(ctx)
	release(Throttled)
	afterSecond := l.Rate()

	if afterSecond != afterFirst {
		t.Errorf("rate changed during cooldown: %.0f → %.0f", afterFirst, afterSecond)
	}
}

func TestNop_Acquire(t *testing.T) {
	release, err := Nop{}.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Nop.Acquire: %v", err)
	}
	// All outcomes are accepted without panic
	release(Success)
	release(Throttled)
	release(Error)
}
