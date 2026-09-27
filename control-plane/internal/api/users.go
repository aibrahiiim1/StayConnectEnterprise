package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// CENTRAL SIGN-INS: the Team (Central's own operators, platform roles, no customer) and each customer's
// users (customer roles, bound to that customer). Both are rows in operators + operator_roles.
//
// A user holds exactly one Central role here. Legacy roles still present in the database are shown as they
// are but grant nothing (§7) and cannot be assigned.

// UserRow is a Team member or a customer user.
type UserRow struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	Role        string    `json:"role"`
	Roles       []string  `json:"roles"`
	CustomerID  *string   `json:"customer_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// queryUsers lists users. customerID "" = the Team (operators holding a platform role).
func (b *Base) queryUsers(ctx context.Context, customerID, userID string) ([]UserRow, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT o.id::text, o.email, COALESCE(o.display_name,''), o.status, o.tenant_id::text, o.created_at,
               COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}')
          FROM operators o
          LEFT JOIN operator_roles r ON r.operator_id = o.id
         WHERE ($2 = '' OR o.id::text = $2)
           AND CASE WHEN $1 = ''
                    THEN EXISTS (SELECT 1 FROM operator_roles p WHERE p.operator_id = o.id AND p.tenant_id IS NULL
                                    AND p.role = ANY($3))
                    ELSE (o.tenant_id::text = $1 OR EXISTS (SELECT 1 FROM operator_roles p
                                    WHERE p.operator_id = o.id AND p.tenant_id::text = $1))
               END
         GROUP BY o.id
         ORDER BY lower(o.email)`, customerID, userID, auth.PlatformRoles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Status, &u.CustomerID, &u.CreatedAt, &u.Roles); err != nil {
			return nil, err
		}
		for _, role := range u.Roles {
			if (customerID == "" && auth.IsPlatformRole(role)) || (customerID != "" && auth.IsCustomerRole(role)) {
				u.Role = role
				break
			}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (b *Base) writeUser(w http.ResponseWriter, r *http.Request, code int, customerID, id string) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.queryUsers(ctx, customerID, id)
	if err != nil || len(rows) == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "user not found")
		return
	}
	WriteJSON(w, code, rows[0])
}

type userWrite struct {
	Email       string   `json:"email"`
	DisplayName *string  `json:"display_name"`
	Password    string   `json:"password"`
	Role        string   `json:"role"`
	Roles       []string `json:"roles"` // the console sends {roles:[role]}; a user holds exactly one
	Status      string   `json:"status"`
}

// normalizeRole folds roles:[x] into role. More than one role is refused.
func (u *userWrite) normalizeRole() bool {
	switch len(u.Roles) {
	case 0:
		return true
	case 1:
		if u.Role != "" && u.Role != u.Roles[0] {
			return false
		}
		u.Role = u.Roles[0]
		return true
	}
	return false
}

// mayGrant decides who may hand out a role: only a platform_owner creates another platform_owner, and only a
// platform role or a tenant_owner creates a tenant_owner.
func mayGrant(s *auth.Session, role string) bool {
	switch role {
	case "platform_owner":
		return hasRole(s, "platform_owner")
	case "tenant_owner":
		return s.HasPermission(auth.PermUsersManage) || hasRole(s, "tenant_owner")
	}
	return true
}

