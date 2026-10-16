package main

// A FLOOD DAMPER IN FRONT OF THE SIGN-IN ROUTES (audit P-19).
//
// The hotel's sign-in protection -- how many wrong guesses, for how long -- is decided in scd and set by the
// hotel (migration 0068, the Sign-in protection settings). That stays the authority, and this does not replace
// it. What portald lacked was any limit of its own: every POST to a sign-in route became an scd call, so one
// device could drive scd (and, for the code routes, the SMS provider) as fast as it could send, and on a site
// where the durable throttle is switched off nothing slowed it at all.
//
// This is deliberately coarse and generous: a device may make signInBurst sign-in attempts in any
// signInWindow, across all the sign-in routes together, keyed by the connection's address. A person never
// reaches it; a script does. It is a protocol safety bound, not an operational setting a hotel would tune
// (docs/OPERATIONAL_SETTINGS_POLICY.md: protocol constants are not exposed as settings).

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	signInBurst  = 30
	signInWindow = time.Minute
	// signInDamperMaxKeys bounds memory. When it is reached, stale entries are pruned; the map is never wiped
	// (wiping is what reset scd's in-memory recovery-code limiter for everybody at once).
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

// allow records one attempt from key and reports whether it is within the bound.
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

// damped wraps a sign-in route. A refused attempt is answered as the hotel's own throttle answers: the sign-in
// page with "Too many attempts" for a form, 429 JSON for a script. scd is not called.
func (h *handler) damped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.signIns == nil {
			next(w, r)
			return
		}
		ip := clientIP(r)
		key := ""
		if ip != nil {
			key = ip.String()
		} else if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			key = host
		}
		if h.signIns.allow(key) {
			next(w, r)
			return
		}
		w.Header().Set("Retry-After", "60")
		if wantsHTML(r) || r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			// The sign-in page with the sentence, as every other refusal of a form is answered.
			h.landing(w, r, msgTooManyAttempts)
			return
		}
		writeJSONPortal(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
	}
}
