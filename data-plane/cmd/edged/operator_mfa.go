package main

// Two-step sign-in is optional per account (D68). An account without a confirmed authenticator signs in with its
// password alone; one with a confirmed authenticator gets only a five-minute "password accepted" state under its own
// cookie, and the session is created when the second step (authenticator code or recovery code) succeeds. A set-up
// that was started but never confirmed does not count. Wrong codes are throttled like wrong passwords (scope 'mfa')
// and audited. scd seals the secret; edged never sees it after the QR code.
// An operator turns it on from their account (password, then the first code) and off with the password and a code.
// Password step-up inside a two-factor session stays a password (NIST SP 800-63B 7.2).
// Sessions end after idle minutes or total hours (public.operator_session_policy); the console's background whoami
// does not count as activity, or an open tab would never idle out.
// Recovery: a site administrator can reset a colleague's second step; the last administrator via `edged reset-mfa --email`.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/totp"
)

const (
	scopeMFA = "mfa"
	// pendingSigninCookie has its own name so the console's middleware (which looks for the session cookie) still shows the sign-in page.
	pendingSigninCookie = "sc_edge_signin"
	pendingSigninTTL    = 5 * time.Minute
	mfaIssuer           = "StayConnect Hotel Admin"
	maxMFACodeLen       = 32
)

var (
	errMFAUnavailable     = errors.New("the authenticator service is unavailable")
	errMFANotEnrolled     = errors.New("no authenticator is set up")
	errMFAAlreadyEnrolled = errors.New("an authenticator is already set up")
)

type mfaStatus struct {
	Enrolled          bool       `json:"enrolled"`
	ConfirmedAt       *time.Time `json:"confirmed_at,omitempty"`
	RecoveryCodesLeft int        `json:"recovery_codes_left"`
}

// mfaService is scd in production; tests give a stand-in that computes real codes.
type mfaService interface {
	Status(ctx context.Context, operatorID string) (mfaStatus, error)
	Enrol(ctx context.Context, operatorID, account string) (secret, uri string, err error)
	Confirm(ctx context.Context, operatorID, code string) (recoveryCodes []string, ok bool, err error)
	Verify(ctx context.Context, operatorID, code string) (method string, recoveryLeft int, ok bool, err error)
	// NewRecoveryCodes accepts only a current authenticator code (never a recovery code), with sign-in's replay rule.
	NewRecoveryCodes(ctx context.Context, operatorID, code string) (recoveryCodes []string, ok bool, err error)
	Reset(ctx context.Context, operatorID string) (removed bool, err error)
}

type scdMFA struct {
	scd *scdClient
	db  *pgxpool.Pool
}

func (m scdMFA) Status(ctx context.Context, operatorID string) (mfaStatus, error) {
	var st mfaStatus
	err := m.db.QueryRow(ctx, `SELECT enrolled, confirmed_at, recovery_codes_left FROM public.operator_mfa_status($1)`,
		operatorID).Scan(&st.Enrolled, &st.ConfirmedAt, &st.RecoveryCodesLeft)
	return st, err
}

