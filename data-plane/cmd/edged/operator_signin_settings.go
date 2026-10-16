package main

// ADMIN CONSOLE SIGN-IN PROTECTION, AS A SETTING (docs/OPERATIONAL_SETTINGS_POLICY.md; migration 0119).
//
// How many wrong passwords per account and per source address, within how long, and how long the lockout
// lasts. Read by everyone who can read Operators; changed by whoever can change Operators (site_admin), with a
// reason and the password step-up, and every change is a row in public.operator_signin_protection_changes
// written by the same definer function that changes the value. The throttle reads the same function on every
// attempt, so a saved change applies to the next attempt with no restart.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type signinProtection struct {
	AccountMaxFailures   int        `json:"account_max_failures"`
	AccountWindowSeconds int        `json:"account_window_seconds"`
	IPMaxFailures        int        `json:"ip_max_failures"`
	IPWindowSeconds      int        `json:"ip_window_seconds"`
	LockoutSeconds       int        `json:"lockout_seconds"`
	ConfigVersion        int64      `json:"config_version"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
	UpdatedBy            *string    `json:"updated_by,omitempty"`
}

type signinBound struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Unit     string `json:"unit"`
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	Default  int    `json:"default"`
	Explains string `json:"explains"`
}

// signinBounds mirror the CHECKs in migration 0119 exactly; the API sends them so the form cannot drift.
var signinBounds = []signinBound{
	{"account_max_failures", "Wrong passwords before an account is locked", "attempts", 3, 20, 5,
		"Counted per account within the window below. From the second wrong password, each one doubles the wait before the next try (1 s, 2 s, 4 s ...)."},
	{"account_window_seconds", "Counting window per account", "seconds", 60, 86400, 900,
		"How long wrong passwords for one account keep counting. A correct password clears the count."},
	{"ip_max_failures", "Wrong passwords from one address before it is locked", "attempts", 5, 500, 20,
		"Counted across every account tried from the same address, so one computer cannot guess at many accounts."},
	{"ip_window_seconds", "Counting window per address", "seconds", 60, 86400, 900,
		"How long wrong passwords from one address keep counting. A correct password does not clear it."},
	{"lockout_seconds", "Lockout", "seconds", 60, 86400, 900,
		"How long a locked account or address must wait. Resetting an operator's password ends that account's lockout."},
}

func (p signinProtection) value(key string) int {
	switch key {
	case "account_max_failures":
		return p.AccountMaxFailures
	case "account_window_seconds":
		return p.AccountWindowSeconds
	case "ip_max_failures":
		return p.IPMaxFailures
	case "ip_window_seconds":
		return p.IPWindowSeconds
	case "lockout_seconds":
		return p.LockoutSeconds
	}
	return 0
}

// validateSigninProtection reports the first value outside its bounds, in the operator's words.
func validateSigninProtection(p signinProtection) (string, bool) {
	for _, b := range signinBounds {
		if v := p.value(b.Key); v < b.Min || v > b.Max {
			return b.Label + " must be between " + itoa(b.Min) + " and " + itoa(b.Max) + " " + b.Unit + ".", false
		}
	}
	return "", true
}

func (s *server) readSigninProtection(ctx context.Context) (signinProtection, error) {
	var p signinProtection
	err := s.db.QueryRow(ctx, `SELECT account_max_failures, account_window_seconds, ip_max_failures, ip_window_seconds,
		       lockout_seconds, config_version, updated_at, updated_by FROM public.operator_signin_protection_get()`).
		Scan(&p.AccountMaxFailures, &p.AccountWindowSeconds, &p.IPMaxFailures, &p.IPWindowSeconds,
			&p.LockoutSeconds, &p.ConfigVersion, &p.UpdatedAt, &p.UpdatedBy)
	return p, err
}

func (s *server) getSigninProtection(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	p, err := s.readSigninProtection(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the sign-in protection could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": p, "bounds": signinBounds})
}

func (s *server) setSigninProtection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		signinProtection
		Reason   string `json:"reason"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if msg, ok := validateSigninProtection(in.signinProtection); !ok {
		jsonErr(w, http.StatusBadRequest, "invalid_setting", msg)
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	// Loosening this is how an attacker who got one session would make the rest of the guessing cheap.
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	label := sess.Email
	if label == "" {
		label = sess.OperatorID
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	p := in.signinProtection
	if err := s.db.QueryRow(ctx, `SELECT public.operator_signin_protection_set($1,$2,$3,$4,$5,$6,$7)`,
		p.AccountMaxFailures, p.AccountWindowSeconds, p.IPMaxFailures, p.IPWindowSeconds, p.LockoutSeconds,
		label, reason).Scan(&version); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "22023") {
			jsonErr(w, http.StatusBadRequest, "invalid_setting", "a value is outside its allowed range")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "update_failed", "the sign-in protection could not be saved")
		return
	}
	s.audit(r, "operator.signin_protection_changed", "operator_signin_protection", "", map[string]any{
		"account_max_failures": p.AccountMaxFailures, "account_window_seconds": p.AccountWindowSeconds,
		"ip_max_failures": p.IPMaxFailures, "ip_window_seconds": p.IPWindowSeconds,
		"lockout_seconds": p.LockoutSeconds, "config_version": version, "reason": reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"config_version": version,
		"notice": "This applies to the next sign-in attempt. An account or address already locked keeps the " +
			"lockout it was given; resetting an operator's password ends that account's lockout.",
	})
}

type signinProtectionChange struct {
	ChangedBy string    `json:"changed_by"`
	Reason    string    `json:"reason"`
	OldValues any       `json:"old_values"`
	NewValues any       `json:"new_values"`
	Version   int64     `json:"config_version"`
	ChangedAt time.Time `json:"changed_at"`
}

func (s *server) listSigninProtectionChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT changed_by, change_reason, old_values, new_values, new_config_version, changed_at
		  FROM public.operator_signin_protection_changes ORDER BY changed_at DESC LIMIT 50`)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the change history could not be read")
		return
	}
	defer rows.Close()
	var out []signinProtectionChange
	for rows.Next() {
		var c signinProtectionChange
		if err := rows.Scan(&c.ChangedBy, &c.Reason, &c.OldValues, &c.NewValues, &c.Version, &c.ChangedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "query_failed", "the change history could not be read")
			return
		}
		out = append(out, c)
	}
	writeList(w, out)
}
