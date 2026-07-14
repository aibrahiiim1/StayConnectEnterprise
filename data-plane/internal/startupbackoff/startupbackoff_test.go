package startupbackoff

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempDir points the tracker at a temp dir so tests don't touch /run.
func withTempDir(t *testing.T) {
	t.Helper()
	d := t.TempDir()
	orig := dirOverride
	dirOverride = d
	t.Cleanup(func() { dirOverride = orig })
}

func TestFastStartsNoBackoff(t *testing.T) {
	withTempDir(t)
	// The first FastStarts restarts must impose no delay (fast transient recovery).
	for i := 0; i < FastStarts; i++ {
		start := time.Now()
		tr := Guard("svc-fast")
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Fatalf("start %d slept %v, expected ~0", i+1, d)
		}
		if tr.LastDelayMS != 0 {
			t.Fatalf("start %d level=%d delay=%dms, expected 0", i+1, tr.Level, tr.LastDelayMS)
		}
	}
}

func TestBackoffEscalatesThenCrashLoop(t *testing.T) {
	withTempDir(t)
	// Simulate rapid restarts by recording starts directly (no real sleeping):
	// after FastStarts, the level and computed delay must grow monotonically and
	// the crash-loop flag must trip at CrashLoopLevel.
	var last int64 = -1
	sawCrashLoop := false
	for i := 0; i < FastStarts+CrashLoopLevel+1; i++ {
		tr := recordOnly("svc-esc", time.Now())
		if tr.Level > 0 {
			if tr.LastDelayMS < last {
				t.Fatalf("delay went backwards: %d < %d", tr.LastDelayMS, last)
			}
			last = tr.LastDelayMS
		}
		if tr.CrashLooping {
			sawCrashLoop = true
		}
	}
	if !sawCrashLoop {
		t.Fatal("expected crash-loop classification after sustained rapid restarts")
	}
}

func TestWindowSelfResets(t *testing.T) {
	withTempDir(t)
	// Record several starts far enough apart that the window prunes them: the
	// count must fall back and backoff clear.
	recordOnly("svc-reset", time.Now().Add(-2*Window))
	recordOnly("svc-reset", time.Now().Add(-2*Window))
	tr := recordOnly("svc-reset", time.Now())
	if tr.CountInWindow != 1 {
		t.Fatalf("expected stale starts pruned, count=%d", tr.CountInWindow)
	}
	if tr.Level != 0 || tr.LastDelayMS != 0 {
		t.Fatalf("expected reset to no-backoff, level=%d delay=%d", tr.Level, tr.LastDelayMS)
	}
}

func TestLoadPersistsState(t *testing.T) {
	withTempDir(t)
	recordOnly("svc-load", time.Now())
	got := Load("svc-load")
	if got.CountInWindow != 1 || got.Service != "svc-load" {
		t.Fatalf("Load did not read persisted tracker: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dirOverride, "svc-load.backoff.json")); err != nil {
		t.Fatalf("tracker file missing: %v", err)
	}
}
