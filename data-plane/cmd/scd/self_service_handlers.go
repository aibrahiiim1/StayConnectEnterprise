package main

// Email accounts: the routes. /v1/auth/account/{register,status,resend,grace,verify,forgot,reset} are called by portald only, with the device as
// portald derives it from the kernel; /v1/admin/accounts/{id}/resend-verification by edged only. Replies never contain a password, a hash or an
// emailed token.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/passwordhash"
)

func decodeSelfServiceBody(r *http.Request, dst any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(dst)
}

func ssErr(w http.ResponseWriter, code int, errCode string) {
	writeJSON(w, code, map[string]any{"error": errCode})
}

// Both ip and mac are required: the account token is bound to the device.
func guestDevice(ipText, macText string) (net.IP, net.HardwareAddr, bool) {
	ip := net.ParseIP(strings.TrimSpace(ipText))
	mac, err := net.ParseMAC(strings.TrimSpace(macText))
	if ip == nil || ip.To4() == nil || err != nil {
		return nil, nil, false
	}
	return ip, mac, true
}

// Deliberately conservative: an address the portal accepts is one every mail server accepts, and it is the username for life.
var (
	emailLocal       = regexp.MustCompile(`^[a-z0-9!#$%&'*+/=?^_{|}~.-]{1,64}$`)
	emailDomainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// lower(trimmed email) when plausible, else empty; the username of a self-service account IS this value (the database CHECK insists on it).
func normalizeAccountEmail(raw string) string {
	e := strings.ToLower(strings.TrimSpace(raw))
	if len(e) < 6 || len(e) > 254 || strings.Count(e, "@") != 1 {
		return ""
	}
	at := strings.IndexByte(e, '@')
	local, domain := e[:at], e[at+1:]
	if !emailLocal.MatchString(local) || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") ||
		strings.Contains(local, "..") {
		return ""
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return ""
	}
	for _, l := range labels {
		if !emailDomainLabel.MatchString(l) {
			return ""
		}
	}
	if tld := labels[len(labels)-1]; len(tld) < 2 || strings.Trim(tld, "0123456789") == "" {
		return ""
	}
	return e
}

// Charges the in-memory login limiter and the durable throttle under the ACCOUNT method, namespaced by action so registration and sign-in
// storms on one address count apart. false = refused (reply written).
func (s *server) selfServiceThrottle(w http.ResponseWriter, r *http.Request, action, identity string, ip net.IP, mac net.HardwareAddr) bool {
	id := action + ":" + identity
	if s.loginRL != nil && !s.loginRL.allow(id, ip.String(), mac.String()) {
		ssErr(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")
		return false
	}
	if ok, retry := s.throttleGuard(r.Context(), "account", ip, mac, id); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		ssErr(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")
		return false
	}
	return true
}

var (
	errEmailTaken        = errors.New("email taken")
	errRegistrationLimit = errors.New("registration limit")
)

// Registration is entirely local (row, sign-in, package step), so it works offline; the verification email is queued, never waited for.
func (s *server) accountRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		IP       string `json:"ip"`
		MAC      string `json:"mac"`
		// Lang is accepted for the portal; messages are English until its catalogue has the email texts.
		Lang string `json:"lang"`
	}
	if err := decodeSelfServiceBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	// Email accounts are licensed as the email_otp module (meaning "email sign-in").
	if !s.licenseGate(w, licstate.FeatEmailOTP) {
		return
	}
	ip, mac, ok := guestDevice(req.IP, req.MAC)
	if !ok {
		httpErr(w, http.StatusBadRequest, "device identity required")
		return
	}
	if s.refuseDisabledMethod(w, r, guestMethodEmailAccount) {
		return
	}
	email := normalizeAccountEmail(req.Email)
	throttleID := email
	if throttleID == "" {
		throttleID = strings.ToLower(strings.TrimSpace(req.Email))
	}
	if !s.selfServiceThrottle(w, r, "register", throttleID, ip, mac) {
		return
	}
	if email == "" {
		ssErr(w, http.StatusUnprocessableEntity, "invalid_email")
		return
	}
	if passwordhash.CheckSelfService(req.Password, email) != nil {
		ssErr(w, http.StatusUnprocessableEntity, "weak_password")
		return
	}
	if s.db == nil {
		ssErr(w, http.StatusServiceUnavailable, "IAMV2_UNAVAILABLE")
		return
	}
	nc := s.resolveNetwork(r.Context(), ip)
	if nc.NetworkID == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "NO_GUEST_NETWORK", "authority": "iam_v2"})
		return
	}
	set, err := s.selfServiceSettingsRead(r.Context())
	if err != nil {
		// Registering without these would mean registering under rules nobody chose.
		slog.Error("email accounts: settings unreadable; refusing registration", "err", err)
		ssErr(w, http.StatusServiceUnavailable, "SETTINGS_UNAVAILABLE")
		return
	}
	hash, err := passwordhash.Hash(req.Password)
	if err != nil {
		slog.Error("email accounts: hash failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "registration failed")
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// One registration at a time per device: the cap is count-then-insert, so parallel requests could both take the last slot.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('self_service_register'), hashtext($1))`,
			s.siteID+"|"+mac.String()); err != nil {
			return err
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.guest_access_accounts
		     WHERE tenant_id = $1 AND site_id = $2 AND lower(username) = $3)`, s.tenID, s.siteID, email).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return errEmailTaken
		}
		var today int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_access_accounts
		     WHERE tenant_id = $1 AND site_id = $2 AND self_registered AND created_by_device_mac = $3::macaddr
		       AND created_at > now() - interval '24 hours'`, s.tenID, s.siteID, mac.String()).Scan(&today); err != nil {
			return err
		}
		if today >= set.RegistrationsPerDevicePerDay {
			return errRegistrationLimit
		}
		var deadline *time.Time
		if set.VerificationRequired {
			d := time.Now().Add(time.Duration(set.DeadlineHours) * time.Hour)
			deadline = &d
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts
		      (tenant_id, site_id, username, password_hash, enabled, self_registered, verification_deadline, created_by_device_mac)
		    VALUES ($1, $2, $3, $4, true, true, $5, $6::macaddr)
		    ON CONFLICT (tenant_id, site_id, (lower(username))) DO NOTHING
		    RETURNING id::text`, s.tenID, s.siteID, email, hash, deadline, mac.String()).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errEmailTaken // lost a race with another device registering the same address
		}
		if err != nil {
			return err
		}
		if set.VerificationRequired {
			return queueEmailToken(ctx, tx, s.tenID, s.siteID, id, tokenPurposeVerify, verifyTokenExpiry(deadline, time.Now()))
		}
		return nil
	})
	switch {
	case errors.Is(err, errEmailTaken):
		ssErr(w, http.StatusConflict, "email_taken")
		return
	case errors.Is(err, errRegistrationLimit):
		ssErr(w, http.StatusTooManyRequests, "registration_limit")
		return
	case err != nil:
		slog.Error("email accounts: registration failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "registration failed")
		return
	}
	s.kickSelfServiceOutbox()
	// Signed in at once through the one ACCOUNT authority (same auth context, device and network pinning as a returning client).
	s.authorizeViaIAMv2(w, withMethodKey(r, keyEmailAccount), iamv2.MethodAccount, iamv2.Request{Username: email, Secret: req.Password,
		Credential: iamv2.CredentialEmailAccount, Device: iamv2.DeviceContext{MAC: mac.String()}}, ip)
}

func (s *server) accountFromToken(w http.ResponseWriter, r *http.Request, tok, ipText, macText string) (selfServiceAccountRow, net.IP, net.HardwareAddr, bool) {
	ip, mac, ok := guestDevice(ipText, macText)
	if !ok {
		httpErr(w, http.StatusBadRequest, "device identity required")
		return selfServiceAccountRow{}, nil, nil, false
	}
	id := s.parseAccountToken(tok, mac.String(), time.Now())
	if id == "" || s.db == nil {
		ssErr(w, http.StatusUnauthorized, "invalid_account_token")
		return selfServiceAccountRow{}, nil, nil, false
	}
	a, err := s.loadSelfServiceAccount(r.Context(), s.db, id)
	if err != nil || !a.SelfRegistered || !a.Enabled {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("email accounts: account unreadable", "err", err)
		}
		ssErr(w, http.StatusUnauthorized, "invalid_account_token")
		return selfServiceAccountRow{}, nil, nil, false
	}
	return a, ip, mac, true
}

func (s *server) accountStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountToken string `json:"account_token"`
		IP           string `json:"ip"`
		MAC          string `json:"mac"`
	}
	if err := decodeSelfServiceBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	a, _, _, ok := s.accountFromToken(w, r, req.AccountToken, req.IP, req.MAC)
	if !ok {
		return
	}
	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{"account": s.accountView(r.Context(), a, s.selfServiceSettingsOrDefault(r.Context()), now)})
}

func (s *server) accountResend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountToken string `json:"account_token"`
		IP           string `json:"ip"`
		MAC          string `json:"mac"`
	}
	if err := decodeSelfServiceBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	a, ip, mac, ok := s.accountFromToken(w, r, req.AccountToken, req.IP, req.MAC)
	if !ok {
		return
	}
	if s.refuseDisabledMethod(w, r, guestMethodEmailAccount) {
		return
	}
	if !s.selfServiceThrottle(w, r, "resend", a.ID, ip, mac) {
		return
	}
	s.writeResendOutcome(w, s.resendVerification(r.Context(), a.ID))
}

type resendOutcome struct {
	code       int
	err        string
	retryAfter int
}

func (s *server) writeResendOutcome(w http.ResponseWriter, o resendOutcome) {
	switch {
	case o.err == "resend_wait":
		w.Header().Set("Retry-After", strconv.Itoa(o.retryAfter))
		writeJSON(w, o.code, map[string]any{"error": o.err, "retry_after_seconds": o.retryAfter})
	case o.err == "resend_limit":
		if o.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(o.retryAfter))
		}
		writeJSON(w, o.code, map[string]any{"error": o.err, "retry_after_seconds": o.retryAfter})
	case o.err != "":
		ssErr(w, o.code, o.err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
	}
}

// Cooldown and daily allowance are checked and queued in one transaction, serialised per account, so two clicks cannot both pass.
func (s *server) resendVerification(ctx context.Context, accountID string) resendOutcome {
	set, err := s.selfServiceSettingsRead(ctx)
	if err != nil {
		slog.Warn("email accounts: settings unreadable; resend refused", "err", err)
		return resendOutcome{code: http.StatusServiceUnavailable, err: "SETTINGS_UNAVAILABLE"}
	}
	var out resendOutcome
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('self_service_resend'), hashtext($1))`, accountID); err != nil {
			return err
		}
		a, err := s.loadSelfServiceAccount(ctx, tx, accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			out = resendOutcome{code: http.StatusNotFound, err: "unknown_account"}
			return nil
		}
		if err != nil {
			return err
		}
		if !a.SelfRegistered {
			out = resendOutcome{code: http.StatusConflict, err: "not_self_service"}
			return nil
		}
		if a.VerifiedAt != nil {
			out = resendOutcome{code: http.StatusConflict, err: "already_verified"}
			return nil
		}
		st, err := s.verifyTokenStats(ctx, tx, accountID, tokenPurposeVerify)
		if err != nil {
			return err
		}
		now := time.Now()
		if wait, limited := resendWait(st.newest, st.oldestInDay, st.inDay, set, now); wait > 0 {
			code := "resend_wait"
			if limited {
				code = "resend_limit"
			}
			out = resendOutcome{code: http.StatusTooManyRequests, err: code, retryAfter: ceilSeconds(wait)}
			return nil
		}
		return queueEmailToken(ctx, tx, s.tenID, s.siteID, accountID, tokenPurposeVerify, verifyTokenExpiry(a.Deadline, now))
	})
	if err != nil {
		slog.Error("email accounts: resend failed", "err", err)
		return resendOutcome{code: http.StatusInternalServerError, err: "resend_failed"}
	}
	if out.err == "" {
		s.kickSelfServiceOutbox()
	}
	return out
}

