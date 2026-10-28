package pmsd

import (
	"testing"
	"time"
)

// Audit P7 L10: the reconnect wait is never below half the current step, and never above it.
func TestBackoff_EqualJitterBounds(t *testing.T) {
	for _, r := range []int64{0, 1, 7, -3, 1 << 62, -(1 << 62)} {
		b := newBackoff(100*time.Millisecond, 800*time.Millisecond, func(int64) int64 { return r })
		step := 100 * time.Millisecond
		for i := 0; i < 6; i++ {
			got := b.next()
			if got < step/2 || got > step {
				t.Fatalf("rnd=%d attempt %d: next() = %v, want in [%v, %v]", r, i, got, step/2, step)
			}
			if step < 800*time.Millisecond {
				step *= 2
			}
		}
	}
}

func TestBackoff_NoRandomIsFullStep(t *testing.T) {
	b := newBackoff(time.Second, 4*time.Second, nil)
	if got := b.next(); got != time.Second {
		t.Fatalf("next() = %v, want 1s", got)
	}
}
