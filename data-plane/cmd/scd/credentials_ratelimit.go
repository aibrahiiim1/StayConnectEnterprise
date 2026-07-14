package main

// Layered, in-memory throttling for guest username/password logins. This is a
// defense-in-depth layer ON TOP of the per-account failed-attempt lockout: it
// blocks brute-force sources even across many different usernames, and is
// especially important because one-character passwords are permitted.
//
// Fixed-window counters keyed by:
//   - endpoint-wide (all guest logins on this appliance)
//   - username + source IP
//   - username + device MAC (when known)
//
// It never reveals whether a username exists (the throttle applies before and
// regardless of the account lookup), so it does not aid enumeration. State is
// process-local and resets on restart — acceptable for a brute-force damper.

import (
	"strings"
	"sync"
	"time"
)

type rlWindow struct {
	count int
	start time.Time
}

type loginLimiter struct {
	mu      sync.Mutex
	windows map[string]*rlWindow
	window  time.Duration
	// limits
	perEndpoint int
	perUserIP   int
	perUserMAC  int
	lastGC      time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		windows:     make(map[string]*rlWindow),
		window:      time.Minute,
		perEndpoint: 200, // whole appliance, per minute
		perUserIP:   10,  // one username from one IP, per minute
		perUserMAC:  10,  // one username from one device, per minute
		lastGC:      time.Now(),
	}
}

// hit records one attempt against key and returns false if key is now over
// limit for the current window.
func (l *loginLimiter) hit(now time.Time, key string, limit int) bool {
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= l.window {
		l.windows[key] = &rlWindow{count: 1, start: now}
		return true
	}
	w.count++
	return w.count <= limit
}

// allow charges one attempt across all layers. Returns true if the attempt may
// proceed. Charging every layer (rather than short-circuiting) keeps counts
// honest for concurrent traffic.
func (l *loginLimiter) allow(username, ip, mac string) bool {
	now := time.Now()
	u := strings.ToLower(strings.TrimSpace(username))
	l.mu.Lock()
	defer l.mu.Unlock()

	// Opportunistic GC so the map can't grow without bound.
	if now.Sub(l.lastGC) > 5*l.window {
		for k, w := range l.windows {
			if now.Sub(w.start) >= l.window {
				delete(l.windows, k)
			}
		}
		l.lastGC = now
	}

	ok := true
	if !l.hit(now, "ep", l.perEndpoint) {
		ok = false
	}
	if !l.hit(now, "ui|"+u+"|"+ip, l.perUserIP) {
		ok = false
	}
	if mac != "" && !l.hit(now, "um|"+u+"|"+mac, l.perUserMAC) {
		ok = false
	}
	return ok
}
