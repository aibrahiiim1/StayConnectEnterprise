//go:build integration

package main

// After Check-out sign-in end to end on scd: room sign-in, checkout, the room + reservation check (AD-36 as amended 2026-10-30),
// the rejoin of the stay's ONE After Check-out entitlement, enforced SESSION. There is no package to choose and nothing is
// granted here. Calls carry exactly the bodies portald forwards (phase5_poststay_handlers.go); scd uses DisallowUnknownFields
// so drift fails here. The portald half is pinned in cmd/portald/phase5_poststay_flow_test.go. No real guest data.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/poststay"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
)

type postStayFixture struct {
	*authFixture
	ps *phase5PostStay
	// afterCheckoutEnt is the stay's After Check-out entitlement once grantAfterCheckout has run.
	afterCheckoutEnt string
}

func newPostStayFixture(t *testing.T) *postStayFixture {
	t.Helper()
	f := newAuthFixture(t)
	thr, err := throttle.New(f.pool, []byte("post-stay-e2e-key-0123456789abcdef"), time.Minute)
	if err != nil {
		t.Fatalf("throttle: %v", err)
	}
	return &postStayFixture{authFixture: f, ps: &phase5PostStay{
		srv: f.srv, p3: f.p3, store: poststay.New(f.pool, thr), ctxs: authctx.NewStore(f.pool),
		contextTTL: 10 * time.Minute,
	}}
}

// grantAfterCheckout writes what the checkout converter writes for a stay that held an active entitlement at the boundary: one
// After Check-out entitlement, ending in two hours, carrying over the device that was on the stay. (internal/checkout's own
// tests run the real converter; this harness has no PMS event stream.)
func (f *postStayFixture) grantAfterCheckout(t *testing.T, device string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, fam := range []string{"commerce_intent", "device_auth"} {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, fam); err != nil {
			t.Fatal(err)
		}
	}
	var purchase string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state,checkout_episode)
		VALUES ($1,$2,$3,(SELECT pms_interface_id FROM iam_v2.stays WHERE id=$4),$4,'CHECKOUT_GRACE',0,'GRANTED',1) RETURNING id::text`,
		f.tenant, f.site, f.pkgRev, f.stay).Scan(&purchase); err != nil {
		t.Fatalf("after-checkout purchase: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,window_ends_at,status,activated_at)
		SELECT $1,$2,$3,(SELECT pms_interface_id FROM iam_v2.stays WHERE id=$3),$4,'{}'::jsonb, ipr.service_plan_revision_id, $5,
		       'VALIDITY_WINDOW','GRACE_AFTER_CHECKOUT', now()+interval '2 hours','ACTIVE', now()
		  FROM iam_v2.internet_package_revisions ipr WHERE ipr.id=$5 RETURNING id::text`,
		f.tenant, f.site, f.stay, purchase, f.pkgRev).Scan(&f.afterCheckoutEnt); err != nil {
		t.Fatalf("after-checkout entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),'GRACE_CONVERSION')`, f.afterCheckoutEnt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id,site_id,entitlement_id,device_id,status,grandfathered,first_authorized,last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED',true,now(),now())`, f.tenant, f.site, f.afterCheckoutEnt, device); err != nil {
		t.Fatalf("carry the device over: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at) VALUES ($1,$2,$3,$4,1,now())`,
		f.tenant, f.site, f.afterCheckoutEnt, device); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *postStayFixture) device() map[string]string {
	return map[string]string{"ip": f.net.guestIP, "mac": f.net.mac}
}

// answers is what the guest types: the room and the reservation of the stay the fixture's device signed in on.
func (f *postStayFixture) answers() map[string]any {
	return map[string]any{"device": f.device(), "room": "412", "reservation": "RES-4001"}
}

func psCall(t *testing.T, h http.HandlerFunc, body map[string]any) psResponse {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out psResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable post-stay response %q: %v", rec.Body.String(), err)
	}
	return out
}

