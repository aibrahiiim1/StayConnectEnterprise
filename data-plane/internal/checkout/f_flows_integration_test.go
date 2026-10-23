//go:build integration

package checkout

import (
	"context"
	"testing"
	"time"
)

// F3 — conversion depends only on the Entitlement being ACTIVE and valid at the boundary, never on its origin.
// The "prepaid" case is a zero-amount fixture, not evidence of any payment.
func TestIntegration_F3_OriginAgnosticConversion(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	for _, origin := range []string{"ADMIN_GRANT", "GUEST_SELECTION_FIXTURE", "POST_STAY_CONVERSION"} {
		f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
		win := f.boundary.Add(48 * time.Hour)
		ent := seedEnt(t, p, f, &win, []txn{{"ACTIVE", f.boundary.Add(-time.Hour)}})
		trigger := origin
		if origin == "GUEST_SELECTION_FIXTURE" {
			trigger = "VOUCHER_REDEMPTION"
		}
		if err := guardedExec(p, commerceFamily, `UPDATE iam_v2.purchases SET trigger=$2, amount_minor=0
			WHERE id=(SELECT purchase_id FROM iam_v2.entitlements WHERE id=$1)`, ent, trigger); err != nil {
			t.Fatal(err)
		}
		r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
		if err != nil {
			t.Fatalf("%s: %v", origin, err)
		}
		if !r.GraceCreated || r.IsEmergency {
			t.Fatalf("%s: grace=%v emergency=%v, want an ordinary grace regardless of origin", origin, r.GraceCreated, r.IsEmergency)
		}
		if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND amount_minor <> 0`, f.stay); n != 0 {
			t.Fatalf("%s: the conversion recorded a non-zero amount", origin)
		}
	}
}

// F4 — devices authorized at the boundary keep access on the grace Entitlement and their sessions are rebound
// without a logout; a device deauthorized before the boundary is excluded.
func TestIntegration_F4_Grandfathering(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	b := f.boundary
	ent := activeEnt(t, p, f)
	inDev, inSess := seedDeviceAuth(t, p, f, ent, 1, b.Add(-2*time.Hour), nil, b.Add(-2*time.Hour), nil)
	goneAt := b.Add(-time.Hour)
	outDev, _ := seedDeviceAuth(t, p, f, ent, 2, b.Add(-3*time.Hour), &goneAt, b.Add(-3*time.Hour), &goneAt)

	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatal(err)
	}
	if r.DevicesGrandfathered != 1 || r.SessionsRebound != 1 {
		t.Fatalf("grandfathered=%d rebound=%d, want 1/1", r.DevicesGrandfathered, r.SessionsRebound)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND device_id=$2 AND grandfathered`,
		r.NewEntitlementID, inDev); n != 1 {
		t.Fatal("the boundary-authorized device was not grandfathered")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND device_id=$2`,
		r.NewEntitlementID, outDev); n != 0 {
		t.Fatal("a device deauthorized before the boundary was grandfathered")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state='active' AND ended IS NULL AND entitlement_id=$2`,
		inSess, r.NewEntitlementID); n != 1 {
		t.Fatal("the grandfathered session was logged out instead of rebound")
	}
}

// F5 — an Entitlement whose validity window elapsed before the boundary earns no grace.
func TestIntegration_F5_ValidityWindowAtBoundary(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	b := f.boundary
	expired := b.Add(-30 * time.Minute)
	ent := seedEnt(t, p, f, &expired, []txn{{"ACTIVE", b.Add(-2 * time.Hour)}})
	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatal(err)
	}
	if r.GraceCreated {
		t.Fatal("an entitlement whose window elapsed before the boundary must not earn grace")
	}
	if liveOriginals(t, p, f.stay) != 0 {
		t.Fatal("the expired entitlement was left live")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1 AND status='TERMINATED'`, ent); n != 1 {
		t.Fatal("the expired entitlement was not terminated at the boundary")
	}
}

// F6 — a configured grace package that disagrees with the published policy falls back to the canonical
// Emergency-Grace catalog rather than granting an unapproved policy.
func TestIntegration_F6_EmergencyFallback(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true,
		mismatchField: "down", bootstrapEmergency: true})
	activeEnt(t, p, f)
	r, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatal(err)
	}
	if !r.GraceCreated || !r.IsEmergency {
		t.Fatalf("grace=%v emergency=%v, want an EMERGENCY grace when the configured package disagrees with the policy",
			r.GraceCreated, r.IsEmergency)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements e
		JOIN iam_v2.internet_package_revisions ipr ON ipr.id=e.package_revision_id
		JOIN iam_v2.internet_packages ip ON ip.id=ipr.package_id
		WHERE e.id=$1 AND ip.is_system`, r.NewEntitlementID); n != 1 {
		t.Fatal("emergency grace was not taken from the canonical system catalog")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.active_operational_alerts WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatal("an emergency fallback must raise exactly one operational alert")
	}
}

// F7 — converting one checkout episode twice creates one grace and one audit row; a reinstatement starts a new episode.
func TestIntegration_F7_EpisodeIdempotency(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	c := NewConverter(p)
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)
	src := checkoutEvent(t, p, f)
	first, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, src)
	if err != nil || !first.GraceCreated {
		t.Fatalf("first conversion: %+v err=%v", first, err)
	}
	second, err := c.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, src)
	if err != nil {
		t.Fatal(err)
	}
	if second.GraceCreated {
		t.Fatal("a repeated conversion of the SAME episode must not create a second grace")
	}
	if second.Reason != "ALREADY_PROCESSED_THIS_EPISODE" {
		t.Fatalf("reason=%s, want ALREADY_PROCESSED_THIS_EPISODE", second.Reason)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("audit rows = %d, want exactly 1 per episode", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 1 {
		t.Fatal("a second grace entitlement was created for one episode")
	}
}
