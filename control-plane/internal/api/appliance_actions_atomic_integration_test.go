package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

func TestCanonicalMAC(t *testing.T) {
	for in, want := range map[string]string{
		"00:11:22:33:44:55": "00:11:22:33:44:55",
		"00-11-22-AA-BB-CC": "00:11:22:aa:bb:cc",
		" 0011.22aa.bbcc ":  "00:11:22:aa:bb:cc",
	} {
		if got, ok := canonicalMAC(in); !ok || got != want {
			t.Errorf("canonicalMAC(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "00:11:22:33:44", "zz:11:22:33:44:55", "00:11:22:33:44:55:66:77", "eth0"} {
		if _, ok := canonicalMAC(bad); ok {
			t.Errorf("canonicalMAC(%q) must be refused", bad)
		}
	}
}

// Audit Phase 1 M7: a malformed MAC is refused, and a failed re-issue leaves wan_mac as it was.
func TestRebindWANMACIsValidatedAndAtomic(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Rebind")
	app := decode(t, f.register(newBox("SC-TEST-M7-1", "hwf-m7")))["appliance_id"].(string)
	f.activate(app, cust, site, 10)
	if rec := f.operatorCall(f.base.rebindWANMAC, http.MethodPost, app, map[string]any{"reason": "nic", "new_mac": "not-a-mac"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a malformed MAC must be refused: %d %s", rec.Code, rec.Body.String())
	}
	// Re-issue fails (no signer): the MAC must not change.
	signer := f.base.Lic.Signer
	f.base.Lic.Signer = nil
	rec := f.operatorCall(f.base.rebindWANMAC, http.MethodPost, app, map[string]any{"reason": "nic", "new_mac": "AA-BB-CC-DD-EE-FF"})
	if rec.Code == http.StatusOK {
		t.Fatalf("rebind without a signer must fail: %s", rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND wan_mac='00:11:22:33:44:55'`, app); n != 1 {
		t.Fatal("a failed re-issue must leave wan_mac unchanged")
	}
	f.base.Lic.Signer = signer
	if rec := f.operatorCall(f.base.rebindWANMAC, http.MethodPost, app, map[string]any{"reason": "nic", "new_mac": "AA-BB-CC-DD-EE-FF"}); rec.Code != http.StatusOK {
		t.Fatalf("rebind: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND wan_mac='aa:bb:cc:dd:ee:ff'`, app); n != 1 {
		t.Fatal("the MAC is stored in canonical form")
	}
}

// Audit Phase 1 M8: an appliance being deleted cannot be activated; a failed step leaves it intact and unflagged.
func TestDeleteMarksTheApplianceAndAFailureClearsIt(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Delete")
	app := decode(t, f.register(newBox("SC-TEST-M8-1", "hwf-m8")))["appliance_id"].(string)

	if ok, err := f.base.startApplianceDeletion(f.ctx, app); err != nil || !ok {
		t.Fatalf("a waiting appliance can be marked: %v %v", ok, err)
	}
	rec := f.operatorCall(f.base.activate, http.MethodPost, app, map[string]any{
		"customer_id": cust, "site_id": site, "reason": "x", "license": map[string]any{"max_concurrent_online_guests": 5}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "being deleted") {
		t.Fatalf("activation of an appliance being deleted = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE appliances SET deletion_started_at=NULL WHERE id=$1`, app); err != nil {
		t.Fatal(err)
	}

	// A step that fails (here the licence revocation) leaves the appliance in place and not flagged.
	if _, err := f.pool.Exec(f.ctx, `CREATE FUNCTION test_refuse() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN RAISE EXCEPTION 'refused by test'; END $$;
        CREATE TRIGGER test_refuse BEFORE UPDATE ON licenses FOR EACH STATEMENT EXECUTE FUNCTION test_refuse()`); err != nil {
		t.Fatal(err)
	}
	rec = f.operatorCall(f.base.deleteAppliance, http.MethodDelete, app, map[string]any{"confirm_serial": "SC-TEST-M8-1", "reason": "gone"})
	if rec.Code == http.StatusNoContent {
		t.Fatal("the delete must fail when a step fails")
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND deletion_started_at IS NULL`, app); n != 1 {
		t.Fatal("a failed delete must leave the appliance in place and unflagged")
	}
	if _, err := f.pool.Exec(f.ctx, `DROP TRIGGER test_refuse ON licenses`); err != nil {
		t.Fatal(err)
	}
	rec = f.operatorCall(f.base.deleteAppliance, http.MethodDelete, app, map[string]any{"confirm_serial": "SC-TEST-M8-1", "reason": "gone"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1`, app); n != 0 {
		t.Fatal("the appliance must be deleted")
	}

	// An activated appliance is never marked.
	app2 := decode(t, f.register(newBox("SC-TEST-M8-2", "hwf-m8b")))["appliance_id"].(string)
	f.activate(app2, cust, site, 5)
	if ok, _ := f.base.startApplianceDeletion(f.ctx, app2); ok {
		t.Fatal("an activated appliance must not be marked for deletion")
	}
}

// Audit Phase 1 L4: suspend and resume decide "already in that state" under the per-appliance lock.
func TestSuspendResumeStateDecidedUnderLock(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Suspend")
	app := decode(t, f.register(newBox("SC-TEST-L4-1", "hwf-l4")))["appliance_id"].(string)
	f.activate(app, cust, site, 5)
	lid, err := f.base.Lic.CurrentLicenseID(f.ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.Lic.Resume(f.ctx, lid, ""); !errors.Is(err, licensing.ErrSameStatus) {
		t.Fatalf("resume of an active licence must be ErrSameStatus, got %v", err)
	}
	doc, err := f.base.Lic.Suspend(f.ctx, lid, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.Lic.Suspend(f.ctx, doc.LicenseID, ""); !errors.Is(err, licensing.ErrSameStatus) {
		t.Fatalf("a second suspend must be ErrSameStatus, got %v", err)
	}
	if n := f.count(`SELECT count(*) FROM licenses WHERE $1::uuid = ANY(appliance_ids) AND status='suspended'`, app); n != 1 {
		t.Fatalf("exactly one suspended licence, got %d", n)
	}
	if err := f.base.Lic.Revoke(f.ctx, doc.LicenseID); err != nil {
		t.Fatalf("revoke under the lock: %v", err)
	}
	if err := f.base.Lic.Revoke(f.ctx, doc.LicenseID); !errors.Is(err, licensing.ErrNoLicense) {
		t.Fatalf("a second revoke is ErrNoLicense, got %v", err)
	}
	// Audit Phase 1 CC5: lookups compare id = $1::uuid; a value that is not a uuid names no licence.
	if err := f.base.Lic.Revoke(f.ctx, "not-a-uuid"); !errors.Is(err, licensing.ErrNoLicense) {
		t.Fatalf("a non-uuid licence id is ErrNoLicense, got %v", err)
	}
	if _, err := f.base.Lic.Reissue(f.ctx, "not-a-uuid", ""); !errors.Is(err, licensing.ErrNoLicense) {
		t.Fatalf("reissue of a non-uuid licence id is ErrNoLicense, got %v", err)
	}
}
