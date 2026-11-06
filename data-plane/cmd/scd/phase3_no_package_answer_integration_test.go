//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/onegate/onegate/data-plane/internal/iamv2"
)

// A verified stay with nothing to offer is told so (NO_PACKAGE), not that it could not be verified; when room charge is only
// withheld because the PMS link is refreshing, it is told to try again shortly (ROOM_CHARGE_PAUSED). Product Owner, 2026-11-06.

func withRoomChargeGate(t *testing.T) {
	t.Helper()
	prev := stayMethodGate
	stayMethodGate = func(context.Context, *server) iamv2.SiteMethods {
		return iamv2.SiteMethods{PaidAccess: true, RoomCharge: true}
	}
	t.Cleanup(func() { stayMethodGate = prev })
}

func (f *authFixture) deactivate(t *testing.T, rev string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.internet_packages ip SET active=false
		  FROM iam_v2.internet_package_revisions r WHERE r.id=$1 AND ip.id=r.package_id`, rev); err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_AVerifiedStayWithNothingToOfferIsToldSo(t *testing.T) {
	f := newAuthFixture(t)
	f.deactivate(t, f.pkgRev)
	f.deactivate(t, f.priced)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000f001-0000-4000-8000-000000000000"))
	if res.Outcome == outcomeVerified || res.FailureClass != "NO_PACKAGE" {
		t.Fatalf("answer %+v, want NOT_VERIFIED with class NO_PACKAGE", res)
	}
}

func TestIntegration_RoomChargeWithheldForAPMSRefreshIsToldToTryAgain(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	withRoomChargeGate(t)

	fresh := func() bool {
		var ok bool
		if err := f.pool.QueryRow(ctx, `SELECT iam_v2.p4_room_charge_interface_fresh($1,$2,$3::uuid)`, f.tenant, f.site, f.iface).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if got := f.p3.roomChargePaused(ctx, f.iface); got == fresh() {
		t.Fatalf("paused=%v while the link is fresh=%v: paused must be exactly 'not fresh' while a room-charge package exists", got, fresh())
	}
	f.goOffline(t, f.iface, "1 minute")
	if fresh() || !f.p3.roomChargePaused(ctx, f.iface) {
		t.Fatal("with the PMS link down, room charge must read as paused")
	}

	// The guest-facing answer: only the room-charge package exists, and it is withheld.
	f.deactivate(t, f.pkgRev)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000f101-0000-4000-8000-000000000000"))
	if res.Outcome == outcomeVerified || res.FailureClass != "ROOM_CHARGE_PAUSED" {
		t.Fatalf("answer %+v, want NOT_VERIFIED with class ROOM_CHARGE_PAUSED", res)
	}

	// Without any room-charge package nothing is paused: the stay simply has nothing to buy.
	f.deactivate(t, f.priced)
	if f.p3.roomChargePaused(ctx, f.iface) {
		t.Fatal("with no room-charge package on sale, nothing is paused")
	}
	_, res = post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000f102-0000-4000-8000-000000000000"))
	if res.FailureClass != "NO_PACKAGE" {
		t.Fatalf("answer %+v, want class NO_PACKAGE", res)
	}
}
