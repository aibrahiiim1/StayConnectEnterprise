package pmsd

// The stay-event application worker applies durable inbox rows to the Stay domain, one loop per Interface,
// through stayengine's per-Interface barrier. A Checkout Converter is required: without one a GO has no
// authoritative boundary.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// StayApplier applies one pending inbox event for a scope; the engine owns the transaction, barrier and Checkout conversion.
type StayApplier interface {
	ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error)
}

// ErrApplierRequired: applying PMS events without the engine (and its Checkout Converter) is unverified, so startup fails closed.
var ErrApplierRequired = errors.New("pmsd: stay-event ingest enabled but no Stay Applier is wired")

// applierConfig bounds the loop. Zero values get defaults.
type applierConfig struct {
	// IdlePause bounds the empty-inbox poll rate; a non-empty inbox is drained without pausing.
	IdlePause time.Duration
	// ErrorPause is the backoff after a failure, which leaves the event PENDING (the engine rolls back).
	ErrorPause time.Duration
	// MaxBatch bounds events per scope before yielding so one backlog cannot starve other Interfaces.
	MaxBatch int
	// wake ends an idle pause early when the worker reports a claimable event; nil keeps the plain poll.
	wake *applierWake
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

type applierScope struct{ Tenant, Site, Interface string }

// runApplierScope drains one Interface's inbox until ctx ends. An application failure is logged and backed off,
// never returned, so one Interface's outage cannot stop the others.
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
				// The engine's own errors are fixed sentinels and are logged as text; a database error is logged
				// as its SQLSTATE and type chain only, because its message and detail can carry a constraint's
				// key values (audit P7 L12).
				log.Warn("pmsd: stay-event application failed; event stays PENDING for retry",
					append(safeErrAttrs(err), "interface", sc.Interface, "cause", applierCause(err))...)
				sleepCtx(ctx, cfg.ErrorPause)
				break
			}
			if !ok {
				break // inbox empty for this scope
			}
			applied++
		}
		if applied == 0 {
			cfg.wake.wait(ctx, sc.Interface, cfg.IdlePause)
		}
	}
}

// applierCause is the error text unless a database error is in the chain, whose text may carry row values.
func applierCause(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return "database error (see sqlstate)"
	}
	return err.Error()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
