package main

// THE NETWORK PAUSE — the operator surface (migration 0136; GDPR audit 2026-10-19, A4).
//
// Many wrong room sign-ins on one guest network pause room sign-in there. Staff see the pause and may:
//
//   - END it early, when it is a false alarm (a tour group mistyping, a PMS change mid-evening);
//   - CONFIRM a genuine guest's device, picked from the devices that tried on that network: that device may try
//     again at once, and its own device restriction ends.
//
// Both are under guest-signin-restrictions, the desk's release permission: they decide who may ATTEMPT, never
// who is let in. Neither creates an entitlement or a session; the guest still signs in with their own details.
// Both need a reason and are audited. The three numbers are under guest-signin-protection, with the password,
// like the per-device policy.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

type networkPolicyOut struct {
	MaxFailures   int  `json:"network_max_failures"`
	WindowSeconds int  `json:"network_window_seconds"`
	PauseSeconds  int  `json:"network_pause_seconds"`
	IsDefault     bool `json:"is_default"`
	Limits        struct {
		MinFailures      int `json:"min_network_max_failures"`
		MaxFailures      int `json:"max_network_max_failures"`
		MinWindowSeconds int `json:"min_network_window_seconds"`
		MaxWindowSeconds int `json:"max_network_window_seconds"`
		MinPauseSeconds  int `json:"min_network_pause_seconds"`
		MaxPauseSeconds  int `json:"max_network_pause_seconds"`
	} `json:"limits"`
}

func (s *server) networkPolicy(ctx context.Context) (networkPolicyOut, error) {
	var out networkPolicyOut
	err := s.db.QueryRow(ctx, `SELECT network_max_failures, network_window_seconds, network_pause_seconds, is_default
		FROM iam_v2.guest_signin_network_policy_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID).
		Scan(&out.MaxFailures, &out.WindowSeconds, &out.PauseSeconds, &out.IsDefault)
	out.Limits.MinFailures, out.Limits.MaxFailures = signinattempt.MinNetworkFailures, signinattempt.MaxNetworkFailures
	out.Limits.MinWindowSeconds, out.Limits.MaxWindowSeconds = signinattempt.MinNetworkWindowSeconds, signinattempt.MaxNetworkWindowSeconds
	out.Limits.MinPauseSeconds, out.Limits.MaxPauseSeconds = signinattempt.MinNetworkPauseSeconds, signinattempt.MaxNetworkPauseSeconds
	return out, err
}

// GET /guest-signin-protection/network
func (s *server) getNetworkPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	out, err := s.networkPolicy(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "policy_unreadable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /guest-signin-protection/network {network_max_failures, network_window_seconds, network_pause_seconds,
// reason, password}
func (s *server) putNetworkPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MaxFailures   int    `json:"network_max_failures"`
		WindowSeconds int    `json:"network_window_seconds"`
		PauseSeconds  int    `json:"network_pause_seconds"`
		Reason        string `json:"reason"`
		Password      string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	p := signinattempt.NetworkPolicy{MaxFailures: in.MaxFailures, WindowSeconds: in.WindowSeconds, PauseSeconds: in.PauseSeconds}
	if err := p.Validate(); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid_policy", err.Error())
		return
	}
	reason, ok := reasonText(in.Reason)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "reason_required", reasonBoundsMessage)
		return
	}
	if _, ok := s.stepUpActor(w, r, in.Password); !ok {
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	before, err := s.networkPolicy(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "policy_unreadable", err.Error())
		return
	}
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.guest_signin_network_policy_set($1::uuid,$2::uuid,$3,$4,$5,$6,$7)`,
		s.tenantID, s.siteID, p.MaxFailures, p.WindowSeconds, p.PauseSeconds, actor, reason).Scan(&version); err != nil {
		jsonErr(w, http.StatusInternalServerError, "policy_not_saved", err.Error())
		return
	}
	s.audit(r, "guest_signin_network_policy.update", "site", s.siteID, map[string]any{
		"previous": map[string]any{"network_max_failures": before.MaxFailures,
			"network_window_seconds": before.WindowSeconds, "network_pause_seconds": before.PauseSeconds,
			"was_default": before.IsDefault},
		"new": map[string]any{"network_max_failures": p.MaxFailures,
			"network_window_seconds": p.WindowSeconds, "network_pause_seconds": p.PauseSeconds},
		"config_version": version, "reason": reason,
	})
	out, err := s.networkPolicy(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "policy_unreadable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type networkPauseOut struct {
	ID               string    `json:"id"`
	GuestNetwork     string    `json:"guest_network"`
	StartedAt        time.Time `json:"started_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	RemainingSeconds int       `json:"remaining_seconds"`
	FailureCount     int       `json:"failure_count"`
	ConfirmedDevices int       `json:"confirmed_devices"`
}

// GET /guest-signin-restrictions/network-pauses: the pauses running now.
func (s *server) listNetworkPauses(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, COALESCE(p.guest_network_name, ''), p.started_at, p.expires_at,
		       GREATEST(0, CEIL(EXTRACT(EPOCH FROM (p.expires_at - now()))))::int, p.failure_count,
		       (SELECT count(*) FROM iam_v2.guest_signin_device_passes d WHERE d.pause_id = p.id)::int
		  FROM iam_v2.guest_signin_network_pauses p
		 WHERE p.tenant_id = $1 AND p.site_id = $2 AND p.ended_at IS NULL AND p.expires_at > now()
		 ORDER BY p.started_at DESC`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "pauses_unreadable", err.Error())
		return
	}
	defer rows.Close()
	out := []networkPauseOut{}
	for rows.Next() {
		var p networkPauseOut
		if err := rows.Scan(&p.ID, &p.GuestNetwork, &p.StartedAt, &p.ExpiresAt, &p.RemainingSeconds,
			&p.FailureCount, &p.ConfirmedDevices); err != nil {
			jsonErr(w, http.StatusInternalServerError, "pauses_unreadable", err.Error())
			return
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"pauses": out})
}