func hasRole(s *auth.Session, role string) bool {
	if s == nil {
		return false
	}
	for _, r := range s.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// createUser inserts an operator with exactly one role. customerID "" = a Team member.
func (b *Base) createUser(w http.ResponseWriter, r *http.Request, customerID string) {
	var in userWrite
	if err := DecodeJSON(r, &in); err != nil || !in.normalizeRole() {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body (a user holds exactly one role)")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Email == "" || !strings.Contains(in.Email, "@") {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "a valid email is required")
		return
	}
	if len(in.Password) < 10 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "password must be at least 10 characters")
		return
	}
	if (customerID == "" && !auth.IsPlatformRole(in.Role)) || (customerID != "" && !auth.IsCustomerRole(in.Role)) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "role is not valid here")
		return
	}
	if !mayGrant(auth.FromContext(r.Context()), in.Role) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "you may not grant "+in.Role)
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "hash failed")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin failed")
		return
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `
        INSERT INTO operators (tenant_id, email, display_name, password_hash, status)
        VALUES (NULLIF($1,'')::uuid, $2, NULLIF($3,''), $4, 'active') RETURNING id::text`,
		customerID, in.Email, strings.TrimSpace(deref(in.DisplayName)), hash).Scan(&id); err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "a user with that email already exists")
		return
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operator_roles (operator_id, tenant_id, role) VALUES ($1, NULLIF($2,'')::uuid, $3)`,
		id, customerID, in.Role); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "role insert failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.created", "operator", id, map[string]any{
		"_tenant_id": customerID, "email": in.Email, "role": in.Role})
	b.writeUser(w, r, http.StatusCreated, customerID, id)
}

// updateUser changes display name, status, role and/or password.
func (b *Base) updateUser(w http.ResponseWriter, r *http.Request, customerID, id string) {
	var in userWrite
	if err := DecodeJSON(r, &in); err != nil || !in.normalizeRole() {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body (a user holds exactly one role)")
		return
	}
	if in.Email != "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "email cannot be changed")
		return
	}
	s := auth.FromContext(r.Context())
	if in.Status != "" && in.Status != "active" && in.Status != "disabled" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "status must be active or disabled")
		return
	}
	if in.Status == "disabled" && s != nil && s.OperatorID == id {
		Fail(w, r, http.StatusConflict, CodeConflict, "you cannot disable yourself")
		return
	}
	if in.Role != "" {
		if (customerID == "" && !auth.IsPlatformRole(in.Role)) || (customerID != "" && !auth.IsCustomerRole(in.Role)) {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "role is not valid here")
			return
		}
		if !mayGrant(s, in.Role) {
			Fail(w, r, http.StatusForbidden, CodeForbidden, "you may not grant "+in.Role)
			return
		}
		if s != nil && s.OperatorID == id {
			Fail(w, r, http.StatusConflict, CodeConflict, "you cannot change your own role")
			return
		}
	}
	if in.Password != "" && len(in.Password) < 10 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "password must be at least 10 characters")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	cur, err := b.queryUsers(ctx, customerID, id)
	if err != nil || len(cur) == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "user not found")
		return
	}
	// Changing an owner requires being allowed to grant owner.
	if !mayGrant(s, cur[0].Role) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "you may not change a "+cur[0].Role)
		return
	}
	demoted := in.Role != "" && in.Role != "platform_owner" && in.Role != "platform_admin"
	if customerID == "" && (in.Status == "disabled" || demoted) && b.isLastAdministrator(ctx, id) {
		Fail(w, r, http.StatusConflict, CodeConflict, "this is the last active platform administrator")
		return
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin failed")
		return
	}
	defer tx.Rollback(ctx)
	if in.DisplayName != nil || in.Status != "" {
		if _, err := tx.Exec(ctx, `
            UPDATE operators SET display_name = CASE WHEN $2 THEN NULLIF(btrim($3),'') ELSE display_name END,
                                 status = COALESCE(NULLIF($4,''), status), updated_at = now()
             WHERE id = $1`, id, in.DisplayName != nil, deref(in.DisplayName), in.Status); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
			return
		}
	}
	if in.Password != "" {
		hash, err := auth.HashPassword(in.Password)
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "hash failed")
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE operators SET password_hash=$2, updated_at=now() WHERE id=$1`, id, hash); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
			return
		}
	}
	if in.Role != "" && in.Role != cur[0].Role {
		roles := auth.CustomerRoles
		if customerID == "" {
			roles = auth.PlatformRoles
		}
		if _, err := tx.Exec(ctx, `DELETE FROM operator_roles WHERE operator_id=$1 AND role = ANY($2)
                                   AND COALESCE(tenant_id::text,'') = $3`, id, roles, customerID); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "role update failed")
			return
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operator_roles (operator_id, tenant_id, role) VALUES ($1, NULLIF($2,'')::uuid, $3)`,
			id, customerID, in.Role); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "role update failed")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.updated", "operator", id, map[string]any{
		"_tenant_id": customerID, "role": in.Role, "status": in.Status, "password_changed": in.Password != ""})
	b.writeUser(w, r, http.StatusOK, customerID, id)
}

// removeUser is DELETE: the sign-in is removed. The audit log keeps what the user did (it records the actor
// id and has no foreign key); licences they issued keep their history with created_by cleared.
func (b *Base) removeUser(w http.ResponseWriter, r *http.Request, customerID, id string) {
	if s := auth.FromContext(r.Context()); s != nil && s.OperatorID == id {
		Fail(w, r, http.StatusConflict, CodeConflict, "you cannot remove yourself")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	cur, err := b.queryUsers(ctx, customerID, id)
	if err != nil || len(cur) == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "user not found")
		return
	}
	if !mayGrant(auth.FromContext(r.Context()), cur[0].Role) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "you may not remove a "+cur[0].Role)
		return
	}
	if customerID == "" && b.isLastAdministrator(ctx, id) {
		Fail(w, r, http.StatusConflict, CodeConflict, "this is the last active platform administrator")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.removed", "operator", id, map[string]any{
		"_tenant_id": customerID, "email": cur[0].Email, "role": cur[0].Role})
	if _, err := b.DB.Exec(ctx, `DELETE FROM operators WHERE id=$1`, id); err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "remove failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isLastAdministrator reports whether id is the only ACTIVE operator holding platform_owner or
// platform_admin — removing, disabling or demoting it would leave nobody able to run Central.
func (b *Base) isLastAdministrator(ctx context.Context, id string) bool {
	var others, self int
	_ = b.DB.QueryRow(ctx, `
        SELECT count(DISTINCT o.id) FILTER (WHERE o.id::text <> $1),
               count(DISTINCT o.id) FILTER (WHERE o.id::text = $1)
          FROM operators o JOIN operator_roles r ON r.operator_id = o.id
         WHERE o.status = 'active' AND r.tenant_id IS NULL AND r.role IN ('platform_owner','platform_admin')`, id).
		Scan(&others, &self)
	return self > 0 && others == 0
}

// ---- Team: /cloud/v1/team ----

func (b *Base) listTeam(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.queryUsers(ctx, "", "")
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (b *Base) postTeam(w http.ResponseWriter, r *http.Request) { b.createUser(w, r, "") }
func (b *Base) patchTeam(w http.ResponseWriter, r *http.Request) {
	b.updateUser(w, r, "", chi.URLParam(r, "id"))
}
func (b *Base) deleteTeam(w http.ResponseWriter, r *http.Request) {
	b.removeUser(w, r, "", chi.URLParam(r, "id"))
}

// teamPassword: POST /cloud/v1/team/{id}/password {password}
func (b *Base) teamPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := DecodeJSON(r, &in); err != nil || len(in.Password) < 10 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "password must be at least 10 characters")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	cur, err := b.queryUsers(ctx, "", id)
	if err != nil || len(cur) == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "user not found")
		return
	}
	if !mayGrant(auth.FromContext(r.Context()), cur[0].Role) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "you may not change a "+cur[0].Role)
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "hash failed")
		return
	}
	if _, err := b.DB.Exec(ctx, `UPDATE operators SET password_hash=$2, updated_at=now() WHERE id=$1`, id, hash); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.password_reset", "operator", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Customer users: /cloud/v1/customers/{id}/users ----

func (b *Base) listCustomerUsers(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !scope.Allows(id) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "customer not found")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.queryUsers(ctx, id, "")
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (b *Base) customerExists(w http.ResponseWriter, r *http.Request, id string) bool {
	ctx, cancel := DBCtx(r)
	defer cancel()
	var n int
	if err := b.DB.QueryRow(ctx, `SELECT count(*) FROM tenants WHERE id::text=$1`, id).Scan(&n); err != nil || n == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "customer not found")
		return false
	}
	return true
}

func (b *Base) postCustomerUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !requireCustomerManage(w, r, id, auth.PermUsersManage, auth.PermCustomerUsersManage) || !b.customerExists(w, r, id) {
		return
	}
	b.createUser(w, r, id)
}

func (b *Base) patchCustomerUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !requireCustomerManage(w, r, id, auth.PermUsersManage, auth.PermCustomerUsersManage) {
		return
	}
	b.updateUser(w, r, id, chi.URLParam(r, "uid"))
}

func (b *Base) deleteCustomerUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !requireCustomerManage(w, r, id, auth.PermUsersManage, auth.PermCustomerUsersManage) {
		return
	}
	b.removeUser(w, r, id, chi.URLParam(r, "uid"))
}
