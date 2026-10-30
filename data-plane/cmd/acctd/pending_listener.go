package main

// A sign-in that has just committed its PENDING_ENFORCEMENT session tells the plan producer through the database
// (pg_notify('onegate_session_pending'), sent by scd inside the activating transaction, so it is delivered only if the session
// exists). Without it a new session waited for the next submission period (the cut-off setting, 5 s by default) before netd was even
// asked to enforce it, and under load that wait stacked on a slow pass (capacity acceptance, 2026-10-30).
//
// The notification is a hint, never the authority: it only brings the next submission forward. The periodic submission stays the
// fallback, so a lost notification, a dropped listen connection or a restart costs at most one period, exactly as before.

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const pendingChannel = "onegate_session_pending"

// pendingMinGap is the shortest time between two submissions started by wake-ups: a plan lists every live session, so it is derived and
// delivered no more than twice a second however many guests sign in.
const pendingMinGap = 500 * time.Millisecond

// listenPending holds one connection LISTENing on pendingChannel and calls kick for every notification until ctx ends. It reconnects
// with a capped backoff; while it is down only the periodic submission runs.
func listenPending(ctx context.Context, dsn string, kick func()) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := listenPendingOnce(ctx, dsn, kick)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("acctd: the new-session notification connection was lost; the periodic submission is the fallback", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func listenPendingOnce(ctx context.Context, dsn string, kick func()) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := pgx.Connect(cctx, dsn)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+pendingChannel); err != nil {
		return err
	}
	// A session created while the connection was away is picked up by the submission this reconnect triggers.
	kick()
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return err
		}
		kick()
	}
}

// acctdDefaultPoolMaxConns sizes acctd's pool explicitly: pgx's default (the larger of 4 and the CPU count) is shared by the accounting
// pass, the expiry tick, the plan submitter and the maintenance job, so one slow statement left the others waiting for a connection. Six
// plus the notification connection stay inside svc_acctd's connection limit (10). A pool_max_conns in the DSN still wins.
const acctdDefaultPoolMaxConns = 6

func acctdPoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = acctdDefaultPoolMaxConns
	}
	return cfg, nil
}
