// Package poststay is the Post-Stay PIN lifecycle: issuance from an already-authenticated guest, one-time
// reveal, throttled verification, operator reset and revocation, and the zero-price Post-Stay conversion.
//
// THE PROPERTY THIS PACKAGE EXISTS TO HOLD. A Post-Stay PIN is the only credential in the system whose
// subject has already left. Everything here is arranged so that it can never be inherited by whoever occupies
// the room next:
//
//   - the subject is a PROFILE bound to a Stay EPISODE, never a room. There is no room-keyed lookup anywhere
//     in this package, so there is no room-number oracle to attack;
//   - a reinstatement increments lifecycle_version and the profile dies with its episode, in the database,
//     without anything having to remember to revoke it;
//   - issuance is only reachable from an ALREADY-AUTHENTICATED guest (or an audited operator). There is no
//     anonymous issuance path, which is why there is no endpoint here that takes a room number;
//   - the plaintext exists exactly once, in the response to the call that minted it. It is never stored and
//     never re-derivable, so "reveal it again" is not a permission that can be granted — only a NEW PIN can
//     be issued, which is a different secret and an audited act.
//
// SCOPE (deliberate, fail-closed): Post-Stay v1 grants INCLUDED (zero-price) package revisions only. A priced
// or settlement-requiring revision is refused with ErrSettlementRequired. No money moves, no PMS posting is
// attempted, and posting_allowed is structurally false for a POST_STAY_ACTIVE Stay.
package poststay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"

	"github.com/stayconnect/enterprise/data-plane/internal/codegen"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// ThrottleMethod is the auth_throttle_buckets method for post-stay attempts. It is deliberately its OWN
// method (migration 0028): a post-stay brute force must not consume — or be masked by — the budget the same
// guest, IP or device has for ordinary portal authentication.
const ThrottleMethod = "post_stay_pin"

// CapPostStayIdentity is the Phase-5 controlled-operation family that owns writes to post_stay_profiles.
const CapPostStayIdentity = "post_stay_identity"

// PIN policy. This reuses the codegen package that already generates voucher codes rather than inventing a
// second alphabet, and the reasons carry over exactly:
//
//   - I, L, O and U are ALWAYS excluded, so a PIN survives normalization unchanged and the guest can type
//     what they were shown;
//   - ExcludeAmbiguous additionally removes 0, 1, 5 and S — the visual-confusion sets — because this secret
//     is read off a screen and typed on a phone, which is exactly the case that policy was written for;
//   - selection is unbiased crypto/rand.
//
// The remaining alphabet is 28 symbols; at 8 characters that is ~38 bits, which is a deliberate choice
// against a DURABLE, fail-closed throttle with a hard lockout and a bounded validity window — not against an
// offline attacker, who never sees the hash.
const (
	pinLength = 8
	pinMode   = codegen.ModeAlnum
)

// Argon2id parameters, matching the hashes StayConnect already issues elsewhere.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

var (
	// ErrNotAuthenticable — the profile is revoked, expired, or bound to an episode the Stay has left. It is
	// also what a WRONG PIN returns: the caller must not be able to tell those apart, and neither must the
	// guest. Every distinction here would be an oracle.
	ErrNotAuthenticable = errors.New("poststay: not authenticable")
	// ErrThrottled — the durable throttle refused this attempt. Separate from ErrNotAuthenticable because the
	// CALLER needs to know to send a Retry-After; the guest-visible envelope stays uniform.
	ErrThrottled = errors.New("poststay: throttled")
	// ErrNotEligible — issuance was attempted from a Stay that is not IN_HOUSE or CHECKED_OUT at the episode
	// named, or that already has a profile for this episode.
	ErrNotEligible = errors.New("poststay: stay is not eligible for a post-stay profile")
	// ErrSettlementRequired — the package revision carries a price or needs a settlement method other than
	// NOT_REQUIRED. Post-Stay v1 is zero-price and refuses rather than silently granting paid access free.
	ErrSettlementRequired = errors.New("poststay: post-stay v1 grants zero-price packages only")
	// ErrPackageNotGrantable — the revision is not a currently-visible POST_STAY package in this scope.
	ErrPackageNotGrantable = errors.New("poststay: package revision is not a grantable post-stay package")
	// ErrAlreadyConverted — this episode has already converted; the operation is idempotent by refusal rather
	// than by creating a second entitlement.
	ErrAlreadyConverted = errors.New("poststay: this episode has already converted to post-stay")
)

