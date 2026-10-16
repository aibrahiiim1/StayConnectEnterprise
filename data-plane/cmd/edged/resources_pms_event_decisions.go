package main

// DECISIONS ON PMS MESSAGES THAT WAIT FOR REVIEW (audit H-15, Product-Owner decision D-D).
//
// A PMS message the engine could not apply sits in MANUAL_REVIEW. Until now nothing could answer one: the
// dashboard and the PMS activity page said thousands "need a decision" that no screen could take. This surface
// is that decision, one message at a time:
//
//	POST /{eventID}/acknowledge  the message needs no action -- with a reason.
//	POST /{eventID}/link         the message concerns this stay -- chosen from GET /{eventID}/candidates, the
//	                             stays it is re-matchable to (same connection; same reservation, or for a
//	                             message without one, a stay that was in that room when it arrived).
//	GET/PUT /settings            the automatic expiry setting: OFF by default; when on, a message that arrived
//	                             after it was switched on and waited the configured days is recorded as expired.
//
// Every write needs the password step-up, writes ONE row through a definer (iam_v2.pms_review_decide /
// pms_review_expiry_settings_set, migration 0123) into an append-only ledger, and is recorded in audit_log with
// the actor. Neither action changes a stay, an entitlement or the message: a checkout is only ever an APPLIED
// GO event. Who may decide: site_admin and the IT manager (resource key pms-event-decisions).

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *server) pmsEventDecisionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/settings", s.getReviewExpirySettings)
	r.Put("/settings", s.putReviewExpirySettings)
	r.Get("/{eventID}/candidates", s.listReviewLinkCandidates)
	r.Post("/{eventID}/acknowledge", s.decideReviewEvent("ACKNOWLEDGE"))
	r.Post("/{eventID}/link", s.decideReviewEvent("LINK"))
	return r
}

type reviewCandidate struct {
	StayID    string     `json:"stay_id"`
	MatchedBy string     `json:"matched_by"`
	Room      *string    `json:"room,omitempty"`
	Status    string     `json:"status"`
	Arrival   *time.Time `json:"arrival,omitempty"`
	Departure *time.Time `json:"departure,omitempty"`
}

