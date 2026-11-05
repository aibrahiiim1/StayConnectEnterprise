//go:build integration

package enforce

import (
	"context"
	"testing"
	"time"
)

// A live session whose package has ended is torn down AS the end of its package, so whichever writer ends it first (netd from
// the plan, or the expiry writer) records ENTITLEMENT_ENDED; netd's generic ENFORCEMENT_TORN_DOWN was what won the race before.
func TestIntegration_PlanTearsAnEndedPackagesSessionAsEntitlementEnded(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p, 5000, 2000, 0)
	past := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	_, ended := grant(t, p, f, &past, time.Now().Add(-2*time.Hour))
	plan, err := New(p).PlanForSite(context.Background(), f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range plan.Tear {
		if s.SessionID == ended {
			found = true
			if s.EndReason != "ENTITLEMENT_ENDED" {
				t.Fatalf("the ended package's session is torn down with reason %q, want ENTITLEMENT_ENDED", s.EndReason)
			}
		}
	}
	if !found {
		t.Fatalf("the ended package's session is not in the teardown list: %+v", plan.Tear)
	}
	for _, s := range plan.Shape {
		if s.SessionID == ended {
			t.Fatal("a session whose package has ended is still shaped")
		}
	}
}

// A live package's session carries no teardown reason.
func TestIntegration_PlanGivesALivePackagesSessionNoTearReason(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p, 5000, 2000, 0)
	future := time.Now().Add(time.Hour)
	_, live := grant(t, p, f, &future, time.Now().Add(-time.Hour))
	plan, err := New(p).PlanForSite(context.Background(), f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Shape {
		if s.SessionID == live && s.EndReason != "" {
			t.Fatalf("a live session carries the teardown reason %q", s.EndReason)
		}
	}
}
