package main

// Flood damper in front of the sign-in routes. scd stays the authority on wrong-guess limits; this coarse bound
// (signInBurst attempts per signInWindow per connection address, all sign-in routes together) stops one device driving
// scd and the SMS provider as fast as it can send. A protocol safety bound, not a hotel setting.

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	signInBurst  = 30
	signInWindow = time.Minute
	// signInDamperMaxKeys bounds memory; stale entries are pruned and the map is never wiped (wiping reset scd's recovery-code limiter for everybody at once).
	signInDamperMaxKeys = 20000
)

type signInDamper struct {
	mu   sync.Mutex
	now  func() time.Time
	hits map[string][]time.Time
}

func newSignInDamper() *signInDamper {
	return &signInDamper{now: time.Now, hits: map[string][]time.Time{}}
}

func (d *signInDamper) allow(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	kept := d.hits[key][:0]
	for _, t := range d.hits[key] {
		if now.Sub(t) < signInWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= signInBurst {
		d.hits[key] = kept
		return false
	}
	d.hits[key] = append(kept, now)
	if len(d.hits) > signInDamperMaxKeys {
		for k, ts := range d.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= signInWindow {
				delete(d.hits, k)
			}
		}
	}
	return true
}

// dampAllows counts the request against its connection address; true when the damper is off.
func (h *handler) dampAllows(r *http.Request) bool {
	if h.signIns == nil {
		return true
	}
	ip := clientIP(r)
	key := ""
	if ip != nil {
		key = ip.String()
	} else if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		key = host
	}
	return h.signIns.allow(key)
}

// dampedBudget is damped for the routes that answer every refusal through the Phase-3 failure-time budget: the
// damper's refusal waits out the same budget and is written by the route's own refusal, so neither its timing nor
// its shape stands out (audit P5 L2).
func (h *handler) dampedBudget(next http.HandlerFunc, refuse func(http.ResponseWriter, *http.Request, *phase3Budget)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.dampAllows(r) {
			next(w, r)
			return
		}
		b := h.newPhase3Budget(r)
		defer b.cancel()
		refuse(w, r, b)
	}
}

// refuseDampedSignIn is the rate-limited sentence with no number: the damper does not know scd's wait.
func (h *handler) refuseDampedSignIn(w http.ResponseWriter, r *http.Request, b *phase3Budget) {
	h.phase3FailRetryAfter(w, r, b, "portal_damped", "RATE_LIMITED", 0)
}

func (h *handler) refuseDampedDevice(w http.ResponseWriter, r *http.Request, b *phase3Budget) {
	h.deviceFail(w, r, b, "portal_damped")
}

// damped refuses with the hotel's own throttle answer (sign-in page for a form, 429 JSON for a script) without calling scd.
func (h *handler) damped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.dampAllows(r) {
			next(w, r)
			return
		}
		w.Header().Set("Retry-After", "60")
		if wantsHTML(r) || r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			h.landing(w, r, msgTooManyAttempts)
			return
		}
		writeJSONPortal(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
	}
}
