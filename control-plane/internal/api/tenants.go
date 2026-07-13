package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type Tenant struct {
	ID           string    `json:"id"`
	Slug         string    `json:"slug"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	ContactEmail string    `json:"contact_email,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type tenantWriteReq struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	ContactEmail string `json:"contact_email"`
	Status       string `json:"status"` // PATCH only
}

// TenantsRoutes returns a chi Router mounted under /v1/tenants.
// All writes require platform_admin; reads are scoped by the caller's session.
func (b *Base) TenantsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", b.listTenants)
	r.Post("/", b.createTenant)
	r.Get("/{id}", b.getTenant)
	r.Patch("/{id}", b.patchTenant)
	r.Delete("/{id}", b.archiveTenant)

	// Subscription + limits endpoints. tenantID param name matches the
	// shared handlers in subscriptions.go.
	r.Get("/{tenantID}/subscription", b.getSubscription)
	r.Post("/{tenantID}/subscription", b.changeSubscription)
	// Explicit commercial terms: the operator CHOOSES active / trial / scheduled,
	// the billing interval, start + renewal dates and auto-renew. A plan having
	// trial days never silently forces a trial.
	r.Post("/{tenantID}/subscription-terms", b.SetSubscription)
	r.Get("/{tenantID}/effective-limits", b.effectiveLimits)
	// Tenant-specific entitlement overrides (plan -> subscription -> override).
	r.Get("/{tenantID}/limit-overrides", b.listOverrides)
	r.With(RequireReauth(b.Redis)).Put("/{tenantID}/limit-overrides", b.setOverride)
	r.With(RequireReauth(b.Redis)).Delete("/{tenantID}/limit-overrides/{key}", b.deleteOverride)
	r.Get("/{tenantID}/audit", b.listAudit)
	r.Mount("/{tenantID}/usage", b.UsageRoutes())

	return r
}

func (b *Base) listTenants(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()

	sess := auth.FromContext(r.Context())
	limit := ParseLimit(r, 50, 200)
	curT, curI, err := DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}

	// Non-superadmins only see their own tenant.
	var rows []Tenant
	var q string
	var args []any
	if sess.IsSuperAdmin {
		q = `SELECT id, slug, name, status, COALESCE(contact_email,''), created_at, updated_at
               FROM tenants
              WHERE ($1::timestamptz IS NULL OR (created_at, id) < ($1, $2::uuid))
              ORDER BY created_at DESC, id DESC
              LIMIT $3`
		var tArg, iArg any
		if !curT.IsZero() {
			tArg = curT
		}
		if curI != "" {
			iArg = curI
		}
		args = []any{tArg, iArg, limit + 1}
	} else if sess.DefaultTenantID != "" {
		q = `SELECT id, slug, name, status, COALESCE(contact_email,''), created_at, updated_at
               FROM tenants WHERE id = $1`
		args = []any{sess.DefaultTenantID}
	} else {
		WriteList[Tenant](w, nil, ListMeta{})
		return
	}

	r2, err := b.DB.Query(ctx, q, args...)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer r2.Close()
	for r2.Next() {
		var t Tenant
		if err := r2.Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.ContactEmail, &t.CreatedAt, &t.UpdatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		rows = append(rows, t)
	}

	meta := ListMeta{}
	if sess.IsSuperAdmin && len(rows) > limit {
		last := rows[limit-1]
		meta.HasMore = true
		meta.Cursor = EncodeCursor(last.CreatedAt, last.ID)
		rows = rows[:limit]
	}
	WriteList(w, rows, meta)
}

func (b *Base) createTenant(w http.ResponseWriter, r *http.Request) {
	sess := auth.FromContext(r.Context())
	if !sess.IsSuperAdmin {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "platform_admin only")
		return
	}
	var req tenantWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if req.Slug == "" || req.Name == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "slug and name required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	var t Tenant
	err := b.DB.QueryRow(ctx, `
        INSERT INTO tenants (slug, name, contact_email)
        VALUES ($1, $2, NULLIF($3,''))
        RETURNING id, slug, name, status, COALESCE(contact_email,''), created_at, updated_at
    `, req.Slug, req.Name, req.ContactEmail).Scan(
		&t.ID, &t.Slug, &t.Name, &t.Status, &t.ContactEmail, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "slug conflict or insert failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "tenant.created", "tenant", t.ID, map[string]any{
		"_tenant_id": t.ID, "slug": t.Slug, "name": t.Name,
	})
	WriteJSON(w, http.StatusCreated, t)
}

func (b *Base) getTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess := auth.FromContext(r.Context())
	if !sess.IsSuperAdmin && sess.DefaultTenantID != id {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var t Tenant
	err := b.DB.QueryRow(ctx, `
        SELECT id, slug, name, status, COALESCE(contact_email,''), created_at, updated_at
          FROM tenants WHERE id = $1
    `, id).Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.ContactEmail, &t.CreatedAt, &t.UpdatedAt)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "tenant not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

