package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	resetCodeDigits    = 8
	resetScopeAccount  = "reset_account"
	resetScopeAddress  = "reset_address"
	resetMailTimeout   = 40 * time.Second
	resetUnavailableTx = "Password reset by email is not available on this appliance. Ask a site admin to reset your password under Operators."
	resetCodeRefusedTx = "The code is wrong or has expired. Check it, or ask for a new code. A code stops working after too many wrong tries."
)

var resetCodePattern = regexp.MustCompile(`^[0-9]{8}$`)

type resetPolicy struct {
	Enabled            bool       `json:"enabled"`
	CodeMinutes        int        `json:"code_minutes"`
	MaxAttempts        int        `json:"max_attempts"`
	AccountPerHour     int        `json:"account_per_hour"`
	AddressPerHour     int        `json:"address_per_hour"`
	AllowLastSiteAdmin bool       `json:"allow_last_site_admin"`
	ConfigVersion      int64      `json:"config_version"`
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
	UpdatedBy          *string    `json:"updated_by,omitempty"`
}

var resetBounds = []signinBound{
	{"code_minutes", "How long a code works", "minutes", 5, 60, 15,
		"A code sent by email stops working after this long. A new request replaces the previous code."},
	{"max_attempts", "Wrong codes before a code stops working", "attempts", 3, 10, 5,
		"After this many wrong codes, or wrong authenticator codes, the code is removed and a new one must be requested."},
	{"account_per_hour", "Codes per email address each hour", "requests", 1, 20, 5,
		"At most this many codes are sent to one email address in an hour, and never two within a minute. Addresses with no account are limited the same way."},
	{"address_per_hour", "Requests from one network address each hour", "requests", 5, 200, 20,
		"At most this many reset requests are accepted from one computer's network address in an hour, whatever the email address."},
}

func (p resetPolicy) value(key string) int {
	switch key {
	case "code_minutes":
		return p.CodeMinutes
	case "max_attempts":
		return p.MaxAttempts
	case "account_per_hour":
		return p.AccountPerHour
	case "address_per_hour":
		return p.AddressPerHour
	}
	return 0
}

func validateResetPolicy(p resetPolicy) (string, bool) {
	for _, b := range resetBounds {
		if v := p.value(b.Key); v < b.Min || v > b.Max {
			return b.Label + " must be between " + itoa(b.Min) + " and " + itoa(b.Max) + " " + b.Unit + ".", false
		}
	}
	return "", true
}

func (s *server) readResetPolicy(ctx context.Context) (resetPolicy, error) {
	var p resetPolicy
	err := s.db.QueryRow(ctx, `SELECT enabled, code_minutes, max_attempts, account_per_hour, address_per_hour,
		       allow_last_site_admin, config_version, updated_at, updated_by FROM public.operator_password_reset_policy_get()`).
		Scan(&p.Enabled, &p.CodeMinutes, &p.MaxAttempts, &p.AccountPerHour, &p.AddressPerHour,
			&p.AllowLastSiteAdmin, &p.ConfigVersion, &p.UpdatedAt, &p.UpdatedBy)
	return p, err
}

type resetMailState struct {
	Known bool   `json:"known"`
	Ready bool   `json:"ready"`
	Kind  string `json:"kind"`
}

