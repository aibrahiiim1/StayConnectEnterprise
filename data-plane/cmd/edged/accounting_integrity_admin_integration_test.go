//go:build integration

package main

import "testing"

// An operator can see whether any usage figure at the site is an upper bound (accounting_integrity_summary, 0181) without
// database access: exact when nothing was recorded, one row per kind when something was, counts and bytes only.
func TestIntegration_AccountingIntegrity_TheOperatorViewIsReadOnlyAndExact(t *testing.T) {
	f := newAPI(t)
	st, body := f.do(t, "GET", "/accounting-integrity/", nil)
	if st != 200 || body["exact"] != true {
		t.Fatalf("a site with no integrity event must read exact: %d %v", st, body)
	}
	if kinds, _ := body["kinds"].([]any); len(kinds) != 0 {
		t.Fatalf("kinds = %v, want none", kinds)
	}

	// One recorded case of the conservative direction (the superuser fixture writes the event table the way the 0181 writers do).
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO iam_v2.accounting_integrity_events (tenant_id, site_id, kind, bytes, detail)
	    VALUES ($1::uuid, $2::uuid, 'RETROACTIVE_BINDING_PAST_RAW_RETENTION', 4096, '{}'::jsonb)`, f.tenant, f.site); err != nil {
		t.Fatalf("seed an integrity event: %v", err)
	}
	st, body = f.do(t, "GET", "/accounting-integrity/", nil)
	kinds, _ := body["kinds"].([]any)
	if st != 200 || body["exact"] != false || len(kinds) != 1 {
		t.Fatalf("after one event: %d %v", st, body)
	}
	k, _ := kinds[0].(map[string]any)
	if k["kind"] != "RETROACTIVE_BINDING_PAST_RAW_RETENTION" || k["events"] != float64(1) || k["bytes"] != float64(4096) {
		t.Fatalf("kind row = %v", k)
	}
	for key := range k {
		switch key {
		case "kind", "events", "entitlements", "bytes", "last_at":
		default:
			t.Fatalf("the view exposes %q: counts and bytes only", key)
		}
	}

	// Read-only.
	if st, _ := f.do(t, "PUT", "/accounting-integrity/", map[string]any{"exact": true}); st != 405 && st != 404 {
		t.Fatalf("the view accepted a write: %d", st)
	}
	// Role scoping.
	other := newAPIIn(t, f.tenant, "hotel_it_manager")
	if st, _ := other.do(t, "GET", "/accounting-integrity/", nil); st != 200 {
		t.Fatalf("a role that can see reports cannot see the integrity view: %d", st)
	}
}
