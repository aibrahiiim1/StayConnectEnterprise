package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

// LicensesBase serves /cloud/v1/licenses (operator side) and the appliance
// license-fetch endpoint. Issuing and revoking are platform_admin actions;
// tenant operators get read access to their own tenant's licenses.
type LicensesBase struct {
	*Base
	Svc *licensing.Service
}

type License struct {
	ID                 string          `json:"id"`
	TenantID           string          `json:"tenant_id"`
	SiteID             string          `json:"site_id"`
	CommercialPlanCode string          `json:"commercial_plan_code"`
	Status             string          `json:"status"`
	IssuedAt           time.Time       `json:"issued_at"`
	ValidUntil         time.Time       `json:"valid_until"`
	OfflineGraceDays   int             `json:"offline_grace_days"`
	ApplianceIDs       []string        `json:"appliance_ids"`
	Features           json.RawMessage `json:"features"`
	Limits             json.RawMessage `json:"limits"`
	KeyID              string          `json:"key_id"`
	RevokedAt          *time.Time      `json:"revoked_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`

	// Simple model (v3): the license IS the entitlement.
	LicenseVersion            int64      `json:"license_version"`
	MaxConcurrentOnlineGuests int        `json:"max_concurrent_online_guests"`
	GracePeriodDays           int        `json:"grace_period_days"`
	ValidFrom                 *time.Time `json:"valid_from,omitempty"`
	SupersedesLicenseID       *string    `json:"supersedes_license_id,omitempty"`
}

func (b *LicensesBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", b.list)
	// Ownership-aware roll-up for the Platform dashboard. Registered before
	// "/{id}" so the literal path is not captured as an id.
	r.Get("/fleet-summary", b.fleetSummary)
	r.Get("/{id}", b.get)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole("platform_admin"))
		r.Use(RequireReauth(b.Redis)) // license changes are billing-sensitive → step-up
		r.Post("/", b.issue)
		r.Post("/{id}/renew", b.issue) // renew = supersede with a fresh signed doc
		r.Post("/{id}/suspend", b.suspend)
		r.Post("/{id}/resume", b.resume)
		r.Post("/{id}/revoke", b.revoke)
		// Revoke active/suspended licenses whose ownership no longer exists
		// (deleted site, or appliance-bound with no surviving appliance).
		r.Post("/reconcile-orphans", b.reconcileOrphans)
	})
	return r
}

// driveApplianceLifecycle mirrors a license state change onto the commercial
// lifecycle_state of the site's appliances. Presence (online/offline) lives in
// the separate legacy `status` column and is driven by heartbeat; this only
// moves the commercial dimension. It never touches suspended→revoked identity
// states set by the enrollment/identity path.
func (b *LicensesBase) driveApplianceLifecycle(ctx context.Context, siteID, to string) {
	var from []string
	switch to {
	case "licensed":
		from = []string{"assigned", "licensed", "grace", "suspended", "license_expired", "revoked"}
	case "suspended":
		from = []string{"licensed", "grace"}
	case "grace":
		from = []string{"licensed"}
	case "license_expired":
		// Natural expiry only — NOT revocation. Revocation is explicit below.
		from = []string{"licensed", "grace"}
	case "revoked":
		// License explicitly cancelled; cannot resume as the same license.
		from = []string{"licensed", "grace", "suspended", "license_expired"}
	default:
		return
	}
	_, _ = b.DB.Exec(ctx, `
        UPDATE appliances SET lifecycle_state=$2, updated_at=now()
         WHERE site_id=$1 AND lifecycle_state = ANY($3)`, siteID, to, from)
}

const licenseCols = `id, tenant_id, site_id, commercial_plan_code, status, issued_at,
       valid_until, offline_grace_days, appliance_ids, features, limits, key_id,
       revoked_at, created_at,
       COALESCE(license_version,0), COALESCE(max_concurrent_online_guests,0),
       COALESCE(grace_period_days,0), valid_from, supersedes_license_id`

