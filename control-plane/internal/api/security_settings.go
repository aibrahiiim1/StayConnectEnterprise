package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// CENTRAL SECURITY SETTINGS (one row, migration 0051; 0052). Two values: the ABSOLUTE console session
// lifetime -- how long a sign-in lasts however active the operator is -- and the idle limit. It is an operational value an
// operator may need to change (shorter for a shared workstation, longer for a field engineer), so it is a
// setting with a default, a unit, bounds, a permission and an audit trail, not a constant
// (docs/OPERATIONAL_SETTINGS_POLICY.md).

type securitySettingsView struct {
	OperatorSessionMaxHours int        `json:"operator_session_max_hours"`
	Default                 int        `json:"default"`
	Min                     int        `json:"min"`
	Max                     int        `json:"max"`
	Unit                    string     `json:"unit"`
	UpdatedAt               *time.Time `json:"updated_at"`
	UpdatedBy               *string    `json:"updated_by"`
	// The idle limit (0052, GDPR plan C10): a session with no activity for this long ends.
	OperatorIdleMinutes int           `json:"operator_idle_minutes"`
	Idle                settingBounds `json:"idle_bounds"`
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
		Idle: settingBounds{Default: auth.DefaultIdleMinutes, Min: auth.MinIdleMinutes, Max: auth.MaxIdleMinutes, Unit: "minutes"}}
	var by string
	var at time.Time
	err := b.DB.QueryRow(ctx, `SELECT operator_session_max_hours, operator_idle_minutes, updated_at, COALESCE(updated_by,'')
                                 FROM central_security_settings WHERE id`).Scan(&v.OperatorSessionMaxHours, &v.OperatorIdleMinutes, &at, &by)
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

// patchSecuritySettings: PATCH /cloud/v1/settings/security {operator_session_max_hours?, operator_idle_minutes?, reason}
//
// The new limits apply to every session at its next request, including sessions already open: a shorter value
// ends the ones that are now too old or too long idle. A value left out keeps its current setting.
func (b *Base) patchSecuritySettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OperatorSessionMaxHours *int   `json:"operator_session_max_hours"`
		OperatorIdleMinutes     *int   `json:"operator_idle_minutes"`
		Reason                  string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil || (in.OperatorSessionMaxHours == nil && in.OperatorIdleMinutes == nil) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "operator_session_max_hours or operator_idle_minutes is required")
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
                                    SET operator_session_max_hours=$1, operator_idle_minutes=$2, updated_at=now(), updated_by=$3
                                  WHERE id`, h, idle, email); err != nil {
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
	audit.Op(r.Context(), b.DB, r, "settings.security_updated", "settings", "security", payload)
	b.getSecuritySettings(w, r)
}
