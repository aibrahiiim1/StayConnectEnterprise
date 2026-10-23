package throttle

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestGlobalRuleIsNotChargedForAClientItsOwnRulesRefuse(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	clk := time.Date(2026, 10, 16, 9, 0, 5, 0, time.UTC)
	s.SetClock(func() time.Time { return clk })
	ctx := context.Background()
	global := Rule{Kind: ScopeEndpoint, Value: "auth", Method: MethodAny, Limit: 50, Global: true}
	attacker := Rule{Kind: ScopeDevice, Value: "02:00:00:00:00:aa", Method: "account", Limit: 5, Block: 15 * time.Minute}

	for i := 0; i < 500; i++ {
		if _, err := s.Allow(ctx, global, attacker); err != nil {
			t.Fatal(err)
		}
	}
	other := Rule{Kind: ScopeDevice, Value: "02:00:00:00:00:bb", Method: "account", Limit: 5, Block: 15 * time.Minute}
	d, err := s.Allow(ctx, global, other)
	if err != nil || !d.Allowed {
		t.Fatalf("another client was refused after one client's flood: %+v err=%v", d, err)
	}
	var charged int
	if err := db.QueryRow(ctx, `SELECT COALESCE(sum(attempt_count),0) FROM public.auth_throttle_buckets
	     WHERE scope_kind='endpoint' AND scope_key=$1`, s.scopeKey(ScopeEndpoint, "auth")).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	if charged != 5+1 {
		t.Fatalf("global charged %d times, want the attacker's 5 allowed attempts plus the other client's 1", charged)
	}
}

func TestGlobalRuleStillRefusesAnApplianceWideFlood(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	clk := time.Date(2026, 10, 16, 9, 0, 5, 0, time.UTC)
	s.SetClock(func() time.Time { return clk })
	ctx := context.Background()
	global := Rule{Kind: ScopeEndpoint, Value: "auth", Method: MethodAny, Limit: 20, Global: true}
	allowed := 0
	for c := 0; c < 30; c++ {
		d, err := s.Allow(ctx, global, Rule{Kind: ScopeIP, Value: fmt.Sprintf("10.30.0.%d", c+1), Method: "account", Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if d.Allowed {
			allowed++
		} else if d.Reason != ScopeEndpoint {
			t.Fatalf("refused for %q, want the global damper", d.Reason)
		}
	}
	if allowed != 20 {
		t.Fatalf("%d of 30 distinct clients allowed, want the global limit 20", allowed)
	}
}

func TestNonGlobalEndpointRuleKeepsItsOldBehaviour(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	clk := time.Date(2026, 10, 16, 9, 0, 5, 0, time.UTC)
	s.SetClock(func() time.Time { return clk })
	ctx := context.Background()
	ep := Rule{Kind: ScopeEndpoint, Value: "pin", Method: "post_stay_pin", Limit: 100}
	dev := Rule{Kind: ScopeDevice, Value: "02:00:00:00:00:cc", Method: "post_stay_pin", Limit: 1}
	for i := 0; i < 4; i++ {
		_, _ = s.Allow(ctx, ep, dev)
	}
	var charged int
	if err := db.QueryRow(ctx, `SELECT COALESCE(sum(attempt_count),0) FROM public.auth_throttle_buckets
	     WHERE scope_kind='endpoint' AND scope_key=$1`, s.scopeKey(ScopeEndpoint, "pin")).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	if charged != 4 {
		t.Fatalf("non-global endpoint rule charged %d, want 4", charged)
	}
}
