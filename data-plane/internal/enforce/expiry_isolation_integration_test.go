//go:build integration

package enforce

import (
	"context"
	"testing"
	"time"
)

// P4 M3: one entitlement whose expiry fails every time does not stop the other expiries on the site.
func TestIntegration_OneFailingEntitlementDoesNotStopTheSweep(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 5000, 2000, 0)
	elapsed := time.Now().Add(-20 * time.Minute).Truncate(time.Microsecond)
	bad, _ := grant(t, p, f, &elapsed, time.Now().Add(-2*time.Hour))
	// One live entitlement per stay: the second needs its own stay and device on the same site.
	f2 := f
	tx := opTx(t, p, "stay")
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version)
		VALUES (gen_random_uuid(),$1,$2,$3,'R2','S2','IN_HOUSE',1,0) RETURNING id::text`, f.tenant, f.site, f.iface).Scan(&f2.stay); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),'02:00:00:00:30:02'::macaddr) RETURNING id::text`, f.tenant, f.site).Scan(&f2.device); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	good, goodSess := grant(t, p, f2, &elapsed, time.Now().Add(-2*time.Hour))

	if _, err := p.Exec(ctx, `
		CREATE OR REPLACE FUNCTION public.aud_fail_one_entitlement() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF NEW.id::text = current_setting('aud.fail_entitlement', true) THEN
		    RAISE EXCEPTION 'test: this entitlement always fails';
		  END IF;
		  RETURN NEW;
		END $$;
		DROP TRIGGER IF EXISTS aud_fail_one ON iam_v2.entitlements;
		CREATE TRIGGER aud_fail_one BEFORE UPDATE ON iam_v2.entitlements
		  FOR EACH ROW EXECUTE FUNCTION public.aud_fail_one_entitlement();`); err != nil {
		t.Fatalf("install the failing trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), `DROP TRIGGER IF EXISTS aud_fail_one ON iam_v2.entitlements;
			DROP FUNCTION IF EXISTS public.aud_fail_one_entitlement(); ALTER DATABASE CURRENT RESET aud.fail_entitlement`)
	})
	var db string
	if err := p.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `ALTER DATABASE "`+db+`" SET aud.fail_entitlement = '`+bad+`'`); err != nil {
		t.Fatal(err)
	}
	p.Reset() // new connections pick up the setting

	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("one failing entitlement failed the whole sweep: %v", err)
	}
	if len(due) != 1 || due[0].EntitlementID != good {
		t.Fatalf("due = %+v, want only the healthy entitlement %s", due, good)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='TERMINATED'`, good); n != 1 {
		t.Fatal("the healthy entitlement was not ended")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state='ended'`, goodSess); n != 1 {
		t.Fatal("the healthy entitlement's session was not ended")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='TERMINATED'`, bad); n != 0 {
		t.Fatal("the failing entitlement should still be live")
	}
	if ExpiryFailures(bad) != 1 {
		t.Fatalf("failure count = %d, want 1", ExpiryFailures(bad))
	}
}
