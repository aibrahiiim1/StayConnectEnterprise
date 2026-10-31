package main

// AFTER CHECK-OUT ACCESS: the one place the hotel configures what a guest keeps after a verified checkout (Product Owner,
// 2026-10-30; docs/architecture/ONEGATE_AFTER_CHECKOUT_ACCESS.md). It replaces the separate Checkout Grace and Post-Stay
// settings. The policy is the site's existing grace configuration (one authoritative, versioned record published through the
// audited boundary iam_v2.publish_checkout_grace_policy, with its immutable history), so there is no second duration,
// package or default. The enable switch is part of the same record (iam_v2.set_after_checkout_enabled, migration 0193).
//
// Publication changes what every departing guest receives, so it needs the operator's password (step-up), a bounded reason
// code and the version the operator was looking at. A change applies to FUTURE checkouts only: an entitlement already
// granted keeps the terms it was granted with (the snapshot).

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/grace"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

func (s *server) afterCheckoutAccessRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getAfterCheckoutAccess)
	r.Put("/", s.putAfterCheckoutAccess)
	r.Get("/history", s.listGraceHistory)
	return r
}

type afterCheckoutPolicy struct {
	DurationHours  float64 `json:"duration_hours"`
	DurationSecs   int     `json:"duration_seconds"`
	DownKbps       int     `json:"down_kbps"`
	UpKbps         int     `json:"up_kbps"`
	DataQuotaBytes int64   `json:"data_quota_bytes"`
	DeviceLimit    int     `json:"device_limit"`
	DevicePolicy   string  `json:"device_policy"`
}

func afterCheckoutBounds() map[string]any {
	return map[string]any{
		"duration_hours":   map[string]any{"min": grace.MinDurationHours, "max": grace.MaxDurationHours, "default": grace.DefaultDurationHours, "unit": "hours"},
		"down_kbps":        map[string]any{"min": grace.MinKbps, "max": grace.MaxKbps, "default": grace.DefaultDown, "unit": "kbps"},
		"up_kbps":          map[string]any{"min": grace.MinKbps, "max": grace.MaxKbps, "default": grace.DefaultUp, "unit": "kbps"},
		"data_quota_bytes": map[string]any{"min": grace.MinQuotaBytes, "max": grace.MaxQuotaBytes, "default": grace.DefaultQuotaBytes, "unit": "bytes"},
		"device_limit":     map[string]any{"min": grace.MinDeviceLimit, "max": grace.MaxDeviceLimit, "default": grace.DefaultDeviceLimit, "unit": "devices"},
	}
}

// getAfterCheckoutAccess reports the switch, the published policy and what a departing guest would actually receive today.
func (s *server) getAfterCheckoutAccess(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	enabled, version, pol, err := s.readAfterCheckout(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out := map[string]any{
		"enabled":                   enabled,
		"configured":                pol != nil,
		"config_version":            version,
		"policy":                    pol,
		"bounds":                    afterCheckoutBounds(),
		"supported_device_policies": supportedDeviceLimitPolicies,
		"emergency_history":         s.emergencyGraceHistory(ctx),
		"eligibility": "Only devices that were signed in and authorised on the same stay before check-out: devices online at " +
			"check-out, and devices authorised earlier on that stay that were offline. A new device, a device removed or revoked " +
			"on the stay, or a device authorised only on another stay is never eligible.",
		"sign_in": "A returning eligible device signs in with the room number and the reservation number only.",
	}
	switch {
	case !enabled:
		out["effective"] = map[string]any{"source": "DISABLED"}
	case pol != nil:
		out["effective"] = map[string]any{"source": "PUBLISHED", "policy": pol}
	default:
		e := emergencyEffective()
		out["effective"] = map[string]any{"source": "EMERGENCY_FALLBACK", "policy": e}
	}
	writeJSON(w, http.StatusOK, out)
}

// readAfterCheckout returns the switch, the config version and the published policy (nil when none is published).
func (s *server) readAfterCheckout(r *http.Request) (bool, int, *afterCheckoutPolicy, error) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var enabled bool
	var version int
	var dur, down, up, devLim *int
	var quota *int64
	var devPolicy *string
	err := s.db.QueryRow(ctx, `SELECT after_checkout_enabled, config_version, grace_duration_seconds, grace_down_kbps,
			grace_up_kbps, grace_data_quota_bytes, grace_device_limit, grace_device_limit_policy
		FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`, s.tenantID, s.siteID).
		Scan(&enabled, &version, &dur, &down, &up, &quota, &devLim, &devPolicy)
	if isNoRows(err) {
		return true, 0, nil, nil
	}
	if err != nil {
		return false, 0, nil, err
	}
	if dur == nil || down == nil || up == nil || quota == nil || devLim == nil || devPolicy == nil {
		return enabled, version, nil, nil
	}
	return enabled, version, &afterCheckoutPolicy{
		DurationHours: float64(*dur) / 3600, DurationSecs: *dur, DownKbps: *down, UpKbps: *up,
		DataQuotaBytes: *quota, DeviceLimit: *devLim, DevicePolicy: *devPolicy,
	}, nil
}

type afterCheckoutPutReq struct {
	Enabled               *bool   `json:"enabled"`
	DurationHours         *int    `json:"duration_hours"`
	DownKbps              *int    `json:"down_kbps"`
	UpKbps                *int    `json:"up_kbps"`
	DataQuotaBytes        *int64  `json:"data_quota_bytes"`
	DeviceLimit           *int    `json:"device_limit"`
	DevicePolicy          *string `json:"device_policy"`
	ExpectedConfigVersion *int    `json:"expected_config_version"`
	ReasonCode            string  `json:"reason_code"`
	Password              string  `json:"password"`
}

