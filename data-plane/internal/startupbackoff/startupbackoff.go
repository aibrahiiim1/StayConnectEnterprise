// Package startupbackoff gives every appliance service adaptive, crash-loop-safe
// startup backoff without a central controller fighting systemd.
//
// systemd on the appliance (v249) has no native exponential restart backoff
// (RestartSteps/RestartMaxDelaySec are v254+), and a flat Restart=always +
// RestartSec=2 means a permanently broken service restarts every 2s forever —
// a restart storm with no backoff. This package fixes that at the process level:
// each service calls Guard() at startup; if it is crash-looping (too many starts
// in a short sliding window) Guard sleeps for a bounded, jittered, exponentially
// increasing delay BEFORE the service does any work. A transient failure (a few
// recent starts) returns immediately, so recovery stays fast. The window slides,
// so after a period of stability the backoff self-resets to zero.
//
// Because the delay is imposed by the service ON ITSELF (systemd still just
// restarts at RestartSec), there is no second controller racing systemd. The
// same logic backs the `svc-run` exec wrapper used for non-Go services
// (caddy/kea/unbound/node), so every appliance service gets identical adaptive
// backoff. The tracker file also exposes the live backoff state (count, level,
// next-retry) that the edged health supervisor reads for diagnosis.
package startupbackoff

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

// Dir holds the per-service backoff trackers. It lives on tmpfs (/run) on
// purpose: a reboot is a clean slate — boot-time transients must not inherit
// backoff accumulated before the reboot.
const Dir = "/run/stayconnect/health"

// Tuning. Base restart cadence is systemd's RestartSec (~2s). The first
// FastStarts restarts in Window get NO extra delay (transient recovery stays
// fast); beyond that the delay doubles per extra restart up to MaxDelay.
const (
	Window     = 120 * time.Second
	FastStarts = 3
	BaseDelay  = 2 * time.Second
	MaxDelay   = 90 * time.Second
)

// Tracker is the persisted per-service backoff state (also read by the health
// supervisor for diagnosis).
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

// CrashLoopLevel is the backoff level at/above which a service is classified as
// a crash loop (used by the health supervisor). Level 3 ⇒ ~6th restart in the
// window with delays already at 8s+.
const CrashLoopLevel = 3

// dirOverride redirects the tracker dir in tests; empty means use Dir.
var dirOverride string

func trackerDir() string {
	if dirOverride != "" {
		return dirOverride
	}
	return Dir
}

func path(service string) string { return filepath.Join(trackerDir(), service+".backoff.json") }

// ensureDir creates the shared tracker dir writable by every service user
// (services run as root/stayconnect/caddy/unbound and each writes its own
// tracker; edged reads them all). It lives on tmpfs and holds only non-sensitive
// restart counters, so a shared-writable mode is acceptable.
func ensureDir() {
	d := trackerDir()
	_ = os.MkdirAll(d, 0o777)
	_ = os.Chmod(d, 0o777)
}

// Load returns the current tracker for a service (zero value if none/unreadable).
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

// Guard records this process start and, if the service is crash-looping, sleeps
// for a bounded jittered exponential delay before returning. Safe to call once
// at the very top of a service's main(). Never blocks longer than MaxDelay.
func Guard(service string) Tracker {
	t := recordOnly(service, time.Now())
	if d := time.Duration(t.LastDelayMS) * time.Millisecond; d > 0 {
		time.Sleep(d)
	}
	return t
}

// recordOnly performs the full record + backoff computation + persistence for a
// start at time `now`, WITHOUT sleeping. Guard wraps it with the sleep; tests
// use it to drive many rapid starts deterministically.
func recordOnly(service string, now time.Time) Tracker {
	t := Load(service)

	// Prune starts older than the sliding window, then record this start.
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

	// Compute the backoff level and delay. The first FastStarts are free.
	delay := time.Duration(0)
	level := t.CountInWindow - FastStarts
	if level < 0 {
		level = 0
	}
	t.Level = level
	if level > 0 {
		d := BaseDelay << uint(level-1) // 2s, 4s, 8s, 16s, ...
		if d > MaxDelay || d <= 0 {
			d = MaxDelay
		}
		// ±20% jitter so a fleet of co-failing services doesn't resonate.
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
	tmp := path(t.Service) + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path(t.Service))
	}
}
