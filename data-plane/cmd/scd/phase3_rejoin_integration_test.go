//go:build integration

package main

// Automatic reconnection (phase3_rejoin.go) and the stale-address refusal on the full schema: a room-signed-in device the network
// disconnected goes back on the SAME entitlement with nothing written but its session; every condition fails closed; a guest's
// logout, staff, a device release, the package ending, another device and an address DHCP does not vouch for all need a sign-in.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/onegate/onegate/data-plane/internal/signinattempt"
)

type fixedOwner string

func (o fixedOwner) AddressOwner(context.Context, net.IP, net.HardwareAddr) (string, error) {
	return string(o), nil
}

func (f *authFixture) rejoin(t *testing.T, ip, mac string) rejoinResp {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"device": map[string]string{"ip": ip, "mac": mac}})
	rec := httptest.NewRecorder()
	f.p3.rejoinHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out rejoinResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable rejoin response %q: %v", rec.Body.String(), err)
	}
	return out
}

// endLive ends the device's live session the way the named actor would record it.
func (f *authFixture) endLive(t *testing.T, mac, reason string) {
	t.Helper()
	var sid string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM iam_v2.sessions
		WHERE mac=$1::macaddr AND ended IS NULL ORDER BY started DESC LIMIT 1`, mac).Scan(&sid); err != nil {
		t.Fatalf("no live session to end: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `SELECT iam_v2.end_session_enforcement($1,$2,$3::uuid,$4)`,
		f.tenant, f.site, sid, reason); err != nil {
		t.Fatalf("end session %s: %v", reason, err)
	}
}

type rjCounts struct{ entitlements, purchases, auths, live int }

func (f *authFixture) rjCounts(t *testing.T) rjCounts {
	t.Helper()
	var c rjCounts
	if err := f.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1),
		(SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1),
		(SELECT count(*) FROM iam_v2.entitlement_device_authorizations a JOIN iam_v2.entitlements e ON e.id=a.entitlement_id WHERE e.stay_id=$1),
		(SELECT count(*) FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id=s.entitlement_id WHERE e.stay_id=$1 AND s.ended IS NULL)`,
		f.stay).Scan(&c.entitlements, &c.purchases, &c.auths, &c.live); err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *authFixture) signInByRoom(t *testing.T, n int) phase3GrantResp {
	t.Helper()
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", f.reqID(t, n)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("room sign-in did not verify: %+v", res)
	}
	if res.LiveAccess {
		g := f.joinLive(t, res.AuthContextID, f.net.guestIP, f.net.mac)
		return phase3GrantResp{Outcome: g.Outcome}
	}
	_, g := f.grantVia(t, res.AuthContextID, f.net.guestIP, f.net.mac)
	if g.SessionID == "" {
		t.Fatalf("room grant opened no session: %+v", g)
	}
	return g
}

