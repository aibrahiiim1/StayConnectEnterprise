package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type authDeps struct {
	Repo   *auth.Repo
	Store  *auth.SessionStore
	DB     *pgxpool.Pool
	Secure bool // set true in prod to add Secure on cookies
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// whoamiResp is the §6 whoami shape; login answers with the same.
type whoamiResp struct {
	OperatorID   string   `json:"operator_id"`
	Email        string   `json:"email"`
	DisplayName  string   `json:"display_name"`
	Roles        []string `json:"roles"`
	IsSuperAdmin bool     `json:"is_super_admin"`
	CustomerID   *string  `json:"customer_id"`
	CustomerName *string  `json:"customer_name"`
	Permissions  []string `json:"permissions"`
}

func (d authDeps) describe(ctx context.Context, s *auth.Session) whoamiResp {
	roles := s.Roles
	if roles == nil {
		roles = []string{}
	}
	resp := whoamiResp{OperatorID: s.OperatorID, Email: s.Email, DisplayName: s.DisplayName, Roles: roles,
		IsSuperAdmin: s.IsSuperAdmin, Permissions: s.Permissions()}
	// A customer-scoped user belongs to one customer; a platform operator to none.
	if s.DefaultTenantID != "" && !s.HasPermission(auth.PermFleetView) {
		id := s.DefaultTenantID
		resp.CustomerID = &id
		var name string
		if d.DB != nil && d.DB.QueryRow(ctx, `SELECT name FROM tenants WHERE id=$1`, id).Scan(&name) == nil {
			resp.CustomerName = &name
		}
	}
	return resp
}

func (d authDeps) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.Fail(w, r, http.StatusBadRequest, api.CodeBadRequest, "bad body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		api.Fail(w, r, http.StatusBadRequest, api.CodeBadRequest, "email and password required")
		return
	}

	op, err := d.Repo.FindByEmail(r.Context(), req.Email)
	if err != nil || op.Status != "active" || op.PasswordHash == "" {
		// Constant response for bad email vs bad password vs disabled.
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "invalid credentials")
		return
	}
	if err := auth.VerifyPassword(op.PasswordHash, req.Password); err != nil {
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "invalid credentials")
		return
	}

	sess, err := d.Store.Create(r.Context(), auth.Session{
		OperatorID:      op.ID,
		Email:           op.Email,
		DisplayName:     op.DisplayName,
		IsSuperAdmin:    op.IsSuperAdmin,
		DefaultTenantID: op.DefaultTenant,
		Roles:           op.Roles,
	})
	if err != nil {
		api.Fail(w, r, http.StatusInternalServerError, api.CodeInternal, "session create failed")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sess.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   d.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
	})
	writeJSON(w, http.StatusOK, d.describe(r.Context(), sess))
}

// reauth re-verifies the CURRENT session operator's password. Licence and activation writes (and every
// other SU route) require this fresh confirmation on top of the permission check. Returns 200 {"ok":true}
// or 401.
func (d authDeps) reauth(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	if s == nil {
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "not authenticated")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		api.Fail(w, r, http.StatusBadRequest, api.CodeBadRequest, "password required")
		return
	}
	op, err := d.Repo.FindByID(r.Context(), s.OperatorID)
	if err != nil || op.Status != "active" || op.PasswordHash == "" {
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "reauthentication failed")
		return
	}
	if err := auth.VerifyPassword(op.PasswordHash, req.Password); err != nil {
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "reauthentication failed")
		return
	}
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		api.MarkReauth(d.Store.R, c.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (d authDeps) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
		_ = d.Store.Destroy(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   d.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (d authDeps) whoami(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	if s == nil {
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, d.describe(r.Context(), s))
}
