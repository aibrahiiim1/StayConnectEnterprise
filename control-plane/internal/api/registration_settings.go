package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
)

// AD-19 / AD-20: limits on unauthenticated appliance registration.
//
// The cap on waiting appliances is a bounded setting (CLAUDE.md 0C): default 200, 10-5000, changed with a reason and
// audited. A NEW waiting row past the cap is refused with 503 and one alert; known appliances re-registering are
// never refused by it.

var registrationMaxWaiting = settingBounds{Default: 200, Min: 10, Max: 5000, Unit: "appliances"}

type registrationSettingsView struct {
	MaxWaitingAppliances int           `json:"max_waiting_appliances"`
	MaxWaiting           settingBounds `json:"max_waiting_appliances_bounds"`
	UpdatedAt            *time.Time    `json:"updated_at"`
	UpdatedBy            *string       `json:"updated_by"`
}

// Serial and fingerprint an appliance derives when it has no DMI data, machine-id or NIC (AD-19). Proven equal to the
// appliance's computation by data-plane/internal/hwid/constant_anchor_test.go.
const (
	constantAnchorSerial      = "SC-KC9P-ZWSS-DPYJ"
	constantAnchorFingerprint = "b3a440cce92f0dfd177b46ef874824a5"
)

// constantAnchorRefusal refuses a NEW registration whose identity facts are the shared constant: such an appliance
// is indistinguishable from every other one without DMI data, so a licence bound to it would bind to none of them.
// Existing rows are left alone by the callers.
func constantAnchorRefusal(serial, fingerprint string) *moveRefusal {
	if strings.EqualFold(serial, constantAnchorSerial) || strings.EqualFold(fingerprint, constantAnchorFingerprint) {
		return &moveRefusal{http.StatusUnprocessableEntity, "hardware_identity_not_unique",
			"This appliance has no hardware identity of its own (no DMI data, machine-id or network adapter), so it " +
				"would share its serial and fingerprint with every other such machine. Give the virtual machine a " +
				"unique UUID/serial or a network adapter, then register it again."}
	}
	return nil
}

// registrationCapRefusal is the AD-20 check for a NEW waiting row. It returns nil while under the cap. The count is
// of rows waiting for activation (pending_approval, not being deleted).
func (b *Base) registrationCapRefusal(ctx context.Context, r *http.Request, serial, ip string) *moveRefusal {
	limit := registrationMaxWaiting.Default
	if err := b.DB.QueryRow(ctx, `SELECT max_waiting_appliances FROM central_registration_settings WHERE id`).Scan(&limit); err != nil &&
		err != pgx.ErrNoRows {
		return &moveRefusal{http.StatusInternalServerError, CodeInternal, "lookup failed"}
	}
	var waiting int
	if err := b.DB.QueryRow(ctx, `SELECT count(*) FROM appliances
         WHERE lifecycle_state='pending_approval' AND deletion_started_at IS NULL`).Scan(&waiting); err != nil {
		return &moveRefusal{http.StatusInternalServerError, CodeInternal, "lookup failed"}
	}
	if waiting < limit {
		return nil
	}
	b.alertAndAudit(ctx, r, "", serial, "registration_cap_reached", map[string]any{
		"reason":          "new appliance registrations are being refused because the number of appliances waiting for activation reached its limit",
		"waiting":         waiting,
		"limit":           limit,
		"operator_action": "Activate or delete the waiting appliances you do not need, or raise the limit under System, Settings, Registration. Unexpected growth may be someone registering fake appliances.",
	}, ip)
	return &moveRefusal{http.StatusServiceUnavailable, "registration_capacity",
		"Central is not accepting new appliance registrations right now; try again later."}
}

func (b *Base) loadRegistrationSettings(r *http.Request) (registrationSettingsView, error) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	v := registrationSettingsView{MaxWaiting: registrationMaxWaiting}
	var by string
	var at time.Time
	if err := b.DB.QueryRow(ctx, `SELECT max_waiting_appliances, updated_at, COALESCE(updated_by,'')
                                    FROM central_registration_settings WHERE id`).Scan(&v.MaxWaitingAppliances, &at, &by); err != nil {
		return v, err
	}
	v.UpdatedAt, v.UpdatedBy = &at, strPtr(by)
	return v, nil
}

// getRegistrationSettings: GET /cloud/v1/settings/registration
func (b *Base) getRegistrationSettings(w http.ResponseWriter, r *http.Request) {
	v, err := b.loadRegistrationSettings(r)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its registration settings.")
		return
	}
	WriteJSON(w, http.StatusOK, v)
}

// patchRegistrationSettings: PATCH /cloud/v1/settings/registration
func (b *Base) patchRegistrationSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MaxWaitingAppliances *int   `json:"max_waiting_appliances"`
		Reason               string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.MaxWaitingAppliances == nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "at least one setting is required")
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
		return
	}
	n := *in.MaxWaitingAppliances
	if n < registrationMaxWaiting.Min || n > registrationMaxWaiting.Max {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The limit on waiting appliances must be between "+
			strconv.Itoa(registrationMaxWaiting.Min)+" and "+strconv.Itoa(registrationMaxWaiting.Max)+" appliances.")
		return
	}
	before, err := b.loadRegistrationSettings(r)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its registration settings.")
		return
	}
	_, email := actorOf(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	if _, err := b.DB.Exec(ctx, `UPDATE central_registration_settings SET max_waiting_appliances=$1, updated_at=now(), updated_by=$2
                                  WHERE id`, n, email); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The setting could not be saved. Try again.")
		return
	}
	audit.Op(r.Context(), b.DB, r, "settings.registration_updated", "settings", "registration", map[string]any{
		"reason": in.Reason, "max_waiting_appliances": map[string]int{"from": before.MaxWaitingAppliances, "to": n}})
	b.getRegistrationSettings(w, r)
}
