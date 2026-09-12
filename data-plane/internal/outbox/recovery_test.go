package outbox

// "NOT DRAINING" IS FOUR DIFFERENT PROBLEMS WITH FOUR DIFFERENT OWNERS.
//
// The product used to infer the problem from the size of the queue and print: "a backlog this size means the
// queue is not draining — check Cloud connection." On the appliance this was written against, the network was
// fine, the appliance held an established mutually-authenticated connection to the cloud, and the reason
// nothing drained was that nothing at the far end was listening on its telemetry subject. An operator
// following that sentence would have spent the afternoon on the wrong system.
//
// These tests pin the mapping from what actually happened to what the operator is told.

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
)

func TestClassifyPublishError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		connected bool
		want      DeliveryState
		why       string
	}{
		{
			name: "no connection at all is a transport problem",
			err:  errors.New("connection closed"), connected: false,
			want: StateTransportUnavailable,
			why:  "nothing about the far end is known, because nothing was asked",
		},
		{
			name: "connected with nobody listening is a RECEIVER problem",
			// THE ONE THIS EXISTS FOR. ErrNoResponders is the exact signature of a healthy appliance whose
			// telemetry consumer is not running at the far end. Reporting it as a network fault points the
			// hotel at its own network, which is working.
			err: nats.ErrNoResponders, connected: true,
			want: StateReceiverUnavailable,
			why:  "a connected appliance with no responder is a cloud-side problem, not a hotel network one",
		},
		{
			name: "an answer that refuses is not a retryable failure",
			err:  errors.New("cloud ingest status 404"), connected: true,
			want: StateReceiverRejected,
			why:  "404 means the far end does not recognise this appliance; retrying cannot fix that",
		},
		{
			name: "a timeout is the receiver not answering, not the link being down",
			err:  nats.ErrTimeout, connected: true,
			want: StateReceiverUnavailable,
			why:  "the request left the appliance, so the link worked",
		},
		{
			name: "a deadline exceeded is treated the same way",
			err:  context.DeadlineExceeded, connected: true,
			want: StateReceiverUnavailable,
			why:  "same evidence, different error type",
		},
		{
			name: "no error is the idle state",
			err:  nil, connected: true,
			want: StateIdle,
			why:  "nothing failed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, detail := classifyPublishError(c.err, c.connected)
			if got != c.want {
				t.Errorf("classify = %s, want %s — %s", got, c.want, c.why)
			}
			if c.want != StateIdle && detail == "" {
				t.Error("a failure state must carry a detail an operator can read")
			}
		})
	}
}

// A wrapped error must still be recognised: the publish path wraps, and a mapping that only matched the bare
// sentinel would silently fall through to "transport unavailable" — the wrong owner again.
func TestClassifyPublishError_UnwrapsWrappedSentinels(t *testing.T) {
	wrapped := errors.Join(errors.New("publishing seq 4211"), nats.ErrNoResponders)
	if got, _ := classifyPublishError(wrapped, true); got != StateReceiverUnavailable {
		t.Errorf("a wrapped no-responders error classified as %s", got)
	}
}

// THE THREE BUCKETS MUST ACCOUNT FOR EVERY RECORD.
//
// This is what makes "the backlog has been recovered" checkable rather than asserted. A record that left the
// table by some route this package does not know about shows up as the sum not adding up, and the screen says
// so instead of quietly displaying a smaller, reassuring number.
func TestAccountingBalanced(t *testing.T) {
	if !(Accounting{Delivered: 10, Pending: 5, Exhausted: 2, Total: 17}).Balanced() {
		t.Error("a consistent accounting was reported as unbalanced")
	}
	if (Accounting{Delivered: 10, Pending: 5, Exhausted: 2, Total: 20}).Balanced() {
		t.Error("three records unaccounted for were reported as balanced")
	}
	if !(Accounting{}).Balanced() {
		t.Error("an empty queue must balance")
	}
}

// The recorded outcome is what the health surface reads. An Outbox that has never tried must say so rather
// than defaulting to a state that reads as a verdict.
func TestLastOutcome_UnknownBeforeAnyAttempt(t *testing.T) {
	o := &Outbox{}
	if got := o.LastOutcome().State; got != StateUnknown {
		t.Errorf("a fresh outbox reported %s; it has not tried anything yet", got)
	}
	o.noteOutcome(StateDraining, 100, "")
	if got := o.LastOutcome(); got.State != StateDraining || got.Sent != 100 {
		t.Errorf("outcome not recorded: %+v", got)
	}
}

// Details are bounded. They are rendered on an operator screen and come from an error string, which is the
// one place an unbounded remote value could reach the UI.
func TestNoteOutcome_BoundsTheDetail(t *testing.T) {
	o := &Outbox{}
	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'x'
	}
	o.noteOutcome(StateReceiverRejected, 0, string(long))
	if n := len(o.LastOutcome().Detail); n > 200 {
		t.Errorf("detail was %d bytes; it is shown on a screen and must be bounded", n)
	}
}
