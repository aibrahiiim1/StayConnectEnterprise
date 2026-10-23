package signinattempt

// Thin enforcement calls (gate, note failure, note success). Every decision is made by the database in one
// atomic operation, the only place two concurrent fifth failures can be serialised. scd has EXECUTE on
// three functions and no table privilege, so it cannot read restrictions, change its policy or release
// anyone; a service that could would be able to exempt itself.

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Gate struct {
	Restricted       bool
	RemainingSeconds int
}

type ProtectionStore struct{ pool *pgxpool.Pool }

func NewProtectionStore(pool *pgxpool.Pool) *ProtectionStore { return &ProtectionStore{pool: pool} }

// Check asks whether this device is currently refused. A device with no hardware address is never
// restricted (nothing to attribute it to; such a submission fails as malformed anyway). An error is NOT
// "allowed": the caller refuses with a service failure that does not count as a wrong credential, since
// allowing on error would let one broken query switch the control off.
func (s *ProtectionStore) Check(ctx context.Context, tenantID, siteID string, mac net.HardwareAddr) (Gate, error) {
	var g Gate
	if s == nil || s.pool == nil || len(mac) == 0 {
		return g, nil
	}
	var remaining *int
	err := s.pool.QueryRow(ctx,
		`SELECT restricted, remaining_seconds FROM iam_v2.guest_signin_gate($1::uuid,$2::uuid,$3::macaddr)`,
		tenantID, siteID, mac.String()).Scan(&g.Restricted, &remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return Gate{}, nil
	}
	if err != nil {
		return Gate{}, err
	}
	if remaining != nil {
		g.RemainingSeconds = *remaining
	}
	return g, nil
}

// NoteFailure must be called after the attempt row is written: the count is derived from the attempts, not a
// second tally, so the number an operator sees is the number the policy acted on.
func (s *ProtectionStore) NoteFailure(ctx context.Context, tenantID, siteID string, mac net.HardwareAddr,
	networkID, networkName, room string) (restricted bool, expiresAt *time.Time, count int, err error) {
	if s == nil || s.pool == nil || len(mac) == 0 {
		return false, nil, 0, nil
	}
	err = s.pool.QueryRow(ctx, `
		SELECT restricted, expires_at, failure_count
		  FROM iam_v2.guest_signin_note_failure($1::uuid,$2::uuid,$3::macaddr,
		                                        NULLIF($4,'')::uuid, $5, $6)`,
		tenantID, siteID, mac.String(), networkID, networkName, room).Scan(&restricted, &expiresAt, &count)
	return restricted, expiresAt, count, err
}

// NoteSuccess clears the device's counter.
func (s *ProtectionStore) NoteSuccess(ctx context.Context, tenantID, siteID string, mac net.HardwareAddr) error {
	if s == nil || s.pool == nil || len(mac) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`SELECT iam_v2.guest_signin_note_success($1::uuid,$2::uuid,$3::macaddr)`,
		tenantID, siteID, mac.String())
	return err
}

// EffectivePolicy is for the operator API and tests; the enforcement functions read the same table
// themselves, keeping one configuration source.
func (s *ProtectionStore) EffectivePolicy(ctx context.Context, tenantID, siteID string) (Policy, bool, error) {
	var p Policy
	var isDefault bool
	err := s.pool.QueryRow(ctx, `
		SELECT max_failed_attempts, observation_window_seconds, restriction_seconds, is_default
		  FROM iam_v2.guest_signin_protection_get($1::uuid,$2::uuid)`,
		tenantID, siteID).Scan(&p.MaxFailedAttempts, &p.WindowSeconds, &p.RestrictionSeconds, &isDefault)
	return p, isDefault, err
}