// Store is the DB-backed post-stay repository.
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

// ---- PIN material ------------------------------------------------------------------------------------------

func generatePIN() (string, error) {
	codes, err := codegen.GenerateN(1, codegen.Options{
		Length: pinLength, Mode: pinMode, ExcludeAmbiguous: true,
	})
	if err != nil {
		return "", err
	}
	return codes[0], nil
}

func hashPIN(pin string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pin), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// dummyHash is verified against when no profile matches, so a wrong PIN and an unknown profile take the same
// work. Without it the response time itself answers "does this profile exist", which is the oracle this whole
// design refuses to provide.
var dummyHash = mustDummy()

func mustDummy() string {
	h, err := hashPIN("dummy-constant-work")
	if err != nil {
		panic("poststay: cannot build the constant-work hash: " + err.Error())
	}
	return h
}

func verifyPIN(pin, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var mem uint32
	var t, p int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	// Bounds: a hostile or corrupt hash must not be able to make this call allocate gigabytes or spin.
	if mem < 8 || mem > 262144 || t < 1 || t > 10 || p < 1 || p > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(pin), salt, uint32(t), mem, uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NormalizePIN prepares guest input for verification: it uppercases and strips dashes, spaces and tabs, which
// is exactly what codegen.Normalize does and therefore exactly what the generator round-trips through.
//
// It does NOT fold confusable characters, and does not need to: the generator excludes I, L, O and U always
// and 0, 1, 5 and S under ExcludeAmbiguous, so neither half of any confusable pair is ever in a PIN. A guest
// who reads an O where a 0 was printed cannot be right either way, because a PIN contains neither.
func NormalizePIN(s string) string { return codegen.Normalize(s) }

// ---- issuance ----------------------------------------------------------------------------------------------

// IssueRequest is a post-stay PIN issuance. The caller has ALREADY authenticated the guest — this package
// never authenticates one — and passes the Stay that authentication resolved to.
type IssueRequest struct {
	Tenant, Site string
	Stay         string
	// Operator is set ONLY for an operator-driven reset/re-issue, and is recorded as the issuer. A guest
	// self-service issuance leaves it empty; the database refuses the incoherent combinations either way.
	Operator string
	ValidFor time.Duration
}

// Issued is what an issuance returns. PIN is the plaintext, and this is the ONLY time it exists: it is not
// stored, not logged, and not re-derivable from the hash. A caller that drops it has to issue a new one.
type Issued struct {
	Profile string
	PIN     string
	Expires time.Time
}

// Issue mints a Post-Stay profile for the CURRENT episode of an authenticated guest's Stay and returns the
// plaintext PIN exactly once.
//
// The one-time reveal is recorded in the SAME transaction that mints the PIN, because the plaintext is
// returned by this call and by nothing else. There is deliberately no separate Reveal operation: revealing a
// stored plaintext later would require storing it, and re-deriving it from the hash is impossible by design.
// So "reveal once" is not a rule the code has to remember — it is the only thing the system is capable of.
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

// IssueTx is Issue inside the caller's transaction, so an issuance can commit atomically with whatever
// authenticated act produced it.
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
	// The episode is read from the Stay row itself, INSIDE the insert, so the caller cannot name one. The
	// database guard then re-checks that it is the current episode and that the Stay is IN_HOUSE or
	// CHECKED_OUT; both layers refuse, and neither is load-bearing alone.
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
		// ErrNotEligible is the ANSWER a caller acts on, but collapsing every failure into it would hide a
		// genuine database fault behind a business refusal — the one shape of bug that is impossible to
		// diagnose from a log. The cause is wrapped so %v shows it while errors.Is still matches.
		if errors.Is(err, pgx.ErrNoRows) {
			return Issued{}, fmt.Errorf("%w: stay not found in scope", ErrNotEligible)
		}
		return Issued{}, fmt.Errorf("%w: %v", ErrNotEligible, err)
	}
	return Issued{Profile: profile, PIN: pin, Expires: expires}, nil
}

