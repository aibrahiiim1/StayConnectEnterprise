package main

// Hotel Admin <-> Central (docs/CENTRAL_CONTROL_PLANE.md section 8).
//
//	GET  /edge/v1/central/status           activation, licence and Central connection -- one document
//	POST /edge/v1/central/refresh          Check now: register if needed, ask Central now, run diagnostics
//	GET  /edge/v1/central/offline-request  the offline activation request this appliance emits
//	POST /edge/v1/central/offline-package  an activation package, or an offline licence package
//	POST /edge/v1/license                  Upload licence file (common.go)
//
// scd computes every state (cmd/scd/central.go); edged authenticates, authorises, rate-limits and audits.
// This file replaces the Cloud Connection page backend, the setup-wizard proxies and the enrollment-token
// submission (enrollment tokens no longer exist), which each reported their own version of these states.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// actionLimiter is a small in-memory fixed-window rate limiter for operator actions that reach Central.
type actionLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newActionLimiter(limit int, window time.Duration) *actionLimiter {
	return &actionLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

func (l *actionLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

// Check now: 6 per source per minute is ample for a person pressing a button, and bounds what one browser tab
// can make the appliance ask of Central.
var checkNowLim = newActionLimiter(6, time.Minute)

// centralStatus proxies the section 8 status. Readable by anyone who can read the licence.
func (s *server) centralStatus(w http.ResponseWriter, r *http.Request) {
	s.scd.proxy(w, r, http.MethodGet, "/v1/central/status", nil)
}

// centralRefresh is Check now.
func (s *server) centralRefresh(w http.ResponseWriter, r *http.Request) {
	if !checkNowLim.allow(clientIP(r), time.Now()) {
		jsonErr(w, http.StatusTooManyRequests, "rate_limited", "Checked a moment ago — wait a minute and try again.")
		return
	}
	s.audit(r, "central.check_now", "appliance", "", nil)
	s.scd.proxy(w, r, http.MethodPost, "/v1/central/refresh", nil)
}

// centralOfflineRequest returns the offline activation request (the appliance generates its identity key
// locally if it has none; the file carries only the public half).
func (s *server) centralOfflineRequest(w http.ResponseWriter, r *http.Request) {
	st, raw, err := s.scd.call(r.Context(), http.MethodGet, "/v1/central/offline-request", nil)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st == http.StatusOK {
		s.audit(r, "central.offline_request_downloaded", "appliance", "", nil)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

// centralOfflinePackage forwards an uploaded package to scd, which decides by shape whether it is a first
// activation package or an offline licence package and verifies it on its own terms. Every outcome is audited.
func (s *server) centralOfflinePackage(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil || len(raw) == 0 || !json.Valid(raw) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "That file is not an activation or licence package.")
		return
	}
	st, resp, err := s.scd.call(r.Context(), http.MethodPost, "/v1/central/offline-package", json.RawMessage(raw))
	if err != nil {
		s.audit(r, "central.offline_package_failed", "appliance", "", map[string]any{"error": err.Error()})
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st >= 200 && st < 300 {
		s.audit(r, "central.offline_package_applied", "appliance", "", nil)
	} else {
		s.audit(r, "central.offline_package_rejected", "appliance", "", map[string]any{"status": st, "detail": string(resp)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// requireAnyWrite admits an operator holding WRITE on any of the given resources. Offline activation changes
// what the appliance is licensed for (the "license" key) and was, until now, gated as a setup action on the
// "network" key; both keep the power they had.
func (s *server) requireAnyWrite(resources ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := sessFrom(r.Context())
			if sess != nil {
				for _, res := range resources {
					if permFor(sess.Roles, res, permWrite) {
						next.ServeHTTP(w, r)
						return
					}
				}
			}
			jsonErr(w, http.StatusForbidden, "forbidden", "insufficient role")
		})
	}
}

// licenceSummary is the small licence fact the dashboard health and the overview show, derived from the SAME
// scd computation as /central/status. ok=false when scd could not be read.
type licenceSummary struct {
	Activation  string
	State       string // section 8 licence state
	ValidUntil  string
	GraceEndsAt string
	DaysLeft    *int
	Central     string
}

func (s *server) readLicenceSummary(ctx context.Context) (licenceSummary, bool) {
	st, raw, err := s.scd.call(ctx, http.MethodGet, "/v1/central/status", nil)
	if err != nil || st != http.StatusOK {
		return licenceSummary{}, false
	}
	var v struct {
		Activation string `json:"activation"`
		License    struct {
			State       string  `json:"state"`
			ValidUntil  *string `json:"valid_until"`
			GraceEndsAt *string `json:"grace_ends_at"`
			DaysLeft    *int    `json:"days_left"`
		} `json:"license"`
		Central struct {
			State string `json:"state"`
		} `json:"central"`
	}
	if json.Unmarshal(raw, &v) != nil || v.License.State == "" {
		return licenceSummary{}, false
	}
	out := licenceSummary{Activation: v.Activation, State: v.License.State, DaysLeft: v.License.DaysLeft,
		Central: v.Central.State}
	if v.License.ValidUntil != nil {
		out.ValidUntil = *v.License.ValidUntil
	}
	if v.License.GraceEndsAt != nil {
		out.GraceEndsAt = *v.License.GraceEndsAt
	}
	return out, true
}
