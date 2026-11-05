//go:build integration

package main

// D93 through the room sign-in the portal uses, after the stay is CHECKED_OUT (0209): the stay's own device signs in again onto
// its After Check-out entitlement without a new purchase, entitlement or allowance; a new device is refused as
// AFTER_CHECKOUT_NEW_DEVICE_REJECTED; once the After Check-out Access has ended the stay is not eligible. Before 0209 every
// one of these was refused as STAY_NOT_ELIGIBLE (found on the test appliance, 2026-11-05).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onegate/onegate/data-plane/internal/signinattempt"
)

// joinLive is the grant portald sends when resolve answered live_access.
func (f *authFixture) joinLive(t *testing.T, authContextID, ip, mac string) phase3Response {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"auth_context_id": authContextID, "join_live": true,
		"device": map[string]string{"ip": ip, "mac": mac}})
	rec := httptest.NewRecorder()
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3Response
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable grant response %q: %v", rec.Body.String(), err)
	}
	return out
}

func (f *authFixture) resolveAsDevice(t *testing.T, ip, mac, last, res, reqID string) phase3Response {
	t.Helper()
	body := f.resolveBody("412", last, res, reqID)
	body["device"] = map[string]string{"ip": ip, "mac": mac}
	_, out := post(t, f.p3.resolveHandler, body)
	return out
}

func TestIntegration_Phase3_AfterCheckoutTheStaysOwnDeviceSignsInAgainAndANewOneIsRefused(t *testing.T) {
	f := newPostStayFixture(t)
	device := f.signInByRoomThenCheckOut(t)
	f.grantAfterCheckout(t, device)
	defer f.startEnforcementOwnerAtEpoch(t, 2)()
	ctx := context.Background()

	var stayStatus string
	if err := f.pool.QueryRow(ctx, `SELECT status FROM iam_v2.stays WHERE id=$1`, f.stay).Scan(&stayStatus); err != nil || stayStatus != "CHECKED_OUT" {
		t.Fatalf("the stay is %q, want CHECKED_OUT (%v)", stayStatus, err)
	}
	counts := func() (ents, purchases int, consumed int64) {
		_ = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1),
			(SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1),
			(SELECT consumed_data_bytes FROM iam_v2.entitlements WHERE id=$2)`, f.stay, f.afterCheckoutEnt).Scan(&ents, &purchases, &consumed)
		return
	}
	ents0, purchases0, consumed0 := counts()

	// THE STAY'S OWN DEVICE: by surname, then by reservation number (the site accepts either), each time onto the same entitlement.
	for i, how := range []struct{ last, res string }{{"Okonkwo", ""}, {"", "RES-4001"}} {
		reqID := f.reqID(t, 10+i)
		res := f.resolveAsDevice(t, f.net.guestIP, f.net.mac, how.last, how.res, reqID)
		if res.Outcome != outcomeVerified || !res.LiveAccess || res.AuthContextID == "" || len(res.Offers) != 0 {
			t.Fatalf("sign-in %d: a departed guest with valid After Check-out Access was not verified onto live access: %+v", i, res)
		}
		g := f.joinLive(t, res.AuthContextID, f.net.guestIP, f.net.mac)
		if g.Outcome != outcomeVerified {
			t.Fatalf("sign-in %d: the stay's own device was not let back in: %+v", i, g)
		}
		if got := f.attemptResult(t, reqID); got != string(signinattempt.Verified) {
			t.Fatalf("sign-in %d recorded %s, want VERIFIED", i, got)
		}
		var sessEnt, state string
		if err := f.pool.QueryRow(ctx, `SELECT entitlement_id::text, state FROM iam_v2.sessions s JOIN iam_v2.devices d ON d.id = s.device_id
			WHERE d.mac = $1::macaddr AND s.ended IS NULL ORDER BY s.started DESC LIMIT 1`, f.net.mac).Scan(&sessEnt, &state); err != nil {
			t.Fatalf("sign-in %d: no live session: %v", i, err)
		}
		if sessEnt != f.afterCheckoutEnt || state != "active" {
			t.Fatalf("sign-in %d: the session is on %s (%s), want the After Check-out entitlement %s", i, sessEnt, state, f.afterCheckoutEnt)
		}
	}
	if ents, purchases, consumed := counts(); ents != ents0 || purchases != purchases0 || consumed != consumed0 {
		t.Fatalf("entitlements %d->%d, purchases %d->%d, used %d->%d: a rejoin must create, buy and reset nothing",
			ents0, ents, purchases0, purchases, consumed0, consumed)
	}

	// A NEW DEVICE with the right room and surname: refused, told so, and recorded as exactly that.
	newMAC := "02:00:00:bb:09:0c"
	reqID := f.reqID(t, 20)
	res := f.resolveAsDevice(t, f.net.otherIP, newMAC, "Okonkwo", "", reqID)
	if res.Outcome != outcomeVerified || !res.LiveAccess {
		t.Fatalf("the departed stay did not verify for the new device: %+v", res)
	}
	refused := f.joinLive(t, res.AuthContextID, f.net.otherIP, newMAC)
	if refused.Outcome != outcomeNotVerified || refused.FailureClass != string(signinattempt.GuestAfterCheckoutDevice) {
		t.Fatalf("a new device after check-out must be told AFTER_CHECKOUT_DEVICE: %+v", refused)
	}
	if got := f.attemptResult(t, reqID); got != string(signinattempt.AfterCheckoutNewDeviceRejected) {
		t.Fatalf("the new device's refusal was recorded as %s, want AFTER_CHECKOUT_NEW_DEVICE_REJECTED", got)
	}
	var placed int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_devices ed JOIN iam_v2.devices d ON d.id = ed.device_id
		WHERE d.mac = $1::macaddr`, newMAC).Scan(&placed)
	if placed != 0 {
		t.Fatalf("the refused device holds %d places", placed)
	}

	// A WRONG SURNAME on the departed stay is a credential mismatch, not "not eligible".
	reqID = f.reqID(t, 30)
	if res := f.resolveAsDevice(t, f.net.guestIP, f.net.mac, "Nobody", "", reqID); res.Outcome == outcomeVerified {
		t.Fatalf("a wrong surname verified: %+v", res)
	}
	if got := f.attemptResult(t, reqID); got != string(signinattempt.CredentialMismatch) {
		t.Fatalf("a wrong surname on a departed stay with valid access was recorded as %s, want CREDENTIAL_MISMATCH", got)
	}

	// ONCE THE AFTER CHECK-OUT ACCESS HAS ENDED, the departed stay is not eligible, as before.
	if _, err := controlledExec(ctx, f.pool, "commerce_intent",
		`SELECT iam_v2.terminate_entitlement_at_boundary($1::uuid, now(), 'ADMIN')`, f.afterCheckoutEnt); err != nil {
		t.Fatalf("end the After Check-out entitlement: %v", err)
	}
	reqID = f.reqID(t, 40)
	if res := f.resolveAsDevice(t, f.net.guestIP, f.net.mac, "Okonkwo", "", reqID); res.Outcome == outcomeVerified {
		t.Fatalf("a departed stay whose After Check-out Access ended still verified: %+v", res)
	}
	if got := f.attemptResult(t, reqID); got != string(signinattempt.StayNotEligible) {
		t.Fatalf("recorded %s, want STAY_NOT_ELIGIBLE once the After Check-out Access has ended", got)
	}
}
