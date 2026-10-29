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
	"sync"
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
	lanes := newLaneScheduler()
	defer w.pool.Close()
	defer lanes.wait() // no lane may still be posting when the pool closes
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
			// Recovery concludes attempts left SENDING as UNKNOWN; it must never see a lane's live attempt, so it waits
			// until no lane is mid-posting.
			lanes.wait()
			if n, err := w.engine.RecoverOrphanedAttempts(ctx, s.tenID, s.siteID, 0); err != nil {
				slog.Error("posting worker: orphan recovery failed", "err", err)
			} else if n > 0 {
				slog.Warn("posting worker: attempts left in flight by a stopped worker are now UNKNOWN (manual review)", "count", n)
			}
			lastRecover = time.Now()
		}
		if time.Since(lastApply) > 30*time.Second {
			lanes.wait()
			if err := w.engine.ApplySettlementOutcomes(ctx, s.tenID, s.siteID); err != nil {
				slog.Error("posting worker: settlement catch-up failed", "err", err)
			}
			lastApply = time.Now()
		}
		for _, iface := range s.queuedLanes(ctx, w) {
			// One posting per lane at a time, each lane in its own goroutine: a lane is also serialized by the database
			// (one IN_FLIGHT per interface), and a slow PMS on one interface no longer holds the others.
			iface := iface
			lanes.start(iface, func() {
				out, err := w.engine.RunOnce(ctx, s.tenID, s.siteID, iface)
				if out.Claimed {
					slog.Info("posting worker: attempt concluded", "posting", out.PostingID, "attempt", out.AttemptNo,
						"result", out.Result, "as", out.ASStatus, "code", string(out.RefusedFor), "not_sent_reason", out.NotSentReason, "parked", out.Parked)
				}
				if err != nil && out.Result != "UNKNOWN" && out.Result != "DECLINED" {
					slog.Warn("posting worker: lane pass failed", "interface", iface, "err", err)
				}
			})
		}
	}
}

// laneScheduler runs each lane's pass in its own goroutine and never starts a lane that is still running.
type laneScheduler struct {
	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

func newLaneScheduler() *laneScheduler { return &laneScheduler{running: map[string]bool{}} }

// start runs fn for lane unless that lane's previous pass is still running; it reports whether it started.
func (l *laneScheduler) start(lane string, fn func()) bool {
	l.mu.Lock()
	if l.running[lane] {
		l.mu.Unlock()
		return false
	}
	l.running[lane] = true
	l.wg.Add(1)
	l.mu.Unlock()
	go func() {
		defer func() {
			l.mu.Lock()
			delete(l.running, lane)
			l.mu.Unlock()
			l.wg.Done()
		}()
		fn()
	}()
	return true
}

// wait blocks until no lane is running.
func (l *laneScheduler) wait() { l.wg.Wait() }

func (s *server) queuedLanes(ctx context.Context, w *postingWorker) []string {
	// A lane whose head row is backing off (not_before in the future) has nothing to do yet; its order is the contract,
	// so rows behind a waiting head are not work either (audit AD-41).
	rows, err := w.pool.Query(ctx, `SELECT DISTINCT o.pms_interface_id::text FROM iam_v2.posting_outbox o
		WHERE o.tenant_id=$1 AND o.site_id=$2 AND o.state='QUEUED' AND o.not_before <= now()
		  AND NOT EXISTS (SELECT 1 FROM iam_v2.posting_outbox h
		                   WHERE h.pms_interface_id = o.pms_interface_id AND h.state='QUEUED' AND (h.enqueued_at, h.id) < (o.enqueued_at, o.id)
		                     AND h.not_before > now())`, s.tenID, s.siteID)
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
