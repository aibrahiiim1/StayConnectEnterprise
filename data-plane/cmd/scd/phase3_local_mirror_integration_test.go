//go:build integration

package main

// Room sign-in during a PMS outage: a live PMS socket is not required when trustworthy Stay data is held
// locally (iam_v2.p3_feed_authorizes). Bodies match cmd/portald/pms_phase3_forwarding_test.go's shape so
// both sides of the hop are tested against one contract.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// postGrant posts a grant and decodes the grant response (Session and Entitlement).
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

// takeInterfaceOffline reproduces what pmsd writes on a transport failure. Continuity stays CONTINUOUS
// because no events were missed (the feed stopped rather than skipping), which is the basis for trusting the mirror.
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

// Protel is unreachable; the guest signs in anyway and ends up with a Session.
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

	// An outage sign-in is a normal sign-in, not marked provisional or degraded.
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

// An outage must not cut off a guest already online: Entitlements and Sessions never consult the feed predicate.
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

// A checked-out guest is still refused while the PMS is down; the mirror rule relaxes only the feed requirement.
func TestIntegration_Phase3Auth_CheckedOutStayIsRefusedWhileOffline(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	takeInterfaceOffline(t, f, "DIAL_FAILED")
	if _, err := controlledExec(context.Background(), f.pool, "stay",
		// effective_checkout_at is required by stays_checkedout_needs_boundary. posting_allowed=false in the same
		// statement, as internal/checkout does: posting_only_in_house is checked before the AFTER trigger recomputes it.
		`UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=now() WHERE id=$1`,
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

// A wrong value fails identically offline and online, or the portal becomes an occupancy oracle.
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
		return
	}
	recMissing, _ := post(t, f.p3.resolveHandler,
		f.resolveBody("999", "Okonkwo", "", "00000046-0000-4000-8000-000000000000"))
	if recBad.Body.String() != recMissing.Body.String() || recBad.Code != recMissing.Code {
		t.Fatalf("offline failures are distinguishable: wrong-name %d %q vs unknown-room %d %q",
			recBad.Code, recBad.Body.String(), recMissing.Code, recMissing.Body.String())
	}
}

// A never-synchronised interface refuses everyone while disconnected; there is no local guest list.
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

// An unresolved continuity gap refuses everyone, offline included (missed events may include a checkout).
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

// Multi-PMS namespacing and ambiguity are unchanged by the outage; searching wider offline could hand one
// hotel's guest another hotel's Stay.
func TestIntegration_Phase3Auth_NamespacingAndAmbiguitySurviveTheOutage(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	takeInterfaceOffline(t, f, "DIAL_FAILED")

	// Reuses the fixture's room-412 "Shared" ambiguity so the outage is the only variable.
	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Shared", "", "00000049-0000-4000-8000-000000000000"))
	if res.Outcome == outcomeVerified {
		t.Fatal("an ambiguous match verified during an outage. Ambiguity must fail closed whether or not the " +
			"PMS is reachable, or the answer depends on which row was found first")
	}
}
