package main

// Email accounts: the self-service settings (verification deadline, expiry action, resend interval, grace hour, accounts per device per day).
// Bounds are held here AND by the SQL setter; changes go through iam_v2.self_service_settings_set, the only writer, which logs operator and reason.
// Governed by the auth-methods resource key: whoever may switch the method on may set its terms.
// A save takes the operator's password and a bounded reason: it changes every self-service client's access at once.
// scd reads iam_v2.self_service_settings_get on every decision and so does this file, so the screen cannot show a policy the appliance is not running.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	suspensionSuspend  = "SUSPEND"
	suspensionWarnOnly = "WARN_ONLY"
)

const selfServiceChangeNotice = "Saved. A new verification deadline applies to accounts registered from now on; " +
	"accounts already registered keep the deadline they were given. Switching verification off, or choosing " +
	"Warn only, applies to every account at once."

type selfServiceSettings struct {
	VerificationRequired         bool   `json:"verification_required"`
	VerificationDeadlineHours    int    `json:"verification_deadline_hours"`
	SuspensionPolicy             string `json:"suspension_policy"`
	ReminderHoursBefore          int    `json:"reminder_hours_before"`
	ResendCooldownSeconds        int    `json:"resend_cooldown_seconds"`
	ResendMaxPerDay              int    `json:"resend_max_per_day"`
	GraceMinutes                 int    `json:"grace_minutes"`
	MaxGraceExtensions           int    `json:"max_grace_extensions"`
	RegistrationsPerDevicePerDay int    `json:"registrations_per_device_per_day"`
}

// defaultSelfServiceSettings are the contract's defaults: what a site with no row runs.
var defaultSelfServiceSettings = selfServiceSettings{
	VerificationRequired:         true,
	VerificationDeadlineHours:    24,
	SuspensionPolicy:             suspensionSuspend,
	ReminderHoursBefore:          4,
	ResendCooldownSeconds:        60,
	ResendMaxPerDay:              5,
	GraceMinutes:                 60,
	MaxGraceExtensions:           1,
	RegistrationsPerDevicePerDay: 3,
}

// selfServiceBound is one numeric setting's range, unit and default. Sent to the UI, so the browser never
// carries its own copy of a number the server enforces.
type selfServiceBound struct {
	Key     string `json:"key"`
	Min     int    `json:"min"`
	Max     int    `json:"max"`
	Unit    string `json:"unit"`
	Default int    `json:"default"`
}

// selfServiceBounds, in the order the screen shows them.
var selfServiceBounds = []selfServiceBound{
	{"verification_deadline_hours", 1, 168, "hours", 24},
	{"reminder_hours_before", 0, 72, "hours", 4},
	{"resend_cooldown_seconds", 30, 3600, "seconds", 60},
	{"resend_max_per_day", 1, 20, "emails per day", 5},
	{"grace_minutes", 15, 240, "minutes", 60},
	{"max_grace_extensions", 0, 3, "times", 1},
	{"registrations_per_device_per_day", 1, 10, "accounts per device per day", 3},
}

func (v selfServiceSettings) numeric(key string) int {
	switch key {
	case "verification_deadline_hours":
		return v.VerificationDeadlineHours
	case "reminder_hours_before":
		return v.ReminderHoursBefore
	case "resend_cooldown_seconds":
		return v.ResendCooldownSeconds
	case "resend_max_per_day":
		return v.ResendMaxPerDay
	case "grace_minutes":
		return v.GraceMinutes
	case "max_grace_extensions":
		return v.MaxGraceExtensions
	case "registrations_per_device_per_day":
		return v.RegistrationsPerDevicePerDay
	}
	return 0
}

// validateSelfServiceSettings mirrors iam_v2.self_service_settings_set and the table CHECKs. It returns the
// offending key and an operator-readable message, or ok.
func validateSelfServiceSettings(v selfServiceSettings) (string, string, bool) {
	if v.SuspensionPolicy != suspensionSuspend && v.SuspensionPolicy != suspensionWarnOnly {
		return "suspension_policy", "suspension_policy must be SUSPEND or WARN_ONLY", false
	}
	for _, b := range selfServiceBounds {
		if n := v.numeric(b.Key); n < b.Min || n > b.Max {
			return b.Key, fmt.Sprintf("%s must be a whole number from %d to %d (%s)", b.Key, b.Min, b.Max, b.Unit), false
		}
	}
	// A reminder that would be sent before the account even exists is no reminder. 0 means "no reminder".
	if v.ReminderHoursBefore >= v.VerificationDeadlineHours {
		return "reminder_hours_before", "reminder_hours_before must be less than verification_deadline_hours " +
			"(use 0 for no reminder)", false
	}
	return "", "", true
}