// GET /guest-signin-restrictions/network-pauses/{id}/devices: the devices that tried room sign-in on that
// network around the pause, so staff can pick the guest in front of them. The room is what was typed,
// unverified; nothing else of what was typed is returned.
func (s *server) listNetworkPauseDevices(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT device_mac, COALESCE(last_submitted_room, ''), last_attempt_at, attempts, confirmed
		FROM iam_v2.guest_signin_network_pause_devices($1::uuid,$2::uuid,$3::uuid)`, s.tenantID, s.siteID, id)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "unknown pause")
		return
	}
	defer rows.Close()
	type device struct {
		DeviceMAC         string    `json:"device_mac"`
		LastSubmittedRoom string    `json:"last_submitted_room_unverified"`
		LastAttemptAt     time.Time `json:"last_attempt_at"`
		Attempts          int       `json:"attempts"`
		Confirmed         bool      `json:"confirmed"`
	}
	out := []device{}
	for rows.Next() {
		var d device
		if err := rows.Scan(&d.DeviceMAC, &d.LastSubmittedRoom, &d.LastAttemptAt, &d.Attempts, &d.Confirmed); err != nil {
			jsonErr(w, http.StatusInternalServerError, "devices_unreadable", err.Error())
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// pauseActionInput is the body of both staff actions: a reason, and for a confirmation the device.
type pauseActionInput struct {
	Reason    string `json:"reason"`
	DeviceMAC string `json:"device_mac"`
}

func (s *server) pauseAction(w http.ResponseWriter, r *http.Request) (id, reason, actor string, in pauseActionInput, ok bool) {
	id = strings.TrimSpace(chi.URLParam(r, "id"))
	if err := decodeJSON(r, &in); err != nil || id == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "pause id and a body are required")
		return
	}
	reason, ok = reasonText(in.Reason)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "reason_required", reasonBoundsMessage)
		return
	}
	actor = protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the action could not be attributed to an operator")
		return "", "", "", in, false
	}
	return id, reason, actor, in, true
}

// pauseNetwork names the paused network for the audit record, or reports that there is no running pause.
func (s *server) pauseNetwork(ctx context.Context, id string) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT COALESCE(guest_network_name,'') FROM iam_v2.guest_signin_network_pauses
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid AND ended_at IS NULL AND expires_at > now()`,
		s.tenantID, s.siteID, id).Scan(&name)
	return name, err
}

// POST /guest-signin-restrictions/network-pauses/{id}/end {reason}
func (s *server) endNetworkPause(w http.ResponseWriter, r *http.Request) {
	id, reason, actor, _, ok := s.pauseAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	network, err := s.pauseNetwork(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no running pause with that id")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "unknown pause")
		return
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.guest_signin_end_network_pause($1::uuid,$2::uuid,$3::uuid,$4,$5)`,
		s.tenantID, s.siteID, id, actor, reason).Scan(&n); err != nil {
		jsonErr(w, http.StatusInternalServerError, "end_failed", err.Error())
		return
	}
	if n == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "no running pause with that id")
		return
	}
	s.audit(r, "guest_signin_network_pause.end", "guest_network_pause", id, map[string]any{
		"guest_network": network, "reason": reason, "grants_access": false})
	writeJSON(w, http.StatusOK, map[string]any{"ended": true,
		"note": "Room sign-in works again on this network. Nobody has been signed in."})
}

// POST /guest-signin-restrictions/network-pauses/{id}/confirm {device_mac, reason}
func (s *server) confirmPausedDevice(w http.ResponseWriter, r *http.Request) {
	id, reason, actor, in, ok := s.pauseAction(w, r)
	if !ok {
		return
	}
	mac, err := net.ParseMAC(strings.TrimSpace(in.DeviceMAC))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "device_mac must be a device address from the list")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	network, err := s.pauseNetwork(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no running pause with that id")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "unknown pause")
		return
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.guest_signin_confirm_device($1::uuid,$2::uuid,$3::uuid,$4::macaddr,$5,$6)`,
		s.tenantID, s.siteID, id, mac.String(), actor, reason).Scan(&n); err != nil {
		jsonErr(w, http.StatusInternalServerError, "confirm_failed", err.Error())
		return
	}
	if n == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "no running pause with that id")
		return
	}
	s.audit(r, "guest_signin_network_pause.confirm_device", "guest_device", mac.String(), map[string]any{
		"pause_id": id, "guest_network": network, "device_mac": mac.String(), "reason": reason,
		"grants_access": false})
	writeJSON(w, http.StatusOK, map[string]any{"confirmed": true,
		"note": "This device may try room sign-in again now. It has not been signed in."})
}
