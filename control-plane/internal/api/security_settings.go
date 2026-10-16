package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// CENTRAL SECURITY SETTINGS (one row, migration 0051). Today one value: the ABSOLUTE console session
// lifetime -- how long a sign-in lasts however active the operator is. It is an operational value an
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
}

func (b *Base) loadSecuritySettings(r *http.Request) (securitySettingsView, error) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	v := securitySettingsView{Default: auth.DefaultSessionMaxHours, Min: auth.MinSessionMaxHours,
		Max: auth.MaxSessionMaxHours, Unit: "hours"}
	var by string
	var at time.Time
	err := b.DB.QueryRow(ctx, `SELECT operator_session_max_hours, updated_at, COALESCE(updated_by,'')
                                 FROM central_security_settings WHERE id`).Scan(&v.OperatorSessionMaxHours, &at, &by)
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

// patchSecuritySettings: PATCH /cloud/v1/settings/security {operator_session_max_hours, reason}
//
// The new lifetime applies to every session at its next request, including sessions already open: a shorter
// value ends the ones that are now too old.
func (b *Base) patchSecuritySettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OperatorSessionMaxHours *int   `json:"operator_session_max_hours"`
		Reason                  string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.OperatorSessionMaxHours == nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "operator_session_max_hours is required")
		return
	}
	h := *in.OperatorSessionMaxHours
	if h < auth.MinSessionMaxHours || h > auth.MaxSessionMaxHours {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The session lifetime must be between 1 and 168 hours.")
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
	_, email := actorOf(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	if _, err := b.DB.Exec(ctx, `UPDATE central_security_settings
                                    SET operator_session_max_hours=$1, updated_at=now(), updated_by=$2 WHERE id`, h, email); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The setting could not be saved. Try again.")
		return
	}
	audit.Op(r.Context(), b.DB, r, "settings.security_updated", "settings", "security", map[string]any{
		"operator_session_max_hours": map[string]int{"from": before.OperatorSessionMaxHours, "to": h},
		"reason":                     in.Reason})
	b.getSecuritySettings(w, r)
}