// Staff help is not a way around the limits that keep a mailbox from being flooded: same cooldown and daily allowance as the client.
func (s *server) adminAccountResendVerification(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if !uuidShape.MatchString(id) {
		ssErr(w, http.StatusNotFound, "unknown_account")
		return
	}
	// Optional {"operator": ...} is logged here; edged authenticates the operator and writes the audit record. An empty body is accepted.
	var req struct {
		Operator string `json:"operator"`
	}
	if r.ContentLength != 0 {
		if err := decodeSelfServiceBody(r, &req); err != nil {
			httpErr(w, http.StatusBadRequest, "bad body")
			return
		}
	}
	if s.db == nil {
		ssErr(w, http.StatusServiceUnavailable, "no database")
		return
	}
	o := s.resendVerification(r.Context(), id)
	if o.err == "" {
		op := strings.TrimSpace(req.Operator)
		if len(op) > 200 {
			op = op[:200]
		}
		slog.Info("email accounts: an operator queued a verification email", "operator", op)
	}
	s.writeResendOutcome(w, o)
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Grace requires the password AGAIN (not the account token) and lifts only the verification gate: no entitlement is created, extended or restored.
func (s *server) accountGrace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		IP       string `json:"ip"`
		MAC      string `json:"mac"`
	}
	if err := decodeSelfServiceBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if !s.licenseGate(w, licstate.FeatEmailOTP) {
		return
	}
	ip, mac, ok := guestDevice(req.IP, req.MAC)
	if !ok {
		httpErr(w, http.StatusBadRequest, "device identity required")
		return
	}
	if s.refuseDisabledMethod(w, r, guestMethodEmailAccount) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	// A grace request IS a password attempt: the same limiter and durable throttle as a sign-in, same identity.
	if s.loginRL != nil && !s.loginRL.allow(email, ip.String(), mac.String()) {
		ssErr(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")
		return
	}
	if ok, retry := s.throttleGuard(r.Context(), "account", ip, mac, email); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		ssErr(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")
		return
	}
	if s.db == nil {
		ssErr(w, http.StatusServiceUnavailable, "IAMV2_UNAVAILABLE")
		return
	}
	a, ok := s.verifySelfServicePassword(r.Context(), email, req.Password)
	if !ok {
		ssErr(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	set := s.selfServiceSettingsOrDefault(r.Context())
	now := time.Now()
	if !accountSuspended(a, set, now) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "not_suspended", "account": s.accountView(r.Context(), a, set, now)})
		return
	}
	if a.GraceUsed >= set.MaxGraceExtensions {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "grace_unavailable", "account": s.accountView(r.Context(), a, set, now)})
		return
	}
	// The allowance is consumed atomically: two concurrent requests cannot both spend the last one.
	tag, err := s.db.Exec(r.Context(), `UPDATE iam_v2.guest_access_accounts
	     SET grace_until = now() + make_interval(mins => $2), grace_extensions_used = grace_extensions_used + 1
	   WHERE id = $1::uuid AND tenant_id = $3 AND site_id = $4 AND self_registered AND email_verified_at IS NULL
	     AND grace_extensions_used < $5 AND (grace_until IS NULL OR grace_until <= now())`,
		a.ID, set.GraceMinutes, s.tenID, s.siteID, set.MaxGraceExtensions)
	if err != nil {
		slog.Error("email accounts: grace failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "grace failed")
		return
	}
	if tag.RowsAffected() == 0 {
		ssErr(w, http.StatusConflict, "grace_unavailable")
		return
	}
	slog.Info("email accounts: grace granted", "minutes", set.GraceMinutes)
	s.authorizeViaIAMv2(w, withMethodKey(r, keyEmailAccount), iamv2.MethodAccount, iamv2.Request{Username: email, Secret: req.Password,
		Credential: iamv2.CredentialEmailAccount, Device: iamv2.DeviceContext{MAC: mac.String()}}, ip)
}

