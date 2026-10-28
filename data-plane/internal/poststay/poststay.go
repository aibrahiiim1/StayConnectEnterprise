// Package poststay is the Post-Stay PIN lifecycle. The PIN's subject is a profile bound to a Stay episode,
// never a room, so it cannot be inherited by the next occupant: there is no room-keyed lookup, a
// reinstatement kills the profile in the database, issuance requires an authenticated guest or audited
// operator, and the plaintext exists only in the issuing call's response. v1 grants zero-price packages only.
package poststay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/codegen"
	"github.com/stayconnect/enterprise/data-plane/internal/passwordhash"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// ThrottleMethod is its own method so a post-stay brute force neither consumes nor is masked by the budget
// for ordinary portal authentication.
const ThrottleMethod = "post_stay_pin"

const CapPostStayIdentity = "post_stay_identity"

// PIN policy reuses codegen: I, L, O, U always excluded, plus 0, 1, 5, S via ExcludeAmbiguous because the
// PIN is read off a screen. 28 symbols at 8 characters is ~38 bits, sized against the durable fail-closed
// throttle rather than an offline attacker (who never sees the hash).
const (
	pinLength = 8
	pinMode   = codegen.ModeAlnum
)

var (
	// ErrNotAuthenticable covers revoked, expired, left-episode and wrong-PIN alike; any distinction would be an oracle.
	ErrNotAuthenticable = errors.New("poststay: not authenticable")
	// ErrThrottled is separate so the caller can send Retry-After; the guest-visible envelope stays uniform.
	ErrThrottled   = errors.New("poststay: throttled")
	ErrNotEligible = errors.New("poststay: stay is not eligible for a post-stay profile")
	// ErrSettlementRequired: v1 refuses priced packages rather than silently granting them free.
	ErrSettlementRequired  = errors.New("poststay: post-stay v1 grants zero-price packages only")
	ErrPackageNotGrantable = errors.New("poststay: package revision is not a grantable post-stay package")
	ErrAlreadyConverted    = errors.New("poststay: this episode has already converted to post-stay")
)

type Store struct {
	pool *pgxpool.Pool
	thr  *throttle.Store
	now  func() time.Time
}

func New(pool *pgxpool.Pool, thr *throttle.Store) *Store {
	return &Store{pool: pool, thr: thr, now: time.Now}
}

// SetClock is for tests only.
func (s *Store) SetClock(f func() time.Time) { s.now = f }

func generatePIN() (string, error) {
	codes, err := codegen.GenerateN(1, codegen.Options{
		Length: pinLength, Mode: pinMode, ExcludeAmbiguous: true,
	})
	if err != nil {
		return "", err
	}
	return codes[0], nil
}

// The PIN is hashed with the one argon2id implementation (internal/passwordhash): same PHC format and parameters
// (64 MiB, t=1, p=4, 16-byte salt, 32-byte key) as before, so stored hashes still verify, and the same process-wide
// bound on concurrent hashes applies.
func hashPIN(pin string) (string, error) { return passwordhash.Hash(pin) }

// dummyHash is verified when no profile matches, so response time does not reveal whether a profile exists.
var dummyHash = passwordhash.Dummy()

func verifyPIN(pin, encoded string) bool {
	ok, err := passwordhash.Verify(pin, encoded)
	return err == nil && ok
}

// NormalizePIN matches codegen.Normalize (uppercase, strip dashes/spaces/tabs). It does not fold confusable
// characters because the generator never emits either half of a confusable pair.
func NormalizePIN(s string) string { return codegen.Normalize(s) }

// IssueRequest assumes the caller has already authenticated the guest and resolved the Stay.
type IssueRequest struct {
	Tenant, Site string
	Stay         string
	// Operator is set only for an operator reset/re-issue and recorded as the issuer.
	Operator string
	ValidFor time.Duration
}

// Issued carries the plaintext PIN, which exists only here: never stored, logged or re-derivable.
type Issued struct {
	Profile string
	PIN     string
	Expires time.Time
}

