package main

// The late-capture re-query window (audit AD-43): how many days back a card charge recorded as failed or unknown is still asked
// about. A per-site setting with a default, a bound, step-up and an audited versioned writer. The re-query itself is a separate
// deployer switch and is off by default; this setting only says how far back it looks once it is on.

import (
	"log/slog"
	"net/http"
	"strings"
)

const (
	cardRecheckMin = 1
	cardRecheckMax = 30
	cardRecheckDef = 7
)

func (s *server) getCardRecheckSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var days int
	var version int64
	var isDefault bool
	if err := s.db.QueryRow(ctx, `SELECT window_days, config_version, is_default FROM iam_v2.card_recheck_settings_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID).Scan(&days, &version, &isDefault); err != nil {
		slog.Error("card recheck: setting unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the setting could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"window_days": days, "config_version": version, "is_default": isDefault,
		"bounds": map[string]int{"min": cardRecheckMin, "max": cardRecheckMax}, "default": cardRecheckDef, "unit": "days",
		"explanation": "How many days back a card payment that OneGate recorded as failed or unknown is still checked with the " +
			"provider for a payment that went through later. A payment found this way is never granted automatically: it opens a " +
			"case for finance. Changing this affects the next check only.",
	})
}

func (s *server) putCardRecheckSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion *int64 `json:"expected_config_version"`
		WindowDays      *int   `json:"window_days"`
		Reason          string `json:"reason"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || in.ExpectedVersion == nil || in.WindowDays == nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "expected_config_version and window_days are required")
		return
	}
	if *in.WindowDays < cardRecheckMin || *in.WindowDays > cardRecheckMax {
		jsonErr(w, http.StatusBadRequest, "out_of_bounds", "the window must be between 1 and 30 days")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if l := len(reason); l < 3 || l > 500 {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a reason of 3 to 500 characters is required")
		return
	}
	actor, ok := s.stepUp(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.card_recheck_settings_set($1::uuid,$2::uuid,$3,$4,$5,$6)`,
		s.tenantID, s.siteID, *in.ExpectedVersion, *in.WindowDays, actor, reason).Scan(&version); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "RECHECK_SETTING_VERSION_CONFLICT"):
			jsonErr(w, http.StatusConflict, "version_conflict", "the setting changed since you opened it; reload and try again")
		case strings.Contains(msg, "RECHECK_SETTING_BOUNDS"), strings.Contains(msg, "RECHECK_SETTING_REASON"):
			jsonErr(w, http.StatusBadRequest, "settings_rejected", boundedDBMessage(msg))
		default:
			slog.Error("card recheck: setting write failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the setting could not be saved")
		}
		return
	}
	s.audit(r, "card_recheck_settings.update", "card_recheck_settings", s.siteID,
		map[string]any{"window_days": *in.WindowDays, "config_version": version, "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}