// selfServiceSettingsReq is the PUT body. Every setting is a POINTER, so a field the caller forgot is refused
// rather than read as zero (a forgotten verification_required must not switch verification off).
type selfServiceSettingsReq struct {
	VerificationRequired         *bool   `json:"verification_required"`
	VerificationDeadlineHours    *int    `json:"verification_deadline_hours"`
	SuspensionPolicy             *string `json:"suspension_policy"`
	ReminderHoursBefore          *int    `json:"reminder_hours_before"`
	ResendCooldownSeconds        *int    `json:"resend_cooldown_seconds"`
	ResendMaxPerDay              *int    `json:"resend_max_per_day"`
	GraceMinutes                 *int    `json:"grace_minutes"`
	MaxGraceExtensions           *int    `json:"max_grace_extensions"`
	RegistrationsPerDevicePerDay *int    `json:"registrations_per_device_per_day"`
	Reason                       string  `json:"reason"`
	Password                     string  `json:"password"`
}

func (in selfServiceSettingsReq) settings() (selfServiceSettings, string) {
	var v selfServiceSettings
	switch {
	case in.VerificationRequired == nil:
		return v, "verification_required"
	case in.VerificationDeadlineHours == nil:
		return v, "verification_deadline_hours"
	case in.SuspensionPolicy == nil:
		return v, "suspension_policy"
	case in.ReminderHoursBefore == nil:
		return v, "reminder_hours_before"
	case in.ResendCooldownSeconds == nil:
		return v, "resend_cooldown_seconds"
	case in.ResendMaxPerDay == nil:
		return v, "resend_max_per_day"
	case in.GraceMinutes == nil:
		return v, "grace_minutes"
	case in.MaxGraceExtensions == nil:
		return v, "max_grace_extensions"
	case in.RegistrationsPerDevicePerDay == nil:
		return v, "registrations_per_device_per_day"
	}
	v = selfServiceSettings{
		VerificationRequired:         *in.VerificationRequired,
		VerificationDeadlineHours:    *in.VerificationDeadlineHours,
		SuspensionPolicy:             strings.TrimSpace(*in.SuspensionPolicy),
		ReminderHoursBefore:          *in.ReminderHoursBefore,
		ResendCooldownSeconds:        *in.ResendCooldownSeconds,
		ResendMaxPerDay:              *in.ResendMaxPerDay,
		GraceMinutes:                 *in.GraceMinutes,
		MaxGraceExtensions:           *in.MaxGraceExtensions,
		RegistrationsPerDevicePerDay: *in.RegistrationsPerDevicePerDay,
	}
	return v, ""
}

type storedSelfServiceSettings struct {
	selfServiceSettings
	Version   int64      `json:"config_version"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	UpdatedBy *string    `json:"updated_by,omitempty"`
}

// errSelfServiceUnavailable: the database has no self-service functions (migration 0117 not applied).
var errSelfServiceUnavailable = errors.New("self-service settings are not available on this appliance")

func isUndefinedObject(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "42883" || pgErr.Code == "42P01" || pgErr.Code == "42703")
}

func isSettingRefusal(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "22023" || pgErr.Code == "P0001")
}

// selfServiceSettingsFor reads through the definer function; a site with no row reads the defaults (config_version 0).
// A database error is returned, never papered over. The row is read as JSON so a column added later is ignored.
func (s *server) selfServiceSettingsFor(ctx context.Context) (storedSelfServiceSettings, error) {
	out := storedSelfServiceSettings{selfServiceSettings: defaultSelfServiceSettings}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT to_jsonb(g) FROM iam_v2.self_service_settings_get($1::uuid, $2::uuid) g`,
		s.tenantID, s.siteID).Scan(&raw)
	if err != nil {
		if isUndefinedObject(err) {
			return out, errSelfServiceUnavailable
		}
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (s *server) selfServiceSettingsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getSelfServiceSettings)
	r.Put("/", s.setSelfServiceSettings)
	return r
}

