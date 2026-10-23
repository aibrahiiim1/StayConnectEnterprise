package main

// In-memory throttling for guest username/password logins and Email-account actions, on top of the per-account lockout and the durable
// throttle (authsec.go throttleGuard); it blocks brute force across many usernames.
// Two tiers: CLIENT (per IP, device MAC, username+IP, username+MAC) is checked first, and a request it refuses is NOT charged to the
// appliance-wide GLOBAL damper, so one client cannot lock everyone out. Identity counters are keyed on (username, client), never username alone.
// Limits are constants behind the operator-visible sign-in protection settings; state resets on restart. It applies regardless of the
// account lookup, so it never reveals whether a username exists.

import (
	"strings"
	"sync"
	"time"
)

const (
	// loginGlobalPerMinute is the appliance-wide damper: 30 clients at their own cap before it refuses anyone.
	loginGlobalPerMinute = 600
	// loginClientPerMinute caps one source IP, and one device, across every username and action.
	loginClientPerMinute = 20
	// loginIdentityPerClientPerMinute caps one username from one IP or one device.
	loginIdentityPerClientPerMinute = 10
)

type rlWindow struct {
	count int
	start time.Time
}

type loginLimiter struct {
	mu          sync.Mutex
	windows     map[string]*rlWindow
	window      time.Duration
	perEndpoint int
	perIP       int
	perMAC      int
	perUserIP   int
	perUserMAC  int
	lastGC      time.Time
	now         func() time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		windows:     make(map[string]*rlWindow),
		window:      time.Minute,
		perEndpoint: loginGlobalPerMinute,
		perIP:       loginClientPerMinute,
		perMAC:      loginClientPerMinute,
		perUserIP:   loginIdentityPerClientPerMinute,
		perUserMAC:  loginIdentityPerClientPerMinute,
		lastGC:      time.Now(),
		now:         time.Now,
	}
}

func (l *loginLimiter) hit(now time.Time, key string, limit int) bool {
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= l.window {
		l.windows[key] = &rlWindow{count: 1, start: now}
		return true
	}
	w.count++
	return w.count <= limit
}

// allow charges every CLIENT-tier counter (no short-circuit, so a client's counts stay honest); the GLOBAL counter only when the client tier allowed it.
func (l *loginLimiter) allow(username, ip, mac string) bool {
	now := l.now()
	u := strings.ToLower(strings.TrimSpace(username))
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastGC) > 5*l.window {
		for k, w := range l.windows {
			if now.Sub(w.start) >= l.window {
				delete(l.windows, k)
			}
		}
		l.lastGC = now
	}

	ok := true
	if ip != "" && !l.hit(now, "ip|"+ip, l.perIP) {
		ok = false
	}
	if mac != "" && !l.hit(now, "mac|"+mac, l.perMAC) {
		ok = false
	}
	if !l.hit(now, "ui|"+u+"|"+ip, l.perUserIP) {
		ok = false
	}
	if mac != "" && !l.hit(now, "um|"+u+"|"+mac, l.perUserMAC) {
		ok = false
	}
	if !ok {
		return false // refused by its own client tier: costs the other guests nothing
	}
	return l.hit(now, "ep", l.perEndpoint)
}
