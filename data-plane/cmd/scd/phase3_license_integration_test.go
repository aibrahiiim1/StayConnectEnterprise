//go:build integration

package main

// ROOM SIGN-IN ANSWERS TO THE SAME LICENCE AS EVERY OTHER GUEST METHOD.
//
// Product-Owner decision: PMS room sign-in follows the same licence and concurrent-capacity contract as
// vouchers, accounts, OTP and social sign-in. Before this, the room grant inserted its session directly --
// no licence gate, no concurrent-guest reservation -- so an unlicensed, expired or full appliance still put
// every PMS guest online. These tests drive the real resolve and grant handlers against a real PostgreSQL with
// a real vendor-signed licence installed, and assert three things for every refusal: what the guest is told,
// what the operator's attempt record says, and that nothing was granted.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

// otherDeviceBody is a resolve submission from a SECOND device on the same guest network: the same room and
// name, a different address and hardware address. It is how a guest's second phone signs in.
func (f *authFixture) otherDeviceBody(reqID string) map[string]any {
	return map[string]any{
		"room": "412", "last_name": "Okonkwo", "request_id": reqID,
		"device": map[string]string{"ip": f.net.otherIP, "mac": "02:00:00:cc:00:01"},
	}
}

func (f *authFixture) attemptResult(t *testing.T, reqID string) string {
	t.Helper()
	var r string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT result FROM iam_v2.sign_in_attempts WHERE tenant_id=$1 AND site_id=$2 AND request_id=$3::uuid`,
		f.tenant, f.site, reqID).Scan(&r); err != nil {
		t.Fatalf("no attempt recorded for %s: %v", reqID, err)
	}
	return r
}

// grantOffered drives the real grant for the first package the resolve offered this device.
func (f *authFixture) grantOffered(t *testing.T, res phase3Response, ip, mac string) (*httptest.ResponseRecorder, phase3GrantResp) {
	t.Helper()
	if len(res.Offers) == 0 {
		t.Fatalf("setup: nothing was offered: %+v", res)
	}
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": res.Offers[0].PackageRevisionID,
		"device":              map[string]string{"ip": ip, "mac": mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3GrantResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable grant response %q: %v", rec.Body.String(), err)
	}
	return rec, out
}

func (f *authFixture) openSessions(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM iam_v2.sessions
		 WHERE tenant_id=$1 AND site_id=$2 AND ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT')`,
		f.tenant, f.site).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Every licence state that refuses a voucher refuses a room guest, BEFORE any evidence is evaluated, with the
// same code, the LICENSE class, and a LICENSE_REFUSED attempt that does not count against the device.
func TestIntegration_Phase3License_RefusalsMatchTheOtherMethods(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(f *authFixture)
		wantCode string
	}{
		{"expired licence", func(f *authFixture) { f.srv.lic = signedLicence(t, 0, true, true) }, "license_expired"},
		{"no licence on a production appliance", func(f *authFixture) {
			f.srv.lic = licstate.New(nil, "", t.TempDir(), "", true)
		}, "unlicensed"},
		{"PMS not entitled", func(f *authFixture) { f.srv.lic = signedLicence(t, 0, false, false) }, "feature_not_licensed"},
		{"cross-tenant transition pending", func(f *authFixture) { f.srv.tenantBlocked.Store(true) }, "tenant_transition_pending"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t)
			tc.setup(f)
			reqID := []string{
				"00001c01-0000-4000-8000-000000000000", "00001c02-0000-4000-8000-000000000000",
				"00001c03-0000-4000-8000-000000000000", "00001c04-0000-4000-8000-000000000000"}[i]
			_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
			if res.Outcome != outcomeNotVerified || res.AuthContextID != "" || len(res.Offers) != 0 {
				t.Fatalf("a licence refusal still verified the guest: %+v", res)
			}
			if res.FailureClass != string(signinattempt.GuestLicense) || res.Error != tc.wantCode {
				t.Fatalf("got class %q code %q, want LICENSE %q", res.FailureClass, res.Error, tc.wantCode)
			}
			if got := f.attemptResult(t, reqID); got != string(signinattempt.LicenseRefused) {
				t.Fatalf("the attempt was recorded as %s, want LICENSE_REFUSED", got)
			}
			if signinattempt.LicenseRefused.CountsAsCredentialFailure() {
				t.Fatal("a licence refusal must never restrict the device that met it")
			}
			if n := f.openSessions(t); n != 0 {
				t.Fatalf("a refused sign-in left %d open sessions", n)
			}
		})
	}
}

