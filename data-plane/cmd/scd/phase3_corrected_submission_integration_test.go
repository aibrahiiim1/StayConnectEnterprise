//go:build integration

package main

// A corrected submission on the same portal page. The portal mints a request id per deliberate submission
// (browser half: hotel-admin/e2e/portal-request-id.spec.ts) and the resolver replays only a SUCCESS, so a
// recorded refusal never freezes a later submission. Bodies match what portald sends in room_any mode.

import (
	"context"
	"fmt"
	"testing"
)

// verifyBody is a room_any submission: one typed value compared against surname, first name and reservation.
func (f *authFixture) verifyBody(room, verification, reqID string) map[string]any {
	return map[string]any{
		"room": room, "verification": verification, "request_id": reqID,
		"device": map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	}
}

// reqID returns a distinct uuid per submission, derived from the fixture's tenant to avoid colliding rows.
func (f *authFixture) reqID(t *testing.T, n int) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("mint request id %d: %v", n, err)
	}
	return id
}

// (1) Wrong value, then the correct surname, on the same page.
func TestIntegration_Phase3Auth_ACorrectedSurnameIsEvaluatedOnTheSamePage(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	_, wrong := post(t, f.p3.resolveHandler, f.verifyBody("412", "Nottheguest", f.reqID(t, 1)))
	if wrong.Outcome != outcomeNotVerified {
		t.Fatalf("a wrong verification value verified: %+v", wrong)
	}

	_, corrected := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 2)))
	if corrected.Outcome != outcomeVerified || corrected.AuthContextID == "" {
		t.Fatalf("the corrected surname was refused: %+v. Before this correction the page carried one request "+
			"id, the recorded refusal was replayed, and the corrected value was never compared at all", corrected)
	}
	if len(corrected.Offers) == 0 {
		t.Fatal("a verified correction produced no package to choose, so the guest cannot be granted access")
	}

	grant := postGrant(t, f, corrected.AuthContextID, corrected.Offers[0].PackageRevisionID)
	if grant.Outcome != outcomeVerified || grant.SessionID == "" {
		t.Fatalf("the corrected submission verified but could not be granted access: %+v", grant)
	}
}

// (2) The same walk ending on a reservation number.
func TestIntegration_Phase3Auth_ACorrectedReservationNumberIsEvaluatedOnTheSamePage(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	_, wrong := post(t, f.p3.resolveHandler, f.verifyBody("412", "RES-0000", f.reqID(t, 1)))
	if wrong.Outcome != outcomeNotVerified {
		t.Fatalf("a wrong reservation number verified: %+v", wrong)
	}

	_, corrected := post(t, f.p3.resolveHandler, f.verifyBody("412", "RES-4001", f.reqID(t, 2)))
	if corrected.Outcome != outcomeVerified || corrected.AuthContextID == "" {
		t.Fatalf("the corrected reservation number was refused: %+v", corrected)
	}
	grant := postGrant(t, f, corrected.AuthContextID, corrected.Offers[0].PackageRevisionID)
	if grant.Outcome != outcomeVerified || grant.SessionID == "" {
		t.Fatalf("the corrected reservation number verified but could not be granted access: %+v", grant)
	}
}

// (3) Repeated wrong submissions stay refused and are each genuinely evaluated (one resolution per id).
func TestIntegration_Phase3Auth_RepeatedWrongSubmissionsAreEachEvaluated(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	ids := make([]string, 3)
	for i := range ids {
		ids[i] = f.reqID(t, i)
		_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", fmt.Sprintf("Wrong%d", i), ids[i]))
		if res.Outcome != outcomeNotVerified {
			t.Fatalf("wrong submission %d verified: %+v", i, res)
		}
		var code string
		var stay *string
		if err := f.pool.QueryRow(ctx, `SELECT outcome_code, resolved_stay_id::text FROM iam_v2.auth_resolutions
			 WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`,
			f.tenant, f.site, ids[i]).Scan(&code, &stay); err != nil {
			t.Fatalf("wrong submission %d recorded no resolution of its own, so it was never evaluated: %v", i, err)
		}
		if code != "NO_MATCH" || stay != nil {
			t.Fatalf("wrong submission %d recorded %s/%v, want a determinate NO_MATCH naming no stay", i, code, stay)
		}
	}

	_, corrected := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 9)))
	if corrected.Outcome != outcomeVerified {
		t.Fatalf("three refusals left the page unable to verify a correct value: %+v", corrected)
	}
}

