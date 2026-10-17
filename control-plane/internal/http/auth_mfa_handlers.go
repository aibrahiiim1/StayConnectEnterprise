package http

// THE SECOND SIGN-IN STEP (migration 0052; GDPR remediation plan C10, decision D-C10; auth/mfa.go).
//
//	POST /v1/auth/login                {email, password} -> {second_step: "code" | "enrol"}  (no session)
//	POST /v1/auth/login/enrol                            -> {secret, otpauth_uri}            (first sign-in)
//	POST /v1/auth/login/code           {code}            -> the session (whoami shape); first sign-in adds recovery_codes
//	GET  /v1/auth/mfa                                    -> the operator's own two-step state
//	POST /v1/auth/mfa/recovery-codes   (after step-up)   -> a new set of recovery codes
//
// The password step (login) is unchanged up to the point where it used to create the session. The password
// step-up (/v1/auth/reauth) stays a password: re-confirming inside a session opened with two factors may use one
// (NIST SP 800-63B section 7.2).

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
)

const maxMFACodeLen = 32

func (d authDeps) setPendingCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{Name: auth.PendingCookieName, Value: tok, Path: "/", HttpOnly: true, Secure: d.Secure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(auth.PendingSigninTTL / time.Second)})
}

func (d authDeps) clearPendingCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: auth.PendingCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: d.Secure, SameSite: http.SameSiteStrictMode})
}

