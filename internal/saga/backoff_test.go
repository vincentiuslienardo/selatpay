package saga

import (
	"testing"
	"time"
)

func TestBackoff_DelayBoundedByMax(t *testing.T) {
	b := NewBackoff(100*time.Millisecond, 2*time.Second)
	for attempt := int32(0); attempt < 20; attempt++ {
		got := b.Delay(attempt)
		if got < 0 {
			t.Fatalf("attempt %d: negative delay %s", attempt, got)
		}
		if got > 2*time.Second {
			t.Fatalf("attempt %d: delay %s exceeds max", attempt, got)
		}
	}
}

func TestBackoff_GrowsThenPlateaus(t *testing.T) {
	// With base=10ms, max=80ms, we expect candidate windows
	// (before jitter) of 10, 20, 40, 80, 80, 80... so the maximum
	// possible delay across many samples must reach the max but not
	// exceed it.
	b := NewBackoff(10*time.Millisecond, 80*time.Millisecond)
	const samples = 200
	var maxSeen time.Duration
	for i := 0; i < samples; i++ {
		got := b.Delay(10)
		if got > maxSeen {
			maxSeen = got
		}
	}
	if maxSeen <= 40*time.Millisecond {
		t.Fatalf("expected attempt-10 delay to plateau near max=80ms, only ever saw %s", maxSeen)
	}
	if maxSeen > 80*time.Millisecond {
		t.Fatalf("delay exceeded max: %s", maxSeen)
	}
}

func TestBackoff_AttemptZeroIsBounded(t *testing.T) {
	b := NewBackoff(50*time.Millisecond, time.Second)
	for i := 0; i < 50; i++ {
		got := b.Delay(0)
		if got < 0 || got >= 50*time.Millisecond {
			t.Fatalf("attempt 0 jitter window violated: %s", got)
		}
	}
}

func TestBackoff_DefaultsApplied(t *testing.T) {
	b := NewBackoff(0, 0)
	// Should produce sane non-zero output without panicking.
	if d := b.Delay(0); d < 0 {
		t.Fatalf("default backoff produced negative delay %s", d)
	}
}
