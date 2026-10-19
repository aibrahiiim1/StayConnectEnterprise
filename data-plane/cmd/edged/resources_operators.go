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
	// MustChangePassword: the operator's next sign-in can only set a new password (0119).
	MustChangePassword bool `json:"must_change_password"`
	// TwoStepEnrolled: an authenticator is set up (0132). Without one, the next sign-in sets one up.
	TwoStepEnrolled bool `json:"two_step_enrolled"`
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
	// Admin Console sign-in protection (0119): read with the operators list, changed by whoever may change
	// operators, under step-up. Registered before /{id} so the literal path wins.
	r.Get("/signin-protection", s.getSigninProtection)
	r.Put("/signin-protection", s.setSigninProtection)
	r.Get("/signin-protection/changes", s.listSigninProtectionChanges)
	// Admin Console session limits (0132): idle and absolute, the same rules as sign-in protection.
	r.Get("/session-policy", s.getSessionPolicy)
	r.Put("/session-policy", s.setSessionPolicy)
	r.Get("/session-policy/changes", s.listSessionPolicyChanges)
	// How long a disabled account is kept before it is anonymised (0138, GDPR audit B4).
	r.Get("/account-retention", s.getAccountRetention)
	r.Put("/account-retention", s.setAccountRetention)
	r.Post("/", s.createEdgeOperator)
	r.Delete("/{id}", s.disableEdgeOperator)
	r.Post("/{id}/set-password", s.setEdgeOperatorPassword)
	r.Post("/{id}/reset-mfa", s.resetEdgeOperatorMFA)
	r.Post("/{id}/roles", s.addEdgeOperatorRole)
	r.Delete("/{id}/roles/{role}", s.removeEdgeOperatorRole)
	return r
}

// confirmYourPassword is the step-up for changing a staff account (GDPR audit B7): creating one, resetting its
// password, disabling it, and adding or removing a role all ask the administrator for their own password first.
// The field is your_password because "password" in these bodies is the colleague's new one.
func (s *server) confirmYourPassword(w http.ResponseWriter, r *http.Request, yours string) bool {
	if !s.reauth(r, yours) {
		s.reauthRefused(w, r, "your password is required to change a staff account")
		return false
	}
	return true
}

func (s *server) loadEdgeOperator(ctx context.Context, id string) (*edgeOperator, error) {
	var op edgeOperator
	err := s.db.QueryRow(ctx, `
        SELECT o.id, o.email, COALESCE(o.display_name,''), o.status, o.created_at,
               COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'::text[]),
               o.must_change_password
          FROM operators o
          LEFT JOIN operator_roles r ON r.operator_id = o.id
         WHERE o.id = $1
         GROUP BY o.id
    `, id).Scan(&op.ID, &op.Email, &op.DisplayName, &op.Status, &op.CreatedAt, &op.Roles, &op.MustChangePassword)
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
               COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'::text[]),
               o.must_change_password, (SELECT m.enrolled FROM public.operator_mfa_status(o.id) m)
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
		if err := rows.Scan(&op.ID, &op.Email, &op.DisplayName, &op.Status, &op.CreatedAt, &op.Roles, &op.MustChangePassword,
			&op.TwoStepEnrolled); err != nil {
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
		// RequireChange: the new operator must choose their own password at first sign-in (0119).
		RequireChange bool   `json:"require_change"`
		YourPassword  string `json:"your_password"`
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
	if msg := operatorPasswordProblem(in.Password, in.Email); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	if in.Role == "" {
		in.Role = "site_viewer"
	}
	if !isValidEdgeRole(in.Role) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid role")
		return
	}
	if !s.confirmYourPassword(w, r, in.YourPassword) {
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
        INSERT INTO operators (email, display_name, password_hash, status, must_change_password)
        VALUES ($1, NULLIF($2,''), $3, 'active', $4)
        RETURNING id
    `, in.Email, in.DisplayName, hash, in.RequireChange).Scan(&id)
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
	s.audit(r, "operator.created", "operator", id, map[string]any{"email": in.Email, "role": in.Role,
		"must_change_password": in.RequireChange})
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
	var in struct {
		YourPassword string `json:"your_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if !s.confirmYourPassword(w, r, in.YourPassword) {
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

// setEdgeOperatorPassword is an ADMINISTRATOR resetting somebody else's password (N-8). It ends every live
// session of that operator -- a reset is how a suspected compromise is closed, and a session opened with the
// old password must not outlive it -- and ends the account's sign-in lockout. require_change makes the operator
// choose their own password at the next sign-in. An operator changes their OWN password at POST
// /edge/v1/auth/password, which asks for the current one; resetting yourself here would skip that.
func (s *server) setEdgeOperatorPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password      string `json:"password"`
		RequireChange bool   `json:"require_change"`
		YourPassword  string `json:"your_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if sess := sessFrom(r.Context()); sess != nil && sess.OperatorID == id {
		jsonErr(w, http.StatusConflict, "use_change_password",
			"Change your own password with Change my password, which asks for your current password.")
		return
	}
	if !s.confirmYourPassword(w, r, in.YourPassword) {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var email string
	if err := s.db.QueryRow(ctx, `SELECT email FROM operators WHERE id = $1`, id).Scan(&email); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	if msg := operatorPasswordProblem(in.Password, email); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}
	ct, err := s.db.Exec(ctx,
		`UPDATE operators SET password_hash=$2, must_change_password=$3, updated_at=now() WHERE id = $1`,
		id, hash, in.RequireChange)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	s.sessions.destroyOperator(id)
	_ = s.throttleClear(ctx, scopeAccount, strings.ToLower(strings.TrimSpace(email)))
	_ = s.throttleClear(ctx, scopeReauth, id)
	s.audit(r, "operator.password_reset", "operator", id, map[string]any{
		"sessions_ended": true, "must_change_password": in.RequireChange,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) addEdgeOperatorRole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Role         string `json:"role"`
		YourPassword string `json:"your_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if !isValidEdgeRole(in.Role) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid role")
		return
	}
	if !s.confirmYourPassword(w, r, in.YourPassword) {
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
	var in struct {
		YourPassword string `json:"your_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if !s.confirmYourPassword(w, r, in.YourPassword) {
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
