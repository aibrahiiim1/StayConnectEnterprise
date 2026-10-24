package auth

// Two-step sign-in to the Central console is optional per account (D68). For an account with a confirmed
// authenticator the password opens only a five-minute pending state, and a TOTP code (RFC 6238) or single-use
// recovery code opens the session; an account without one (or with a set-up never confirmed) signs in with the
// password alone. The TOTP secret is sealed with AES-256-GCM under
// CTRLAPI_OPERATOR_MFA_KEY and bound to the operator; recovery codes are stored as an HMAC under the same key.
// A code is never accepted twice. The appliance Admin Console does the same (data-plane cmd/scd/operator_mfa.go).

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

const (
	mfaDomain         = "central-operator-totp:v1"
	MFAIssuer         = "OneGate Central"
	recoveryCodeCount = 10
	recoveryAlphabet  = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // no 0/O, 1/I/L: read off paper and typed
	recoveryCodeLen   = 10
	PendingSigninTTL  = 5 * time.Minute
	PendingCookieName = "sc_signin"
)

var (
	ErrMFAUnavailable     = errors.New("two-step sign-in is unavailable")
	ErrMFANotEnrolled     = errors.New("no authenticator is set up")
	ErrMFAAlreadyEnrolled = errors.New("an authenticator is already set up")
)

type ErrMFALocked struct{ Until time.Time }

func (e ErrMFALocked) Error() string { return "too many wrong codes" }

// MFA is the authenticator service. A nil *MFA (no key configured) refuses everything: sign-in fails closed.
type MFA struct {
	DB    *pgxpool.Pool
	aead  cipher.AEAD
	keyID string
	mac   []byte
	// throttle keys the wrong-password counter (signin_throttle.go).
	throttle []byte
}

func LoadMFAKeyFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) == 32 {
		return raw, nil
	}
	if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw))); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("%s: want a 32-byte key", path)
}