// Issue mints a profile for the Stay's current episode and returns the plaintext PIN once. There is
// deliberately no separate Reveal: that would require storing the plaintext.
func (s *Store) Issue(ctx context.Context, req IssueRequest) (Issued, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Issued{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := s.IssueTx(ctx, tx, req)
	if err != nil {
		return Issued{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Issued{}, err
	}
	return out, nil
}

// IssueTx runs inside the caller's transaction so issuance commits atomically with the act that produced it.
func (s *Store) IssueTx(ctx context.Context, tx pgx.Tx, req IssueRequest) (Issued, error) {
	if req.ValidFor <= 0 || req.ValidFor > 30*24*time.Hour {
		return Issued{}, ErrNotEligible
	}
	if err := writerguard.OpenPhase5(ctx, tx, CapPostStayIdentity); err != nil {
		return Issued{}, err
	}
	pin, err := generatePIN()
	if err != nil {
		return Issued{}, err
	}
	hash, err := hashPIN(pin)
	if err != nil {
		return Issued{}, err
	}
	issuedVia, operator := "GUEST_AUTHENTICATED_SESSION", any(nil)
	if strings.TrimSpace(req.Operator) != "" {
		issuedVia, operator = "OPERATOR_RESET", req.Operator
	}
	var profile string
	var expires time.Time
	// The episode is read from the Stay row inside the insert so the caller cannot name one; the database
	// guard re-checks it is current and the Stay is IN_HOUSE or CHECKED_OUT.
	err = tx.QueryRow(ctx, `INSERT INTO iam_v2.post_stay_profiles
		(tenant_id, site_id, origin_stay_id, origin_lifecycle_version, pin_hash, valid_until,
		 issued_via, issued_by_operator, pin_revealed_at)
		SELECT $1, $2, st.id, st.lifecycle_version, $4, now() + make_interval(secs => $5), $6, $7, now()
		  FROM iam_v2.stays st
		 WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.id=$3
		RETURNING id::text, valid_until`,
		req.Tenant, req.Site, req.Stay, hash, req.ValidFor.Seconds(), issuedVia, operator).
		Scan(&profile, &expires)
	if err != nil {
		// Wrap the cause so a database fault stays visible while errors.Is(ErrNotEligible) still matches.
		if errors.Is(err, pgx.ErrNoRows) {
			return Issued{}, fmt.Errorf("%w: stay not found in scope", ErrNotEligible)
		}
		return Issued{}, fmt.Errorf("%w: %v", ErrNotEligible, err)
	}
	return Issued{Profile: profile, PIN: pin, Expires: expires}, nil
}

// ResetRequest re-issues a PIN for an existing profile as an audited operator action; the old PIN stops
// working on commit.
type ResetRequest struct {
	Tenant, Site string
	Profile      string
	Operator     string
	Reason       string
	// ValidFor, when positive, also moves validity to now+ValidFor; zero keeps the window unchanged.
	ValidFor time.Duration
}

// Reset mints a new PIN generation and returns the plaintext once. It does not resurrect a revoked profile
// or extend the episode.
func (s *Store) Reset(ctx context.Context, req ResetRequest) (Issued, error) {
	if strings.TrimSpace(req.Operator) == "" || strings.TrimSpace(req.Reason) == "" {
		return Issued{}, ErrNotEligible
	}
	if req.ValidFor < 0 || req.ValidFor > 30*24*time.Hour {
		return Issued{}, ErrNotEligible
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Issued{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := writerguard.OpenPhase5(ctx, tx, CapPostStayIdentity); err != nil {
		return Issued{}, err
	}
	pin, err := generatePIN()
	if err != nil {
		return Issued{}, err
	}
	hash, err := hashPIN(pin)
	if err != nil {
		return Issued{}, err
	}
	var expires time.Time
	err = tx.QueryRow(ctx, `UPDATE iam_v2.post_stay_profiles
		SET pin_hash=$4, pin_generation=pin_generation+1, pin_set_at=now(), pin_revealed_at=now(),
		    valid_until=CASE WHEN $5::double precision > 0 THEN now() + make_interval(secs => $5)
		                     ELSE valid_until END
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3 AND status='ACTIVE'
		RETURNING valid_until`,
		req.Tenant, req.Site, req.Profile, hash, req.ValidFor.Seconds()).Scan(&expires)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Issued{}, ErrNotAuthenticable
		}
		return Issued{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Issued{}, err
	}
	return Issued{Profile: req.Profile, PIN: pin, Expires: expires}, nil
}

// RevokeRequest revokes a profile; revocation is a state, not a deletion, so the row stays as the record.
type RevokeRequest struct {
	Tenant, Site string
	Profile      string
	Operator     string
	Reason       string
}

func (s *Store) Revoke(ctx context.Context, req RevokeRequest) error {
	if strings.TrimSpace(req.Operator) == "" || strings.TrimSpace(req.Reason) == "" {
		return ErrNotEligible
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := writerguard.OpenPhase5(ctx, tx, CapPostStayIdentity); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `UPDATE iam_v2.post_stay_profiles
		SET status='REVOKED', revoked_at=now(), revoked_by=$4, revoke_reason=$5
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3 AND status='ACTIVE'`,
		req.Tenant, req.Site, req.Profile, req.Operator, req.Reason)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotAuthenticable
	}
	return tx.Commit(ctx)
}

// VerifyRequest is a guest's PIN attempt. Profile is not secret and not sufficient; no variant takes a room number.
type VerifyRequest struct {
	Tenant, Site string
	Profile      string
	PIN          string
	// Device and IP are throttle dimensions only; the throttle store hashes them.
	Device string
	IP     string
}

// chargeThrottle charges one attempt against every dimension. It is shared by the profile-named and
// device-derived paths so neither is a way around the other's limits. The throttle is charged before the
// argon2 hash so its cost is not an amplifier, and an unreachable store denies the attempt.
func (s *Store) chargeThrottle(ctx context.Context, req VerifyRequest) error {
	rules := []throttle.Rule{
		{Kind: throttle.ScopeDevice, Value: req.Device, Method: ThrottleMethod, Limit: 10,
			Block: 15 * time.Minute},
		{Kind: throttle.ScopeEndpoint, Value: "post-stay-pin", Method: ThrottleMethod, Limit: 200},
	}
	if strings.TrimSpace(req.Profile) != "" {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity, Value: req.Profile,
			Method: ThrottleMethod, Limit: 5, Block: 15 * time.Minute})
	}
	if strings.TrimSpace(req.IP) != "" {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIP, Value: req.IP,
			Method: ThrottleMethod, Limit: 20, Block: 15 * time.Minute})
	}
	d, err := s.thr.Allow(ctx, rules...)
	if err != nil || !d.Allowed {
		return ErrThrottled
	}
	return nil
}

// Verify returns ErrNotAuthenticable for every failure, indistinguishable by value and (via the dummy hash) by time.
func (s *Store) Verify(ctx context.Context, req VerifyRequest) (string, error) {
	if s.thr != nil {
		if err := s.chargeThrottle(ctx, req); err != nil {
			return "", err
		}
	}

	var hash string
	err := s.pool.QueryRow(ctx, `SELECT p.pin_hash
		FROM iam_v2.post_stay_profiles p
		WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.id=$3
		  AND iam_v2.p5_post_stay_authenticable($1,$2,$3)`,
		req.Tenant, req.Site, req.Profile).Scan(&hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Constant work so timing does not answer what the uniform error refuses to.
			_ = verifyPIN(NormalizePIN(req.PIN), dummyHash)
			return "", ErrNotAuthenticable
		}
		return "", err
	}
	if !verifyPIN(NormalizePIN(req.PIN), hash) {
		return "", ErrNotAuthenticable
	}
	return req.Profile, nil
}
