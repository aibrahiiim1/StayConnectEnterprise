// Package configpush publishes small "config changed" events to NATS so
// appliances can live-reload the affected subsystem without a restart.
// Phase 5.3 introduces it for the PMS subsystem; walled-garden and
// ticket-templates plug in later by adding new Subjects here.
package configpush

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/control-plane/internal/metrics"
)

// Pusher is what handlers call after they commit a mutation. A nil receiver
// is safe: all methods no-op when NATS isn't configured (dev / tests).
type Pusher struct {
	nc  *nats.Conn
	met *metrics.Registry
}

func New(nc *nats.Conn) *Pusher { return &Pusher{nc: nc} }

// NewWithMetrics is the production constructor — increments
// ctrlapi_config_pushed_total on each publish.
func NewWithMetrics(nc *nats.Conn, met *metrics.Registry) *Pusher {
	return &Pusher{nc: nc, met: met}
}

// Event is the on-wire payload. Kept minimal on purpose — scd doesn't need
// to know *what* changed, only *that* something changed, and re-reads the
// DB to rebuild state. Including name + action is enough for operator
// observability in NATS tracing.
type Event struct {
	Action string `json:"action"`         // "created" | "updated" | "deleted"
	Name   string `json:"name,omitempty"` // the row's name (where applicable)
}

// PMS publishes config.{tenantID}.pms. Best-effort: publish failure is
// logged but never escalated — the mutation already committed, and the
// next reload (on another event or a restart) will pick up the change.
func (p *Pusher) PMS(ctx context.Context, tenantID, action, name string) {
	if p == nil || p.nc == nil {
		return
	}
	body, _ := json.Marshal(Event{Action: action, Name: name})
	if err := p.nc.Publish("config."+tenantID+".pms", body); err != nil {
		slog.Warn("configpush: pms publish failed", "err", err, "tenant", tenantID)
		return
	}
	if p.met != nil {
		p.met.ConfigPushed.WithLabelValues("pms", action).Inc()
	}
}
