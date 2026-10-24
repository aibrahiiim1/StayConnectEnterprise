package main

// Admin Console authenticator. edged authenticates the password, throttles and audits, but cannot open the authenticator
// secret (the key is here); these routes seal a new secret, check a code, stop a code being used twice, and make and spend
// recovery codes. Under /v1/admin/, so peerGate admits edged alone.
// The secret leaves this process once, in the enrol answer (for the QR code); recovery codes leave once, when made, and are stored as an HMAC under the same key.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/totp"
)

const (
	operatorMFAKeyFile = "operator_totp_dek.key"
	operatorMFADomain  = "operator-totp-aead:v1"
	recoveryCodeCount  = 10
	// recoveryAlphabet has no 0/O, 1/I/L: a code is read off paper and typed.
	recoveryAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	recoveryCodeLen  = 10
)

type operatorMFAKeys struct {
	seal sealbox.Key
	mac  []byte
}

var operatorIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (k operatorMFAKeys) present() bool { return k.seal.Present() && len(k.mac) == 32 }

// initOperatorMFAKey loads the sealing key. Without it no authenticator can be set up or checked: an operator who
// turned two-step sign-in on is refused at the code step rather than let through on the password (fail closed).
func (s *server) initOperatorMFAKey(secretsDir string) {
	raw, err := localkeys.LoadExistingKey(filepath.Join(secretsDir, operatorMFAKeyFile))
	if err != nil {
		slog.Error("admin console authenticator: sealing key missing (run keybootstrap at deploy); two-step sign-in cannot be set up or checked", "err", err)
		return
	}
	k, err := sealbox.NewKey(operatorMFADomain, raw)
	if err != nil {
		slog.Error("admin console authenticator: bad key", "err", err)
		return
	}
	m := hmac.New(sha256.New, raw)
	m.Write([]byte("operator-mfa-recovery-mac:v1"))
	s.operatorMFA = operatorMFAKeys{seal: k, mac: m.Sum(nil)}
}

func (s *server) operatorMFARoutes(r chi.Router) {
	r.Post("/v1/admin/operator-mfa/enrol", s.operatorMFAEnrol)
	r.Post("/v1/admin/operator-mfa/confirm", s.operatorMFAConfirm)
	r.Post("/v1/admin/operator-mfa/verify", s.operatorMFAVerify)
	r.Post("/v1/admin/operator-mfa/recovery-codes", s.operatorMFARecoveryCodes)
	r.Post("/v1/admin/operator-mfa/reset", s.operatorMFAReset)
}

type operatorMFAReq struct {
	OperatorID string `json:"operator_id"`
	Code       string `json:"code"`
	Issuer     string `json:"issuer"`
	Account    string `json:"account"`
}

func (s *server) operatorMFARequest(w http.ResponseWriter, r *http.Request) (operatorMFAReq, bool) {
	var req operatorMFAReq
	if err := decodeJSONBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad request")
		return req, false
	}
	if !operatorIDRe.MatchString(req.OperatorID) {
		httpErr(w, http.StatusBadRequest, "operator_id is required")
		return req, false
	}
	if s.db == nil || !s.operatorMFA.present() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mfa_unavailable"})
		return req, false
	}
	return req, true
}

func (s *server) operatorMFAEnrol(w http.ResponseWriter, r *http.Request) {
	req, ok := s.operatorMFARequest(w, r)
	if !ok {
		return
	}
	issuer, account := strings.TrimSpace(req.Issuer), strings.TrimSpace(req.Account)
	if issuer == "" {
		issuer = "OneGate Hotel Admin"
	}
	if len(issuer) > 80 || account == "" || len(account) > 320 {
		httpErr(w, http.StatusBadRequest, "issuer and account are required")
		return
	}
	secret, err := totp.NewSecret()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "no randomness")
		return
	}
	sealed, err := s.operatorMFA.seal.Seal(secret, req.OperatorID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "seal failed")
		return
	}
	// A pending (unconfirmed) authenticator is replaced: the operator scanned nothing, or scanned and lost it.
	// A confirmed one is never replaced here -- that is a reset, which edged guards separately.
	ct, err := s.db.Exec(r.Context(), `
		INSERT INTO public.operator_mfa (operator_id, key_id, nonce, ciphertext, cipher_version)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (operator_id) DO UPDATE
		   SET key_id = EXCLUDED.key_id, nonce = EXCLUDED.nonce, ciphertext = EXCLUDED.ciphertext,
		       cipher_version = EXCLUDED.cipher_version, created_at = now(), last_used_step = NULL
		 WHERE public.operator_mfa.confirmed_at IS NULL`,
		req.OperatorID, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, sealed.CipherVersion)
	if err != nil {
		slog.Error("operator mfa: enrol write failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "enrol failed")
		return
	}
	if ct.RowsAffected() == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already_enrolled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": totp.EncodeSecret(secret), "otpauth_uri": totp.URI(secret, issuer, account),
	})
}

