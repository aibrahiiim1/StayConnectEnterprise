// Package startupbackoff gives each service crash-loop backoff in-process: systemd v249 has no exponential
// restart backoff (RestartSteps is v254+), so Guard() sleeps a bounded, jittered, exponential delay when the
// service has started too often in a sliding window. The same logic backs the `svc-run` wrapper for non-Go
// services, and the tracker file is read by the edged health supervisor.
package startupbackoff

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/healthdir"
)

// Dir is on tmpfs so a reboot starts clean: boot transients must not inherit earlier backoff.
const Dir = "/run/stayconnect/health"

// The first FastStarts starts in Window get no extra delay; beyond that it doubles per start up to MaxDelay.
const (
	Window     = 120 * time.Second
	FastStarts = 3
	BaseDelay  = 2 * time.Second
	MaxDelay   = 90 * time.Second
)

type Tracker struct {
	Service        string    `json:"service"`
	Starts         []int64   `json:"starts"`           // unix-nano of recent starts in Window
	CountInWindow  int       `json:"count_in_window"`  // len(Starts) after this start
	Level          int       `json:"level"`            // 0 = transient/fast; >0 = backing off
	LastDelayMS    int64     `json:"last_delay_ms"`    // delay applied on this start
	FirstInWindow  time.Time `json:"first_in_window"`  // oldest start still in the window
	LastStart      time.Time `json:"last_start"`       // when this start happened
	NextEligibleAt time.Time `json:"next_eligible_at"` // when the delay ends (start + delay)
	CrashLooping   bool      `json:"crash_looping"`    // Level past the crash-loop threshold
}

// CrashLoopLevel is the level at which the health supervisor classifies a crash loop.
const CrashLoopLevel = 3

var dirOverride string

func trackerDir() string {
	if dirOverride != "" {
		return dirOverride
	}
	return Dir
}

func path(service string) string { return filepath.Join(trackerDir(), service+".backoff.json") }

// ensureDir makes the tracker dir sticky (1777): every service account may create its own file and remove only
// its own. It holds only non-sensitive restart counters.
func ensureDir() {
	healthdir.Ensure(trackerDir())
}

func Load(service string) Tracker {
	var t Tracker
	b, err := os.ReadFile(path(service))
	if err != nil {
		t.Service = service
		return t
	}
	_ = json.Unmarshal(b, &t)
	t.Service = service
	return t
}

// Guard records this start and sleeps if crash-looping; call once at the top of main(). Never blocks past MaxDelay.
func Guard(service string) Tracker {
	t := recordOnly(service, time.Now())
	if d := time.Duration(t.LastDelayMS) * time.Millisecond; d > 0 {
		time.Sleep(d)
	}
	return t
}

// recordOnly is Guard without the sleep, so tests can drive rapid starts deterministically.
func recordOnly(service string, now time.Time) Tracker {
	t := Load(service)

	cutoff := now.Add(-Window).UnixNano()
	kept := t.Starts[:0]
	for _, s := range t.Starts {
		if s >= cutoff {
			kept = append(kept, s)
		}
	}
	kept = append(kept, now.UnixNano())
	t.Starts = kept
	t.CountInWindow = len(kept)
	t.LastStart = now
	t.FirstInWindow = time.Unix(0, kept[0])

	delay := time.Duration(0)
	level := t.CountInWindow - FastStarts
	if level < 0 {
		level = 0
	}
	t.Level = level
	if level > 0 {
		d := BaseDelay << uint(level-1)
		if d > MaxDelay || d <= 0 {
			d = MaxDelay
		}
		// ±20% jitter so co-failing services don't resonate.
		jitter := time.Duration((rand.Float64()*0.4 - 0.2) * float64(d))
		delay = d + jitter
		if delay < 0 {
			delay = 0
		}
	}
	t.LastDelayMS = delay.Milliseconds()
	t.NextEligibleAt = now.Add(delay)
	t.CrashLooping = level >= CrashLoopLevel

	save(t)
	return t
}

func save(t Tracker) {
	ensureDir()
	b, err := json.Marshal(t)
	if err != nil {
		return
	}
	// Exclusive temp file renamed over the target, so a planted symlink is never written through (several writers are root).
	_ = healthdir.WriteFile(trackerDir(), t.Service+".backoff.json", b)
}
