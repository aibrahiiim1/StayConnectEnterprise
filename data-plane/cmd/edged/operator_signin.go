package main

// Console sign-in: login, password step-up, password change and their throttle.
// The throttle is durable (public.operator_signin_throttle) so restarts and parallel requests cannot reset it;
// it is per account and per source address, doubles the wait from the second failure, and locks at the configured
// threshold (public.operator_signin_protection). A correct password clears the account's counter, never the address's.
// Unknown accounts are throttled like real ones and checked against a dummy hash, so neither timing nor lockout reveals which exist.
// Every failure and lockout is audited with reason and source address, never the password.
// A weak or administrator-chosen password puts the session in must-change mode until the operator sets a new one.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	minOperatorPasswordLen = 10
	// maxOperatorPasswordLen bounds what reaches argon2; it is an input bound, not a tunable policy.
	maxOperatorPasswordLen = 1024
	maxOperatorEmailLen    = 320
	// authBodyLimit bounds bodies read before anybody is authenticated.
	authBodyLimit = 16 << 10
	// passwordVerifySlots bounds concurrent argon2 checks (64 MiB each), so a burst cannot exhaust memory.
	passwordVerifySlots = 4
	// backoffCap is the longest wait between attempts below the lockout threshold.
	backoffCap = 30 * time.Second
)

const (
	scopeAccount = "account"
	scopeIP      = "ip"
	scopeReauth  = "reauth"
)

// operatorPasswordProblem is the one password policy; "" means acceptable.
func operatorPasswordProblem(pw, email string) string {
	switch {
	case len(pw) < minOperatorPasswordLen:
		return "The password must be at least " + strconv.Itoa(minOperatorPasswordLen) + " characters."
	case len(pw) > maxOperatorPasswordLen:
		return "The password must be at most " + strconv.Itoa(maxOperatorPasswordLen) + " characters."
	case email != "" && strings.EqualFold(strings.TrimSpace(pw), strings.TrimSpace(email)):
		return "The password must not be the account name."
	}
	return ""
}

var (
	dummyHashOnce sync.Once
	dummyHash     string
	verifySem     = make(chan struct{}, passwordVerifySlots)
)