// beginSecondStep replaces the session the password used to open.
func (d authDeps) beginSecondStep(w http.ResponseWriter, r *http.Request, op *auth.Operator) {
	if d.MFA == nil || d.DB == nil {
		slog.Error("sign-in refused: two-step sign-in is not configured (CTRLAPI_OPERATOR_MFA_KEY)")
		api.Fail(w, r, http.StatusServiceUnavailable, "signin_unavailable",
			"Sign-in is unavailable: two-step sign-in is not configured on this Central.")
		return
	}
	st, err := d.MFA.Status(r.Context(), op.ID)
	if err != nil {
		api.Fail(w, r, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	tok, err := auth.CreatePending(r.Context(), d.DB, op.ID, !st.Enrolled)
	if err != nil {
		api.Fail(w, r, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	d.setPendingCookie(w, tok)
	step := "code"
	if !st.Enrolled {
		step = "enrol"
	}
	writeJSON(w, http.StatusOK, map[string]string{"second_step": step})
}

// pending is the caller's "password accepted" state, or a 401 telling the console to start again.
func (d authDeps) pending(w http.ResponseWriter, r *http.Request) (string, *auth.PendingSignin, bool) {
	var p *auth.PendingSignin
	c, err := r.Cookie(auth.PendingCookieName)
	if err == nil && c.Value != "" && d.DB != nil {
		p, _ = auth.LookupPending(r.Context(), d.DB, c.Value)
	}
	if p == nil {
		d.clearPendingCookie(w)
		api.Fail(w, r, http.StatusUnauthorized, "signin_expired", "Sign in again: the code step took longer than five minutes.")
		return "", nil, false
	}
	return c.Value, p, true
}

// loginEnrol: POST /v1/auth/login/enrol
func (d authDeps) loginEnrol(w http.ResponseWriter, r *http.Request) {
	tok, p, ok := d.pending(w, r)
	if !ok {
		return
	}
	if !p.Enrol {
		api.Fail(w, r, http.StatusConflict, "already_enrolled", "An authenticator is already set up. Enter its code.")
		return
	}
	op, err := d.Repo.FindByID(r.Context(), p.OperatorID)
	if err != nil {
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "invalid credentials")
		return
	}
	secret, uri, err := d.MFA.Enrol(r.Context(), op.ID, op.Email)
	if errors.Is(err, auth.ErrMFAAlreadyEnrolled) {
		auth.SetPendingEnrol(r.Context(), d.DB, tok, false)
		api.Fail(w, r, http.StatusConflict, "already_enrolled", "An authenticator is already set up. Enter its code.")
		return
	}
	if err != nil {
		slog.Error("two-step set-up failed", "err", err)
		api.Fail(w, r, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_uri": uri, "issuer": auth.MFAIssuer, "account": op.Email})
}

// loginCode: POST /v1/auth/login/code {code}
func (d authDeps) loginCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil ||
		strings.TrimSpace(in.Code) == "" || len(in.Code) > maxMFACodeLen {
		api.Fail(w, r, http.StatusBadRequest, api.CodeBadRequest, "code required")
		return
	}
	tok, p, ok := d.pending(w, r)
	if !ok {
		return
	}
	var (
		recovery []string
		method   = "totp"
		left     = -1
		valid    bool
		locked   *time.Time
		err      error
	)
	if p.Enrol {
		recovery, valid, locked, err = d.MFA.Confirm(r.Context(), p.OperatorID, in.Code)
		method = "enrolled"
	} else {
		method, left, valid, locked, err = d.MFA.Verify(r.Context(), p.OperatorID, in.Code)
	}
	var lockedErr auth.ErrMFALocked
	switch {
	case errors.As(err, &lockedErr):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(lockedErr.Until).Seconds())+1)))
		api.Fail(w, r, http.StatusTooManyRequests, "too_many_attempts",
			"Too many wrong codes. Wait until "+lockedErr.Until.UTC().Format("15:04")+" UTC and try again.")
		return
	case errors.Is(err, auth.ErrMFANotEnrolled), errors.Is(err, auth.ErrMFAAlreadyEnrolled):
		auth.DropPending(r.Context(), d.DB, tok)
		d.clearPendingCookie(w)
		api.Fail(w, r, http.StatusConflict, "signin_restart", "Your sign-in changed while you were entering the code. Sign in again.")
		return
	case err != nil:
		slog.Error("two-step code check failed", "err", err)
		api.Fail(w, r, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	case !valid:
		d.auditFailure(r, "auth.mfa_failed", p.OperatorID, map[string]any{"reason": "wrong code"})
		if locked != nil {
			audit.Emit(r.Context(), d.DB, audit.Entry{ActorType: "system", Action: "auth.mfa_locked", TargetType: "operator",
				TargetID: p.OperatorID, IP: clientip.From(r), Payload: map[string]any{"locked_until": locked.UTC()}})
		}
		api.Fail(w, r, http.StatusUnauthorized, "wrong_code",
			"That code is not right. Enter the 6-digit code your authenticator app shows now, or a recovery code.")
		return
	}
	auth.DropPending(r.Context(), d.DB, tok)
	d.clearPendingCookie(w)
	op, err := d.Repo.FindByID(r.Context(), p.OperatorID)
	if err != nil || op.Status != "active" {
		api.Fail(w, r, http.StatusUnauthorized, api.CodeUnauthenticated, "invalid credentials")
		return
	}
	sess, ok := d.openSession(w, r, op, method)
	if !ok {
		return
	}
	if p.Enrol {
		audit.Op(auth.ContextWithSession(r.Context(), sess), d.DB, r, "auth.mfa_enrolled", "operator", op.ID, nil)
	}
	if method == "recovery_code" {
		audit.Op(auth.ContextWithSession(r.Context(), sess), d.DB, r, "auth.mfa_recovery_code_used", "operator", op.ID,
			map[string]any{"recovery_codes_left": left})
	}
	type codeResp struct {
		whoamiResp
		RecoveryCodes     []string `json:"recovery_codes,omitempty"`
		RecoveryCodesLeft *int     `json:"recovery_codes_left,omitempty"`
	}
	out := codeResp{whoamiResp: d.describe(r.Context(), sess), RecoveryCodes: recovery}
	if method == "recovery_code" {
		out.RecoveryCodesLeft = &left
	}
	writeJSON(w, http.StatusOK, out)
}

// openSession creates the console session after both steps, exactly as the password alone used to.
func (d authDeps) openSession(w http.ResponseWriter, r *http.Request, op *auth.Operator, secondStep string) (*auth.Session, bool) {
	create := d.NewSession
	if create == nil {
		create = d.Store.Create
	}
	sess, err := create(r.Context(), auth.Session{
		OperatorID:      op.ID,
		Email:           op.Email,
		DisplayName:     op.DisplayName,
		IsSuperAdmin:    op.IsSuperAdmin,
		DefaultTenantID: op.DefaultTenant,
		Roles:           op.Roles,
	})
	if err != nil {
		api.Fail(w, r, http.StatusInternalServerError, api.CodeInternal, "session create failed")
		return nil, false
	}
	if d.DB != nil {
		_, _ = d.DB.Exec(r.Context(), `UPDATE operators SET last_login_at=now() WHERE id=$1`, op.ID)
		audit.Emit(r.Context(), d.DB, audit.Entry{ActorType: "operator", ActorID: op.ID, TenantID: op.DefaultTenant,
			Action: "auth.login_succeeded", TargetType: "operator", TargetID: op.ID,
			IP: clientip.From(r), UserAgent: r.UserAgent(), Payload: map[string]any{"second_step": secondStep}})
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
	return sess, true
}

// mfaStatus: GET /v1/auth/mfa
func (d authDeps) mfaStatus(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	st, err := d.MFA.Status(r.Context(), s.OperatorID)
	if err != nil {
		api.Fail(w, r, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now.")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// mfaRecoveryCodes: POST /v1/auth/mfa/recovery-codes (after the password step-up).
func (d authDeps) mfaRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	codes, err := d.MFA.NewRecoveryCodes(r.Context(), s.OperatorID)
	if err != nil {
		api.Fail(w, r, http.StatusServiceUnavailable, "mfa_unavailable", "New recovery codes could not be made. Try again shortly.")
		return
	}
	audit.Op(r.Context(), d.DB, r, "auth.mfa_recovery_codes_replaced", "operator", s.OperatorID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}