func NewMFA(db *pgxpool.Pool, key []byte) (*MFA, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("the two-step sign-in key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	id := sha256.Sum256(append([]byte(mfaDomain+"|id:"), key...))
	m := hmac.New(sha256.New, key)
	m.Write([]byte(mfaDomain + "|recovery-mac"))
	t := hmac.New(sha256.New, key)
	t.Write([]byte(mfaDomain + "|signin-throttle"))
	return &MFA{DB: db, aead: gcm, keyID: hex.EncodeToString(id[:8]), mac: m.Sum(nil), throttle: t.Sum(nil)}, nil
}

func (s *MFA) ready() error {
	if s == nil || s.DB == nil || s.aead == nil {
		return ErrMFAUnavailable
	}
	return nil
}

func (s *MFA) aad(operatorID string) []byte { return []byte(mfaDomain + "|" + operatorID) }

type MFAStatus struct {
	Enrolled          bool       `json:"enrolled"`
	ConfirmedAt       *time.Time `json:"confirmed_at,omitempty"`
	RecoveryCodesLeft int        `json:"recovery_codes_left"`
}

func (s *MFA) Status(ctx context.Context, operatorID string) (MFAStatus, error) {
	var st MFAStatus
	if s == nil || s.DB == nil {
		return st, ErrMFAUnavailable
	}
	err := s.DB.QueryRow(ctx, `
		SELECT m.confirmed_at IS NOT NULL, m.confirmed_at,
		       (SELECT count(*)::int FROM operator_mfa_recovery_codes c WHERE c.operator_id = $1::uuid AND c.used_at IS NULL)
		  FROM (SELECT 1) one LEFT JOIN operator_mfa m ON m.operator_id = $1::uuid`, operatorID).
		Scan(&st.Enrolled, &st.ConfirmedAt, &st.RecoveryCodesLeft)
	return st, err
}

func (s *MFA) Enrol(ctx context.Context, operatorID, account string) (secret, uri string, err error) {
	if err := s.ready(); err != nil {
		return "", "", err
	}
	raw, err := totp.NewSecret()
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	ct := s.aead.Seal(nil, nonce, raw, s.aad(operatorID))
	tag, err := s.DB.Exec(ctx, `
		INSERT INTO operator_mfa (operator_id, key_id, nonce, ciphertext, cipher_version) VALUES ($1::uuid, $2, $3, $4, 1)
		ON CONFLICT (operator_id) DO UPDATE
		   SET key_id = EXCLUDED.key_id, nonce = EXCLUDED.nonce, ciphertext = EXCLUDED.ciphertext, created_at = now(),
		       last_used_step = NULL, failed_codes = 0, locked_until = NULL
		 WHERE operator_mfa.confirmed_at IS NULL`, operatorID, s.keyID, nonce, ct)
	if err != nil {
		return "", "", err
	}
	if tag.RowsAffected() == 0 {
		return "", "", ErrMFAAlreadyEnrolled
	}
	return totp.EncodeSecret(raw), totp.URI(raw, MFAIssuer, account), nil
}

type mfaRow struct {
	secret []byte
	last   *int64
	failed int
	locked *time.Time
}

func (s *MFA) load(ctx context.Context, tx pgx.Tx, operatorID string, confirmed bool) (mfaRow, error) {
	var r mfaRow
	var keyID string
	var nonce, ct []byte
	err := tx.QueryRow(ctx, `
		SELECT key_id, nonce, ciphertext, last_used_step, failed_codes, locked_until FROM operator_mfa
		 WHERE operator_id = $1::uuid AND (confirmed_at IS NOT NULL) = $2 FOR UPDATE`, operatorID, confirmed).
		Scan(&keyID, &nonce, &ct, &r.last, &r.failed, &r.locked)
	if errors.Is(err, pgx.ErrNoRows) {
		if confirmed {
			return r, ErrMFANotEnrolled
		}
		return r, ErrMFAAlreadyEnrolled
	}
	if err != nil {
		return r, err
	}
	if keyID != s.keyID || len(nonce) != s.aead.NonceSize() {
		return r, ErrMFAUnavailable
	}
	r.secret, err = s.aead.Open(nil, nonce, ct, s.aad(operatorID))
	if err != nil {
		return r, ErrMFAUnavailable
	}
	if r.locked != nil && time.Now().Before(*r.locked) {
		return r, ErrMFALocked{Until: *r.locked}
	}
	return r, nil
}

// failTx counts one wrong code; signin_lock_failures of them lock the authenticator for signin_lock_minutes (the
// password lockout's settings).
//
// It must run in the check's own transaction, with the row locked FOR UPDATE, and that transaction commits even for
// a wrong code. A count written in a separate statement let a burst of parallel wrong codes all be checked before
// the first failure landed, defeating the limit.
func (s *MFA) failTx(ctx context.Context, tx pgx.Tx, operatorID string) (lockedUntil *time.Time, err error) {
	err = tx.QueryRow(ctx, `
		WITH lim AS (SELECT COALESCE(max(signin_lock_failures), 5) AS n, COALESCE(max(signin_lock_minutes), 15) AS mins
		               FROM central_security_settings WHERE id)
		UPDATE operator_mfa SET
		  failed_codes = CASE WHEN failed_codes + 1 >= lim.n THEN 0 ELSE failed_codes + 1 END,
		  locked_until = CASE WHEN failed_codes + 1 >= lim.n THEN now() + make_interval(mins => lim.mins) ELSE locked_until END
		  FROM lim
		 WHERE operator_id = $1::uuid RETURNING CASE WHEN failed_codes = 0 THEN locked_until END`,
		operatorID).Scan(&lockedUntil)
	return lockedUntil, err
}

// Confirm proves a new authenticator with its first code and makes the recovery codes. lockedUntil is set when a
// wrong code started a lockout.
func (s *MFA) Confirm(ctx context.Context, operatorID, code string) (codes []string, ok bool, lockedUntil *time.Time, err error) {
	if err := s.ready(); err != nil {
		return nil, false, nil, err
	}
	wrong := false
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		r, err := s.load(ctx, tx, operatorID, false)
		if err != nil {
			return err
		}
		step, valid := totp.Check(r.secret, code, time.Now())
		if !valid {
			wrong = true
			lockedUntil, err = s.failTx(ctx, tx, operatorID)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE operator_mfa SET confirmed_at = now(), last_used_step = $2, failed_codes = 0,
			locked_until = NULL WHERE operator_id = $1::uuid`, operatorID, step); err != nil {
			return err
		}
		codes, err = s.replaceRecoveryCodes(ctx, tx, operatorID)
		return err
	})
	if err != nil {
		return nil, false, nil, err
	}
	if wrong {
		return nil, false, lockedUntil, nil
	}
	return codes, true, nil, nil
}

// Verify checks a code or recovery code; method is "totp" or "recovery_code", left the recovery codes remaining.
func (s *MFA) Verify(ctx context.Context, operatorID, code string) (method string, left int, ok bool, lockedUntil *time.Time, err error) {
	if err := s.ready(); err != nil {
		return "", 0, false, nil, err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		method, ok, lockedUntil, err = s.verifyTx(ctx, tx, operatorID, code, true)
		return err
	})
	if err != nil {
		return "", 0, false, nil, err
	}
	if !ok {
		return method, 0, false, lockedUntil, nil
	}
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM operator_mfa_recovery_codes WHERE operator_id = $1::uuid AND used_at IS NULL`,
		operatorID).Scan(&left)
	return method, left, true, nil, nil
}

// verifyTx checks code inside tx with the row locked FOR UPDATE. A 6-digit code is refused unless its step is newer
// than the last used; a recovery code is tried only when allowRecovery. A wrong code is counted in tx, so the
// caller must commit tx whatever the verdict.
func (s *MFA) verifyTx(ctx context.Context, tx pgx.Tx, operatorID, code string, allowRecovery bool) (method string, ok bool, lockedUntil *time.Time, err error) {
	c := totp.Normalize(code)
	method = "totp"
	r, err := s.load(ctx, tx, operatorID, true)
	if err != nil {
		return method, false, nil, err
	}
	wrong := func() (string, bool, *time.Time, error) {
		until, err := s.failTx(ctx, tx, operatorID)
		return method, false, until, err
	}
	if len(c) == totp.Digits || !allowRecovery {
		step, valid := totp.Check(r.secret, c, time.Now())
		if !valid || (r.last != nil && step <= *r.last) {
			return wrong()
		}
		_, err = tx.Exec(ctx, `UPDATE operator_mfa SET last_used_step = $2, failed_codes = 0 WHERE operator_id = $1::uuid`,
			operatorID, step)
		return method, err == nil, nil, err
	}
	method = "recovery_code"
	tag, err := tx.Exec(ctx, `UPDATE operator_mfa_recovery_codes SET used_at = now()
		WHERE operator_id = $1::uuid AND code_mac = $2 AND used_at IS NULL`, operatorID, s.recoveryMAC(operatorID, code))
	if err != nil {
		return method, false, nil, err
	}
	if tag.RowsAffected() != 1 {
		return wrong()
	}
	_, err = tx.Exec(ctx, `UPDATE operator_mfa SET failed_codes = 0 WHERE operator_id = $1::uuid`, operatorID)
	return method, err == nil, nil, err
}

// NewRecoveryCodes replaces every recovery code. It needs a current authenticator code: a password step-up or
// recovery code is not enough, since new recovery codes are new second factors. The code is checked like a sign-in
// code (never twice, wrong ones count towards the lockout) in the transaction that replaces the set. ok=false is
// a wrong code; lockedUntil is set when it started a lockout.
func (s *MFA) NewRecoveryCodes(ctx context.Context, operatorID, code string) (codes []string, ok bool, lockedUntil *time.Time, err error) {
	if err := s.ready(); err != nil {
		return nil, false, nil, err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		_, ok, lockedUntil, err = s.verifyTx(ctx, tx, operatorID, code, false)
		if err != nil || !ok {
			return err
		}
		codes, err = s.replaceRecoveryCodes(ctx, tx, operatorID)
		return err
	})
	if err != nil {
		return nil, false, nil, err
	}
	return codes, ok, lockedUntil, nil
}

