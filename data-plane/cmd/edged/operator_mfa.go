package main

// ADMIN CONSOLE SECOND STEP AND SESSION LIMITS (migration 0132; GDPR remediation plan C10, decision D-C10;
// NIST SP 800-63B, OWASP ASVS V2/V3).
//
// SIGNING IN IS TWO STEPS. The password (login, operator_signin.go) no longer opens a session: it opens a short
// "password accepted" state, held here for five minutes under its own cookie, and the session is created only
// when the second step succeeds:
//
//	POST /auth/login        {email, password}  -> {second_step: "code" | "enrol"}
//	POST /auth/login/enrol                      -> {secret, otpauth_uri}   (first sign-in: the QR code)
//	POST /auth/login/code   {code}              -> the session; on first sign-in also the 10 recovery codes
//
// A code is the 6 digits from the authenticator app, or one of the recovery codes. Wrong codes are throttled per
// operator with the same limits as wrong passwords (scope 'mfa') and per source address, and audited
// (operator.mfa_failed / operator.mfa_locked). The secret is sealed by scd, which holds the key; edged never sees
// it after the QR code and cannot open it.
//
// THE PASSWORD STEP-UP STAYS A PASSWORD. Re-confirming inside a session that was opened with both factors may
// use one (NIST SP 800-63B section 7.2), so the screens that already ask for the password are unchanged.
//
// SESSIONS END WHEN IDLE OR OLD. requireAuth ends a session after public.operator_session_policy's idle minutes
// without activity, and its hours after sign-in whatever the activity (defaults 30 minutes and 12 hours). The
// console's background whoami check does not count as activity; otherwise an open tab would never be idle.
//
// LOSING THE PHONE: a recovery code signs in once each; a site administrator can reset a colleague's second
// step (password step-up, reason, audited, every session of theirs ended); the last administrator can be reset
// on the appliance itself with `edged reset-mfa --email`.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	// pendingSigninCookie carries the "password accepted" state between the two steps. Its own name, so the
	// console's middleware (which looks for the session cookie) still shows the sign-in page.
	pendingSigninCookie = "sc_edge_signin"
	// pendingSigninTTL is how long the second step may take after the password, setting up the app included.
	pendingSigninTTL = 5 * time.Minute
	mfaIssuer        = "StayConnect Hotel Admin"
	maxMFACodeLen    = 32
)

// ----- the authenticator service (scd) ------------------------------------------------------------------------

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

