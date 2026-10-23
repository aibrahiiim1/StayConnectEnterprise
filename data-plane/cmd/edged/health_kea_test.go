package main

import "testing"

// A factory-clean appliance has no LAN bridge, so Kea is deliberately stopped; waiting must be benign, and apply only while the prerequisite is absent.
func TestKeaWaitingIsNotAFailure(t *testing.T) {
	t.Run("waiting does not block boot convergence", func(t *testing.T) {
		if blocksConvergence(stWaiting) {
			t.Fatal("a service waiting for its prerequisite must not hold boot convergence open")
		}
		if blocksConvergence(stHealthy) {
			t.Fatal("healthy must not block convergence")
		}
		for _, st := range []string{stDegraded, stFailed, stCrashLoop, stStarting, stRecovering, stUnknown} {
			if !blocksConvergence(st) {
				t.Fatalf("%s must still block boot convergence", st)
			}
		}
	})

	t.Run("waiting does not degrade the appliance", func(t *testing.T) {
		all := []serviceHealth{
			{Service: "scd", State: stHealthy, Critical: true},
			{Service: "kea", State: stWaiting, Critical: true},
		}
		overall, counts := overallHealth(all)
		if overall != "healthy" {
			t.Fatalf("an appliance whose only non-healthy service is waiting must be healthy, got %q", overall)
		}
		if counts[stWaiting] != 1 {
			t.Fatalf("waiting must be counted, got %d", counts[stWaiting])
		}
		all[0].State = stFailed
		if overall, _ = overallHealth(all); overall == "healthy" {
			t.Fatal("a failed critical service must not be masked by another service waiting")
		}
	})
}

func TestKeaApplicabilityFailsTowardsReporting(t *testing.T) {
	cases := []struct {
		name       string
		view       keaView
		reachable  bool
		applicable bool
	}{
		{
			name:       "no guest network configured yet — not applicable",
			view:       keaView{KeaConfigured: false, KeaDetail: "waiting for client networking"},
			reachable:  true,
			applicable: false,
		},
		{
			name:       "guest network configured — checked normally, even when unhealthy",
			view:       keaView{KeaConfigured: true, KeaHealthy: false},
			reachable:  true,
			applicable: true,
		},
		{
			name:       "guest network configured and healthy",
			view:       keaView{KeaConfigured: true, KeaHealthy: true},
			reachable:  true,
			applicable: true,
		},
		{
			// Safety case: an unreachable netd must not suppress a broken Kea.
			name:       "netd unreachable — assume applicable, never suppress a real failure",
			view:       keaView{},
			reachable:  false,
			applicable: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := keaApplicableFrom(c.view, c.reachable)
			if got != c.applicable {
				t.Fatalf("applicable = %v, want %v", got, c.applicable)
			}
			if !got && reason == "" {
				t.Fatal("a service reported as waiting must say what it is waiting for")
			}
		})
	}
}
