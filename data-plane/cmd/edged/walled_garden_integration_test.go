//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
)

// N-7 against the real schema (migration 0125): the policy, its audited change, rule create/edit refusals.
func TestIntegration_WalledGarden_PolicyAndRuleChecks(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()

	st, pol := f.do(t, "GET", "/walled-garden/policy", nil)
	if st != 200 || pol["widest_prefix_len"].(float64) != 24 || pol["config_version"].(float64) != 0 {
		t.Fatalf("default policy = %d %v", st, pol)
	}
	for _, body := range []map[string]any{
		{"kind": "cidr", "value": "0.0.0.0/0"},
		{"kind": "cidr", "value": "52.84.0.0/16"},
		{"kind": "ip", "value": "10.20.0.5"},
		{"kind": "ip", "value": "169.254.1.1"},
	} {
		if st, b := f.do(t, "POST", "/walled-garden", body); st != 400 {
			t.Errorf("%v accepted: %d %v", body, st, b)
		}
	}
	st, created := f.do(t, "POST", "/walled-garden", map[string]any{"kind": "cidr", "value": "52.84.12.0/24", "ports": []int{443}})
	if st != 201 {
		t.Fatalf("a /24 rule = %d %v", st, created)
	}
	id := created["id"].(string)

	// edit: the same checks, and the audit carries old and new ports
	if st, b := f.do(t, "PUT", "/walled-garden/"+id, map[string]any{"kind": "cidr", "value": "0.0.0.0/0"}); st != 400 {
		t.Fatalf("an edit to 0.0.0.0/0 = %d %v", st, b)
	}
	if st, b := f.do(t, "PUT", "/walled-garden/"+id, map[string]any{"kind": "ip", "value": "52.84.12.9", "ports": []int{443, 8443}}); st != 200 {
		t.Fatalf("a valid edit = %d %v", st, b)
	}
	var detail string
	if err := f.pool.QueryRow(ctx, `SELECT payload::text FROM audit_log WHERE action='walled_garden.updated'
	    AND target_id=$1 ORDER BY ts DESC LIMIT 1`, id).Scan(&detail); err != nil {
		t.Fatalf("no audit row for the edit: %v", err)
	} else if !strings.Contains(detail, "8443") || !strings.Contains(detail, "52.84.12.0/24") {
		t.Fatalf("the edit's audit row lacks the ports or the previous value: %s", detail)
	}

	// the policy change needs the password and a reason, and leaves a change row
	if st, _ := f.do(t, "PUT", "/walled-garden/policy", map[string]any{"widest_prefix_len": 16, "reason": "CDN block", "password": "wrong"}); st != 401 {
		t.Fatalf("policy change with a wrong password = %d", st)
	}
	if st, b := f.do(t, "PUT", "/walled-garden/policy", map[string]any{"widest_prefix_len": 8, "reason": "too wide", "password": f.password}); st != 400 {
		t.Fatalf("a /8 policy = %d %v", st, b)
	}
	if st, b := f.do(t, "PUT", "/walled-garden/policy", map[string]any{"widest_prefix_len": 16, "reason": "CDN block", "password": f.password}); st != 200 || b["widest_prefix_len"].(float64) != 16 {
		t.Fatalf("policy change = %d %v", st, b)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.walled_garden_settings_changes
	    WHERE tenant_id=$1 AND site_id=$2 AND new_widest_prefix_len=16 AND old_widest_prefix_len IS NULL`, f.tenant, f.site).Scan(&n); err != nil || n != 1 {
		t.Fatalf("change rows = %d %v", n, err)
	}
	if st, b := f.do(t, "POST", "/walled-garden", map[string]any{"kind": "cidr", "value": "52.84.0.0/16"}); st != 201 {
		t.Fatalf("a /16 under a /16 policy = %d %v", st, b)
	}
}