func TestIntegration_Rejoin_TheNetworkDisconnectedDeviceReturnsToItsOwnEntitlementOnly(t *testing.T) {
	f := newAuthFixture(t)
	soleOccupant(t, f)
	// A new session on the same address needs a newer class generation, as netd allocates one per session.
	epoch := int64(1)
	stop := f.startEnforcementOwnerAtEpoch(t, epoch)
	defer func() { stop() }()
	next := func() { stop(); epoch++; stop = f.startEnforcementOwnerAtEpoch(t, epoch) }
	f.srv.owners = fixedOwner(addressOwned)
	ctx := context.Background()

	g := f.signInByRoom(t, 1)
	f.endLive(t, f.net.mac, "ADDRESS_NO_LONGER_OWNED")
	before := f.rjCounts(t)
	var consumedBefore int64
	_ = f.pool.QueryRow(ctx, `SELECT consumed_data_bytes FROM iam_v2.entitlements WHERE id=$1`, g.EntitlementID).Scan(&consumedBefore)

	// THE NETWORK ENDED IT: back on the same entitlement, nothing written but the session.
	next()
	out := f.rejoin(t, f.net.guestIP, f.net.mac)
	if out.Outcome != rejoinOutcomeRejoined || out.SessionID == "" {
		t.Fatalf("a device the network disconnected was not reconnected: %+v", out)
	}
	var ent, method, state string
	if err := f.pool.QueryRow(ctx, `SELECT entitlement_id::text, credential_method, state FROM iam_v2.sessions WHERE id=$1`, out.SessionID).
		Scan(&ent, &method, &state); err != nil || ent != g.EntitlementID || method != rejoinMethod || state != "active" {
		t.Fatalf("reconnected session on %s (%s, %s), want %s, %s, active: %v", ent, method, state, g.EntitlementID, rejoinMethod, err)
	}
	after := f.rjCounts(t)
	if after.entitlements != before.entitlements || after.purchases != before.purchases || after.auths != before.auths || after.live != before.live+1 {
		t.Fatalf("a reconnection wrote more than its session: before %+v after %+v", before, after)
	}
	var consumedAfter int64
	_ = f.pool.QueryRow(ctx, `SELECT consumed_data_bytes FROM iam_v2.entitlements WHERE id=$1`, g.EntitlementID).Scan(&consumedAfter)
	if consumedAfter != consumedBefore {
		t.Fatalf("usage changed on reconnection: %d -> %d", consumedBefore, consumedAfter)
	}

	// A LIVE DEVICE is not given a second session, however many reconnections arrive at once.
	var wg sync.WaitGroup
	results := make([]rejoinResp, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = f.rejoin(t, f.net.guestIP, f.net.mac) }(i)
	}
	wg.Wait()
	for _, r := range results {
		if r.Outcome == rejoinOutcomeRejoined {
			t.Fatalf("a device with a live session was reconnected again: %+v", results)
		}
	}
	// ...and concurrent reconnections of a disconnected device open exactly one.
	f.endLive(t, f.net.mac, "ADDRESS_NO_LONGER_OWNED")
	next()
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = f.rejoin(t, f.net.guestIP, f.net.mac) }(i)
	}
	wg.Wait()
	if c := f.rjCounts(t); c.live != 1 {
		t.Fatalf("%d live sessions after concurrent reconnections, want exactly 1", c.live)
	}

	// EVERY NON-NETWORK ENDING needs a fresh sign-in: the guest, staff, the device release, a replacement, the package ending.
	for _, reason := range []string{"LOGOUT", "ADMIN", "DEVICE_RELEASED", "DEVICE_REPLACED", "ENTITLEMENT_ENDED", "NETWORK_REPLACED"} {
		f.endLive(t, f.net.mac, reason)
		if r := f.rejoin(t, f.net.guestIP, f.net.mac); r.Outcome != rejoinOutcomeNotEligible {
			t.Fatalf("a session ended %s was reconnected without a sign-in: %+v", reason, r)
		}
		// A fresh sign-in restores the normal course.
		next()
		f.signInByRoom(t, 10)
	}

	// DHCP MUST VOUCH for the address: unknown and foreign are both refusals.
	f.endLive(t, f.net.mac, "ADDRESS_NO_LONGER_OWNED")
	for _, st := range []string{"unknown", addressForeign} {
		f.srv.owners = fixedOwner(st)
		if r := f.rejoin(t, f.net.guestIP, f.net.mac); r.Outcome != rejoinOutcomeNotEligible {
			t.Fatalf("reconnected with DHCP ownership %q: %+v", st, r)
		}
	}
	f.srv.owners = nil
	if r := f.rejoin(t, f.net.guestIP, f.net.mac); r.Outcome != rejoinOutcomeNotEligible {
		t.Fatalf("reconnected with no way to ask DHCP: %+v", r)
	}
	f.srv.owners = fixedOwner(addressOwned)

	// ANOTHER DEVICE with no authorization on the stay never gets in this way.
	if r := f.rejoin(t, f.net.otherIP, "02:00:00:cc:09:0d"); r.Outcome != rejoinOutcomeNotEligible {
		t.Fatalf("a device the stay never authorized was reconnected: %+v", r)
	}

	// A RELEASED DEVICE (its authorization closed) is not reconnected even after a network ending.
	if _, err := controlledExec(ctx, f.pool, "device_auth", `SELECT iam_v2.deauthorize_entitlement_device($1::uuid,
		(SELECT id FROM iam_v2.devices WHERE mac=$2::macaddr AND tenant_id=$3), now(), 'RELEASED_BY_TEST')`,
		g.EntitlementID, f.net.mac, f.tenant); err != nil {
		t.Fatalf("release the device: %v", err)
	}
	if r := f.rejoin(t, f.net.guestIP, f.net.mac); r.Outcome != rejoinOutcomeNotEligible {
		t.Fatalf("a released device was reconnected: %+v", r)
	}
	// A reconnection never opens an authorization, so a release racing it cannot be undone: the database refuses a session
	// for a device whose authorization is closed, which is the guard a racing reconnection meets.
	var device string
	_ = f.pool.QueryRow(ctx, `SELECT id::text FROM iam_v2.devices WHERE mac=$1::macaddr AND tenant_id=$2`, f.net.mac, f.tenant).Scan(&device)
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.sessions(tenant_id,site_id,entitlement_id,device_id,credential_method,state,started,ip,mac)
		VALUES ($1,$2,$3,$4,'REJOIN','PENDING_ENFORCEMENT',now(),$5::inet,$6::macaddr)`,
		f.tenant, f.site, g.EntitlementID, device, f.net.guestIP, f.net.mac); err == nil {
		t.Fatal("a session was opened for a device whose authorization had been released")
	}
	var open int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NULL`,
		g.EntitlementID, device).Scan(&open)
	if open != 0 {
		t.Fatalf("the released device's authorization was reopened (%d open)", open)
	}
}