func selfServiceLimits() map[string]any {
	return map[string]any{
		"bounds":              selfServiceBounds,
		"suspension_policies": []string{suspensionSuspend, suspensionWarnOnly},
	}
}

func (s *server) getSelfServiceSettings(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		// No site yet: nothing can be saved, but the screen can still show the defaults and the bounds.
		writeJSON(w, http.StatusOK, map[string]any{
			"settings": defaultSelfServiceSettings, "defaults": defaultSelfServiceSettings, "config_version": 0,
			"limits": selfServiceLimits(), "changes": []any{}, "awaiting_assignment": true,
		})
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	cur, err := s.selfServiceSettingsFor(ctx)
	if errors.Is(err, errSelfServiceUnavailable) {
		jsonErr(w, http.StatusServiceUnavailable, "not_available",
			"this appliance's database does not have the Email accounts settings yet")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the Email accounts settings could not be read")
		return
	}
	changes, err := s.selfServiceSettingChanges(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the Email accounts settings history could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": cur.selfServiceSettings, "defaults": defaultSelfServiceSettings,
		"config_version": cur.Version, "updated_at": cur.UpdatedAt, "updated_by": cur.UpdatedBy,
		"limits": selfServiceLimits(), "changes": changes,
	})
}

// selfServiceSettingChanges is the newest 50 rows of the append-only change log, as the database holds them
// (each row carries who, when, why and the old and new values), less the scope columns the caller already knows.
func (s *server) selfServiceSettingChanges(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := s.db.Query(ctx, `
	    SELECT to_jsonb(c) - 'tenant_id' - 'site_id'
	      FROM iam_v2.self_service_settings_changes c
	     WHERE c.tenant_id = $1 AND c.site_id = $2
	     ORDER BY c.changed_at DESC LIMIT 50`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}

func (s *server) setSelfServiceSettings(w http.ResponseWriter, r *http.Request) {
	var in selfServiceSettingsReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	next, missing := in.settings()
	if missing != "" {
		jsonErr(w, http.StatusBadRequest, "invalid_setting", missing+" is required")
		return
	}
	if _, msg, ok := validateSelfServiceSettings(next); !ok {
		jsonErr(w, http.StatusBadRequest, "invalid_setting", msg)
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet, so nothing can be saved")
		return
	}
	actor, ok := s.portalStepUp(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// The previous values go into the audit record with the new ones (settings policy: old and new).
	prev, err := s.selfServiceSettingsFor(ctx)
	if errors.Is(err, errSelfServiceUnavailable) {
		jsonErr(w, http.StatusServiceUnavailable, "not_available",
			"this appliance's database does not have the Email accounts settings yet")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the Email accounts settings could not be read")
		return
	}
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.self_service_settings_set($1::uuid, $2::uuid,
	        $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		s.tenantID, s.siteID, next.VerificationRequired, next.VerificationDeadlineHours, next.SuspensionPolicy,
		next.ReminderHoursBefore, next.ResendCooldownSeconds, next.ResendMaxPerDay, next.GraceMinutes,
		next.MaxGraceExtensions, next.RegistrationsPerDevicePerDay, actor, reason).Scan(&version); err != nil {
		switch {
		case isUndefinedObject(err):
			jsonErr(w, http.StatusServiceUnavailable, "not_available",
				"this appliance's database does not have the Email accounts settings yet")
		case isSettingRefusal(err):
			jsonErr(w, http.StatusBadRequest, "invalid_setting", "the appliance refused these values")
		default:
			jsonErr(w, http.StatusInternalServerError, "update_failed", "the Email accounts settings could not be saved")
		}
		return
	}
	s.audit(r, "self_service_settings.changed", "self_service_settings", s.siteID, map[string]any{
		"old": prev.selfServiceSettings, "new": next, "config_version": version, "reason": reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": next, "config_version": version,
		"notice": selfServiceChangeNotice,
	})
}
