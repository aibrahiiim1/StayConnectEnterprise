package api

import (
	"net/http"
	"testing"
)

// A waiting appliance's identity key can be replaced only from the same hardware; a refusal raises one alert
// (repeats fold into it) and every attempt is audited.
func TestWaitingIdentityReplacementRequiresSameHardware(t *testing.T) {
	f := newFixture(t)
	first := newBox("SC-TEST-0001", "hwf-genuine")
	rec := f.register(first)
	if rec.Code != http.StatusOK {
		t.Fatalf("first registration: %d %s", rec.Code, rec.Body.String())
	}
	id := decode(t, rec)["appliance_id"].(string)

	for i := 0; i < 3; i++ {
		rec = f.register(newBox("SC-TEST-0001", "hwf-attacker"))
		if rec.Code != http.StatusForbidden || decode(t, rec)["error"] != "identity_replacement_refused" {
			t.Fatalf("replacement from other hardware must be refused, got %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec = f.register(newBox("SC-TEST-0001", "")); rec.Code != http.StatusForbidden {
		t.Fatalf("replacement without a fingerprint must be refused, got %d", rec.Code)
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND public_key=$2`, id, first.pubB64); n != 1 {
		t.Fatal("the waiting appliance's key changed after a refused replacement")
	}
	if n := f.count(`SELECT count(*) FROM appliance_security_alerts WHERE appliance_id=$1 AND kind='identity_replacement_refused'`, id); n != 1 {
		t.Fatalf("repeated refusals must fold into ONE open alert, got %d", n)
	}
	if n := f.count(`SELECT (detail->>'repeats')::int FROM appliance_security_alerts WHERE appliance_id=$1 AND kind='identity_replacement_refused'`, id); n != 4 {
		t.Fatalf("the alert must count every attempt (4), got %d", n)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='appliance.identity_replacement_refused' AND target_id=$1`, id); n != 4 {
		t.Fatalf("every refused replacement must be audited, got %d", n)
	}

	reset := newBox("SC-TEST-0001", "hwf-genuine")
	rec = f.register(reset)
	if rec.Code != http.StatusOK || decode(t, rec)["appliance_id"] != id {
		t.Fatalf("same-hardware replacement must keep the waiting row: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND public_key=$2`, id, reset.pubB64); n != 1 {
		t.Fatal("the new key was not adopted")
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='appliance.identity_replaced' AND target_id=$1`, id); n != 1 {
		t.Fatalf("an accepted replacement must be audited, got %d", n)
	}
	if rec = f.register(first); rec.Code != http.StatusForbidden || decode(t, rec)["error"] != "identity_retired" {
		t.Fatalf("the replaced key must never register again, got %d %s", rec.Code, rec.Body.String())
	}
}

// A retired appliance that re-registers after a factory reset is a new row; the old row keeps its history.
func TestRetiredApplianceReRegistersAsNewRow(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("First Hotel")
	old := newBox("SC-TEST-0002", "hwf-2")
	rec := f.register(old)
	oldID := decode(t, rec)["appliance_id"].(string)
	f.activate(oldID, cust, site, 50)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliance_security_alerts (appliance_id, serial, kind, detail)
        VALUES ($1,'SC-TEST-0002','wan_mac_mismatch','{}')`, oldID); err != nil {
		t.Fatal(err)
	}
	rec = f.operatorCall(f.base.retire, http.MethodPost, oldID, map[string]any{
		"reason": "returned", "emergency": true, "confirm_serial": "SC-TEST-0002"})
	if rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}

	reset := newBox("SC-TEST-0002", "hwf-2")
	rec = f.register(reset)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-registration: %d %s", rec.Code, rec.Body.String())
	}
	newID := decode(t, rec)["appliance_id"].(string)
	if newID == oldID {
		t.Fatal("a retired appliance's row was reused for the re-registration")
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND lifecycle_state='revoked' AND tenant_id IS NOT NULL`, oldID); n != 1 {
		t.Fatal("the retired row must stay retired with its customer")
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND lifecycle_state='pending_approval' AND tenant_id IS NULL
                      AND held_customer_id IS NULL`, newID); n != 1 {
		t.Fatal("the new row must be a clean waiting appliance")
	}
	if n := f.count(`SELECT count(*) FROM licenses WHERE $1::uuid = ANY(appliance_ids)`, oldID); n == 0 {
		t.Fatal("the licence history must stay with the old row")
	}
	if n := f.count(`SELECT count(*) FROM licenses WHERE $1::uuid = ANY(appliance_ids)`, newID); n != 0 {
		t.Fatal("the new row must not inherit any licence")
	}
	if n := f.count(`SELECT count(*) FROM appliance_security_alerts WHERE appliance_id=$1`, newID); n != 0 {
		t.Fatal("the new row must not inherit the old row's alerts")
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE target_id=$1 AND action <> 'appliance.registered_after_retirement'`, newID); n != 0 {
		t.Fatal("the new row must start with an empty audit trail")
	}
	if n := f.count(`SELECT count(*) FROM retired_appliance_identities WHERE public_key=$1`, old.pubB64); n != 1 {
		t.Fatal("the retired identity key must be remembered")
	}
	rec = f.operatorCall(f.base.getAppliance, http.MethodGet, newID, nil)
	d := decode(t, rec)
	if lics := d["licenses"].([]any); len(lics) != 0 {
		t.Fatalf("the new appliance's page lists %d licence(s) of the previous customer", len(lics))
	}
	for _, e := range d["events"].([]any) {
		if a := e.(map[string]any)["action"]; a != "appliance.registered_after_retirement" {
			t.Fatalf("the new appliance's page shows the previous life's event %v", a)
		}
	}
	if rec = f.register(newBox("SC-TEST-0002", "hwf-other")); rec.Code != http.StatusForbidden {
		t.Fatalf("a third identity from other hardware must still be refused, got %d", rec.Code)
	}
}

func TestHardwareReusedAlertIsBounded(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Bounded")
	rec := f.register(newBox("SC-TEST-0003", "hwf-3"))
	id := decode(t, rec)["appliance_id"].(string)
	f.activate(id, cust, site, 10)
	for i := 0; i < 5; i++ {
		if rec = f.register(newBox("SC-TEST-0003", "hwf-3")); rec.Code != http.StatusForbidden {
			t.Fatalf("new key on an activated serial must be refused, got %d", rec.Code)
		}
	}
	if n := f.count(`SELECT count(*) FROM appliance_security_alerts WHERE appliance_id=$1 AND kind='hardware_reused'`, id); n != 1 {
		t.Fatalf("want one hardware_reused alert, got %d", n)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='security_alert.hardware_reused'`); n != 1 {
		t.Fatalf("want one audit row for the alert, got %d", n)
	}
}

func TestRegistrationReplayIsRefused(t *testing.T) {
	f := newFixture(t)
	bx := newBox("SC-TEST-0004", "hwf-4")
	body := registerBody(bx)
	tok := registerToken(t, bx, body)
	if rec := f.registerWith(body, tok); rec.Code != http.StatusOK {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := f.registerWith(body, tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed registration must be refused, got %d", rec.Code)
	}
}