func scanLicense(row interface{ Scan(...any) error }) (License, error) {
	var l License
	err := row.Scan(&l.ID, &l.TenantID, &l.SiteID, &l.CommercialPlanCode, &l.Status,
		&l.IssuedAt, &l.ValidUntil, &l.OfflineGraceDays, &l.ApplianceIDs,
		&l.Features, &l.Limits, &l.KeyID, &l.RevokedAt, &l.CreatedAt,
		&l.LicenseVersion, &l.MaxConcurrentOnlineGuests, &l.GracePeriodDays,
		&l.ValidFrom, &l.SupersedesLicenseID)
	return l, err
}

func (b *LicensesBase) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Simple license model: superseded documents are immutable history and are
	// NEVER returned from the normal fetch — only the current effective license
	// per site is listed (plus revoked, which the UI still surfaces for audit).
	conds := []string{`status <> 'superseded'`}
	args := []any{}
	if tenantID != "" {
		args = append(args, tenantID)
		conds = append(conds, `tenant_id = $`+strconv.Itoa(len(args)))
	} else if s := auth.FromContext(r.Context()); s == nil || !s.IsSuperAdmin {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant scope required")
		return
	}
	if site := r.URL.Query().Get("site_id"); site != "" {
		args = append(args, site)
		conds = append(conds, `site_id = $`+strconv.Itoa(len(args)))
	}
	q := `SELECT ` + licenseCols + ` FROM licenses WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY created_at DESC LIMIT 200`

	rows, err := b.DB.Query(ctx, q, args...)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []License
	for rows.Next() {
		l, err := scanLicense(rows)
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, l)
	}
	WriteList(w, out, ListMeta{})
}

// ownershipValidSQL is the single source of truth for "does this license still
// have a valid owner": its tenant and site must exist, and if it is
// appliance-bound (non-empty appliance_ids) at least one of those appliances
// must still exist. A site license (empty appliance_ids) only needs its site.
const ownershipValidSQL = `(
    EXISTS (SELECT 1 FROM tenants t WHERE t.id = l.tenant_id)
    AND EXISTS (SELECT 1 FROM sites s WHERE s.id = l.site_id)
    AND (
        COALESCE(cardinality(l.appliance_ids), 0) = 0
        OR EXISTS (SELECT 1 FROM appliances a WHERE a.id = ANY(l.appliance_ids))
    )
)`

// fleetSummary returns the license roll-up for the dashboard, counting ONLY
// licenses with valid ownership. A license bound to a deleted appliance (or a
// deleted site) is never counted as Active/Expiring/etc — it is reported under
// "orphaned" so the integrity issue is visible and can be reconciled.
func (b *LicensesBase) fleetSummary(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()

	where := `l.status <> 'superseded'`
	args := []any{}
	if tenantID != "" {
		where += ` AND l.tenant_id = $1`
		args = append(args, tenantID)
	} else if s := auth.FromContext(r.Context()); s == nil || !s.IsSuperAdmin {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant scope required")
		return
	}

	q := `
    WITH scoped AS (
        SELECT l.status, l.valid_until, ` + ownershipValidSQL + ` AS owner_ok
          FROM licenses l
         WHERE ` + where + `
    )
    SELECT
      count(*) FILTER (WHERE owner_ok AND status='active' AND valid_until >  now() + interval '30 days')                                  AS active,
      count(*) FILTER (WHERE owner_ok AND status='active' AND valid_until >  now() AND valid_until <= now() + interval '30 days')          AS expiring,
      count(*) FILTER (WHERE owner_ok AND status='active' AND valid_until <= now())                                                       AS expired,
      count(*) FILTER (WHERE owner_ok AND status='suspended')                                                                             AS suspended,
      count(*) FILTER (WHERE status='revoked')                                                                                            AS revoked,
      count(*) FILTER (WHERE status IN ('active','suspended') AND NOT owner_ok)                                                           AS orphaned,
      count(*) FILTER (WHERE owner_ok)                                                                                                     AS total
      FROM scoped`

	var active, expiring, expired, suspended, revoked, orphaned, total int
	if err := b.DB.QueryRow(ctx, q, args...).Scan(&active, &expiring, &expired, &suspended, &revoked, &orphaned, &total); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "summary query failed")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"active": active, "expiring": expiring, "expired": expired,
		"suspended": suspended, "revoked": revoked, "orphaned": orphaned, "total": total,
	})
}

