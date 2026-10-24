package http

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
	"github.com/stayconnect/enterprise/control-plane/internal/totp"
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

// finishPasswordStep opens the session at once for an account without a confirmed authenticator, and otherwise only
// the pending sign-in that the code step completes. An unreadable state refuses: it cannot tell which applies.
func (d authDeps) finishPasswordStep(w http.ResponseWriter, r *http.Request, op *auth.Operator) {
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
	if !st.Enrolled {
		d.clearPendingCookie(w)
		sess, ok := d.openSession(w, r, op, "none")
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, struct {
			SecondStep string `json:"second_step"`
			whoamiResp
		}{"none", d.describe(r.Context(), sess)})
		return
	}
	tok, err := auth.CreatePending(r.Context(), d.DB, op.ID)
	if err != nil {
		api.Fail(w, r, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	d.setPendingCookie(w, tok)
	writeJSON(w, http.StatusOK, map[string]string{"second_step": "code"})
}

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
	method, left, valid, locked, err := d.MFA.Verify(r.Context(), p.OperatorID, in.Code)
	var lockedErr auth.ErrMFALocked
	switch {
	case errors.As(err, &lockedErr):
		d.codeLocked(w, r, lockedErr.Until)
		return
	case errors.Is(err, auth.ErrMFANotEnrolled):
		// Reset meanwhile: the password step must be taken again, never skipped.
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
	if method == "recovery_code" {
		audit.Op(auth.ContextWithSession(r.Context(), sess), d.DB, r, "auth.mfa_recovery_code_used", "operator", op.ID,
			map[string]any{"recovery_codes_left": left})
	}
	type codeResp struct {
		whoamiResp
		RecoveryCodesLeft *int `json:"recovery_codes_left,omitempty"`
	}
	out := codeResp{whoamiResp: d.describe(r.Context(), sess)}
	if method == "recovery_code" {
		out.RecoveryCodesLeft = &left
	}
	writeJSON(w, http.StatusOK, out)
}

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

func (d authDeps) mfaStatus(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	st, err := d.MFA.Status(r.Context(), s.OperatorID)
	if err != nil {
		api.Fail(w, r, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now.")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// mfaRecoveryCodes needs a current authenticator code (never a recovery code), checked like a sign-in code.
// A wrong code answers 400, not 401: the console reads a 401 as the sign-in having ended.
func (d authDeps) mfaRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	var in struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in)
	if c := totp.Normalize(in.Code); len(c) != totp.Digits || strings.Trim(c, "0123456789") != "" {
		api.Fail(w, r, http.StatusBadRequest, "code_required",
			"Enter the 6-digit code your authenticator app shows now. A recovery code cannot be used for this.")
		return
	}
	codes, ok, locked, err := d.MFA.NewRecoveryCodes(r.Context(), s.OperatorID, in.Code)
	var lockedErr auth.ErrMFALocked
	switch {
	case errors.As(err, &lockedErr):
		d.codeLocked(w, r, lockedErr.Until)
		return
	case errors.Is(err, auth.ErrMFANotEnrolled):
		api.Fail(w, r, http.StatusConflict, "not_enrolled", "No authenticator is set up for this account.")
		return
	case err != nil:
		api.Fail(w, r, http.StatusServiceUnavailable, "mfa_unavailable", "New recovery codes could not be made. Try again shortly.")
		return
	case !ok:
		d.wrongOwnCode(w, r, s.OperatorID, "new recovery codes",
			"That code is not right. Enter the 6-digit code your authenticator app shows now.", locked)
		return
	}
	audit.Op(r.Context(), d.DB, r, "auth.mfa_recovery_codes_replaced", "operator", s.OperatorID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (d authDeps) codeLocked(w http.ResponseWriter, r *http.Request, until time.Time) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(until).Seconds())+1)))
	api.Fail(w, r, http.StatusTooManyRequests, "too_many_attempts",
		"Too many wrong codes. Wait until "+until.UTC().Format("15:04")+" UTC and try again.")
}

