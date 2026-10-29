//go:build integration

package checkout

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Audit AD-45 (P6 L6): an old PMS check-out time is clamped up to the stay's floor and recorded suspect.

func boundaryAt(t *testing.T, f fixture) (time.Time, bool, string) {
	t.Helper()
	var at time.Time
	var suspect bool
	var reason string
	if err := pool(t).QueryRow(context.Background(), `SELECT boundary_at, boundary_clock_suspect, boundary_reason_code
		FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, f.stay).Scan(&at, &suspect, &reason); err != nil {
		t.Fatal(err)
	}
	return at, suspect, reason
}

func TestIntegration_AD45_CheckoutBeforeArrivalIsClampedAndSuspect(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	old := time.Now().Add(-10 * 24 * time.Hour).Truncate(time.Microsecond)
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stays SET arrival=(now() - interval '3 days')::date WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	eid := seedEvent(t, p, f, &old, false, 5, "LIVE", 0, "")
	applyEvent(t, p, eid, f.stay)
	res, err := NewConverter(p).ConvertAtCheckout(context.Background(), f.tenant, f.site, f.iface, f.stay, BoundarySource{StayEventID: eid})
	if err != nil {
		t.Fatal(err)
	}
	if !res.BoundaryClockSuspect || res.BoundaryReason != "PMS_TIME_BEFORE_ARRIVAL" {
		t.Fatalf("a check-out ten days old on a stay that arrived three days ago is suspect: %+v", res)
	}
	at, suspect, reason := boundaryAt(t, f)
	if !suspect || reason != "PMS_TIME_BEFORE_ARRIVAL" {
		t.Fatalf("audit records the clamp: %v %s", suspect, reason)
	}
	if !at.After(old.Add(5*24*time.Hour)) || at.After(time.Now()) {
		t.Fatalf("the boundary is clamped up to the arrival floor (about four days ago) and never into the future: %v", at)
	}
}

func TestIntegration_AD45_CheckoutBeforeTheLastAppliedEventIsClamped(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	prior := time.Now().Add(-5 * time.Hour).Truncate(time.Microsecond)
	var priorID string
	if err := guarded(p, stayFamily, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
			(tenant_id,site_id,pms_interface_id,stay_id,external_event_identity,event_type,pms_timestamp_raw,pms_timestamp_utc,
			 source_timezone,sequence_version,normalization_version,clock_suspect,payload,processing_status)
			VALUES ($1,$2,$3,NULL,'EV-prior','RC','x',$4,'UTC',3,1,false,'{}','PENDING') RETURNING id::text`,
			f.tenant, f.site, f.iface, prior).Scan(&priorID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stay_events SET stay_id=$2, processing_status='APPLIED', processed_at=now() WHERE id=$1`, priorID, f.stay); err != nil {
		t.Fatal(err)
	}
	early := time.Now().Add(-9 * time.Hour).Truncate(time.Microsecond)
	eid := seedEvent(t, p, f, &early, false, 5, "LIVE", 0, "")
	applyEvent(t, p, eid, f.stay)
	res, err := NewConverter(p).ConvertAtCheckout(context.Background(), f.tenant, f.site, f.iface, f.stay, BoundarySource{StayEventID: eid})
	if err != nil {
		t.Fatal(err)
	}
	if !res.BoundaryClockSuspect || res.BoundaryReason != "PMS_TIME_BEFORE_LAST_EVENT" {
		t.Fatalf("a check-out time before the stay's own last event is suspect: %+v", res)
	}
	at, _, _ := boundaryAt(t, f)
	if d := at.Sub(prior); d < -time.Second || d > time.Second {
		t.Fatalf("the boundary is the last applied event's time, got %v want %v", at, prior)
	}
}

func TestIntegration_AD45_AHonestCheckoutIsTrustedUnchanged(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true, systemGracePackage: true, bootstrapEmergency: true})
	if err := guardedExec(p, stayFamily, `UPDATE iam_v2.stays SET arrival=(now() - interval '2 days')::date WHERE id=$1`, f.stay); err != nil {
		t.Fatal(err)
	}
	res, err := NewConverter(p).ConvertAtCheckout(context.Background(), f.tenant, f.site, f.iface, f.stay, checkoutEvent(t, p, f))
	if err != nil {
		t.Fatal(err)
	}
	if res.BoundaryClockSuspect || res.BoundaryReason != "TRUSTED_PMS_CHECKOUT_TS" {
		t.Fatalf("a check-out two hours ago on a stay that arrived two days ago is trusted: %+v", res)
	}
	at, _, _ := boundaryAt(t, f)
	if d := at.Sub(f.boundary); d < -time.Second || d > time.Second {
		t.Fatalf("the trusted boundary is the PMS time: %v vs %v", at, f.boundary)
	}
}
