package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

// LICENCES, one appliance at a time. Every change is a NEW signed document with the next licence_version;
// the current one is never edited. Licence operations never touch the appliance lifecycle (F4): an appliance
// whose licence is suspended or revoked keeps its identity and keeps talking to Central, which is how it
// receives the suspension and the revocation list.

// LicensesBase serves the appliance's licence fetch.
type LicensesBase struct {
	*Base
	Svc *licensing.Service
}

// licenseTerms is the licence block of activate and of Set licence.
type licenseTerms struct {
	MaxConcurrentOnlineGuests int        `json:"max_concurrent_online_guests"`
	ValidUntil                *time.Time `json:"valid_until"`
	ValidDays                 int        `json:"valid_days"`
	GracePeriodDays           *int       `json:"grace_period_days"`
}

const (
	defaultValidDays = 365
	defaultGraceDays = 30
)

// params turns the request terms into issue parameters. valid_until wins over valid_days; with neither the
// licence runs 365 days. An omitted grace period is 30 days.
func (t licenseTerms) params(applianceID, createdBy string) (licensing.IssueParams, error) {
	p := licensing.IssueParams{ApplianceID: applianceID, CreatedBy: createdBy,
		MaxConcurrentOnlineGuests: t.MaxConcurrentOnlineGuests, GracePeriodDays: defaultGraceDays}
	if t.MaxConcurrentOnlineGuests < 0 {
		return p, errors.New("max_concurrent_online_guests cannot be negative (0 = unlimited)")
	}
	if t.GracePeriodDays != nil {
		p.GracePeriodDays = *t.GracePeriodDays
	}
	switch {
	case t.ValidUntil != nil:
		p.ValidUntil = *t.ValidUntil
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
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
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
	doc, _, err := b.Lic.Issue(ctx, p)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "licence not issued: "+err.Error())
		return
	}
	audit.Op(r.Context(), b.DB, r, "license.issued", "appliance", id, map[string]any{
		"_tenant_id": doc.TenantID, "license_id": doc.LicenseID, "license_version": doc.LicenseVersion,
		"max_concurrent_online_guests": doc.MaxConcurrentOnlineGuests, "valid_until": doc.ValidUntil,
		"grace_period_days": doc.GracePeriodDays, "supersedes": doc.SupersedesLicenseID, "reason": in.Reason})
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
		_ = DecodeJSON(r, &in)
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
		case err != nil:
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
			return
		}
		verb := map[string]string{"suspend": "suspended", "resume": "resumed", "revoke": "revoked"}[action]
		audit.Op(r.Context(), b.DB, r, "license."+verb, "appliance", applianceID, map[string]any{
			"_tenant_id": tenantID, "license_id": id, "new_license_id": resultID, "reason": in.Reason})
		b.writeLicense(w, r, http.StatusOK, resultID)
	}
}

// ApplianceLicenseHandler serves GET /v1/appliance/license (appliance-JWT). Returns the signed envelope of
// the appliance's CURRENT licence (active or suspended) plus the ids of its revoked licences, so a revocation
// takes effect even before a replacement is issued. A successful fetch counts as a cloud validation on the
// edge side. The wire format is unchanged.
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
	var revoked []string
	rows, err := b.DB.Query(ctx, `
        SELECT l.id FROM licenses l
         WHERE $1::uuid = ANY(l.appliance_ids) AND l.status = 'revoked'
    `, ident.ApplianceID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				revoked = append(revoked, id)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"license_id":  licenseID,
		"envelope":    json.RawMessage(envelope),
		"revoked":     revoked,
		"server_time": time.Now().UTC(),
	})
}
