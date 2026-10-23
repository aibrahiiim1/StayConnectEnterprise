//go:build integration

package checkout

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Reinstatement is a new episode, so grace conversions (unique on stay_id, checkout_episode) must convert again.
// The full lifecycle runs through Processor.ProcessNext in
// internal/stayengine/reinstatement_lifecycle_integration_test.go; this file keeps only the two claims about
// the database itself.

func stayState(t *testing.T, p *pgxpool.Pool, stay string) (status string, episode int, checkoutAt *time.Time) {
	t.Helper()
	if err := p.QueryRow(context.Background(),
		`SELECT status, lifecycle_version, effective_checkout_at FROM iam_v2.stays WHERE id=$1`, stay).
		Scan(&status, &episode, &checkoutAt); err != nil {
		t.Fatalf("read the stay: %v", err)
	}
	return
}

// One live entitlement per subject: for a PMS room-auth guest the subject is the stay, enforced by the partial
// unique index ent_live_stay. Two stays in one room are two subjects; two live entitlements on one stay are not.
func TestIntegration_SecondLiveEntitlementForOneStayIsRefusedByTheDatabase(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEventFor(t, p, f, f.stay, 5))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v err=%v", res, err)
	}

	// Even a direct write cannot give the stay a second live entitlement.
	_, err = p.Exec(ctx, `
		INSERT INTO iam_v2.entitlements
		  (id,tenant_id,site_id,stay_id,pms_interface_id,service_plan_revision_id,package_revision_id,
		   time_accounting_mode,end_mode,status,activated_at)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$6,'VALIDITY_WINDOW','HARD_EXPIRY','ACTIVE',now())`,
		f.tenant, f.site, f.stay, f.iface, f.svcRev, f.gracePkgRev)
	if err == nil {
		t.Fatal("the database accepted a SECOND live entitlement for one stay; ent_live_stay is not protecting the invariant")
	}

	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("after the refused insert the stay holds %d live entitlements, want 1", n)
	}
}

// A second stay is a second subject (the index is keyed on stay_id) and may hold its own live entitlement.
func TestIntegration_ANewStayIsANewSubjectAndMayBeLiveBesideAnOldGrace(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEventFor(t, p, f, f.stay, 5))
	if err != nil || !res.GraceCreated {
		t.Fatalf("checkout: %+v err=%v", res, err)
	}

	// The guest returns under a new reservation: a new stay and entitlement are created here.
	returning := secondStayInSameRoom(t, p, f, "412", "RES-RETURN")
	returningEnt := entForStay(t, p, f, returning)

	if returningEnt == res.NewEntitlementID {
		t.Fatal("the returning guest was handed the old grace entitlement")
	}
	var entStay string
	var isGrace bool
	var supersedes *string
	if err := p.QueryRow(ctx,
		`SELECT stay_id::text, is_emergency_grace, supersedes_entitlement_id::text
		   FROM iam_v2.entitlements WHERE id=$1`, returningEnt).Scan(&entStay, &isGrace, &supersedes); err != nil {
		t.Fatal(err)
	}
	if entStay != returning {
		t.Fatalf("the new entitlement belongs to stay %s, want the new stay %s", entStay, returning)
	}
	if isGrace {
		t.Fatal("the returning guest started on a grace entitlement instead of a normal package")
	}
	if supersedes != nil {
		t.Fatalf("a new stay's first entitlement must supersede nothing; it superseded %s", *supersedes)
	}

	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("the departed stay holds %d live entitlements, want 1 (its grace)", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status='ACTIVE'`, returning); n != 1 {
		t.Fatalf("the returning stay holds %d live entitlements, want 1", n)
	}
}
