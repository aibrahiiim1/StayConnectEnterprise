package main

// Local operator management. Ported from control-plane operators.go with the
// tenant/platform scoping removed: every operator on the appliance belongs to
// this site, and roles live in operator_roles with tenant_id NULL.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type edgeOperator struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	Roles       []string  `json:"roles"`
	CreatedAt   time.Time `json:"created_at"`
}

// isValidEdgeRole accepts site_admin plus every role in the rolePerms matrix
// (which includes the legacy tenant_* roles kept for migrated operators).
func isValidEdgeRole(role string) bool {
	if role == "site_admin" {
		return true
	}
	_, ok := rolePerms[role]
	return ok
}

func (s *server) operatorsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listEdgeOperators)
	r.Post("/", s.createEdgeOperator)
	r.Delete("/{id}", s.disableEdgeOperator)
	r.Post("/{id}/set-password", s.setEdgeOperatorPassword)
	r.Post("/{id}/roles", s.addEdgeOperatorRole)
	r.Delete("/{id}/roles/{role}", s.removeEdgeOperatorRole)
	return r
}

func (s *server) loadEdgeOperator(ctx context.Context, id string) (*edgeOperator, error) {
	var op edgeOperator
	err := s.db.QueryRow(ctx, `
        SELECT o.id, o.email, COALESCE(o.display_name,''), o.status, o.created_at,
               COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'::text[])
          FROM operators o
          LEFT JOIN operator_roles r ON r.operator_id = o.id
         WHERE o.id = $1
         GROUP BY o.id
    `, id).Scan(&op.ID, &op.Email, &op.DisplayName, &op.Status, &op.CreatedAt, &op.Roles)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func (s *server) listEdgeOperators(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
        SELECT o.id, o.email, COALESCE(o.display_name,''), o.status, o.created_at,
               COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'::text[])
          FROM operators o
          LEFT JOIN operator_roles r ON r.operator_id = o.id
         GROUP BY o.id
         ORDER BY o.created_at DESC, o.id DESC
    `)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeOperator
	for rows.Next() {
		var op edgeOperator
		if err := rows.Scan(&op.ID, &op.Email, &op.DisplayName, &op.Status, &op.CreatedAt, &op.Roles); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, op)
	}
	writeList(w, out)
}

func (s *server) createEdgeOperator(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" || !strings.Contains(in.Email, "@") {
		jsonErr(w, http.StatusBadRequest, "bad_request", "valid email required")
		return
	}
	if len(in.Password) < 10 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "password must be at least 10 characters")
		return
	}
	if in.Role == "" {
		in.Role = "site_viewer"
	}
	if !isValidEdgeRole(in.Role) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid role")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	if !s.enforceLimit(ctx, w, "max_operators", 1,
		`SELECT count(*) FROM operators WHERE status != 'disabled'`) {
		return
	}

	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx, `
        INSERT INTO operators (email, display_name, password_hash, status)
        VALUES ($1, NULLIF($2,''), $3, 'active')
        RETURNING id
    `, in.Email, in.DisplayName, hash).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "email already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO operator_roles (operator_id, tenant_id, role)
        VALUES ($1, NULL, $2)
    `, id, in.Role); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "role insert failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}

	op, err := s.loadEdgeOperator(ctx, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "post-commit load failed")
		return
	}
	s.audit(r, "operator.created", "operator", id, map[string]any{"email": in.Email, "role": in.Role})
	writeJSON(w, http.StatusCreated, op)
}

// disableEdgeOperator soft-deletes: status='disabled' and every live session
// is destroyed. Self-disable is refused to avoid locking yourself out.
func (s *server) disableEdgeOperator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if sess := sessFrom(r.Context()); sess != nil && sess.OperatorID == id {
		jsonErr(w, http.StatusConflict, "conflict", "cannot disable yourself")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	ct, err := s.db.Exec(ctx,
		`UPDATE operators SET status='disabled', updated_at=now() WHERE id = $1`, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	s.sessions.destroyOperator(id)
	s.audit(r, "operator.disabled", "operator", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) setEdgeOperatorPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if len(in.Password) < 10 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "password must be at least 10 characters")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	ct, err := s.db.Exec(ctx,
		`UPDATE operators SET password_hash=$2, updated_at=now() WHERE id = $1`, id, hash)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	s.audit(r, "operator.password_reset", "operator", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) addEdgeOperatorRole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if !isValidEdgeRole(in.Role) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid role")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM operators WHERE id = $1)`, id).Scan(&exists); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if !exists {
		jsonErr(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}

	if _, err := s.db.Exec(ctx, `
        INSERT INTO operator_roles (operator_id, tenant_id, role)
        SELECT $1, NULL, $2
         WHERE NOT EXISTS (SELECT 1 FROM operator_roles WHERE operator_id = $1 AND role = $2)
    `, id, in.Role); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "role insert failed")
		return
	}
	// Cached session roles are stale now; force a re-login for that operator.
	s.sessions.destroyOperator(id)
	s.audit(r, "operator.role_added", "operator", id, map[string]any{"role": in.Role})
	writeJSON(w, http.StatusCreated, map[string]string{"operator_id": id, "role": in.Role})
}

func (s *server) removeEdgeOperatorRole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	role := chi.URLParam(r, "role")
	if !isValidEdgeRole(role) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid role")
		return
	}
	if sess := sessFrom(r.Context()); sess != nil && sess.OperatorID == id && role == "site_admin" {
		jsonErr(w, http.StatusConflict, "conflict", "cannot remove your own site_admin role")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	ct, err := s.db.Exec(ctx,
		`DELETE FROM operator_roles WHERE operator_id = $1 AND role = $2`, id, role)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if ct.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "role assignment not found")
		return
	}
	// Permissions shrank — kill any cached sessions so the change bites now.
	s.sessions.destroyOperator(id)
	s.audit(r, "operator.role_removed", "operator", id, map[string]any{"role": role})
	w.WriteHeader(http.StatusNoContent)
}
