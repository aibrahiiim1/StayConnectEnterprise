package main

// AD-34: a device that rotates its hardware address leaves a row in iam_v2.devices on every first sight, and nothing
// removed them. The hourly retention run now asks the database to remove devices unseen for the site's device-retention
// setting that nothing references (iam_v2.prune_idle_devices reads the references from the catalogue, so it can never
// remove a device with a session, a slot, a credential or any other dependent row).

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	deviceRetentionBatch     = 500
	deviceRetentionMaxPasses = 40 // 20,000 a tick; a larger backlog is worked off over several hours
)

func (s *server) pruneIdleDevices(ctx context.Context) {
	if s.db == nil || s.tenID == "" || s.siteID == "" {
		return
	}
	total := 0
	for pass := 0; pass < deviceRetentionMaxPasses; pass++ {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		var n int
		err := s.db.QueryRow(cctx, `SELECT iam_v2.prune_idle_devices($1::uuid,$2::uuid,$3)`, s.tenID, s.siteID, deviceRetentionBatch).Scan(&n)
		cancel()
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42883" {
				slog.Warn("device retention unavailable: this database predates migration 0170")
			} else if ctx.Err() == nil {
				slog.Warn("device retention run failed", "err", err)
			}
			return
		}
		total += n
		if n < deviceRetentionBatch {
			break
		}
	}
	if total > 0 {
		slog.Info("idle devices removed by retention", "devices", total)
	}
}

// alertNetworkPaused records that room sign-in was paused on a guest network (AD-31). Best effort: the pause itself is
// already in force and visible on the Room sign-in page; this is the notification.
func (s *server) alertNetworkPaused(ctx context.Context, network string, failures int, until *time.Time) {
	if s.db == nil {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	payload := map[string]any{"guest_network": network, "wrong_sign_ins": failures}
	if until != nil {
		payload["until"] = until.UTC().Format(time.RFC3339)
	}
	if _, err := s.db.Exec(actx, `INSERT INTO audit_log
	    (tenant_id, actor_type, actor_id, action, target_type, target_id, payload)
	    VALUES (NULLIF($1,'')::uuid, 'system', NULL, 'signin.room_network_paused', 'guest_network', $2, $3)`,
		s.tenID, network, payload); err != nil {
		slog.Warn("room sign-in pause: the staff alert could not be recorded", "err", err)
	}
}
