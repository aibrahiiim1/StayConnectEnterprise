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
}

// ErrContextInvalid is returned uniformly for a missing / expired / already-consumed context (no detail
// distinguishes them to the caller — replay and expiry look identical).
var ErrContextInvalid = errors.New("authctx: context invalid, expired, or already consumed")

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

// Consume atomically consumes the context EXACTLY once — it must be un-consumed AND unexpired — and returns
// its server pins. The single-row UPDATE ... WHERE consumed_at IS NULL AND expires_at > now() guarantees at
// most one caller ever wins; a replay or an expired context affects zero rows → ErrContextInvalid.
func (s *Store) Consume(ctx context.Context, tenant, site, id string) (Consumed, error) {
	var c Consumed
	err := s.pool.QueryRow(ctx, `UPDATE iam_v2.auth_contexts SET consumed_at = now()
		WHERE id=$1 AND tenant_id=$2 AND site_id=$3 AND consumed_at IS NULL AND expires_at > now()
		RETURNING method, COALESCE(stay_id::text,''), COALESCE(pms_interface_id::text,'')`,
		id, tenant, site).Scan(&c.Method, &c.Stay, &c.Interface)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Consumed{}, ErrContextInvalid
		}
		return Consumed{}, err
	}
	return c, nil
}