func (s *server) putAfterCheckoutAccess(w http.ResponseWriter, r *http.Request) {
	var in afterCheckoutPutReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if in.ExpectedConfigVersion == nil {
		jsonErr(w, http.StatusBadRequest, "expected_version_required",
			"expected_config_version is required so a concurrent change cannot be silently overwritten")
		return
	}
	reason := strings.TrimSpace(in.ReasonCode)
	if reason == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason code is required to change After Check-out Access")
		return
	}
	if !graceReasonCode.MatchString(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_invalid",
			"reason_code must be a bounded machine code matching ^[A-Z][A-Z0-9_]{0,63}$, not free text")
		return
	}
	anyPolicy := in.DurationHours != nil || in.DownKbps != nil || in.UpKbps != nil || in.DataQuotaBytes != nil ||
		in.DeviceLimit != nil || in.DevicePolicy != nil
	if in.Enabled == nil && !anyPolicy {
		jsonErr(w, http.StatusBadRequest, "nothing_to_change", "name the switch (enabled) and/or the policy fields to change")
		return
	}
	var pol grace.Policy
	if anyPolicy {
		if in.DurationHours == nil || in.DownKbps == nil || in.UpKbps == nil || in.DataQuotaBytes == nil || in.DeviceLimit == nil {
			jsonErr(w, http.StatusBadRequest, "validation",
				"the policy is published whole: duration_hours, down_kbps, up_kbps, data_quota_bytes and device_limit are all required")
			return
		}
		pol = grace.Policy{DurationSeconds: *in.DurationHours * 3600, DownKbps: *in.DownKbps, UpKbps: *in.UpKbps,
			DataQuotaBytes: *in.DataQuotaBytes, DeviceLimit: *in.DeviceLimit, DeviceLimitPolicy: grace.DeviceLimitRejectNew}
		if in.DevicePolicy != nil {
			pol.DeviceLimitPolicy = *in.DevicePolicy
		}
		if msg := grace.ValidateOperatorPolicy(pol); msg != "" {
			jsonErr(w, http.StatusBadRequest, "validation", msg)
			return
		}
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
		return
	}
	curEnabled, curVersion, curPol, err := s.readAfterCheckout(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if curVersion != *in.ExpectedConfigVersion {
		jsonErr(w, http.StatusConflict, "version_conflict", "the configuration changed since it was read; reload it")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	version := *in.ExpectedConfigVersion
	changed := map[string]any{}
	if anyPolicy {
		repo, ok := s.commerceRepo.(iamv2.GracePublishRepository)
		if !ok || repo == nil {
			jsonErr(w, http.StatusServiceUnavailable, "phase2_disabled",
				"the commercial catalog is not available, so the After Check-out package cannot be derived")
			return
		}
		v, _, perr := iamv2.PublishSystemGracePolicy(ctx, repo, iamv2.GracePublishRequest{
			TenantID: s.tenantID, SiteID: s.siteID,
			Policy: iamv2.SystemGracePolicy{
				DurationSeconds: pol.DurationSeconds, DownKbps: pol.DownKbps, UpKbps: pol.UpKbps,
				DataQuotaBytes: pol.DataQuotaBytes, DeviceLimit: pol.DeviceLimit, DeviceLimitPolicy: pol.DeviceLimitPolicy,
				EligibilityWindowSeconds: graceEligibilityWindowUnused,
			},
			ActorOperatorID: sess.OperatorID, ReasonCode: reason, ExpectedVersion: version,
		})
		if perr != nil {
			var de *iamv2.Error
			if errors.As(perr, &de) && de.Code == iamv2.ErrInvalidInput {
				jsonErr(w, http.StatusBadRequest, "validation", de.Msg)
				return
			}
			status, code := graceFailureStatus(perr.Error())
			jsonErr(w, status, code, "the After Check-out Access policy was refused")
			return
		}
		version = v
		changed["policy_published"] = curPol == nil || *curPol != afterCheckoutPolicy{
			DurationHours: float64(pol.DurationSeconds) / 3600, DurationSecs: pol.DurationSeconds, DownKbps: pol.DownKbps,
			UpKbps: pol.UpKbps, DataQuotaBytes: pol.DataQuotaBytes, DeviceLimit: pol.DeviceLimit, DevicePolicy: pol.DeviceLimitPolicy}
		changed["duration_hours"], changed["down_kbps"], changed["up_kbps"] = pol.DurationSeconds/3600, pol.DownKbps, pol.UpKbps
		changed["data_quota_bytes"], changed["device_limit"] = pol.DataQuotaBytes, pol.DeviceLimit
	}
	if in.Enabled != nil && *in.Enabled != curEnabled {
		var v int
		if err := s.db.QueryRow(ctx, `SELECT iam_v2.set_after_checkout_enabled($1::uuid,$2::uuid,$3,$4,$5::uuid,$6)`,
			s.tenantID, s.siteID, *in.Enabled, version, sess.OperatorID, reason).Scan(&v); err != nil {
			status, code := graceFailureStatus(err.Error())
			jsonErr(w, status, code, "the After Check-out Access switch was refused")
			return
		}
		version = v
		changed["enabled"] = *in.Enabled
	}
	// The audit row names the operator (from the session) and the terms; it holds no guest data.
	s.audit(r, "after_checkout_access.updated", "site_checkout_grace_config", s.siteID,
		map[string]any{"config_version": version, "reason_code": reason, "changed": changed})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}
