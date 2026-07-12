package main

// Hotel Admin — appliance WAN/LAN (system) network settings. These proxy to the
// privileged netd daemon (edged never touches netplan itself). RBAC: the
// "network" resource key gates read (network.view/history/diagnostics) vs write
// (network.change/apply/rollback). Apply and rollback additionally require the
// operator to re-enter their password (Part 7 re-authentication).

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// reauth re-verifies the logged-in operator's password before a high-risk change.
func (s *server) reauth(r *http.Request, password string) bool {
	sess := sessFrom(r.Context())
	if sess == nil || password == "" {
		return false
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var hash, status string
	if err := s.db.QueryRow(ctx,
		`SELECT COALESCE(password_hash,''), status FROM operators WHERE id=$1`, sess.OperatorID).
		Scan(&hash, &status); err != nil {
		return false
	}
	return status == "active" && hash != "" && verifyPassword(password, hash)
}

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
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation failed")
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
	// A confirmed system-network change may have moved the management IP. Trigger an
	// idempotent Hotel Admin cert renewal (no-op if the IP/SAN are unchanged) so the
	// certificate's IP SAN tracks the new management IP. Detached from the request so
	// it outlives the response; the manager handles validation/rollback safely.
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
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation failed")
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
