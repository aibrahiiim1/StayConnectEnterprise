package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeLegacySet models the legacy auth_ipv4 set: a separate set the Phase-3 gate cannot reach.
type fakeLegacySet struct {
	elems   map[string]bool
	drains  int
	failErr error
}

func (l *fakeLegacySet) Drain(ctx context.Context) (int, error) {
	l.drains++
	if l.failErr != nil {
		return 0, l.failErr
	}
	n := len(l.elems)
	l.elems = map[string]bool{}
	return n, nil
}

// B-15: the legacy set still GRANTS (the forward chain accepts it) and nothing writes or reconciles it. While
// netd owns Phase-3 enforcement it is kept empty, counted as strays, and the Phase-3 gate is still never used
// to do it.
func TestSystem_LegacySetIsDrainedToEmpty(t *testing.T) {
	_, g, _, _, _, p := sysSetup()
	legacy := &fakeLegacySet{elems: map[string]bool{"br-guest|10.0.0.200": true, "br-guest|10.0.0.201": true}}
	p.legacy = legacy

	res, _ := p.submit(context.Background(), envelopeOn([]string{"br-guest"}, 1), time.Now())
	if len(legacy.elems) != 0 {
		t.Fatalf("legacy authorizations survived reconciliation: %v", legacy.elems)
	}
	if res.StraysRemoved < 2 {
		t.Fatalf("the drained legacy elements were not reported: %+v", res)
	}
	for _, c := range g.callLog() {
		if contains(c, "10.0.0.200") || contains(c, "10.0.0.201") {
			t.Fatalf("the Phase-3 gate was used on a legacy address: %s", c)
		}
	}
	// A second pass finds nothing and reports nothing extra.
	res2, _ := p.submit(context.Background(), envelopeOn([]string{"br-guest"}, 2), time.Now())
	if res2.StraysRemoved != 0 || res2.Degraded {
		t.Fatalf("an empty legacy set produced %+v", res2)
	}
}

// A legacy set that cannot be read is reported as a problem, not hidden.
func TestSystem_LegacySetDrainFailureIsReported(t *testing.T) {
	_, _, _, _, _, p := sysSetup()
	p.legacy = &fakeLegacySet{failErr: errors.New("no such set")}
	res, _ := p.submit(context.Background(), envelopeOn([]string{"br-guest"}, 1), time.Now())
	if !res.Degraded || res.Failed == 0 {
		t.Fatalf("a failed legacy drain was not reported: %+v", res)
	}
}
