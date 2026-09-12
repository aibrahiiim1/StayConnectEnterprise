package signinattempt

// THE ENFORCEMENT SIDE OF GUEST SIGN-IN PROTECTION.
//
// Three calls, all of them thin: ask the gate before evaluating anything, report a wrong credential after the
// outcome is known, report a success. Every decision — the thresholds, the rolling count, whether to create a
// restriction, whether one is already running — is made by the database in one atomic operation, because that
// is the only place two concurrent fifth failures can be serialised against each other.
//
// SCD HOLDS NO TABLE PRIVILEGE ON ANY OF IT. It has EXECUTE on three functions and nothing else: it cannot
// read the restriction list, cannot change the policy it is subject to, and cannot release anybody. A service
// that could do those things could quietly exempt itself from the control it enforces.

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Gate is the answer to "may this device submit right now".
type Gate struct {
	Restricted       bool
	RemainingSeconds int
}

// ProtectionStore performs the three enforcement operations.
type ProtectionStore struct{ pool *pgxpool.Pool }

func NewProtectionStore(pool *pgxpool.Pool) *ProtectionStore { return &ProtectionStore{pool: pool} }

// Check asks whether this device is currently refused.
//
// A DEVICE WITH NO HARDWARE ADDRESS IS NEVER RESTRICTED, because there is nothing to attribute a restriction
// to. That is not a hole: a submission that arrives before the appliance can identify the device fails as a
// malformed submission anyway, and malformed submissions do not count towards the threshold either.
//
// AN ERROR HERE IS NOT "ALLOWED". The caller treats it as a service failure and refuses the submission with
// the technical message — which is what every other database failure on this path already does, and which
// notably does NOT count as a wrong credential. Returning "allowed" on error would make one broken query the
// way to switch the whole control off.
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

// NoteFailure reports ONE wrong credential and returns whether that took the device over the threshold.
//
// It is called AFTER the attempt row is written, so the rolling count the database performs includes the
// attempt being reported. The ordering is not incidental: the count is derived from the attempts themselves
// rather than kept as a second tally, precisely so the number an operator reads on screen and the number the
// policy acted on cannot be different numbers.
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

// NoteSuccess clears that device's counter. A guest who proves who they are has demonstrated the thing the
// counter exists to doubt.
func (s *ProtectionStore) NoteSuccess(ctx context.Context, tenantID, siteID string, mac net.HardwareAddr) error {
	if s == nil || s.pool == nil || len(mac) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`SELECT iam_v2.guest_signin_note_success($1::uuid,$2::uuid,$3::macaddr)`,
		tenantID, siteID, mac.String())
	return err
}

// EffectivePolicy reads the site's current numbers. Used by the operator API and by tests; the enforcement
// path never needs it, because the enforcement functions read the same table themselves — which is what makes
// "one authoritative configuration source" true rather than merely intended.
func (s *ProtectionStore) EffectivePolicy(ctx context.Context, tenantID, siteID string) (Policy, bool, error) {
	var p Policy
	var isDefault bool
	err := s.pool.QueryRow(ctx, `
		SELECT max_failed_attempts, observation_window_seconds, restriction_seconds, is_default
		  FROM iam_v2.guest_signin_protection_get($1::uuid,$2::uuid)`,
		tenantID, siteID).Scan(&p.MaxFailedAttempts, &p.WindowSeconds, &p.RestrictionSeconds, &isDefault)
	return p, isDefault, err
}