// mfaService is what edged needs from the holder of the authenticator key. In production it is scd; the
// sign-in tests give a stand-in that computes real codes.
type mfaService interface {
	Status(ctx context.Context, operatorID string) (mfaStatus, error)
	Enrol(ctx context.Context, operatorID, account string) (secret, uri string, err error)
	// Confirm proves a freshly set-up authenticator with its first code and makes the recovery codes.
	Confirm(ctx context.Context, operatorID, code string) (recoveryCodes []string, ok bool, err error)
	// Verify checks a code or a recovery code against a confirmed authenticator.
	Verify(ctx context.Context, operatorID, code string) (method string, recoveryLeft int, ok bool, err error)
	// NewRecoveryCodes replaces the set, but only against a current authenticator code (never a recovery code),
	// spent with the same replay rule as sign-in. ok is false for a wrong or already-used code.
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

// post calls one scd route and decodes its answer; 409 and 503 become the errors the handlers act on.
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

// ----- the "password accepted" state --------------------------------------------------------------------------

type pendingSignin struct {
	OperatorID         string
	Email              string
	DisplayName        string
	Roles              []string
	MustChangePassword bool
	// Enrol: no authenticator is confirmed yet; the second step sets one up.
	Enrol   bool
	Expires time.Time
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

func (p *pendingSignins) get(tok string) *pendingSignin {
	p.mu.Lock()
	defer p.mu.Unlock()
	ps, ok := p.m[tok]
	if !ok || time.Now().After(ps.Expires) {
		delete(p.m, tok)
		return nil
	}
	return ps
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

// pendingFrom is the caller's "password accepted" state, or a 401 telling the console to start again.
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

// beginSecondStep replaces the session the password used to open (login): it answers which second step comes.
func (s *server) beginSecondStep(w http.ResponseWriter, r *http.Request, ctx context.Context, ps *pendingSignin) {
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
	ps.Enrol = !st.Enrolled
	s.setPendingCookie(w, s.pending.create(ps))
	step := "code"
	if ps.Enrol {
		step = "enrol"
	}
	writeJSON(w, http.StatusOK, map[string]any{"second_step": step})
}

// POST /auth/login/enrol: the QR code for an operator who has no authenticator yet.
func (s *server) loginEnrol(w http.ResponseWriter, r *http.Request) {
	_, ps, ok := s.pendingFrom(w, r)
	if !ok {
		return
	}
	if !ps.Enrol {
		jsonErr(w, http.StatusConflict, "already_enrolled", "An authenticator is already set up. Enter its code.")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	secret, uri, err := s.mfa.Enrol(ctx, ps.OperatorID, ps.Email)
	switch {
	case errors.Is(err, errMFAAlreadyEnrolled):
		ps.Enrol = false
		jsonErr(w, http.StatusConflict, "already_enrolled", "An authenticator is already set up. Enter its code.")
		return
	case err != nil:
		slog.Error("sign-in: authenticator set-up failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_uri": uri, "issuer": mfaIssuer, "account": ps.Email})
}

// POST /auth/login/code {code}: the second step. Creates the session.
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

	var recovery []string
	method, left := "totp", -1
	var valid bool
	var err error
	if ps.Enrol {
		recovery, valid, err = s.mfa.Confirm(ctx, ps.OperatorID, in.Code)
		method = "enrolled"
	} else {
		method, left, valid, err = s.mfa.Verify(ctx, ps.OperatorID, in.Code)
	}
	switch {
	case errors.Is(err, errMFANotEnrolled), errors.Is(err, errMFAAlreadyEnrolled):
		// The authenticator changed between the two steps (a reset, or set up elsewhere): start again.
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
	// The account may have been disabled while the code was being typed.
	var status string
	if err := s.db.QueryRow(ctx, `SELECT status FROM operators WHERE id = $1`, ps.OperatorID).Scan(&status); err != nil || status != "active" {
		s.pending.drop(tok)
		s.clearPendingCookie(w)
		jsonErr(w, http.StatusUnauthorized, "unauthenticated", "invalid credentials")
		return
	}
	s.pending.drop(tok)
	s.clearPendingCookie(w)
	sess := &session{OperatorID: ps.OperatorID, Email: ps.Email, DisplayName: ps.DisplayName, Roles: ps.Roles,
		MustChangePassword: ps.MustChangePassword}
	s.setSessionCookie(w, s.sessions.create(sess))
	ar := r.WithContext(context.WithValue(r.Context(), sessKey, sess))
	if ps.Enrol {
		s.audit(ar, "operator.mfa_enrolled", "operator", ps.OperatorID, map[string]any{"source_ip": ip})
	}
	if method == "recovery_code" {
		s.audit(ar, "operator.mfa_recovery_code_used", "operator", ps.OperatorID, map[string]any{
			"source_ip": ip, "recovery_codes_left": left})
	}
	s.audit(ar, "operator.login", "operator", ps.OperatorID, map[string]any{
		"source_ip": ip, "must_change_password": ps.MustChangePassword, "second_step": method})
	out := map[string]any{"operator_id": ps.OperatorID, "roles": ps.Roles, "must_change_password": ps.MustChangePassword}
	if recovery != nil {
		out["recovery_codes"] = recovery
	}
	if left >= 0 && method == "recovery_code" {
		out["recovery_codes_left"] = left
	}
	writeJSON(w, http.StatusOK, out)
}

// ----- the operator's own second step (Your account > Two-step sign-in) ----------------------------------------

// GET /auth/mfa
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

// POST /auth/mfa/recovery-codes {password, code}: a new set, replacing every earlier code.
//
// THE PASSWORD AND A CURRENT AUTHENTICATOR CODE (Product-Owner decision D-R3). With the password alone, a stolen
// session plus the password could mint codes that later sign in with the password alone -- the second factor
// bypassed for good. The code is the authenticator's 6 digits, never a recovery code; it is checked by scd with
// sign-in's replay rule and counted against sign-in's wrong-code throttle and audit.
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
	ip := clientIP(r)
	ctx, cancel := dbCtx(r)
	defer cancel()
	unlock := signinLocks.lock("mfa\x00" + sess.OperatorID)
	defer unlock()
	stIP, err1 := s.throttleStateFor(ctx, scopeIP, ip)
	stOp, err2 := s.throttleStateFor(ctx, scopeMFA, sess.OperatorID)
	if err1 != nil || err2 != nil {
		slog.Error("code throttle unreadable; refusing new recovery codes", "ip_err", err1, "operator_err", err2)
		jsonErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "New recovery codes could not be made. Try again shortly.")
		return
	}
	if wait := max(stIP.refusal(false), stOp.refusal(true)); wait > 0 {
		writeThrottled(w, wait, "codes")
		return
	}
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
		s.recordFailure(r, "operator.mfa_failed", "operator.mfa_locked", sess.OperatorID, "wrong_code",
			map[string]string{scopeMFA: sess.OperatorID, scopeIP: ip})
		jsonErr(w, http.StatusUnauthorized, "wrong_code",
			"That code is not right. Enter the 6-digit code your authenticator app shows now; a code already used is not accepted again.")
		return
	}
	if err := s.throttleClear(ctx, scopeMFA, sess.OperatorID); err != nil {
		slog.Warn("could not clear the code counter", "err", err)
	}
	s.audit(r, "operator.mfa_recovery_codes_replaced", "operator", sess.OperatorID, map[string]any{"source_ip": ip})
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// isTOTPCode reports whether a normalised code is the authenticator's 6 digits (a recovery code never is).
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

// ----- an administrator resetting a colleague's second step ----------------------------------------------------

// POST /operators/{id}/reset-mfa {password, reason}. The colleague sets up a new authenticator at their next
// sign-in. Every session of theirs ends. Not for one's own account: a stolen session plus the password must not
// be enough to remove the second factor.
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
		"notice": "Their sessions were ended. They set up a new authenticator the next time they sign in."})
}

// ----- the session limits ----------------------------------------------------------------------------------------

type sessionPolicy struct {
	IdleTimeoutMinutes int        `json:"idle_timeout_minutes"`
	MaxSessionHours    int        `json:"max_session_hours"`
	ConfigVersion      int64      `json:"config_version"`
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
	UpdatedBy          *string    `json:"updated_by,omitempty"`
}

func (p sessionPolicy) idle() time.Duration { return time.Duration(p.IdleTimeoutMinutes) * time.Minute }
func (p sessionPolicy) max() time.Duration  { return time.Duration(p.MaxSessionHours) * time.Hour }

// defaultSessionPolicy mirrors 0132's defaults; it is what applies if the setting cannot be read.
var defaultSessionPolicy = sessionPolicy{IdleTimeoutMinutes: 30, MaxSessionHours: 12}

// sessionBounds mirror the CHECKs in migration 0132 exactly; the API sends them so the form cannot drift.
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

// sessionPolicyFor is what requireAuth enforces. Read on every request (no cache, §0C), and the defaults if the
// read fails: a database blip must not sign everybody out, and the defaults are never looser than the bounds.
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
