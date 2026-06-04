// Package runner manages the lifecycle of child processes for gonner.
package runner

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/michaelishri/gonner/internal/config"
)

// Backoff implements configurable exponential backoff with jitter.
type Backoff struct {
	initialDelay time.Duration
	maxDelay     time.Duration
	multiplier   float64
	attempt      int
	lastStart    time.Time
}

// NewBackoff creates a Backoff from config. If cfg is nil, defaults are used.
func NewBackoff(cfg *config.BackoffConfig) *Backoff {
	if cfg == nil {
		defaults := config.DefaultBackoff()
		cfg = &defaults
	}
	return &Backoff{
		initialDelay: time.Duration(cfg.InitialDelay),
		maxDelay:     time.Duration(cfg.MaxDelay),
		multiplier:   cfg.Multiplier,
	}
}

// RecordStart records that the process has just started.
// If it was running longer than maxDelay, the backoff counter resets (stability).
func (b *Backoff) RecordStart() {
	now := time.Now()
	if !b.lastStart.IsZero() && now.Sub(b.lastStart) > b.maxDelay {
		b.attempt = 0
	}
	b.lastStart = now
}

// Next returns the delay to wait before the next restart attempt.
func (b *Backoff) Next() time.Duration {
	delay := float64(b.initialDelay) * math.Pow(b.multiplier, float64(b.attempt))
	if delay > float64(b.maxDelay) {
		delay = float64(b.maxDelay)
	}

	// Add ±10% jitter
	jitter := delay * 0.1
	delay = delay - jitter + (rand.Float64() * 2 * jitter)

	b.attempt++
	return time.Duration(delay)
}

// Wait waits for the backoff delay, respecting context cancellation.
// Returns false if the context was cancelled during the wait.
func (b *Backoff) Wait(ctx context.Context) bool {
	delay := b.Next()
	select {
	case <-time.After(delay):
		return true
	case <-ctx.Done():
		return false
	}
}

// Reset resets the backoff counter.
func (b *Backoff) Reset() {
	b.attempt = 0
	b.lastStart = time.Time{}
}
