package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// Central security settings (one row): the absolute console session lifetime (however active the operator is),
// the idle limit, and the sign-in lockout.

type securitySettingsView struct {
	OperatorSessionMaxHours int        `json:"operator_session_max_hours"`
	Default                 int        `json:"default"`
	Min                     int        `json:"min"`
	Max                     int        `json:"max"`
	Unit                    string     `json:"unit"`
	UpdatedAt               *time.Time `json:"updated_at"`
	UpdatedBy               *string    `json:"updated_by"`
	// A session with no activity for this long ends.
	OperatorIdleMinutes int           `json:"operator_idle_minutes"`
	Idle                settingBounds `json:"idle_bounds"`
	// This many wrong passwords or authenticator codes within the lock time lock the account for the lock time.
	SigninLockFailures int           `json:"signin_lock_failures"`
	LockFailures       settingBounds `json:"signin_lock_failures_bounds"`
	SigninLockMinutes  int           `json:"signin_lock_minutes"`
	LockMinutes        settingBounds `json:"signin_lock_minutes_bounds"`
}

type settingBounds struct {
	Default int    `json:"default"`
	Min     int    `json:"min"`
	Max     int    `json:"max"`
	Unit    string `json:"unit"`
}

func (b *Base) loadSecuritySettings(r *http.Request) (securitySettingsView, error) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	v := securitySettingsView{Default: auth.DefaultSessionMaxHours, Min: auth.MinSessionMaxHours,
		Max: auth.MaxSessionMaxHours, Unit: "hours",
		Idle: settingBounds{Default: auth.DefaultIdleMinutes, Min: auth.MinIdleMinutes, Max: auth.MaxIdleMinutes, Unit: "minutes"},
		LockFailures: settingBounds{Default: auth.DefaultSigninLockFailures, Min: auth.MinSigninLockFailures,
			Max: auth.MaxSigninLockFailures, Unit: "wrong attempts"},
		LockMinutes: settingBounds{Default: auth.DefaultSigninLockMinutes, Min: auth.MinSigninLockMinutes,
			Max: auth.MaxSigninLockMinutes, Unit: "minutes"}}
	var by string
	var at time.Time
	err := b.DB.QueryRow(ctx, `SELECT operator_session_max_hours, operator_idle_minutes, signin_lock_failures,
                                        signin_lock_minutes, updated_at, COALESCE(updated_by,'')
                                 FROM central_security_settings WHERE id`).Scan(&v.OperatorSessionMaxHours, &v.OperatorIdleMinutes,
		&v.SigninLockFailures, &v.SigninLockMinutes, &at, &by)
	if err != nil {
		return v, err
	}
	v.UpdatedAt, v.UpdatedBy = &at, strPtr(by)
	return v, nil
}

// getSecuritySettings: GET /cloud/v1/settings/security
func (b *Base) getSecuritySettings(w http.ResponseWriter, r *http.Request) {
	v, err := b.loadSecuritySettings(r)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its security settings.")
		return
	}
	WriteJSON(w, http.StatusOK, v)
}

// patchSecuritySettings: PATCH /cloud/v1/settings/security; omitted values keep their setting.
//
// New limits apply at each session's next request, including open ones: a shorter value ends sessions now too old or idle.
func (b *Base) patchSecuritySettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OperatorSessionMaxHours *int   `json:"operator_session_max_hours"`
		OperatorIdleMinutes     *int   `json:"operator_idle_minutes"`
		SigninLockFailures      *int   `json:"signin_lock_failures"`
		SigninLockMinutes       *int   `json:"signin_lock_minutes"`
		Reason                  string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil || (in.OperatorSessionMaxHours == nil && in.OperatorIdleMinutes == nil &&
		in.SigninLockFailures == nil && in.SigninLockMinutes == nil) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "at least one setting is required")
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
		return
	}
	before, err := b.loadSecuritySettings(r)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its security settings.")
		return
	}
	h, idle := before.OperatorSessionMaxHours, before.OperatorIdleMinutes
	lockN, lockMins := before.SigninLockFailures, before.SigninLockMinutes
	if in.SigninLockFailures != nil {
		lockN = *in.SigninLockFailures
	}
	if in.SigninLockMinutes != nil {
		lockMins = *in.SigninLockMinutes
	}
	if lockN < auth.MinSigninLockFailures || lockN > auth.MaxSigninLockFailures {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "Wrong attempts before a lock must be between 3 and 20.")
		return
	}
	if lockMins < auth.MinSigninLockMinutes || lockMins > auth.MaxSigninLockMinutes {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The lock time must be between 1 and 1440 minutes.")
		return
	}
	if in.OperatorSessionMaxHours != nil {
		h = *in.OperatorSessionMaxHours
	}
	if in.OperatorIdleMinutes != nil {
		idle = *in.OperatorIdleMinutes
	}
	if h < auth.MinSessionMaxHours || h > auth.MaxSessionMaxHours {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The session lifetime must be between 1 and 168 hours.")
		return
	}
	if idle < auth.MinIdleMinutes || idle > auth.MaxIdleMinutes {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "Sign out after inactivity must be between 5 and 240 minutes.")
		return
	}
	_, email := actorOf(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	if _, err := b.DB.Exec(ctx, `UPDATE central_security_settings
                                    SET operator_session_max_hours=$1, operator_idle_minutes=$2, signin_lock_failures=$4,
                                        signin_lock_minutes=$5, updated_at=now(), updated_by=$3
                                  WHERE id`, h, idle, email, lockN, lockMins); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The setting could not be saved. Try again.")
		return
	}
	payload := map[string]any{"reason": in.Reason}
	if h != before.OperatorSessionMaxHours {
		payload["operator_session_max_hours"] = map[string]int{"from": before.OperatorSessionMaxHours, "to": h}
	}
	if idle != before.OperatorIdleMinutes {
		payload["operator_idle_minutes"] = map[string]int{"from": before.OperatorIdleMinutes, "to": idle}
	}
	if lockN != before.SigninLockFailures {
		payload["signin_lock_failures"] = map[string]int{"from": before.SigninLockFailures, "to": lockN}
	}
	if lockMins != before.SigninLockMinutes {
		payload["signin_lock_minutes"] = map[string]int{"from": before.SigninLockMinutes, "to": lockMins}
	}
	audit.Op(r.Context(), b.DB, r, "settings.security_updated", "settings", "security", payload)
	b.getSecuritySettings(w, r)
}