// dummyPasswordHash is checked for unknown accounts so they cost the same as a wrong password.
func dummyPasswordHash() string {
	dummyHashOnce.Do(func() {
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		h, err := hashPassword(base64.RawStdEncoding.EncodeToString(b))
		if err != nil {
			// Same parameters as hashPassword: an unparseable value would return early and leak timing.
			h = "$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}
		dummyHash = h
	})
	return dummyHash
}

var errVerifyBusy = errors.New("password verification is busy")

func verifyOperatorPassword(ctx context.Context, pw, encoded string) (bool, error) {
	if len(pw) > maxOperatorPasswordLen {
		return false, nil
	}
	select {
	case verifySem <- struct{}{}:
	case <-ctx.Done():
		return false, errVerifyBusy
	}
	defer func() { <-verifySem }()
	return verifyPassword(pw, encoded), nil
}

// keyedLocks serialises attempts per account so parallel guesses are sequential, each seeing the previous failure.
// The database counter makes it durable; this makes it exact. Striped so different accounts rarely contend.
type keyedLocks struct{ stripes [64]sync.Mutex }

func (k *keyedLocks) lock(key string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	m := &k.stripes[h.Sum32()%uint32(len(k.stripes))]
	m.Lock()
	return m.Unlock
}

var signinLocks keyedLocks

// throttleKey stores a SHA-256 of scope and value, never the value.
func throttleKey(scope, value string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(sum[:])
}

type throttleState struct {
	Failures    int
	LastFailure *time.Time
	LockedUntil *time.Time
	MaxFailures int
	Now         time.Time
}

func (s *server) throttleStateFor(ctx context.Context, scope, value string) (throttleState, error) {
	var st throttleState
	err := s.db.QueryRow(ctx, `
		SELECT failures, last_failure_at, locked_until, max_failures, now()
		  FROM public.operator_signin_throttle_state($1, $2)`,
		scope, throttleKey(scope, value)).Scan(&st.Failures, &st.LastFailure, &st.LockedUntil, &st.MaxFailures, &st.Now)
	return st, err
}

func (s *server) throttleFail(ctx context.Context, scope, value string) (failures int, lockedUntil *time.Time, newlyLocked bool, err error) {
	// newly_locked is NULL when no lockout is running; it is coalesced because scanning NULL into a bool fails.
	err = s.db.QueryRow(ctx, `SELECT failures, locked_until, COALESCE(newly_locked, false) FROM public.operator_signin_throttle_fail($1, $2)`,
		scope, throttleKey(scope, value)).Scan(&failures, &lockedUntil, &newlyLocked)
	return
}

func (s *server) throttleClear(ctx context.Context, scope, value string) error {
	_, err := s.db.Exec(ctx, `SELECT public.operator_signin_throttle_clear($1, $2)`, scope, throttleKey(scope, value))
	return err
}

// backoffFor: the first failure costs nothing; each later one doubles the wait (1 s, 2 s, 4 s ...), capped.
func backoffFor(failures int) time.Duration {
	if failures <= 1 {
		return 0
	}
	if failures > 7 {
		return backoffCap
	}
	d := time.Second << uint(failures-2)
	if d > backoffCap {
		return backoffCap
	}
	return d
}

// refusal returns how long an attempt is refused; withBackoff applies the doubling wait, and the address scope only locks.
func (st throttleState) refusal(withBackoff bool) time.Duration {
	if st.LockedUntil != nil && st.Now.Before(*st.LockedUntil) {
		return st.LockedUntil.Sub(st.Now)
	}
	if withBackoff && st.Failures > 0 && st.LastFailure != nil {
		if next := st.LastFailure.Add(backoffFor(st.Failures)); st.Now.Before(next) {
			return next.Sub(st.Now)
		}
	}
	return 0
}

func waitWords(d time.Duration) string {
	if d < time.Minute {
		sec := int((d + time.Second - 1) / time.Second)
		if sec <= 1 {
			return "a second"
		}
		return strconv.Itoa(sec) + " seconds"
	}
	m := int((d + time.Minute - 1) / time.Minute)
	if m == 1 {
		return "a minute"
	}
	return strconv.Itoa(m) + " minutes"
}

func writeThrottled(w http.ResponseWriter, wait time.Duration, what string) {
	sec := int((wait + time.Second - 1) / time.Second)
	if sec < 1 {
		sec = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(sec))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":               "too_many_attempts",
		"message":             "Too many failed " + what + ". Wait " + waitWords(wait) + " and try again.",
		"retry_after_seconds": sec,
	})
}