// The licence is asked again at the grant: a licence that lapses between the resolve and the grant grants
// nothing, and the context stays unconsumed.
func TestIntegration_Phase3License_TheGrantAsksAgain(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	reqID := "00001c11-0000-4000-8000-000000000000"
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}
	f.srv.lic = signedLicence(t, 0, true, true) // the licence expired in between

	_, out := f.grantVia(t, res.AuthContextID, f.net.guestIP, f.net.mac)
	if out.Outcome == outcomeVerified || out.SessionID != "" {
		t.Fatalf("an expired licence still granted a session: %+v", out)
	}
	c := f.census(t, res.AuthContextID)
	if c.contextConsumed || c.sessions != 0 || c.entitlements != 0 {
		t.Fatalf("a refused grant left state behind: %+v", c)
	}
	if got := f.attemptResult(t, reqID); got != string(signinattempt.LicenseRefused) {
		t.Fatalf("the attempt was completed as %s, want LICENSE_REFUSED", got)
	}
}

// THE CONCURRENT-GUEST CAP covers first sign-in, a second device joining the stay, and a device rejoining
// after its session ended -- each opens a new session, so each takes a licensed slot.
func TestIntegration_Phase3License_CapacityCoversFirstJoinAndRejoin(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.lic = signedLicence(t, 1, true, false) // one concurrent guest on this appliance
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	// FIRST SIGN-IN takes the only slot.
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00001c21-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}
	_, first := f.grantVia(t, res.AuthContextID, f.net.guestIP, f.net.mac)
	if first.Outcome != outcomeVerified || first.SessionID == "" {
		t.Fatalf("the first guest was refused under a free licence slot: %+v", first)
	}

	// The fixture has ONE free package and the stay now holds it. The second device is still offered it --
	// choosing it joins the stay's live entitlement rather than buying it again (phase3_offers.go).

	// SECOND DEVICE JOINING the same stay: verified, then refused at the grant with the capacity answer.
	joinReq := "00001c22-0000-4000-8000-000000000000"
	_, res2 := post(t, f.p3.resolveHandler, f.otherDeviceBody(joinReq))
	if res2.Outcome != outcomeVerified {
		t.Fatalf("setup: the second device did not verify: %+v", res2)
	}
	rec, join := f.grantOffered(t, res2, f.net.otherIP, "02:00:00:cc:00:01")
	if join.Outcome == outcomeVerified || join.SessionID != "" {
		t.Fatalf("a second device was admitted past the licensed capacity: %+v", join)
	}
	var refusal phase3Response
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.FailureClass != string(signinattempt.GuestCapacity) || refusal.Error != "LICENSE_CAPACITY_REACHED" {
		t.Fatalf("the capacity refusal reads %+v; want class CAPACITY, error LICENSE_CAPACITY_REACHED", refusal)
	}
	if got := f.attemptResult(t, joinReq); got != string(signinattempt.LicenseCapacityReached) {
		t.Fatalf("the attempt was completed as %s, want LICENSE_CAPACITY_REACHED", got)
	}
	// The refusal rolled the whole grant back: the second device's proof is still usable once there is room.
	var consumed bool
	if err := f.pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM iam_v2.auth_contexts WHERE id=$1`,
		res2.AuthContextID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed {
		t.Fatal("a capacity refusal consumed the second device's Auth Context")
	}
	if n := f.openSessions(t); n != 1 {
		t.Fatalf("%d open sessions under a one-guest licence", n)
	}

	// The first device's session ends; the slot is free again.
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason='admin'
		WHERE id=$1::uuid`, first.SessionID); err != nil {
		t.Fatal(err)
	}
	_, joined := f.grantOffered(t, res2, f.net.otherIP, "02:00:00:cc:00:01")
	if joined.Outcome != outcomeVerified || joined.SessionID == "" {
		t.Fatalf("the second device was refused once the slot was free: %+v", joined)
	}

	// REJOIN: the first device signs in again while the second holds the slot -- refused; nothing opened.
	rejoinReq := "00001c23-0000-4000-8000-000000000000"
	_, res3 := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", rejoinReq))
	if res3.Outcome != outcomeVerified {
		t.Fatalf("setup: the returning device did not verify: %+v", res3)
	}
	_, rejoin := f.grantOffered(t, res3, f.net.guestIP, f.net.mac)
	if rejoin.Outcome == outcomeVerified {
		t.Fatalf("a rejoining device was admitted past the licensed capacity: %+v", rejoin)
	}
	if got := f.attemptResult(t, rejoinReq); got != string(signinattempt.LicenseCapacityReached) {
		t.Fatalf("the rejoin attempt was completed as %s, want LICENSE_CAPACITY_REACHED", got)
	}
	if n := f.openSessions(t); n != 1 {
		t.Fatalf("%d open sessions under a one-guest licence", n)
	}
}
