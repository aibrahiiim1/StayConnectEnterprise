// Package otp issues + verifies one-time codes for guest auth.
//
// Design:
//   - 6-digit numeric codes — easy to type on a phone keypad
//   - SHA-256(per-row salt + code) — fast verify, no global secret needed,
//     OTP lifetime is short (10m default) and rate-limited so brute-force
//     resistance comes from the attempt cap, not the hash work-factor
//   - Code is shown to the user once (returned from Issue) and emailed via
//     the Mailer. We never log it server-side after delivery.
package otp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/otpkey"
)

const (
	DefaultTTL         = 10 * time.Minute
	DefaultMaxAttempts = 5
	IssueCooldown      = 60 * time.Second // between issues to same destination
	IssueHourlyCap     = 5                // max issues per destination per hour
	IPHourlyCap        = 20               // max issues per source IP per hour (anti-abuse)
)

// Errors surfaced to callers (HTTP handlers map to status codes).
var (
	ErrCooldown         = errors.New("otp: cooldown — wait before requesting another code")
	ErrHourlyCap        = errors.New("otp: too many requests this hour")
	ErrIPRateLimited    = errors.New("otp: too many requests from your network")
	ErrNotFound         = errors.New("otp: challenge not found")
	ErrExpired          = errors.New("otp: code expired")
	ErrAttemptsExceeded = errors.New("otp: too many wrong attempts")
	ErrAlreadyUsed      = errors.New("otp: code already used")
	ErrCodeMismatch     = errors.New("otp: incorrect code")
)

type IssueParams struct {
	TenantID    string
	ApplianceID string
	TemplateID  string // ticket_template to apply on successful verify
	Channel     string // "email" or "sms"
	Destination string // email address or phone number
	IP          string // source IP of requester (for forensics)
	UserAgent   string
}

type Issued struct {
	ChallengeID string
	Code        string // plaintext — return to caller, never persisted
	ExpiresAt   time.Time
}

// Issue creates an OTP row, returns the plaintext code (for the mailer).
// Enforces cooldown + hourly cap before generating.
//
// When ring is non-nil (keyed-HMAC / D7 enabled) the stored code_hash is a keyed HMAC over the
// domain-separated (tenant, channel, destination, challenge id) tuple using the ring's ACTIVE
// generation, and otp_key_generation records that generation. When ring is nil the legacy
// per-row salt:sha256(salt|code) format is written and otp_key_generation is left NULL — byte-for-byte
// the previous behavior (safe against a database that has not yet applied migration 0008).
func Issue(ctx context.Context, db *pgxpool.Pool, ring *otpkey.Ring, p IssueParams) (*Issued, error) {
	if p.TenantID == "" || p.Channel == "" || p.Destination == "" {
		return nil, fmt.Errorf("otp: tenant/channel/destination required")
	}
	dest := strings.ToLower(strings.TrimSpace(p.Destination))

	// Rate-limit per destination: cooldown + hourly cap.
	var lastAt *time.Time
	var hourCount int
	if err := db.QueryRow(ctx, `
        SELECT max(issued_at),
               count(*) FILTER (WHERE issued_at > now() - interval '1 hour')
          FROM auth_otps
         WHERE tenant_id = $1 AND channel = $2 AND lower(destination) = $3
    `, p.TenantID, p.Channel, dest).Scan(&lastAt, &hourCount); err != nil {
		return nil, fmt.Errorf("otp: rate-limit lookup: %w", err)
	}
	if lastAt != nil && time.Since(*lastAt) < IssueCooldown {
		return nil, ErrCooldown
	}
	if hourCount >= IssueHourlyCap {
		return nil, ErrHourlyCap
	}

	// Rate-limit per source IP across ALL destinations + channels.
	// Mitigates address-enumeration / brute-spam from a single guest.
	if p.IP != "" {
		var ipHour int
		if err := db.QueryRow(ctx, `
            SELECT count(*) FROM auth_otps
             WHERE tenant_id = $1
               AND ip = $2::inet
               AND issued_at > now() - interval '1 hour'
        `, p.TenantID, p.IP).Scan(&ipHour); err != nil {
			return nil, fmt.Errorf("otp: ip rate-limit lookup: %w", err)
		}
		if ipHour >= IPHourlyCap {
			return nil, ErrIPRateLimited
		}
	}

	code := generate6()
	expires := time.Now().Add(DefaultTTL)

	if ring == nil {
		// Legacy path — unchanged: per-row salt:sha256, otp_key_generation stays NULL.
		salt := randHex(8)
		hash := hashOTP(salt, code)
		var id string
		if err := db.QueryRow(ctx, `
        INSERT INTO auth_otps
          (tenant_id, appliance_id, template_id, channel, destination,
           code_hash, expires_at, max_attempts, ip, user_agent)
        VALUES
          ($1, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid, $4, $5,
           $6, $7, $8,
           CASE WHEN $9 = '' THEN NULL ELSE $9::inet END,
           NULLIF($10,''))
        RETURNING id
    `, p.TenantID, p.ApplianceID, p.TemplateID, p.Channel, dest,
			salt+":"+hash, expires, DefaultMaxAttempts, p.IP, p.UserAgent).Scan(&id); err != nil {
			return nil, fmt.Errorf("otp: insert: %w", err)
		}
		return &Issued{ChallengeID: id, Code: code, ExpiresAt: expires}, nil
	}

	// Keyed-HMAC path (D7). The digest binds the challenge id, so the id must be known BEFORE the HMAC
	// is computed — pre-generate it, then insert with the explicit id and its key generation.
	var id string
	if err := db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return nil, fmt.Errorf("otp: id gen: %w", err)
	}
	gen, digestHex, err := ring.Issue(otpkey.Challenge{
		TenantID: p.TenantID, Channel: p.Channel, Destination: dest, ChallengeID: id,
	}, code)
	if err != nil {
		return nil, fmt.Errorf("otp: hmac issue: %w", err)
	}
	if _, err := db.Exec(ctx, `
        INSERT INTO auth_otps
          (id, tenant_id, appliance_id, template_id, channel, destination,
           code_hash, otp_key_generation, expires_at, max_attempts, ip, user_agent)
        VALUES
          ($1::uuid, $2, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid, $5, $6,
           $7, $8, $9, $10,
           CASE WHEN $11 = '' THEN NULL ELSE $11::inet END,
           NULLIF($12,''))
    `, id, p.TenantID, p.ApplianceID, p.TemplateID, p.Channel, dest,
		digestHex, gen, expires, DefaultMaxAttempts, p.IP, p.UserAgent); err != nil {
		return nil, fmt.Errorf("otp: insert: %w", err)
	}
	return &Issued{ChallengeID: id, Code: code, ExpiresAt: expires}, nil
}

