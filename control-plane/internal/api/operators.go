package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// -----------------------------------------------------------------------------
// Types
// -----------------------------------------------------------------------------

type Operator struct {
	ID          string         `json:"id"`
	TenantID    *string        `json:"tenant_id,omitempty"` // NULL = platform
	Email       string         `json:"email"`
	DisplayName string         `json:"display_name,omitempty"`
	Status      string         `json:"status"`
	Roles       []OperatorRole `json:"roles,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type OperatorRole struct {
	ID       string  `json:"id"`
	Role     string  `json:"role"`
	TenantID *string `json:"tenant_id,omitempty"`
}

type createOpReq struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	// Role to attach on creation. Defaults to "tenant_operator" when
	// tenant-scoped; platform_admins may pass "platform_admin" to create
	// another super user (which ignores tenant_id).
	Role string `json:"role"`
}

type patchOpReq struct {
	DisplayName *string `json:"display_name,omitempty"`
	Status      *string `json:"status,omitempty"`
}

type setPasswordReq struct {
	Password string `json:"password"`
}

type addRoleReq struct {
	Role     string  `json:"role"`
	TenantID *string `json:"tenant_id,omitempty"` // platform_admin only
}

// -----------------------------------------------------------------------------
// Routes
// -----------------------------------------------------------------------------

func (b *Base) OperatorsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", b.listOperators)
	r.Post("/", b.createOperator)
	r.Get("/{id}", b.getOperator)
	r.Patch("/{id}", b.patchOperator)
	r.Delete("/{id}", b.disableOperator)
	r.Post("/{id}/set-password", b.setOperatorPassword)
	r.Post("/{id}/roles", b.addOperatorRole)
	r.Delete("/{id}/roles/{role}", b.removeOperatorRole)
	return r
}

// -----------------------------------------------------------------------------
// Access helpers
// -----------------------------------------------------------------------------

// tenantScopeOrDeny resolves the tenant the caller is operating within.
// Returns "" for a platform_admin operating globally (no ?tenant_id= override).
// For non-super-admin callers, the session's DefaultTenantID is authoritative.
func tenantScopeOrDeny(r *http.Request, w http.ResponseWriter) (string, bool) {
	s := auth.FromContext(r.Context())
	if s == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "not authenticated")
		return "", false
	}
	tenantID := auth.EffectiveTenantID(r)
	// Non-platform callers must have a tenant. Platform callers may or may not.
	if !s.IsSuperAdmin && tenantID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant scope required")
		return "", false
	}
	return tenantID, true
}

// mayManageOperators returns true if the caller may list/create/update
// operators in the given tenant scope. tenantID="" means "global / platform".
func mayManageOperators(r *http.Request, tenantID string) bool {
	s := auth.FromContext(r.Context())
	if s == nil {
		return false
	}
	if s.IsSuperAdmin {
		return true
	}
	if tenantID == "" {
		return false // non-super-admin cannot operate globally
	}
	if s.DefaultTenantID != tenantID {
		return false
	}
	return hasAnyRole(s.Roles, "tenant_admin")
}

// -----------------------------------------------------------------------------
// List / Get
// -----------------------------------------------------------------------------

func (b *Base) listOperators(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantScopeOrDeny(r, w)
	if !ok {
		return
	}
	if !mayManageOperators(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "operators management requires tenant_admin / platform_admin")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	limit := ParseLimit(r, 50, 200)
	curT, curI, err := DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	var tArg, iArg any
	if !curT.IsZero() {
		tArg = curT
	}
	if curI != "" {
		iArg = curI
	}

	// Platform admins without a tenant scope see all operators; otherwise
	// scope by tenant_id matching the operator's primary tenant_id.
	var tenantFilter any
	if tenantID != "" {
		tenantFilter = tenantID
	}

	rows, err := b.DB.Query(ctx, `
        SELECT id, tenant_id::text, email, COALESCE(display_name,''), status, created_at, updated_at
          FROM operators
         WHERE ($1::uuid IS NULL OR tenant_id = $1)
           AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3::uuid))
         ORDER BY created_at DESC, id DESC
         LIMIT $4
    `, tenantFilter, tArg, iArg, limit+1)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []Operator
	for rows.Next() {
		var op Operator
		var tid *string
		if err := rows.Scan(&op.ID, &tid, &op.Email, &op.DisplayName, &op.Status, &op.CreatedAt, &op.UpdatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		op.TenantID = tid
		out = append(out, op)
	}
	meta := ListMeta{}
	if len(out) > limit {
		last := out[limit-1]
		meta.HasMore = true
		meta.Cursor = EncodeCursor(last.CreatedAt, last.ID)
		out = out[:limit]
	}
	WriteList(w, out, meta)
}

func (b *Base) getOperator(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantScopeOrDeny(r, w)
	if !ok {
		return
	}
	if !mayManageOperators(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	op, err := b.loadOperator(ctx, id, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "operator not found")
			return
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, op)
}

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

func (b *Base) createOperator(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantScopeOrDeny(r, w)
	if !ok {
		return
	}
	if !mayManageOperators(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	var req createOpReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "valid email required")
		return
	}
	if len(req.Password) < 10 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "password must be at least 10 characters")
		return
	}
	if req.Role == "" {
		req.Role = "tenant_operator"
	}
	if !isValidRole(req.Role) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "invalid role")
		return
	}
	// Only platform_admin may mint another platform_admin.
	sess := auth.FromContext(r.Context())
	if req.Role == "platform_admin" && !sess.IsSuperAdmin {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "only platform_admin may grant platform_admin")
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Limit only applies to tenant-scoped operators. Platform-scoped
	// operators (req.Role == "platform_admin") bypass per-tenant counts.
	if tenantID != "" && req.Role != "platform_admin" {
		if !EnforceCreateLimit(ctx, b.DB, w, r, tenantID, "max_operators",
			`SELECT count(*) FROM operators WHERE tenant_id = $1 AND status != 'disabled'`,
			tenantID) {
			return
		}
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "hash failed")
		return
	}

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var opTenantArg any
	if req.Role != "platform_admin" && tenantID != "" {
		opTenantArg = tenantID
	}

	var id string
	err = tx.QueryRow(ctx, `
        INSERT INTO operators (tenant_id, email, display_name, password_hash, status)
        VALUES ($1, $2, NULLIF($3,''), $4, 'active')
        RETURNING id
    `, opTenantArg, req.Email, req.DisplayName, hash).Scan(&id)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "email conflict or insert failed")
		return
	}

	// Role assignment: platform_admin uses NULL tenant; everything else uses
	// the operator's tenant.
	var roleTenantArg any
	if req.Role != "platform_admin" {
		roleTenantArg = tenantID
	}
	_, err = tx.Exec(ctx, `
        INSERT INTO operator_roles (operator_id, tenant_id, role)
        SELECT $1, $2, $3
         WHERE NOT EXISTS (
             SELECT 1 FROM operator_roles
              WHERE operator_id = $1
                AND COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
                AND role = $3
         )
    `, id, roleTenantArg, req.Role)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "role insert failed")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	op, err := b.loadOperator(r.Context(), id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "post-commit load failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "operator.created", "operator", op.ID, map[string]any{
		"_tenant_id": tenantID, "email": op.Email, "role": req.Role,
	})
	WriteJSON(w, http.StatusCreated, op)
}

// -----------------------------------------------------------------------------
// Patch
// -----------------------------------------------------------------------------

func (b *Base) patchOperator(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantScopeOrDeny(r, w)
	if !ok {
		return
	}
	if !mayManageOperators(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	id := chi.URLParam(r, "id")
	var req patchOpReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if req.Status != nil && *req.Status != "" {
		switch *req.Status {
		case "active", "disabled", "invited":
			// ok
		default:
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "invalid status")
			return
		}
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	var nameArg, statusArg any
	if req.DisplayName != nil {
		nameArg = *req.DisplayName
	}
	if req.Status != nil {
		statusArg = *req.Status
	}
	ct, err := b.DB.Exec(ctx, `
        UPDATE operators
           SET display_name = COALESCE($3, display_name),
               status       = COALESCE($4, status),
               updated_at   = now()
         WHERE id = $1 AND ($2::uuid IS NULL OR tenant_id = $2)
    `, id, nilIfEmpty(tenantID), nameArg, statusArg)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "operator not found")
		return
	}
	op, err := b.loadOperator(r.Context(), id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "post-update load failed")
		return
	}
	WriteJSON(w, http.StatusOK, op)
}

// disableOperator soft-deletes by setting status='disabled'.
// You cannot disable yourself — prevents accidental lockout.
func (b *Base) disableOperator(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantScopeOrDeny(r, w)
	if !ok {
		return
	}
	if !mayManageOperators(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	id := chi.URLParam(r, "id")
	sess := auth.FromContext(r.Context())
	if sess.OperatorID == id {
		Fail(w, r, http.StatusConflict, CodeConflict, "cannot disable yourself")
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx, `
        UPDATE operators SET status='disabled', updated_at=now()
         WHERE id = $1 AND ($2::uuid IS NULL OR tenant_id = $2)
    `, id, nilIfEmpty(tenantID))
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "operator not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "operator.disabled", "operator", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// Password / Roles
// -----------------------------------------------------------------------------

func (b *Base) setOperatorPassword(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantScopeOrDeny(r, w)
	if !ok {
		return
	}
	if !mayManageOperators(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	id := chi.URLParam(r, "id")
	var req setPasswordReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if len(req.Password) < 10 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "password must be at least 10 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "hash failed")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx, `
        UPDATE operators SET password_hash=$3, updated_at=now()
         WHERE id=$1 AND ($2::uuid IS NULL OR tenant_id = $2)
    `, id, nilIfEmpty(tenantID), hash)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "operator not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "operator.password_reset", "operator", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

func (b *Base) addOperatorRole(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantScopeOrDeny(r, w)
	if !ok {
		return
	}
	if !mayManageOperators(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	id := chi.URLParam(r, "id")
	var req addRoleReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if !isValidRole(req.Role) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "invalid role")
		return
	}
	sess := auth.FromContext(r.Context())
	if req.Role == "platform_admin" && !sess.IsSuperAdmin {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "only platform_admin may grant platform_admin")
		return
	}
	// Scope: platform_admin role implies NULL tenant.
	var roleTenant any
	switch {
	case req.Role == "platform_admin":
		roleTenant = nil
	case req.TenantID != nil && *req.TenantID != "":
		if !sess.IsSuperAdmin {
			Fail(w, r, http.StatusForbidden, CodeForbidden, "cross-tenant role requires platform_admin")
			return
		}
		roleTenant = *req.TenantID
	case tenantID != "":
		roleTenant = tenantID
	default:
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant_id required for tenant-scoped roles")
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Confirm the target operator exists and is visible to the caller.
	var opTenant *string
	if err := b.DB.QueryRow(ctx, `SELECT tenant_id::text FROM operators WHERE id=$1`, id).Scan(&opTenant); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "operator not found")
			return
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	if !sess.IsSuperAdmin {
		if opTenant == nil || *opTenant != tenantID {
			Fail(w, r, http.StatusForbidden, CodeForbidden, "operator not in your tenant")
			return
		}
	}

	var roleID string
	err := b.DB.QueryRow(ctx, `
        WITH ins AS (
            INSERT INTO operator_roles (operator_id, tenant_id, role)
            SELECT $1, $2, $3
             WHERE NOT EXISTS (
                 SELECT 1 FROM operator_roles
                  WHERE operator_id = $1
                    AND COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
                    AND role = $3
             )
            RETURNING id
        )
        SELECT id FROM ins
        UNION ALL
        SELECT id FROM operator_roles
         WHERE operator_id = $1
           AND COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
           AND role = $3
         LIMIT 1
    `, id, roleTenant, req.Role).Scan(&roleID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "role upsert failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "operator.role_added", "operator", id, map[string]any{
		"_tenant_id": tenantID, "role": req.Role,
	})
	WriteJSON(w, http.StatusCreated, OperatorRole{ID: roleID, Role: req.Role, TenantID: ptrStr(roleTenant)})
}

func (b *Base) removeOperatorRole(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantScopeOrDeny(r, w)
	if !ok {
		return
	}
	if !mayManageOperators(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	id := chi.URLParam(r, "id")
	role := chi.URLParam(r, "role")
	if !isValidRole(role) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "invalid role")
		return
	}
	sess := auth.FromContext(r.Context())

	// Safety: an operator cannot remove their own platform_admin role.
	if sess.OperatorID == id && role == "platform_admin" {
		Fail(w, r, http.StatusConflict, CodeConflict, "cannot remove your own platform_admin role")
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Determine scope: platform_admin role is tenant-null; others tied to
	// the caller's tenant scope (unless platform_admin overrides).
	var roleTenant any
	if role != "platform_admin" {
		if tenantID != "" {
			roleTenant = tenantID
		} else {
			// platform caller removing a tenant role — require ?tenant_id=
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant_id required for tenant-scoped role removal")
			return
		}
	}

	ct, err := b.DB.Exec(ctx, `
        DELETE FROM operator_roles
         WHERE operator_id = $1
           AND role = $2
           AND COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid)
             = COALESCE($3::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
    `, id, role, roleTenant)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "role assignment not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "operator.role_removed", "operator", id, map[string]any{
		"_tenant_id": tenantID, "role": role,
	})
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// Internals
// -----------------------------------------------------------------------------

func (b *Base) loadOperator(parentCtx context.Context, id, tenantID string) (*Operator, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 5*time.Second)
	defer cancel()

	var op Operator
	var tid *string
	err := b.DB.QueryRow(ctx, `
        SELECT id, tenant_id::text, email, COALESCE(display_name,''), status, created_at, updated_at
          FROM operators
         WHERE id = $1 AND ($2::uuid IS NULL OR tenant_id = $2)
    `, id, nilIfEmpty(tenantID)).Scan(&op.ID, &tid, &op.Email, &op.DisplayName, &op.Status, &op.CreatedAt, &op.UpdatedAt)
	if err != nil {
		return nil, err
	}
	op.TenantID = tid

	rows, err := b.DB.Query(ctx, `
        SELECT id, role, tenant_id::text
          FROM operator_roles WHERE operator_id = $1 ORDER BY role
    `, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var or OperatorRole
		var rtid *string
		if err := rows.Scan(&or.ID, &or.Role, &rtid); err != nil {
			return nil, err
		}
		or.TenantID = rtid
		op.Roles = append(op.Roles, or)
	}
	return &op, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func ptrStr(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

func isValidRole(r string) bool {
	switch r {
	case "platform_admin", "tenant_admin", "tenant_operator", "viewer", "billing":
		return true
	}
	return false
}