// recordFailure counts a failure on every scope and audits it and any lockout start; never the password.
func (s *server) recordFailure(r *http.Request, action, lockAction, operatorID, reason string, scopes map[string]string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ip := clientIP(r)
	payload := map[string]any{"reason": reason, "source_ip": ip}
	for _, scope := range []string{scopeAccount, scopeReauth, scopeMFA, scopeIP} {
		value, ok := scopes[scope]
		if !ok {
			continue
		}
		n, until, newly, err := s.throttleFail(ctx, scope, value)
		if err != nil {
			slog.Error("sign-in throttle could not record a failure", "scope", scope, "err", err)
			continue
		}
		if scope != scopeIP {
			payload["failures"] = n
		}
		if newly && until != nil {
			s.audit(r, lockAction, "operator", operatorID, map[string]any{
				"scope": scope, "source_ip": ip, "locked_until": until.UTC(),
			})
		}
	}
	if action == "operator.reauth_failed" {
		payload["action"] = r.Method + " " + r.URL.Path
	}
	s.audit(r, action, "operator", operatorID, payload)
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, authBodyLimit)
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Email) == "" || in.Password == "" ||
		len(in.Email) > maxOperatorEmailLen || len(in.Password) > maxOperatorPasswordLen {
		jsonErr(w, http.StatusBadRequest, "bad_request", "email and password required")
		return
	}
	account := strings.ToLower(strings.TrimSpace(in.Email))
	ip := clientIP(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	unlock := signinLocks.lock("login\x00" + account)
	defer unlock()

	// Fail closed: an unreadable throttle must not allow unlimited guesses.
	stIP, err1 := s.throttleStateFor(ctx, scopeIP, ip)
	stAcct, err2 := s.throttleStateFor(ctx, scopeAccount, account)
	if err1 != nil || err2 != nil {
		slog.Error("sign-in throttle unreadable; refusing sign-in", "ip_err", err1, "account_err", err2)
		jsonErr(w, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	if wait := stIP.refusal(false); wait > 0 {
		slog.Warn("sign-in refused: source address locked", "source_ip", ip)
		writeThrottled(w, wait, "sign-in attempts from this address")
		return
	}
	if wait := stAcct.refusal(true); wait > 0 {
		slog.Warn("sign-in refused: account throttled", "source_ip", ip)
		writeThrottled(w, wait, "sign-in attempts")
		return
	}

	var id, display, hash, status string
	var mustChange bool
	err := s.db.QueryRow(ctx, `
        SELECT id::text, COALESCE(display_name,''), COALESCE(password_hash,''), status, must_change_password
          FROM operators WHERE lower(email) = $1
         ORDER BY (status = 'active') DESC, created_at
         LIMIT 1`, account).Scan(&id, &display, &hash, &status, &mustChange)
	known := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("operator lookup failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "signin_unavailable", "Sign-in is unavailable right now. Try again shortly.")
		return
	}
	cmp := hash
	if !known || hash == "" {
		cmp = dummyPasswordHash()
	}
	ok, verr := verifyOperatorPassword(ctx, in.Password, cmp)
	if verr != nil {
		jsonErr(w, http.StatusServiceUnavailable, "signin_busy", "Sign-in is busy. Try again in a moment.")
		return
	}
	reason := ""
	switch {
	case !known:
		reason = "unknown_account"
	case hash == "":
		reason = "no_password"
	case !ok:
		reason = "wrong_password"
	case status != "active":
		reason = "account_" + status
	}
	if reason != "" {
		s.recordFailure(r, "operator.login_failed", "operator.login_locked", id, reason,
			map[string]string{scopeAccount: account, scopeIP: ip})
		// Same answer for every failure, so it never reveals which check failed.
		jsonErr(w, http.StatusUnauthorized, "unauthenticated", "invalid credentials")
		return
	}
	roles, err := s.loadRoles(ctx, id)
	if err != nil || len(roles) == 0 {
		s.audit(r, "operator.login_failed", "operator", id, map[string]any{"reason": "no_roles", "source_ip": ip})
		jsonErr(w, http.StatusForbidden, "forbidden", "no roles assigned")
		return
	}
	if err := s.throttleClear(ctx, scopeAccount, account); err != nil {
		slog.Warn("could not clear the account sign-in counter", "err", err)
	}
	// A password that does not meet the policy must be replaced before the console is usable (catches a seeded admin/admin).
	if !mustChange && operatorPasswordProblem(in.Password, "") != "" {
		if _, err := s.db.Exec(ctx, `UPDATE operators SET must_change_password = true, updated_at = now() WHERE id = $1`, id); err != nil {
			slog.Error("could not mark a weak operator password for change", "err", err)
		}
		mustChange = true
		s.audit(r, "operator.password_change_required", "operator", id, map[string]any{"reason": "weak_password"})
	}
	// No session yet: the authenticator code opens it.
	s.beginSecondStep(w, r, ctx, &pendingSignin{OperatorID: id, Email: in.Email, DisplayName: display, Roles: roles,
		MustChangePassword: mustChange, PasswordFP: passwordFingerprint(hash)})
}

func (s *server) setSessionCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.secure,
		// The browser's bound only; requireAuth enforces the session limits.
		Expires: time.Now().Add(sessionStoreCap),
	})
}

