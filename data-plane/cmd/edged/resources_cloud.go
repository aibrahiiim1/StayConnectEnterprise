package main

// Hotel Admin — Cloud Connection page backend (carryover F). Aggregates the
// appliance's real cloud link status from scd (identity/endpoints, license,
// outbox) plus a LIVE reachability + certificate probe of the Central Control
// Plane. No fake "Connected" state: connection health is derived from the live
// probe and the outbox/heartbeat facts. Secrets are never returned.

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// enrollLimiter is a small in-memory fixed-window rate limiter for enrollment
// attempts (defence against token brute force from the management network).
type enrollLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newEnrollLimiter(limit int, window time.Duration) *enrollLimiter {
	return &enrollLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

func (l *enrollLimiter) allow(key string, now time.Time) bool {
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

// 5 enrollment submissions per source per minute is ample for a real install.
var enrollLim = newEnrollLimiter(5, time.Minute)

func (s *server) scdJSON(r *http.Request, path string) map[string]any {
	st, raw, err := s.scd.call(r.Context(), http.MethodGet, path, nil)
	if err != nil || st != http.StatusOK {
		return map[string]any{}
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func (s *server) cloudStatus(w http.ResponseWriter, r *http.Request) {
	info := s.scdJSON(r, "/v1/cloud/info")
	lic := s.scdJSON(r, "/v1/license/status")
	obx := s.scdJSON(r, "/v1/admin/outbox/stats")

	// LIVE probe of the Central Control Plane (reachability + certificate).
	conn := map[string]any{"reachable": false, "cert_valid": false, "http_code": 0}
	if u, _ := info["cloud_api_url"].(string); u != "" {
		client := &http.Client{Timeout: 6 * time.Second}
		if resp, err := client.Get(u + "/healthz"); err == nil {
			resp.Body.Close()
			conn["reachable"] = true
			conn["http_code"] = resp.StatusCode
			conn["cert_valid"] = resp.TLS != nil // full verification via system trust; success => valid CA
		} else {
			conn["error"] = err.Error()
			// distinguish TLS/cert failure for the operator
			if _, ok := err.(*tls.CertificateVerificationError); ok {
				conn["cert_valid"] = false
			}
		}
	}
	// Derive an honest connection state from real facts (never a hardcoded "up").
	state := "disconnected"
	if conn["reachable"] == true {
		state = "connected"
	} else if ls, _ := lic["state"].(string); ls == "Active" {
		state = "offline_cached" // cloud unreachable but operating on cached license
	}
	conn["state"] = state

	writeJSON(w, http.StatusOK, map[string]any{
		"cloud":      info,
		"license":    lic,
		"outbox":     obx,
		"connection": conn,
	})
}

// cloudTest re-probes the cloud API and returns the connection block only.
func (s *server) cloudTest(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "cloud.test_connection", "cloud", "", nil)
	s.cloudStatus(w, r)
}

// setupStatus proxies the local enrollment-wizard status (read-only).
func (s *server) setupStatus(w http.ResponseWriter, r *http.Request) {
	s.scd.proxy(w, r, http.MethodGet, "/v1/setup/status", nil)
}

// setupEnroll forwards an Enrollment Token submission to scd (Hotel-IT gated +
// audited). Body: {token, serial}.
func (s *server) setupEnroll(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !enrollLim.allow(ip, time.Now()) {
		s.audit(r, "setup.enroll_rate_limited", "appliance", "", map[string]any{"ip": ip})
		jsonErr(w, http.StatusTooManyRequests, "rate_limited", "too many enrollment attempts — wait a minute and retry")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.audit(r, "setup.enroll_submitted", "appliance", "", map[string]any{"serial": body["serial"]})
	// Call scd and record the OUTCOME locally (accepted vs rejected + reason) so
	// every enrollment attempt is audited, not just the submission.
	st, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/setup/enroll", body)
	if err != nil {
		s.audit(r, "setup.enroll_failed", "appliance", "", map[string]any{"error": err.Error()})
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st >= 200 && st < 300 {
		s.audit(r, "setup.enroll_accepted", "appliance", "", map[string]any{"serial": body["serial"]})
	} else {
		s.audit(r, "setup.enroll_rejected", "appliance", "", map[string]any{"status": st, "detail": string(raw)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

// setupOfflineImport forwards a signed offline activation package to scd.
func (s *server) setupOfflineImport(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.audit(r, "setup.offline_import", "appliance", "", nil)
	s.scd.proxy(w, r, http.MethodPost, "/v1/setup/offline-import", body)
}

// cloudRefreshLicense / cloudRetryOutbox proxy the scd admin actions.
func (s *server) cloudRefreshLicense(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "cloud.refresh_license", "cloud", "", nil)
	s.scd.proxy(w, r, http.MethodPost, "/v1/license/refresh", nil)
}