func (m scdMFA) post(ctx context.Context, path string, body map[string]string, out any) error {
	if m.scd == nil {
		return errMFAUnavailable
	}
	st, raw, err := m.scd.call(ctx, http.MethodPost, "/v1/admin/operator-mfa/"+path, body)
	if err != nil {
		return fmt.Errorf("%w: %v", errMFAUnavailable, err)
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	switch {
	case st == http.StatusConflict && e.Error == "already_enrolled":
		return errMFAAlreadyEnrolled
	case st == http.StatusConflict:
		return errMFANotEnrolled
	case st != http.StatusOK:
		return fmt.Errorf("%w: scd answered %d %s", errMFAUnavailable, st, e.Error)
	}
	return json.Unmarshal(raw, out)
}

func (m scdMFA) Enrol(ctx context.Context, operatorID, account string) (string, string, error) {
	var out struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	err := m.post(ctx, "enrol", map[string]string{"operator_id": operatorID, "issuer": mfaIssuer, "account": account}, &out)
	return out.Secret, out.URI, err
}

func (m scdMFA) Confirm(ctx context.Context, operatorID, code string) ([]string, bool, error) {
	var out struct {
		Confirmed bool     `json:"confirmed"`
		Codes     []string `json:"recovery_codes"`
	}
	err := m.post(ctx, "confirm", map[string]string{"operator_id": operatorID, "code": code}, &out)
	return out.Codes, out.Confirmed, err
}

func (m scdMFA) Verify(ctx context.Context, operatorID, code string) (string, int, bool, error) {
	var out struct {
		OK     bool   `json:"ok"`
		Method string `json:"method"`
		Left   int    `json:"recovery_codes_left"`
	}
	err := m.post(ctx, "verify", map[string]string{"operator_id": operatorID, "code": code}, &out)
	return out.Method, out.Left, out.OK, err
}

func (m scdMFA) NewRecoveryCodes(ctx context.Context, operatorID, code string) ([]string, bool, error) {
	var out struct {
		OK    bool     `json:"ok"`
		Codes []string `json:"recovery_codes"`
	}
	err := m.post(ctx, "recovery-codes", map[string]string{"operator_id": operatorID, "code": code}, &out)
	return out.Codes, out.OK, err
}

func (m scdMFA) Reset(ctx context.Context, operatorID string) (bool, error) {
	var out struct {
		Removed bool `json:"removed"`
	}
	err := m.post(ctx, "reset", map[string]string{"operator_id": operatorID}, &out)
	return out.Removed, err
}

// pendingSignin is what the password step proved. Roles, display name and must-change are not trusted at the code
// step (an administrator may change the account meanwhile), so it reloads them. PasswordFP lets a password changed
// in between end the pending sign-in.
type pendingSignin struct {
	OperatorID         string
	Email              string
	DisplayName        string
	Roles              []string
	MustChangePassword bool
	PasswordFP         string
	Expires            time.Time
}

func passwordFingerprint(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:])
}

type pendingSignins struct {
	mu sync.Mutex
	m  map[string]*pendingSignin
}

func (p *pendingSignins) create(ps *pendingSignin) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	ps.Expires = now.Add(pendingSigninTTL)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]*pendingSignin{}
	}
	for k, v := range p.m {
		if now.After(v.Expires) || v.OperatorID == ps.OperatorID {
			delete(p.m, k) // one pending sign-in per operator: a new password step replaces the old one
		}
	}
	p.m[tok] = ps
	return tok
}

// get returns a copy so concurrent handlers never share the struct.
func (p *pendingSignins) get(tok string) *pendingSignin {
	p.mu.Lock()
	defer p.mu.Unlock()
	ps, ok := p.m[tok]
	if !ok || time.Now().After(ps.Expires) {
		delete(p.m, tok)
		return nil
	}
	cp := *ps
	return &cp
}

func (p *pendingSignins) drop(tok string) {
	p.mu.Lock()
	delete(p.m, tok)
	p.mu.Unlock()
}

func (p *pendingSignins) dropOperator(operatorID string) {
	p.mu.Lock()
	for k, v := range p.m {
		if v.OperatorID == operatorID {
			delete(p.m, k)
		}
	}
	p.mu.Unlock()
}

func (s *server) setPendingCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{Name: pendingSigninCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.secure, MaxAge: int(pendingSigninTTL / time.Second)})
}

func (s *server) clearPendingCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: pendingSigninCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.secure})
}

func (s *server) pendingFrom(w http.ResponseWriter, r *http.Request) (string, *pendingSignin, bool) {
	ck, err := r.Cookie(pendingSigninCookie)
	var ps *pendingSignin
	if err == nil && ck.Value != "" {
		ps = s.pending.get(ck.Value)
	}
	if ps == nil {
		s.clearPendingCookie(w)
		jsonErr(w, http.StatusUnauthorized, "signin_expired", "Sign in again: the code step took longer than five minutes.")
		return "", nil, false
	}
	return ck.Value, ps, true
}

