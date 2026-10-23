package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// Hotel Admin TLS certificate status and manual controls. Renewal lives in the root-run manager
// (/usr/local/sbin/stayconnect-hotel-admin-cert-manager); edged is sandboxed (NoNewPrivileges) and never handles
// key material, so it enforces permission and step-up here and proxies the privileged exec to scd.
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

func (s *server) hotelAdminCertGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, readCertStatus())
}

// hotelAdminCertCheck validates the active certificate without rotating; the privileged work runs in scd.
func (s *server) hotelAdminCertCheck(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "hotel_admin_cert.check", "hotel_admin_cert", "", nil)
	s.scd.proxy(w, r, http.MethodPost, "/v1/hotel-admin-cert/check", map[string]any{})
}

// hotelAdminCertRotate forces a rotation through the same lifecycle as the timer (mint, validate, atomic swap,
// reload, health, rollback on failure). Gated by Hotel-IT permission, step-up, a reason and a typed
// confirmation; it cannot upload a key. The manager audits the outcome.
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
		s.reauthRefused(w, r, "password confirmation failed")
		return
	}
	sess := sessFrom(r.Context())
	s.audit(r, "hotel_admin_cert.rotate_requested", "hotel_admin_cert", "",
		map[string]any{"reason": in.Reason, "actor": sess.Email})
	s.scd.proxy(w, r, http.MethodPost, "/v1/hotel-admin-cert/rotate", map[string]any{})
}
