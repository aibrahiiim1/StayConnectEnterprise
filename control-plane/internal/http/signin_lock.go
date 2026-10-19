package http

// THE SIGN-IN LOCKOUT AT THE HANDLERS (Central migration 0053; auth/signin_throttle.go). A locked sign-in name is
// refused before its password is checked, at sign-in and at the password confirmation alike, and the refusal
// looks the same whether or not the name has an account.

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

func refuseLocked(w http.ResponseWriter, r *http.Request, until time.Time) {
	secs := int(time.Until(until).Seconds()) + 1
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	api.Fail(w, r, http.StatusTooManyRequests, "signin_locked",
		"Too many wrong passwords for this account. Try again in "+strconv.Itoa((secs+59)/60)+" minutes.")
}

// signinLocked refuses the request when email is locked. A lookup that fails lets the request through: the
// per-address rate limit still applies, and refusing every sign-in because a counter cannot be read would lock
// everyone out.
func (d authDeps) signinLocked(w http.ResponseWriter, r *http.Request, email, operatorID string) bool {
	if d.DB == nil || email == "" {
		return false
	}
	until, err := auth.SigninLockedUntil(r.Context(), d.DB, email)
	if err != nil {
		slog.Warn("sign-in lockout could not be read", "err", err)
		return false
	}
	if until == nil {
		return false
	}
	d.auditFailure(r, "auth.signin_locked_refused", operatorID, nil)
	refuseLocked(w, r, *until)
	return true
}

// signinFailed counts a wrong password for email and, when that started a lockout, audits it and answers 429.
func (d authDeps) signinFailed(w http.ResponseWriter, r *http.Request, email, operatorID string) bool {
	if d.DB == nil || email == "" {
		return false
	}
	until, err := auth.SigninFailed(r.Context(), d.DB, email)
	if err != nil {
		slog.Warn("sign-in failure could not be counted", "err", err)
		return false
	}
	if until == nil {
		return false
	}
	d.auditFailure(r, "auth.signin_locked", operatorID, map[string]any{"until": until.UTC().Format(time.RFC3339)})
	refuseLocked(w, r, *until)
	return true
}

func (d authDeps) signinSucceeded(r *http.Request, email string) {
	if d.DB == nil || email == "" {
		return
	}
	if err := auth.SigninSucceeded(r.Context(), d.DB, email); err != nil {
		slog.Warn("sign-in failures could not be cleared", "err", err)
	}
}
