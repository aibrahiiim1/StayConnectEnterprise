// Package authctx is the Increment-6 one-time, TTL-bounded PMS Auth Context. A successful STRICT resolution
// issues an Auth Context — NOT a guest session directly. Every identity pin (tenant/site/interface/stay/
// revision) is SERVER-DERIVED from the resolution, never client input. The context is consumed EXACTLY once;
// a replay or an expired context is rejected uniformly (no detail). Consuming it is the gate a later session
// issuance passes through — this package issues no session and no financial command.
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

// validPinUUID reports whether s is a canonical 8-4-4-4-12 lowercase/uppercase-hex UUID and NOT the nil UUID
// (a real identity pin is never nil). Validating in Go means a malformed id returns a typed sanitized error
// BEFORE any SQL, never a raw PostgreSQL cast error.
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

// valid reports whether every required server-derived pin is a proper UUID and the TTL is positive/bounded. A
// PMS Auth Context is NEVER issued without the full valid pin set.
func (g PMSGrant) valid() bool {
	for _, v := range []string{g.Tenant, g.Site, g.Interface, g.Revision, g.Stay, g.Device, g.GuestNetwork} {
		if !validPinUUID(strings.TrimSpace(v)) {
			return false
		}
	}
	return g.TTLSeconds > 0 && g.TTLSeconds <= maxTTLSeconds
}

// valid reports whether the presenter identity is fully-formed (proper UUIDs). A malformed presenter is a
// uniform ErrContextInvalid, never a raw cast error.
func (p Presenter) valid() bool {
	return validPinUUID(strings.TrimSpace(p.Tenant)) && validPinUUID(strings.TrimSpace(p.Site)) &&
		validPinUUID(strings.TrimSpace(p.Device)) && validPinUUID(strings.TrimSpace(p.GuestNetwork))
}

// Store is the DB-backed Auth Context issuer/consumer.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// PMSGrant is the SERVER-DERIVED pin set for a PMS Auth Context. Interface/Revision/Stay come from the
// verified resolution; Device/GuestNetwork from the trusted request context. TTLSeconds bounds its lifetime.
type PMSGrant struct {
	Tenant, Site        string
	Interface, Revision string
	Stay                string
	Device              string
	GuestNetwork        string
	TTLSeconds          int
	// lifecycle_version + occupancy_evidence_version are NOT supplied by the caller — they are read
	// authoritatively from the resolved Stay row inside IssuePMSTx (the caller cannot invent them).
}

// Consumed is what a successful one-time consumption yields — the server-pinned subject the caller may then
// act on (e.g. issue a scoped session). It carries NO guest credential.
type Consumed struct {
	Method    string
	Stay      string
	Interface string
	Revision  string
	// PostStayProfile is the server-pinned subject of a POST_STAY_PIN context. It is empty for every other
	// method, and a caller acting on a post-stay context MUST read it rather than Stay: the origin Stay is
	// lineage, not the subject, and treating it as the subject is how post-stay access would end up attached
	// to a room again.
	PostStayProfile string
}

// Presenter is the request-side identity a consumption is checked against. A context pinned to one
// device/guest-network/scope is UNUSABLE from another — a mismatch is a uniform ErrContextInvalid.
type Presenter struct {
	Tenant       string
	Site         string
	Device       string
	GuestNetwork string
}

// ErrContextInvalid is returned uniformly for a missing / expired / already-consumed context, OR one presented
// from a different device / guest-network / scope (no detail distinguishes these to the caller).
var ErrContextInvalid = errors.New("authctx: context invalid, expired, consumed, or wrong presenter")

// IssuePMS creates a one-time, TTL-bounded PMS Auth Context and returns its opaque id (the single-use token).
// expires_at is computed SERVER-SIDE (now() + TTL) so no client clock influences the lifetime. A
// non-positive TTL yields an already-expired context (fail-closed).
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

