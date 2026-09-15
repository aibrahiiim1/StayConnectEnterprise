//go:build integration

package checkout

import (
	"context"
	"testing"
	"time"
)

// THE GRACE CLOCK BELONGS TO THE ROW, NOT TO A RUNNING PROCESS.
//
// A guest gets an hour after checkout. The two ways that promise gets quietly broken are a restart handing
// them a fresh hour, and ordinary activity -- reconnecting, a second device, a new session -- nudging the
// deadline along. Both are easy to introduce and neither shows up in a log: the guest just stays online
// longer than the hotel agreed to, and nobody notices until somebody adds up the usage.
//
// Nothing in the existing suite asked either question. It proves the conversion, the idempotency, the
// grandfathering and the boundary arithmetic, but never that the deadline SURVIVES -- and survival is the
// part a reboot tests in production whether or not anybody wrote it down.
//
// These read the window from the database, then do the thing that might move it, then read it again.

// A RESTART MUST NOT GRANT A SECOND HOUR.
//
// The appliance's authorization predicate is `window_ends_at IS NULL OR window_ends_at > now()`, evaluated
// against the stored column on every check. So the deadline is durable by construction -- but "by
// construction" is exactly the kind of claim that stops being true the first time somebody caches it in
// memory for performance. A fresh Converter over a fresh connection is what a service restart produces, and
// the window it sees must be the window the previous process wrote.
func TestIntegration_GraceWindowSurvivesRestartUnchanged(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	// ONE episode, one boundary source -- reused below, because replaying a checkout means the SAME event
	// arriving twice, not a second departure.
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

	// THE RESTART. A genuinely new connection pool and a new Converter over it -- nothing carried from the
	// process that created the grace. Reusing the original pool would have tested almost nothing: the point
	// is that the deadline lives in the database and is re-read, not remembered.
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

	// AND THE DEADLINE IS STILL THE ORIGINAL ONE, measured from the original activation rather than from the
	// restart. A window that silently re-based on process start is the precise failure this guards.
	if got := after.Sub(activated); got <= 0 {
		t.Fatalf("window_ends_at %s is not after activated_at %s", after, activated)
	}

	// A REPEATED CHECKOUT AFTER THE RESTART MUST NOT RE-ARM IT EITHER. This is the realistic shape of the
	// bug: the appliance reboots, the PMS re-delivers the departure, and a second conversion hands out a
	// fresh hour.
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

// RECONNECTING DOES NOT BUY MORE TIME.
//
// During grace a guest's laptop sleeps and wakes, their phone joins, a lease changes and a new session is
// written. Every one of those is a perfectly ordinary write against the same subject, and every one is an
// opportunity for a deadline to be recomputed "helpfully". The window must be indifferent to all of it.
func TestIntegration_ActivityDuringGraceDoesNotExtendIt(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	activeEnt(t, p, f)

	res, err := NewConverter(p).ConvertAtCheckout(ctx, f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil || !res.GraceCreated {
		t.Fatalf("conversion: %+v err=%v", res, err)
	}
	var before time.Time
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// A DEVICE RECONNECTS AND A NEW SESSION OPENS against the grace entitlement -- the ordinary shape of a
	// guest still using the wifi during their grace hour. sessions.device_id is NOT NULL, so the device is
	// real rather than a placeholder.
	var dev string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),$3::macaddr) RETURNING id`,
		f.tenant, f.site, "02:00:00:00:be:ef").Scan(&dev); err != nil {
		t.Fatalf("seed a reconnecting device: %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.sessions
		(id,tenant_id,site_id,entitlement_id,device_id,state,started)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,'active',now())`,
		f.tenant, f.site, res.NewEntitlementID, dev); err != nil {
		t.Fatalf("open a session during grace: %v", err)
	}

	var after time.Time
	if err := p.QueryRow(ctx,
		`SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, res.NewEntitlementID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("activity during grace moved the deadline: %s -> %s", before, after)
	}

	// AND THE ENTITLEMENT IS STILL THE ONLY LIVE ONE for this stay. A reconnect that quietly minted a second
	// entitlement would extend access just as effectively as moving the deadline, and would be harder to see.
	if n := count(t, p, `
		SELECT count(*) FROM iam_v2.entitlements
		 WHERE stay_id=$1 AND status='ACTIVE'`, f.stay); n != 1 {
		t.Fatalf("live entitlements for this stay = %d, want exactly 1", n)
	}
}
