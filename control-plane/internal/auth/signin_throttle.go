package auth

// SIGN-IN LOCKOUT (Central migration 0053). Wrong passwords are counted per sign-in name, keyed by a SHA-256 of the
// lower-cased email so unknown names are counted exactly like real ones; signin_lock_failures within
// signin_lock_minutes lock the name for signin_lock_minutes (central_security_settings).

import (
	"context"
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

func signinKey(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// SigninLockedUntil is when a running lockout on email ends, or nil.
func SigninLockedUntil(ctx context.Context, db *pgxpool.Pool, email string) (*time.Time, error) {
	var until *time.Time
	err := db.QueryRow(ctx, `SELECT max(locked_until) FROM operator_signin_throttle
	                          WHERE key_hash = $1 AND locked_until > now()`, signinKey(email)).Scan(&until)
	return until, err
}

// SigninFailed counts one wrong password for email. newlyLockedUntil is set when this failure started a lockout.
func SigninFailed(ctx context.Context, db *pgxpool.Pool, email string) (newlyLockedUntil *time.Time, err error) {
	var n int
	err = db.QueryRow(ctx, `SELECT failures, newly_locked_until FROM operator_signin_throttle_fail($1)`,
		signinKey(email)).Scan(&n, &newlyLockedUntil)
	return newlyLockedUntil, err
}

// SigninSucceeded clears the count for email.
func SigninSucceeded(ctx context.Context, db *pgxpool.Pool, email string) error {
	_, err := db.Exec(ctx, `DELETE FROM operator_signin_throttle WHERE key_hash = $1`, signinKey(email))
	return err
}
