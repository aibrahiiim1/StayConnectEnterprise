package auth

// THE CENTRAL CONSOLE AUTHENTICATOR (migration 0052; GDPR remediation plan C10, decision D-C10).
//
// Signing in to Central is two steps: the password opens a five-minute pending state (operator_signin_pending),
// and the 6-digit code from the operator's authenticator app (TOTP, RFC 6238) -- or a single-use recovery code --
// opens the session. The first sign-in sets the app up. The TOTP secret is sealed here with AES-256-GCM under
// CTRLAPI_OPERATOR_MFA_KEY and bound to the operator; recovery codes are stored as an HMAC under the same key.
// Wrong codes lock the authenticator for fifteen minutes after five, and a code is never accepted twice.
//
// The appliance's Admin Console does the same (data-plane cmd/scd/operator_mfa.go, migration 0132).

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
	mfaDomain = "central-operator-totp:v1"
	// MFAIssuer is the name the authenticator app shows.
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

// ErrMFALocked is a wrong-code lockout; Until says when it ends.
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

// LoadMFAKeyFile reads a 32-byte key file (raw, or base64 of 32 bytes).
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

// NewMFA builds the service from a 32-byte key.
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

// MFAStatus is what may be shown: whether a second step is set up, and how many recovery codes are left.
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

// Enrol makes a new secret for an operator without a confirmed authenticator, replacing an unconfirmed one.
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

// failTx counts one wrong code. signin_lock_failures wrong codes lock the authenticator for signin_lock_minutes, the
// same two settings as the password lockout (central_security_settings, 0053).
//
// IT RUNS IN THE CHECK'S OWN TRANSACTION, with the authenticator row already locked FOR UPDATE by load, and that
// transaction COMMITS although the answer is "wrong code". A count written in a separate statement after the
// check let a burst of parallel wrong codes all be checked before the first failure landed, so five guesses
// became as many as could be sent at once. Now each check waits for the previous one's verdict and count.
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

// Confirm proves a freshly set-up authenticator with its first code and makes the recovery codes.
// lockedUntil is set when this wrong code started a lockout.
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

// Verify checks a code, or a recovery code, against a confirmed authenticator. method is "totp" or
// "recovery_code"; left is the recovery codes left after a recovery code was used.
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

// verifyTx checks code inside tx, with the authenticator row locked FOR UPDATE until tx ends. A 6-digit code is
// refused when its step is not newer than the last one used (a code is never accepted twice). A recovery code is
// tried only when allowRecovery. A wrong code is counted in tx (failTx), so the caller must COMMIT tx whatever
// the verdict; ok=false then.
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

// NewRecoveryCodes replaces every recovery code with a fresh set. It needs a CURRENT code from the authenticator
// app (Product-Owner decision D-R3, 2026-10-20): the password step-up alone, or a recovery code, is not enough,
// because a new set of recovery codes is a new set of second factors. The code is checked exactly like a sign-in
// code -- never twice, wrong ones counted towards the same lockout -- in the same transaction that replaces the
// set. ok=false is a wrong code; lockedUntil is set when it started a lockout.
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

// ResetMFA removes an operator's authenticator and recovery codes. It needs no key: it opens nothing. Usable
// with only a database (the seed-admin style break-glass). Returns whether there was one.
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

// normalizeRecoveryCode is a recovery code as it was generated: the alphabet's upper-case letters and digits,
// with every space, tab, line break and dash removed wherever it was typed or pasted ("abcde fghjk",
// "ABCDE-\nFGHJK"), not only at the ends.
func normalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, code))
}

// ----- the five minutes between the password and the code ------------------------------------------------------

// PendingSignin is a correct password waiting for its code.
type PendingSignin struct {
	OperatorID string
	Enrol      bool
}

func pendingHash(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// CreatePending stores a pending sign-in and returns the cookie token. One per operator: a new password step
// replaces the previous one. Expired rows are removed on the way.
func CreatePending(ctx context.Context, db *pgxpool.Pool, operatorID string, enrol bool) (string, error) {
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
		_, err := tx.Exec(ctx, `INSERT INTO operator_signin_pending (token_hash, operator_id, enrol, expires_at)
			VALUES ($1, $2::uuid, $3, now() + make_interval(secs => $4))`, pendingHash(tok), operatorID, enrol,
			PendingSigninTTL.Seconds())
		return err
	})
	return tok, err
}

// LookupPending returns the pending sign-in a token names, or nil when there is none or it expired.
func LookupPending(ctx context.Context, db *pgxpool.Pool, tok string) (*PendingSignin, error) {
	var p PendingSignin
	err := db.QueryRow(ctx, `SELECT operator_id::text, enrol FROM operator_signin_pending
		WHERE token_hash = $1 AND expires_at > now()`, pendingHash(tok)).Scan(&p.OperatorID, &p.Enrol)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

// SetPendingEnrol records that the operator turned out to have an authenticator already (a race).
func SetPendingEnrol(ctx context.Context, db *pgxpool.Pool, tok string, enrol bool) {
	_, _ = db.Exec(ctx, `UPDATE operator_signin_pending SET enrol = $2 WHERE token_hash = $1`, pendingHash(tok), enrol)
}

// DropPending removes a pending sign-in (used, or abandoned).
func DropPending(ctx context.Context, db *pgxpool.Pool, tok string) {
	_, _ = db.Exec(ctx, `DELETE FROM operator_signin_pending WHERE token_hash = $1`, pendingHash(tok))
}

// ----- is this the right key? -------------------------------------------------------------------------------------

// ErrMFAWrongKey means the key opens none of the confirmed authenticators: they were sealed under another key.
var ErrMFAWrongKey = errors.New("the two-step sign-in key opens none of the confirmed authenticators")

// CheckKey proves the key against the database before a release starts using it (`ctrlapi check-mfa-key`, run by
// central-deploy.sh before it switches releases). With no confirmed authenticator there is nothing to prove and
// it succeeds. Otherwise at least one must open under this key, or ErrMFAWrongKey: a key that opens none would
// refuse every operator who has set up the app. opened and unreadable count the authenticators that did and did
// not open (some can be legitimately unreadable, e.g. after a restore without the key, until they are reset).
// A database without the 0052 table has no authenticators.
func (s *MFA) CheckKey(ctx context.Context) (opened, unreadable int, err error) {
	if err := s.ready(); err != nil {
		return 0, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT operator_id::text, key_id, nonce, ciphertext FROM operator_mfa WHERE confirmed_at IS NOT NULL`)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "42P01" { // undefined_table: before migration 0052
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

// KeyID is the short identifier of the key every sealed authenticator records (operator_mfa.key_id).
func (s *MFA) KeyID() string {
	if s == nil {
		return ""
	}
	return s.keyID
}
