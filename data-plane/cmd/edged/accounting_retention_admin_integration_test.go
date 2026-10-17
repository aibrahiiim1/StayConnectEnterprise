//go:build integration

package main

import "testing"

// D-H: accounting-detail retention is an audited §0C setting. 365 days by default (0131, GDPR C2); bounded; a reason and the
// password to change it; logged; site administrators write, the roles that can see backups can read.
func TestIntegration_AccountingRetention_TheSetting(t *testing.T) {
	f := newAPI(t)
	st, body := f.do(t, "GET", "/accounting-retention/", nil)
	set, _ := body["settings"].(map[string]any)
	if st != 200 || set == nil || set["detail_retention_days"] != float64(365) || body["config_version"] != float64(0) {
		t.Fatalf("default = %d %v; it must be 365 days and never changed", st, body)
	}
	for _, bad := range []map[string]any{
		{"detail_retention_days": 7, "reason": "too short", "password": f.password},
		{"detail_retention_days": 4000, "reason": "too long", "password": f.password},
		{"reason": "neither", "password": f.password},
		{"detail_retention_days": 90, "unlimited": true, "reason": "both", "password": f.password},
		{"detail_retention_days": 90, "reason": "", "password": f.password},
	} {
		if st, _ := f.do(t, "PUT", "/accounting-retention/", bad); st != 400 {
			t.Fatalf("accepted %v (%d)", bad, st)
		}
	}
	if st, _ := f.do(t, "PUT", "/accounting-retention/", map[string]any{"detail_retention_days": 90, "reason": "a quarter", "password": "wrong"}); st != 401 {
		t.Fatal("a wrong step-up password changed the retention")
	}
	st, body = f.do(t, "PUT", "/accounting-retention/", map[string]any{"detail_retention_days": 90, "reason": "keep a quarter", "password": f.password})
	if st != 200 || body["config_version"] != float64(1) {
		t.Fatalf("save = %d %v", st, body)
	}
	st, body = f.do(t, "PUT", "/accounting-retention/", map[string]any{"unlimited": true, "reason": "keep everything again", "password": f.password})
	if st != 200 || body["config_version"] != float64(2) {
		t.Fatalf("back to unlimited = %d %v", st, body)
	}
	st, body = f.do(t, "GET", "/accounting-retention/", nil)
	ch, _ := body["changes"].([]any)
	if set, _ := body["settings"].(map[string]any); st != 200 || set["detail_retention_days"] != nil || len(ch) != 2 {
		t.Fatalf("after two changes = %v", body)
	}
	var audited int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM public.audit_log WHERE action='accounting_retention.changed' AND target_id=$1`, f.site).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("audit rows %d (%v), want 2", audited, err)
	}
	it := newAPIIn(t, f.tenant, "hotel_it_manager")
	if st, _ := it.do(t, "GET", "/accounting-retention/", nil); st != 200 {
		t.Fatalf("a role that can see backups cannot see the retention: %d", st)
	}
	if st, _ := it.do(t, "PUT", "/accounting-retention/", map[string]any{"detail_retention_days": 30, "reason": "it try", "password": it.password}); st != 403 {
		t.Fatalf("a role without backups write changed the retention: %d", st)
	}
	vouchers := newAPIIn(t, f.tenant, "voucher_operator")
	if st, _ := vouchers.do(t, "GET", "/accounting-retention/", nil); st != 403 {
		t.Fatalf("a role with no backups access read the retention: %d", st)
	}
}