func (d authDeps) wrongOwnCode(w http.ResponseWriter, r *http.Request, operatorID, what, msg string, locked *time.Time) {
	d.auditFailure(r, "auth.mfa_failed", operatorID, map[string]any{"reason": "wrong code", "for": what})
	if locked != nil {
		audit.Emit(r.Context(), d.DB, audit.Entry{ActorType: "system", Action: "auth.mfa_locked", TargetType: "operator",
			TargetID: operatorID, IP: clientip.From(r), Payload: map[string]any{"locked_until": locked.UTC()}})
	}
	api.Fail(w, r, http.StatusBadRequest, "wrong_code", msg)
}

// mfaSetup starts turning two-step sign-in on (behind the password step-up): a new secret for the QR code. A set-up
// never confirmed is replaced; a confirmed authenticator never is, so this cannot swap one out.
func (d authDeps) mfaSetup(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	secret, uri, err := d.MFA.Enrol(r.Context(), s.OperatorID, s.Email)
	switch {
	case errors.Is(err, auth.ErrMFAAlreadyEnrolled):
		api.Fail(w, r, http.StatusConflict, "already_enrolled", "Two-step sign-in is already on for this account.")
		return
	case err != nil:
		slog.Error("two-step set-up failed", "err", err)
		api.Fail(w, r, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in could not be set up. Try again shortly.")
		return
	}
	audit.Op(r.Context(), d.DB, r, "auth.mfa_setup_started", "operator", s.OperatorID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_uri": uri, "issuer": auth.MFAIssuer, "account": s.Email})
}

// mfaConfirm turns two-step sign-in on with the first code from the app, and answers the recovery codes once.
func (d authDeps) mfaConfirm(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	var in struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in)
	c := totp.Normalize(in.Code)
	if len(c) != totp.Digits || strings.Trim(c, "0123456789") != "" {
		api.Fail(w, r, http.StatusBadRequest, "code_required", "Enter the 6-digit code your authenticator app shows now.")
		return
	}
	codes, ok, locked, err := d.MFA.Confirm(r.Context(), s.OperatorID, c)
	var lockedErr auth.ErrMFALocked
	switch {
	case errors.As(err, &lockedErr):
		d.codeLocked(w, r, lockedErr.Until)
		return
	case errors.Is(err, auth.ErrMFAAlreadyEnrolled):
		api.Fail(w, r, http.StatusConflict, "nothing_to_confirm", "There is no set-up waiting to be confirmed. Start again.")
		return
	case err != nil:
		slog.Error("two-step confirm failed", "err", err)
		api.Fail(w, r, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in could not be turned on. Try again shortly.")
		return
	case !ok:
		d.wrongOwnCode(w, r, s.OperatorID, "turning two-step sign-in on",
			"That code is not right. Enter the 6-digit code your authenticator app shows now.", locked)
		return
	}
	audit.Op(r.Context(), d.DB, r, "auth.mfa_enrolled", "operator", s.OperatorID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"enrolled": true, "recovery_codes": codes})
}

// mfaDisable turns two-step sign-in off. Behind the password step-up it also needs a code from the second factor
// (the app's or a recovery code): a stolen session plus the password is not enough.
func (d authDeps) mfaDisable(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	var in struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in)
	if strings.TrimSpace(in.Code) == "" || len(in.Code) > maxMFACodeLen {
		api.Fail(w, r, http.StatusBadRequest, "code_required",
			"Enter the 6-digit code your authenticator app shows now, or one of your recovery codes.")
		return
	}
	method, ok, locked, err := d.MFA.Disable(r.Context(), s.OperatorID, in.Code)
	var lockedErr auth.ErrMFALocked
	switch {
	case errors.As(err, &lockedErr):
		d.codeLocked(w, r, lockedErr.Until)
		return
	case errors.Is(err, auth.ErrMFANotEnrolled):
		api.Fail(w, r, http.StatusConflict, "not_enrolled", "Two-step sign-in is not on for this account.")
		return
	case err != nil:
		slog.Error("two-step disable failed", "err", err)
		api.Fail(w, r, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in could not be turned off. Try again shortly.")
		return
	case !ok:
		d.wrongOwnCode(w, r, s.OperatorID, "turning two-step sign-in off",
			"That code is not right. Enter the 6-digit code your authenticator app shows now, or a recovery code.", locked)
		return
	}
	audit.Op(r.Context(), d.DB, r, "auth.mfa_disabled", "operator", s.OperatorID, map[string]any{"second_step": method})
	writeJSON(w, http.StatusOK, map[string]any{"enrolled": false})
}