func (s *server) openOperatorSecret(ctx context.Context, q pgx.Tx, operatorID string, confirmed bool) ([]byte, *int64, error) {
	var keyID string
	var ver int
	var nonce, ct []byte
	var last *int64
	err := q.QueryRow(ctx, `
		SELECT key_id, cipher_version, nonce, ciphertext, last_used_step FROM public.operator_mfa
		 WHERE operator_id = $1 AND (confirmed_at IS NOT NULL) = $2 FOR UPDATE`, operatorID, confirmed).
		Scan(&keyID, &ver, &nonce, &ct, &last)
	if err != nil {
		return nil, nil, err
	}
	secret, err := s.operatorMFA.seal.Open(sealbox.Sealed{Ciphertext: ct, Nonce: nonce, KeyID: keyID, CipherVersion: ver}, operatorID)
	return secret, last, err
}

func (s *server) operatorMFAConfirm(w http.ResponseWriter, r *http.Request) {
	req, ok := s.operatorMFARequest(w, r)
	if !ok {
		return
	}
	var codes []string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		secret, _, err := s.openOperatorSecret(r.Context(), tx, req.OperatorID, false)
		if err != nil {
			return err
		}
		step, valid := totp.Check(secret, req.Code, time.Now())
		if !valid {
			return errWrongCode
		}
		if _, err := tx.Exec(r.Context(), `UPDATE public.operator_mfa SET confirmed_at = now(), last_used_step = $2
			WHERE operator_id = $1`, req.OperatorID, step); err != nil {
			return err
		}
		codes, err = s.replaceRecoveryCodes(r.Context(), tx, req.OperatorID)
		return err
	})
	switch {
	case errors.Is(err, errWrongCode):
		writeJSON(w, http.StatusOK, map[string]any{"confirmed": false})
	case errors.Is(err, pgx.ErrNoRows):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "nothing_to_confirm"})
	case err != nil:
		slog.Error("operator mfa: confirm failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "confirm failed")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"confirmed": true, "recovery_codes": codes})
	}
}

var errWrongCode = errors.New("wrong code")

