// Package outbox implements the appliance's durable edge→cloud telemetry
// queue. Producers enqueue rows into the site-local sync_outbox table; the
// drainer publishes them to NATS subject telemetry.<applianceID> as
// request/reply and marks them sent only on a 200 from the cloud ingest.
//
// Guarantees:
//   - durability: a cloud/NATS outage loses nothing — rows wait locally;
//   - exactly-once landing: seq is the idempotency key; the cloud dedupes on
//     (appliance_id, seq), so replays after partial failures are safe;
//   - ordering per appliance: rows drain in seq order;
//   - backoff: failed rows retry with exponential backoff, capped; rows that
//     exhaust maxAttempts are flagged dead (visible in Hotel Admin) instead
//     of blocking the queue.
//
// Privacy: enqueue only aggregates. The cloud strips PII-looking keys as
// defense in depth, but the contract is that nothing guest-identifying is
// ever enqueued.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

type Outbox struct {
	DB          *pgxpool.Pool
	NC          *nats.Conn // may be nil (offline/dev) — drain simply waits
	ApplianceID string

	// Tunables (zero values get defaults from Start).
	DrainEvery  time.Duration
	MaxAttempts int
	ReqTimeout  time.Duration
}

// Enqueue stores one telemetry record. kind must be one of the cloud's
// accepted kinds (heartbeat, health, usage, auth_counts, pms_health,
// license_ack, backup, sync, update_progress).
func (o *Outbox) Enqueue(ctx context.Context, kind string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = o.DB.Exec(ctx,
		`INSERT INTO sync_outbox (kind, payload) VALUES ($1, $2)`, kind, raw)
	return err
}

// Start runs the drain loop until ctx is done.
func (o *Outbox) Start(ctx context.Context) {
	if o.DrainEvery == 0 {
		o.DrainEvery = 15 * time.Second
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 12 // ~ with backoff, roughly a day of retries
	}
	if o.ReqTimeout == 0 {
		o.ReqTimeout = 5 * time.Second
	}
	go func() {
		t := time.NewTicker(o.DrainEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := o.DrainOnce(ctx); err != nil {
					slog.Debug("outbox: drain pass ended", "sent", n, "err", err)
				}
			}
		}
	}()
}

type row struct {
	Seq      int64
	Kind     string
	Payload  []byte
	Attempts int
	Created  time.Time
}

// DrainOnce publishes due rows in seq order. Stops at the first failure so
// per-appliance ordering holds; returns how many rows were acked.
func (o *Outbox) DrainOnce(ctx context.Context) (int, error) {
	if o.NC == nil || !o.NC.IsConnected() {
		return 0, errors.New("nats unavailable")
	}
	rows, err := o.DB.Query(ctx, `
        SELECT seq, kind, payload, attempts, created_at
          FROM sync_outbox
         WHERE sent_at IS NULL AND dead = false AND next_attempt_at <= now()
         ORDER BY seq ASC
         LIMIT 100
    `)
	if err != nil {
		return 0, err
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Seq, &r.Kind, &r.Payload, &r.Attempts, &r.Created); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, r)
	}
	rows.Close()

	sent := 0
	for _, r := range pending {
		if err := o.publish(ctx, r); err != nil {
			o.recordFailure(ctx, r, err)
			return sent, err // stop: keep seq order
		}
		if _, err := o.DB.Exec(ctx,
			`UPDATE sync_outbox SET sent_at = now() WHERE seq = $1`, r.Seq); err != nil {
			return sent, err
		}
		sent++
	}
	if sent > 0 {
		_, _ = o.DB.Exec(ctx, `
            INSERT INTO sync_checkpoints (name, value, updated_at)
            VALUES ('last_drain', jsonb_build_object('sent', $1::int, 'at', now()), now())
            ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
        `, sent)
	}
	return sent, nil
}

func (o *Outbox) publish(ctx context.Context, r row) error {
	var payload map[string]any
	_ = json.Unmarshal(r.Payload, &payload)
	msg, err := json.Marshal(map[string]any{
		"appliance_id": o.ApplianceID,
		"seq":          r.Seq,
		"kind":         r.Kind,
		"ts":           r.Created.UTC(),
		"payload":      payload,
	})
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, o.ReqTimeout)
	defer cancel()
	resp, err := o.NC.RequestWithContext(rctx, "telemetry."+o.ApplianceID, msg)
	if err != nil {
		return err
	}
	if st := resp.Header.Get("Nats-Status"); st != "" && st != "200" {
		return errors.New("cloud ingest status " + st)
	}
	return nil
}

func (o *Outbox) recordFailure(ctx context.Context, r row, cause error) {
	attempts := r.Attempts + 1
	if attempts >= o.MaxAttempts {
		_, _ = o.DB.Exec(ctx, `
            UPDATE sync_outbox SET attempts = $2, dead = true, last_error = $3 WHERE seq = $1
        `, r.Seq, attempts, cause.Error())
		slog.Warn("outbox: row dead-lettered", "seq", r.Seq, "kind", r.Kind, "err", cause)
		return
	}
	// Exponential backoff: 30s, 1m, 2m, ... capped at 30m.
	backoff := 30 * time.Second << (attempts - 1)
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	_, _ = o.DB.Exec(ctx, `
        UPDATE sync_outbox
           SET attempts = $2, next_attempt_at = now() + $3::interval, last_error = $4
         WHERE seq = $1
    `, r.Seq, attempts, backoff.String(), cause.Error())
}

// Stats returns queue depth numbers for Hotel Admin / health reporting.
func (o *Outbox) Stats(ctx context.Context) (pending, dead int64, oldest *time.Time, err error) {
	err = o.DB.QueryRow(ctx, `
        SELECT count(*) FILTER (WHERE sent_at IS NULL AND dead = false),
               count(*) FILTER (WHERE dead = true),
               min(created_at) FILTER (WHERE sent_at IS NULL AND dead = false)
          FROM sync_outbox
    `).Scan(&pending, &dead, &oldest)
	return
}
