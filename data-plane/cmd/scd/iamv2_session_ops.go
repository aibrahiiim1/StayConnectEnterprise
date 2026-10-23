package main

// Session operations over iam_v2.sessions, the single session authority. Ending a session because its ENTITLEMENT expired belongs to
// internal/enforce (which also closes device authorizations); these are operator- or network-initiated revocations of one device's access.

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *server) activeSessionCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
        SELECT count(*) FROM iam_v2.sessions
         WHERE tenant_id=$1 AND site_id=$2 AND state='active'`, s.tenID, s.siteID).Scan(&n)
	return n, err
}

// findActiveSessionByIP: the IP is pinned on the session row, so no join and no stale device-to-address mapping.
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

// endActiveSessionByIP closes every live session behind an IP through endSessionControlled and returns how many it ended.
// Idempotent: an IP with no live session already satisfies the caller's intent.
func (s *server) endActiveSessionByIP(ctx context.Context, ip net.IP, reason string) (int, error) {
	rows, err := s.db.Query(ctx, `
        SELECT id::text FROM iam_v2.sessions
         WHERE tenant_id=$1 AND site_id=$2 AND ip=$3::inet AND ended IS NULL
           AND state IN ('active','PENDING_ENFORCEMENT')`, s.tenID, s.siteID, ip.String())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		ended, err := s.endSessionControlled(ctx, s.db, id, reason)
		if err != nil {
			return n, err
		}
		if ended {
			n++
		}
	}
	return n, nil
}

type sessionQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// endSessionControlled ends ONE session through iam_v2.end_session_enforcement and reports whether this call ended it.
// Not a raw UPDATE: the sessions guard (p3_controlled_writer_only) refuses non-owner writes out of PENDING_ENFORCEMENT, so a grant whose
// enforcement never converged could not be ended and kept its licensed slot and device place (audit B-4).
func (s *server) endSessionControlled(ctx context.Context, q sessionQuerier, sessionID, reason string) (bool, error) {
	var out string
	if err := q.QueryRow(ctx, `SELECT iam_v2.end_session_enforcement($1::uuid,$2::uuid,$3::uuid,$4)`,
		s.tenID, s.siteID, sessionID, sessionEndCode(reason)).Scan(&out); err != nil {
		return false, err
	}
	return out == "ENDED", nil
}

// sessionEndCode maps a reason onto the bounded code end_session_enforcement accepts (^[A-Z][A-Z0-9_]{0,63}$);
// anything that does not fit is recorded as ADMIN rather than failing the disconnect.
func sessionEndCode(reason string) string {
	c := strings.ToUpper(strings.TrimSpace(reason))
	if !sessionEndCodeRe.MatchString(c) {
		return "ADMIN"
	}
	return c
}

var sessionEndCodeRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// reserveLicensedSlot admits one more concurrent guest on THIS APPLIANCE or refuses. The scope is the appliance, not site or tenant:
// a licence is bound to the appliance's hardware, so appliances of one customer admit concurrently with no cross-appliance serialization.
// iam_v2.sessions has no appliance_id; the session's device carries it.
// Counting alone is not enough: under READ COMMITTED two concurrent activations both see the last slot free, so a transaction-scoped
// advisory lock on the appliance id makes check-then-insert atomic.
// Call it INSIDE the transaction that inserts the session. A non-positive limit means unlimited; the limit comes from the signed
// licence on disk, so admission stays local-first.
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
