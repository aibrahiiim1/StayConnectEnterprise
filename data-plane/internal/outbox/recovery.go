package outbox

// Recovery, retention, accounting, and saying which of those a stuck queue actually needs.
//
// The queue had three problems that all looked identical on the screen — a number going up.
//
//   1. A record that exhausted its retries was marked dead and never offered again. Nothing cleared the
//      flag, so "given up on" was permanent and the product had no way to change its mind. On the appliance
//      this was written for that is the OLDEST 9 395 records: turning the link on would have drained the
//      77 000 newer ones straight past them and left a contiguous hole at the far end that neither side
//      would notice.
//   2. Delivered records were never removed. 76 MB and climbing on the disk that also holds the guest
//      database.
//   3. "Not draining" was inferred from queue SIZE, which cannot tell a broken network from an absent
//      listener from a queue that is draining perfectly well and simply has a lot to get through.
//
// So: RecoverExhausted puts records back in bounded batches, PruneDelivered removes delivered ones and only
// delivered ones, Accounting makes every record fall in exactly one bucket, and the drain loop records WHY
// it last stopped so an operator is told which of the three they have.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// DeliveryState is what the last drain attempt learned. It is evidence, not a size threshold: every value
// here is set by something that happened, and Unknown means nothing has been tried yet.
type DeliveryState string

const (
	// StateUnknown — no attempt since this process started.
	StateUnknown DeliveryState = "UNKNOWN"
	// StateTransportUnavailable — there is no usable connection to the messaging transport at all. The
	// appliance cannot reach the cloud; nothing about the receiver is known because nothing was asked.
	StateTransportUnavailable DeliveryState = "TRANSPORT_UNAVAILABLE"
	// StateReceiverUnavailable — the transport is connected and the request went out, but nothing is
	// listening on the appliance's telemetry subject. This is the one that used to read as a network fault:
	// the appliance is online, the cloud is reachable, and the service that consumes telemetry is not there.
	StateReceiverUnavailable DeliveryState = "RECEIVER_UNAVAILABLE"
	// StateReceiverRejected — the receiver answered and refused. A 404 means it does not recognise this
	// appliance; a 400 means it rejected the record. Retrying will not change either, and both need a human.
	StateReceiverRejected DeliveryState = "RECEIVER_REJECTED"
	// StateDraining — the last pass delivered records and there are more to go.
	StateDraining DeliveryState = "DRAINING"
	// StateIdle — the last pass found nothing to send. The healthy steady state.
	StateIdle DeliveryState = "IDLE"
)

// Outcome is the result of the most recent drain attempt.
type Outcome struct {
	At     time.Time     `json:"at"`
	State  DeliveryState `json:"state"`
	Sent   int           `json:"sent"`
	Detail string        `json:"detail,omitempty"` // bounded, never a payload and never a credential
}

// Accounting places every record in exactly one bucket. It exists so "the backlog has been recovered" can be
// checked rather than asserted: delivered + pending + exhausted must equal total, and a record that quietly
// disappeared would show up as the sum not adding up.
type Accounting struct {
	Delivered       int64      `json:"delivered"`
	Pending         int64      `json:"pending"`
	Exhausted       int64      `json:"exhausted"`
	Total           int64      `json:"total"`
	OldestPending   *time.Time `json:"oldest_pending,omitempty"`
	OldestExhausted *time.Time `json:"oldest_exhausted,omitempty"`
	NewestCreated   *time.Time `json:"newest_created,omitempty"`
	Bytes           int64      `json:"bytes"`
}

// Balanced reports whether the three buckets account for every record. A false here means a record left the
// table by some route this package does not know about, which is worth saying out loud rather than rounding.
func (a Accounting) Balanced() bool {
	return a.Delivered+a.Pending+a.Exhausted == a.Total
}

var outcomeMu sync.Mutex

// noteOutcome records what the last drain attempt learned. Guarded because Start's goroutine writes it while
// a health request reads it.
func (o *Outbox) noteOutcome(state DeliveryState, sent int, detail string) {
	outcomeMu.Lock()
	defer outcomeMu.Unlock()
	if len(detail) > 200 {
		detail = detail[:200]
	}
	o.lastOutcome = Outcome{At: time.Now().UTC(), State: state, Sent: sent, Detail: detail}
}

