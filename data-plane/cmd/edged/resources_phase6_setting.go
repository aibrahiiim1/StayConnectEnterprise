package main

// PHASE-6 OPERATOR SURFACE — the per-appliance Guest Device Self-Service setting.
//
// THE ENTIRE SECURITY OF THIS SURFACE IS ABOUT WHERE THE IDENTITIES COME FROM, so it is worth being explicit
// about all four:
//
//   tenant / site   : s.tenantID / s.siteID — this process's own scope, from the signed local assignment.
//   appliance       : s.applianceID() — this appliance's own identity, read from the signed identity file
//                     the assignment agent maintains. edged already resolves it this way for telemetry, and
//                     reusing that source rather than adding a second one is the point: two sources of the
//                     same identity are two things that can disagree.
//   operator id     : sessFrom(ctx).OperatorID — the authenticated session, never a request field.
//   operator label  : the session's display name, likewise.
//
// NONE of them is a request parameter, and none of them CAN be: the request type below has exactly two
// fields, `enabled` and `reason`, and the decoder refuses unknown fields. A caller cannot supply a tenant, a
// site, an appliance or an actor because there is nowhere to put one — which is a stronger guarantee than
// validating them, because there is no validation to get wrong.
//
// The write itself goes through iam_v2.p6_set_guest_device_self_service, which performs the setting change
// and its audit as ONE operation. edged's database role holds no direct write on either table, so the audit
// is mandatory by privilege rather than by this handler remembering to write it.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/deviceselfservice"
)

type guestDeviceSettingReq struct {
	// Enabled is the desired value. Required, and a pointer so "absent" is distinguishable from "false" --
	// a PUT that forgot the field must not read as a request to turn the feature off.
	Enabled *bool `json:"enabled"`
	// Reason is a human note for the audit. It is the operator's words about WHY, never about WHO or WHERE.
	Reason string `json:"reason"`
}

type guestDeviceSettingResp struct {
	Enabled bool `json:"enabled"`
	// Changed reports whether this request actually moved the value, so a screen can distinguish "saved" from
	// "already was".
	Changed bool `json:"changed,omitempty"`
	// PhaseGateEnabled says whether the Phase-6 DEPLOYMENT gate is on in this build. It is surfaced so the
	// admin screen can tell the truth: turning the product setting ON does not, by itself, make the guest
	// feature reachable while the deployment gate is off. Conflating the two would let an operator switch
	// something on, see it confirmed, and believe guests can use it.
	PhaseGateEnabled bool `json:"phase_gate_enabled"`
}

// getGuestDeviceSetting serves GET /phase6/settings/guest-device-self-service.
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

// setGuestDeviceSetting serves PUT /phase6/settings/guest-device-self-service.
func (s *server) setGuestDeviceSetting(w http.ResponseWriter, r *http.Request) {
	var in guestDeviceSettingReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		// An unknown field is a REFUSAL, not something to ignore. A request carrying tenant_id, appliance_id
		// or operator_id must fail loudly rather than have it silently dropped: silently ignoring an identity
		// field is indistinguishable, from the caller's side, from honouring it.
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

	// THE ACTOR IS THE SESSION'S. Not a field, not a header, not a claim the caller makes about themselves.
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
		// The audit column refuses a blank actor, and failing here gives a better message than letting the
		// database refuse it — but the database refusing it is what makes this a guarantee rather than a
		// convention.
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "the operator session carries no identity")
		return
	}
	if s.applianceID() == "" {
		// Without a signed local identity there is no appliance to scope the setting to, and inventing one
		// would create managed state for an appliance that does not exist.
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

	// edged's own operator audit, in addition to the durable setting audit the database wrote. They answer
	// different questions -- "what did this operator do in the admin UI" and "how did this setting reach its
	// current value" -- and neither is a substitute for the other.
	s.audit(r, "phase6.guest_device_self_service", "appliance", s.applianceID(), map[string]any{
		"enabled": *in.Enabled, "previous": before, "reason": reason,
	})
	writeJSON(w, http.StatusOK, guestDeviceSettingResp{
		Enabled: *in.Enabled, Changed: before != *in.Enabled, PhaseGateEnabled: s.phase6.DeviceGuestOn()})
}
