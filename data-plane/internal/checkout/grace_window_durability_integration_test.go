//go:build integration

package checkout

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The grace clock belongs to the row, not a process: neither a restart nor ordinary activity (reconnecting,
// a second device, a new session) may move the deadline. Each test reads the window, acts, and reads again.

// A restart must not grant a second hour: authorization reads the stored window_ends_at every check, and a
// fresh Converter over a fresh connection must see what the previous process wrote.
func TestIntegration_GraceWindowSurvivesRestartUnchanged(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	// One boundary source, reused below: a replay is the same event twice, not a second departure.
	src := checkoutEvent(t, p, f)
	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, src)
	if err != nil || !res.GraceCreated {
		t.Fatalf("conversion: %+v err=%v", res, err)
	}

	var before time.Time
	var activated time.Time
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at, activated_at FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID).
		Scan(&before, &activated); err != nil {
		t.Fatal(err)
	}
	if before.IsZero() {
		t.Fatal("a grace entitlement with no window_ends_at would never end on time")
	}

	// The restart: a new pool and Converter, nothing carried over, so the deadline must be re-read.
	p2 := pool(t)
	defer p2.Close()
	restarted := NewConverter(p2)

	var after time.Time
	if err := p2.QueryRow(ctx,
		`SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("the grace deadline moved across a restart: %s -> %s", before, after)
	}

	// The deadline is still measured from the original activation, not the restart.
	if got := after.Sub(activated); got <= 0 {
		t.Fatalf("window_ends_at %s is not after activated_at %s", after, activated)
	}

	// A repeated checkout after the restart (the PMS re-delivers the departure) must not re-arm the window.
	again, err := restarted.ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, src)
	if err != nil {
		t.Fatal(err)
	}
	if again.GraceCreated {
		t.Fatal("a checkout replayed after a restart created a SECOND grace")
	}
	var final time.Time
	if err := p2.QueryRow(ctx,
		`SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID).Scan(&final); err != nil {
		t.Fatal(err)
	}
	if !final.Equal(before) {
		t.Fatalf("a replayed checkout extended the grace: %s -> %s", before, final)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay); n != 1 {
		t.Fatalf("grace entitlements for this stay = %d, want exactly 1", n)
	}
}

// Reconnecting, a new session or a repeated sign-in must not recompute the deadline, and (After Check-out Access,
// 2026-10-30) a device that was NOT authorised on the stay before check-out cannot be added to the entitlement at all.
// This test used to authorise a brand-new device on the grace entitlement and call that "a second device"; under the
// unified contract that device is refused, so the test now reconnects a device that was on the stay and asserts the refusal
// of the new one.
func TestIntegration_ActivityDuringGraceDoesNotExtendIt(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	ent := activeEnt(t, p, f)
	b := f.boundary
	// A device authorised on the stay before check-out, currently OFFLINE (its session ended before the boundary).
	known, _ := seedDeviceAuth(t, p, f, ent, 7, b.Add(-2*time.Hour), nil, b.Add(-2*time.Hour), ptr(b.Add(-90*time.Minute)))

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("conversion: %+v err=%v", res, err)
	}
	if res.DevicesGrandfathered != 1 {
		t.Fatalf("the offline previously-authorised device was not carried over: %+v", res)
	}
	var before time.Time
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// The known device reconnects and a new session opens; authorising it again is idempotent and moves nothing.
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, res.NewEntitlementID, known); err != nil {
		t.Fatalf("authorize the reconnecting known device: %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.sessions
		(id,tenant_id,site_id,entitlement_id,device_id,state,started)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,'active',now())`,
		f.tenant, f.site, res.NewEntitlementID, known); err != nil {
		t.Fatalf("open a session during grace: %v", err)
	}

	// A device that was never authorised on the stay cannot be added, by any route.
	var fresh string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),$3::macaddr) RETURNING id`,
		f.tenant, f.site, "02:00:00:00:be:ef").Scan(&fresh); err != nil {
		t.Fatalf("seed a new device: %v", err)
	}
	_, err = p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, res.NewEntitlementID, fresh)
	if err == nil || !strings.Contains(err.Error(), "AFTER_CHECKOUT_NEW_DEVICE_REJECTED") {
		t.Fatalf("a new device was added to the After Check-out entitlement: %v", err)
	}

	var after time.Time
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("activity during grace moved the deadline: %s -> %s", before, after)
	}

	// It is still the only live entitlement for the stay: a second one would extend access just as well.
	if n := count(t, p, `
		SELECT count(*) FROM iam_v2.entitlements
		 WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("live entitlements for this stay = %d, want exactly 1", n)
	}
}