func (s *server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.secure})
}

// logout ends only the caller's own session. Without a live session it answers 401 but still clears the cookie, since
// the console sends a stale cookie here to escape a redirect loop after edged forgot its sessions.
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie(sessionCookie)
	var sess *session
	if err == nil && ck.Value != "" {
		sess = s.sessions.peek(ck.Value)
	}
	s.clearSessionCookie(w)
	if sess == nil {
		jsonErr(w, http.StatusUnauthorized, "unauthenticated", "no session")
		return
	}
	s.sessions.destroy(ck.Value)
	s.audit(r.WithContext(context.WithValue(r.Context(), sessKey, sess)), "operator.logout", "operator", sess.OperatorID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// mustChangeAllowed lists the only routes a must-change session may call.
func mustChangeAllowed(r *http.Request) bool {
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/edge/v1/auth/whoami", "/edge/v1/auth/password", "/edge/v1/auth/logout":
		return true
	}
	return false
}

type reauthNoteKeyT struct{}

var reauthNoteKey reauthNoteKeyT

// reauthNote carries why a step-up was refused from reauth to reauthRefused, so callers answer 429 for a throttled operator.
type reauthNote struct {
	retryAfter  time.Duration
	unavailable bool
}

func reauthNoteFrom(ctx context.Context) *reauthNote {
	n, _ := ctx.Value(reauthNoteKey).(*reauthNote)
	return n
}

// reauth re-verifies the operator's password before a high-risk change; throttled per operator and address, failures audited.
func (s *server) reauth(r *http.Request, password string) bool {
	sess := sessFrom(r.Context())
	if sess == nil || password == "" || len(password) > maxOperatorPasswordLen {
		return false
	}
	note := reauthNoteFrom(r.Context())
	ip := clientIP(r)
	ctx, cancel := dbCtx(r)
	defer cancel()

	unlock := signinLocks.lock("reauth\x00" + sess.OperatorID)
	defer unlock()

	stIP, err1 := s.throttleStateFor(ctx, scopeIP, ip)
	stOp, err2 := s.throttleStateFor(ctx, scopeReauth, sess.OperatorID)
	if err1 != nil || err2 != nil {
		slog.Error("step-up throttle unreadable; refusing step-up", "ip_err", err1, "operator_err", err2)
		if note != nil {
			note.unavailable = true
		}
		return false
	}
	if wait := max(stIP.refusal(false), stOp.refusal(true)); wait > 0 {
		if note != nil {
			note.retryAfter = wait
		}
		return false
	}
	var hash, status string
	if err := s.db.QueryRow(ctx,
		`SELECT COALESCE(password_hash,''), status FROM operators WHERE id=$1`, sess.OperatorID).
		Scan(&hash, &status); err != nil {
		return false
	}
	cmp := hash
	if cmp == "" {
		cmp = dummyPasswordHash()
	}
	ok, verr := verifyOperatorPassword(ctx, password, cmp)
	if verr != nil {
		if note != nil {
			note.unavailable = true
		}
		return false
	}
	reason := ""
	switch {
	case hash == "":
		reason = "no_password"
	case !ok:
		reason = "wrong_password"
	case status != "active":
		reason = "account_" + status
	}
	if reason != "" {
		s.recordFailure(r, "operator.reauth_failed", "operator.reauth_locked", sess.OperatorID, reason,
			map[string]string{scopeReauth: sess.OperatorID, scopeIP: ip})
		return false
	}
	if err := s.throttleClear(ctx, scopeReauth, sess.OperatorID); err != nil {
		slog.Warn("could not clear the step-up counter", "err", err)
	}
	return true
}

