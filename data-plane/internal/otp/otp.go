// Package otp issues and verifies one-time guest codes. Salted SHA-256 is enough because brute force is bounded by the
// attempt cap and short TTL, not the hash work factor.
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
	IssueCooldown      = 60 * time.Second
	IssueHourlyCap     = 5
	IPHourlyCap        = 20
	// DestinationHourlyCap is the ceiling for one destination across every device (AD-30). It is higher than the
	// per-device cap, so one device cannot use up another device's codes, but a number still cannot be flooded.
	DestinationHourlyCap = 15
)

// Limits are the issue-side limits. They are site settings (iam_v2.site_signin_capacity_settings); a zero field means
// its default, so a caller with no settings gets exactly the previous behaviour.
type Limits struct {
	Lifetime             time.Duration
	MaxAttempts          int
	DeviceHourlyCap      int // codes per destination AND device per hour
	DestinationHourlyCap int // codes per destination, all devices, per hour
	SourceHourlyCap      int // codes per source address per hour, all destinations
}

func (l Limits) WithDefaults() Limits {
	if l.Lifetime <= 0 {
		l.Lifetime = DefaultTTL
	}
	if l.MaxAttempts <= 0 {
		l.MaxAttempts = DefaultMaxAttempts
	}
	if l.DeviceHourlyCap <= 0 {
		l.DeviceHourlyCap = IssueHourlyCap
	}
	if l.DestinationHourlyCap <= 0 {
		l.DestinationHourlyCap = DestinationHourlyCap
	}
	if l.DestinationHourlyCap < l.DeviceHourlyCap {
		l.DestinationHourlyCap = l.DeviceHourlyCap
	}
	if l.SourceHourlyCap <= 0 {
		l.SourceHourlyCap = IPHourlyCap
	}
	return l
}

var (
	ErrCooldown  = errors.New("otp: cooldown — wait before requesting another code")
	ErrHourlyCap = errors.New("otp: too many requests this hour")
	// ErrDestinationCap: this destination has been sent its ceiling of codes this hour, from all devices together.
	ErrDestinationCap   = errors.New("otp: too many codes sent to this destination this hour")
	ErrIPRateLimited    = errors.New("otp: too many requests from your network")
	ErrNotFound         = errors.New("otp: challenge not found")
	ErrExpired          = errors.New("otp: code expired")
	ErrAttemptsExceeded = errors.New("otp: too many wrong attempts")
	ErrAlreadyUsed      = errors.New("otp: code already used")
	ErrCodeMismatch     = errors.New("otp: incorrect code")
	// ErrOtherDevice: the code was presented from a different device than it was issued to; it is not a wrong guess and
	// is not counted.
	ErrOtherDevice = errors.New("otp: code was requested from another device")
)

type IssueParams struct {
	TenantID    string
	ApplianceID string
	TemplateID  string
	Channel     string
	Destination string
	IP          string
	UserAgent   string
	// DeviceMAC binds the challenge to that device (iam_v2.otp_device_bindings); VerifyFrom refuses any other.
	DeviceMAC string
	// Limits are the site's settings; the zero value means the defaults.
	Limits Limits
}

