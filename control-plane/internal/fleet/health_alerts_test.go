package fleet

import "testing"

func condMap(conds []condition) map[string]condition {
	m := map[string]condition{}
	for _, c := range conds {
		m[c.key()] = c
	}
	return m
}

func TestDeriveConditions(t *testing.T) {
	payload := map[string]any{
		"services": []any{
			map[string]any{"service": "scd", "state": "healthy"},
			map[string]any{"service": "edged", "state": "degraded", "degraded_dependency": "postgres",
				"last_failure_reason": "site DB unreachable", "backoff_level": float64(0), "restart_count": float64(2)},
			map[string]any{"service": "netd", "state": "crash_loop", "backoff_level": float64(4), "restart_count": float64(9)},
			map[string]any{"service": "kea", "state": "failed"},
		},
		"boot": map[string]any{"converged": false, "alert_open": true, "pending": []any{"netd"}},
	}
	got := condMap(deriveConditions(payload))

	// healthy service raises nothing
	if _, ok := got["service_crash_loop|scd"]; ok {
		t.Fatal("healthy scd should not raise")
	}
	// degraded edged -> active_but_unhealthy + dependency_unavailable
	if _, ok := got["service_active_but_unhealthy|edged"]; !ok {
		t.Fatal("degraded edged should raise active_but_unhealthy")
	}
	if _, ok := got["dependency_unavailable|edged->postgres"]; !ok {
		t.Fatal("edged dependency postgres should raise dependency_unavailable")
	}
	// crash_loop netd
	if _, ok := got["service_crash_loop|netd"]; !ok {
		t.Fatal("crash_loop netd should raise service_crash_loop")
	}
	// failed kea -> unavailable
	if _, ok := got["service_unavailable|kea"]; !ok {
		t.Fatal("failed kea should raise service_unavailable")
	}
	// boot not converged
	if _, ok := got["appliance_boot_not_converged|appliance"]; !ok {
		t.Fatal("boot alert_open should raise appliance_boot_not_converged")
	}
}

func TestDeriveConditionsAllHealthy(t *testing.T) {
	payload := map[string]any{
		"services": []any{
			map[string]any{"service": "scd", "state": "healthy"},
			map[string]any{"service": "edged", "state": "healthy"},
		},
		"boot": map[string]any{"converged": true, "alert_open": false},
	}
	if n := len(deriveConditions(payload)); n != 0 {
		t.Fatalf("all-healthy payload should derive 0 conditions, got %d", n)
	}
}
