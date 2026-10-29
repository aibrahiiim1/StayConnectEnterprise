package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

// Every licence change is a new signed document with the next licence_version. Licence operations never touch the
// appliance lifecycle: a suspended or revoked appliance keeps talking to Central, which is how it learns of it.

type LicensesBase struct {
	*Base
	Svc *licensing.Service
}

// licenseTerms is the licence block of activate and of Set licence.
type licenseTerms struct {
	MaxConcurrentOnlineGuests int `json:"max_concurrent_online_guests"`
	// UnlimitedOnlineGuests must be sent (with the cap 0 or omitted) to issue a licence with no concurrent cap;
	// an omitted or zero cap alone is refused, so unlimited is always deliberate.
	UnlimitedOnlineGuests bool       `json:"unlimited_online_guests"`
	ValidUntil            *time.Time `json:"valid_until"`
	ValidDays             int        `json:"valid_days"`
	GracePeriodDays       *int       `json:"grace_period_days"`
	// Modules are the registered module ids the licence authorises (GET /cloud/v1/modules). Omitted or empty =
	// core only. Unknown ids and unmet dependencies are refused with 400.
	Modules []string `json:"modules"`
}

const (
	defaultValidDays = 365
	defaultGraceDays = 30
	// Upper bounds on signed terms: past them a duration overflows or a typo signs a licence for centuries
	// (audit Phase 1 L10).
	maxValidDays  = 3650
	maxValidYears = 10
	maxGraceDays  = 365
	maxGuestCap   = 1_000_000
)

// params turns the request terms into issue parameters. valid_until wins over valid_days; with neither the
// licence runs 365 days. An omitted grace period is 30 days.
func (t licenseTerms) params(applianceID, createdBy string) (licensing.IssueParams, error) {
	p := licensing.IssueParams{ApplianceID: applianceID, CreatedBy: createdBy,
		MaxConcurrentOnlineGuests: t.MaxConcurrentOnlineGuests, GracePeriodDays: defaultGraceDays}
	switch {
	case t.MaxConcurrentOnlineGuests < 0:
		return p, errors.New("max_concurrent_online_guests cannot be negative")
	case t.MaxConcurrentOnlineGuests > maxGuestCap:
		return p, fmt.Errorf("max_concurrent_online_guests must be at most %d", maxGuestCap)
	case t.UnlimitedOnlineGuests && t.MaxConcurrentOnlineGuests != 0:
		return p, errors.New("give either max_concurrent_online_guests or unlimited_online_guests, not both")
	case t.MaxConcurrentOnlineGuests == 0 && !t.UnlimitedOnlineGuests:
		return p, errors.New("max_concurrent_online_guests must be 1 or more; for a licence with no cap send unlimited_online_guests: true")
	}
	_, _, ids, err := licensing.ModuleTerms(t.Modules)
	if err != nil {
		return p, errors.New("modules: " + err.Error())
	}
	p.Modules = ids
	if t.GracePeriodDays != nil {
		if *t.GracePeriodDays < 0 || *t.GracePeriodDays > maxGraceDays {
			return p, fmt.Errorf("grace_period_days must be between 0 and %d", maxGraceDays)
		}
		p.GracePeriodDays = *t.GracePeriodDays
	}
	switch {
	case t.ValidUntil != nil:
		if t.ValidUntil.After(time.Now().AddDate(maxValidYears, 0, 0)) {
			return p, fmt.Errorf("valid_until must be at most %d years from now", maxValidYears)
		}
		p.ValidUntil = *t.ValidUntil
	case t.ValidDays > maxValidDays:
		return p, fmt.Errorf("valid_days must be at most %d", maxValidDays)
	case t.ValidDays > 0:
		p.ValidFor = time.Duration(t.ValidDays) * 24 * time.Hour
	case t.ValidDays < 0:
		return p, errors.New("valid_days must be positive")
	default:
		p.ValidFor = defaultValidDays * 24 * time.Hour
	}
	return p, nil
}

// setLicense: POST /cloud/v1/appliances/{id}/license — issue, renew or change terms.
func (b *Base) setLicense(w http.ResponseWriter, r *http.Request) {
	if b.Lic == nil {
		Fail(w, r, http.StatusServiceUnavailable, "licensing_unavailable", "the vendor signing key is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	var in struct {
		licenseTerms
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
		return
	}
	row, ok := b.loadOne(w, r, Scope{All: true}, id)
	if !ok {
		return
	}
	if row.lifecycle != "assigned" || row.Activation == ActivationRetiring {
		Fail(w, r, http.StatusConflict, "invalid_state", "only an activated appliance can hold a licence")
		return
	}
	operatorID, _ := actorOf(r)
	p, err := in.params(id, operatorID)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	// Re-check under lock: a retirement or move committed since the check above must not be followed by a fresh licence.
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		failInternal(w, r, "licence not issued", err)
		return
	}
	defer tx.Rollback(ctx)
	var lifecycle string
	var retiring bool
	if err := tx.QueryRow(ctx, `SELECT a.lifecycle_state, EXISTS (SELECT 1 FROM appliance_terminal_delivery td WHERE td.appliance_id=a.id)
                                  FROM appliances a WHERE a.id=$1 FOR UPDATE`, id).Scan(&lifecycle, &retiring); err != nil ||
		lifecycle != "assigned" || retiring {
		Fail(w, r, http.StatusConflict, "invalid_state", "only an activated appliance can hold a licence")
		return
	}
	doc, _, err := b.Lic.IssueTx(ctx, tx, p)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "licence not issued: "+publicError(r, err))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		failInternal(w, r, "licence not issued", err)
		return
	}
	audit.Op(r.Context(), b.DB, r, "license.issued", "appliance", id, map[string]any{
		"_tenant_id": doc.TenantID, "license_id": doc.LicenseID, "license_version": doc.LicenseVersion,
		"max_concurrent_online_guests": doc.MaxConcurrentOnlineGuests, "valid_until": doc.ValidUntil,
		"grace_period_days": doc.GracePeriodDays, "modules": doc.Modules.ModuleIDs(),
		"supersedes": doc.SupersedesLicenseID, "reason": in.Reason})
	b.writeLicense(w, r, http.StatusCreated, doc.LicenseID)
}