// finishPasswordStep opens the session at once for an account without a confirmed authenticator, and otherwise only
// the pending sign-in that the code step completes. An unreadable state refuses: it cannot tell which applies.
func (s *server) finishPasswordStep(w http.ResponseWriter, r *http.Request, ctx context.Context, ps *pendingSignin) {
	if s.mfa == nil {
		jsonErr(w, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	st, err := s.mfa.Status(ctx, ps.OperatorID)
	if err != nil {
		slog.Error("sign-in: authenticator status unreadable; refusing", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	if !st.Enrolled {
		s.pending.dropOperator(ps.OperatorID)
		s.clearPendingCookie(w)
		_, out := s.openSession(w, r, ps, "none")
		out["second_step"] = "none"
		writeJSON(w, http.StatusOK, out)
		return
	}
	s.setPendingCookie(w, s.pending.create(ps))
	writeJSON(w, http.StatusOK, map[string]any{"second_step": "code"})
}

// openSession creates the session, sets its cookie and audits the sign-in. It answers the request carrying the new
// session (for further audit rows) and the operator payload.
func (s *server) openSession(w http.ResponseWriter, r *http.Request, ps *pendingSignin, secondStep string) (*http.Request, map[string]any) {
	sess := &session{OperatorID: ps.OperatorID, Email: ps.Email, DisplayName: ps.DisplayName, Roles: ps.Roles,
		MustChangePassword: ps.MustChangePassword}
	s.setSessionCookie(w, s.sessions.create(sess))
	ar := r.WithContext(context.WithValue(r.Context(), sessKey, sess))
	s.audit(ar, "operator.login", "operator", ps.OperatorID, map[string]any{
		"source_ip": clientIP(r), "must_change_password": ps.MustChangePassword, "second_step": secondStep})
	return ar, map[string]any{"operator_id": ps.OperatorID, "roles": ps.Roles, "must_change_password": ps.MustChangePassword}
}

func (s *server) loginCode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, authBodyLimit)
	var in struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Code) == "" || len(in.Code) > maxMFACodeLen {
		jsonErr(w, http.StatusBadRequest, "bad_request", "code required")
		return
	}
	tok, ps, ok := s.pendingFrom(w, r)
	if !ok {
		return
	}
	ip := clientIP(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	unlock := signinLocks.lock("mfa\x00" + ps.OperatorID)
	defer unlock()

	stIP, err1 := s.throttleStateFor(ctx, scopeIP, ip)
	stOp, err2 := s.throttleStateFor(ctx, scopeMFA, ps.OperatorID)
	if err1 != nil || err2 != nil {
		slog.Error("code throttle unreadable; refusing sign-in", "ip_err", err1, "operator_err", err2)
		jsonErr(w, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	if wait := max(stIP.refusal(false), stOp.refusal(true)); wait > 0 {
		writeThrottled(w, wait, "codes")
		return
	}

	method, left, valid, err := s.mfa.Verify(ctx, ps.OperatorID, in.Code)
	switch {
	case errors.Is(err, errMFANotEnrolled):
		// The authenticator was reset meanwhile: the password step must be taken again, never skipped.
		s.pending.drop(tok)
		s.clearPendingCookie(w)
		jsonErr(w, http.StatusConflict, "signin_restart", "Your sign-in changed while you were entering the code. Sign in again.")
		return
	case err != nil:
		slog.Error("sign-in: code check failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	case !valid:
		s.recordFailure(r, "operator.mfa_failed", "operator.mfa_locked", ps.OperatorID, "wrong_code",
			map[string]string{scopeMFA: ps.OperatorID, scopeIP: ip})
		jsonErr(w, http.StatusUnauthorized, "wrong_code",
			"That code is not right. Enter the 6-digit code your authenticator app shows now, or a recovery code.")
		return
	}
	if err := s.throttleClear(ctx, scopeMFA, ps.OperatorID); err != nil {
		slog.Warn("could not clear the code counter", "err", err)
	}
	// Use the account as it is now: it may have been disabled, re-roled or re-passworded while the code was typed.
	cur, err := s.currentOperator(ctx, ps)
	if err != nil {
		s.pending.drop(tok)
		s.clearPendingCookie(w)
		if errors.Is(err, errNoRoles) {
			s.audit(r, "operator.login_failed", "operator", ps.OperatorID, map[string]any{"reason": "no_roles", "source_ip": ip})
			jsonErr(w, http.StatusForbidden, "forbidden", "no roles assigned")
			return
		}
		jsonErr(w, http.StatusUnauthorized, "unauthenticated", "invalid credentials")
		return
	}
	ps = cur
	s.pending.drop(tok)
	s.clearPendingCookie(w)
	ar, out := s.openSession(w, r, ps, method)
	if method == "recovery_code" {
		s.audit(ar, "operator.mfa_recovery_code_used", "operator", ps.OperatorID, map[string]any{
			"source_ip": ip, "recovery_codes_left": left})
		out["recovery_codes_left"] = left
	}
	writeJSON(w, http.StatusOK, out)
}

var errNoRoles = errors.New("no roles assigned")

// currentOperator refuses an account that is no longer active or whose password changed since the password step.
func (s *server) currentOperator(ctx context.Context, ps *pendingSignin) (*pendingSignin, error) {
	var display, hash, status string
	var mustChange bool
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(display_name,''), COALESCE(password_hash,''), status, must_change_password
		  FROM operators WHERE id = $1`, ps.OperatorID).Scan(&display, &hash, &status, &mustChange); err != nil {
		return nil, err
	}
	if status != "active" || hash == "" || passwordFingerprint(hash) != ps.PasswordFP {
		return nil, errors.New("the account changed since the password step")
	}
	roles, err := s.loadRoles(ctx, ps.OperatorID)
	if err != nil {
		return nil, err
	}
	if len(roles) == 0 {
		return nil, errNoRoles
	}
	cur := *ps
	cur.DisplayName, cur.Roles, cur.MustChangePassword = display, roles, mustChange
	return &cur, nil
}

func (s *server) getOwnMFA(w http.ResponseWriter, r *http.Request) {
	sess := sessFrom(r.Context())
	if s.mfa == nil {
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now.")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	st, err := s.mfa.Status(ctx, sess.OperatorID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "two-step sign-in could not be read")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// codeGate serialises an operator's code checks and refuses while the wrong-code counter (sign-in's) is throttled.
// It answers the unlock, or nil after answering the refusal.
func (s *server) codeGate(w http.ResponseWriter, r *http.Request, ctx context.Context, operatorID, what string) func() {
	unlock := signinLocks.lock("mfa\x00" + operatorID)
	ip := clientIP(r)
	stIP, err1 := s.throttleStateFor(ctx, scopeIP, ip)
	stOp, err2 := s.throttleStateFor(ctx, scopeMFA, operatorID)
	if err1 != nil || err2 != nil {
		unlock()
		slog.Error("code throttle unreadable; refusing", "for", what, "ip_err", err1, "operator_err", err2)
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now. Try again shortly.")
		return nil
	}
	if wait := max(stIP.refusal(false), stOp.refusal(true)); wait > 0 {
		unlock()
		writeThrottled(w, wait, "codes")
		return nil
	}
	return unlock
}

func (s *server) wrongOwnCode(w http.ResponseWriter, r *http.Request, operatorID, msg string) {
	s.recordFailure(r, "operator.mfa_failed", "operator.mfa_locked", operatorID, "wrong_code",
		map[string]string{scopeMFA: operatorID, scopeIP: clientIP(r)})
	jsonErr(w, http.StatusUnauthorized, "wrong_code", msg)
}

// setupOwnMFA starts turning two-step sign-in on: after the password, a new secret for the QR code. A set-up that
// was never confirmed is replaced; a confirmed authenticator never is (scd refuses), so this cannot swap one out.
func (s *server) setupOwnMFA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "password required")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "Your password is not correct.")
		return
	}
	if s.mfa == nil {
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now.")
		return
	}
	sess := sessFrom(r.Context())
	ctx, cancel := dbCtx(r)
	defer cancel()
	secret, uri, err := s.mfa.Enrol(ctx, sess.OperatorID, sess.Email)
	switch {
	case errors.Is(err, errMFAAlreadyEnrolled):
		jsonErr(w, http.StatusConflict, "already_enrolled", "Two-step sign-in is already on for your account.")
		return
	case err != nil:
		slog.Error("two-step set-up failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in could not be set up. Try again shortly.")
		return
	}
	s.audit(r, "operator.mfa_setup_started", "operator", sess.OperatorID, map[string]any{"source_ip": clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_uri": uri, "issuer": mfaIssuer, "account": sess.Email})
}

// confirmOwnMFA turns two-step sign-in on with the first code from the app, and answers the recovery codes once.
func (s *server) confirmOwnMFA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "code required")
		return
	}
	code := totp.Normalize(in.Code)
	if !isTOTPCode(code) {
		jsonErr(w, http.StatusBadRequest, "code_required", "Enter the 6-digit code your authenticator app shows now.")
		return
	}
	if s.mfa == nil {
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now.")
		return
	}
	sess := sessFrom(r.Context())
	ctx, cancel := dbCtx(r)
	defer cancel()
	unlock := s.codeGate(w, r, ctx, sess.OperatorID, "confirming an authenticator")
	if unlock == nil {
		return
	}
	defer unlock()
	codes, valid, err := s.mfa.Confirm(ctx, sess.OperatorID, code)
	switch {
	case errors.Is(err, errMFANotEnrolled), errors.Is(err, errMFAAlreadyEnrolled):
		jsonErr(w, http.StatusConflict, "nothing_to_confirm", "There is no set-up waiting to be confirmed. Start again.")
		return
	case err != nil:
		slog.Error("two-step confirm failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in could not be turned on. Try again shortly.")
		return
	case !valid:
		s.wrongOwnCode(w, r, sess.OperatorID, "That code is not right. Enter the 6-digit code your authenticator app shows now.")
		return
	}
	if err := s.throttleClear(ctx, scopeMFA, sess.OperatorID); err != nil {
		slog.Warn("could not clear the code counter", "err", err)
	}
	s.audit(r, "operator.mfa_enrolled", "operator", sess.OperatorID, map[string]any{"source_ip": clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{"enrolled": true, "recovery_codes": codes})
}

// disableOwnMFA turns two-step sign-in off. It needs the password and a code from the second factor (the app's, or
// a recovery code), checked with sign-in's replay rule and throttle: a stolen session plus the password is not enough.
func (s *server) disableOwnMFA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "password and code required")
		return
	}
	code := strings.TrimSpace(in.Code)
	if code == "" || len(code) > maxMFACodeLen {
		jsonErr(w, http.StatusBadRequest, "code_required",
			"Enter the 6-digit code your authenticator app shows now, or one of your recovery codes.")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "Your password is not correct.")
		return
	}
	if s.mfa == nil {
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now.")
		return
	}
	sess := sessFrom(r.Context())
	ctx, cancel := dbCtx(r)
	defer cancel()
	unlock := s.codeGate(w, r, ctx, sess.OperatorID, "turning two-step sign-in off")
	if unlock == nil {
		return
	}
	defer unlock()
	method, _, valid, err := s.mfa.Verify(ctx, sess.OperatorID, code)
	switch {
	case errors.Is(err, errMFANotEnrolled):
		jsonErr(w, http.StatusConflict, "not_enrolled", "Two-step sign-in is not on for your account.")
		return
	case err != nil:
		slog.Error("two-step disable: code check failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in could not be turned off. Try again shortly.")
		return
	case !valid:
		s.wrongOwnCode(w, r, sess.OperatorID,
			"That code is not right. Enter the 6-digit code your authenticator app shows now, or a recovery code.")
		return
	}
	if _, err := s.mfa.Reset(ctx, sess.OperatorID); err != nil {
		slog.Error("two-step disable failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in could not be turned off. Try again shortly.")
		return
	}
	s.pending.dropOperator(sess.OperatorID)
	if err := s.throttleClear(ctx, scopeMFA, sess.OperatorID); err != nil {
		slog.Warn("could not clear the code counter", "err", err)
	}
	s.audit(r, "operator.mfa_disabled", "operator", sess.OperatorID, map[string]any{"source_ip": clientIP(r), "second_step": method})
	writeJSON(w, http.StatusOK, map[string]any{"enrolled": false})
}

// Replaces every earlier recovery code. Needs the password and a current authenticator code (never a recovery
// code): otherwise a stolen session plus the password could mint codes and bypass the second factor for good.
// The code is checked with sign-in's replay rule and counted against its wrong-code throttle.
func (s *server) newOwnRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "password required")
		return
	}
	code := totp.Normalize(in.Code)
	if !isTOTPCode(code) {
		jsonErr(w, http.StatusBadRequest, "code_required",
			"Enter the 6-digit code your authenticator app shows now. A recovery code cannot be used to make new ones.")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	if s.mfa == nil {
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now.")
		return
	}
	sess := sessFrom(r.Context())
	ctx, cancel := dbCtx(r)
	defer cancel()
	unlock := s.codeGate(w, r, ctx, sess.OperatorID, "new recovery codes")
	if unlock == nil {
		return
	}
	defer unlock()
	codes, valid, err := s.mfa.NewRecoveryCodes(ctx, sess.OperatorID, code)
	switch {
	case errors.Is(err, errMFANotEnrolled):
		jsonErr(w, http.StatusConflict, "not_enrolled", "Two-step sign-in is not set up for your account.")
		return
	case err != nil:
		slog.Error("new recovery codes failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "New recovery codes could not be made. Try again shortly.")
		return
	case !valid:
		s.wrongOwnCode(w, r, sess.OperatorID,
			"That code is not right. Enter the 6-digit code your authenticator app shows now; a code already used is not accepted again.")
		return
	}
	if err := s.throttleClear(ctx, scopeMFA, sess.OperatorID); err != nil {
		slog.Warn("could not clear the code counter", "err", err)
	}
	s.audit(r, "operator.mfa_recovery_codes_replaced", "operator", sess.OperatorID, map[string]any{"source_ip": clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func isTOTPCode(code string) bool {
	if len(code) != totp.Digits {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Ends every session of the colleague, who then signs in with the password alone until they turn two-step sign-in
// on again. Not for one's own account: turning it off oneself needs a code (disableOwnMFA).
func (s *server) resetEdgeOperatorMFA(w http.ResponseWriter, r *http.Request) {
	id, ok := operatorIDParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
		Reason   string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if sess := sessFrom(r.Context()); sess != nil && sess.OperatorID == id {
		jsonErr(w, http.StatusConflict, "not_yourself",
			"Another site administrator must reset your two-step sign-in.")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var one int
	if err := s.db.QueryRow(ctx, `SELECT 1 FROM operators WHERE id = $1`, id).Scan(&one); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	if s.mfa == nil {
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "Two-step sign-in is unavailable right now.")
		return
	}
	removed, err := s.mfa.Reset(ctx, id)
	if err != nil {
		slog.Error("two-step reset failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "The reset could not be made. Try again shortly.")
		return
	}
	s.sessions.destroyOperator(id)
	s.pending.dropOperator(id)
	_ = s.throttleClear(ctx, scopeMFA, id)
	s.audit(r, "operator.mfa_reset", "operator", id, map[string]any{
		"reason": reason, "had_authenticator": removed, "sessions_ended": true})
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed,
		"notice": "Their sessions were ended. They now sign in with their password alone and can turn two-step sign-in on again from their account."})
}

type sessionPolicy struct {
	IdleTimeoutMinutes int        `json:"idle_timeout_minutes"`
	MaxSessionHours    int        `json:"max_session_hours"`
	ConfigVersion      int64      `json:"config_version"`
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
	UpdatedBy          *string    `json:"updated_by,omitempty"`
}

func (p sessionPolicy) idle() time.Duration { return time.Duration(p.IdleTimeoutMinutes) * time.Minute }
func (p sessionPolicy) max() time.Duration  { return time.Duration(p.MaxSessionHours) * time.Hour }

// defaultSessionPolicy applies if the setting cannot be read.
var defaultSessionPolicy = sessionPolicy{IdleTimeoutMinutes: 30, MaxSessionHours: 12}

// sessionBounds mirror the table CHECKs exactly; the API sends them so the form cannot drift.
var sessionBounds = []signinBound{
	{"idle_timeout_minutes", "Sign out after inactivity", "minutes", 5, 240, 30,
		"An Admin Console session with no activity for this long ends, and the next screen asks to sign in again. An open tab that is only being looked at counts as inactive."},
	{"max_session_hours", "Longest session", "hours", 1, 24, 12,
		"However active, a session ends this long after signing in."},
}

func (s *server) readSessionPolicy(ctx context.Context) (sessionPolicy, error) {
	var p sessionPolicy
	err := s.db.QueryRow(ctx, `SELECT idle_timeout_minutes, max_session_hours, config_version, updated_at, updated_by
		FROM public.operator_session_policy_get()`).Scan(&p.IdleTimeoutMinutes, &p.MaxSessionHours, &p.ConfigVersion,
		&p.UpdatedAt, &p.UpdatedBy)
	return p, err
}

// sessionPolicyFor is read on every request (no cache); on failure the defaults apply, so a database blip does not
// sign everybody out.
func (s *server) sessionPolicyFor(ctx context.Context) sessionPolicy {
	if s.db == nil {
		return defaultSessionPolicy
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p, err := s.readSessionPolicy(ctx)
	if err != nil {
		slog.Warn("session limits unreadable; applying the defaults", "err", err)
		return defaultSessionPolicy
	}
	return p
}

func (s *server) getSessionPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	p, err := s.readSessionPolicy(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the session limits could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": p, "bounds": sessionBounds})
}

func (s *server) setSessionPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IdleTimeoutMinutes int    `json:"idle_timeout_minutes"`
		MaxSessionHours    int    `json:"max_session_hours"`
		Reason             string `json:"reason"`
		Password           string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	for _, b := range sessionBounds {
		v := in.IdleTimeoutMinutes
		if b.Key == "max_session_hours" {
			v = in.MaxSessionHours
		}
		if v < b.Min || v > b.Max {
			jsonErr(w, http.StatusBadRequest, "invalid_setting",
				b.Label+" must be between "+itoa(b.Min)+" and "+itoa(b.Max)+" "+b.Unit+".")
			return
		}
	}
	if in.IdleTimeoutMinutes > in.MaxSessionHours*60 {
		jsonErr(w, http.StatusBadRequest, "invalid_setting", "Sign out after inactivity cannot be longer than the longest session.")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	// Lengthening these is how a stolen session would be kept alive.
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	label := sess.Email
	if label == "" {
		label = sess.OperatorID
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT public.operator_session_policy_set($1,$2,$3,$4)`,
		in.IdleTimeoutMinutes, in.MaxSessionHours, label, reason).Scan(&version); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "22023") {
			jsonErr(w, http.StatusBadRequest, "invalid_setting", "a value is outside its allowed range")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "update_failed", "the session limits could not be saved")
		return
	}
	s.audit(r, "operator.session_policy_changed", "operator_session_policy", "", map[string]any{
		"idle_timeout_minutes": in.IdleTimeoutMinutes, "max_session_hours": in.MaxSessionHours,
		"config_version": version, "reason": reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"config_version": version,
		"notice":         "This applies to every open session from its next request, yours included.",
	})
}

func (s *server) listSessionPolicyChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT changed_by, change_reason, old_values, new_values, new_config_version, changed_at
		  FROM public.operator_session_policy_changes ORDER BY changed_at DESC LIMIT 50`)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the change history could not be read")
		return
	}
	defer rows.Close()
	var out []signinProtectionChange
	for rows.Next() {
		var c signinProtectionChange
		if err := rows.Scan(&c.ChangedBy, &c.Reason, &c.OldValues, &c.NewValues, &c.Version, &c.ChangedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "query_failed", "the change history could not be read")
			return
		}
		out = append(out, c)
	}
	writeList(w, out)
}
