// Package heartbeat consumes scd heartbeat messages and keeps the
// appliances.{last_seen_at,status} columns up to date.
//
// Subjects (phase 5.4):
//
//	hb.<applianceID>   — published by scd every 10s
//
// Transitions:
//
//	enrolled | offline | online  + heartbeat  →  online
//	online                       + 30s silence →  offline
//	pending | retired            + heartbeat  →  untouched (admin-owned)
package heartbeat

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/control-plane/internal/metrics"
)

const (
	// StaleAfter is how long since last_seen_at we allow before flipping
	// an online appliance to offline. Three missed heartbeats.
	StaleAfter = 30 * time.Second

	// SweepInterval is how often the staleness sweeper runs.
	SweepInterval = 15 * time.Second
)

type hbMsg struct {
	ApplianceID   string `json:"appliance_id"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Version       string `json:"version"`
}

// StartConsumer subscribes to hb.* and, in a separate goroutine, runs the
// staleness sweeper. Both halves stop when ctx is cancelled. met may be
// nil — counters silently no-op in that case.
func StartConsumer(ctx context.Context, nc *nats.Conn, db *pgxpool.Pool, met *metrics.Registry) error {
	sub, err := nc.Subscribe("hb.*", func(m *nats.Msg) {
		var hb hbMsg
		if err := json.Unmarshal(m.Data, &hb); err != nil {
			slog.Warn("heartbeat decode failed", "subj", m.Subject, "err", err)
			return
		}
		if hb.ApplianceID == "" {
			return
		}
		// The UPDATE is a no-op for pending/retired rows — those remain
		// admin-controlled. Enrolled/offline both promote to online on any
		// heartbeat. Version is persisted so the admin UI can render
		// "scd 0.0.3-dev" without a separate RPC.
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var tenantID string
		if err := db.QueryRow(ctx, `
            UPDATE appliances
               SET last_seen_at = now(),
                   status = CASE WHEN status IN ('enrolled','offline','online')
                                 THEN 'online' ELSE status END,
                   version = COALESCE(NULLIF($2,''), version),
                   updated_at = now()
             WHERE id = $1
            RETURNING tenant_id::text
        `, hb.ApplianceID, hb.Version).Scan(&tenantID); err != nil {
			slog.Warn("heartbeat update failed", "id", hb.ApplianceID, "err", err)
			return
		}
		if met != nil && tenantID != "" {
			met.HeartbeatsReceived.WithLabelValues(tenantID).Inc()
		}
	})
	if err != nil {
		return err
	}
	slog.Info("heartbeat consumer subscribed", "subject", "hb.*")

	go func() {
		<-ctx.Done()
		_ = sub.Drain()
	}()
	go sweepLoop(ctx, db, met)
	return nil
}

func sweepLoop(ctx context.Context, db *pgxpool.Pool, met *metrics.Registry) {
	t := time.NewTicker(SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-StaleAfter)
			sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			tag, err := db.Exec(sctx, `
                UPDATE appliances
                   SET status = 'offline', updated_at = now()
                 WHERE status = 'online'
                   AND (last_seen_at IS NULL OR last_seen_at < $1)
            `, cutoff)
			cancel()
			if err != nil {
				slog.Warn("heartbeat sweep failed", "err", err)
				continue
			}
			if n := tag.RowsAffected(); n > 0 {
				slog.Info("heartbeat sweep", "flipped_offline", n)
				if met != nil {
					met.AppliancesOffline.Add(float64(n))
				}
			}
		}
	}
}
