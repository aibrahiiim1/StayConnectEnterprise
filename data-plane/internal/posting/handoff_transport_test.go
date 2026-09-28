package posting

// THE HAND-OFF KEEPS THE UNKNOWN CONTRACT (decision D45). The engine settles on exactly three facts: a matched
// PA, provably not sent (the only case it may queue again), or UNKNOWN (manual review, never resent). These
// pin that pmsd's answers map onto them conservatively: only an unreachable pmsd or pmsd's own "wrote nothing"
// is not-sent; everything else that is not a matched, well-formed PA for THIS P# is UNKNOWN.

import (
	"context"
	"errors"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/postinghandoff"
)

const hoBody = "PS|RN1421|G#5|TA1500|PT|SOOG|CTWIFI|P#42|WSOG|"

func hoWith(resp postinghandoff.Response, err error) (*handoffTransport, *postinghandoff.Request) {
	var seen postinghandoff.Request
	return &handoffTransport{send: func(_ context.Context, _ string, req postinghandoff.Request) (postinghandoff.Response, error) {
		seen = req
		return resp, err
	}}, &seen
}

func TestHandoffTransport_Classification(t *testing.T) {
	for name, tc := range map[string]struct {
		resp    postinghandoff.Response
		err     error
		notSent bool
		posted  bool
	}{
		"pmsd unreachable":          {err: postinghandoff.ErrNotSent, notSent: true},
		"pmsd wrote nothing":        {resp: postinghandoff.Response{Result: postinghandoff.NotTransmitted, Code: "LINK_RESYNCING"}, notSent: true},
		"pmsd says unknown":         {resp: postinghandoff.Response{Result: postinghandoff.Unknown, Code: "ANSWER_TIMEOUT"}},
		"duplicate is unknown":      {resp: postinghandoff.Response{Result: postinghandoff.Unknown, Code: "DUPLICATE_COMMAND"}},
		"answer lost on the socket": {err: errors.New("posting hand-off: no answer from pmsd")},
		"PA for another P#":         {resp: postinghandoff.Response{Result: postinghandoff.Answered, PABody: "PA|P#41|ASOK|"}},
		"PA outside the catalog":    {resp: postinghandoff.Response{Result: postinghandoff.Answered, PABody: "PA|P#42|ASXX|"}},
		"PA with no AS":             {resp: postinghandoff.Response{Result: postinghandoff.Answered, PABody: "PA|P#42|"}},
		"matched OK":                {resp: postinghandoff.Response{Result: postinghandoff.Answered, PABody: "PA|P#42|ASOK|"}, posted: true},
	} {
		t.Run(name, func(t *testing.T) {
			h, seen := hoWith(tc.resp, tc.err)
			pa, err := h.SendPS(context.Background(), "iface", 42, hoBody)
			if tc.posted {
				if err != nil || pa == nil || !pa.Posted() || pa.PNumber != 42 {
					t.Fatalf("want a matched OK, got %+v %v", pa, err)
				}
			} else {
				if pa != nil || err == nil {
					t.Fatalf("want an error, got %+v", pa)
				}
				if NotTransmitted(err) != tc.notSent {
					t.Fatalf("NotTransmitted=%v, want %v (err %v)", NotTransmitted(err), tc.notSent, err)
				}
				if !tc.notSent && !errors.Is(err, ErrTransmittedNoAnswer) {
					t.Fatal("an inconclusive hand-off must be UNKNOWN")
				}
			}
			// The command carries exactly the engine's bytes and their hash.
			if seen.Body != hoBody || seen.BodySHA256 != postinghandoff.BodyHash(hoBody) || seen.PNumber != 42 {
				t.Fatalf("the command must be the exact authorised bytes: %+v", *seen)
			}
		})
	}
}

func TestHandoffTransport_RefusesAMalformedCommandUnsent(t *testing.T) {
	called := false
	h := &handoffTransport{send: func(context.Context, string, postinghandoff.Request) (postinghandoff.Response, error) {
		called = true
		return postinghandoff.Response{}, nil
	}}
	_, err := h.SendPS(context.Background(), "iface", 43, hoBody) // body carries P#42
	if !NotTransmitted(err) || called {
		t.Fatalf("a command whose body disagrees with its P# never leaves: %v called=%v", err, called)
	}
}

func TestProductionTransport_IsTheHandoffOnlyWhenTransmitting(t *testing.T) {
	if tr, err := ProductionTransportFor(Config{MasterEnabled: true, OutboxEnabled: true}); err != nil || tr != nil {
		t.Fatalf("DARK has no transport at all: %v %v", tr, err)
	}
	tr, err := ProductionTransportFor(Config{MasterEnabled: true, OutboxEnabled: true, TransmitEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.(*handoffTransport); !ok {
		t.Fatalf("the production transport is the pmsd hand-off, got %T", tr)
	}
}