// Disable turns the operator's own two-step sign-in off against a code from the second factor (the app's or a
// recovery code), checked like a sign-in code; the check and the removal are one transaction. ok=false is a wrong
// code; lockedUntil is set when it started a lockout.
func (s *MFA) Disable(ctx context.Context, operatorID, code string) (method string, ok bool, lockedUntil *time.Time, err error) {
	if err := s.ready(); err != nil {
		return "", false, nil, err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		method, ok, lockedUntil, err = s.verifyTx(ctx, tx, operatorID, code, true)
		if err != nil || !ok {
			return err
		}
		_, err = ResetMFA(ctx, tx, operatorID)
		return err
	})
	if err != nil {
		return "", false, nil, err
	}
	return method, ok, lockedUntil, nil
}

// ResetMFA removes an operator's authenticator and recovery codes. It needs no key, only a database (break-glass).
func ResetMFA(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, operatorID string) (bool, error) {
	if _, err := q.Exec(ctx, `DELETE FROM operator_mfa_recovery_codes WHERE operator_id = $1::uuid`, operatorID); err != nil {
		return false, err
	}
	tag, err := q.Exec(ctx, `DELETE FROM operator_mfa WHERE operator_id = $1::uuid`, operatorID)
	if err != nil {
		return false, err
	}
	_, _ = q.Exec(ctx, `DELETE FROM operator_signin_pending WHERE operator_id = $1::uuid`, operatorID)
	return tag.RowsAffected() > 0, nil
}