// reconcileOrphans revokes every active/suspended license whose ownership no
// longer exists, so orphaned entitlements never linger silently. Returns the
// revoked ids. Platform-admin + step-up gated (mounted in the write group).
func (b *LicensesBase) reconcileOrphans(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT l.id::text, COALESCE(l.site_id::text,''), COALESCE(l.tenant_id::text,'')
          FROM licenses l
         WHERE l.status IN ('active','suspended') AND NOT `+ownershipValidSQL)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	type orphan struct{ id, site, tenant string }
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if rows.Scan(&o.id, &o.site, &o.tenant) == nil {
			orphans = append(orphans, o)
		}
	}
	rows.Close()

	revoked := []string{}
	for _, o := range orphans {
		if err := b.Svc.Revoke(ctx, o.id); err != nil {
			continue
		}
		revoked = append(revoked, o.id)
		if o.site != "" {
			b.driveApplianceLifecycle(ctx, o.site, "revoked")
		}
		audit.Op(r.Context(), b.DB, r, "license.orphan_reconciled", "license", o.id, map[string]any{
			"_tenant_id": o.tenant, "site_id": o.site, "reason": "owner (site/appliance) no longer exists",
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"revoked": len(revoked), "license_ids": revoked})
}

func (b *LicensesBase) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	l, err := scanLicense(b.DB.QueryRow(ctx, `SELECT `+licenseCols+` FROM licenses WHERE id = $1`, id))
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "license not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	// Tenant scoping: non-super operators only see their own tenant.
	if s := auth.FromContext(r.Context()); s != nil && !s.IsSuperAdmin && s.DefaultTenantID != l.TenantID {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "license not found")
		return
	}
	WriteJSON(w, http.StatusOK, l)
}

// issue creates (or, on /{id}/renew, supersedes with) a signed license under
// the SIMPLE model: one appliance, a max-concurrent-online-guests cap, a
// validity window and an explicit grace period. No plan/subscription needed.
// Renew may change the cap, dates and grace — the change always lands as a NEW
// signed document with a higher license_version (the active document is never
// silently mutated, and the appliance's anti-rollback rejects the old one).
func (b *LicensesBase) issue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TenantID                  string     `json:"tenant_id"`
		SiteID                    string     `json:"site_id"`
		ApplianceID               string     `json:"appliance_id"` // bind the license to this appliance's hardware/identity
		MaxConcurrentOnlineGuests int        `json:"max_concurrent_online_guests"`
		ValidFrom                 *time.Time `json:"valid_from"`
		ValidUntil                *time.Time `json:"valid_until"`
		ValidDays                 int        `json:"valid_days"` // fallback when valid_until absent
		GracePeriodDays           int        `json:"grace_period_days"`
		OfflineGraceDays          int        `json:"offline_grace_days"` // legacy field name, maps to grace
	}
	if err := DecodeJSON(r, &in); err != nil || in.SiteID == "" || in.TenantID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant_id and site_id are required")
		return
	}
	if in.ValidUntil == nil && in.ValidDays <= 0 {
		in.ValidDays = 365
	}
	if in.GracePeriodDays <= 0 {
		in.GracePeriodDays = in.OfflineGraceDays
	}
	if in.GracePeriodDays <= 0 {
		in.GracePeriodDays = 30
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	createdBy := ""
	if s := auth.FromContext(r.Context()); s != nil {
		createdBy = s.OperatorID
	}
	p := licensing.IssueParams{
		TenantID: in.TenantID, SiteID: in.SiteID, ApplianceID: in.ApplianceID,
		CreatedBy:                 createdBy,
		MaxConcurrentOnlineGuests: in.MaxConcurrentOnlineGuests,
		GracePeriodDays:           in.GracePeriodDays,
		ValidFor:                  time.Duration(in.ValidDays) * 24 * time.Hour,
	}
	if in.ValidFrom != nil {
		p.ValidFrom = *in.ValidFrom
	}
	if in.ValidUntil != nil {
		p.ValidUntil = *in.ValidUntil
	}
	doc, env, err := b.Svc.Issue(ctx, p)
	switch {
	case errors.Is(err, licensing.ErrNoSigner):
		Fail(w, r, http.StatusServiceUnavailable, CodeInternal, "vendor signing key not configured")
		return
	case err != nil:
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	// Drive the site's appliances into the 'licensed' commercial state — the
	// lifecycle now follows the actual signed license, not a manual DB edit.
	b.driveApplianceLifecycle(ctx, in.SiteID, "licensed")
	audit.Op(r.Context(), b.DB, r, "license.issued", "license", doc.LicenseID, map[string]any{
		"_tenant_id": in.TenantID, "site_id": in.SiteID,
		"license_version": doc.LicenseVersion, "max_concurrent_online_guests": doc.MaxConcurrentOnlineGuests,
		"grace_period_days": doc.GracePeriodDays, "valid_from": doc.ValidFrom, "valid_until": doc.ValidUntil,
		"supersedes": doc.SupersedesLicenseID,
	})
	WriteJSON(w, http.StatusCreated, map[string]any{
		"license_id":      doc.LicenseID,
		"license_version": doc.LicenseVersion,
		"document":        doc,
		"envelope":        env,
	})
}

func (b *LicensesBase) suspend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	createdBy := ""
	if s := auth.FromContext(r.Context()); s != nil {
		createdBy = s.OperatorID
	}
	siteID, err := b.Svc.Suspend(ctx, id, createdBy)
	if err != nil {
		if errors.Is(err, licensing.ErrNoLicense) {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "no current license with that id")
			return
		}
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	b.driveApplianceLifecycle(ctx, siteID, "suspended")
	audit.Op(r.Context(), b.DB, r, "license.suspended", "license", id, map[string]any{"site_id": siteID})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "suspended", "license_id": id})
}

func (b *LicensesBase) resume(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	createdBy := ""
	if s := auth.FromContext(r.Context()); s != nil {
		createdBy = s.OperatorID
	}
	siteID, err := b.Svc.Resume(ctx, id, createdBy)
	if err != nil {
		if errors.Is(err, licensing.ErrNoLicense) {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "no current license with that id")
			return
		}
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	b.driveApplianceLifecycle(ctx, siteID, "licensed")
	audit.Op(r.Context(), b.DB, r, "license.resumed", "license", id, map[string]any{"site_id": siteID})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "active", "license_id": id})
}

func (b *LicensesBase) revoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var tenantID, siteID string
	_ = b.DB.QueryRow(ctx, `SELECT tenant_id::text, site_id::text FROM licenses WHERE id = $1`, id).Scan(&tenantID, &siteID)
	if err := b.Svc.Revoke(ctx, id); err != nil {
		if errors.Is(err, licensing.ErrNoLicense) {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "no current license with that id")
			return
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "revoke failed")
		return
	}
	// Revocation drives the distinct 'revoked' state — never 'license_expired'.
	b.driveApplianceLifecycle(ctx, siteID, "revoked")
	audit.Op(r.Context(), b.DB, r, "license.revoked", "license", id, map[string]any{"_tenant_id": tenantID, "site_id": siteID})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "revoked", "license_id": id})
}

// ApplianceLicenseHandler serves GET /v1/appliance/license (appliance-JWT).
// Returns the signed envelope for the appliance's site plus a revocation
// hint. A successful fetch counts as a cloud validation on the edge side.
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
		// No CURRENT license — still a 200: the appliance must receive the
		// revocation list below so a freshly revoked license takes effect
		// even before a replacement is issued.
		envelope, licenseID = "null", ""
	case err != nil:
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "license lookup failed")
		return
	}
	// Revoked license ids for this site so the edge can populate its local
	// revocation store even when a new license hasn't been issued yet.
	var revoked []string
	rows, err := b.DB.Query(ctx, `
        SELECT l.id FROM licenses l
          JOIN appliances a ON a.site_id = l.site_id
         WHERE a.id = $1 AND l.status = 'revoked'
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
