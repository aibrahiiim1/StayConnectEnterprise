package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/mail"
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
	// LastLoginAt is the last successful console sign-in; null when the user never signed in.
	LastLoginAt *time.Time `json:"last_login_at"`
	// TwoStepEnrolled: an authenticator app is set up (0052). Without one, the next sign-in sets one up.
	TwoStepEnrolled bool `json:"two_step_enrolled"`
}

// queryUsers lists users. customerID "" = the Team (operators holding a platform role).
func (b *Base) queryUsers(ctx context.Context, customerID, userID string) ([]UserRow, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT o.id::text, o.email, COALESCE(o.display_name,''), o.status, o.tenant_id::text, o.created_at, o.last_login_at,
               COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'),
               EXISTS (SELECT 1 FROM operator_mfa m WHERE m.operator_id = o.id AND m.confirmed_at IS NOT NULL)
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
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Status, &u.CustomerID, &u.CreatedAt, &u.LastLoginAt, &u.Roles,
			&u.TwoStepEnrolled); err != nil {
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

// validUserEmail is the one check for a Central user's email, at creation and at a correction: a single plain
// address (no display name, no comment, no spaces), with a dotted domain, at most 254 characters. The caller has
// lower-cased and trimmed it.
func validUserEmail(e string) bool {
	if e == "" || len(e) > 254 || strings.ContainsAny(e, " \t\r\n<>\"(),;:\\") {
		return false
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || a.Name != "" {
		return false
	}
	at := strings.LastIndexByte(e, '@')
	domain := e[at+1:]
	return at > 0 && strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".") &&
		!strings.Contains(domain, "..")
}

// createUser inserts an operator with exactly one role. customerID "" = a Team member.
func (b *Base) createUser(w http.ResponseWriter, r *http.Request, customerID string) {
	var in userWrite
	if err := DecodeJSON(r, &in); err != nil || !in.normalizeRole() {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body (a user holds exactly one role)")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !validUserEmail(in.Email) {
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
	// AN EMAIL CAN BE CORRECTED (GDPR audit B5, Art. 16). The history then names the person by the corrected
	// address, so a correction is not a second identity.
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Email != "" && !validUserEmail(in.Email) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "a valid email is required")
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
	emailChanged := in.Email != "" && in.Email != strings.ToLower(cur[0].Email)
	if emailChanged {
		if _, err := tx.Exec(ctx, `UPDATE operators SET email=$2, updated_at=now() WHERE id=$1`, id, in.Email); err != nil {
			if isUniqueViolation(err) {
				Fail(w, r, http.StatusConflict, CodeConflict, "another user already has this email")
				return
			}
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
			return
		}
		// Only what is attributable to this user follows the correction (0056): the who-acted columns holding the old
		// address and the audit entries made by or about them -- never a matching string anywhere else in the
		// platform's history, which a customer's administrator must not be able to rewrite.
		if _, err := tx.Exec(ctx, `SELECT central_correct_actor_email($1::uuid, $2, $3)`, id, cur[0].Email, in.Email); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
			return
		}
	}
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
	// Losing access, changing role or getting a new password ends every console session the user has open:
	// they stop at their next request instead of running out the sliding lifetime on the old access.
	sessionsEnded := in.Status == "disabled" || (in.Role != "" && in.Role != cur[0].Role) || in.Password != "" || emailChanged
	if sessionsEnded {
		if err := auth.EndOperatorSessions(ctx, tx, id); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.updated", "operator", id, map[string]any{
		"_tenant_id": customerID, "role": in.Role, "status": in.Status, "password_changed": in.Password != "",
		"email_changed":  emailChanged,
		"sessions_ended": sessionsEnded})
	b.writeUser(w, r, http.StatusOK, customerID, id)
}