func (b *Base) writeLicense(w http.ResponseWriter, r *http.Request, code int, id string) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.queryLicenses(ctx, licenseFilter{licenseID: id, includeSuperseded: true})
	if err != nil || len(rows) == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "licence not found")
		return
	}
	WriteJSON(w, code, rows[0])
}

// licenseAction: POST /cloud/v1/licenses/{id}/suspend|resume|revoke {reason}
func (b *Base) licenseAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if b.Lic == nil {
			Fail(w, r, http.StatusServiceUnavailable, "licensing_unavailable", "the vendor signing key is not configured")
			return
		}
		id := chi.URLParam(r, "id")
		var in struct {
			Reason string `json:"reason"`
		}
		if limitExceeded(w, r, DecodeJSON(r, &in)) {
			return
		}
		if strings.TrimSpace(in.Reason) == "" {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
			return
		}
		ctx, cancel := DBCtx(r)
		defer cancel()
		var tenantID, applianceID string
		_ = b.DB.QueryRow(ctx, `SELECT tenant_id::text, COALESCE(appliance_ids[1]::text,'') FROM licenses WHERE id::text=$1`, id).
			Scan(&tenantID, &applianceID)
		operatorID, _ := actorOf(r)
		resultID := id
		var err error
		switch action {
		case "suspend":
			var cur licensing.IssueParams
			if cur, err = b.Lic.CurrentTerms(ctx, id); err == nil && cur.Status == "suspended" {
				Fail(w, r, http.StatusConflict, "invalid_state", "the licence is already suspended")
				return
			}
			if err == nil {
				if doc, e := b.Lic.Suspend(ctx, id, operatorID); e == nil {
					resultID = doc.LicenseID
				} else {
					err = e
				}
			}
		case "resume":
			var cur licensing.IssueParams
			if cur, err = b.Lic.CurrentTerms(ctx, id); err == nil && cur.Status != "suspended" {
				Fail(w, r, http.StatusConflict, "invalid_state", "the licence is not suspended")
				return
			}
			if err == nil {
				if doc, e := b.Lic.Resume(ctx, id, operatorID); e == nil {
					resultID = doc.LicenseID
				} else {
					err = e
				}
			}
		case "revoke":
			err = b.Lic.Revoke(ctx, id)
		}
		switch {
		case errors.Is(err, licensing.ErrNoLicense):
			Fail(w, r, http.StatusNotFound, CodeNotFound, "no current licence with that id")
			return
		case errors.Is(err, licensing.ErrSameStatus):
			Fail(w, r, http.StatusConflict, "invalid_state", "the licence is already "+map[string]string{"suspend": "suspended", "resume": "active"}[action])
			return
		case err != nil:
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, publicError(r, err))
			return
		}
		verb := map[string]string{"suspend": "suspended", "resume": "resumed", "revoke": "revoked"}[action]
		audit.Op(r.Context(), b.DB, r, "license."+verb, "appliance", applianceID, map[string]any{
			"_tenant_id": tenantID, "license_id": id, "new_license_id": resultID, "reason": in.Reason})
		b.writeLicense(w, r, http.StatusOK, resultID)
	}
}

// ApplianceLicenseHandler returns the signed envelope of the current licence plus the ids of revoked licences, so a
// revocation takes effect before a replacement is issued.
func (b *LicensesBase) ApplianceLicenseHandler(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "appliance identity required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	envelope, licenseID, err := b.Svc.CurrentEnvelopeForAppliance(ctx, ident.ApplianceID)
	switch {
	case errors.Is(err, licensing.ErrNoLicense):
		envelope, licenseID = "null", ""
	case err != nil:
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "license lookup failed")
		return
	}
	// Fail rather than send an empty list after a failed query, which the appliance would read as "nothing revoked".
	var revoked []string
	rows, err := b.DB.Query(ctx, `
        SELECT l.id FROM licenses l
         WHERE $1::uuid = ANY(l.appliance_ids) AND l.status = 'revoked'
    `, ident.ApplianceID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "license lookup failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "license lookup failed")
			return
		}
		revoked = append(revoked, id)
	}
	if rows.Err() != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "license lookup failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"license_id":  licenseID,
		"envelope":    json.RawMessage(envelope),
		"revoked":     revoked,
		"server_time": time.Now().UTC(),
	}); err != nil || len(revoked) == 0 {
		return
	}
	// The appliance now keeps these revocations (license.Store.AddRevocation), so deleting the site cannot undo them (deleteSite).
	//
	// revocation_delivered_at means SENT, not confirmed (AD-21, audit Phase 1 L5): it is set once the response has been
	// encoded for the appliance. The appliance applies revocations only over mutual TLS (AD-16) and stores them durably
	// before it validates the fetch, and the next fetch repeats the list until the licence is gone, but Central does
	// not wait for a confirmation. Confirmation by protocol is deferred to the Phase 2 licensing work.
	_, _ = b.DB.Exec(ctx, `UPDATE licenses SET revocation_delivered_at = now()
         WHERE $1::uuid = ANY(appliance_ids) AND status = 'revoked' AND id::text = ANY($2) AND revocation_delivered_at IS NULL`,
		ident.ApplianceID, revoked)
}