// When the package itself has ended, a network-ended session is not reconnected: there is nothing to return to.
func TestIntegration_Rejoin_NoReconnectionOnceThePackageHasEnded(t *testing.T) {
	f := newAuthFixture(t)
	soleOccupant(t, f)
	defer f.startEnforcementOwner(t)()
	f.srv.owners = fixedOwner(addressOwned)
	g := f.signInByRoom(t, 1)
	f.endLive(t, f.net.mac, "ADDRESS_NO_LONGER_OWNED")
	if _, err := controlledExec(context.Background(), f.pool, "commerce_intent",
		`SELECT iam_v2.terminate_entitlement_at_boundary($1::uuid, now(), 'DATA')`, g.EntitlementID); err != nil {
		t.Fatalf("end the package: %v", err)
	}
	if r := f.rejoin(t, f.net.guestIP, f.net.mac); r.Outcome != rejoinOutcomeNotEligible {
		t.Fatalf("reconnected onto an ended package: %+v", r)
	}
}

// A device using an address DHCP has leased to another device is told so before any room is looked up, and nothing is issued.
func TestIntegration_Phase3_AStaleAddressIsRefusedAsAnAddressConflict(t *testing.T) {
	f := newAuthFixture(t)
	soleOccupant(t, f)
	f.srv.owners = fixedOwner(addressForeign)
	reqID := f.reqID(t, 1)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
	if res.Outcome != outcomeNotVerified || res.FailureClass != string(signinattempt.GuestAddressConflict) || res.AuthContextID != "" {
		t.Fatalf("a stale address was answered %+v", res)
	}
	if got := f.attemptResult(t, reqID); got != string(signinattempt.AddressConflict) {
		t.Fatalf("recorded %s, want ADDRESS_CONFLICT", got)
	}
	var contexts int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.auth_contexts WHERE resolution_request_id=$1::uuid`, reqID).Scan(&contexts)
	if contexts != 0 {
		t.Fatalf("%d sign-in contexts issued to a device on another device's address", contexts)
	}
	// "unknown" never refuses a sign-in: netd still checks every session itself.
	f.srv.owners = fixedOwner("unknown")
	if _, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", f.reqID(t, 2))); res.Outcome != outcomeVerified {
		t.Fatalf("an unverifiable address refused a sign-in: %+v", res)
	}
}

// 0210: a retried grant that produced its session replaces a retryable refusal; a success is never downgraded.
func TestIntegration_SignInAttempt_ARetriedSuccessReplacesARetryableRefusal(t *testing.T) {
	f := newAuthFixture(t)
	soleOccupant(t, f)
	defer f.startEnforcementOwner(t)()
	reqID := f.reqID(t, 1)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
	if res.Outcome != outcomeVerified {
		t.Fatalf("resolve: %+v", res)
	}
	_, g := f.grantVia(t, res.AuthContextID, f.net.guestIP, f.net.mac)
	ctx := context.Background()
	exec := func(result, ent, sess string) int {
		var n int
		var e, s any
		if ent != "" {
			e = ent
		}
		if sess != "" {
			s = sess
		}
		if err := f.pool.QueryRow(ctx, `SELECT iam_v2.complete_sign_in_attempt($1,$2,$3::uuid,$4,$5::uuid,$6::uuid)`,
			f.tenant, f.site, reqID, result, e, s).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Put the row back to a recorded refusal, as a first grant that hit the device limit leaves it.
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sign_in_attempts SET result='DEVICE_LIMIT_REACHED', session_id=NULL, entitlement_id=NULL WHERE request_id=$1::uuid`, reqID); err != nil {
		t.Fatal(err)
	}
	if n := exec("VERIFIED", g.EntitlementID, ""); n != 0 {
		t.Fatal("a VERIFIED completion without a session replaced a refusal")
	}
	if n := exec("VERIFIED", g.EntitlementID, g.SessionID); n != 1 || f.attemptResult(t, reqID) != "VERIFIED" {
		t.Fatalf("the retried success did not replace the refusal (%d rows, %s)", n, f.attemptResult(t, reqID))
	}
	if n := exec("SERVICE_UNAVAILABLE", "", ""); n != 0 || f.attemptResult(t, reqID) != "VERIFIED" {
		t.Fatal("a success was downgraded")
	}
}

