package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type authDeps struct {
	Repo   *auth.Repo
	Store  *auth.SessionStore
	Secure bool // set true in prod to add Secure on cookies
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type whoamiResp struct {
	OperatorID      string   `json:"operator_id"`
	Email           string   `json:"email"`
	DisplayName     string   `json:"display_name,omitempty"`
	IsSuperAdmin    bool     `json:"is_super_admin"`
	DefaultTenantID string   `json:"default_tenant_id,omitempty"`
	Roles           []string `json:"roles"`
	ExpiresAt       string   `json:"expires_at"`
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
		IsSuperAdmin:    op.IsSuperAdmin,
		DefaultTenantID: op.DefaultTenant,
		Roles:           op.Roles,
		SiteIDs:         op.SiteIDs,
		TenantWide:      op.TenantWide,
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
	writeJSON(w, http.StatusOK, whoamiResp{
		OperatorID:      op.ID,
		Email:           op.Email,
		DisplayName:     op.DisplayName,
		IsSuperAdmin:    op.IsSuperAdmin,
		DefaultTenantID: op.DefaultTenant,
		Roles:           op.Roles,
		ExpiresAt:       sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// reauth re-verifies the CURRENT session operator's password. Sensitive
// Platform actions (assign, reassign, revoke, issue/revoke license, replace,
// decommission, mint offline package) require a fresh password confirmation
// on top of the permission check. Returns 200 {"ok":true} or 401.
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
	op, err := d.Repo.FindByEmail(r.Context(), s.Email)
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
	writeJSON(w, http.StatusOK, whoamiResp{
		OperatorID:      s.OperatorID,
		Email:           s.Email,
		IsSuperAdmin:    s.IsSuperAdmin,
		DefaultTenantID: s.DefaultTenantID,
		Roles:           s.Roles,
		ExpiresAt:       s.ExpiresAt.UTC().Format(time.RFC3339),
	})
}
