package main

// STAFF HELP FOR CLIENTS WHO ASKED FOR A CODE (Product Owner, 2026-10-12; contract §6.7).
//
// Two resource keys, deliberately not one (auth.go):
//
//	code-requests  GET  /code-requests              recent code requests with each device's temporary-internet
//	                                                window and limits
//	               POST /code-requests/clear        let ONE device try again: its ended windows stop counting
//	                                                towards the limits and the wait. Nothing is deleted and no
//	                                                access is granted; the client still has to sign in.
//	code-reveal    POST /code-reveal/{id}           read a LIVE code to the client it was sent to
//
// Both actions take the operator's password (step-up) and a reason, and are audited here. The code itself is
// never written to the audit record, the log or any other response. scd owns the data and the sealed copy;
// edged never holds the key. Scope is the appliance's own tenant and site, bound inside scd.

import (
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
)

var codeChallengeRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (s *server) codeRequestsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listCodeRequests)
	r.Post("/clear", s.clearCodeRequestDevice)
	return r
}

func (s *server) codeRevealRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{id}", s.revealCode)
	return r
}

func (s *server) relayScd(w http.ResponseWriter, st int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

func (s *server) listCodeRequests(w http.ResponseWriter, r *http.Request) {
	path := "/v1/admin/temporary-access"
	if h := r.URL.Query().Get("hours"); h != "" {
		path += "?hours=" + url.QueryEscape(h)
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodGet, path, nil)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the sign-in service could not be reached")
		return
	}
	s.relayScd(w, st, raw)
}

func (s *server) clearCodeRequestDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceMAC string `json:"device_mac"`
		Reason    string `json:"reason"`
		Password  string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	hw, err := net.ParseMAC(strings.TrimSpace(in.DeviceMAC))
	reason := strings.TrimSpace(in.Reason)
	if err != nil || reason == "" || len(reason) > 500 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "a device and a reason are required")
		return
	}
	actor, ok := s.paymentStepUp(w, r, in.Password)
	if !ok {
		return
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/admin/temporary-access/clear",
		map[string]any{"device_mac": hw.String(), "operator": actor, "reason": reason})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the sign-in service could not be reached")
		return
	}
	if st < 300 {
		s.audit(r, "temporary_access.cleared", "guest_device", hw.String(), map[string]any{
			"reason": reason,
			// Stated in the record, because "cleared" can read like "let in".
			"effect": "ended temporary-internet windows no longer count towards the limits; no access granted",
		})
	}
	s.relayScd(w, st, raw)
}

func (s *server) revealCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason   string `json:"reason"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || len(reason) > 500 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "a reason is required")
		return
	}
	id := chi.URLParam(r, "id")
	if !codeChallengeRE.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad id")
		return
	}
	actor, ok := s.paymentStepUp(w, r, in.Password)
	if !ok {
		return
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/admin/otp/"+id+"/reveal", map[string]any{"operator": actor})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the sign-in service could not be reached")
		return
	}
	if st < 300 {
		// The record names the challenge and the reason; never the code.
		s.audit(r, "otp_code.revealed", "otp_challenge", id, map[string]any{"reason": reason})
	}
	s.relayScd(w, st, raw)
}
