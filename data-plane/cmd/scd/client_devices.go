package main

// A client's devices on their package: list, release, or replace one (docs/architecture/ONEGATE_DEVICE_MANAGEMENT.md).
// Devices keep their slot after signing out, so moving one is an explicit, limited, audited act made only through
// iam_v2.client_device_action, which derives the package from proof the device holds, never from the request.
// The target is taken off the network first through enforcement; the package's window, data, time and owner are untouched.
// Refusals reveal only "too many changes today" versus "try again"; iam_v2.device_slot_actions records the exact outcome.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	endReasonDeviceReplaced = "DEVICE_REPLACED"
	endReasonDeviceReleased = "DEVICE_RELEASED"
)

type clientDeviceReq struct {
	IP            string `json:"ip"`
	MAC           string `json:"mac"`
	AuthContextID string `json:"auth_context_id,omitempty"`
	DeviceID      string `json:"device_id,omitempty"` // the TARGET of release / replace
}

type clientDeviceView struct {
	ID         string     `json:"id"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`
	Online     bool       `json:"online"`
	Removable  bool       `json:"removable"`
	ThisDevice bool       `json:"this_device"`
}

type clientDevicesOut struct {
	Outcome string `json:"outcome"`
	Limit   int    `json:"limit"`
	// Unlimited: the plan sets no device limit (Limit is then 0 and means nothing), so a device always joins.
	Unlimited     bool               `json:"unlimited,omitempty"`
	InUse         int                `json:"in_use"`
	ChangesLeft   int                `json:"changes_left"`
	Devices       []clientDeviceView `json:"devices"`
	EntitlementID string             `json:"entitlement_id,omitempty"`
	DeviceID      string             `json:"device_id,omitempty"`
}

// clientDeviceOf resolves the device asking; portald derives address and MAC from the connection, never from the client.
func (s *server) clientDeviceOf(ctx context.Context, req clientDeviceReq) (string, net.IP, bool) {
	ip := net.ParseIP(req.IP)
	mac, err := net.ParseMAC(req.MAC)
	if ip == nil || ip.To4() == nil || err != nil || s.db == nil || s.tenID == "" {
		return "", nil, false
	}
	var id string
	if err := s.db.QueryRow(ctx, `SELECT id::text FROM iam_v2.devices
	     WHERE tenant_id=$1 AND site_id=$2 AND mac=$3::macaddr ORDER BY last_seen DESC NULLS LAST LIMIT 1`,
		s.tenID, s.siteID, mac.String()).Scan(&id); err != nil {
		return "", nil, false
	}
	return id, ip, true
}

func (s *server) clientPackageOf(ctx context.Context, deviceID, authContextID string) (string, bool) {
	var ent string
	var err error
	if authContextID != "" {
		err = s.db.QueryRow(ctx, `
		    SELECT e.id::text
		      FROM iam_v2.auth_contexts a
		      JOIN iam_v2.entitlements e ON e.tenant_id = a.tenant_id AND e.site_id = a.site_id AND e.status = 'ACTIVE'
		       AND ((a.guest_account_id IS NOT NULL AND e.guest_account_id = a.guest_account_id)
		         OR (a.voucher_id IS NOT NULL AND e.voucher_id = a.voucher_id)
		         OR (a.stay_id IS NOT NULL AND e.stay_id = a.stay_id)
		         OR (a.guest_principal_id IS NOT NULL AND e.guest_principal_id = a.guest_principal_id)
		         OR (a.anonymous_subject_id IS NOT NULL AND e.anonymous_subject_id = a.anonymous_subject_id))
		     WHERE a.id = $1::uuid AND a.tenant_id = $2 AND a.site_id = $3 AND a.device_id = $4::uuid
		       AND a.expires_at > now()
		     LIMIT 1`, authContextID, s.tenID, s.siteID, deviceID).Scan(&ent)
	} else {
		err = s.db.QueryRow(ctx, `
		    SELECT s.entitlement_id::text FROM iam_v2.sessions s
		      JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		     WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.device_id=$3::uuid AND s.state='active' AND s.ended IS NULL
		       AND e.status='ACTIVE'
		     ORDER BY s.started DESC LIMIT 1`, s.tenID, s.siteID, deviceID).Scan(&ent)
	}
	return ent, err == nil
}

