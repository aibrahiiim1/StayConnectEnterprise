package main

// Appliance WAN/LAN system network settings, proxied to netd (edged never touches netplan).
// RBAC key "network": read = view/history/diagnostics; write = change/apply/rollback, with apply and rollback requiring a password step-up.

import (
	"context"
	"encoding/json"
	"net/http"
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
	sess := sessFrom(r.Context())
	body := map[string]any{"Actor": sess.Email, "ActorID": sess.OperatorID, "SourceIP": clientIP(r)}
	s.audit(r, "network.system.confirm", "system_network", "", nil)
	// A confirmed change may have moved the management IP: trigger an idempotent Hotel Admin cert renewal, detached from the request so it outlives the response.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		_, _, _ = s.scd.call(ctx, http.MethodPost, "/v1/hotel-admin-cert/renew", map[string]any{})
	}()
	s.netd.proxy(w, r, http.MethodPost, "/v1/system-network/confirm", body)
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
