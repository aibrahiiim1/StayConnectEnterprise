package api

import (
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
	// Archive is the NORMAL retire action (soft, reversible). Restore reverses it.
	r.Post("/{id}/archive", b.archiveTenant)
	r.Post("/{id}/restore", b.restoreTenant)
	// Permanent delete is empty-only, step-up gated, typed-name + reason confirmed.
	r.With(RequireReauth(b.Redis)).Delete("/{id}", b.deleteTenantSafe)

	// Subscription + limits endpoints. tenantID param name matches the
	// shared handlers in subscriptions.go.
	r.Get("/{tenantID}/subscription", b.getSubscription)
	r.Post("/{tenantID}/subscription", b.changeSubscription)
	r.With(RequireReauth(b.Redis)).Post("/{tenantID}/subscription/cancel", b.cancelSubscription)
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

// archiveTenant soft-deletes by setting status='archived'. This is the NORMAL
// action for a Customer that is no longer active: it hides the record from
// active lists but keeps every Site, Appliance, License and audit record intact.
func (b *Base) archiveTenant(w http.ResponseWriter, r *http.Request) {
	sess := auth.FromContext(r.Context())
	if !sess.IsSuperAdmin {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "platform_admin only")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx, `UPDATE tenants SET status='archived', updated_at=now() WHERE id=$1 AND status <> 'archived'`, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		// Either not found or already archived — distinguish for a clean UX.
		var exists bool
		_ = b.DB.QueryRow(ctx, `SELECT true FROM tenants WHERE id=$1`, id).Scan(&exists)
		if !exists {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "tenant not found")
			return
		}
	}
	audit.Op(r.Context(), b.DB, r, "tenant.archived", "tenant", id, map[string]any{"_tenant_id": id})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "archived", "id": id})
}

// restoreTenant returns an archived Customer to active status.
func (b *Base) restoreTenant(w http.ResponseWriter, r *http.Request) {
	sess := auth.FromContext(r.Context())
	if !sess.IsSuperAdmin {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "platform_admin only")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx, `UPDATE tenants SET status='active', updated_at=now() WHERE id=$1`, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "tenant not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "tenant.restored", "tenant", id, map[string]any{"_tenant_id": id})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "active", "id": id})
}

// deleteTenantSafe PERMANENTLY deletes a Customer, but ONLY when it is completely
// empty. It NEVER cascades a subtree away: while any Site, Appliance, License,
// active Subscription or live Enrollment Token exists the delete is rejected with
// a 409 listing exactly what must be removed first (in deletion order). The DB
// ownership RESTRICT constraints (migration 0037) are the final backstop if a
// caller bypasses this check.
//
// Requires super-admin, a password step-up (route middleware), a typed name
// confirmation and a reason — all recorded in the immutable audit log, which has
// no foreign key and therefore survives the deletion.
func (b *Base) deleteTenantSafe(w http.ResponseWriter, r *http.Request) {
	sess := auth.FromContext(r.Context())
	if sess == nil || !sess.IsSuperAdmin {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "platform_admin only")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	var t Tenant
	err := b.DB.QueryRow(ctx, `SELECT id, slug, name, status FROM tenants WHERE id=$1`, id).
		Scan(&t.ID, &t.Slug, &t.Name, &t.Status)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "tenant not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}

	var req deleteConfirm
	_ = DecodeJSON(r, &req)
	if req.Confirm != t.Name && req.Confirm != t.Slug {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest,
			"type the exact Customer name to confirm permanent deletion",
			map[string]any{"expected": t.Name})
		return
	}

	blockers, err := b.tenantBlockers(ctx, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "dependency check failed: "+err.Error())
		return
	}
	if len(blockers) > 0 {
		failBlocked(w, r, "Customer", blockers)
		return
	}

	// Empty customer: permanent delete. Record the audit event BEFORE the delete
	// so it is durable even though audit_log has no FK to the tenant.
	audit.Op(r.Context(), b.DB, r, "tenant.deleted", "tenant", id, map[string]any{
		"_tenant_id": id, "slug": t.Slug, "name": t.Name, "reason": req.Reason,
	})

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	// Deleting the tenant row cascades to guest-data tables (guests/sessions/
	// vouchers) whose statement-level read-only guard fires even at zero rows;
	// disable it for the tx, then re-enable before commit.
	toggled, err := disableLegacyGuards(ctx, tx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed: "+err.Error())
		return
	}
	// Clear tenant-scoped leaf rows that block the RESTRICT edges but carry no
	// standalone meaning for an empty customer (canceled subs, spent tokens,
	// limit overrides). Sites/appliances/licenses are guaranteed absent above.
	for _, stmt := range []string{
		`DELETE FROM appliance_bootstrap_tokens WHERE tenant_id=$1`,
		`DELETE FROM tenant_subscriptions WHERE tenant_id=$1`,
		`DELETE FROM tenant_limit_overrides WHERE tenant_id=$1`,
		`DELETE FROM tenants WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed: "+err.Error())
			return
		}
	}
	if err := enableLegacyGuards(ctx, tx, toggled); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