// ResetRequest re-issues a PIN for an EXISTING profile as an audited operator action. The old PIN stops
// working the instant this commits, because the hash it verified against is gone.
type ResetRequest struct {
	Tenant, Site string
	Profile      string
	Operator     string
	Reason       string
	ValidFor     time.Duration
}

// Reset mints a NEW generation for an existing profile and returns the new plaintext once. It does not
// resurrect a revoked profile — that is a new profile, not a new PIN — and it does not extend the episode.
func (s *Store) Reset(ctx context.Context, req ResetRequest) (Issued, error) {
	if strings.TrimSpace(req.Operator) == "" || strings.TrimSpace(req.Reason) == "" {
		return Issued{}, ErrNotEligible
	}
	if req.ValidFor <= 0 || req.ValidFor > 30*24*time.Hour {
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
		    valid_until=now() + make_interval(secs => $5)
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

// RevokeRequest kills a profile. Revocation is a STATE, not a deletion: the row remains as the durable answer
// to who was allowed post-stay access and on whose authority.
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

// ---- verification --------------------------------------------------------------------------------------------

// VerifyRequest is a guest's PIN attempt. Profile identifies WHICH post-stay identity is being claimed; it is
// not secret and not sufficient — the PIN is the secret. There is no variant of this call that takes a room
// number, and that absence is the point.
type VerifyRequest struct {
	Tenant, Site string
	Profile      string
	PIN          string
	// Device and IP are throttle dimensions only. They are hashed before storage by the throttle store and
	// never recorded here.
	Device string
	IP     string
}

// Verify charges the durable throttle, then verifies the PIN against a profile that is authenticable RIGHT
// NOW. Every failure returns ErrNotAuthenticable — wrong PIN, unknown profile, revoked, expired, or an
// episode that has moved on are indistinguishable to the caller, and the constant-work dummy hash keeps them
// indistinguishable in TIME as well.
//
// The throttle is charged BEFORE the hash is computed, so an attacker cannot use the argon2 cost as an
// amplifier, and it fails closed: a throttle store that cannot be reached denies the attempt.
func (s *Store) Verify(ctx context.Context, req VerifyRequest) (string, error) {
	if s.thr != nil {
		rules := []throttle.Rule{
			{Kind: throttle.ScopeIdentity, Value: req.Profile, Method: ThrottleMethod, Limit: 5,
				Block: 15 * time.Minute},
			{Kind: throttle.ScopeDevice, Value: req.Device, Method: ThrottleMethod, Limit: 10,
				Block: 15 * time.Minute},
			{Kind: throttle.ScopeEndpoint, Value: "post-stay-pin", Method: ThrottleMethod, Limit: 200},
		}
		if strings.TrimSpace(req.IP) != "" {
			rules = append(rules, throttle.Rule{Kind: throttle.ScopeIP, Value: req.IP,
				Method: ThrottleMethod, Limit: 20, Block: 15 * time.Minute})
		}
		d, err := s.thr.Allow(ctx, rules...)
		if err != nil || !d.Allowed {
			return "", ErrThrottled
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
			// Constant work for an unknown or dead profile. Skipping this would make the response time
			// answer a question the uniform error refuses to.
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