func (s *server) operatorMFAVerify(w http.ResponseWriter, r *http.Request) {
	req, ok := s.operatorMFARequest(w, r)
	if !ok {
		return
	}
	code := totp.Normalize(req.Code)
	method := "totp"
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		secret, last, err := s.openOperatorSecret(r.Context(), tx, req.OperatorID, true)
		if err != nil {
			return err
		}
		if len(code) == totp.Digits {
			return spendTOTP(r.Context(), tx, req.OperatorID, secret, last, code)
		}
		method = "recovery_code"
		ct, err := tx.Exec(r.Context(), `UPDATE public.operator_mfa_recovery_codes SET used_at = now()
			WHERE operator_id = $1 AND code_mac = $2 AND used_at IS NULL`, req.OperatorID, s.recoveryMAC(req.OperatorID, code))
		if err != nil {
			return err
		}
		if ct.RowsAffected() != 1 {
			return errWrongCode
		}
		return nil
	})
	switch {
	case errors.Is(err, errWrongCode):
		writeJSON(w, http.StatusOK, map[string]any{"ok": false})
		return
	case errors.Is(err, pgx.ErrNoRows):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "not_enrolled"})
		return
	case errors.Is(err, sealbox.ErrSeal):
		slog.Error("operator mfa: a stored authenticator does not open (key replaced?)", "operator_id", req.OperatorID)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mfa_unavailable"})
		return
	case err != nil:
		slog.Error("operator mfa: verify failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "verify failed")
		return
	}
	var left int
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.operator_mfa_recovery_codes
		WHERE operator_id = $1 AND used_at IS NULL`, req.OperatorID).Scan(&left)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "method": method, "recovery_codes_left": left})
}

// spendTOTP accepts a 6-digit code once: it must be valid now and newer than the last code accepted, and its step
// is recorded so the same code -- or an older one -- is refused next time.
func spendTOTP(ctx context.Context, tx pgx.Tx, operatorID string, secret []byte, last *int64, code string) error {
	step, valid := totp.Check(secret, code, time.Now())
	// A code already accepted -- or an older one -- is refused: someone who saw it cannot reuse it.
	if !valid || (last != nil && step <= *last) {
		return errWrongCode
	}
	_, err := tx.Exec(ctx, `UPDATE public.operator_mfa SET last_used_step = $2 WHERE operator_id = $1`, operatorID, step)
	return err
}

// operatorMFARecoveryCodes makes a new set only against a current authenticator code: a stolen session plus the password
// must not mint codes that later sign in with the password alone. A recovery code is refused here and not spent; the code
// is spent in the same transaction that replaces the set.
func (s *server) operatorMFARecoveryCodes(w http.ResponseWriter, r *http.Request) {
	req, ok := s.operatorMFARequest(w, r)
	if !ok {
		return
	}
	code := totp.Normalize(req.Code)
	var codes []string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		secret, last, err := s.openOperatorSecret(r.Context(), tx, req.OperatorID, true)
		if err != nil {
			return err
		}
		if len(code) != totp.Digits {
			return errWrongCode
		}
		if err := spendTOTP(r.Context(), tx, req.OperatorID, secret, last, code); err != nil {
			return err
		}
		codes, err = s.replaceRecoveryCodes(r.Context(), tx, req.OperatorID)
		return err
	})
	switch {
	case errors.Is(err, errWrongCode):
		writeJSON(w, http.StatusOK, map[string]any{"ok": false})
	case errors.Is(err, sealbox.ErrSeal):
		slog.Error("operator mfa: a stored authenticator does not open (key replaced?)", "operator_id", req.OperatorID)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mfa_unavailable"})
	case errors.Is(err, pgx.ErrNoRows):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "not_enrolled"})
	case err != nil:
		slog.Error("operator mfa: new recovery codes failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "recovery codes failed")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "recovery_codes": codes})
	}
}

func (s *server) operatorMFAReset(w http.ResponseWriter, r *http.Request) {
	var req operatorMFAReq
	if err := decodeJSONBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if !operatorIDRe.MatchString(req.OperatorID) || s.db == nil {
		httpErr(w, http.StatusBadRequest, "operator_id is required")
		return
	}
	// No key needed: removing an authenticator opens nothing.
	var removed bool
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM public.operator_mfa_recovery_codes WHERE operator_id = $1`, req.OperatorID); err != nil {
			return err
		}
		ct, err := tx.Exec(r.Context(), `DELETE FROM public.operator_mfa WHERE operator_id = $1`, req.OperatorID)
		removed = ct.RowsAffected() > 0
		return err
	})
	if err != nil {
		slog.Error("operator mfa: reset failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "reset failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *server) replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, operatorID string) ([]string, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM public.operator_mfa_recovery_codes WHERE operator_id = $1`, operatorID); err != nil {
		return nil, err
	}
	codes := make([]string, 0, recoveryCodeCount)
	for len(codes) < recoveryCodeCount {
		c, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		ct, err := tx.Exec(ctx, `INSERT INTO public.operator_mfa_recovery_codes (operator_id, code_mac) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, operatorID, s.recoveryMAC(operatorID, c))
		if err != nil {
			return nil, err
		}
		if ct.RowsAffected() == 1 {
			codes = append(codes, c[:recoveryCodeLen/2]+"-"+c[recoveryCodeLen/2:])
		}
	}
	return codes, nil
}

func newRecoveryCode() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(recoveryAlphabet)))
	for i := 0; i < recoveryCodeLen; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(recoveryAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// recoveryMAC binds a code to its operator; codes are compared upper-case without the dash.
func (s *server) recoveryMAC(operatorID, code string) string {
	m := hmac.New(sha256.New, s.operatorMFA.mac)
	m.Write([]byte(operatorID + "\x00" + strings.ToUpper(strings.ReplaceAll(code, "-", ""))))
	return hex.EncodeToString(m.Sum(nil))
}
