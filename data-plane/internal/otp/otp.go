// Package otp issues + verifies one-time codes for guest auth.
//
// Design:
//   * 6-digit numeric codes — easy to type on a phone keypad
//   * SHA-256(per-row salt + code) — fast verify, no global secret needed,
//     OTP lifetime is short (10m default) and rate-limited so brute-force
//     resistance comes from the attempt cap, not the hash work-factor
//   * Code is shown to the user once (returned from Issue) and emailed via
//     the Mailer. We never log it server-side after delivery.
package otp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	ErrCooldown        = errors.New("otp: cooldown — wait before requesting another code")
	ErrHourlyCap       = errors.New("otp: too many requests this hour")
	ErrIPRateLimited   = errors.New("otp: too many requests from your network")
	ErrNotFound        = errors.New("otp: challenge not found")
	ErrExpired         = errors.New("otp: code expired")
	ErrAttemptsExceeded = errors.New("otp: too many wrong attempts")
	ErrAlreadyUsed     = errors.New("otp: code already used")
	ErrCodeMismatch    = errors.New("otp: incorrect code")
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
func Issue(ctx context.Context, db *pgxpool.Pool, p IssueParams) (*Issued, error) {
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
	salt := randHex(8)
	hash := hashOTP(salt, code)
	expires := time.Now().Add(DefaultTTL)

	var id string
	err := db.QueryRow(ctx, `
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
		salt+":"+hash, expires, DefaultMaxAttempts, p.IP, p.UserAgent).Scan(&id)
	if err != nil {
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
func Verify(ctx context.Context, db *pgxpool.Pool, challengeID, code string) (*Verified, error) {
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
	)
	err = tx.QueryRow(ctx, `
        SELECT tenant_id::text, channel, destination, code_hash,
               template_id::text, expires_at, attempts, max_attempts, consumed_at
          FROM auth_otps WHERE id = $1 FOR UPDATE
    `, challengeID).Scan(&tenantID, &channel, &destination, &codeHash,
		&templateID, &expiresAt, &attempts, &maxAttempts, &consumedAt)
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

	parts := strings.SplitN(codeHash, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("otp: malformed hash")
	}
	want := parts[1]
	got := hashOTP(parts[0], strings.TrimSpace(code))
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
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