type Verified struct {
	ChallengeID string
	TenantID    string
	TemplateID  string // empty if not configured
	Channel     string
	Destination string
}

// Verify checks the code, records the attempt, and returns details on success.
// The row is marked consumed on success — single-use.
//
// When ring is non-nil (D7) a row that pins a key generation is verified with the keyed HMAC for that
// exact generation; a row with a NULL generation is a legacy salt:sha256 OTP and is accepted only
// while unexpired (the compatibility window — no new legacy rows are issued once the ring is on). When
// ring is nil the legacy verify path runs unchanged (and does not reference otp_key_generation, so it
// is safe against a pre-0008 database). Attempt-increment and consume are atomic in one FOR UPDATE tx.
func Verify(ctx context.Context, db *pgxpool.Pool, ring *otpkey.Ring, challengeID, code string) (*Verified, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var (
		tenantID, channel, destination, codeHash string
		templateID                               *string
		expiresAt                                time.Time
		attempts, maxAttempts                    int
		consumedAt                               *time.Time
		keyGen                                   *int
	)
	if ring == nil {
		// Legacy SELECT — does not reference otp_key_generation (pre-0008 safe).
		err = tx.QueryRow(ctx, `
        SELECT tenant_id::text, channel, destination, code_hash,
               template_id::text, expires_at, attempts, max_attempts, consumed_at
          FROM auth_otps WHERE id = $1 FOR UPDATE
    `, challengeID).Scan(&tenantID, &channel, &destination, &codeHash,
			&templateID, &expiresAt, &attempts, &maxAttempts, &consumedAt)
	} else {
		err = tx.QueryRow(ctx, `
        SELECT tenant_id::text, channel, destination, code_hash,
               template_id::text, expires_at, attempts, max_attempts, consumed_at, otp_key_generation
          FROM auth_otps WHERE id = $1 FOR UPDATE
    `, challengeID).Scan(&tenantID, &channel, &destination, &codeHash,
			&templateID, &expiresAt, &attempts, &maxAttempts, &consumedAt, &keyGen)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if consumedAt != nil {
		return nil, ErrAlreadyUsed
	}
	if time.Now().After(expiresAt) {
		return nil, ErrExpired
	}
	if attempts >= maxAttempts {
		return nil, ErrAttemptsExceeded
	}

	// Bump attempts regardless of outcome (atomic with verify).
	if _, err := tx.Exec(ctx, `UPDATE auth_otps SET attempts = attempts + 1 WHERE id = $1`, challengeID); err != nil {
		return nil, err
	}

	match, err := verifyDigest(ring, keyGen, codeHash, otpkey.Challenge{
		TenantID: tenantID, Channel: channel, Destination: destination, ChallengeID: challengeID,
	}, strings.TrimSpace(code))
	if err != nil {
		return nil, err
	}
	if !match {
		// Persist the attempt count, then fail.
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrCodeMismatch
	}

	if _, err := tx.Exec(ctx, `UPDATE auth_otps SET consumed_at = now() WHERE id = $1`, challengeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out := &Verified{
		ChallengeID: challengeID,
		TenantID:    tenantID,
		Channel:     channel,
		Destination: destination,
	}
	if templateID != nil {
		out.TemplateID = *templateID
	}
	return out, nil
}

// generate6 returns a 6-digit code (left-padded with zeros).
func generate6() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) % 1_000_000
	return fmt.Sprintf("%06d", n)
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hashOTP(salt, code string) string {
	h := sha256.Sum256([]byte(salt + "|" + code))
	return hex.EncodeToString(h[:])
}

// verifyDigest checks a candidate code against the stored digest. A row that pins a key generation
// (keyGen != nil) is verified with that exact generation's keyed HMAC and REQUIRES a ring — a pinned
// row with no ring loaded fails closed (a keyed OTP must never fall back to a weaker check). A row
// with no pinned generation is a legacy salt:sha256 OTP and is verified with the constant-time legacy
// comparison (compatibility window for already-issued codes).
func verifyDigest(ring *otpkey.Ring, keyGen *int, stored string, c otpkey.Challenge, code string) (bool, error) {
	if keyGen != nil {
		if ring == nil {
			return false, fmt.Errorf("otp: row pins key generation %d but no OTP key ring is loaded", *keyGen)
		}
		return ring.Verify(*keyGen, stored, c, code)
	}
	// legacy salt:sha256 (no pinned generation)
	if !otpkey.IsLegacyFormat(stored) {
		return false, fmt.Errorf("otp: malformed legacy digest")
	}
	return otpkey.VerifyLegacy(stored, code)
}
