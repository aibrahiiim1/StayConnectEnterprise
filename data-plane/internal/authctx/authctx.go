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
	return g.OccupancyEvidenceVersion > 0 && g.TTLSeconds > 0 && g.TTLSeconds <= maxTTLSeconds
}

// Store is the DB-backed Auth Context issuer/consumer.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// PMSGrant is the SERVER-DERIVED pin set for a PMS Auth Context. Interface/Revision/Stay come from the
// verified resolution; Device/GuestNetwork from the trusted request context. TTLSeconds bounds its lifetime.
type PMSGrant struct {
	Tenant, Site             string
	Interface, Revision      string
	Stay                     string
	Device                   string
	GuestNetwork             string
	OccupancyEvidenceVersion int // the exact occupancy-evidence version the successful resolution used
	TTLSeconds               int
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
	if !g.valid() {
		return "", ErrGrantIncomplete // fail BEFORE any INSERT — never persist an unusable context
	}
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts
		(tenant_id, site_id, method, stay_id, pms_interface_id, authentication_interface_revision_id,
		 device_id, guest_network_id, pinned_occupancy_evidence_version, expires_at)
		VALUES ($1,$2,'PMS',$3,$4,$5,$6,$7,$8, now() + make_interval(secs => $9))
		RETURNING id::text`,
		g.Tenant, g.Site, g.Stay, g.Interface, g.Revision, g.Device, g.GuestNetwork, g.OccupancyEvidenceVersion, g.TTLSeconds).Scan(&id)
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
		          -- PROVENANCE: the Stay's occupancy evidence must have been produced by the SAME immutable
		          -- Revision the context authenticated against (single-Revision model). A matching evidence
		          -- version integer under a DIFFERENT Revision is NOT accepted.
		          AND st.occupancy_revision_id = ac.authentication_interface_revision_id
		          AND st.occupancy_evidence_at IS NOT NULL
		          AND st.occupancy_clock_suspect IS NOT TRUE
		          AND st.occupancy_normalization_version IS NOT DISTINCT FROM ac.pinned_occupancy_evidence_version
		          -- freshness: bounded, fail-closed parse of the pinned Revision's max_auth_cache_age. Only a
		          -- positive integer (regex-guarded, capped at 7 days) is honored; anything malformed / absent
		          -- / zero / negative falls back to a strict 300s default (never an SQL cast error or an
		          -- unbounded window).
		          AND st.occupancy_evidence_at > now() - make_interval(secs =>
		                CASE WHEN (pr.config->>'max_auth_cache_age_seconds') ~ '^[1-9][0-9]*$'
		                     THEN LEAST((pr.config->>'max_auth_cache_age_seconds')::int, 604800)
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
