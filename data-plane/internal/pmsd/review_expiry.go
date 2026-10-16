package pmsd

// AUTOMATIC EXPIRY OF PMS REVIEW MESSAGES (audit H-15, Product-Owner decision D-D).
//
// The site's expiry setting is OFF by default. When a hotel switches it on, a message that arrived AFTER that
// moment and has waited the configured number of days for a decision is recorded as EXPIRED_UNDECIDED in the
// append-only disposition ledger -- by iam_v2.pms_review_expire_due (0123), which owns every rule: the setting,
// the start instant, the waiting period, one disposition per message and a bounded batch. Messages that arrived
// before the setting was switched on are never touched, so the existing backlog is never mass-updated.
//
// pmsd runs the sweep because it is the process that is always running on an appliance with a PMS; it adds only
// the cadence. A failure is logged and retried on the next pass -- an expiry that runs a few minutes late changes
// nothing a guest can see.

import (
	"context"
	"sync"
	"time"
)

// ReviewExpirer is implemented by the PostgreSQL repository. It is an optional capability rather than a Repo
// method so test doubles that do not care about expiry need not implement it.
type ReviewExpirer interface {
	ExpireDueReviews(ctx context.Context, tenantID, siteID string) (int, error)
}

// reviewExpiryEvery is how often the sweep runs. The setting is in DAYS, so a ten-minute cadence is far finer
// than anything an operator can observe; it is not a setting because no hotel needs to tune it.
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

// ExpireDueReviews runs one bounded expiry pass for the site.
func (r *pgRepo) ExpireDueReviews(ctx context.Context, tenantID, siteID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT iam_v2.pms_review_expire_due($1::uuid,$2::uuid,500)`, tenantID, siteID).Scan(&n)
	return n, err
}