// (4) Repeating a successful request cannot double-grant; a Stay holds exactly one live Entitlement:
//   - the same context from the same device returns the same session;
//   - the same request id replays the resolution;
//   - a new request id verifies again but is refused a second grant.
func TestIntegration_Phase3Auth_RepeatingASuccessCannotDoubleGrant(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	first := f.reqID(t, 1)
	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", first))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: the stay did not verify: %+v", res)
	}
	granted := postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)
	if granted.Outcome != outcomeVerified || granted.SessionID == "" {
		t.Fatalf("setup: the grant produced no access: %+v", granted)
	}

	again := postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)
	if again.Outcome != outcomeVerified || again.SessionID != granted.SessionID {
		t.Fatalf("a retry from the same device against its own consumed context returned %+v, want the session "+
			"the first grant created (%s)", again, granted.SessionID)
	}

	_, replayed := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", first))
	if replayed.Outcome != outcomeVerified {
		t.Fatalf("replaying a successful request id stopped verifying: %+v", replayed)
	}
	if n := countRows(t, f, `SELECT count(*) FROM iam_v2.auth_resolutions
		 WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`, f.tenant, f.site, first); n != 1 {
		t.Fatalf("a replayed success recorded %d resolutions, want 1", n)
	}

	_, fresh := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 2)))
	if fresh.Outcome != outcomeVerified {
		t.Fatalf("a fresh submission by an already-connected guest failed to verify: %+v", fresh)
	}
	second := postGrant(t, f, fresh.AuthContextID, fresh.Offers[0].PackageRevisionID)
	if second.Outcome == outcomeVerified && second.EntitlementID != granted.EntitlementID {
		t.Fatalf("a second grant produced a NEW entitlement %s alongside %s", second.EntitlementID, granted.EntitlementID)
	}

	// Read from the database, not a response: one entitlement, one session.
	if n := countRows(t, f, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); n != 1 {
		t.Fatalf("entitlements for the stay = %d, want exactly 1", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM iam_v2.sessions s
		 JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		 WHERE e.stay_id=$1 AND s.ended IS NULL`, f.stay); n != 1 {
		t.Fatalf("live sessions for the stay = %d, want exactly 1", n)
	}
	_ = ctx
}

// (5) Correcting a mistyped value works while the PMS transport is down, for a Stay the mirror holds.
func TestIntegration_Phase3Auth_ACorrectionIsEvaluatedWhileOffline(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	takeInterfaceOffline(t, f, "DIAL_FAILED")

	_, wrong := post(t, f.p3.resolveHandler, f.verifyBody("412", "Nottheguest", f.reqID(t, 1)))
	if wrong.Outcome != outcomeNotVerified {
		t.Fatalf("a wrong value verified during an outage: %+v", wrong)
	}
	_, corrected := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 2)))
	if corrected.Outcome != outcomeVerified || corrected.AuthContextID == "" {
		t.Fatalf("a correction was refused while the PMS was down, for a Stay the mirror holds: %+v", corrected)
	}
	grant := postGrant(t, f, corrected.AuthContextID, corrected.Offers[0].PackageRevisionID)
	if grant.Outcome != outcomeVerified || grant.SessionID == "" {
		t.Fatalf("the offline correction verified but could not be granted access: %+v", grant)
	}
}

// (6) A Stay the mirror does not hold is refused while offline, however often submitted: admitting it
// would rest on evidence nothing can vouch for.
func TestIntegration_Phase3Auth_AStayAbsentFromTheMirrorStaysRefusedOffline(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	takeInterfaceOffline(t, f, "DIAL_FAILED")

	for i := 0; i < 3; i++ {
		_, res := post(t, f.p3.resolveHandler, f.verifyBody("905", "Okonkwo", f.reqID(t, i)))
		if res.Outcome != outcomeNotVerified {
			t.Fatalf("a room absent from the mirror verified on attempt %d: %+v", i, res)
		}
	}
	if n := countRows(t, f, `SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id=$1 AND site_id=$2`,
		f.tenant, f.site); n != 0 {
		t.Fatalf("a stay absent from the mirror produced %d entitlements", n)
	}
}

// (7) Nothing outside this guest's tenant, site or mapped interface can be matched. Each decoy is asserted
// at the resolution (a determinate NO_MATCH naming no Stay), since a later refusal would look identical to
// the guest while the row had in fact been reachable.
func TestIntegration_Phase3Auth_ConfinedToTheTenantSiteAndMappedInterface(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	// (a) another tenant with its own site, interface and network
	other := newAuthFixture(t)
	if _, err := controlledExec(ctx, other.pool, "stay",
		`UPDATE iam_v2.stays SET normalized_room_number='777' WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		other.tenant, other.site, other.stay); err != nil {
		t.Fatalf("seed the other tenant's decoy room: %v", err)
	}

	// (b) another site of this tenant, (c) another active interface not mapped to this network; both hold room
	// 777 with the same surname, so scope is the only difference.
	if _, err := controlledExec(ctx, f.pool, "stay", `WITH
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, $1, g::text, 's' FROM gen_random_uuid() g RETURNING id,tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	                                  external_stay_identity,normalized_room_number,status,lifecycle_version,
	                                  last_applied_event_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'RES-7771','STAY-7771','777','IN_HOUSE',1,0
	           FROM pi RETURNING id,tenant_id,site_id,pms_interface_id)
	INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
	SELECT st.tenant_id, st.site_id, st.pms_interface_id, st.id,'OKONKWO',true FROM st`, f.tenant); err != nil {
		t.Fatalf("seed the other-site decoy: %v", err)
	}
	if _, err := controlledExec(ctx, f.pool, "stay", `WITH
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	                                  external_stay_identity,normalized_room_number,status,lifecycle_version,
	                                  last_applied_event_version)
	         SELECT gen_random_uuid(),$1,$2,pi.id,'RES-7772','STAY-7772','777','IN_HOUSE',1,0 FROM pi RETURNING id,pms_interface_id)
	INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
	SELECT $1,$2,st.pms_interface_id,st.id,'OKONKWO',true FROM st`, f.tenant, f.site); err != nil {
		t.Fatalf("seed the unmapped-interface decoy: %v", err)
	}

	for i, value := range []string{"Okonkwo", "RES-7771", "RES-7772"} {
		id := f.reqID(t, i)
		_, res := post(t, f.p3.resolveHandler, f.verifyBody("777", value, id))
		if res.Outcome != outcomeNotVerified {
			t.Fatalf("room 777 / %q verified for a guest whose network maps to neither the other tenant, the "+
				"other site, nor the unmapped interface: %+v", value, res)
		}
		var code string
		var stay *string
		if err := f.pool.QueryRow(ctx, `SELECT outcome_code, resolved_stay_id::text FROM iam_v2.auth_resolutions
			 WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`,
			f.tenant, f.site, id).Scan(&code, &stay); err != nil {
			t.Fatalf("%q recorded no resolution: %v", value, err)
		}
		if code != "NO_MATCH" || stay != nil {
			t.Fatalf("%q resolved to %s/%v — an out-of-scope Stay was reachable by the probe and refused only "+
				"later, which is a confinement failure however it ends", value, code, stay)
		}
	}

	if n := countRows(t, f, `SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id IN ($1,$2)`,
		f.tenant, other.tenant); n != 0 {
		t.Fatalf("out-of-scope submissions produced %d entitlements", n)
	}
}

func countRows(t *testing.T, f *authFixture, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}