// Same rules as the ACCOUNT authority (enabled, validity, lockout) and the same constant work for every refusal.
func (s *server) verifySelfServicePassword(ctx context.Context, email, password string) (selfServiceAccountRow, bool) {
	var a selfServiceAccountRow
	var hash string
	var vf, vu, locked *time.Time
	err := s.db.QueryRow(ctx, `SELECT `+selfServiceAccountCols+`, password_hash, valid_from, valid_until, locked_until
	      FROM iam_v2.guest_access_accounts WHERE tenant_id = $1 AND site_id = $2 AND lower(username) = lower($3)`,
		s.tenID, s.siteID, email).Scan(&a.ID, &a.Username, &a.SelfRegistered, &a.Enabled, &a.VerifiedAt, &a.Deadline,
		&a.GraceUntil, &a.GraceUsed, &hash, &vf, &vu, &locked)
	now := time.Now()
	if err != nil || !a.SelfRegistered || !a.Enabled || (locked != nil && locked.After(now)) ||
		(vf != nil && now.Before(*vf)) || (vu != nil && now.After(*vu)) {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("email accounts: account unreadable", "err", err)
		}
		passwordhash.BurnVerify(password)
		return selfServiceAccountRow{}, false
	}
	ok, err := passwordhash.Verify(password, hash)
	if err != nil || !ok {
		return selfServiceAccountRow{}, false
	}
	return a, true
}

