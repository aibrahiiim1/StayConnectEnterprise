package auth

// SIGN-IN LOCKOUT (Central migrations 0053, 0056). Wrong passwords are counted per sign-in name AND client
// address, keyed by a hash of the lower-cased email and the address so unknown names are counted exactly like
// real ones; signin_lock_failures within signin_lock_minutes lock that name at that address for
// signin_lock_minutes (central_security_settings).
//
// PER ADDRESS (Product-Owner decision D-R1, 2026-10-20, "Email+IP, plus unlock"). Counted per email alone, the lock
// let anyone who knew an administrator's address keep them locked out from anywhere. Failures from one address
// now lock the account only for that address; the per-address request limit still bounds guessing from it. Every
// lock on an account can be cleared by its name hash (ClearSigninLocks): `ctrlapi unlock-signin` and an
// administrator's "Reset two-step".
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

	"github.com/jackc/pgx/v5/pgconn"
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

// SigninName is the name hash for email (every lock on the account carries it): an HMAC under a key derived from
// the two-step sign-in key. A nil MFA (every sign-in is refused then) falls back to a plain hash so the counter
// still works.
func (m *MFA) SigninName(email string) string {
	return m.throttleMAC(strings.ToLower(strings.TrimSpace(email)))
}

// SigninKey is the counter key for email signing in from the client address addr (clientip.From).
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

// SigninLockedUntil is when a running lockout on the key ends, or nil. key is MFA.SigninKey.
func SigninLockedUntil(ctx context.Context, db *pgxpool.Pool, key string) (*time.Time, error) {
	var until *time.Time
	err := db.QueryRow(ctx, `SELECT max(locked_until) FROM operator_signin_throttle
	                          WHERE key_hash = $1 AND locked_until > now()`, key).Scan(&until)
	return until, err
}

// SigninFailed counts one wrong password for key (MFA.SigninKey) of the account name (MFA.SigninName).
// newlyLockedUntil is set when this failure started a lockout.
func SigninFailed(ctx context.Context, db *pgxpool.Pool, key, name string) (newlyLockedUntil *time.Time, err error) {
	var n int
	err = db.QueryRow(ctx, `SELECT failures, newly_locked_until FROM operator_signin_throttle_fail($1, $2)`,
		key, name).Scan(&n, &newlyLockedUntil)
	return newlyLockedUntil, err
}

// ClearSigninLocks removes every wrong-password count and lock on the account name (MFA.SigninName), from every
// address. Returns how many there were.
func ClearSigninLocks(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, name string) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM operator_signin_throttle WHERE name_hash = $1`, name)
	return tag.RowsAffected(), err
}

// SigninSucceeded clears the count for the key: the right password from one address does not reset what other
// addresses have tried.
func SigninSucceeded(ctx context.Context, db *pgxpool.Pool, key string) error {
	_, err := db.Exec(ctx, `DELETE FROM operator_signin_throttle WHERE key_hash = $1`, key)
	return err
}
