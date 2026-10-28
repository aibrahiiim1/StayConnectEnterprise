package main

// Appliance WAN/LAN system network settings, proxied to netd (edged never touches netplan).
// RBAC key "network": read = view/history/diagnostics; write = change/apply/rollback, with apply and rollback requiring a password step-up.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

func (s *server) sysNetGet(w http.ResponseWriter, r *http.Request) {
	s.netd.proxy(w, r, http.MethodGet, "/v1/system-network", nil)
}

func (s *server) sysNetValidate(w http.ResponseWriter, r *http.Request) {
	var proposal json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&proposal); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid proposal")
		return
	}
	s.netd.proxy(w, r, http.MethodPost, "/v1/system-network/validate", json.RawMessage(proposal))
}

func (s *server) sysNetApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Proposal json.RawMessage `json:"proposal"`
		Password string          `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Proposal) == 0 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "proposal required")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation failed")
		return
	}
	sess := sessFrom(r.Context())
	body := map[string]any{
		"proposal":  in.Proposal,
		"actor":     sess.Email,
		"actor_id":  sess.OperatorID,
		"source_ip": clientIP(r),
	}
	s.audit(r, "network.system.apply", "system_network", "", map[string]any{"source_ip": clientIP(r)})
	s.netd.proxy(w, r, http.MethodPost, "/v1/system-network/apply", body)
}

func (s *server) sysNetConfirm(w http.ResponseWriter, r *http.Request) {
	// The console sends the revision the operator saw, so netd can refuse to keep a different pending change
	// (audit P8 L14, edged half). Optional while netd's system-network state does not report it yet.
	var in struct {
		RevisionID string `json:"revision_id"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
			return
		}
	}
	if in.RevisionID != "" && !uuidRe.MatchString(in.RevisionID) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "revision_id must be the pending change's id")
		return
	}
	sess := sessFrom(r.Context())
	body := map[string]any{"Actor": sess.Email, "ActorID": sess.OperatorID, "SourceIP": clientIP(r)}
	if in.RevisionID != "" {
		// Both spellings: netd's request struct may name the field either way, and the other key is ignored.
		body["RevisionID"], body["revision_id"] = in.RevisionID, in.RevisionID
	}
	s.audit(r, "network.system.confirm", "system_network", in.RevisionID, nil)
	st, raw, err := s.netd.call(r.Context(), http.MethodPost, "/v1/system-network/confirm", body)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", err.Error())
		return
	}
	// A confirmed change may have moved the management IP: renew the Hotel Admin certificate, detached from the
	// request so it outlives the response. Only once netd has confirmed, and never two at once (audit P9 L6).
	if st >= 200 && st < 300 {
		s.renewHotelAdminCertOnce()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

// certRenewalRunning is set while a detached Hotel Admin certificate renewal runs.
var certRenewalRunning atomic.Bool

// renewHotelAdminCertOnce starts the idempotent renewal unless one is already running, and reports whether it did.
func (s *server) renewHotelAdminCertOnce() bool {
	if s.scd == nil || !certRenewalRunning.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer certRenewalRunning.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		_, _, _ = s.scd.call(ctx, http.MethodPost, "/v1/hotel-admin-cert/renew", map[string]any{})
	}()
	return true
}

func (s *server) sysNetRollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation failed")
		return
	}
	sess := sessFrom(r.Context())
	body := map[string]any{"Actor": sess.Email, "ActorID": sess.OperatorID, "SourceIP": clientIP(r)}
	s.audit(r, "network.system.rollback", "system_network", "", nil)
	s.netd.proxy(w, r, http.MethodPost, "/v1/system-network/rollback", body)
}

func (s *server) sysNetHistory(w http.ResponseWriter, r *http.Request) {
	s.netd.proxy(w, r, http.MethodGet, "/v1/system-network/history", nil)
}

func (s *server) sysNetDiagnostics(w http.ResponseWriter, r *http.Request) {
	s.netd.proxy(w, r, http.MethodGet, "/v1/system-network/diagnostics", nil)
}