// Marks a token used once if it is this site, this purpose, unused, not superseded and unexpired. One empty answer for every reason, so a link reveals nothing.
func (s *server) consumeEmailToken(ctx context.Context, tx pgx.Tx, token, purpose string) (string, error) {
	key := s.selfServiceKey(accountEmailTokenDomain)
	if key == nil || token == "" || len(token) > 128 {
		return "", nil
	}
	var accountID string
	err := tx.QueryRow(ctx, `UPDATE iam_v2.account_email_tokens SET used_at = now()
	     WHERE token_hmac = $1 AND purpose = $2 AND tenant_id = $3 AND site_id = $4
	       AND used_at IS NULL AND superseded_at IS NULL AND expires_at > now()
	   RETURNING account_id::text`, emailTokenHMAC(key, token), purpose, s.tenID, s.siteID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return accountID, err
}

// Verifying restores normal status at once; the package and any live connection are untouched.
func (s *server) accountVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
		IP    string `json:"ip"`
	}
	if err := decodeSelfServiceBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if ip := net.ParseIP(strings.TrimSpace(req.IP)); ip != nil {
		if ok, retry := s.throttleGuard(r.Context(), "account", ip, nil, ""); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
			ssErr(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")
			return
		}
	}
	if s.db == nil {
		ssErr(w, http.StatusGone, "invalid_or_used")
		return
	}
	ctx := r.Context()
	var verified bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		id, err := s.consumeEmailToken(ctx, tx, strings.TrimSpace(req.Token), tokenPurposeVerify)
		if err != nil || id == "" {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_access_accounts SET email_verified_at = COALESCE(email_verified_at, now())
		     WHERE id = $1::uuid AND self_registered`, id); err != nil {
			return err
		}
		// Any other outstanding VERIFY link is now pointless; a RESET link stays valid.
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.account_email_tokens SET superseded_at = now()
		     WHERE account_id = $1::uuid AND purpose = 'VERIFY' AND used_at IS NULL AND superseded_at IS NULL`, id); err != nil {
			return err
		}
		verified = true
		return nil
	})
	if err != nil {
		slog.Error("email accounts: verify failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "verify failed")
		return
	}
	if !verified {
		ssErr(w, http.StatusGone, "invalid_or_used")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": true})
}

// ALWAYS answers 202 (unknown address, Personal account, switched-off method, throttle, cooldown, spent allowance), so the route cannot reveal
// who has an account.
func (s *server) accountForgot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		IP    string `json:"ip"`
		MAC   string `json:"mac"`
	}
	if err := decodeSelfServiceBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	accepted := func() { writeJSON(w, http.StatusAccepted, map[string]any{"queued": true}) }
	ip, mac, ok := guestDevice(req.IP, req.MAC)
	email := normalizeAccountEmail(req.Email)
	if !ok || email == "" || s.db == nil || !s.guestMethodEnabled(r.Context(), guestMethodEmailAccount) {
		accepted()
		return
	}
	if s.loginRL != nil && !s.loginRL.allow("forgot:"+email, ip.String(), mac.String()) {
		accepted()
		return
	}
	if ok, _ := s.throttleGuard(r.Context(), "account", ip, mac, "forgot:"+email); !ok {
		accepted()
		return
	}
	ctx := r.Context()
	set, err := s.selfServiceSettingsRead(ctx)
	if err != nil {
		slog.Warn("email accounts: settings unreadable; reset not queued", "err", err)
		accepted()
		return
	}
	queued := false
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.guest_access_accounts
		     WHERE tenant_id = $1 AND site_id = $2 AND lower(username) = $3 AND self_registered AND enabled`,
			s.tenID, s.siteID, email).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('self_service_resend'), hashtext($1))`, id); err != nil {
			return err
		}
		st, err := s.verifyTokenStats(ctx, tx, id, tokenPurposeReset)
		if err != nil {
			return err
		}
		if wait, _ := resendWait(st.newest, st.oldestInDay, st.inDay, set, time.Now()); wait > 0 {
			return nil
		}
		if err := queueEmailToken(ctx, tx, s.tenID, s.siteID, id, tokenPurposeReset, time.Now().Add(resetTokenLifetime)); err != nil {
			return err
		}
		queued = true
		return nil
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("email accounts: forgot-password failed", "err", err)
	}
	if queued {
		s.kickSelfServiceOutbox()
	}
	accepted()
}

