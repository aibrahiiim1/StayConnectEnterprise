package main

// HOW LONG A DISABLED STAFF ACCOUNT IS KEPT (migration 0138; GDPR audit 2026-10-19, B4).
//
// GET/PUT /edge/v1/operators/account-retention. An audited appliance setting (docs/OPERATIONAL_SETTINGS_POLICY.md):
// default 730 days, 30 to 3650, changed with a reason and the operator's password by whoever may change
// operators. scd applies it hourly (public.run_staff_retention).

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type accountRetention struct {
	DisabledAccountDays int        `json:"disabled_account_days"`
	ConfigVersion       int64      `json:"config_version"`
	UpdatedAt           *time.Time `json:"updated_at,omitempty"`
	UpdatedBy           *string    `json:"updated_by,omitempty"`
}

// accountRetentionBound mirrors the CHECK in migration 0138 exactly; the API sends it so the form cannot drift.
var accountRetentionBound = signinBound{"disabled_account_days", "Keep disabled accounts for", "days", 30, 3650, 730,
	"A disabled staff account is anonymised this long after it was disabled: its email, name, password and authenticator are removed, and the activity log then shows it as a removed account."}

func (s *server) getAccountRetention(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var p accountRetention
	if err := s.db.QueryRow(ctx, `SELECT disabled_account_days, config_version, updated_at, updated_by
		FROM public.operator_account_retention_get()`).Scan(&p.DisabledAccountDays, &p.ConfigVersion, &p.UpdatedAt, &p.UpdatedBy); err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the disabled-account period could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": p, "bounds": []signinBound{accountRetentionBound}})
}

func (s *server) setAccountRetention(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisabledAccountDays int    `json:"disabled_account_days"`
		Reason              string `json:"reason"`
		Password            string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	b := accountRetentionBound
	if in.DisabledAccountDays < b.Min || in.DisabledAccountDays > b.Max {
		jsonErr(w, http.StatusBadRequest, "invalid_setting",
			b.Label+" must be between "+itoa(b.Min)+" and "+itoa(b.Max)+" "+b.Unit+".")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT public.operator_account_retention_set($1,$2,$3)`,
		in.DisabledAccountDays, operatorLabel(r), reason).Scan(&version); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "22023") {
			jsonErr(w, http.StatusBadRequest, "invalid_setting", "the value is outside its allowed range")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "update_failed", "the disabled-account period could not be saved")
		return
	}
	s.audit(r, "operator.account_retention_changed", "operator_account_retention", "", map[string]any{
		"disabled_account_days": in.DisabledAccountDays, "config_version": version, "reason": reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}