// signInByRoomThenCheckOut gives the device a room-sign-in lineage, then checks the stay out as the checkout
// path does (entitlement terminated, device interval closed, session ended, CHECKED_OUT with posting off).
func (f *postStayFixture) signInByRoomThenCheckOut(t *testing.T) string {
	t.Helper()
	soleOccupant(t, f.authFixture)
	stopRoomOwner := f.startEnforcementOwner(t)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", f.reqID(t, 0)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("room sign-in did not verify: %+v", res)
	}
	_, g := f.grantVia(t, res.AuthContextID, f.net.guestIP, f.net.mac)
	if g.SessionID == "" {
		t.Fatalf("room grant opened no session: %+v", g)
	}
	stopRoomOwner()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{
		`SELECT iam_v2.begin_controlled_operation('stay')`,
		`SELECT iam_v2.begin_controlled_operation('device_auth')`,
		`SELECT iam_v2.terminate_entitlement_at_boundary('` + g.EntitlementID + `'::uuid, now(), 'CHECKOUT')`,
		`UPDATE iam_v2.entitlement_device_authorizations SET deauthorized_at = GREATEST(now(), authorized_at)
		  WHERE entitlement_id='` + g.EntitlementID + `' AND deauthorized_at IS NULL`,
		`UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED', disconnected_reason='CHECKOUT_BOUNDARY'
		  WHERE entitlement_id='` + g.EntitlementID + `'`,
		`UPDATE iam_v2.sessions SET state='ended', ended=GREATEST(now(), started), end_reason='CHECKOUT_BOUNDARY'
		  WHERE entitlement_id='` + g.EntitlementID + `' AND ended IS NULL`,
		`UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=now()
		  WHERE id='` + f.stay + `'`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("checkout step %q: %v", q, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("checkout commit: %v", err)
	}
	var device string
	if err := f.pool.QueryRow(ctx, `SELECT device_id::text FROM iam_v2.sessions WHERE id=$1`, g.SessionID).Scan(&device); err != nil {
		t.Fatalf("the room session's device: %v", err)
	}
	return device
}

func TestIntegration_PostStay_RoomAndReservationToEnforcedSessionEndToEnd(t *testing.T) {
	f := newPostStayFixture(t)
	device := f.signInByRoomThenCheckOut(t)
	f.grantAfterCheckout(t, device)
	defer f.startEnforcementOwnerAtEpoch(t, 2)()

	// verify: the room and reservation of the stay this device signed in on. There are no offers: nothing is granted here.
	ver := psCall(t, f.ps.verifyHandler, f.answers())
	if ver.Outcome != psOutcomeVerified || ver.AuthContextID == "" {
		t.Fatalf("the room and reservation did not verify: %+v", ver)
	}

	// convert: no package in the body (a package field is refused like any unknown field)
	if out := psCall(t, f.ps.convertHandler, map[string]any{
		"device": f.device(), "auth_context_id": ver.AuthContextID, "package_revision_id": "00000000-0000-0000-0000-000000000001"}); out.Outcome != psOutcomeUnavailab {
		t.Fatalf("a conversion carrying a package was answered %+v", out)
	}
	conv := psCall(t, f.ps.convertHandler, map[string]any{"device": f.device(), "auth_context_id": ver.AuthContextID})
	if conv.Outcome != psOutcomeGranted || conv.SessionID == "" || conv.EntitlementID == "" {
		t.Fatalf("the rejoin did not give a session: %+v (the refused attempt must not spend the context)", conv)
	}
	if conv.SessionID == conv.EntitlementID {
		t.Fatal("the session id is the entitlement id again; no session was opened")
	}
	if conv.EntitlementID != f.afterCheckoutEnt {
		t.Fatalf("the device joined entitlement %s, want the stay's After Check-out entitlement %s", conv.EntitlementID, f.afterCheckoutEnt)
	}

	// The session is an enforced row on THE entitlement, for this device, labelled post-stay; the stay stays CHECKED_OUT.
	var state, method, sessEnt, entStatus, stayStatus string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT s.state, COALESCE(s.credential_method,''), s.entitlement_id::text, e.status, st.status
		  FROM iam_v2.sessions s
		  JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		  JOIN iam_v2.stays st ON st.id = e.stay_id
		 WHERE s.id = $1`, conv.SessionID).Scan(&state, &method, &sessEnt, &entStatus, &stayStatus); err != nil {
		t.Fatalf("the returned session id names no session: %v", err)
	}
	if state != "active" || method != postStaySessionMethod || sessEnt != conv.EntitlementID ||
		entStatus != "ACTIVE" || stayStatus != "CHECKED_OUT" {
		t.Fatalf("session state=%s method=%s ent=%s entStatus=%s stay=%s", state, method, sessEnt, entStatus, stayStatus)
	}

	// A retry (lost reply) returns the same session rather than refusing.
	again := psCall(t, f.ps.convertHandler, map[string]any{"device": f.device(), "auth_context_id": ver.AuthContextID})
	if again.Outcome != psOutcomeGranted || again.SessionID != conv.SessionID {
		t.Fatalf("a retried rejoin did not return the session it opened: %+v", again)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND trigger IN ('POST_STAY_CONVERSION')`, f.stay).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d post-stay purchases (%v), want none: the rejoin grants nothing", n, err)
	}
}

// A stay that holds no After Check-out entitlement (nothing eligible at checkout, the site grants none, or it ended) opens
// nothing: the check does not even produce a context, whatever is typed.
func TestIntegration_PostStay_NoAfterCheckoutEntitlementMeansNoContext(t *testing.T) {
	f := newPostStayFixture(t)
	f.signInByRoomThenCheckOut(t) // checked out, no After Check-out entitlement granted
	defer f.startEnforcementOwnerAtEpoch(t, 2)()
	if ver := psCall(t, f.ps.verifyHandler, f.answers()); ver.Outcome != psOutcomeUnavailab || ver.AuthContextID != "" {
		t.Fatalf("verify on a stay with no After Check-out access answered %+v", ver)
	}
}

