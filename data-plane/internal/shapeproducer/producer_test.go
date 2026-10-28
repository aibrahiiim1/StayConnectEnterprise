package shapeproducer

import (
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// The producer half on its own (audit Phase 6 CC4): what acctd builds is a plan netd's validator accepts, every
// session bridge and the fallback are declared managed, and ended sessions travel as not entitled.
func TestBuildEnvelopeIsAPlanTheApplierAccepts(t *testing.T) {
	now := time.Now()
	plan := enforce.Plan{
		Shape: []enforce.SessionShape{{SessionID: "live", DeviceID: "d1", IP: "10.20.0.5", Bridge: "br-g20", DownKbps: 8000, UpKbps: 2000}},
		Tear:  []enforce.SessionShape{{SessionID: "ended", DeviceID: "d2", IP: "10.20.0.6", EndReason: "EXPIRED"}},
	}
	scope := Scope{TenantID: "t1", SiteID: "s1", ApplianceID: "a1", AssignmentGen: 4}
	env := BuildEnvelope(plan, scope, 7, 1, []string{"br-empty"}, "br-lan", now, 90*time.Second)

	if r, ok := shapeplan.Validate(env, shapeplan.Scope{TenantID: "t1", SiteID: "s1", ApplianceID: "a1", AssignGen: 4},
		shapeplan.Accepted{}, false, now); !ok {
		t.Fatalf("the applier refuses what the producer builds: %s", r)
	}
	declared := map[string]bool{}
	for _, b := range env.ManagedBridges {
		declared[b] = true
	}
	for _, b := range []string{"br-g20", "br-lan", "br-empty"} {
		if !declared[b] {
			t.Errorf("bridge %s is not declared managed: %v", b, env.ManagedBridges)
		}
	}
	for _, s := range env.Sessions {
		switch s.SessionID {
		case "live":
			if !s.Entitled || s.DownKbps != 8000 {
				t.Errorf("live session: %+v", s)
			}
		case "ended":
			if s.Entitled || s.Bridge != "br-lan" {
				t.Errorf("an ended session must be not entitled, on the fallback bridge: %+v", s)
			}
		}
	}
	if !env.ExpiresAt.Equal(now.UTC().Add(90 * time.Second)) {
		t.Errorf("validity: %v", env.ExpiresAt)
	}
}