// bindDevice failing fails the issue: an unbound code would be verifiable from anywhere. Old bindings are pruned by scd's periodic job
// (pruneOTPEscrow), not on this path.
func bindDevice(ctx context.Context, tx pgx.Tx, challengeID, tenantID, mac string) error {
	if strings.TrimSpace(mac) == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.otp_device_bindings (challenge_id, tenant_id, device_mac)
	     VALUES ($1::uuid, $2::uuid, $3::macaddr)`, challengeID, tenantID, mac); err != nil {
		return fmt.Errorf("otp: bind device: %w", err)
	}
	return nil
}

type Issued struct {
	ChallengeID string
	Code        string
	ExpiresAt   time.Time
}

// Issue enforces cooldown and hourly caps, then stores the code. The checks and the insert run in one transaction under advisory locks on the
// destination and the source address, so parallel requests cannot all pass the same count. With a ring the code_hash is a keyed HMAC pinned to the
// active generation; with a nil ring it is the legacy salt:sha256 and otp_key_generation stays NULL.
func Issue(ctx context.Context, db *pgxpool.Pool, ring *otpkey.Ring, p IssueParams) (*Issued, error) {
	if p.TenantID == "" || p.Channel == "" || p.Destination == "" {
		return nil, fmt.Errorf("otp: tenant/channel/destination required")
	}
	dest := strings.ToLower(strings.TrimSpace(p.Destination))
	lim := p.Limits.WithDefaults()
	devMAC := strings.TrimSpace(p.DeviceMAC)

	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("otp: begin: %w", err)
	}
	defer tx.Rollback(ctx)
	// Lock order is always destination, then address, so two requests never wait on each other in a cycle.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('otp-dest:' || $1 || ':' || $2 || ':' || $3))`,
		p.TenantID, p.Channel, dest); err != nil {
		return nil, fmt.Errorf("otp: lock: %w", err)
	}
	if p.IP != "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('otp-ip:' || $1 || ':' || $2))`, p.TenantID, p.IP); err != nil {
			return nil, fmt.Errorf("otp: lock: %w", err)
		}
	}

	// AD-30: the cooldown and the hourly cap are counted per destination AND device, so one device cannot use up the
	// codes of the guest who owns the number from another device; a higher destination-wide ceiling still stops a number
	// being flooded by many devices. A request with no device (none reaches here in production) is counted destination-wide.
	var lastAt *time.Time
	var devHour, destHour int
	if err := tx.QueryRow(ctx, `
        SELECT max(o.issued_at) FILTER (WHERE $4 = '' OR b.device_mac = NULLIF($4,'')::macaddr),
               count(*) FILTER (WHERE o.issued_at > now() - interval '1 hour'
                                  AND ($4 = '' OR b.device_mac = NULLIF($4,'')::macaddr)),
               count(*) FILTER (WHERE o.issued_at > now() - interval '1 hour')
          FROM auth_otps o
          LEFT JOIN iam_v2.otp_device_bindings b ON b.challenge_id = o.id
         WHERE o.tenant_id = $1 AND o.channel = $2 AND lower(o.destination) = $3
    `, p.TenantID, p.Channel, dest, devMAC).Scan(&lastAt, &devHour, &destHour); err != nil {
		return nil, fmt.Errorf("otp: rate-limit lookup: %w", err)
	}
	if lastAt != nil && time.Since(*lastAt) < IssueCooldown {
		return nil, ErrCooldown
	}
	if devHour >= lim.DeviceHourlyCap {
		return nil, ErrHourlyCap
	}
	if destHour >= lim.DestinationHourlyCap {
		return nil, ErrDestinationCap
	}

	// Per-source-IP limit across all destinations and channels (anti-enumeration).
	if p.IP != "" {
		var ipHour int
		if err := tx.QueryRow(ctx, `
            SELECT count(*) FROM auth_otps
             WHERE tenant_id = $1
               AND ip = $2::inet
               AND issued_at > now() - interval '1 hour'
        `, p.TenantID, p.IP).Scan(&ipHour); err != nil {
			return nil, fmt.Errorf("otp: ip rate-limit lookup: %w", err)
		}
		if ipHour >= lim.SourceHourlyCap {
			return nil, ErrIPRateLimited
		}
	}

	code := generate6()
	expires := time.Now().Add(lim.Lifetime)

	var id string
	if ring == nil {
		salt := randHex(8)
		hash := hashOTP(salt, code)
		if err := tx.QueryRow(ctx, `
        INSERT INTO auth_otps
          (tenant_id, appliance_id, channel, destination,
           code_hash, expires_at, max_attempts, ip, user_agent)
        VALUES
          ($1, NULLIF($2,'')::uuid, $3, $4,
           $5, $6, $7,
           CASE WHEN $8 = '' THEN NULL ELSE $8::inet END,
           NULLIF($9,''))
        RETURNING id
    `, p.TenantID, p.ApplianceID, p.Channel, dest,
			salt+":"+hash, expires, lim.MaxAttempts, p.IP, p.UserAgent).Scan(&id); err != nil {
			return nil, fmt.Errorf("otp: insert: %w", err)
		}
	} else {
		// The digest binds the challenge id, so the id is generated before the HMAC is computed.
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return nil, fmt.Errorf("otp: id gen: %w", err)
		}
		gen, digestHex, err := ring.Issue(otpkey.Challenge{
			TenantID: p.TenantID, Channel: p.Channel, Destination: dest, ChallengeID: id,
		}, code)
		if err != nil {
			return nil, fmt.Errorf("otp: hmac issue: %w", err)
		}
		if _, err := tx.Exec(ctx, `
        INSERT INTO auth_otps
          (id, tenant_id, appliance_id, channel, destination,
           code_hash, otp_key_generation, expires_at, max_attempts, ip, user_agent)
        VALUES
          ($1::uuid, $2, NULLIF($3,'')::uuid, $4, $5,
           $6, $7, $8, $9,
           CASE WHEN $10 = '' THEN NULL ELSE $10::inet END,
           NULLIF($11,''))
    `, id, p.TenantID, p.ApplianceID, p.Channel, dest,
			digestHex, gen, expires, lim.MaxAttempts, p.IP, p.UserAgent); err != nil {
			return nil, fmt.Errorf("otp: insert: %w", err)
		}
	}
	if err := bindDevice(ctx, tx, id, p.TenantID, p.DeviceMAC); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("otp: commit: %w", err)
	}
	return &Issued{ChallengeID: id, Code: code, ExpiresAt: expires}, nil
}