// LastOutcome reports what the last drain attempt learned, for the health surface.
func (o *Outbox) LastOutcome() Outcome {
	outcomeMu.Lock()
	defer outcomeMu.Unlock()
	if o.lastOutcome.State == "" {
		return Outcome{State: StateUnknown}
	}
	return o.lastOutcome
}

// classify turns a publish failure into the state it actually demonstrates.
//
// The distinction that matters is between "we could not ask" and "we asked and nobody answered". NATS
// reports the second as ErrNoResponders, and it is the exact signature of a connected appliance whose
// telemetry consumer is not running — which is a cloud-side problem, on a screen that used to blame the
// network.
func classifyPublishError(err error, connected bool) (DeliveryState, string) {
	switch {
	case err == nil:
		return StateIdle, ""
	case !connected:
		return StateTransportUnavailable, "no connection to the messaging transport"
	case errors.Is(err, nats.ErrNoResponders):
		return StateReceiverUnavailable, "no telemetry receiver is listening for this appliance"
	case strings.Contains(err.Error(), "cloud ingest status"):
		return StateReceiverRejected, err.Error()
	case errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded):
		return StateReceiverUnavailable, "the telemetry receiver did not answer in time"
	default:
		return StateTransportUnavailable, err.Error()
	}
}

// Account reads the queue through the definer function created by migration 0069, so the numbers an operator
// reads and the numbers a recovery is measured against come from one definition.
func (o *Outbox) Account(ctx context.Context) (Accounting, error) {
	var a Accounting
	err := o.DB.QueryRow(ctx, `SELECT delivered, pending, exhausted, total,
	                                  oldest_pending, newest_created, oldest_exhausted, bytes
	                             FROM public.sync_outbox_accounting()`).
		Scan(&a.Delivered, &a.Pending, &a.Exhausted, &a.Total,
			&a.OldestPending, &a.NewestCreated, &a.OldestExhausted, &a.Bytes)
	return a, err
}

// RecoveryResult is what one recovery batch moved.
type RecoveryResult struct {
	Recovered int   `json:"recovered"`
	SeqFrom   int64 `json:"seq_from,omitempty"`
	SeqTo     int64 `json:"seq_to,omitempty"`
	Remaining int64 `json:"remaining"`
}

// RecoverExhausted returns a bounded batch of exhausted records to the queue, oldest sequence first, and
// writes the append-only log row that says who asked and why. The operator identity and reason are required
// by the function, not by this wrapper — so there is no path that recovers silently.
func (o *Outbox) RecoverExhausted(ctx context.Context, operator, reason string, limit int) (RecoveryResult, error) {
	var r RecoveryResult
	var from, to *int64
	err := o.DB.QueryRow(ctx,
		`SELECT rows_recovered, seq_from, seq_to, exhausted_remaining
		   FROM public.sync_outbox_recover_exhausted($1, $2, $3)`,
		operator, reason, limit).Scan(&r.Recovered, &from, &to, &r.Remaining)
	if err != nil {
		return r, err
	}
	if from != nil {
		r.SeqFrom = *from
	}
	if to != nil {
		r.SeqTo = *to
	}
	return r, nil
}

// PruneDelivered removes delivered records older than the site's configured retention. It can reach nothing
// else: the function's WHERE clause names sent_at IS NOT NULL and takes no parameter that could widen it.
func (o *Outbox) PruneDelivered(ctx context.Context, days int) (int64, error) {
	var n int64
	err := o.DB.QueryRow(ctx, `SELECT public.sync_outbox_prune_delivered($1)`, days).Scan(&n)
	return n, err
}

// RetentionDays reads the site's configured retention, falling back to the approved default when the
// settings row has never been written or the scope is not yet known. An unreadable setting must not stop
// retention running — it runs at the default, which is the same thing the screen says it would do.
func (o *Outbox) RetentionDays(ctx context.Context, tenantID, siteID string) int {
	const approvedDefault = 30
	if tenantID == "" || siteID == "" {
		return approvedDefault
	}
	var days int
	if err := o.DB.QueryRow(ctx,
		`SELECT delivered_retention_days FROM iam_v2.cloud_sync_settings_get($1::uuid, $2::uuid)`,
		tenantID, siteID).Scan(&days); err != nil || days < 1 {
		return approvedDefault
	}
	return days
}
