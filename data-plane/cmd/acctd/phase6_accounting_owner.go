package main

// THE ACCOUNTING OWNER FOR AGGREGATE ENTITLEMENTS MUST NOT DEPEND ON A DIFFERENT PHASE'S FLAGS.
//
// The Phase-6 accrual rides inside the Phase-3 arm's expiry sweep, and that arm is constructed only when the
// Phase-3 master and checkout-grace flags are on. Every other part of Phase 6 was made independent of its own
// flag for safety -- accrual is data-driven precisely so a disabled capability cannot turn a finite
// entitlement unlimited -- and the same argument applies with more force here, because Phase-3's flags are
// not even the ones an operator would think to check:
//
//   Phase-3 flags OFF + an aggregate entitlement already granted
//     => no sweep runs at all
//     => nothing consumes the budget, nothing exhausts, nothing terminates
//     => a finite package silently becomes unlimited, from a configuration change in an unrelated phase.
//
// So this arm exists: an expiry sweep with no Phase-3 prerequisites of any kind. It runs only when the
// Phase-3 arm is absent, so a normal deployment sweeps exactly once per tick and nothing changes; when the
// Phase-3 arm IS absent it is the accounting owner of last resort.
//
// It is data-driven like the tick it drives. On an appliance with no aggregate entitlements -- every
// appliance today -- the sweep's candidate query finds nothing and the accrual loop iterates over zero rows,
// so this writes nothing at all.

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
	// lastWarn keeps the "running without the Phase-3 arm" notice to once an hour: it is a real condition an
	// operator should see, and a line every tick would bury it.
	lastWarn time.Time
}

// newAggregateOwner returns the fallback owner, or nil when the Phase-3 arm is present and already sweeping.
func newAggregateOwner(p3 *phase3, db *pgxpool.Pool, tenant, site string, chargeBound int) *aggregateOwner {
	if p3 != nil {
		return nil // the Phase-3 arm owns the sweep; two sweepers would race for no benefit
	}
	if db == nil || tenant == "" || site == "" {
		return nil
	}
	return &aggregateOwner{
		enf:    enforce.New(db).WithAggregateOnlineTime(chargeBound),
		tenant: tenant, site: site,
	}
}

// sweep runs one expiry pass. A nil owner is safe to call, so the tick needs no branch.
func (o *aggregateOwner) sweep(ctx context.Context) {
	if o == nil {
		return
	}
	due, err := o.enf.EnforceExpiries(ctx, o.tenant, o.site)
	if err != nil {
		// Loud, and it does not stop the loop: the entitlements stay live and the next tick tries again.
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
