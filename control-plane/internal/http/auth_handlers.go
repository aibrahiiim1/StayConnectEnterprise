package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
)

type authDeps struct {
	Repo   *auth.Repo
	Store  *auth.SessionStore
	DB     *pgxpool.Pool
	Secure bool
	// Failures bounds how many failed sign-ins / step-ups are written to the audit log.
	Failures *ratebudget.Budget
	// MFA nil refuses every sign-in: fail closed.
	MFA *auth.MFA
	// NewSession nil means Store.Create.
	NewSession func(context.Context, auth.Session) (*auth.Session, error)
}

// auditFailure records a failure within the budget; suppressed failures are summarised as one
// auth.failures_suppressed row in the next window, so a flood cannot fill the log.
func (d authDeps) auditFailure(r *http.Request, action, operatorID string, payload map[string]any) {
	if d.DB == nil {
		return
	}
	ip := clientip.From(r)
	if d.Failures != nil {
		ok, suppressed := d.Failures.Allow(ip)
		if suppressed > 0 {
			audit.Emit(r.Context(), d.DB, audit.Entry{ActorType: "system", Action: "auth.failures_suppressed",
				Payload: map[string]any{"count": suppressed, "window_seconds": int(d.Failures.Window.Seconds())}})
		}
		if !ok {
			return
		}
	}
	actor := "operator"
	if operatorID == "" {
		actor = "api"
	}
	audit.Emit(r.Context(), d.DB, audit.Entry{ActorType: actor, ActorID: operatorID, Action: action,
		TargetType: "operator", TargetID: operatorID, IP: ip, UserAgent: r.UserAgent(), Payload: payload})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type whoamiResp struct {
	OperatorID   string   `json:"operator_id"`
	Email        string   `json:"email"`
	DisplayName  string   `json:"display_name"`
	Roles        []string `json:"roles"`
	IsSuperAdmin bool     `json:"is_super_admin"`
	CustomerID   *string  `json:"customer_id"`
	CustomerName *string  `json:"customer_name"`
	Permissions  []string `json:"permissions"`

	// idle_expires_at moves with activity (whoami does not count); absolute_expires_at never moves.
	IdleExpiresAt     *time.Time `json:"idle_expires_at,omitempty"`
	AbsoluteExpiresAt *time.Time `json:"absolute_expires_at,omitempty"`
	ServerTime        *time.Time `json:"server_time,omitempty"`
}

func withSessionTiming(resp whoamiResp, s *auth.Session, now time.Time) whoamiResp {
	if !s.IdleExpiresAt.IsZero() {
		idle := s.IdleExpiresAt.UTC()
		resp.IdleExpiresAt = &idle
	}
	if !s.AbsoluteExpiresAt.IsZero() {
		abs := s.AbsoluteExpiresAt.UTC()
		resp.AbsoluteExpiresAt = &abs
	}
	n := now.UTC()
	resp.ServerTime = &n
	return resp
}

func (d authDeps) describe(ctx context.Context, s *auth.Session) whoamiResp {
	roles := s.Roles
	if roles == nil {
		roles = []string{}
	}
	resp := whoamiResp{OperatorID: s.OperatorID, Email: s.Email, DisplayName: s.DisplayName, Roles: roles,
		IsSuperAdmin: s.IsSuperAdmin, Permissions: s.Permissions()}
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil { // audit Phase 1 M9
		api.Fail(w, r, http.StatusBadRequest, api.CodeBadRequest, "bad body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		api.Fail(w, r, http.StatusBadRequest, api.CodeBadRequest, "email and password required")
		return
	}

	if d.signinLocked(w, r, req.Email, "") {
		return
	}

	op, err := d.Repo.FindByEmail(r.Context(), req.Email)
	if err != nil || op.Status != "active" || op.PasswordHash == "" {
		// Same response and timing for every failure (a decoy password check), so names with accounts are not revealed.
		if errors.Is(auth.SpendPasswordCheck(req.Password), auth.ErrBusy) {
			api.Fail(w, r, http.StatusServiceUnavailable, "busy", "Central is busy checking other sign-ins. Try again in a moment.")
			return
		}
		reason, opID := "unknown email", ""
		if err == nil {
			opID = op.ID
			reason = "operator " + op.Status
			if op.Status == "active" {
				reason = "no password set"
			}
		}
		d.auditFailure(r, "auth.login_failed", opID, map[string]any{"reason": reason})
		if d.signinFailed(w, r, req.Email, opID) {
			return
		}
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "invalid credentials")
		return
	}
	if err := auth.VerifyPassword(op.PasswordHash, req.Password); err != nil {
		if errors.Is(err, auth.ErrBusy) {
			api.Fail(w, r, http.StatusServiceUnavailable, "busy", "Central is busy checking other sign-ins. Try again in a moment.")
			return
		}
		d.auditFailure(r, "auth.login_failed", op.ID, map[string]any{"reason": "wrong password"})
		if d.signinFailed(w, r, req.Email, op.ID) {
			return
		}
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "invalid credentials")
		return
	}
	d.signinSucceeded(r, req.Email)

	// With a confirmed authenticator the password is only the first step: no session until its code.
	d.finishPasswordStep(w, r, op)
}

// reauth re-verifies the current operator's password; licence, activation and other SU writes require it on top of the permission.
func (d authDeps) reauth(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	if s == nil {
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "not authenticated")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Password == "" {
		api.Fail(w, r, http.StatusBadRequest, api.CodeBadRequest, "password required")
		return
	}
	if d.signinLocked(w, r, s.Email, s.OperatorID) {
		return
	}
	op, err := d.Repo.FindByID(r.Context(), s.OperatorID)
	if err != nil || op.Status != "active" || op.PasswordHash == "" {
		d.auditFailure(r, "auth.reauth_failed", s.OperatorID, map[string]any{"reason": "operator not active"})
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "reauthentication failed")
		return
	}
	if err := auth.VerifyPassword(op.PasswordHash, req.Password); err != nil {
		if errors.Is(err, auth.ErrBusy) {
			api.Fail(w, r, http.StatusServiceUnavailable, "busy", "Central is busy checking other sign-ins. Try again in a moment.")
			return
		}
		d.auditFailure(r, "auth.reauth_failed", s.OperatorID, map[string]any{"reason": "wrong password"})
		if d.signinFailed(w, r, s.Email, s.OperatorID) {
			return
		}
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "reauthentication failed")
		return
	}
	d.signinSucceeded(r, s.Email)
	// The session always comes from the cookie (auth.Middleware), so there is a cookie to mark.
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := api.MarkReauth(r.Context(), d.Store.R, c.Value); err != nil {
			slog.Warn("step-up could not be recorded", "err", err)
			api.Fail(w, r, http.StatusServiceUnavailable, "unavailable", "Central could not record the password confirmation. Try again in a moment.")
			return
		}
	}
	audit.Op(r.Context(), d.DB, r, "auth.reauth_succeeded", "operator", s.OperatorID, nil)
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
	writeJSON(w, http.StatusOK, withSessionTiming(d.describe(r.Context(), s), s, time.Now()))
}