func (s *server) resetMail(ctx context.Context) resetMailState {
	if s.scd == nil {
		return resetMailState{}
	}
	st, raw, err := s.scd.call(ctx, http.MethodGet, "/v1/admin/operator-mail/state", nil)
	if err != nil || st != http.StatusOK {
		return resetMailState{}
	}
	var v struct {
		Kind  string `json:"kind"`
		Ready bool   `json:"ready"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return resetMailState{}
	}
	return resetMailState{Known: true, Ready: v.Ready, Kind: v.Kind}
}

func (s *server) resetAvailable(ctx context.Context) (resetPolicy, bool, error) {
	p, err := s.readResetPolicy(ctx)
	if err != nil {
		return p, false, err
	}
	if !p.Enabled {
		return p, false, nil
	}
	return p, s.resetMail(ctx).Ready, nil
}

func newResetCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return "", err
	}
	code := n.String()
	return strings.Repeat("0", resetCodeDigits-len(code)) + code, nil
}

func resetCodeHash(operatorID, code string) string {
	sum := sha256.Sum256([]byte("operator-password-reset\x00" + operatorID + "\x00" + code))
	return hex.EncodeToString(sum[:])
}

func resetRefusal(state string) bool { return state != "ok" }

type resetAccount struct {
	ID, Email string
	Active    bool
	LastAdmin bool
}

func (s *server) lookupResetAccount(ctx context.Context, account string) (resetAccount, bool, error) {
	var a resetAccount
	var status string
	err := s.db.QueryRow(ctx, `
		SELECT o.id::text, o.email, o.status,
		       EXISTS (SELECT 1 FROM operator_roles r WHERE r.operator_id = o.id AND r.role = 'site_admin')
		       AND (SELECT count(DISTINCT x.id) FROM operators x JOIN operator_roles xr ON xr.operator_id = x.id
		             WHERE xr.role = 'site_admin' AND x.status = 'active') <= 1
		  FROM operators o WHERE lower(o.email) = $1
		 ORDER BY (o.status = 'active') DESC, o.created_at
		 LIMIT 1`, account).Scan(&a.ID, &a.Email, &status, &a.LastAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	a.Active = status == "active"
	return a, true, nil
}

func resetIneligible(a resetAccount, known bool, p resetPolicy) string {
	switch {
	case !known:
		return "unknown_account"
	case !a.Active:
		return "account_not_active"
	case a.LastAdmin && !p.AllowLastSiteAdmin:
		return "last_site_admin"
	}
	return ""
}

func (s *server) sendOperatorMail(operatorID, to, template, code string, minutes int) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resetMailTimeout)
		defer cancel()
		if s.scd == nil {
			return
		}
		st, raw, err := s.scd.call(ctx, http.MethodPost, "/v1/admin/operator-mail/send", map[string]any{
			"to": to, "template": template, "code": code, "minutes": minutes,
		})
		if err != nil || st != http.StatusOK {
			slog.Warn("operator email not sent", "template", template, "operator", operatorID, "status", st, "err", err, "answer", string(raw))
		}
	}()
}

func (s *server) passwordResetAvailable(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, ok, err := s.resetAvailable(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	out := map[string]any{"available": ok}
	if ok {
		out["code_minutes"] = p.CodeMinutes
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) passwordResetRequest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, authBodyLimit)
	var in struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Email) == "" ||
		len(in.Email) > maxOperatorEmailLen {
		jsonErr(w, http.StatusBadRequest, "bad_request", "an email address is required")
		return
	}
	account := strings.ToLower(strings.TrimSpace(in.Email))
	ip := clientIP(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	p, ok, err := s.resetAvailable(ctx)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	if !ok {
		jsonErr(w, http.StatusConflict, "reset_unavailable", resetUnavailableTx)
		return
	}
	var accountAllowed, addressAllowed bool
	if err := s.db.QueryRow(ctx, `SELECT account_allowed, address_allowed FROM public.operator_password_reset_request_allowed($1, $2)`,
		throttleKey(resetScopeAccount, account), throttleKey(resetScopeAddress, ip)).Scan(&accountAllowed, &addressAllowed); err != nil {
		slog.Error("password reset limits unreadable; refusing", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	if !addressAllowed {
		w.Header().Set("Retry-After", "3600")
		jsonErr(w, http.StatusTooManyRequests, "too_many_requests",
			"Too many password reset requests came from this computer's network address. Try again in an hour, or ask a site admin to reset your password.")
		return
	}

	unlock := signinLocks.lock("reset\x00" + account)
	defer unlock()
	a, known, err := s.lookupResetAccount(ctx, account)
	if err != nil {
		slog.Error("password reset: operator lookup failed", "err", err)
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	reason := resetIneligible(a, known, p)
	if reason == "" && !accountAllowed {
		reason = "request_limit"
	}
	if reason == "" {
		code, cerr := newResetCode()
		if cerr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
			return
		}
		if _, ierr := s.db.Exec(ctx, `SELECT public.operator_password_reset_issue($1, $2)`, a.ID, resetCodeHash(a.ID, code)); ierr != nil {
			slog.Error("password reset: code not stored", "err", ierr)
			jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
			return
		}
		s.sendOperatorMail(a.ID, a.Email, "reset_code", code, p.CodeMinutes)
		s.audit(r, "operator.password_reset_requested", "operator", a.ID, map[string]any{"source_ip": ip})
	} else {
		target := ""
		if known {
			target = a.ID
		}
		s.audit(r, "operator.password_reset_refused", "operator", target, map[string]any{"reason": reason, "source_ip": ip})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"code_minutes": p.CodeMinutes,
		"message": "If that email address belongs to an account here that can reset its password, a code is on its way. " +
			"It works for " + strconv.Itoa(p.CodeMinutes) + " minutes.",
	})
}

func (s *server) passwordResetConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, authBodyLimit)
	var in struct {
		Email    string `json:"email"`
		Code     string `json:"code"`
		Password string `json:"password"`
		MFACode  string `json:"mfa_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Email) == "" ||
		len(in.Email) > maxOperatorEmailLen || in.Password == "" || len(in.Password) > maxOperatorPasswordLen ||
		len(in.MFACode) > maxMFACodeLen {
		jsonErr(w, http.StatusBadRequest, "bad_request", "the email address, the code and a new password are required")
		return
	}
	account := strings.ToLower(strings.TrimSpace(in.Email))
	code := strings.TrimSpace(in.Code)
	if msg := operatorPasswordProblem(in.Password, account); msg != "" {
		jsonErr(w, http.StatusBadRequest, "password_weak", msg)
		return
	}
	ip := clientIP(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	p, ok, err := s.resetAvailable(ctx)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	if !ok {
		jsonErr(w, http.StatusConflict, "reset_unavailable", resetUnavailableTx)
		return
	}
	stIP, err := s.throttleStateFor(ctx, scopeIP, ip)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	if wait := stIP.refusal(false); wait > 0 {
		writeThrottled(w, wait, "attempts from this address")
		return
	}

	unlock := signinLocks.lock("reset\x00" + account)
	defer unlock()
	a, known, err := s.lookupResetAccount(ctx, account)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	refuse := func(id, reason string) {
		s.recordFailure(r, "operator.password_reset_failed", "operator.login_locked", id, reason, map[string]string{scopeIP: ip})
		jsonErr(w, http.StatusBadRequest, "code_refused", resetCodeRefusedTx)
	}
	if reason := resetIneligible(a, known, p); reason != "" {
		if known {
			_, _ = s.db.Exec(ctx, `SELECT public.operator_password_reset_discard($1)`, a.ID)
		}
		refuse(a.ID, reason)
		return
	}
	if !resetCodePattern.MatchString(code) {
		refuse(a.ID, "malformed_code")
		return
	}
	hash := resetCodeHash(a.ID, code)
	var state string
	if err := s.db.QueryRow(ctx, `SELECT public.operator_password_reset_check($1, $2)`, a.ID, hash).Scan(&state); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	if resetRefusal(state) {
		refuse(a.ID, "code_"+state)
		return
	}

	mfaMethod := ""
	st, err := s.mfa.Status(ctx, a.ID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	if st.Enrolled && st.ConfirmedAt != nil {
		if strings.TrimSpace(in.MFACode) == "" {
			jsonErr(w, http.StatusUnauthorized, "mfa_required",
				"This account uses an authenticator app. Enter the 6-digit code it shows now, or a recovery code.")
			return
		}
		stOp, terr := s.throttleStateFor(ctx, scopeMFA, a.ID)
		if terr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
			return
		}
		if wait := stOp.refusal(true); wait > 0 {
			writeThrottled(w, wait, "authenticator codes")
			return
		}
		method, _, valid, verr := s.mfa.Verify(ctx, a.ID, strings.TrimSpace(in.MFACode))
		if verr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
			return
		}
		if !valid {
			s.recordFailure(r, "operator.mfa_failed", "operator.mfa_locked", a.ID, "wrong_code_in_password_reset",
				map[string]string{scopeMFA: a.ID, scopeIP: ip})
			var left string
			_ = s.db.QueryRow(ctx, `SELECT public.operator_password_reset_fail($1)`, a.ID).Scan(&left)
			if left == "wrong" {
				jsonErr(w, http.StatusUnauthorized, "mfa_wrong",
					"That authenticator code is not right. Enter the 6-digit code your app shows now, or a recovery code.")
				return
			}
			jsonErr(w, http.StatusBadRequest, "code_refused", resetCodeRefusedTx)
			return
		}
		mfaMethod = method
		_ = s.throttleClear(ctx, scopeMFA, a.ID)
	}

	newHash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the new password could not be stored")
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var consumed bool
	if err := tx.QueryRow(ctx, `SELECT public.operator_password_reset_consume($1, $2)`, a.ID, hash).Scan(&consumed); err != nil || !consumed {
		jsonErr(w, http.StatusBadRequest, "code_refused", resetCodeRefusedTx)
		return
	}
	ct, err := tx.Exec(ctx, `UPDATE operators SET password_hash = $2, must_change_password = false, updated_at = now()
		WHERE id = $1 AND status = 'active'`, a.ID, newHash)
	if err != nil || ct.RowsAffected() != 1 {
		jsonErr(w, http.StatusBadRequest, "code_refused", resetCodeRefusedTx)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "reset_unavailable", "Password reset is unavailable right now. Try again shortly.")
		return
	}
	s.sessions.destroyOperator(a.ID)
	s.pending.dropOperator(a.ID)
	_ = s.throttleClear(ctx, scopeAccount, account)
	_ = s.throttleClear(ctx, scopeReauth, a.ID)
	s.audit(r, "operator.password_reset_by_email", "operator", a.ID, map[string]any{
		"source_ip": ip, "sessions_ended": true, "second_step": mfaMethod,
	})
	s.sendOperatorMail(a.ID, a.Email, "password_changed", "", 0)
	writeJSON(w, http.StatusOK, map[string]any{"message": "Your password was changed and every session of this account was signed out. Sign in with the new password."})
}