type Verified struct {
	ChallengeID string
	TenantID    string
	Channel     string
	Destination string
}

// VerifyFrom fails closed: no device, another device, or no binding is refused with ErrOtherDevice before the code is
// compared, so no attempt is counted. An unbound challenge can only predate device binding and has already expired.
func VerifyFrom(ctx context.Context, db *pgxpool.Pool, ring *otpkey.Ring, challengeID, code, deviceMAC string) (*Verified, error) {
	mac := strings.TrimSpace(deviceMAC)
	if mac == "" {
		return nil, ErrOtherDevice
	}
	var bound *string
	if err := db.QueryRow(ctx, `SELECT device_mac::text FROM iam_v2.otp_device_bindings WHERE challenge_id = $1::uuid`,
		challengeID).Scan(&bound); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("otp: device binding: %w", err)
	}
	if bound == nil || !strings.EqualFold(*bound, mac) {
		return nil, ErrOtherDevice
	}
	return verify(ctx, db, ring, challengeID, code)
}

// verify records the attempt and consumes the row on success; attempt increment and consume are atomic in one FOR
// UPDATE tx. A pinned key generation verifies with its keyed HMAC; a NULL generation is a legacy OTP accepted only
// while unexpired. It does not check the device: guest paths call VerifyFrom.
func verify(ctx context.Context, db *pgxpool.Pool, ring *otpkey.Ring, challengeID, code string) (*Verified, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var (
		tenantID, channel, destination, codeHash string
		expiresAt                                time.Time
		attempts, maxAttempts                    int
		consumedAt                               *time.Time
		keyGen                                   *int
	)
	if ring == nil {
		err = tx.QueryRow(ctx, `
        SELECT tenant_id::text, channel, destination, code_hash,
               expires_at, attempts, max_attempts, consumed_at
          FROM auth_otps WHERE id = $1 FOR UPDATE
    `, challengeID).Scan(&tenantID, &channel, &destination, &codeHash,
			&expiresAt, &attempts, &maxAttempts, &consumedAt)
	} else {
		err = tx.QueryRow(ctx, `
        SELECT tenant_id::text, channel, destination, code_hash,
               expires_at, attempts, max_attempts, consumed_at, otp_key_generation
          FROM auth_otps WHERE id = $1 FOR UPDATE
    `, challengeID).Scan(&tenantID, &channel, &destination, &codeHash,
			&expiresAt, &attempts, &maxAttempts, &consumedAt, &keyGen)
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
	return &Verified{
		ChallengeID: challengeID,
		TenantID:    tenantID,
		Channel:     channel,
		Destination: destination,
	}, nil
}

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

// A row pinning a key generation REQUIRES a ring and fails closed without one; a NULL generation uses the constant-time
// legacy comparison.
func verifyDigest(ring *otpkey.Ring, keyGen *int, stored string, c otpkey.Challenge, code string) (bool, error) {
	if keyGen != nil {
		if ring == nil {
			return false, fmt.Errorf("otp: row pins key generation %d but no OTP key ring is loaded", *keyGen)
		}
		return ring.Verify(*keyGen, stored, c, code)
	}
	if !otpkey.IsLegacyFormat(stored) {
		return false, fmt.Errorf("otp: malformed legacy digest")
	}
	return otpkey.VerifyLegacy(stored, code)
}
