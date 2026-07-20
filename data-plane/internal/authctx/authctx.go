// Package authctx is the Increment-6 one-time, TTL-bounded PMS Auth Context. A successful STRICT resolution
// issues an Auth Context — NOT a guest session directly. Every identity pin (tenant/site/interface/stay/
// revision) is SERVER-DERIVED from the resolution, never client input. The context is consumed EXACTLY once;
// a replay or an expired context is rejected uniformly (no detail). Consuming it is the gate a later session
// issuance passes through — this package issues no session and no financial command.
package authctx

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts
		(tenant_id, site_id, method, stay_id, pms_interface_id, authentication_interface_revision_id,
		 device_id, guest_network_id, expires_at)
		VALUES ($1,$2,'PMS',$3,$4,$5,$6,$7, now() + make_interval(secs => $8))
		RETURNING id::text`,
		g.Tenant, g.Site, g.Stay, g.Interface, g.Revision, g.Device, g.GuestNetwork, g.TTLSeconds).Scan(&id)
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
	err := tx.QueryRow(ctx, `UPDATE iam_v2.auth_contexts ac SET consumed_at = now()
		WHERE ac.id=$1 AND ac.tenant_id=$2 AND ac.site_id=$3
		  AND ac.device_id=$4 AND ac.guest_network_id=$5
		  AND ac.consumed_at IS NULL AND ac.expires_at > now()
		  AND (ac.method <> 'PMS' OR EXISTS (
		        SELECT 1 FROM iam_v2.stays st
		        WHERE st.id = ac.stay_id AND st.tenant_id = ac.tenant_id AND st.site_id = ac.site_id
		          AND st.pms_interface_id = ac.pms_interface_id AND st.status = 'IN_HOUSE'))
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
