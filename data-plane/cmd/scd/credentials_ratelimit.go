package main

// Layered, in-memory throttling for guest username/password logins and the Email-account actions (register,
// sign-in, grace, forgot). This is a defense-in-depth layer ON TOP of the per-account failed-attempt lockout
// and the durable throttle (authsec.go throttleGuard): it blocks brute-force sources even across many different
// usernames, and is especially important because one-character passwords are permitted.
//
// Fixed-window counters, in two tiers:
//
//	CLIENT tier -- what ONE client may do:
//	  - per source IP           (every action from that address)
//	  - per device MAC          (when known)
//	  - username + source IP
//	  - username + device MAC   (when known)
//	GLOBAL tier -- an appliance-wide damper over all clients.
//
// ONE CLIENT MUST NOT BE ABLE TO LOCK EVERYONE OUT (audit B-10). The global damper used to be charged by every
// request, including requests the client tier would refuse anyway, and it was the only cap one client met
// before the identity caps: a single device sending ~200 forgot/register requests a minute refused every
// guest's sign-in on the appliance. Now:
//   - the client tier is checked FIRST and a request it refuses is NOT charged to the global tier;
//   - one client can therefore put at most clientPerIP requests a minute into the global tier, and the global
//     limit is sized as many times that (loginGlobalPerMinute / loginClientPerMinute distinct busy clients
//     before it bites), so it only ever acts on appliance-wide floods.
//
// The identity counters are keyed on (username, client), never on the username alone, so nobody can exhaust
// someone else's sign-ins by typing their address.
//
// These limits are named constants, not settings: they are an abuse damper behind the operator-visible
// sign-in protection settings (migration 0068), and exposing them would need a schema change and console work
// outside this change (recorded in the audit-remediation report). State is process-local and resets on restart
// -- acceptable for a damper.
//
// It never reveals whether a username exists (the throttle applies before and regardless of the account
// lookup), so it does not aid enumeration.

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
	mu      sync.Mutex
	windows map[string]*rlWindow
	window  time.Duration
	// limits
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

// allow charges one attempt and returns true if it may proceed. Every CLIENT-tier counter is charged (rather
// than short-circuiting) so a client's own counts stay honest; the GLOBAL counter is charged only when the
// client tier allowed the attempt.
func (l *loginLimiter) allow(username, ip, mac string) bool {
	now := l.now()
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