// Completing a reset PROVES CONTROL OF THE ADDRESS: it marks the address verified, revokes every outstanding link and ends the account live
// sessions, so a squatter on an address that is not theirs loses account and connection at once.
func (s *server) accountReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		IP       string `json:"ip"`
	}
	if err := decodeSelfServiceBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if ip := net.ParseIP(strings.TrimSpace(req.IP)); ip != nil {
		if ok, retry := s.throttleGuard(r.Context(), "account", ip, nil, ""); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
			ssErr(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")
			return
		}
	}
	key := s.selfServiceKey(accountEmailTokenDomain)
	token := strings.TrimSpace(req.Token)
	if s.db == nil || key == nil || token == "" || len(token) > 128 {
		ssErr(w, http.StatusGone, "invalid_or_used")
		return
	}
	ctx := r.Context()
	// Read first, without consuming: a password the policy refuses must not spend the link.
	var accountID, username string
	err := s.db.QueryRow(ctx, `SELECT a.id::text, a.username
	      FROM iam_v2.account_email_tokens t JOIN iam_v2.guest_access_accounts a ON a.id = t.account_id
	     WHERE t.token_hmac = $1 AND t.purpose = 'RESET' AND t.tenant_id = $2 AND t.site_id = $3
	       AND t.used_at IS NULL AND t.superseded_at IS NULL AND t.expires_at > now() AND a.self_registered`,
		emailTokenHMAC(key, token), s.tenID, s.siteID).Scan(&accountID, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		ssErr(w, http.StatusGone, "invalid_or_used")
		return
	}
	if err != nil {
		slog.Error("email accounts: reset lookup failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "reset failed")
		return
	}
	if passwordhash.CheckSelfService(req.Password, username) != nil {
		ssErr(w, http.StatusUnprocessableEntity, "weak_password")
		return
	}
	hash, err := passwordhash.Hash(req.Password)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "reset failed")
		return
	}
	done := false
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		id, err := s.consumeEmailToken(ctx, tx, token, tokenPurposeReset)
		if err != nil || id == "" || id != accountID {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_access_accounts
		     SET password_hash = $2, email_verified_at = COALESCE(email_verified_at, now())
		   WHERE id = $1::uuid AND self_registered`, id, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.account_email_tokens SET superseded_at = now()
		     WHERE account_id = $1::uuid AND used_at IS NULL AND superseded_at IS NULL`, id); err != nil {
			return err
		}
		done = true
		return nil
	})
	if err != nil {
		slog.Error("email accounts: reset failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "reset failed")
		return
	}
	if !done {
		ssErr(w, http.StatusGone, "invalid_or_used")
		return
	}
	ended := s.endLiveSessionsOfAccount(ctx, accountID, endReasonPasswordReset)
	slog.Info("email accounts: password reset completed", "sessions_ended", ended)
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}