// reauthRefused answers 429 while throttled, 503 when the check could not run, otherwise 401 with the caller's wording.
func (s *server) reauthRefused(w http.ResponseWriter, r *http.Request, msg string) {
	if n := reauthNoteFrom(r.Context()); n != nil {
		if n.retryAfter > 0 {
			writeThrottled(w, n.retryAfter, "password confirmations")
			return
		}
		if n.unavailable {
			jsonErr(w, http.StatusServiceUnavailable, "reauth_unavailable",
				"Password confirmation is unavailable right now. Try again shortly.")
			return
		}
	}
	jsonErr(w, http.StatusUnauthorized, "reauth_required", msg)
}

// changeOwnPassword ends every session of the operator (how someone takes back an account they suspect is in use
// elsewhere) and gives the caller a fresh one.
func (s *server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, authBodyLimit)
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "current_password and new_password are required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil {
		jsonErr(w, http.StatusUnauthorized, "unauthenticated", "login required")
		return
	}
	if msg := operatorPasswordProblem(in.New, sess.Email); msg != "" {
		jsonErr(w, http.StatusBadRequest, "weak_password", msg)
		return
	}
	if in.New == in.Current {
		jsonErr(w, http.StatusBadRequest, "same_password", "The new password must be different from the current one.")
		return
	}
	if !s.reauth(r, in.Current) {
		s.reauthRefused(w, r, "Your current password is not correct.")
		return
	}
	hash, err := hashPassword(in.New)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	ct, err := s.db.Exec(ctx, `UPDATE operators SET password_hash = $2, must_change_password = false, updated_at = now()
		WHERE id = $1 AND status = 'active'`, sess.OperatorID, hash)
	if err != nil || ct.RowsAffected() == 0 {
		jsonErr(w, http.StatusInternalServerError, "internal", "the password could not be changed")
		return
	}
	wasForced := sess.MustChangePassword
	s.sessions.destroyOperator(sess.OperatorID)
	s.pending.dropOperator(sess.OperatorID)
	fresh := &session{OperatorID: sess.OperatorID, Email: sess.Email, DisplayName: sess.DisplayName, Roles: sess.Roles,
		CreatedAt: sess.CreatedAt}
	s.setSessionCookie(w, s.sessions.create(fresh))
	s.audit(r, "operator.password_changed", "operator", sess.OperatorID, map[string]any{
		"self": true, "was_required": wasForced, "source_ip": clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "changed", "must_change_password": false,
		"notice": "Your password was changed. Any other place you were signed in has been signed out.",
	})
}

// sameOrigin refuses a state-changing request whose Origin (or Referer) names another site, in addition to the
// SameSite=Lax cookie. edged's own Host is 127.0.0.1:8090 behind Caddy and the Next proxy, so the browser's address
// arrives as X-Forwarded-Host, which a cross-site browser request cannot forge. A request with neither header is
// not from a browser page (curl, appliance scripts) and CSRF is a browser attack, so it passes.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !requestIsSameOrigin(r) {
			jsonErr(w, http.StatusForbidden, "cross_origin", "This request did not come from the Admin Console.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestIsSameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
		if src == "" {
			return true
		}
	}
	if src == "null" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Host)
	allowed := []string{r.Host}
	for _, h := range r.Header.Values("X-Forwarded-Host") {
		for _, part := range strings.Split(h, ",") {
			allowed = append(allowed, part)
		}
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if a == host || stripDefaultPort(a, u.Scheme) == stripDefaultPort(host, u.Scheme) {
			return true
		}
	}
	return false
}

func stripDefaultPort(hostport, scheme string) string {
	switch {
	case scheme == "https" && strings.HasSuffix(hostport, ":443"):
		return strings.TrimSuffix(hostport, ":443")
	case scheme == "http" && strings.HasSuffix(hostport, ":80"):
		return strings.TrimSuffix(hostport, ":80")
	}
	return hostport
}