// IssuePMSTx issues the PMS Auth Context INSIDE the caller's transaction (ideally the successful
// STRICT-resolution transaction). It locks the resolved Stay, verifies scope + IN_HOUSE + valid occupancy
// evidence produced by the pinned Revision, and reads the AUTHORITATIVE lifecycle_version + monotonic
// occupancy_evidence_version from that row — the caller supplies only the verified resolution identity, never
// the current counters. Fails closed (ErrGrantIncomplete) on any missing/invalid pin, without inserting.
func (s *Store) IssuePMSTx(ctx context.Context, tx pgx.Tx, g PMSGrant) (string, error) {
	if !g.valid() {
		return "", ErrGrantIncomplete
	}
	// The Auth Context family is capability-scoped: this transaction declares the operation it is about to
	// perform, and writes to auth_contexts are refused without that declaration.
	if err := writerguard.Open(ctx, tx, writerguard.CapAuthContext); err != nil {
		return "", err
	}
	var lifecycleVer int
	var evVer int64
	// authoritative snapshot from the resolved Stay: locked (L1 Stay-first), IN_HOUSE, occupancy evidence
	// present with a real MONOTONIC version (> 0), produced by the SAME Revision the resolution authenticated
	// against, not clock-suspect, AND still FRESH under that Revision's pinned max_auth_cache_age. Freshness is
	// revalidated HERE so a context that is already stale at the moment of issue is NEVER persisted — the
	// evidence-version guard alone cannot detect wall-clock staleness of unchanged evidence. Same cast-safe,
	// fail-closed 300s parse as consume (regex-bounded 1..6 digits, nested CASE, <= 604800).
	err := tx.QueryRow(ctx, `SELECT st.lifecycle_version, st.occupancy_evidence_version
		FROM iam_v2.stays st
		JOIN iam_v2.pms_interfaces pi
		  ON pi.tenant_id=st.tenant_id AND pi.site_id=st.site_id AND pi.id=st.pms_interface_id
		JOIN iam_v2.pms_interface_revisions pr
		  ON pr.tenant_id=st.tenant_id AND pr.site_id=st.site_id AND pr.pms_interface_id=st.pms_interface_id
		     AND pr.id=$5
		WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.pms_interface_id=$3 AND st.id=$4
		  AND st.status='IN_HOUSE' AND pi.lifecycle_state='ACTIVE'
		  AND st.occupancy_evidence_at IS NOT NULL AND st.occupancy_clock_suspect IS NOT TRUE
		  AND st.occupancy_evidence_version > 0
		  AND st.occupancy_revision_id=$5
		  AND st.occupancy_evidence_at > now() - make_interval(secs =>
		        CASE WHEN (pr.config->>'max_auth_cache_age_seconds') ~ '^[1-9][0-9]{0,5}$'
		             THEN CASE WHEN (pr.config->>'max_auth_cache_age_seconds')::int <= 604800
		                       THEN (pr.config->>'max_auth_cache_age_seconds')::int ELSE 300 END
		             ELSE 300 END)
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

// PostStayGrant is the SERVER-DERIVED pin set for a POST_STAY_PIN Auth Context. It is deliberately NOT a
// PMSGrant with fields left blank: a PMS context pins an interface, a Revision and the occupancy evidence
// that proved the guest was in the room, and NONE of that is what a post-stay PIN proves. Sharing the struct
// would have meant either lying about those pins or making them optional for everyone.
//
// The subject is the PROFILE. The origin Stay is reached through it and is never supplied by the caller —
// that is what keeps post-stay identity off the room.
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

// ErrPostStayNotAuthenticable is a typed, SANITIZED refusal for a post-stay context that must not exist: the
// profile is revoked, past its validity window, or bound to an episode the Stay has already left. It carries
// no PIN, no guest identifier and no hint about WHICH of those it was — the caller returns the same uniform
// non-success either way, and an error that distinguished them would be an oracle.
var ErrPostStayNotAuthenticable = errors.New("authctx: post-stay profile is not authenticable")

// IssuePostStayTx issues a one-time, TTL-bounded POST_STAY_PIN Auth Context inside the caller's transaction —
// ideally the same one that verified the PIN. It verifies NOTHING about the PIN itself (that is the caller's
// job and happens before this); what it does is bind the context to the profile's CURRENT episode so that a
// reinstatement between issue and consume invalidates it.
//
// It locks the origin Stay (L1 first, as everywhere else) and reads the episode AUTHORITATIVELY from that
// locked row: the caller supplies the verified profile identity and never the counter. A profile that is not
// authenticable at this instant yields ErrPostStayNotAuthenticable and NO row — a context that is already
// dead the moment it is minted is never persisted.
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

// IssuePostStay is IssuePostStayTx in its own transaction, for a caller that has nothing else to commit.
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

// Consume atomically consumes the context EXACTLY once against the FULL server pin set — it must be
// un-consumed, unexpired, in the presenter's tenant/site, AND presented from the SAME device + guest network.
// Runs in its own transaction (for a standalone session issuance). For commerce, use ConsumeTx so the
// consumption commits/rolls back ATOMICALLY with the Quote/Purchase — a failed purchase must not leave a
// context permanently consumed.
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

// ConsumeTx performs the one-time consumption inside the CALLER's transaction, so the caller can bind it to a
// Quote/Purchase and roll the consumption back if that fails. It obeys the approved GLOBAL lock order
// L1 Stay → L2 Subject/Credential (the Auth Context row) → L3 Entitlement → L4 Capacity → L5 Config, so it
// serializes deterministically against Checkout / Reinstatement / occupancy-evidence replacement (which all
// take the Stay lock first) and never deadlocks against them:
//
//	(1) resolve the context's scoped Stay identity (unlocked read, filtered by presenter + unconsumed +
//	    unexpired) — no context detail is returned to the caller on failure;
//	(2) lock the pinned Stay row FIRST (SELECT ... FOR UPDATE OF st) while re-verifying the FULL pin set:
//	    tenant/site/interface, IN_HOUSE, interface ACTIVE, Revision provenance, episode + monotonic evidence
//	    snapshot, evidence present + not clock-suspect, and freshness under the pinned Revision;
//	(3) only THEN atomically flip the context unconsumed→consumed (single-row UPDATE guarded by
//	    consumed_at IS NULL AND expires_at > now()) — exactly one concurrent winner.
//
// The caller continues Quote/Purchase/Entitlement in the SAME transaction while still holding the Stay lock,
// so a Checkout cannot interleave; a failed purchase rolls the consumption back with it. Every failure —
// invalid/malformed id, stale, consumed, expired, wrong presenter — is the uniform ErrContextInvalid.
func (s *Store) ConsumeTx(ctx context.Context, tx pgx.Tx, id string, p Presenter) (Consumed, error) {
	if !validPinUUID(strings.TrimSpace(id)) || !p.valid() {
		return Consumed{}, ErrContextInvalid // typed/sanitized BEFORE any SQL — never a raw cast error
	}
	// The Auth Context family is capability-scoped: this transaction declares the operation it is about to
	// perform, and writes to auth_contexts are refused without that declaration.
	if err := writerguard.Open(ctx, tx, writerguard.CapAuthContext); err != nil {
		return Consumed{}, err
	}

	// (1) Resolve the context's scoped Stay identity WITHOUT locking the context. Presenter scope + one-time +
	//     unexpired are enforced here (and re-asserted atomically in step 3). We learn only which Stay to lock.
	var method, stayID string
	err := tx.QueryRow(ctx, `SELECT ac.method, COALESCE(ac.stay_id::text,'')
		FROM iam_v2.auth_contexts ac
		WHERE ac.id=$1 AND ac.tenant_id=$2 AND ac.site_id=$3
		  AND ac.device_id=$4 AND ac.guest_network_id=$5
		  AND ac.consumed_at IS NULL AND ac.expires_at > now()`,
		id, p.Tenant, p.Site, p.Device, p.GuestNetwork).Scan(&method, &stayID)
	// NOTE for anyone adding a method here: step (2) below re-verifies live subject state, and it is written
	// PER METHOD. A method with no arm gets NO re-verification at all — the context stays usable for its whole
	// TTL no matter what happens to its subject in the meantime. That is correct for a voucher (the subject is
	// the voucher) and catastrophic for anything whose subject can be superseded underneath it.
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Consumed{}, ErrContextInvalid
		}
		return Consumed{}, err
	}

	// (2) L1: lock the pinned Stay FIRST and re-verify the full server pin set against live authoritative state.
	//     Holding this lock serializes the whole consumption+commerce transaction against Checkout /
	//     Reinstatement / evidence replacement, which acquire the same Stay lock before mutating it.
	if method == "PMS" {
		var one int
		err := tx.QueryRow(ctx, `SELECT 1
			FROM iam_v2.stays st
			JOIN iam_v2.auth_contexts ac
			  ON ac.id=$1 AND ac.stay_id=st.id AND ac.tenant_id=st.tenant_id AND ac.site_id=st.site_id
			     AND ac.pms_interface_id=st.pms_interface_id
			JOIN iam_v2.pms_interfaces pi
			  ON pi.tenant_id=st.tenant_id AND pi.site_id=st.site_id AND pi.id=st.pms_interface_id
			JOIN iam_v2.pms_interface_revisions pr
			  ON pr.tenant_id=st.tenant_id AND pr.site_id=st.site_id AND pr.pms_interface_id=st.pms_interface_id
			     AND pr.id=ac.authentication_interface_revision_id
			WHERE st.status='IN_HOUSE'
			  AND pi.lifecycle_state='ACTIVE'
			  -- SNAPSHOT: pinned Stay EPISODE + MONOTONIC occupancy-evidence version unchanged — a
			  -- Checkout→Reinstatement (lifecycle_version bump) or an authoritative evidence replacement
			  -- (evidence_version bump) invalidates the context even within TTL.
			  AND st.lifecycle_version IS NOT DISTINCT FROM ac.pinned_lifecycle_version
			  AND st.occupancy_evidence_version IS NOT DISTINCT FROM ac.pinned_occupancy_evidence_version
			  -- PROVENANCE: evidence produced by the SAME immutable Revision the context authenticated against.
			  AND st.occupancy_revision_id = ac.authentication_interface_revision_id
			  AND st.occupancy_evidence_at IS NOT NULL
			  AND st.occupancy_evidence_version > 0
			  AND st.occupancy_clock_suspect IS NOT TRUE
			  -- freshness: CAST-SAFE, fail-closed (regex-bounded 1..6 digits → nested CASE cast → <= 604800,
			  -- else strict 300s). An invalid value is NEVER widened to a large window.
			  AND st.occupancy_evidence_at > now() - make_interval(secs =>
			        CASE WHEN (pr.config->>'max_auth_cache_age_seconds') ~ '^[1-9][0-9]{0,5}$'
			             THEN CASE WHEN (pr.config->>'max_auth_cache_age_seconds')::int <= 604800
			                       THEN (pr.config->>'max_auth_cache_age_seconds')::int ELSE 300 END
			             ELSE 300 END)
			FOR UPDATE OF st`, id).Scan(&one)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Consumed{}, ErrContextInvalid
			}
			return Consumed{}, err
		}
	}

	// (2b) POST_STAY_PIN. This method CANNOT reuse the PMS arm above, and the difference is not cosmetic:
	//
	//   * the PMS arm demands IN_HOUSE, an interface Revision, occupancy evidence and freshness. A post-stay
	//     guest has checked OUT, and their identity was never proven from occupancy evidence — every one of
	//     those conditions is false for a perfectly valid post-stay context;
	//   * what post-stay needs instead is the EPISODE check, and it needs it here rather than only at issue.
	//     Without this arm the method would fall through with NO re-verification at all: a context minted
	//     seconds before a reinstatement would stay usable for its whole TTL, which is precisely the
	//     next-occupant leak the whole design exists to prevent.
	//
	// The Stay is locked FIRST, keeping the approved L1→L2 order, so this serializes against Checkout and
	// Reinstatement exactly as the PMS arm does. The predicate itself is the single one the database, the
	// tests and this code all share (p5_post_stay_authenticable), and the pinned episode must ALSO still match
	// the context — a profile that is authenticable again for a LATER episode is not this context's subject.
	if method == "POST_STAY_PIN" {
		var one int
		err := tx.QueryRow(ctx, `SELECT 1
			FROM iam_v2.auth_contexts ac
			JOIN iam_v2.post_stay_profiles psp
			  ON psp.tenant_id=ac.tenant_id AND psp.site_id=ac.site_id AND psp.id=ac.post_stay_profile_id
			JOIN iam_v2.stays st
			  ON st.tenant_id=psp.tenant_id AND st.site_id=psp.site_id AND st.id=psp.origin_stay_id
			WHERE ac.id=$1
			  AND st.lifecycle_version IS NOT DISTINCT FROM ac.pinned_lifecycle_version
			  AND iam_v2.p5_post_stay_authenticable(ac.tenant_id, ac.site_id, ac.post_stay_profile_id)
			FOR UPDATE OF st`, id).Scan(&one)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Consumed{}, ErrContextInvalid
			}
			return Consumed{}, err
		}
	}

	// (3) L2: atomically flip the Auth Context unconsumed→consumed. The consumed_at IS NULL guard yields exactly
	//     one concurrent winner even after the Stay lock is granted; presenter scope + expiry re-asserted.
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
