package runner

import (
	"context"
	"testing"
	"time"

	"github.com/michaelishri/gonner/internal/config"
)

func TestNewBackoff_NilConfig(t *testing.T) {
	b := NewBackoff(nil)
	if b.initialDelay != time.Second {
		t.Errorf("default initialDelay: got %v, want 1s", b.initialDelay)
	}
	if b.maxDelay != 30*time.Second {
		t.Errorf("default maxDelay: got %v, want 30s", b.maxDelay)
	}
	if b.multiplier != 2.0 {
		t.Errorf("default multiplier: got %f, want 2.0", b.multiplier)
	}
}

func TestNewBackoff_CustomConfig(t *testing.T) {
	cfg := &config.BackoffConfig{
		InitialDelay: config.Duration(5 * time.Second),
		MaxDelay:     config.Duration(2 * time.Minute),
		Multiplier:   3.0,
	}

	b := NewBackoff(cfg)
	if b.initialDelay != 5*time.Second {
		t.Errorf("initialDelay: got %v, want 5s", b.initialDelay)
	}
	if b.maxDelay != 2*time.Minute {
		t.Errorf("maxDelay: got %v, want 2m", b.maxDelay)
	}
	if b.multiplier != 3.0 {
		t.Errorf("multiplier: got %f, want 3.0", b.multiplier)
	}
}

func TestBackoff_Next_ExponentialGrowth(t *testing.T) {
	b := NewBackoff(&config.BackoffConfig{
		InitialDelay: config.Duration(100 * time.Millisecond),
		MaxDelay:     config.Duration(10 * time.Second),
		Multiplier:   2.0,
	})

	// Collect several delays and verify they grow (approximately)
	// Note: jitter adds ±10%, so we check within bounds
	delays := make([]time.Duration, 5)
	for i := range delays {
		delays[i] = b.Next()
	}

	// Each delay should be roughly 2x the previous (within jitter tolerance)
	for i := 1; i < len(delays); i++ {
		expected := float64(delays[i-1]) * 2.0
		// Allow 40% tolerance for jitter accumulation
		lower := time.Duration(expected * 0.5)
		upper := time.Duration(expected * 1.5)
		if delays[i] < lower || delays[i] > upper {
			t.Errorf("delay[%d]=%v not in expected range [%v, %v] (prev=%v)",
				i, delays[i], lower, upper, delays[i-1])
		}
	}
}

func TestBackoff_Next_CappedAtMaxDelay(t *testing.T) {
	b := NewBackoff(&config.BackoffConfig{
		InitialDelay: config.Duration(1 * time.Second),
		MaxDelay:     config.Duration(5 * time.Second),
		Multiplier:   10.0,
	})

	// After a few iterations, delay should be capped at maxDelay (+ jitter)
	maxWithJitter := 5*time.Second + time.Duration(float64(5*time.Second)*0.15)
	for i := 0; i < 10; i++ {
		delay := b.Next()
		if delay > maxWithJitter {
			t.Errorf("delay %v exceeds max %v (with jitter tolerance %v)", delay, 5*time.Second, maxWithJitter)
		}
	}
}

func TestBackoff_Reset(t *testing.T) {
	b := NewBackoff(&config.BackoffConfig{
		InitialDelay: config.Duration(100 * time.Millisecond),
		MaxDelay:     config.Duration(10 * time.Second),
		Multiplier:   2.0,
	})

	// Advance several steps
	for i := 0; i < 5; i++ {
		b.Next()
	}

	b.Reset()

	// After reset, first delay should be near initialDelay
	delay := b.Next()
	base := float64(100 * time.Millisecond)
	lower := time.Duration(base * 0.85)
	upper := time.Duration(base * 1.15)
	if delay < lower || delay > upper {
		t.Errorf("after reset, delay %v not near initialDelay (expected ~100ms)", delay)
	}
}

func TestBackoff_RecordStart_ResetAfterStability(t *testing.T) {
	b := NewBackoff(&config.BackoffConfig{
		InitialDelay: config.Duration(100 * time.Millisecond),
		MaxDelay:     config.Duration(1 * time.Second),
		Multiplier:   2.0,
	})

	// Advance a few attempts
	for i := 0; i < 5; i++ {
		b.Next()
	}
	currentAttempt := b.attempt

	// Simulate start a long time ago (stable process)
	b.lastStart = time.Now().Add(-2 * time.Second) // > maxDelay
	b.RecordStart()

	if b.attempt != 0 {
		t.Errorf("attempt should reset after stable period: got %d (was %d)", b.attempt, currentAttempt)
	}
}

func TestBackoff_Wait_RespectsContext(t *testing.T) {
	b := NewBackoff(&config.BackoffConfig{
		InitialDelay: config.Duration(10 * time.Second), // Long delay
		MaxDelay:     config.Duration(10 * time.Second),
		Multiplier:   1.0,
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short time
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	ok := b.Wait(ctx)
	elapsed := time.Since(start)

	if ok {
		t.Error("Wait should return false when context is cancelled")
	}
	if elapsed > 1*time.Second {
		t.Errorf("Wait should have returned quickly after cancel, took %v", elapsed)
	}
}

func TestBackoff_Wait_CompletesNormally(t *testing.T) {
	b := NewBackoff(&config.BackoffConfig{
		InitialDelay: config.Duration(10 * time.Millisecond),
		MaxDelay:     config.Duration(10 * time.Millisecond),
		Multiplier:   1.0,
	})

	ctx := context.Background()
	ok := b.Wait(ctx)
	if !ok {
		t.Error("Wait should return true when delay completes normally")
	}
}
