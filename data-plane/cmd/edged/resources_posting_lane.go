package main

// The room-charge lane setting and the release of a parked charge (audit AD-41). Both are site_admin writes with
// password step-up, through definers that append their own record; edged writes no table.

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

const (
	laneParkMin = 1
	laneParkMax = 1000
	laneParkDef = 10
)

func (s *server) mountPostingLaneRoutes(r chi.Router) {
	r.Get("/lane-settings", s.getPostingLaneSettings)
	r.With(s.requireRole("pms-financial-onboarding", permWrite), requireSiteAdmin).Put("/lane-settings", s.putPostingLaneSettings)
	r.With(s.requireRole("pms-financial-onboarding", permWrite), requireSiteAdmin).Post("/parked/{outbox}/requeue", s.requeueParkedCharge)
}

func (s *server) getPostingLaneSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var n int
	var version int64
	var isDefault bool
	if err := s.db.QueryRow(ctx, `SELECT park_after_declines, config_version, is_default
		FROM iam_v2.posting_lane_settings_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID).Scan(&n, &version, &isDefault); err != nil {
		slog.Error("posting lane: setting unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the setting could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"park_after_declines": n, "config_version": version, "is_default": isDefault,
		"bounds":  map[string]int{"min": laneParkMin, "max": laneParkMax},
		"default": laneParkDef,
		"unit":    "refusals",
		"explanation": "How many times the same room charge may be refused before the system stops retrying it and holds it for " +
			"an operator. Charges behind it then continue. A change affects the next refusal; a charge already waiting keeps the " +
			"count it has.",
	})
}

func (s *server) putPostingLaneSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion *int64 `json:"expected_config_version"`
		ParkAfter       *int   `json:"park_after_declines"`
		Reason          string `json:"reason"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if in.ExpectedVersion == nil || in.ParkAfter == nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "expected_config_version and park_after_declines are required")
		return
	}
	if *in.ParkAfter < laneParkMin || *in.ParkAfter > laneParkMax {
		jsonErr(w, http.StatusBadRequest, "out_of_bounds", "the number of refusals must be between 1 and 1000")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if l := len(reason); l < 3 || l > 500 {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a reason of 3 to 500 characters is required")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	operator := operatorLabel(r)
	if operator == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.posting_lane_settings_set($1::uuid,$2::uuid,$3,$4,$5,$6)`,
		s.tenantID, s.siteID, *in.ExpectedVersion, *in.ParkAfter, operator, reason).Scan(&version); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "LANE_SETTING_VERSION_CONFLICT"):
			jsonErr(w, http.StatusConflict, "version_conflict", "the setting changed since you opened it; reload and try again")
		case strings.Contains(msg, "LANE_SETTING_BOUNDS"), strings.Contains(msg, "LANE_SETTING_REASON"):
			jsonErr(w, http.StatusBadRequest, "settings_rejected", boundedDBMessage(msg))
		default:
			slog.Error("posting lane: setting write failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the setting could not be saved")
		}
		return
	}
	s.audit(r, "posting_lane.setting_changed", "posting_lane_settings", s.siteID,
		map[string]any{"park_after_declines": *in.ParkAfter, "config_version": version, "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}

func (s *server) requeueParkedCharge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "outbox")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "the charge id is not valid")
		return
	}
	var in struct {
		Reason   string `json:"reason"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if l := len(reason); l < 3 || l > 500 {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a reason of 3 to 500 characters is required")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	operator := operatorLabel(r)
	if operator == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var state string
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.posting_outbox_requeue_parked($1::uuid,$2::uuid,$3::uuid,$4,$5)`,
		s.tenantID, s.siteID, id, operator, reason).Scan(&state); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "LANE_REQUEUE_UNKNOWN"):
			jsonErr(w, http.StatusNotFound, "not_found", "no such charge in this site")
		case strings.Contains(msg, "LANE_REQUEUE_NOT_PARKED"):
			jsonErr(w, http.StatusConflict, "not_parked", "this charge was not held by repeated refusals")
		case strings.Contains(msg, "LANE_REQUEUE_MAY_HAVE_SENT"):
			jsonErr(w, http.StatusConflict, "may_have_sent", "an attempt on this charge may have reached the PMS; it needs the review decision")
		case strings.Contains(msg, "LANE_REQUEUE_REASON"), strings.Contains(msg, "LANE_REQUEUE_ACTOR"):
			jsonErr(w, http.StatusBadRequest, "bad_request", boundedDBMessage(msg))
		default:
			slog.Error("posting lane: requeue failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the charge could not be released")
		}
		return
	}
	s.audit(r, "posting_lane.parked_charge_released", "posting_outbox", id, map[string]any{"reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"state": state})
}