// D93 with reconnection: a checked-out stay's own device, back on After Check-out Access, that the network then disconnects is
// reconnected onto the After Check-out entitlement; nothing is granted and the stay stays checked out.
func TestIntegration_Rejoin_AfterCheckoutTheStaysOwnDeviceIsReconnected(t *testing.T) {
	f := newPostStayFixture(t)
	device := f.signInByRoomThenCheckOut(t)
	f.grantAfterCheckout(t, device)
	stop := f.startEnforcementOwnerAtEpoch(t, 2)
	f.srv.owners = fixedOwner(addressOwned)
	res := f.resolveAsDevice(t, f.net.guestIP, f.net.mac, "Okonkwo", "", f.reqID(t, 30))
	if g := f.joinLive(t, res.AuthContextID, f.net.guestIP, f.net.mac); g.Outcome != outcomeVerified {
		t.Fatalf("the departed guest's own device did not sign in: %+v", g)
	}
	f.endLive(t, f.net.mac, "ADDRESS_NO_LONGER_OWNED")
	stop()
	defer f.startEnforcementOwnerAtEpoch(t, 3)()
	before := f.rjCounts(t)
	out := f.rejoin(t, f.net.guestIP, f.net.mac)
	if out.Outcome != rejoinOutcomeRejoined {
		t.Fatalf("the stay's own device was not reconnected onto After Check-out Access: %+v", out)
	}
	var ent string
	_ = f.pool.QueryRow(context.Background(), `SELECT entitlement_id::text FROM iam_v2.sessions WHERE id=$1`, out.SessionID).Scan(&ent)
	if ent != f.afterCheckoutEnt {
		t.Fatalf("reconnected onto %s, want the After Check-out entitlement %s", ent, f.afterCheckoutEnt)
	}
	if after := f.rjCounts(t); after.entitlements != before.entitlements || after.purchases != before.purchases || after.auths != before.auths {
		t.Fatalf("the reconnection granted something: before %+v after %+v", before, after)
	}
}

// A paid room sign-in answers PENDING and its session opens later at activation; that session is linked back to the sign-in's
// attempt, for the device that signed in only.
func TestIntegration_SignInAttempt_APaidSessionIsLinkedToItsSignIn(t *testing.T) {
	f := newAuthFixture(t)
	soleOccupant(t, f)
	f.srv.p3auth = f.p3
	ctx := context.Background()
	reqID := f.reqID(t, 1)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
	if res.Outcome != outcomeVerified {
		t.Fatalf("resolve: %+v", res)
	}
	var device, ent, sess string
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`)
	if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.devices WHERE mac=$1::macaddr AND tenant_id=$2`, f.net.mac, f.tenant).Scan(&device); err != nil {
		t.Fatal(err)
	}
	var pur, quote string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.offer_quotes(tenant_id,site_id,auth_context_id,package_revision_id,pms_interface_id,
		price_minor,currency,currency_exponent,grant_snapshot,expires_at,consumed_at)
		VALUES ($1,$2,$3::uuid,$4,$5,1500,'USD',2,'{}'::jsonb,now()+interval '5 minutes',now()) RETURNING id::text`,
		f.tenant, f.site, res.AuthContextID, f.priced, f.iface).Scan(&quote); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases(tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,pms_interface_id,stay_id,trigger,amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5::uuid,$6,$7,'GUEST_SELECTION',1500,'USD',2,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, f.priced, quote, res.AuthContextID, f.iface, f.stay).Scan(&pur); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
		service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status)
		SELECT $1,$2,$3,$4,$5,'{}'::jsonb,r.service_plan_revision_id,r.id,'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE'
		  FROM iam_v2.internet_package_revisions r WHERE r.id=$6 RETURNING id::text`,
		f.tenant, f.site, f.stay, f.iface, pur, f.priced).Scan(&ent); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.sessions(tenant_id,site_id,entitlement_id,device_id,state,started,ip,mac)
		VALUES ($1,$2,$3,$4,'active',now(),$5::inet,$6::macaddr) RETURNING id::text`,
		f.tenant, f.site, ent, device, f.net.guestIP, f.net.mac).Scan(&sess); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Another device activating the same entitlement does not complete this guest's sign-in.
	f.srv.linkPaidSignIn(ctx, ent, "00000000-0000-4000-8000-0000000000ff", sess)
	var linked *string
	_ = f.pool.QueryRow(ctx, `SELECT session_id::text FROM iam_v2.sign_in_attempts WHERE request_id=$1::uuid`, reqID).Scan(&linked)
	if linked != nil {
		t.Fatalf("another device's activation linked this sign-in to %s", *linked)
	}
	f.srv.linkPaidSignIn(ctx, ent, device, sess)
	var result string
	if err := f.pool.QueryRow(ctx, `SELECT result, COALESCE(session_id::text,'') FROM iam_v2.sign_in_attempts WHERE request_id=$1::uuid`, reqID).
		Scan(&result, &sess); err != nil || result != "VERIFIED" || sess == "" {
		t.Fatalf("the paid sign-in was not linked to its session: %s %q %v", result, sess, err)
	}
}
