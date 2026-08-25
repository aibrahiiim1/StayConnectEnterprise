//go:build integration

package main

// ROOM SIGN-IN DURING A PMS OUTAGE, through the handlers scd actually serves, against a real PostgreSQL.
//
// The Product Owner's rule is that a live PMS socket must not be required for a new Room sign-in when
// StayConnect already holds trustworthy PMS-derived Stay data locally. iam_v2.p3_feed_authorizes is where that
// rule lives and internal/authctx pins the predicate directly; what those cannot show is whether a guest can
// actually walk the path while the interface is down — whether the resolver, the Auth Context, the offer
// engine and the grant all still function with no PMS behind them.
//
// THE REQUEST BODIES HERE ARE THE ONES PORTALD SENDS. Not a paraphrase: `room` plus exactly one verification
// field plus `request_id` plus a `device` the appliance derived, which is the shape
// cmd/portald/pms_phase3_forwarding_test.go pins on the other side of the hop. The two suites meet at that
// shape deliberately — the defect corrected in PC-0002 was a portald struct that dropped fields scd expected,
// and it survived precisely because each side was tested against its own idea of the contract.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// postGrant posts a grant and decodes it as the grant response, which carries the Session and Entitlement the
// plain phase3Response does not.
func postGrant(t *testing.T, f *authFixture, contextID, pkgRev string) phase3GrantResp {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id": contextID, "package_revision_id": pkgRev,
		"device": map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	rec := httptest.NewRecorder()
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3GrantResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable grant response %q: %v", rec.Body.String(), err)
	}
	return out
}

// takeInterfaceOffline reproduces what pmsd writes when a real PMS transport fails: the socket is gone, a
// resync is required before live events can be trusted again, and the mirror is left exactly as it was.
//
// Continuity stays CONTINUOUS because no events were MISSED — the feed stopped rather than skipping. That
// distinction is the whole basis for trusting the mirror, so it is set explicitly rather than inherited.
func takeInterfaceOffline(t *testing.T, f *authFixture, errCode string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET transport_status='DISCONNECTED', transport_error_code=$2,
		    sync_status='RESYNC_REQUIRED', continuity_status='CONTINUOUS',
		    disconnected_since=now(), resync_started_at=NULL,
		    last_complete_sync_at=now() - interval '3 hours',
		    updated_at=now()
		WHERE tenant_id=$1`, f.tenant, errCode); err != nil {
		t.Fatalf("take interface offline: %v", err)
	}
}

// THE RULE, walked by a guest. Protel is unreachable; the guest signs in anyway and ends up with a Session.
func TestIntegration_Phase3Auth_SignsInWhilePMSIsDisconnected(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	takeInterfaceOffline(t, f, "DIAL_FAILED")

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000041-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || res.AuthContextID == "" {
		t.Fatalf("a guest whose Stay is mirrored locally could not sign in while the PMS transport was down: "+
			"%+v. The socket being unreachable is not evidence that the local guest list is wrong", res)
	}
	if len(res.Offers) != 1 || res.Offers[0].PackageRevisionID != f.pkgRev {
		t.Fatalf("offers during an outage = %+v, want exactly the included package. The offer engine reads "+
			"local package configuration and must not depend on the PMS either", res.Offers)
	}

	grant := postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)
	if grant.Outcome != outcomeVerified || grant.SessionID == "" {
		t.Fatalf("the grant refused a verified offline guest: %+v", grant)
	}

	// The Session is real, and it is not marked as provisional or degraded anywhere: an outage sign-in is a
	// normal sign-in, which is the point of the rule.
	var sessions int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.sessions s
		   JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		  WHERE e.stay_id=$1`, f.stay).Scan(&sessions); err != nil {
		t.Fatalf("session count: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("sessions after an offline sign-in = %d, want 1", sessions)
	}
}

// SCENARIO 3 — AN OUTAGE MUST NOT CUT OFF A GUEST WHO IS ALREADY ONLINE.
//
// This is the one that would hurt most in a real hotel: a guest signs in while the PMS is healthy, the
// interface drops an hour later, and their Internet keeps working. Entitlements and Sessions never consulted
// the feed predicate, and this asserts that they still do not.
func TestIntegration_Phase3Auth_DisconnectDoesNotRevokeExistingAccess(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000042-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: healthy-feed sign-in failed: %+v", res)
	}
	grant := postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)
	if grant.Outcome != outcomeVerified || grant.SessionID == "" {
		t.Fatalf("setup: grant failed: %+v", grant)
	}

	before := entitlementAndSessionState(t, f, grant.SessionID)
	takeInterfaceOffline(t, f, "DIAL_FAILED")
	after := entitlementAndSessionState(t, f, grant.SessionID)

	if before != after {
		t.Fatalf("a PMS disconnect changed an already-authorised guest's access from %q to %q. An outage must "+
			"never take Internet away from someone who is already online", before, after)
	}
}

