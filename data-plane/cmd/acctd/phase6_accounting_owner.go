package main

// Aggregate accrual rides in the Phase-3 arm's expiry sweep. With Phase-3 flags off and an aggregate
// entitlement already granted, no sweep would run and a finite package would silently become unlimited, so this
// fallback sweeps instead. It runs only when the Phase-3 arm is absent and writes nothing without aggregate
// entitlements.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
)

type aggregateOwner struct {
	enf          *enforce.Enforcer
	tenant, site string
	lastWarn     time.Time // limits the "without the Phase-3 arm" notice to hourly
}

// newAggregateOwner returns nil when the Phase-3 arm is present: two sweepers would only race.
func newAggregateOwner(p3 *phase3, db *pgxpool.Pool, tenant, site string, chargeBound int) *aggregateOwner {
	if p3 != nil {
		return nil
	}
	if db == nil || tenant == "" || site == "" {
		return nil
	}
	return &aggregateOwner{
		enf:    enforce.New(db).WithAggregateOnlineTime(chargeBound),
		tenant: tenant, site: site,
	}
}

// sweep is safe on a nil owner.
func (o *aggregateOwner) sweep(ctx context.Context) {
	if o == nil {
		return
	}
	due, err := o.enf.EnforceExpiries(ctx, o.tenant, o.site)
	if err != nil {
		// Loud, but it must not stop the loop; the next tick retries.
		slog.Error("phase6: aggregate expiry sweep failed", "err", err)
		return
	}
	if time.Since(o.lastWarn) > time.Hour {
		o.lastWarn = time.Now()
		slog.Info("phase6: accounting aggregate entitlements without the Phase-3 arm; " +
			"this is the safety path that keeps a finite budget from becoming unlimited")
	}
	for _, x := range due {
		slog.Info("phase6: access ended at its true time",
			"entitlement", x.EntitlementID, "reason", x.Reason, "effective_at", x.At,
			"sessions_ended", x.Sessions, "devices_revoked", x.Devices)
	}
}
