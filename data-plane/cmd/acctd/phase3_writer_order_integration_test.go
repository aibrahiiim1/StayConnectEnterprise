//go:build integration

package main

import (
	"context"
	"testing"
	"time"
)

// Ingest locks a session then its entitlement and the expiry sweep the reverse, so the two must never run at once (40P01 on the test appliance).
func TestIntegration_Acct_ExpirySweepNeverOverlapsIngest(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 2000)

	f.p3.wmu.Lock()
	swept, ingested := make(chan struct{}), make(chan struct{})
	go func() { f.p3.enforceExpiries(ctx); close(swept) }()
	go func() { f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); close(ingested) }()
	select {
	case <-swept:
		t.Fatal("the expiry sweep ran while another acctd writer held the database")
	case <-ingested:
		t.Fatal("ingest ran while another acctd writer held the database")
	case <-time.After(300 * time.Millisecond):
	}
	f.p3.wmu.Unlock()
	for name, ch := range map[string]chan struct{}{"expiry sweep": swept, "ingest": ingested} {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("the %s did not resume once the other writer finished", name)
		}
	}
	if _, _, _, class := f.checkpoint(t, sess); class != "BASELINED" {
		t.Fatalf("ingest after the wait recorded %q, want BASELINED", class)
	}
}