func (s *MFA) replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, operatorID string) ([]string, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM operator_mfa_recovery_codes WHERE operator_id = $1::uuid`, operatorID); err != nil {
		return nil, err
	}
	codes := make([]string, 0, recoveryCodeCount)
	max := big.NewInt(int64(len(recoveryAlphabet)))
	for len(codes) < recoveryCodeCount {
		var b strings.Builder
		for i := 0; i < recoveryCodeLen; i++ {
			n, err := rand.Int(rand.Reader, max)
			if err != nil {
				return nil, err
			}
			b.WriteByte(recoveryAlphabet[n.Int64()])
		}
		c := b.String()
		tag, err := tx.Exec(ctx, `INSERT INTO operator_mfa_recovery_codes (operator_id, code_mac) VALUES ($1::uuid, $2)
			ON CONFLICT DO NOTHING`, operatorID, s.recoveryMAC(operatorID, c))
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 1 {
			codes = append(codes, c[:recoveryCodeLen/2]+"-"+c[recoveryCodeLen/2:])
		}
	}
	return codes, nil
}

func (s *MFA) recoveryMAC(operatorID, code string) string {
	m := hmac.New(sha256.New, s.mac)
	m.Write([]byte(operatorID + "\x00" + normalizeRecoveryCode(code)))
	return hex.EncodeToString(m.Sum(nil))
}

// normalizeRecoveryCode restores the code as generated: upper case, with every space, tab, line break and dash
// removed wherever it was typed or pasted, not only at the ends.
func normalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, code))
}

type PendingSignin struct {
	OperatorID string
}

func pendingHash(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// CreatePending stores a pending sign-in and returns the cookie token. One per operator: a new password step replaces it.
func CreatePending(ctx context.Context, db *pgxpool.Pool, operatorID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM operator_signin_pending WHERE operator_id = $1::uuid OR expires_at < now()`,
			operatorID); err != nil {
			return err
		}
		// The enrol column (0052) stays false: an authenticator is set up from the account page, never at sign-in.
		_, err := tx.Exec(ctx, `INSERT INTO operator_signin_pending (token_hash, operator_id, enrol, expires_at)
			VALUES ($1, $2::uuid, false, now() + make_interval(secs => $3))`, pendingHash(tok), operatorID,
			PendingSigninTTL.Seconds())
		return err
	})
	return tok, err
}

func LookupPending(ctx context.Context, db *pgxpool.Pool, tok string) (*PendingSignin, error) {
	var p PendingSignin
	err := db.QueryRow(ctx, `SELECT operator_id::text FROM operator_signin_pending
		WHERE token_hash = $1 AND expires_at > now()`, pendingHash(tok)).Scan(&p.OperatorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

func DropPending(ctx context.Context, db *pgxpool.Pool, tok string) {
	_, _ = db.Exec(ctx, `DELETE FROM operator_signin_pending WHERE token_hash = $1`, pendingHash(tok))
}

var ErrMFAWrongKey = errors.New("the two-step sign-in key opens none of the confirmed authenticators")

// CheckKey proves the key against the database before a release uses it (`ctrlapi check-mfa-key`). With no confirmed
// authenticator it succeeds; otherwise at least one must open under this key, or ErrMFAWrongKey, since a key that
// opens none would refuse every operator. Some can be legitimately unreadable (e.g. restored without the key) until reset.
func (s *MFA) CheckKey(ctx context.Context) (opened, unreadable int, err error) {
	if err := s.ready(); err != nil {
		return 0, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT operator_id::text, key_id, nonce, ciphertext FROM operator_mfa WHERE confirmed_at IS NOT NULL`)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "42P01" { // undefined_table: no authenticators yet
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, keyID string
		var nonce, ct []byte
		if err := rows.Scan(&id, &keyID, &nonce, &ct); err != nil {
			return 0, 0, err
		}
		if keyID != s.keyID || len(nonce) != s.aead.NonceSize() {
			unreadable++
			continue
		}
		if _, err := s.aead.Open(nil, nonce, ct, s.aad(id)); err != nil {
			unreadable++
			continue
		}
		opened++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if opened == 0 && unreadable > 0 {
		return 0, unreadable, ErrMFAWrongKey
	}
	return opened, unreadable, nil
}

func (s *MFA) KeyID() string {
	if s == nil {
		return ""
	}
	return s.keyID
}
