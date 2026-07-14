// Package fleet ingests aggregated, non-PII telemetry pushed by appliances
// over NATS (subject telemetry.<applianceID>) and records it in the
// fleet_telemetry hypertable.
//
// Idempotency: every message carries the appliance's outbox sequence number;
// (appliance_id, seq) is inserted into fleet_telemetry_dedupe first — a
// conflict means the message was already processed and it is acked without
// a second insert, so replays after cloud outages land exactly once.
//
// Privacy: telemetry is summaries only. As defense in depth against an edge
// bug, payload keys that look like guest PII are stripped before storage.
package fleet

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

// piiKeys never belong in fleet telemetry. Case-insensitive substring match.
var piiKeys = []string{
	"mac", "email", "phone", "guest_name", "first_name", "last_name",
	"room", "reservation", "voucher_code", "code", "otp", "password", "ip",
}

// Sanitize strips disallowed keys from a telemetry payload (top level and
// one level of nesting — telemetry payloads are flat by contract).
func Sanitize(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		if isPII(k) {
			continue
		}
		if m, ok := v.(map[string]any); ok {
			inner := make(map[string]any, len(m))
			for ik, iv := range m {
				if !isPII(ik) {
					inner[ik] = iv
				}
			}
			out[k] = inner
			continue
		}
		out[k] = v
	}
	return out
}

func isPII(key string) bool {
	k := strings.ToLower(key)
	for _, p := range piiKeys {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// Message is the wire format appliances publish on telemetry.<applianceID>.
type Message struct {
	ApplianceID string         `json:"appliance_id"`
	Seq         int64          `json:"seq"`
	Kind        string         `json:"kind"`
	TS          time.Time      `json:"ts"`
	Payload     map[string]any `json:"payload"`
}

var allowedKinds = map[string]bool{
	"heartbeat": true, "health": true, "usage": true, "auth_counts": true,
	"pms_health": true, "license_ack": true, "backup": true, "sync": true,
	"update_progress": true, "service_health": true,
}

type Consumer struct {
	DB *pgxpool.Pool
}

// Start subscribes to telemetry.> and processes messages until ctx ends.
func (c *Consumer) Start(ctx context.Context, nc *nats.Conn) error {
	sub, err := nc.Subscribe("telemetry.>", func(m *nats.Msg) {
		c.handle(ctx, m)
	})
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = sub.Drain()
	}()
	return nil
}

func (c *Consumer) handle(ctx context.Context, m *nats.Msg) {
	var msg Message
	if err := json.Unmarshal(m.Data, &msg); err != nil {
		slog.Warn("fleet: malformed telemetry", "err", err)
		respond(m, http400)
		return
	}
	// Subject is telemetry.<applianceID>; the payload must agree — an
	// appliance can only speak for itself (subject identity wins).
	subjID := strings.TrimPrefix(m.Subject, "telemetry.")
	if msg.ApplianceID == "" {
		msg.ApplianceID = subjID
	}
	if msg.ApplianceID != subjID || !allowedKinds[msg.Kind] || msg.Seq <= 0 {
		respond(m, http400)
		return
	}

	dbctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Resolve tenant/site/serial from the appliance registry (never trust payload).
	var tenantID, siteID, serial string
	err := c.DB.QueryRow(dbctx,
		`SELECT tenant_id, COALESCE(site_id::text,''), COALESCE(serial,'') FROM appliances WHERE id = $1`,
		msg.ApplianceID).Scan(&tenantID, &siteID, &serial)
	if err != nil {
		respond(m, http404)
		return
	}

	// Dedupe gate first: replays ack without a second telemetry row.
	tag, err := c.DB.Exec(dbctx, `
        INSERT INTO fleet_telemetry_dedupe (appliance_id, seq)
        VALUES ($1, $2) ON CONFLICT DO NOTHING
    `, msg.ApplianceID, msg.Seq)
	if err != nil {
		respond(m, http500)
		return
	}
	if tag.RowsAffected() == 0 {
		respond(m, http200) // already processed — idempotent ack
		return
	}

	ts := msg.TS
	if ts.IsZero() || ts.After(time.Now().Add(24*time.Hour)) {
		ts = time.Now().UTC() // clock-skew guard: never index far-future rows
	}
	clean, _ := json.Marshal(Sanitize(msg.Payload))
	var siteArg any
	if siteID != "" {
		siteArg = siteID
	}
	if _, err := c.DB.Exec(dbctx, `
        INSERT INTO fleet_telemetry (ts, tenant_id, site_id, appliance_id, kind, seq, payload)
        VALUES ($1,$2,$3,$4,$5,$6,$7)
    `, ts, tenantID, siteArg, msg.ApplianceID, msg.Kind, msg.Seq, clean); err != nil {
		// Roll the dedupe row back so the appliance's retry can land.
		_, _ = c.DB.Exec(dbctx,
			`DELETE FROM fleet_telemetry_dedupe WHERE appliance_id = $1 AND seq = $2`,
			msg.ApplianceID, msg.Seq)
		respond(m, http500)
		return
	}
	// Service-health telemetry drives the deduplicated, lifecycle-aware fleet
	// alerts (crash loop / unavailable / dependency / boot-not-converged), which
	// auto-resolve when the appliance reports recovery.
	if msg.Kind == "service_health" {
		c.reconcileHealthAlerts(dbctx, msg.ApplianceID, serial, msg.Payload)
	}
	respond(m, http200)
}

const (
	http200 = "200"
	http400 = "400"
	http404 = "404"
	http500 = "500"
)

// respond acks request/reply publishes; fire-and-forget publishes have no
// reply subject and the write is simply durable via the outbox retry.
func respond(m *nats.Msg, status string) {
	if m.Reply == "" {
		return
	}
	r := nats.NewMsg(m.Reply)
	r.Header = nats.Header{"Nats-Status": []string{status}}
	_ = m.RespondMsg(r)
}
