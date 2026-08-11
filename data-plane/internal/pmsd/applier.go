package pmsd

// STAY-EVENT APPLICATION WORKER.
//
// The connector's job ends when a PMS frame becomes a durable, admitted row in the stay_events inbox. Somebody
// then has to APPLY those rows to the Stay domain — and until this existed, nobody did: the Stay Engine and
// Checkout Converter were reachable only from tests.
//
// This worker is that owner, supervised alongside (never inside) the connector workers:
//
//   - it runs only when the Phase-3 master AND ingest flags are ON; while dark it is never constructed, opens
//     no database handle and does no work;
//   - it consumes the DURABLE inbox — it never parses a frame, never opens a second PMS socket, and does not
//     care how a row got there (live, resync or replay);
//   - each PMS Interface gets its own loop, so one Interface's failure cannot stall another's;
//   - application goes through stayengine's per-Interface barrier, so ordering holds even with several
//     processes running;
//   - a Checkout Converter is REQUIRED: without one a GO event would have no authoritative boundary, so the
//     worker refuses to start rather than silently applying checkouts with a server clock;
//   - it drains on shutdown, and because every event is claimed transactionally, a restart resumes exactly the
//     events that were still PENDING without re-applying anything.

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// StayApplier applies ONE pending inbox event for a scope, returning whether it processed anything. It is the
// narrow contract the worker needs from the Stay Engine; the engine owns the transaction, the barrier and the
// Checkout conversion.
type StayApplier interface {
	ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error)
}

// ErrApplierRequired — the ingest flag is ON but no Stay Applier was wired. Applying PMS events without the
// engine (and therefore without the Checkout Converter it carries) is exactly the unverified path Phase 3
// exists to remove, so startup fails closed instead.
var ErrApplierRequired = errors.New("pmsd: stay-event ingest enabled but no Stay Applier is wired")

// applierConfig bounds the loop. Zero values get sane defaults.
type applierConfig struct {
	// IdlePause is how long a scope waits after finding nothing to do. It bounds the empty-inbox poll rate;
	// it does NOT delay work, because a non-empty inbox is drained without pausing.
	IdlePause time.Duration
	// ErrorPause is the backoff after a failed application. A failure leaves the event PENDING (the engine
	// rolls its transaction back), so retrying is safe — but retrying instantly would spin.
	ErrorPause time.Duration
	// MaxBatch bounds how many events one scope applies before yielding, so a large backlog on one Interface
	// cannot starve the others.
	MaxBatch int
}

func (c *applierConfig) withDefaults() {
	if c.IdlePause <= 0 {
		c.IdlePause = 2 * time.Second
	}
	if c.ErrorPause <= 0 {
		c.ErrorPause = 5 * time.Second
	}
	if c.MaxBatch <= 0 {
		c.MaxBatch = 64
	}
}

// applierScope is one Interface's application loop identity.
type applierScope struct{ Tenant, Site, Interface string }

// runApplierScope drains one Interface's inbox until the context ends. It never returns an error for an
// application failure: a failure is that Interface's problem, it is logged with a bounded code, and the loop
// backs off and continues — which is what keeps one Interface's outage from stopping the others.
func runApplierScope(ctx context.Context, ap StayApplier, sc applierScope, cfg applierConfig, log *slog.Logger) {
	cfg.withDefaults()
	for {
		if ctx.Err() != nil {
			return
		}
		applied := 0
		for applied < cfg.MaxBatch {
			ok, err := ap.ProcessNext(ctx, sc.Tenant, sc.Site, sc.Interface)
			if err != nil {
				if ctx.Err() != nil {
					return // shutting down: not an error worth reporting
				}
				log.Warn("pmsd: stay-event application failed; event stays PENDING for retry",
					"code", Classify(err).String(), "interface", sc.Interface)
				sleepCtx(ctx, cfg.ErrorPause)
				break
			}
			if !ok {
				break // inbox empty for this scope
			}
			applied++
		}
		if applied == 0 {
			sleepCtx(ctx, cfg.IdlePause)
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
