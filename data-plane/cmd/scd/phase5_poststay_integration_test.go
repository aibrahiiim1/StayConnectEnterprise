//go:build integration

package main

// Post-stay PIN end to end on scd: room sign-in, checkout, PIN issue, verify, post-stay package, enforced SESSION.
// Calls carry exactly the bodies portald forwards (phase5_poststay_handlers.go); scd uses DisallowUnknownFields
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
	ps               *phase5PostStay
	pkgA, pkgB, paid string
}

func newPostStayFixture(t *testing.T) *postStayFixture {
	t.Helper()
	f := newAuthFixture(t)
	thr, err := throttle.New(f.pool, []byte("post-stay-e2e-key-0123456789abcdef"), time.Minute)
	if err != nil {
		t.Fatalf("throttle: %v", err)
	}
	pf := &postStayFixture{authFixture: f, ps: &phase5PostStay{
		srv: f.srv, p3: f.p3, store: poststay.New(f.pool, thr), ctxs: authctx.NewStore(f.pool),
		contextTTL: 10 * time.Minute, pinValid: 24 * time.Hour,
	}}
	// Two free post-stay packages and one priced one, which must never be offered.
	mk := func(code string, price int64, methods string) string {
		var rev, pkg string
		if err := f.pool.QueryRow(context.Background(), `WITH
		  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,active)
		         SELECT gen_random_uuid(), $1, $2, $3, true RETURNING id),
		  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
		            service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
		          SELECT gen_random_uuid(), $1, $2, ip.id, 1,
		                 (SELECT service_plan_revision_id FROM iam_v2.internet_package_revisions WHERE id=$4),
		                 'POST_STAY', $5, $6::text[], '{"duration_minutes":120}'::jsonb FROM ip RETURNING id, package_id)
		SELECT id::text, package_id::text FROM ipr`,
			f.tenant, f.site, code, f.pkgRev, price, methods).Scan(&rev, &pkg); err != nil {
			t.Fatalf("seed post-stay package %s: %v", code, err)
		}
		// A data-modifying CTE cannot see its sibling's insert, so the current-revision pointer is its own step.
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, rev, pkg); err != nil {
			t.Fatalf("publish post-stay package %s: %v", code, err)
		}
		return rev
	}
	pf.pkgA = mk("PS-A", 0, "{NOT_REQUIRED}")
	pf.pkgB = mk("PS-B", 0, "{NOT_REQUIRED}")
	pf.paid = mk("PS-PAID", 500, "{PMS_POSTING}")
	return pf
}

