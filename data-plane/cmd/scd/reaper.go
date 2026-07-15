package main

// Session reaper (phase 6.4).
//
// acctd already enforces voucher quotas (bytes/time) when traffic flows.
// What it can't catch: sessions whose guest device went silent after
// auth — no packets means no acctd tick means no enforcement. The reaper
// fills that gap by scanning the sessions table on a timer and closing
// rows whose expires_at is past, OR whose last_activity_at is older than
// the configured idle window.
//
// On close: revoke from nft, remove tc class, mark sessions row state=closed
// with reason='quota_time' (expired) or 'idle' (no traffic). Both reasons
// are in the existing CHECK constraint; we don't extend it.
//
// One scd at the site does the work. With HA pair (5.5), the loser of
// the queue subscription would just re-process some rows that the winner
// already closed — idempotent, no harm.

import (
	"context"
	"log/slog"
	"net"
	"time"
)

const (
	// reaperInterval is how often we poll. 30s gives a ~30s lag worst case
	// before an expired session is closed; small enough to feel timely,
	// big enough to keep DB load trivial.
	reaperInterval = 30 * time.Second

	// idleTimeout is the no-traffic window after which we close a session.
	// 30 minutes matches typical hotspot defaults; tenant-tunable in a
	// future phase via tenants.auth_methods JSON.
	idleTimeout = 30 * time.Minute

	// reaperBatch caps each sweep so a backlog doesn't pin a connection.
	reaperBatch = 200
)

// startReaperLoop kicks off the background sweeper. Stops when ctx is done.
func (s *server) startReaperLoop(ctx context.Context) {
	t := time.NewTicker(reaperInterval)
	defer t.Stop()
	// Fire one immediately so a long-uptime restart catches up before the
	// first 30s window elapses.
	s.reaperSweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reaperSweep(ctx)
		}
	}
}

func (s *server) reaperSweep(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	idleCutoff := time.Now().Add(-idleTimeout)

	// Converge voucher lifecycle state: an activated voucher whose validity
	// window has closed becomes 'expired' (time), and one whose AGGREGATE data
	// across all its sessions has met the plan cap becomes 'exhausted' (data).
	// Authoritative login gating lives in voucher.Validate; this keeps the
	// persisted state, batch totals and plan-edit guard correct even for idle
	// vouchers with no active session. Idempotent (only active → terminal).
	if _, err := s.db.Exec(rctx, `
        UPDATE vouchers v
           SET state = CASE
                 WHEN COALESCE(v.expires_at,
                        v.activated_at + make_interval(secs => t.duration_seconds)) <= now() THEN 'expired'
                 ELSE 'exhausted' END
          FROM ticket_templates t
         WHERE v.tenant_id = $1 AND t.id = v.template_id AND v.state = 'active'
           AND ( COALESCE(v.expires_at,
                    v.activated_at + make_interval(secs => t.duration_seconds)) <= now()
                 OR (t.data_cap_bytes IS NOT NULL
                     AND COALESCE((SELECT SUM(se.bytes_up + se.bytes_down)
                                     FROM sessions se WHERE se.voucher_id = v.id), 0) >= t.data_cap_bytes) )
    `, s.tenID); err != nil {
		slog.Warn("reaper voucher reconcile failed", "err", err)
	}

	// Two reasons in one query: 'quota_time' (expired) wins over 'idle'
	// when both are true (don't blame the user for going quiet on a
	// session they couldn't have used past expiry).
	rows, err := s.db.Query(rctx, `
        SELECT id, host(ip),
               CASE
                 WHEN expires_at IS NOT NULL AND expires_at <= now() THEN 'quota_time'
                 ELSE 'idle'
               END AS reason
          FROM sessions
         WHERE tenant_id = $1
           AND site_id = $2
           AND state = 'active'
           AND ((expires_at IS NOT NULL AND expires_at <= now())
                OR last_activity_at < $3)
         ORDER BY last_activity_at NULLS FIRST
         LIMIT $4
    `, s.tenID, s.siteID, idleCutoff, reaperBatch)
	if err != nil {
		slog.Warn("reaper query failed", "err", err)
		return
	}
	type rrow struct {
		id, ipStr, reason string
	}
	var batch []rrow
	for rows.Next() {
		var r rrow
		if err := rows.Scan(&r.id, &r.ipStr, &r.reason); err != nil {
			rows.Close()
			slog.Warn("reaper scan failed", "err", err)
			return
		}
		batch = append(batch, r)
	}
	rows.Close()
	if len(batch) == 0 {
		return
	}
	expired, idled := 0, 0
	for _, r := range batch {
		ip := net.ParseIP(r.ipStr)
		if ip == nil {
			continue
		}
		// Order matters: drop kernel state first so even if the DB write
		// fails, the guest is no longer authorized.
		nc := s.resolveNetwork(rctx, ip)
		if err := s.nft.Deny(rctx, nc.Bridge, ip); err != nil {
			slog.Warn("reaper nft deny", "ip", r.ipStr, "err", err)
		}
		if err := s.shp.DeleteSession(rctx, nc.Bridge, ip); err != nil {
			slog.Warn("reaper shape delete", "ip", r.ipStr, "err", err)
		}
		if err := s.sess.End(rctx, ip, r.reason); err != nil {
			slog.Warn("reaper session end", "ip", r.ipStr, "err", err)
			continue
		}
		s.met.ReaperClosed.WithLabelValues(r.reason).Inc()
		s.met.SessionsClosed.WithLabelValues(r.reason).Inc()
		if r.reason == "quota_time" {
			expired++
		} else {
			idled++
		}
	}
	if expired+idled > 0 {
		slog.Info("reaper swept", "expired", expired, "idle", idled)
	}
}
