package auth

// SIGN-IN LOCKOUT (Central migration 0053). Wrong passwords are counted per sign-in name, keyed by a hash of the
// lower-cased email so unknown names are counted exactly like real ones; signin_lock_failures within
// signin_lock_minutes lock the name for signin_lock_minutes (central_security_settings).
//
// THE KEY IS AN HMAC (GDPR audit 2026-10-19, A8). A plain SHA-256 of an email can be reversed by anyone holding
// a list of addresses, so the counter held personal data. MFA.SigninName keys it with a secret derived from
// the two-step sign-in key; the caller passes the result in.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The lockout settings' defaults and bounds; the table CHECKs (0053) are the authority.
const (
	DefaultSigninLockFailures = 5
	MinSigninLockFailures     = 3
	MaxSigninLockFailures     = 20
	DefaultSigninLockMinutes  = 15
	MinSigninLockMinutes      = 1
	MaxSigninLockMinutes      = 1440
)

// SigninName is the counter key for email: an HMAC under a key derived from the two-step sign-in key. A nil MFA
// (every sign-in is refused then) falls back to a plain hash so the counter still works.
func (m *MFA) SigninName(email string) string {
	name := []byte(strings.ToLower(strings.TrimSpace(email)))
	if m == nil || len(m.throttle) == 0 {
		sum := sha256.Sum256(name)
		return hex.EncodeToString(sum[:])
	}
	h := hmac.New(sha256.New, m.throttle)
	h.Write(name)
	return hex.EncodeToString(h.Sum(nil))
}

// SigninLockedUntil is when a running lockout on the name ends, or nil. key is MFA.SigninName.
func SigninLockedUntil(ctx context.Context, db *pgxpool.Pool, key string) (*time.Time, error) {
	var until *time.Time
	err := db.QueryRow(ctx, `SELECT max(locked_until) FROM operator_signin_throttle
	                          WHERE key_hash = $1 AND locked_until > now()`, key).Scan(&until)
	return until, err
}

// SigninFailed counts one wrong password for the name. newlyLockedUntil is set when this failure started a lockout.
func SigninFailed(ctx context.Context, db *pgxpool.Pool, key string) (newlyLockedUntil *time.Time, err error) {
	var n int
	err = db.QueryRow(ctx, `SELECT failures, newly_locked_until FROM operator_signin_throttle_fail($1)`,
		key).Scan(&n, &newlyLockedUntil)
	return newlyLockedUntil, err
}

// SigninSucceeded clears the count for the name.
func SigninSucceeded(ctx context.Context, db *pgxpool.Pool, key string) error {
	_, err := db.Exec(ctx, `DELETE FROM operator_signin_throttle WHERE key_hash = $1`, key)
	return err
}
