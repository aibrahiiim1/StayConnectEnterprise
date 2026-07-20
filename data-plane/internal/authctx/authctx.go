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
)

// maxTTLSeconds bounds a PMS Auth Context lifetime (a one-time context is short-lived by design).
const maxTTLSeconds = 3600

// ErrGrantIncomplete is a typed, SANITIZED error for a PMS grant missing/invalid a required server-derived
// pin. It carries no guest credential or raw identifier.
var ErrGrantIncomplete = errors.New("authctx: incomplete or invalid PMS grant")

// valid reports whether every required server-derived pin is present and in range. A PMS Auth Context is
// NEVER issued without the full pin set (an unusable context — e.g. a NULL evidence version or an
// already-expired TTL — must not reach the table).
func (g PMSGrant) valid() bool {
	for _, v := range []string{g.Tenant, g.Site, g.Interface, g.Revision, g.Stay, g.Device, g.GuestNetwork} {
		if strings.TrimSpace(v) == "" {
			return false
		}
	}
	return g.TTLSeconds > 0 && g.TTLSeconds <= maxTTLSeconds
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
	var lifecycleVer int
	var evVer int64
	// authoritative snapshot from the resolved Stay: locked, IN_HOUSE, occupancy evidence present + produced
	// by the SAME Revision the resolution authenticated against, not clock-suspect.
	err := tx.QueryRow(ctx, `SELECT st.lifecycle_version, st.occupancy_evidence_version
		FROM iam_v2.stays st
		JOIN iam_v2.pms_interfaces pi
		  ON pi.tenant_id=st.tenant_id AND pi.site_id=st.site_id AND pi.id=st.pms_interface_id
		WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.pms_interface_id=$3 AND st.id=$4
		  AND st.status='IN_HOUSE' AND pi.lifecycle_state='ACTIVE'
		  AND st.occupancy_evidence_at IS NOT NULL AND st.occupancy_clock_suspect IS NOT TRUE
		  AND st.occupancy_revision_id=$5
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
// Quote/Purchase and roll the consumption back if that fails. The single-row UPDATE guarded by
// consumed_at IS NULL AND expires_at > now() AND the full pin set guarantees at most one winner and rejects a
// replay / expiry / wrong device / wrong network / cross-scope uniformly as ErrContextInvalid. It also
// re-verifies the pinned Stay is still IN_HOUSE (occupancy evidence still valid for the operation).
func (s *Store) ConsumeTx(ctx context.Context, tx pgx.Tx, id string, p Presenter) (Consumed, error) {
	var c Consumed
	// For a PMS context the EXISTS clause re-verifies the FULL server pin set at consume time: the pinned
	// Interface is still ACTIVE (not disabled/auth-ineligible); the pinned Revision still exists (immutable —
	// only an explicit lifecycle invalidation removes it); the pinned Stay is still IN_HOUSE; the Stay's
	// occupancy evidence still matches the PINNED evidence version, is present and NOT clock-suspect, and is
	// fresh under the pinned Revision's max_auth_cache_age (fail-closed default 300s). Any mismatch → zero rows
	// → ErrContextInvalid (uniform).
	err := tx.QueryRow(ctx, `UPDATE iam_v2.auth_contexts ac SET consumed_at = now()
		WHERE ac.id=$1 AND ac.tenant_id=$2 AND ac.site_id=$3
		  AND ac.device_id=$4 AND ac.guest_network_id=$5
		  AND ac.consumed_at IS NULL AND ac.expires_at > now()
		  AND (ac.method <> 'PMS' OR EXISTS (
		        SELECT 1
		        FROM iam_v2.stays st
		        JOIN iam_v2.pms_interfaces pi
		          ON pi.tenant_id=ac.tenant_id AND pi.site_id=ac.site_id AND pi.id=ac.pms_interface_id
		        JOIN iam_v2.pms_interface_revisions pr
		          ON pr.tenant_id=ac.tenant_id AND pr.site_id=ac.site_id AND pr.pms_interface_id=ac.pms_interface_id
		             AND pr.id=ac.authentication_interface_revision_id
		        WHERE st.id=ac.stay_id AND st.tenant_id=ac.tenant_id AND st.site_id=ac.site_id
		          AND st.pms_interface_id=ac.pms_interface_id
		          AND st.status='IN_HOUSE'
		          AND pi.lifecycle_state='ACTIVE'
		          -- SNAPSHOT: the pinned Stay EPISODE and MONOTONIC occupancy-evidence version must be
		          -- unchanged — a Checkout→Reinstatement (lifecycle_version bump) or an authoritative evidence
		          -- replacement (evidence_version bump) invalidates the context even within TTL.
		          AND st.lifecycle_version IS NOT DISTINCT FROM ac.pinned_lifecycle_version
		          AND st.occupancy_evidence_version IS NOT DISTINCT FROM ac.pinned_occupancy_evidence_version
		          -- PROVENANCE: occupancy evidence produced by the SAME immutable Revision the context
		          -- authenticated against (single-Revision model). A matching version under a DIFFERENT
		          -- Revision is NOT accepted.
		          AND st.occupancy_revision_id = ac.authentication_interface_revision_id
		          AND st.occupancy_evidence_at IS NOT NULL
		          AND st.occupancy_clock_suspect IS NOT TRUE
		          -- freshness: CAST-SAFE, fail-closed. The regex bounds the value to 1..6 digits (max 999999,
		          -- so the ::int cast can never overflow); a NESTED CASE only casts after that guard; anything
		          -- malformed / absent / zero / negative / > 604800 (incl. overflow-sized) → strict 300s
		          -- default. An invalid value is NEVER widened to a large window.
		          AND st.occupancy_evidence_at > now() - make_interval(secs =>
		                CASE WHEN (pr.config->>'max_auth_cache_age_seconds') ~ '^[1-9][0-9]{0,5}$'
		                     THEN CASE WHEN (pr.config->>'max_auth_cache_age_seconds')::int <= 604800
		                               THEN (pr.config->>'max_auth_cache_age_seconds')::int ELSE 300 END
		                     ELSE 300 END)))
		RETURNING method, COALESCE(stay_id::text,''), COALESCE(pms_interface_id::text,''),
		          COALESCE(authentication_interface_revision_id::text,'')`,
		id, p.Tenant, p.Site, p.Device, p.GuestNetwork).Scan(&c.Method, &c.Stay, &c.Interface, &c.Revision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Consumed{}, ErrContextInvalid
		}
		return Consumed{}, err
	}
	return c, nil
}
