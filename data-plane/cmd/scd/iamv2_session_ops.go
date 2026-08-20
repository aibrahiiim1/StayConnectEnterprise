package main

// SESSION OPERATIONS OVER THE SINGLE SESSION AUTHORITY.
//
// These three operations -- count the active sessions, find the active session behind an IP, end it -- used
// to be served by internal/session over public.sessions. That table was the superseded session domain and is
// gone; iam_v2.sessions is the authority, so the operations move rather than disappear.
//
// They are deliberately thin. Ending a session because its ENTITLEMENT expired belongs to internal/enforce,
// which also closes the entitlement's device authorizations; what lives here is the operator- or
// network-initiated revocation of one device's access, which is a different act with a different reason.

import (
	"context"
	"errors"
	"net"

	"github.com/jackc/pgx/v5"
)

// activeSessionCount reports how many sessions are currently active for this site. Used by the operator
// dashboard and the setup surface, both of which only ever displayed a number.
func (s *server) activeSessionCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
        SELECT count(*) FROM iam_v2.sessions
         WHERE tenant_id=$1 AND site_id=$2 AND state='active'`, s.tenID, s.siteID).Scan(&n)
	return n, err
}

// findActiveSessionByIP resolves the active session behind a source IP. The IP is pinned on the session row
// itself, so this needs no join and cannot pick up a stale device-to-address mapping.
func (s *server) findActiveSessionByIP(ctx context.Context, ip net.IP) (string, bool, error) {
	var id string
	err := s.db.QueryRow(ctx, `
        SELECT id::text FROM iam_v2.sessions
         WHERE tenant_id=$1 AND site_id=$2 AND state='active' AND ip=$3::inet
         ORDER BY started DESC LIMIT 1`, s.tenID, s.siteID, ip.String()).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

// endActiveSessionByIP closes the active session behind an IP. `ended` is clamped to at least `started` so a
// revocation can never produce a session that ended before it began, which is the same rule internal/enforce
// applies when an entitlement expires.
//
// It is idempotent: revoking an IP with no active session is not an error, because the caller's intent --
// that this address is not on the network -- is already satisfied.
func (s *server) endActiveSessionByIP(ctx context.Context, ip net.IP, reason string) error {
	_, err := s.db.Exec(ctx, `
        UPDATE iam_v2.sessions
           SET state='ended', ended=GREATEST(now(), started), end_reason=$4
         WHERE tenant_id=$1 AND site_id=$2 AND ip=$3::inet AND state IN ('active','PENDING_ENFORCEMENT')`,
		s.tenID, s.siteID, ip.String(), reason)
	return err
}

// reserveLicensedSlot admits one more concurrent guest on THIS APPLIANCE, or refuses.
//
// SCOPE IS THE APPLIANCE, NOT THE SITE AND NOT THE TENANT. One licence is issued per appliance -- it is bound
// to that appliance's hardware serial and WAN MAC -- so two appliances under the same customer and the same
// site must admit guests concurrently, each enforcing only its own limit, with no cross-appliance
// serialization and no capacity leaking between them. The superseded session manager scoped and locked on the
// appliance id for exactly this reason. The scope moved with the authority; it did not change.
//
// COUNTING INSIDE THE TRANSACTION IS NOT ENOUGH. Under READ COMMITTED two concurrent activations both read
// the pre-insert count, both find the last slot free, and both insert -- which is precisely how a licensed
// limit gets exceeded by the number of simultaneous logins. The advisory lock makes check-then-insert atomic.
// It is taken on the APPLIANCE id, so contention is confined to one appliance's admissions, and it is a
// transaction-scoped lock, so it is released on commit AND on rollback without any unlock path to forget.
//
// iam_v2.sessions carries no appliance_id; the DEVICE does, and a session's device is where the appliance
// identity lives. The join is the accurate scope rather than a convenience.
//
// A non-positive limit means unlimited. Central Control Plane availability plays no part: the number comes
// from the signed licence on disk, so guest admission stays local-first and survives an outage.
//
// It must be called INSIDE the same transaction that inserts the session. Called anywhere else it proves
// nothing, because the lock would be gone before the row existed.
func reserveLicensedSlot(ctx context.Context, tx pgx.Tx, applianceID string, limit int64) error {
	if limit <= 0 {
		return nil // unlimited
	}
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 7))`, applianceID); err != nil {
		return err
	}
	var online int64
	if err := tx.QueryRow(ctx, `
	    SELECT count(*) FROM iam_v2.sessions se
	      JOIN iam_v2.devices d ON d.id = se.device_id
	     WHERE d.appliance_id = $1::uuid AND se.ended IS NULL
	       AND se.state IN ('active','PENDING_ENFORCEMENT')`, applianceID).Scan(&online); err != nil {
		return err
	}
	if online >= limit {
		return &licenseCapacityError{Limit: limit, Current: online}
	}
	return nil
}
