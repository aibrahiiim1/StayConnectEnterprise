package api

import (
	"net/http"
	"strings"
	"testing"
)

// AD-1: Activate needs the code the appliance shows, derived from the identity key Central holds.
func TestActivateRequiresTheApplianceActivationCode(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Code Hotel")
	id := decode(t, f.register(newBox("SC-TEST-AD1-1", "hwf-ad1")))["appliance_id"].(string)
	body := func(code string) map[string]any {
		m := map[string]any{"customer_id": cust, "site_id": site, "reason": "test",
			"license": map[string]any{"max_concurrent_online_guests": 5, "valid_days": 30}}
		if code != "" {
			m["activation_code"] = code
		}
		return m
	}
	rec := f.operatorCall(f.base.activate, http.MethodPost, id, body(""))
	if rec.Code != http.StatusBadRequest || decode(t, rec)["error"] != "activation_code_required" {
		t.Fatalf("missing code: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.operatorCall(f.base.activate, http.MethodPost, id, body("AAAA-AAAA"))
	if rec.Code != http.StatusConflict || decode(t, rec)["error"] != "activation_code_mismatch" {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), f.codeFor(id)) {
		t.Fatal("the refusal must not reveal the expected code")
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND lifecycle_state='pending_approval'`, id); n != 1 {
		t.Fatal("a refused activation must leave the appliance waiting")
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='appliance.activation_code_refused' AND target_id=$1`, id); n != 2 {
		t.Fatalf("both refusals must be audited, got %d", n)
	}
	// Typed the way an operator might: lower case, no dash, spaces.
	typed := strings.ToLower(strings.ReplaceAll(f.codeFor(id), "-", " "))
	rec = f.operatorCall(f.base.activate, http.MethodPost, id, body(typed))
	if rec.Code != http.StatusOK {
		t.Fatalf("right code: %d %s", rec.Code, rec.Body.String())
	}
}

// AD-1: an attacker who registered a serial first with their own key cannot show the real box's code.
func TestSquattedSerialFailsTheCodeCheck(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Squat Hotel")
	attacker := newBox("SC-TEST-AD1-2", "hwf-real")
	id := decode(t, f.register(attacker))["appliance_id"].(string)
	real := newBox("SC-TEST-AD1-2", "hwf-real")
	if rec := f.register(real); rec.Code != http.StatusForbidden {
		t.Fatalf("the real box must not take over the row either: %d", rec.Code)
	}
	rec := f.operatorCall(f.base.activate, http.MethodPost, id, map[string]any{
		"customer_id": cust, "site_id": site, "reason": "test", "activation_code": codeOfPub(t, real.pubB64),
		"license": map[string]any{"max_concurrent_online_guests": 5, "valid_days": 30}})
	if rec.Code != http.StatusConflict || decode(t, rec)["error"] != "activation_code_mismatch" {
		t.Fatalf("the real box's code must not activate the squatter's row: %d %s", rec.Code, rec.Body.String())
	}
}

// AD-19: a NEW registration carrying the shared constant-anchor serial or fingerprint is refused.
func TestConstantAnchorRegistrationIsRefused(t *testing.T) {
	f := newFixture(t)
	rec := f.register(newBox(constantAnchorSerial, "hwf-x"))
	if rec.Code != http.StatusUnprocessableEntity || decode(t, rec)["error"] != "hardware_identity_not_unique" {
		t.Fatalf("constant serial: %d %s", rec.Code, rec.Body.String())
	}
	bx := newBox("SC-TEST-AD19-1", "ignored")
	bx.hwf = constantAnchorFingerprint
	rec = f.register(bx)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("constant fingerprint: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances`); n != 0 {
		t.Fatalf("nothing may be stored, got %d rows", n)
	}
}

// AD-20: past the cap a NEW waiting row is refused with 503 and one alert; a known appliance re-registering is not.
func TestWaitingApplianceCap(t *testing.T) {
	f := newFixture(t)
	if _, err := f.pool.Exec(f.ctx, `UPDATE central_registration_settings SET max_waiting_appliances=10`); err != nil {
		t.Fatal(err)
	}
	a := newBox("SC-TEST-AD20-0", "hwf-a")
	if rec := f.register(a); rec.Code != http.StatusOK {
		t.Fatalf("under the cap: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliances (serial, lifecycle_state, public_key)
        SELECT 'SC-FILL-'||g, 'pending_approval', 'fill'||g FROM generate_series(1,9) g`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rec := f.register(newBox("SC-TEST-AD20-3", "hwf-c"))
		if rec.Code != http.StatusServiceUnavailable || decode(t, rec)["error"] != "registration_capacity" {
			t.Fatalf("over the cap: %d %s", rec.Code, rec.Body.String())
		}
	}
	if n := f.count(`SELECT count(*) FROM appliance_security_alerts WHERE kind='registration_cap_reached'`); n != 1 {
		t.Fatalf("one alert, got %d", n)
	}
	if rec := f.register(a); rec.Code != http.StatusOK {
		t.Fatalf("a known appliance must still re-register at the cap: %d", rec.Code)
	}
}
