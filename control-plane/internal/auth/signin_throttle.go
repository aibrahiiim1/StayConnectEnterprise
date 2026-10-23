package auth

// Sign-in lockout: wrong passwords are counted per sign-in name and client address (unknown names counted like
// real ones); signin_lock_failures within signin_lock_minutes lock that name at that address.
//
// The lock is per address: counted per email alone, anyone knowing an administrator's address could keep them
// locked out from anywhere. Every lock on an account is cleared by its name hash (ClearSigninLocks).
//
// The key is an HMAC, not a plain SHA-256: a plain hash of an email is reversible from a list of addresses, so
// the counter would hold personal data. MFA.SigninName keys it with a secret derived from the two-step key.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The lockout settings' defaults and bounds; the table CHECK constraints are the authority.
const (
	DefaultSigninLockFailures = 5
	MinSigninLockFailures     = 3
	MaxSigninLockFailures     = 20
	DefaultSigninLockMinutes  = 15
	MinSigninLockMinutes      = 1
	MaxSigninLockMinutes      = 1440
)

// SigninName is the HMAC name hash every lock on the account carries. A nil MFA (every sign-in refused) falls back
// to a plain hash so the counter still works.
func (m *MFA) SigninName(email string) string {
	return m.throttleMAC(strings.ToLower(strings.TrimSpace(email)))
}

func (m *MFA) SigninKey(email, addr string) string {
	return m.throttleMAC("addr:v1\x00" + strings.ToLower(strings.TrimSpace(email)) + "\x00" + strings.TrimSpace(addr))
}

func (m *MFA) throttleMAC(s string) string {
	if m == nil || len(m.throttle) == 0 {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	h := hmac.New(sha256.New, m.throttle)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

func SigninLockedUntil(ctx context.Context, db *pgxpool.Pool, key string) (*time.Time, error) {
	var until *time.Time
	err := db.QueryRow(ctx, `SELECT max(locked_until) FROM operator_signin_throttle
	                          WHERE key_hash = $1 AND locked_until > now()`, key).Scan(&until)
	return until, err
}

// SigninFailed counts one wrong password; newlyLockedUntil is set when it started a lockout.
func SigninFailed(ctx context.Context, db *pgxpool.Pool, key, name string) (newlyLockedUntil *time.Time, err error) {
	var n int
	err = db.QueryRow(ctx, `SELECT failures, newly_locked_until FROM operator_signin_throttle_fail($1, $2)`,
		key, name).Scan(&n, &newlyLockedUntil)
	return newlyLockedUntil, err
}

// ClearSigninLocks removes every count and lock on the account name, from every address.
func ClearSigninLocks(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, name string) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM operator_signin_throttle WHERE name_hash = $1`, name)
	return tag.RowsAffected(), err
}

// SigninSucceeded clears the count for the key only; it does not reset what other addresses have tried.
func SigninSucceeded(ctx context.Context, db *pgxpool.Pool, key string) error {
	_, err := db.Exec(ctx, `DELETE FROM operator_signin_throttle WHERE key_hash = $1`, key)
	return err
}
