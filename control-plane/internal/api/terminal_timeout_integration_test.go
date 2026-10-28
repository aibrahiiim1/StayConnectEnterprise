package api

import (
	"net/http"
	"testing"
)

// Audit Phase 1 L6: an overdue retirement becomes terminal_delivery_failed only together with its alert.
func TestTerminalTimeoutStateAndAlertCommitTogether(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Timeout")
	app := decode(t, f.register(newBox("SC-TEST-L6-1", "hwf-l6")))["appliance_id"].(string)
	f.activate(app, cust, site, 5)
	if rec := f.operatorCall(f.base.retire, http.MethodPost, app, map[string]any{"reason": "end"}); rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE appliance_terminal_delivery SET timeout_at = now() - interval '1 minute' WHERE appliance_id=$1`, app); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `CREATE FUNCTION test_refuse_alert() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN RAISE EXCEPTION 'refused by test'; END $$;
        CREATE TRIGGER test_refuse_alert BEFORE INSERT ON appliance_security_alerts FOR EACH ROW EXECUTE FUNCTION test_refuse_alert()`); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileTerminalTimeouts(f.ctx, f.base); err == nil {
		t.Fatal("a failed alert insert must be reported")
	}
	if n := f.count(`SELECT count(*) FROM appliance_terminal_delivery WHERE appliance_id=$1 AND delivery_state='terminal_delivery_pending'`, app); n != 1 {
		t.Fatal("without its alert the delivery must stay pending, to be retried")
	}
	if _, err := f.pool.Exec(f.ctx, `DROP TRIGGER test_refuse_alert ON appliance_security_alerts`); err != nil {
		t.Fatal(err)
	}
	if n, err := ReconcileTerminalTimeouts(f.ctx, f.base); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	if n := f.count(`SELECT count(*) FROM appliance_security_alerts WHERE appliance_id=$1 AND kind='terminal_delivery_failed'`, app); n != 1 {
		t.Fatal("exactly one alert after the retry")
	}
}