func (s *server) getPasswordResetPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	p, err := s.readResetPolicy(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the password reset settings could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": p, "bounds": resetBounds, "mail": s.resetMail(ctx)})
}

func (s *server) setPasswordResetPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		resetPolicy
		ExpectedVersion *int64 `json:"expected_version"`
		Reason          string `json:"reason"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if msg, ok := validateResetPolicy(in.resetPolicy); !ok {
		jsonErr(w, http.StatusBadRequest, "invalid_setting", msg)
		return
	}
	reason := settingReason(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	if !s.reauthStrict(r, in.Password) {
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
	p := in.resetPolicy
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT public.operator_password_reset_policy_set($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		p.Enabled, p.CodeMinutes, p.MaxAttempts, p.AccountPerHour, p.AddressPerHour, p.AllowLastSiteAdmin,
		in.ExpectedVersion, label, reason).Scan(&version); err != nil {
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "40001":
			jsonErr(w, http.StatusConflict, "version_conflict", "the password reset settings changed since you opened them; reload and try again")
		case errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "22023"):
			jsonErr(w, http.StatusBadRequest, "invalid_setting", "a value is outside its allowed range")
		default:
			jsonErr(w, http.StatusInternalServerError, "update_failed", "the password reset settings could not be saved")
		}
		return
	}
	s.audit(r, "operator.password_reset_policy_changed", "operator_password_reset_policy", "", map[string]any{
		"enabled": p.Enabled, "code_minutes": p.CodeMinutes, "max_attempts": p.MaxAttempts,
		"account_per_hour": p.AccountPerHour, "address_per_hour": p.AddressPerHour,
		"allow_last_site_admin": p.AllowLastSiteAdmin, "config_version": version, "reason": reason,
	})
	mail := s.resetMail(ctx)
	notice := "Saved. Forgot password is off at sign-in."
	switch {
	case p.Enabled && mail.Ready:
		notice = "Saved. Forgot password is offered at sign-in from now on."
	case p.Enabled:
		notice = "Saved, but forgot password stays off at sign-in until an SMTP mail server is set up under Delivery."
	}
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version, "notice": notice, "mail": mail})
}

func (s *server) listPasswordResetPolicyChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT changed_by, change_reason, old_values, new_values, new_config_version, changed_at
		  FROM public.operator_password_reset_policy_changes ORDER BY changed_at DESC LIMIT 50`)
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

func (s *server) discardResetCode(ctx context.Context, operatorID string) {
	if _, err := s.db.Exec(ctx, `SELECT public.operator_password_reset_discard($1)`, operatorID); err != nil {
		slog.Warn("a live password reset code could not be removed", "operator", operatorID, "err", err)
	}
}

func (s *server) passwordResetNeedsMail(ctx context.Context) bool {
	p, err := s.readResetPolicy(ctx)
	if err != nil || !p.Enabled {
		return false
	}
	m := s.resetMail(ctx)
	return m.Known && !m.Ready
}

func passwordResetNoMailAttention() attentionItem {
	return attentionItem{ID: "password-reset-no-mail", Severity: "warn",
		Title: "Forgot password is off at sign-in: no SMTP mail server is set up",
		Detail: "Password reset by email is turned on, but the appliance has no working SMTP mail server to send the codes, " +
			"so the sign-in page does not offer it. Set up the site's own SMTP server under Delivery. Until then, " +
			"a site admin resets forgotten passwords under Operators.",
		Href: "/operators", Action: "Open password reset"}
}