// A device that was not on the stay is refused with the CORRECT room and reservation (the surname is not a field any more).
func TestIntegration_PostStay_ANewDeviceIsRefusedEvenWithTheRightCredentials(t *testing.T) {
	f := newPostStayFixture(t)
	device := f.signInByRoomThenCheckOut(t)
	f.grantAfterCheckout(t, device)
	defer f.startEnforcementOwnerAtEpoch(t, 2)()
	other := f.answers()
	other["device"] = map[string]string{"ip": f.net.guestIP, "mac": "02:00:00:aa:bb:cc"}
	if out := psCall(t, f.ps.verifyHandler, other); out.Outcome != psOutcomeUnavailab {
		t.Fatalf("a new device with the right room + reservation was answered %+v", out)
	}
	withName := f.answers()
	withName["last_name"] = "Okonkwo"
	if out := psCall(t, f.ps.verifyHandler, withName); out.Outcome != psOutcomeUnavailab {
		t.Fatalf("a request carrying a surname was answered %+v: the surname is not part of the sign-in", out)
	}
}

// AD-36 closure: a device that comes back (its session ended, or the first answer was lost) continues the SAME
// entitlement; it never gets a second one or a fresh allowance. A refused attempt leaves an audit row without guest data.
func TestIntegration_PostStay_ReturningDeviceContinuesTheEntitlementAndRefusalsAreAudited(t *testing.T) {
	f := newPostStayFixture(t)
	device := f.signInByRoomThenCheckOut(t)
	f.grantAfterCheckout(t, device)
	defer f.startEnforcementOwnerAtEpoch(t, 2)()
	ctx := context.Background()

	ver := psCall(t, f.ps.verifyHandler, f.answers())
	conv := psCall(t, f.ps.convertHandler, map[string]any{"device": f.device(), "auth_context_id": ver.AuthContextID})
	if conv.Outcome != psOutcomeGranted {
		t.Fatalf("first rejoin: %+v", conv)
	}

	// Back again while the session is still live (a lost reply): the same entitlement and the same session.
	ver2 := psCall(t, f.ps.verifyHandler, f.answers())
	if ver2.Outcome != psOutcomeVerified {
		t.Fatalf("returning device did not verify: %+v", ver2)
	}
	again := psCall(t, f.ps.convertHandler, map[string]any{"device": f.device(), "auth_context_id": ver2.AuthContextID})
	if again.Outcome != psOutcomeGranted || again.EntitlementID != conv.EntitlementID || again.SessionID != conv.SessionID {
		t.Fatalf("returning device: %+v, want entitlement %s and session %s again", again, conv.EntitlementID, conv.SessionID)
	}

	// Back after the session ended: a new session on the SAME entitlement.
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason='TEST' WHERE id=$1`, conv.SessionID); err != nil {
		t.Fatal(err)
	}
	ver3 := psCall(t, f.ps.verifyHandler, f.answers())
	third := psCall(t, f.ps.convertHandler, map[string]any{"device": f.device(), "auth_context_id": ver3.AuthContextID})
	// (This harness's enforcement stand-in re-registers the device's accounting origin only once per session minor, so the
	// reply may stay UNAVAILABLE here; what matters is the database: a new session row on the same entitlement.)
	_ = third
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sessions
		WHERE entitlement_id=$1 AND id<>$2 AND ended IS NULL AND state IN ('active','PENDING_ENFORCEMENT')`,
		conv.EntitlementID, conv.SessionID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d new live sessions on entitlement %s (%v), want exactly 1", n, conv.EntitlementID, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, f.stay).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d After Check-out entitlements (%v), want exactly 1", n, err)
	}

	// A wrong answer is refused the same way and is on record, without the answer or the guest.
	bad := f.answers()
	bad["reservation"] = "WRONG-RES-ZQ9"
	if out := psCall(t, f.ps.verifyHandler, bad); out.Outcome != psOutcomeUnavailab {
		t.Fatalf("wrong reservation: %+v", out)
	}
	var rows int
	var leaked bool
	if err := f.pool.QueryRow(ctx, `SELECT count(*), COALESCE(bool_or(payload::text ~* '(WRONG-RES-ZQ9|RES-4001|Okonkwo)'), false)
		FROM public.audit_log WHERE tenant_id=$1 AND action='post_stay.signin_refused'`, f.tenant).Scan(&rows, &leaked); err != nil {
		t.Fatal(err)
	}
	if rows == 0 || leaked {
		t.Fatalf("refusal audit rows=%d leaked guest data=%v", rows, leaked)
	}
}