func (b *Base) patchTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess := auth.FromContext(r.Context())
	if !sess.IsSuperAdmin && sess.DefaultTenantID != id {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	var req tenantWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if req.Status != "" && !sess.IsSuperAdmin {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "only platform_admin may change status")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	var t Tenant
	err := b.DB.QueryRow(ctx, `
        UPDATE tenants SET
            name          = COALESCE(NULLIF($2,''), name),
            contact_email = COALESCE(NULLIF($3,''), contact_email),
            status        = COALESCE(NULLIF($4,''), status),
            updated_at    = now()
         WHERE id = $1
         RETURNING id, slug, name, status, COALESCE(contact_email,''), created_at, updated_at
    `, id, req.Name, req.ContactEmail, req.Status).Scan(
		&t.ID, &t.Slug, &t.Name, &t.Status, &t.ContactEmail, &t.CreatedAt, &t.UpdatedAt,
	)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "tenant not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "tenant.updated", "tenant", t.ID, pickNonEmpty(map[string]any{
		"_tenant_id": t.ID, "name": req.Name, "contact_email": req.ContactEmail, "status": req.Status,
	}))
	WriteJSON(w, http.StatusOK, t)
}

// archiveTenant soft-deletes by setting status='archived'. Hard-delete is not
// exposed to avoid accidental loss of accounting data (foreign-keyed).
func (b *Base) archiveTenant(w http.ResponseWriter, r *http.Request) {
	sess := auth.FromContext(r.Context())
	if !sess.IsSuperAdmin {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "platform_admin only")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	// ?purge=true → HARD delete: remove the customer and everything under it
	// (appliances + their assignments/certs, licenses, tokens, subscriptions,
	// sites) so it can be re-created from scratch. Default = soft archive (keeps
	// the row + history). Purge is intended for test/reset and is fully audited.
	if r.URL.Query().Get("purge") == "true" {
		tx, err := b.DB.Begin(ctx)
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "tx failed")
			return
		}
		defer tx.Rollback(ctx)

		// accounting_records is a TimescaleDB hypertable with the same read-only
		// guard but with NO foreign key to tenants — the tenant cascade never
		// reaches it, so we clear its tenant rows explicitly. Triggers can't be
		// ALTERed on a hypertable, so suppress the guard via replica role (scoped
		// to this tx) instead.
		if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "purge failed: "+err.Error())
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM accounting_records WHERE tenant_id=$1`, id); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "purge failed: "+err.Error())
			return
		}
		if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = origin"); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "purge failed: "+err.Error())
			return
		}

		// Legacy guest-data tables (guests/sessions/vouchers) carry a
		// statement-level `legacy_ro` guard that hard-blocks writes — central no
		// longer owns this data (it moved to the edge). The guard fires even on a
		// cascade DELETE with zero rows, so it would abort the tenant delete
		// below. Disable it for the duration of THIS transaction (DDL is
		// transactional: a rollback reverts the disable too), delete, then
		// re-enable before commit. FK cascade stays on, so deleting the tenant row
		// still cleans every child table.
		legacyGuarded := []string{"guests", "sessions", "vouchers"}
		var toggled []string
		for _, t := range legacyGuarded {
			var has bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=$1::regclass AND tgname='legacy_ro' AND NOT tgisinternal)`,
				t).Scan(&has); err != nil {
				Fail(w, r, http.StatusInternalServerError, CodeInternal, "purge failed: "+err.Error())
				return
			}
			if !has {
				continue
			}
			if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER TABLE %s DISABLE TRIGGER legacy_ro", t)); err != nil {
				Fail(w, r, http.StatusInternalServerError, CodeInternal, "purge failed: "+err.Error())
				return
			}
			toggled = append(toggled, t)
		}

		for _, stmt := range []string{
			`DELETE FROM appliance_security_alerts WHERE appliance_id IN (SELECT id FROM appliances WHERE tenant_id=$1)`,
			`DELETE FROM appliance_signed_assignments WHERE appliance_id IN (SELECT id FROM appliances WHERE tenant_id=$1)`,
			`DELETE FROM appliance_certificates WHERE appliance_id IN (SELECT id FROM appliances WHERE tenant_id=$1)`,
			`DELETE FROM appliance_certificate_requests WHERE appliance_id IN (SELECT id FROM appliances WHERE tenant_id=$1)`,
			`DELETE FROM appliance_assignments WHERE tenant_id=$1`,
			`DELETE FROM appliances WHERE tenant_id=$1`,
			`DELETE FROM licenses WHERE tenant_id=$1`,
			`DELETE FROM appliance_bootstrap_tokens WHERE tenant_id=$1`,
			`DELETE FROM tenant_subscriptions WHERE tenant_id=$1`,
			`DELETE FROM tenant_limit_overrides WHERE tenant_id=$1`,
			`DELETE FROM sites WHERE tenant_id=$1`,
			`DELETE FROM tenants WHERE id=$1`,
		} {
			if _, err := tx.Exec(ctx, stmt, id); err != nil {
				Fail(w, r, http.StatusInternalServerError, CodeInternal, "purge failed: "+err.Error())
				return
			}
		}

		// Re-enable the guard before committing so the tables return to
		// read-only for everyone else.
		for _, t := range toggled {
			if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ENABLE TRIGGER legacy_ro", t)); err != nil {
				Fail(w, r, http.StatusInternalServerError, CodeInternal, "purge failed: "+err.Error())
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
			return
		}
		audit.Op(r.Context(), b.DB, r, "tenant.purged", "tenant", id, map[string]any{"_tenant_id": id})
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ct, err := b.DB.Exec(ctx, `UPDATE tenants SET status='archived', updated_at=now() WHERE id = $1`, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "tenant not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "tenant.archived", "tenant", id, map[string]any{"_tenant_id": id})
	w.WriteHeader(http.StatusNoContent)
}