func (f *postStayFixture) device() map[string]string {
	return map[string]string{"ip": f.net.guestIP, "mac": f.net.mac}
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
func (f *postStayFixture) signInByRoomThenCheckOut(t *testing.T) {
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
}

func TestIntegration_PostStay_PINToEnforcedSessionEndToEnd(t *testing.T) {
	f := newPostStayFixture(t)
	f.signInByRoomThenCheckOut(t)
	defer f.startEnforcementOwnerAtEpoch(t, 2)()

	// issue
	iss := psCall(t, f.ps.issueHandler, map[string]any{"device": f.device()})
	if iss.Outcome != psOutcomeIssued || iss.PIN == "" {
		t.Fatalf("no PIN was issued for a checked-out stay this device signed in on: %+v", iss)
	}

	// verify: the reply carries the grantable packages
	ver := psCall(t, f.ps.verifyHandler, map[string]any{"device": f.device(), "pin": iss.PIN})
	if ver.Outcome != psOutcomeVerified || ver.AuthContextID == "" {
		t.Fatalf("the PIN did not verify: %+v", ver)
	}
	got := map[string]bool{}
	for _, o := range ver.Offers {
		got[o.PackageRevisionID] = true
	}
	if len(ver.Offers) != 2 || !got[f.pkgA] || !got[f.pkgB] {
		t.Fatalf("offers = %+v, want exactly the two free post-stay packages", ver.Offers)
	}
	if got[f.paid] {
		t.Fatal("a priced post-stay package was offered; the conversion grants zero-price only")
	}

	// convert
	conv := psCall(t, f.ps.convertHandler, map[string]any{
		"device": f.device(), "auth_context_id": ver.AuthContextID, "package_revision_id": f.pkgB})
	if conv.Outcome != psOutcomeGranted || conv.SessionID == "" || conv.EntitlementID == "" {
		t.Fatalf("the conversion did not grant a session: %+v", conv)
	}
	if conv.SessionID == conv.EntitlementID {
		t.Fatal("the session id is the entitlement id again; no session was opened")
	}

	// The session is an enforced row on the new entitlement, for this device, labelled post-stay; the stay is POST_STAY_ACTIVE.
	var state, method, sessEnt, pkg, entStatus, stayStatus string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT s.state, COALESCE(s.credential_method,''), s.entitlement_id::text, e.package_revision_id::text,
		       e.status, st.status
		  FROM iam_v2.sessions s
		  JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		  JOIN iam_v2.stays st ON st.id = e.stay_id
		 WHERE s.id = $1`, conv.SessionID).Scan(&state, &method, &sessEnt, &pkg, &entStatus, &stayStatus); err != nil {
		t.Fatalf("the returned session id names no session: %v", err)
	}
	if state != "active" || method != postStaySessionMethod || sessEnt != conv.EntitlementID ||
		pkg != f.pkgB || entStatus != "ACTIVE" || stayStatus != "POST_STAY_ACTIVE" {
		t.Fatalf("session state=%s method=%s ent=%s pkg=%s entStatus=%s stay=%s", state, method, sessEnt, pkg,
			entStatus, stayStatus)
	}

	// A retry (lost reply) returns the same session rather than refusing.
	again := psCall(t, f.ps.convertHandler, map[string]any{
		"device": f.device(), "auth_context_id": ver.AuthContextID, "package_revision_id": f.pkgB})
	if again.Outcome != psOutcomeGranted || again.SessionID != conv.SessionID {
		t.Fatalf("a retried conversion did not return the session it opened: %+v", again)
	}
}

// A conversion without a package_revision_id is refused and consumes nothing.
func TestIntegration_PostStay_ConversionWithoutAPackageIsRefused(t *testing.T) {
	f := newPostStayFixture(t)
	f.signInByRoomThenCheckOut(t)
	defer f.startEnforcementOwnerAtEpoch(t, 2)()

	iss := psCall(t, f.ps.issueHandler, map[string]any{"device": f.device()})
	ver := psCall(t, f.ps.verifyHandler, map[string]any{"device": f.device(), "pin": iss.PIN})
	if ver.Outcome != psOutcomeVerified {
		t.Fatalf("verify: %+v", ver)
	}
	if out := psCall(t, f.ps.convertHandler, map[string]any{
		"device": f.device(), "auth_context_id": ver.AuthContextID, "package_revision_id": ""}); out.Outcome != psOutcomeUnavailab {
		t.Fatalf("a conversion without a package was answered %+v", out)
	}
	if out := psCall(t, f.ps.convertHandler, map[string]any{
		"device": f.device(), "auth_context_id": ver.AuthContextID, "package_revision_id": f.paid}); out.Outcome != psOutcomeUnavailab {
		t.Fatalf("a priced post-stay package was converted: %+v", out)
	}
	if out := psCall(t, f.ps.convertHandler, map[string]any{
		"device": f.device(), "auth_context_id": ver.AuthContextID, "package_revision_id": f.pkgA}); out.Outcome != psOutcomeGranted {
		t.Fatalf("the refused attempts spent the context: %+v", out)
	}
}

// A site with no free post-stay package issues no context: the PIN verifies but nothing could be granted.
func TestIntegration_PostStay_NoGrantablePackageMeansNoContext(t *testing.T) {
	f := newPostStayFixture(t)
	f.signInByRoomThenCheckOut(t)
	defer f.startEnforcementOwnerAtEpoch(t, 2)()
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.internet_packages SET active=false
		WHERE tenant_id=$1 AND site_id=$2 AND code IN ('PS-A','PS-B')`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	iss := psCall(t, f.ps.issueHandler, map[string]any{"device": f.device()})
	if ver := psCall(t, f.ps.verifyHandler, map[string]any{"device": f.device(), "pin": iss.PIN}); ver.Outcome != psOutcomeUnavailab || ver.AuthContextID != "" {
		t.Fatalf("verify on a site with nothing to grant answered %+v", ver)
	}
}