func (s *server) clientDevicesOn(ctx context.Context, ent, thisDevice string) (clientDevicesOut, error) {
	out := clientDevicesOut{Outcome: "LISTED", Devices: []clientDeviceView{}}
	var limit *int
	if err := s.db.QueryRow(ctx, `SELECT spr.max_concurrent_devices FROM iam_v2.entitlements e
	     JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id WHERE e.id = $1::uuid`,
		ent).Scan(&limit); err != nil {
		return out, err
	}
	if limit != nil {
		out.Limit = *limit
	} else {
		out.Unlimited = true
	}
	var allowed int
	if err := s.db.QueryRow(ctx, `SELECT client_device_changes_per_day FROM iam_v2.device_settings_get($1::uuid,$2::uuid)`,
		s.tenID, s.siteID).Scan(&allowed); err != nil {
		return out, err
	}
	rows, err := s.db.Query(ctx, `
	    SELECT ed.device_id::text, d.last_seen,
	           EXISTS (SELECT 1 FROM iam_v2.sessions x WHERE x.entitlement_id = ed.entitlement_id
	                    AND x.device_id = ed.device_id AND x.state IN ('active','PENDING_ENFORCEMENT') AND x.ended IS NULL)
	      FROM iam_v2.entitlement_devices ed
	      JOIN iam_v2.devices d ON d.id = ed.device_id
	     WHERE ed.entitlement_id = $1::uuid AND ed.tenant_id = $2 AND ed.site_id = $3 AND ed.status = 'AUTHORIZED'
	     ORDER BY ed.first_authorized NULLS LAST, ed.device_id`, ent, s.tenID, s.siteID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v clientDeviceView
		if err := rows.Scan(&v.ID, &v.LastSeen, &v.Online); err != nil {
			return out, err
		}
		v.ThisDevice = v.ID == thisDevice
		v.Removable = !v.ThisDevice
		out.Devices = append(out.Devices, v)
	}
	out.InUse = len(out.Devices)
	out.ChangesLeft = allowed
	return out, rows.Err()
}

