package main

// Tenant, site, appliance and operator come only from the signed assignment and the session, never the request;
// the decoder refuses unknown fields. The write and its audit are one DB function, so the audit cannot be skipped.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/deviceselfservice"
)

type guestDeviceSettingReq struct {
	// A pointer so "absent" is distinguishable from "false": a PUT that omitted it must not turn the feature off.
	Enabled *bool  `json:"enabled"`
	Reason  string `json:"reason"`
}

type guestDeviceSettingResp struct {
	Enabled bool `json:"enabled"`
	Changed bool `json:"changed,omitempty"`
	// The deployment gate can be off while the setting is on; both are shown so the UI never implies guests can use it.
	PhaseGateEnabled bool `json:"phase_gate_enabled"`
}

func (s *server) getGuestDeviceSetting(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	svc := deviceselfservice.New(s.db)
	on, err := svc.EnabledForAppliance(ctx, s.tenantID, s.siteID, s.applianceID())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, guestDeviceSettingResp{
		Enabled: on, PhaseGateEnabled: s.phase6.DeviceGuestOn()})
}

func (s *server) setGuestDeviceSetting(w http.ResponseWriter, r *http.Request) {
	var in guestDeviceSettingReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		// Unknown fields are refused: silently dropping a tenant_id/operator_id is indistinguishable from honouring it.
		jsonErr(w, http.StatusBadRequest, "invalid",
			"body must contain only 'enabled' and an optional 'reason'")
		return
	}
	if in.Enabled == nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "'enabled' is required")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if len(reason) > 500 {
		jsonErr(w, http.StatusBadRequest, "invalid", "reason is too long")
		return
	}

	// The actor is the session's, never a request field.
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "no operator session")
		return
	}
	label := strings.TrimSpace(sess.DisplayName)
	if label == "" {
		label = strings.TrimSpace(sess.Email)
	}
	if label == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "the operator session carries no identity")
		return
	}
	if s.applianceID() == "" {
		// No signed local identity means no appliance to scope to; do not invent one.
		jsonErr(w, http.StatusConflict, "no_appliance_identity",
			"this appliance has no signed identity yet; the setting is scoped to an enrolled appliance")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	svc := deviceselfservice.New(s.db)
	before, err := svc.EnabledForAppliance(ctx, s.tenantID, s.siteID, s.applianceID())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if err := svc.SetForAppliance(ctx, s.tenantID, s.siteID, s.applianceID(), *in.Enabled,
		sess.OperatorID, label, reason); err != nil {
		jsonErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}

	// Operator audit is separate from the DB's durable setting audit; they answer different questions.
	s.audit(r, "phase6.guest_device_self_service", "appliance", s.applianceID(), map[string]any{
		"enabled": *in.Enabled, "previous": before, "reason": reason,
	})
	writeJSON(w, http.StatusOK, guestDeviceSettingResp{
		Enabled: *in.Enabled, Changed: before != *in.Enabled, PhaseGateEnabled: s.phase6.DeviceGuestOn()})
}

// Mounted via mountResource, so the role matrix applies (write: hotel_it_manager, site_admin; none for voucher_operator).
func (s *server) guestDeviceSelfServiceRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getGuestDeviceSetting)
	r.Put("/", s.setGuestDeviceSetting)
	return r
}
