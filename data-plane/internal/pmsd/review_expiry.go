package pmsd

// Expiry of PMS review messages: iam_v2.pms_review_expire_due owns every rule (setting, start instant,
// waiting period, one disposition per message, bounded batch; earlier backlog is never touched). pmsd only
// supplies the cadence; a failed pass is retried on the next.

import (
	"context"
	"sync"
	"time"
)

// ReviewExpirer is optional (not a Repo method) so test doubles need not implement it.
type ReviewExpirer interface {
	ExpireDueReviews(ctx context.Context, tenantID, siteID string) (int, error)
}

// Not a setting: the expiry setting is in days, so this cadence is finer than anyone can observe.
const reviewExpiryEvery = 10 * time.Minute

var reviewExpiryLast struct {
	sync.Mutex
	at map[string]time.Time
}

func expireDueReviews(ctx context.Context, a Assignment, repo Repo, deps *Deps) {
	ex, ok := repo.(ReviewExpirer)
	if !ok {
		return
	}
	key := a.TenantID + "/" + a.SiteID
	reviewExpiryLast.Lock()
	if reviewExpiryLast.at == nil {
		reviewExpiryLast.at = map[string]time.Time{}
	}
	if last, seen := reviewExpiryLast.at[key]; seen && time.Since(last) < reviewExpiryEvery {
		reviewExpiryLast.Unlock()
		return
	}
	reviewExpiryLast.at[key] = time.Now()
	reviewExpiryLast.Unlock()

	n, err := ex.ExpireDueReviews(ctx, a.TenantID, a.SiteID)
	if err != nil {
		deps.log().Warn("pmsd: review-expiry sweep could not run; the next pass retries", "err", err)
		return
	}
	if n > 0 {
		// A count, never which messages: the messages carry reservation numbers.
		deps.log().Info("pmsd: review messages expired by the site's expiry setting", "count", n)
	}
}

func (r *pgRepo) ExpireDueReviews(ctx context.Context, tenantID, siteID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT iam_v2.pms_review_expire_due($1::uuid,$2::uuid,500)`, tenantID, siteID).Scan(&n)
	return n, err
}
