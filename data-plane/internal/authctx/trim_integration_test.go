//go:build integration

package authctx

import (
	"context"
	"testing"
)

// P2 L9: padded ids are validated and bound trimmed, so they work (or are refused uniformly), never fail with a raw cast error.
func TestIntegration_PaddedIdsAreTrimmedForBinding(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := NewStore(p)
	g := grant(f, 600)
	g.Stay, g.Device = " "+g.Stay+" ", g.Device+"\n"
	id, err := s.IssuePMS(context.Background(), g)
	if err != nil {
		t.Fatalf("issue with padded ids: %v", err)
	}
	pr := pres(f)
	pr.Device = " " + pr.Device
	if _, err := s.Consume(context.Background(), " "+id+" ", pr); err != nil {
		t.Fatalf("consume with padded ids: %v", err)
	}
}