// removeUser is DELETE: the sign-in is removed. The audit log keeps what the user did (it records the actor
// id and has no foreign key); licences they issued keep their history with created_by cleared. Deleting the row
// replaces their email everywhere the history keeps it as text (migration 0055, GDPR audit B5), so the entry
// for the removal itself does not repeat it either.
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
	// Removing the row also ends the user's console sessions: the next request finds no operator
	// (session_policy.go) and the session is destroyed.
	if _, err := b.DB.Exec(ctx, `DELETE FROM operators WHERE id=$1`, id); err != nil {
		slog.Warn("remove user failed", "operator_id", id, "err", err)
		Fail(w, r, http.StatusConflict, CodeConflict, "The user could not be removed. Try again.")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.removed", "operator", id, map[string]any{
		"_tenant_id": customerID, "role": cur[0].Role, "sessions_ended": true})
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
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE operators SET password_hash=$2, updated_at=now() WHERE id=$1`, id, hash); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	// A password reset ends the sessions opened with the old password.
	if err := auth.EndOperatorSessions(ctx, tx, id); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.password_reset", "operator", id, map[string]any{"sessions_ended": true})
	w.WriteHeader(http.StatusNoContent)
}

// teamMFAReset: POST /cloud/v1/team/{id}/mfa-reset {reason}
func (b *Base) teamMFAReset(w http.ResponseWriter, r *http.Request) {
	b.resetUserMFA(w, r, "", chi.URLParam(r, "id"))
}

// customerUserMFAReset: POST /cloud/v1/customers/{id}/users/{uid}/mfa-reset {reason}
func (b *Base) customerUserMFAReset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !requireCustomerManage(w, r, id, auth.PermUsersManage, auth.PermCustomerUsersManage) {
		return
	}
	b.resetUserMFA(w, r, id, chi.URLParam(r, "uid"))
}

// resetUserMFA removes a colleague's authenticator and recovery codes (0052, GDPR plan C10): their sessions end
// and they set up a new authenticator at their next sign-in. Never one's own -- a stolen session plus the
// password must not be enough to remove the second factor -- and with a reason, audited. It also lifts every
// wrong-password lock on their account, from every address (0056, decision D-R1), so a colleague locked out
// can be let back in from the console.
func (b *Base) resetUserMFA(w http.ResponseWriter, r *http.Request, customerID, id string) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil || len(strings.TrimSpace(in.Reason)) < 4 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "a reason of at least 4 characters is required")
		return
	}
	s := auth.FromContext(r.Context())
	if s != nil && s.OperatorID == id {
		Fail(w, r, http.StatusConflict, "not_yourself", "Another administrator must reset your two-step sign-in.")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	cur, err := b.queryUsers(ctx, customerID, id)
	if err != nil || len(cur) == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "user not found")
		return
	}
	if !mayGrant(s, cur[0].Role) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "you may not change a "+cur[0].Role)
		return
	}
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "reset failed")
		return
	}
	defer tx.Rollback(ctx)
	had, err := auth.ResetMFA(ctx, tx, id)
	if err == nil {
		err = auth.EndOperatorSessions(ctx, tx, id)
	}
	var unlocked int64
	if err == nil {
		unlocked, err = auth.ClearSigninLocks(ctx, tx, b.MFA.SigninName(cur[0].Email))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "reset failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.mfa_reset", "operator", id, map[string]any{
		"reason": strings.TrimSpace(in.Reason), "had_authenticator": had, "sessions_ended": true,
		"signin_locks_cleared": unlocked})
	WriteJSON(w, http.StatusOK, map[string]any{"removed": had})
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

// userDataExport: POST /cloud/v1/users/{id}/data-export {reason}. One user's data as a JSON document, for the
// platform to hand to them (GDPR audit B5, Art. 15/20; migration 0055). Platform-only (Team management), with
// the password re-confirmed and a reason, and audited. Works for Team members and customer users alike.
func (b *Base) userDataExport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if len(reason) < 4 || len(reason) > 500 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "a reason of 4-500 characters is required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var doc []byte
	if err := b.DB.QueryRow(ctx, `SELECT central_user_data($1::uuid)::text`, id).Scan(&doc); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "user not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "user.data_exported", "operator", id, map[string]any{"reason": reason})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}
