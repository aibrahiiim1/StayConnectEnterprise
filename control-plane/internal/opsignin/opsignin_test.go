package opsignin

import (
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

func TestNextStepNeverRepeatsAndWaitsOnlyWhenItMust(t *testing.T) {
	now := time.Unix(1_800_000_015, 0) // 15 s into its step
	cur := totp.Step(now)
	var slept time.Duration
	sleep := func(d time.Duration) { slept += d }

	if s := nextStep(0, now, sleep); s != cur || slept != 0 {
		t.Fatalf("fresh: step %d slept %v, want %d and no wait", s, slept, cur)
	}
	if s := nextStep(cur, now, sleep); s != cur+1 || slept != 0 {
		t.Fatalf("after now: step %d slept %v, want %d (inside the skew) and no wait", s, slept, cur+1)
	}
	if s := nextStep(cur+1, now, sleep); s != cur+2 || slept < 15*time.Second || slept > 16*time.Second {
		t.Fatalf("after now+1: step %d slept %v, want %d after about 15 s", s, slept, cur+2)
	}
}