func (s *server) listReviewLinkCandidates(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "eventID")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "the message id is not valid")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT stay_id::text, matched_by, room, status, arrival, departure
		FROM iam_v2.pms_review_link_candidates($1::uuid,$2::uuid,$3::uuid)`, s.tenantID, s.siteID, id)
	if err != nil {
		slog.Error("pms review: candidates unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the matching stays could not be read")
		return
	}
	defer rows.Close()
	out := []reviewCandidate{}
	for rows.Next() {
		var c reviewCandidate
		if err := rows.Scan(&c.StayID, &c.MatchedBy, &c.Room, &c.Status, &c.Arrival, &c.Departure); err != nil {
			slog.Error("pms review: candidates unreadable", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the matching stays could not be read")
			return
		}
		out = append(out, c)
	}
	writeList(w, out)
}

type reviewDecisionReq struct {
	StayID   string `json:"stay_id"`
	Reason   string `json:"reason"`
	Password string `json:"password"`
}

func (s *server) decideReviewEvent(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "eventID")
		if !uuidRe.MatchString(id) {
			jsonErr(w, http.StatusBadRequest, "bad_request", "the message id is not valid")
			return
		}
		var in reviewDecisionReq
		if err := decodeJSON(r, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
			return
		}
		reason := strings.TrimSpace(in.Reason)
		if l := len(reason); l < 3 || l > 500 {
			jsonErr(w, http.StatusBadRequest, "reason_required", "a reason of 3 to 500 characters is required")
			return
		}
		var stayArg any
		if action == "LINK" {
			if !uuidRe.MatchString(strings.TrimSpace(in.StayID)) {
				jsonErr(w, http.StatusBadRequest, "stay_required", "choose the stay this message is about")
				return
			}
			stayArg = strings.TrimSpace(in.StayID)
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
		var resID string
		err := s.db.QueryRow(ctx, `SELECT iam_v2.pms_review_decide($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7)::text`,
			s.tenantID, s.siteID, id, action, stayArg, operator, reason).Scan(&resID)
		if err != nil {
			status, code := reviewDecisionFailure(err.Error())
			if status == http.StatusInternalServerError {
				slog.Error("pms review: decision failed", "err", err)
				jsonErr(w, status, code, "the decision could not be recorded")
				return
			}
			jsonErr(w, status, code, boundedDBMessage(err.Error()))
			return
		}
		payload := map[string]any{"decision": action, "reason": reason, "resolution_id": resID}
		if action == "LINK" {
			payload["stay_id"] = stayArg
		}
		s.audit(r, "pms_event."+strings.ToLower(action), "stay_events", id, payload)
		writeJSON(w, http.StatusOK, map[string]any{"stay_event_id": id, "decision": action, "resolution_id": resID})
	}
}

func reviewDecisionFailure(msg string) (int, string) {
	switch {
	case strings.Contains(msg, "REVIEW_NOT_FOUND"):
		return http.StatusNotFound, "not_found"
	case strings.Contains(msg, "REVIEW_ALREADY_DECIDED"):
		return http.StatusConflict, "already_decided"
	case strings.Contains(msg, "REVIEW_NOT_WAITING"):
		return http.StatusConflict, "not_waiting"
	case strings.Contains(msg, "REVIEW_NOT_MATCHABLE"):
		return http.StatusUnprocessableEntity, "not_matchable"
	case strings.Contains(msg, "REVIEW_REASON"), strings.Contains(msg, "REVIEW_STAY_REQUIRED"),
		strings.Contains(msg, "REVIEW_ACTION"):
		return http.StatusBadRequest, "bad_request"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// ---- the expiry setting -------------------------------------------------------------------------------------

const (
	reviewExpiryDaysMin = 1
	reviewExpiryDaysMax = 365
)

func (s *server) getReviewExpirySettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var enabled, isDefault bool
	var days int
	var from *time.Time
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT enabled, expire_after_days, applies_from, config_version, is_default
		FROM iam_v2.pms_review_expiry_settings_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID).
		Scan(&enabled, &days, &from, &version, &isDefault); err != nil {
		slog.Error("pms review: expiry setting unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the expiry setting could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": enabled, "expire_after_days": days, "applies_from": from,
		"config_version": version, "is_default": isDefault,
		"bounds":  map[string]int{"min_days": reviewExpiryDaysMin, "max_days": reviewExpiryDaysMax},
		"default": map[string]any{"enabled": false, "expire_after_days": 30},
		"unit":    "days",
	})
}

func (s *server) putReviewExpirySettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion *int64 `json:"expected_config_version"`
		Enabled         *bool  `json:"enabled"`
		Days            *int   `json:"expire_after_days"`
		Reason          string `json:"reason"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if in.ExpectedVersion == nil || in.Enabled == nil || in.Days == nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "expected_config_version, enabled and expire_after_days are required")
		return
	}
	if *in.Days < reviewExpiryDaysMin || *in.Days > reviewExpiryDaysMax {
		jsonErr(w, http.StatusBadRequest, "out_of_bounds", "the waiting period must be between 1 and 365 days")
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
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.pms_review_expiry_settings_set($1::uuid,$2::uuid,$3,$4,$5,$6,$7)`,
		s.tenantID, s.siteID, *in.ExpectedVersion, *in.Enabled, *in.Days, operator, reason).Scan(&version); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "REVIEW_EXPIRY_VERSION_CONFLICT"):
			jsonErr(w, http.StatusConflict, "version_conflict", "the setting changed since you opened it; reload and try again")
		case strings.Contains(msg, "REVIEW_EXPIRY_BOUNDS"), strings.Contains(msg, "REVIEW_EXPIRY_REASON"):
			jsonErr(w, http.StatusBadRequest, "settings_rejected", boundedDBMessage(msg))
		default:
			slog.Error("pms review: expiry setting write failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the expiry setting could not be saved")
		}
		return
	}
	s.audit(r, "pms_event.expiry_setting_changed", "pms_review_expiry_settings", s.siteID,
		map[string]any{"enabled": *in.Enabled, "expire_after_days": *in.Days, "config_version": version, "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}
