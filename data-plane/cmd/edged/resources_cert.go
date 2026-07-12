package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// Hotel Admin TLS certificate — status surface + manual controls.
//
// The renewal LIFECYCLE lives in the root-run manager script
// (/usr/local/sbin/stayconnect-hotel-admin-cert-manager), driven by a systemd
// timer. edged runs unprivileged (stayconnect) and is sandboxed with
// NoNewPrivileges, so it CANNOT run the privileged manager itself. It enforces
// Hotel-IT permission + password step-up here, then proxies the actual exec to
// scd (which runs as root) over the local socket. edged never handles private
// key material and never mints certs itself.
const (
	certStatusFile = "/etc/caddy/hotel-admin/status.json"
)

func readCertStatus() map[string]any {
	b, err := os.ReadFile(certStatusFile)
	if err != nil {
		return map[string]any{"available": false}
	}
	var st map[string]any
	if json.Unmarshal(b, &st) != nil {
		return map[string]any{"available": false}
	}
	st["available"] = true
	return st
}

// hotelAdminCertGet returns the last-known certificate status (subject, issuer,
// serial, fingerprint, SANs, dates, days remaining, thresholds, renewal history,
// current management IP, SAN-match). No private key or secret material.
func (s *server) hotelAdminCertGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, readCertStatus())
}

// hotelAdminCertCheck runs a diagnostic-only validation of the ACTIVE certificate
// (no rotation). The privileged work runs in scd (root); the UI re-fetches
// GET /hotel-admin-cert for the refreshed status afterwards.
func (s *server) hotelAdminCertCheck(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "hotel_admin_cert.check", "hotel_admin_cert", "", nil)
	s.scd.proxy(w, r, http.MethodPost, "/v1/hotel-admin-cert/check", map[string]any{})
}

// hotelAdminCertRotate forces a rotation through the SAME safe lifecycle as the
// automatic timer (mint → validate → atomic swap → caddy reload → dual-URL health
// → rollback on failure). Gated HERE by Hotel-IT permission (route), password
// step-up, a reason and a typed confirmation; the privileged mint/swap runs in
// scd. It cannot upload a key or bypass validation. The manager itself audits the
// outcome (renewal_succeeded/failed, rollback_*).
func (s *server) hotelAdminCertRotate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password     string `json:"password"`
		Reason       string `json:"reason"`
		Confirmation string `json:"confirmation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "reason is required")
		return
	}
	if in.Confirmation != "ROTATE" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "type ROTATE to confirm")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation failed")
		return
	}
	sess := sessFrom(r.Context())
	s.audit(r, "hotel_admin_cert.rotate_requested", "hotel_admin_cert", "",
		map[string]any{"reason": in.Reason, "actor": sess.Email})
	s.scd.proxy(w, r, http.MethodPost, "/v1/hotel-admin-cert/rotate", map[string]any{})
}
