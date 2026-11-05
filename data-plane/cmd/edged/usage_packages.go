package main

// Packages of one access source, and the per-package split of a session carried across packages (a package change or a
// check-out moves a live session onto the next package). Each package's use is its own accounting meter, the one that
// enforces its allowance; a carried session's split comes from its binding history and its raw samples, and is shown
// only when those samples add up exactly to the session's total. Nothing is estimated and nothing is rewritten.

import (
	"context"
	"slices"
	"strconv"
	"time"
)

type usagePackageRow struct {
	EntitlementID string     `json:"entitlement_id"`
	Name          string     `json:"name"`
	Type          string     `json:"package_type,omitempty"`
	ServicePlan   string     `json:"service_plan,omitempty"`
	Status        string     `json:"status"`
	Activated     *time.Time `json:"activated_at,omitempty"`
	Terminated    *time.Time `json:"terminated_at,omitempty"`
	EndReason     string     `json:"end_reason,omitempty"`
	QuotaBytes    *int64     `json:"quota_bytes"`
	UsedBytes     int64      `json:"used_bytes"`
}

// sessionPackageSegment is one stretch of a session under one package. The byte fields are nil when the split is not exact.
type sessionPackageSegment struct {
	EntitlementID string     `json:"entitlement_id"`
	PackageName   string     `json:"package_name"`
	PackageType   string     `json:"package_type,omitempty"`
	From          time.Time  `json:"from"`
	Until         *time.Time `json:"until,omitempty"`
	BytesDown     *int64     `json:"bytes_down,omitempty"`
	BytesUp       *int64     `json:"bytes_up,omitempty"`
	BytesAll      *int64     `json:"bytes_total,omitempty"`
}

// packageHistoryMax bounds the package list; a source with more keeps its newest, so the latest package is always in it.
const packageHistoryMax = 100

// subjectPackagesSQL lists one access source's packages newest first, in the latest-package order of the lists, one past
// the bound so a cut list is known; subjectPackages returns them oldest first.
func subjectPackagesSQL(col string) string {
	return `
		SELECT e.id::text, ` + packageNameExpr + `, ` + packageTypeExpr + `, COALESCE(spr.name, ''),
		       e.status, e.activated_at, e.terminated_at, COALESCE(e.terminal_reason, ''),
		       COALESCE(e.data_quota_bytes, spr.data_quota_bytes), COALESCE(e.consumed_data_bytes, 0)
		  FROM iam_v2.entitlements e
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id` + packageJoins + `
		 WHERE e.` + col + ` = $1 AND e.tenant_id = $2 AND e.site_id = $3
		 ORDER BY e.activated_at DESC NULLS LAST, e.id DESC
		 LIMIT ` + strconv.Itoa(packageHistoryMax+1)
}

// sessionSegmentsSQL reads the binding history of the sessions given that ran under more than one package, with the raw
// samples inside each binding. A sample belongs to the binding covering its instant, half-open as the meter charges it;
// the closing-instant sample belongs to the last binding (arm 1 of accounting_usage_entitlement).
const sessionSegmentsSQL = `
		SELECT b.session_id::text, b.entitlement_id::text, ` + packageNameExpr + `, ` + packageTypeExpr + `,
		       b.bound_from, b.bound_until, COALESCE(seg.down, 0), COALESCE(seg.up, 0)
		  FROM iam_v2.session_entitlement_bindings b
		  JOIN iam_v2.entitlements e ON e.id = b.entitlement_id` + packageJoins + `
		  LEFT JOIN LATERAL (
		      SELECT sum(ar.bytes_down) AS down, sum(ar.bytes_up) AS up
		        FROM iam_v2.accounting_records ar
		       WHERE ar.session_id = b.session_id AND ar.tenant_id = b.tenant_id AND ar.site_id = b.site_id
		         AND ar.sampled_at >= b.bound_from
		         AND (b.bound_until IS NULL OR ar.sampled_at < b.bound_until
		              OR (ar.sampled_at = b.bound_until AND b.seq = (
		                  SELECT max(b2.seq) FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = b.session_id)))) seg ON true
		 WHERE b.session_id = ANY($1::uuid[]) AND b.tenant_id = $2 AND b.site_id = $3
		   AND (SELECT count(*) FROM iam_v2.session_entitlement_bindings b3 WHERE b3.session_id = b.session_id) > 1
		 ORDER BY b.session_id, b.seq`

// subjectPackages returns at most packageHistoryMax packages, oldest first, and whether older ones were left out.
func (s *server) subjectPackages(ctx context.Context, col, id string) ([]usagePackageRow, bool, error) {
	out := []usagePackageRow{}
	rows, err := s.db.Query(ctx, subjectPackagesSQL(col), id, s.tenantID, s.siteID)
	if err != nil {
		return out, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var p usagePackageRow
		if err := rows.Scan(&p.EntitlementID, &p.Name, &p.Type, &p.ServicePlan, &p.Status, &p.Activated,
			&p.Terminated, &p.EndReason, &p.QuotaBytes, &p.UsedBytes); err != nil {
			return out, false, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return out, false, err
	}
	out, more := oldestFirst(out)
	return out, more, nil
}

// oldestFirst takes a newest-first list read one past the bound and returns the newest packageHistoryMax, oldest first.
func oldestFirst(newest []usagePackageRow) ([]usagePackageRow, bool) {
	more := len(newest) > packageHistoryMax
	if more {
		newest = newest[:packageHistoryMax]
	}
	slices.Reverse(newest)
	return newest, more
}

type rawSegment struct {
	sessionID string
	seg       sessionPackageSegment
	down, up  int64
}

// attachSegments gives each carried session its packages, with bytes only when the per-binding sample sums equal the
// session's totals exactly; otherwise (raw samples already summarised, or a sample outside every binding) it says so.
func (s *server) attachSegments(ctx context.Context, sessions []staySessionRow) error {
	if len(sessions) == 0 {
		return nil
	}
	ids := make([]string, len(sessions))
	for i, x := range sessions {
		ids[i] = x.SessionID
	}
	rows, err := s.db.Query(ctx, sessionSegmentsSQL, ids, s.tenantID, s.siteID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var raw []rawSegment
	for rows.Next() {
		var r rawSegment
		if err := rows.Scan(&r.sessionID, &r.seg.EntitlementID, &r.seg.PackageName, &r.seg.PackageType,
			&r.seg.From, &r.seg.Until, &r.down, &r.up); err != nil {
			return err
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	applySegments(sessions, raw)
	return nil
}

// applySegments is attachSegments' decision, separate so it is tested without a database.
func applySegments(sessions []staySessionRow, raw []rawSegment) {
	by := map[string][]rawSegment{}
	for _, r := range raw {
		by[r.sessionID] = append(by[r.sessionID], r)
	}
	for i := range sessions {
		segs := by[sessions[i].SessionID]
		if len(segs) < 2 {
			continue
		}
		var down, up int64
		for _, r := range segs {
			down += r.down
			up += r.up
		}
		exact := down == sessions[i].BytesDown && up == sessions[i].BytesUp
		out := make([]sessionPackageSegment, len(segs))
		for j, r := range segs {
			out[j] = r.seg
			if exact {
				d, u, a := r.down, r.up, r.down+r.up
				out[j].BytesDown, out[j].BytesUp, out[j].BytesAll = &d, &u, &a
			}
		}
		sessions[i].Packages = out
		sessions[i].SplitExact = &exact
	}
}
