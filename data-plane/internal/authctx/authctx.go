// Package authctx is the one-time, TTL-bounded PMS Auth Context. A successful STRICT resolution issues a context, NOT a guest session; every identity pin
// is SERVER-DERIVED, never client input. The context is consumed EXACTLY once; a replay or expired context is rejected uniformly. This package issues no
// session and no financial command.
package authctx

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// maxTTLSeconds bounds a PMS Auth Context lifetime (a one-time context is short-lived by design).
const maxTTLSeconds = 3600

// ErrGrantIncomplete is a typed, SANITIZED error for a PMS grant missing/invalid a required server-derived
// pin. It carries no guest credential or raw identifier.
var ErrGrantIncomplete = errors.New("authctx: incomplete or invalid PMS grant")

const nilUUID = "00000000-0000-0000-0000-000000000000"

// Reports whether s is a canonical UUID and not the nil UUID; validating in Go returns a typed sanitized error BEFORE any SQL, never a raw PostgreSQL cast error.
func validPinUUID(s string) bool {
	if len(s) != 36 || s == nilUUID {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// A PMS Auth Context is NEVER issued without the full valid pin set.
func (g PMSGrant) valid() bool {
	for _, v := range []string{g.Tenant, g.Site, g.Interface, g.Revision, g.Stay, g.Device, g.GuestNetwork} {
		if !validPinUUID(strings.TrimSpace(v)) {
			return false
		}
	}
	return g.TTLSeconds > 0 && g.TTLSeconds <= maxTTLSeconds
}

// valid reports whether the presenter identity is well-formed; a malformed presenter is a uniform ErrContextInvalid.
func (p Presenter) valid() bool {
	return validPinUUID(strings.TrimSpace(p.Tenant)) && validPinUUID(strings.TrimSpace(p.Site)) &&
		validPinUUID(strings.TrimSpace(p.Device)) && validPinUUID(strings.TrimSpace(p.GuestNetwork))
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// The SERVER-DERIVED pin set: Interface, Revision and Stay come from the verified resolution; Device and GuestNetwork from the trusted request context.
type PMSGrant struct {
	Tenant, Site        string
	Interface, Revision string
	Stay                string
	Device              string
	GuestNetwork        string
	TTLSeconds          int
	// lifecycle_version and occupancy_evidence_version are read from the resolved Stay row inside IssuePMSTx, never supplied by the caller.
}

// Consumed is the server-pinned subject a successful consumption yields; it carries NO guest credential.
type Consumed struct {
	Method    string
	Stay      string
	Interface string
	Revision  string
	// PostStayProfile is the server-pinned subject of a POST_STAY_PIN context (empty otherwise); a caller must read it rather than Stay, since the origin
	// Stay is lineage and treating it as the subject would attach post-stay access to a room again.
	PostStayProfile string
}

// The request-side identity a consumption is checked against: a context pinned to one device, guest network and scope is UNUSABLE from another
// (uniform ErrContextInvalid).
type Presenter struct {
	Tenant       string
	Site         string
	Device       string
	GuestNetwork string
}

// Returned uniformly for a missing, expired, consumed or mispresented context; no detail distinguishes these to the caller.
var ErrContextInvalid = errors.New("authctx: context invalid, expired, consumed, or wrong presenter")

// Creates a one-time PMS Auth Context and returns its opaque id. expires_at is computed SERVER-SIDE (now() + TTL); a non-positive TTL yields an
// already-expired context (fail-closed).
func (s *Store) IssuePMS(ctx context.Context, g PMSGrant) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := s.IssuePMSTx(ctx, tx, g)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// Issues the PMS Auth Context INSIDE the caller's transaction: locks the resolved Stay, verifies scope, IN_HOUSE and valid occupancy evidence from the pinned
// Revision, and reads the AUTHORITATIVE lifecycle_version and occupancy_evidence_version from that row, never from the caller. Fails closed
// (ErrGrantIncomplete) on any missing or invalid pin, without inserting.
func (s *Store) IssuePMSTx(ctx context.Context, tx pgx.Tx, g PMSGrant) (string, error) {
	if !g.valid() {
		return "", ErrGrantIncomplete
	}
	// The Auth Context family is capability-scoped: declare the operation first, or writes to auth_contexts are refused.
	if err := writerguard.Open(ctx, tx, writerguard.CapAuthContext); err != nil {
		return "", err
	}
	var lifecycleVer int
	var evVer int64
	// Authoritative snapshot of the resolved Stay: locked (L1 Stay-first) and eligible per iam_v2.p3_feed_authorizes, the SINGLE definition of the feed-health
	// rule shared with iam_v2.issue_or_return_pms_context (eligibility follows FEED health, not the age of the last event about the Stay). Revalidated here
	// too, so a context ineligible at the moment of issue is never persisted.
	// THE LINEARIZATION POINT, taken AFTER the Stay lock (L1 Stay-first). p3_feed_authorizes is STABLE under READ COMMITTED, so it proves only that the
	// roster was materialized as of its snapshot: a concurrent pmsd admission can commit a PENDING occupancy event right after and this transaction can
	// still commit. SERIALIZABLE does not close it (SSI sees no dangerous cycle). So lock the row admission serializes on: until commit no new occupancy
	// event can be admitted for this interface. Taken through the scoped helper, not inline: a row lock needs UPDATE as well as SELECT, and granting scd
	// UPDATE on the PMS feed state would let the role being authorised rewrite the feed health it is authorised against.
	if _, err := tx.Exec(ctx, `SELECT iam_v2.lock_pms_interface_runtime($1,$2,$3)`,
		g.Tenant, g.Site, g.Interface); err != nil {
		return "", err
	}

	err := tx.QueryRow(ctx, `SELECT st.lifecycle_version, st.occupancy_evidence_version
		FROM iam_v2.stays st
		WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.pms_interface_id=$3 AND st.id=$4
		  AND st.status='IN_HOUSE'
		  AND st.occupancy_evidence_at IS NOT NULL AND st.occupancy_clock_suspect IS NOT TRUE
		  AND st.occupancy_evidence_version > 0
		  AND st.occupancy_revision_id=$5
		  AND iam_v2.p3_feed_authorizes($1,$2,$3,$5, st.occupancy_evidence_at)
		FOR UPDATE OF st`,
		g.Tenant, g.Site, g.Interface, g.Stay, g.Revision).Scan(&lifecycleVer, &evVer)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrGrantIncomplete // stay not resolvable/eligible for a PMS context — never persist one
		}
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts
		(tenant_id, site_id, method, stay_id, pms_interface_id, authentication_interface_revision_id,
		 device_id, guest_network_id, pinned_lifecycle_version, pinned_occupancy_evidence_version, expires_at)
		VALUES ($1,$2,'PMS',$3,$4,$5,$6,$7,$8,$9, now() + make_interval(secs => $10))
		RETURNING id::text`,
		g.Tenant, g.Site, g.Stay, g.Interface, g.Revision, g.Device, g.GuestNetwork, lifecycleVer, evVer, g.TTLSeconds).Scan(&id)
	return id, err
}

// The SERVER-DERIVED pin set for a POST_STAY_PIN context. Deliberately NOT a PMSGrant with blank fields: a post-stay PIN proves none of the interface,
// Revision or occupancy evidence. The subject is the PROFILE; the origin Stay is reached through it and never supplied by the caller, which keeps
// post-stay identity off the room.
type PostStayGrant struct {
	Tenant, Site string
	Profile      string
	Device       string
	GuestNetwork string
	TTLSeconds   int
}

func (g PostStayGrant) valid() bool {
	for _, v := range []string{g.Tenant, g.Site, g.Profile, g.Device, g.GuestNetwork} {
		if !validPinUUID(strings.TrimSpace(v)) {
			return false
		}
	}
	return g.TTLSeconds > 0 && g.TTLSeconds <= maxTTLSeconds
}

// A typed, SANITIZED refusal for a post-stay context that must not exist (profile revoked, past its window, or bound to an episode the Stay has left).
// It does not say WHICH, since distinguishing them would be an oracle.
var ErrPostStayNotAuthenticable = errors.New("authctx: post-stay profile is not authenticable")

// Issues a one-time POST_STAY_PIN context inside the caller's transaction (ideally the one that verified the PIN; this verifies NOTHING about the PIN).
// It binds the context to the profile's CURRENT episode so a reinstatement between issue and consume invalidates it, locking the origin Stay (L1 first)
// and reading the episode AUTHORITATIVELY from it. A profile not authenticable now yields ErrPostStayNotAuthenticable and NO row.
func (s *Store) IssuePostStayTx(ctx context.Context, tx pgx.Tx, g PostStayGrant) (string, error) {
	if !g.valid() {
		return "", ErrGrantIncomplete
	}
	if err := writerguard.Open(ctx, tx, writerguard.CapAuthContext); err != nil {
		return "", err
	}
	var lifecycleVer int
	err := tx.QueryRow(ctx, `SELECT st.lifecycle_version
		FROM iam_v2.post_stay_profiles psp
		JOIN iam_v2.stays st
		  ON st.tenant_id=psp.tenant_id AND st.site_id=psp.site_id AND st.id=psp.origin_stay_id
		WHERE psp.tenant_id=$1 AND psp.site_id=$2 AND psp.id=$3
		  AND iam_v2.p5_post_stay_authenticable($1,$2,$3)
		FOR UPDATE OF st`, g.Tenant, g.Site, g.Profile).Scan(&lifecycleVer)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrPostStayNotAuthenticable
		}
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts
		(tenant_id, site_id, method, post_stay_profile_id, device_id, guest_network_id,
		 pinned_lifecycle_version, expires_at)
		VALUES ($1,$2,'POST_STAY_PIN',$3,$4,$5,$6, now() + make_interval(secs => $7))
		RETURNING id::text`,
		g.Tenant, g.Site, g.Profile, g.Device, g.GuestNetwork, lifecycleVer, g.TTLSeconds).Scan(&id)
	return id, err
}

func (s *Store) IssuePostStay(ctx context.Context, g PostStayGrant) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := s.IssuePostStayTx(ctx, tx, g)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// Consumes the context EXACTLY once against the FULL server pin set (unconsumed, unexpired, presenter's tenant/site, SAME device + guest network) in its
// own transaction. For commerce use ConsumeTx so consumption rolls back atomically with a failed purchase.
func (s *Store) Consume(ctx context.Context, id string, p Presenter) (Consumed, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Consumed{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	c, err := s.ConsumeTx(ctx, tx, id, p)
	if err != nil {
		return Consumed{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Consumed{}, err
	}
	return c, nil
}

// Performs the one-time consumption inside the CALLER's transaction, obeying the global lock order L1 Stay, L2 Subject/Credential (the Auth Context row),
// L3 Entitlement, L4 Capacity, L5 Config, so it serializes against Checkout, Reinstatement and evidence replacement and never deadlocks. (1) Resolve the
// scoped Stay identity (unlocked read, no detail on failure). (2) Lock the pinned Stay FIRST while re-verifying the full pin set. (3) Only THEN flip
// unconsumed to consumed, guarded by consumed_at IS NULL AND expires_at > now(): exactly one winner. The caller continues in the SAME transaction
// holding the Stay lock. Every failure is the uniform ErrContextInvalid.
func (s *Store) ConsumeTx(ctx context.Context, tx pgx.Tx, id string, p Presenter) (Consumed, error) {
	if !validPinUUID(strings.TrimSpace(id)) || !p.valid() {
		return Consumed{}, ErrContextInvalid // typed/sanitized BEFORE any SQL — never a raw cast error
	}
	// The Auth Context family is capability-scoped: declare the operation first, or writes to auth_contexts are refused.
	if err := writerguard.Open(ctx, tx, writerguard.CapAuthContext); err != nil {
		return Consumed{}, err
	}

	// (1) Resolve the scoped Stay identity WITHOUT locking the context; presenter scope, one-time and expiry are enforced here and re-asserted in step 3.
	var method, stayID, ifaceID, profileID string
	err := tx.QueryRow(ctx, `SELECT ac.method, COALESCE(ac.stay_id::text,''),
		       COALESCE(ac.pms_interface_id::text,''), COALESCE(ac.post_stay_profile_id::text,'')
		FROM iam_v2.auth_contexts ac
		WHERE ac.id=$1 AND ac.tenant_id=$2 AND ac.site_id=$3
		  AND ac.device_id=$4 AND ac.guest_network_id=$5
		  AND ac.consumed_at IS NULL AND ac.expires_at > now()`,
		id, p.Tenant, p.Site, p.Device, p.GuestNetwork).Scan(&method, &stayID, &ifaceID, &profileID)
	// NOTE for anyone adding a method: step (2) re-verifies live subject state PER METHOD, and a method with no arm gets NO re-verification, so the
	// context stays usable for its whole TTL however its subject changes (fine for a voucher, catastrophic for anything that can be superseded).
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Consumed{}, ErrContextInvalid
		}
		return Consumed{}, err
	}

	// (2) L1: lock the pinned Stay FIRST and re-verify the full pin set against live state; this serializes against Checkout, Reinstatement and
	// evidence replacement.
	if method == "PMS" {
		var one int
		// GRANT-TIME LINEARIZATION: a PMS context lives minutes, so gating only issuance leaves issue-to-grant unguarded (a GO can be admitted, still
		// unapplied, while the guest redeems a context that was valid when minted). Same lock, scoped helper and order as IssuePMSTx, taken before the Stay
		// lock only because the Stay id comes from the context row; L1 Stay-first still holds for Stay-row conditions. The interface is the one PINNED IN
		// THE CONTEXT and step (2) re-verifies it is still the Stay's interface.
		if _, lerr := tx.Exec(ctx, `SELECT iam_v2.lock_pms_interface_runtime($1,$2,$3::uuid)`,
			p.Tenant, p.Site, ifaceID); lerr != nil {
			return Consumed{}, lerr
		}
		// L1: the Stay lock via the scoped helper before its conditions are tested; it blocks until a concurrent Checkout commits and the next statement
		// reads a fresh snapshot, so a departed guest is refused.
		if _, lerr := tx.Exec(ctx, `SELECT iam_v2.lock_stay($1,$2,$3::uuid)`,
			p.Tenant, p.Site, stayID); lerr != nil {
			return Consumed{}, lerr
		}
		err := tx.QueryRow(ctx, `SELECT 1
			FROM iam_v2.stays st
			JOIN iam_v2.auth_contexts ac
			  ON ac.id=$1 AND ac.stay_id=st.id AND ac.tenant_id=st.tenant_id AND ac.site_id=st.site_id
			     AND ac.pms_interface_id=st.pms_interface_id
			WHERE
			  -- SNAPSHOT: pinned Stay EPISODE + MONOTONIC occupancy-evidence version unchanged — a
			  -- Checkout→Reinstatement (lifecycle_version bump) or an authoritative evidence replacement
			  -- (evidence_version bump) invalidates the context even within TTL. This is the part that is
			  -- specific to CONSUMING a context, so it stays here.
			  st.lifecycle_version IS NOT DISTINCT FROM ac.pinned_lifecycle_version
			  AND st.occupancy_evidence_version IS NOT DISTINCT FROM ac.pinned_occupancy_evidence_version
			  -- Conditions on the Stay row stay INLINE, never inside a function. This query holds
			  -- FOR UPDATE OF st, and when a concurrent Checkout commits while we wait on that lock,
			  -- PostgreSQL re-evaluates these against the UPDATED tuple and correctly refuses the consume.
			  -- A STABLE function reading iam_v2.stays internally would answer from the pre-Checkout
			  -- statement snapshot and let a departed guest through.
			  AND st.status='IN_HOUSE'
			  AND st.occupancy_revision_id = ac.authentication_interface_revision_id
			  AND st.occupancy_evidence_at IS NOT NULL
			  AND st.occupancy_evidence_version > 0
			  AND st.occupancy_clock_suspect IS NOT TRUE
			  -- Feed health and the freshness ceiling are the shared rule, re-evaluated at consume against the
			  -- SAME Revision the context authenticated under and the locked tuple's own evidence timestamp.
			  -- A feed that has gone away between issue and consume stops the consumption here.
			  AND iam_v2.p3_feed_authorizes(st.tenant_id, st.site_id, st.pms_interface_id,
			                                ac.authentication_interface_revision_id, st.occupancy_evidence_at)
			`, id).Scan(&one)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Consumed{}, ErrContextInvalid
			}
			return Consumed{}, err
		}
	}

	// (2b) POST_STAY_PIN cannot reuse the PMS arm: a post-stay guest has checked OUT and was never proven from occupancy evidence, so IN_HOUSE, Revision,
	// evidence and freshness are all false for a valid context. It needs the EPISODE check here, not only at issue: with no arm the method gets NO
	// re-verification and a context minted before a reinstatement would stay usable for its whole TTL (the next-occupant leak). The Stay is locked FIRST
	// (L1 then L2); the predicate is the single p5_post_stay_authenticable shared with the database and tests, and the pinned episode must also match.
	if method == "POST_STAY_PIN" {
		var one int
		// L1 first here too, keyed by the profile the context pins.
		if _, lerr := tx.Exec(ctx, `SELECT iam_v2.lock_origin_stay($1,$2,$3::uuid)`,
			p.Tenant, p.Site, profileID); lerr != nil {
			return Consumed{}, lerr
		}
		err := tx.QueryRow(ctx, `SELECT 1
			FROM iam_v2.auth_contexts ac
			JOIN iam_v2.post_stay_profiles psp
			  ON psp.tenant_id=ac.tenant_id AND psp.site_id=ac.site_id AND psp.id=ac.post_stay_profile_id
			JOIN iam_v2.stays st
			  ON st.tenant_id=psp.tenant_id AND st.site_id=psp.site_id AND st.id=psp.origin_stay_id
			WHERE ac.id=$1
			  AND st.lifecycle_version IS NOT DISTINCT FROM ac.pinned_lifecycle_version
			  AND iam_v2.p5_post_stay_authenticable(ac.tenant_id, ac.site_id, ac.post_stay_profile_id)
			`, id).Scan(&one)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Consumed{}, ErrContextInvalid
			}
			return Consumed{}, err
		}
	}

	// (3) L2: atomically flip unconsumed to consumed; the consumed_at IS NULL guard yields exactly one concurrent winner even after the Stay lock is
	// granted, and presenter scope and expiry are re-asserted.
	var c Consumed
	err = tx.QueryRow(ctx, `UPDATE iam_v2.auth_contexts ac SET consumed_at = now()
		WHERE ac.id=$1 AND ac.tenant_id=$2 AND ac.site_id=$3
		  AND ac.device_id=$4 AND ac.guest_network_id=$5
		  AND ac.consumed_at IS NULL AND ac.expires_at > now()
		RETURNING method, COALESCE(stay_id::text,''), COALESCE(pms_interface_id::text,''),
		          COALESCE(authentication_interface_revision_id::text,''),
		          COALESCE(post_stay_profile_id::text,'')`,
		id, p.Tenant, p.Site, p.Device, p.GuestNetwork).
		Scan(&c.Method, &c.Stay, &c.Interface, &c.Revision, &c.PostStayProfile)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Consumed{}, ErrContextInvalid
		}
		return Consumed{}, err
	}
	return c, nil
}
