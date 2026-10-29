package pmsd

// Superseded RESYNC copies (audit AD-38, D6-L1). Every reconnect publishes a roster generation, and each stores every in-house
// guest's record again. iam_v2.pms_resync_supersede_payloads owns every rule (only generations older than the published one,
// only once the newest is fully applied, only a middle copy of a record that also exists earlier and later, payload only,
// reversible). pmsd supplies the cadence so reconnects stop accumulating copies; a failed pass is retried on the next.

import (
	"context"
	"sync"
	"time"
)

// SupersedeSweeper is optional (not a Repo method) so test doubles need not implement it.
type SupersedeSweeper interface {
	SupersedeResyncPayloads(ctx context.Context, tenantID, siteID string) (int, error)
}

// Not a setting: it paces a background tidy-up whose result nobody can observe from the outside.
const supersedeEvery = time.Hour

var supersedeLast struct {
	sync.Mutex
	at map[string]time.Time
}

func supersedeResyncCopies(ctx context.Context, a Assignment, repo Repo, deps *Deps) {
	sw, ok := repo.(SupersedeSweeper)
	if !ok {
		return
	}
	key := a.TenantID + "/" + a.SiteID
	supersedeLast.Lock()
	if supersedeLast.at == nil {
		supersedeLast.at = map[string]time.Time{}
	}
	if last, seen := supersedeLast.at[key]; seen && time.Since(last) < supersedeEvery {
		supersedeLast.Unlock()
		return
	}
	supersedeLast.at[key] = time.Now()
	supersedeLast.Unlock()

	n, err := sw.SupersedeResyncPayloads(ctx, a.TenantID, a.SiteID)
	if err != nil {
		deps.log().Warn("pmsd: superseded roster copies could not be reduced; the next pass retries", safeErrAttrs(err)...)
		return
	}
	if n > 0 {
		// A count only: the rows carry guest records.
		deps.log().Info("pmsd: superseded roster copies reduced to their hash", "count", n)
	}
}

func (r *pgRepo) SupersedeResyncPayloads(ctx context.Context, tenantID, siteID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT iam_v2.pms_resync_supersede_payloads($1::uuid,$2::uuid,2000)`, tenantID, siteID).Scan(&n)
	return n, err
}
