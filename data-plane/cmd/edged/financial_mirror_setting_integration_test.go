//go:build integration && phase4

package main

import (
	"context"
	"testing"
)

// THE FINANCIAL MIRROR MAXIMUM AGE (D48) IS AN AUDITED OPERATIONAL SETTING: the list shows the 4-hour default,
// a change needs a site administrator's step-up, and every change is logged with its author.
func TestIntegrationFinancialMirrorSetting_DefaultStepUpAndAudit(t *testing.T) {
	f := newAPI(t, "site_admin")
	ctx := context.Background()
	var iface string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind,display_label)
		VALUES ($1,$2,'protel-fias','Protel') RETURNING id::text`, f.tenant, f.site).Scan(&iface); err != nil {
		t.Fatalf("seed interface: %v", err)
	}
	mirror := func() map[string]any {
		code, body := f.do(t, "GET", "/pms-financial-onboarding", nil)
		if code != 200 {
			t.Fatalf("list: %d %v", code, body)
		}
		for _, x := range body["interfaces"].([]any) {
			m := x.(map[string]any)
			if m["pms_interface_id"] == iface {
				return m["financial_mirror"].(map[string]any)
			}
		}
		t.Fatal("interface not listed")
		return nil
	}
	if m := mirror(); m["max_age_seconds"].(float64) != 14400 || m["is_default"] != true {
		t.Fatalf("default: %v", m)
	}
	path := "/pms-financial-onboarding/" + iface + "/financial-mirror-max-age"
	if code, _ := f.do(t, "POST", path, map[string]any{"max_age_seconds": 7200, "reason": "measured quiet periods"}); code != 401 {
		t.Fatalf("a change without step-up was accepted: %d", code)
	}
	if code, body := f.do(t, "POST", path, map[string]any{"max_age_seconds": 600, "reason": "too short", "password": f.password}); code != 400 {
		t.Fatalf("an out-of-range value was accepted: %d %v", code, body)
	}
	if code, body := f.do(t, "POST", path, map[string]any{"max_age_seconds": 7200, "reason": "measured quiet periods", "password": f.password}); code != 200 {
		t.Fatalf("a valid change was refused: %d %v", code, body)
	}
	if m := mirror(); m["max_age_seconds"].(float64) != 7200 || m["is_default"] != false {
		t.Fatalf("after the change: %v", m)
	}
	var author string
	if err := f.pool.QueryRow(ctx, `SELECT changed_by::text FROM iam_v2.pms_interface_financial_setting_changes WHERE pms_interface_id=$1`, iface).Scan(&author); err != nil || author != f.operator {
		t.Fatalf("the change log names the session operator: %q %v", author, err)
	}
}