func entitlementAndSessionState(t *testing.T, f *authFixture, sessionID string) string {
	t.Helper()
	var state string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT COALESCE(e.status,'?') || '/' || COALESCE(s.state,'?')
		  FROM iam_v2.sessions s
		  LEFT JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		 WHERE s.id=$1`, sessionID).Scan(&state); err != nil {
		t.Fatalf("read entitlement/session state: %v", err)
	}
	return state
}

// SCENARIO 4 — A CHECKED-OUT GUEST IS STILL REFUSED WHILE THE PMS IS DOWN.
//
// The local-mirror rule relaxes the FEED requirement and nothing else. Stay lifecycle is judged from the same
// locally persisted state, so a guest the mirror records as checked out is refused exactly as they would be on
// a live feed — and Checkout-Grace, not Room sign-in, remains the path that serves them.
func TestIntegration_Phase3Auth_CheckedOutStayIsRefusedWhileOffline(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	takeInterfaceOffline(t, f, "DIAL_FAILED")
	if _, err := f.pool.Exec(context.Background(),
		// effective_checkout_at is required by stays_checkedout_needs_boundary: a checked-out Stay must carry
		// the boundary that Checkout-Grace is measured from, so there is no such thing as a checkout without one.
		`UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id=$1`,
		f.stay); err != nil {
		t.Fatalf("check the stay out: %v", err)
	}

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000043-0000-4000-8000-000000000000"))
	if res.Outcome == outcomeVerified {
		t.Fatal("a checked-out guest signed in during a PMS outage. The offline branch relaxes the feed " +
			"requirement, never the Stay's own eligibility")
	}
}

// SCENARIO 7 — A WRONG VERIFICATION VALUE STILL FAILS, and fails IDENTICALLY.
//
// During an outage the resolver is comparing against local data, which is exactly when an implementation is
// tempted to be helpful. It must not be: a wrong surname offline and a wrong surname online are the same
// answer, or the portal becomes an occupancy oracle precisely when nobody is watching the PMS.
func TestIntegration_Phase3Auth_WrongVerificationStillFailsWhileOffline(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	takeInterfaceOffline(t, f, "DIAL_FAILED")

	recOK, _ := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000044-0000-4000-8000-000000000000"))
	recBad, bad := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "NotTheGuest", "", "00000045-0000-4000-8000-000000000000"))

	if bad.Outcome == outcomeVerified {
		t.Fatal("a wrong verification value verified while the PMS was down")
	}
	if recBad.Code == recOK.Code && recBad.Body.String() == recOK.Body.String() {
		return // identical envelopes are the desired outcome for the failure case
	}
	recMissing, _ := post(t, f.p3.resolveHandler,
		f.resolveBody("999", "Okonkwo", "", "00000046-0000-4000-8000-000000000000"))
	if recBad.Body.String() != recMissing.Body.String() || recBad.Code != recMissing.Code {
		t.Fatalf("offline failures are distinguishable: wrong-name %d %q vs unknown-room %d %q",
			recBad.Code, recBad.Body.String(), recMissing.Code, recMissing.Body.String())
	}
}

// SCENARIO 6, at the handler rather than the predicate — a never-synchronised interface refuses everyone while
// disconnected. There is no local guest list to answer from, and answering anyway would authorise from an
// empty table.
func TestIntegration_Phase3Auth_NeverSynchronisedRefusesWhileOffline(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET transport_status='DISCONNECTED', transport_error_code='DIAL_FAILED',
		    sync_status='RESYNC_REQUIRED', continuity_status='CONTINUOUS',
		    last_complete_sync_at=NULL, resync_started_at=NULL, updated_at=now()
		WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatalf("seed never-synchronised runtime: %v", err)
	}

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000047-0000-4000-8000-000000000000"))
	if res.Outcome == outcomeVerified {
		t.Fatal("an interface that has never completed a sync authorised a guest while disconnected")
	}
}

// SCENARIO 5, at the handler — an unresolved continuity gap refuses everyone, offline included. The missing
// events are exactly the ones that would have said this guest had left.
func TestIntegration_Phase3Auth_ContinuityGapRefusesWhileOffline(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	takeInterfaceOffline(t, f, "DIAL_FAILED")
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET continuity_status='GAP_DETECTED', discontinuity_detected_at=now(), updated_at=now()
		WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatalf("mark continuity gap: %v", err)
	}

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000048-0000-4000-8000-000000000000"))
	if res.Outcome == outcomeVerified {
		t.Fatal("a guest was authorised from a mirror with an unresolved continuity gap")
	}
}

// SCENARIO 8/9 — MULTI-PMS NAMESPACING AND AMBIGUITY ARE UNCHANGED BY THE OUTAGE.
//
// Room numbers are scoped per interface and STRICT resolution is automatic, neither of which is a property of
// the transport. An offline path that started searching more widely — because "the PMS is down, let us be
// helpful" — would hand one hotel's guest another hotel's Stay.
func TestIntegration_Phase3Auth_NamespacingAndAmbiguitySurviveTheOutage(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	takeInterfaceOffline(t, f, "DIAL_FAILED")

	// The fixture already seeds two Stays that collide on room 412 + surname "Shared" — the same ambiguity
	// TestIntegration_Phase3Auth_AmbiguityGrantsNothing uses on a healthy feed. Reusing it means this test
	// asserts the outage changes nothing, rather than asserting a differently-constructed ambiguity.
	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Shared", "", "00000049-0000-4000-8000-000000000000"))
	if res.Outcome == outcomeVerified {
		t.Fatal("an ambiguous match verified during an outage. Ambiguity must fail closed whether or not the " +
			"PMS is reachable, or the answer depends on which row was found first")
	}
}