// clientChangesLeft is what the database function will still allow today: the setting minus successful changes in the last 24 hours.
func (s *server) clientChangesLeft(ctx context.Context, ent string, allowed int) int {
	var used int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.device_slot_actions
	     WHERE entitlement_id = $1::uuid AND actor = 'CLIENT' AND outcome = 'OK'
	       AND acted_at > now() - interval '24 hours'`, ent).Scan(&used); err != nil {
		return allowed
	}
	if used >= allowed {
		return 0
	}
	return allowed - used
}

func (s *server) decodeClientDeviceReq(w http.ResponseWriter, r *http.Request) (clientDeviceReq, bool) {
	var req clientDeviceReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return req, false
	}
	req.AuthContextID = strings.TrimSpace(req.AuthContextID)
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	return req, true
}

func (s *server) clientDevicesList(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeClientDeviceReq(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	dev, _, ok := s.clientDeviceOf(ctx, req)
	if !ok {
		writeJSON(w, http.StatusOK, clientDevicesOut{Outcome: "UNAVAILABLE"})
		return
	}
	ent, ok := s.clientPackageOf(ctx, dev, req.AuthContextID)
	if !ok {
		writeJSON(w, http.StatusOK, clientDevicesOut{Outcome: "UNAVAILABLE"})
		return
	}
	out, err := s.clientDevicesOn(ctx, ent, dev)
	if err != nil {
		slog.Warn("client devices: list failed", "err", err)
		writeJSON(w, http.StatusOK, clientDevicesOut{Outcome: "UNAVAILABLE"})
		return
	}
	out.ChangesLeft = s.clientChangesLeft(ctx, ent, out.ChangesLeft)
	writeJSON(w, http.StatusOK, out)
}

func (s *server) clientDevicesRelease(w http.ResponseWriter, r *http.Request) {
	s.clientDeviceChange(w, r, "RELEASE")
}

func (s *server) clientDevicesReplace(w http.ResponseWriter, r *http.Request) {
	s.clientDeviceChange(w, r, "REPLACE")
}

func (s *server) clientDeviceChange(w http.ResponseWriter, r *http.Request, action string) {
	req, ok := s.decodeClientDeviceReq(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	dev, _, ok := s.clientDeviceOf(ctx, req)
	if !ok || req.DeviceID == "" || (action == "REPLACE" && req.AuthContextID == "") {
		writeJSON(w, http.StatusOK, clientDevicesOut{Outcome: "UNAVAILABLE"})
		return
	}
	// An unproven request still goes to the database function, which refuses and records it so probing leaves a trace; only a proven package has its target taken offline.
	ent, proven := s.clientPackageOf(ctx, dev, req.AuthContextID)
	reason := endReasonDeviceReleased
	if action == "REPLACE" {
		reason = endReasonDeviceReplaced
	}
	// Off the network first, but only when the change can go through: a change the database will refuse must leave the target
	// untouched, so pre-check here (the function re-checks under the package lock).
	if proven && req.DeviceID != dev && s.clientChangeWouldProceed(ctx, ent, action) {
		s.endTargetSessions(ctx, ent, req.DeviceID, reason)
	}
	var authCtx any
	if action == "REPLACE" {
		authCtx = req.AuthContextID
	}
	var outcome string
	var entOut *string
	err := s.db.QueryRow(ctx, `SELECT outcome, entitlement_id::text
	     FROM iam_v2.client_device_action($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::uuid)`,
		s.tenID, s.siteID, action, dev, authCtx, req.DeviceID).Scan(&outcome, &entOut)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("client devices: change failed", "action", action, "err", err)
		writeJSON(w, http.StatusOK, clientDevicesOut{Outcome: "UNAVAILABLE"})
		return
	}
	slog.Info("client devices: change", "action", action, "outcome", outcome)
	switch outcome {
	case "OK":
		out := clientDevicesOut{Outcome: "RELEASED"}
		if action == "REPLACE" {
			out.Outcome = "REPLACED"
			if entOut != nil {
				out.EntitlementID = *entOut
			}
			out.DeviceID = dev
		}
		writeJSON(w, http.StatusOK, out)
	case "REFUSED_THROTTLED":
		writeJSON(w, http.StatusOK, clientDevicesOut{Outcome: "LIMIT_TODAY"})
	case "REFUSED_LIMIT_NOT_REACHED":
		writeJSON(w, http.StatusOK, clientDevicesOut{Outcome: "SLOT_FREE", EntitlementID: ent, DeviceID: dev})
	default:
		writeJSON(w, http.StatusOK, clientDevicesOut{Outcome: "UNAVAILABLE"})
	}
}

// endTargetSessions ends the target device's live sessions on the package through enforcement.
func (s *server) endTargetSessions(ctx context.Context, ent, device, reason string) {
	rows, err := s.db.Query(ctx, `SELECT id::text, COALESCE(host(ip),'') FROM iam_v2.sessions
	     WHERE tenant_id=$1 AND site_id=$2 AND entitlement_id=$3::uuid AND device_id=$4::uuid
	       AND state IN ('active','PENDING_ENFORCEMENT') AND ended IS NULL`, s.tenID, s.siteID, ent, device)
	if err != nil {
		slog.Warn("client devices: could not read the target's sessions", "err", err)
		return
	}
	type live struct{ id, ip string }
	var all []live
	for rows.Next() {
		var l live
		if rows.Scan(&l.id, &l.ip) == nil {
			all = append(all, l)
		}
	}
	rows.Close()
	for _, l := range all {
		if err := s.endSessionNow(ctx, l.id, net.ParseIP(l.ip), reason); err != nil {
			slog.Warn("client devices: could not end the target's session", "err", err)
		}
	}
}

// clientChangeWouldProceed is the pre-check before a target is taken offline; any read failure answers false and the database function decides.
func (s *server) clientChangeWouldProceed(ctx context.Context, ent, action string) bool {
	var allowed int
	if err := s.db.QueryRow(ctx, `SELECT client_device_changes_per_day FROM iam_v2.device_settings_get($1::uuid,$2::uuid)`,
		s.tenID, s.siteID).Scan(&allowed); err != nil || s.clientChangesLeft(ctx, ent, allowed) <= 0 {
		return false
	}
	if action != "REPLACE" {
		return true
	}
	var limit *int
	var held int
	if err := s.db.QueryRow(ctx, `SELECT spr.max_concurrent_devices,
	     (SELECT count(*)::int FROM iam_v2.entitlement_devices x WHERE x.entitlement_id = e.id AND x.status = 'AUTHORIZED')
	     FROM iam_v2.entitlements e JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
	     WHERE e.id = $1::uuid`, ent).Scan(&limit, &held); err != nil || limit == nil {
		return false
	}
	return held >= *limit
}
