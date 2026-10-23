package main

// Room-charge financial execution path: scd claims a queued posting per lane (PMS interface), re-verifies
// pinned evidence, allocates the P#, builds the PS, records the attempt with the SHA-256 of those exact
// bytes, then hands it to pmsd (sole owner of the FIAS connection, carries bytes verbatim). PA=OK settles and
// grants, any other PA fails, anything inconclusive is UNKNOWN for audited manual review; nothing retries an UNKNOWN.
//
// Runs as its own login (svc_posting, SCD_POSTING_DB_URL), separate from the guest-authority pool, only when
// the outbox worker is deployed (STAYCONNECT_PHASE4_MASTER + _OUTBOX_WORKER). Real transmission also needs
// STAYCONNECT_PHASE4_PMS_TRANSMIT on scd and pmsd; otherwise the DARK guard refuses every PS and releases the
// claim without consuming a P#.

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/posting"
)

const EnvPostingDSN = "SCD_POSTING_DB_URL"

type postingWorker struct {
	engine *posting.Engine
	pool   *pgxpool.Pool
}

// initPostingWorker builds the worker, or returns nil when the outbox worker is not deployed here.
func (s *server) initPostingWorker(ctx context.Context) *postingWorker {
	cfg, err := posting.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("posting worker: configuration refused; room charges will not be carried", "err", err)
		return nil
	}
	if !cfg.OutboxOn() {
		slog.Info("posting worker: not deployed (outbox worker off)")
		return nil
	}
	dsn := strings.TrimSpace(os.Getenv(EnvPostingDSN))
	if dsn == "" {
		slog.Error("posting worker: " + EnvPostingDSN + " is not set; room charges will not be carried")
		return nil
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("posting worker: database unavailable", "err", err)
		return nil
	}
	eng, err := posting.NewProductionEngine(posting.NewRepo(pool))
	if err != nil {
		pool.Close()
		slog.Error("posting worker: engine refused to start", "err", err)
		return nil
	}
	slog.Info("posting worker ready", "transmit", cfg.TransmitOn())
	return &postingWorker{engine: eng, pool: pool}
}

// postingLoop drives the lanes. Orphaned attempts are recovered first (and every minute): one left SENDING is
// concluded UNKNOWN before anything new is claimed on its lane.
func (s *server) postingLoop(ctx context.Context, w *postingWorker) {
	if w == nil {
		return
	}
	defer w.pool.Close()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var lastRecover, lastApply time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s.tenID == "" || s.siteID == "" {
			continue
		}
		if time.Since(lastRecover) > time.Minute {
			if n, err := w.engine.RecoverOrphanedAttempts(ctx, s.tenID, s.siteID, 0); err != nil {
				slog.Error("posting worker: orphan recovery failed", "err", err)
			} else if n > 0 {
				slog.Warn("posting worker: attempts left in flight by a stopped worker are now UNKNOWN (manual review)", "count", n)
			}
			lastRecover = time.Now()
		}
		if time.Since(lastApply) > 30*time.Second {
			if err := w.engine.ApplySettlementOutcomes(ctx, s.tenID, s.siteID); err != nil {
				slog.Error("posting worker: settlement catch-up failed", "err", err)
			}
			lastApply = time.Now()
		}
		for _, iface := range s.queuedLanes(ctx, w) {
			// One posting per lane per tick: a lane is serialized by the database (one IN_FLIGHT per
			// interface), and a slow PMS on one interface never holds another.
			out, err := w.engine.RunOnce(ctx, s.tenID, s.siteID, iface)
			if out.Claimed {
				slog.Info("posting worker: attempt concluded", "posting", out.PostingID, "attempt", out.AttemptNo,
					"result", out.Result, "as", out.ASStatus, "code", string(out.RefusedFor), "not_sent_reason", out.NotSentReason)
			}
			if err != nil && out.Result != "UNKNOWN" && out.Result != "DECLINED" {
				slog.Warn("posting worker: lane pass failed", "interface", iface, "err", err)
			}
		}
	}
}

func (s *server) queuedLanes(ctx context.Context, w *postingWorker) []string {
	rows, err := w.pool.Query(ctx, `SELECT DISTINCT pms_interface_id::text FROM iam_v2.posting_outbox
		WHERE tenant_id=$1 AND site_id=$2 AND state='QUEUED'`, s.tenID, s.siteID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}
