//go:build integration

package main

// A CORRECTED SUBMISSION ON THE SAME PORTAL PAGE, through the handlers scd actually serves.
//
// THE DEFECT THESE EXIST FOR. The portal minted one resolution request id per attempt "key", and the key was
// built from last_name / first_name / reservation_number. room_any -- the combined mode, and the one a real
// property runs so the guest is never asked which KIND of identifier they are holding -- puts the typed value
// in `verification`, which the key never read. Every submission from one page therefore carried the SAME id.
// The server did exactly what an idempotency key says to do and replayed the resolution recorded under it, so
// a guest who mistyped a surname and corrected it was answered by their own typo until they reloaded the page.
// The uniform failure sentence -- correctly uniform, and a security property -- made that indistinguishable
// from a name that was genuinely wrong, from either end.
//
// The correction has two halves and both are asserted here. The portal now mints an id per DELIBERATE
// submission (the browser half is pinned in hotel-admin/e2e/portal-request-id.spec.ts, because the id is
// generated in the guest's browser and nowhere else). And the resolver now replays only a SUCCESS: a recorded
// refusal is re-evaluated rather than returned, so no stored non-success can freeze a later submission.
//
// THE BODIES HERE ARE THE ONES PORTALD SENDS, in the combined mode the defect lived in: `room`, one
// `verification` value, a `request_id`, and a `device` the appliance derived.

import (
	"context"
	"fmt"
	"testing"
)

// verifyBody is a room_any submission: ONE typed value, and the server compares it against surname, first
// name and reservation number together. The portal deliberately does not inspect its shape.
func (f *authFixture) verifyBody(room, verification, reqID string) map[string]any {
	return map[string]any{
		"room": room, "verification": verification, "request_id": reqID,
		"device": map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	}
}

// reqID returns a distinct canonical uuid for each deliberate submission a test makes -- which is what the
// corrected portal sends. Derived from the fixture's own tenant so a database that has run this suite before
// carries no colliding row.
func (f *authFixture) reqID(t *testing.T, n int) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("mint request id %d: %v", n, err)
	}
	return id
}

// (1) WRONG VALUE, THEN THE CORRECT SURNAME, ON THE SAME PAGE. This is the Product Owner's acceptance walk.
func TestIntegration_Phase3Auth_ACorrectedSurnameIsEvaluatedOnTheSamePage(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	_, wrong := post(t, f.p3.resolveHandler, f.verifyBody("412", "Nottheguest", f.reqID(t, 1)))
	if wrong.Outcome != outcomeNotVerified {
		t.Fatalf("a wrong verification value verified: %+v", wrong)
	}

	// No reload, no new page: the next deliberate submission, with its own request id, exactly as the
	// corrected portal sends it.
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

// (2) THE SAME WALK ENDING ON A RESERVATION NUMBER. Combined mode compares the one typed value against the
// surname and the reservation identifier together, so both corrections have to work and for the same reason.
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

// (3) REPEATED WRONG SUBMISSIONS STAY REFUSED -- AND ARE GENUINELY EVALUATED.
//
// "Still refused" is the easy half and would also be true of the defect: a frozen replay refuses too. The half
// that matters is that each attempt was actually decided, which is what makes the correction that follows it
// possible. Each submission records its own resolution against its own id, so the count IS the evidence.
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

	// ...and the page is not poisoned by any of them.
	_, corrected := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 9)))
	if corrected.Outcome != outcomeVerified {
		t.Fatalf("three refusals left the page unable to verify a correct value: %+v", corrected)
	}
}

// (4) REPEATING AN ALREADY SUCCESSFUL REQUEST CANNOT DOUBLE-GRANT.
//
// Minting an id per submission means a determined guest CAN reach a second Auth Context, so the guard against
// two grants must be the server's and not the page's. It is: a Stay holds exactly one live Entitlement.
//
// Three repeats are walked, because they fail differently and all three must hold:
//   - the same context again from the same device returns the SAME session (a lost reply, recovered);
//   - the same request id again replays the resolution rather than re-proving identity;
//   - a NEW request id -- what the corrected portal sends on the next tap -- verifies again and is then
//     refused a second grant, leaving the guest's existing access untouched.
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

	// (a) the same context, same device: the session that consumption already produced.
	again := postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)
	if again.Outcome != outcomeVerified || again.SessionID != granted.SessionID {
		t.Fatalf("a retry from the same device against its own consumed context returned %+v, want the session "+
			"the first grant created (%s)", again, granted.SessionID)
	}

	// (b) the same request id: the stored SUCCESS is replayed, so identity is not re-proved.
	_, replayed := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", first))
	if replayed.Outcome != outcomeVerified {
		t.Fatalf("replaying a successful request id stopped verifying: %+v", replayed)
	}
	if n := countRows(t, f, `SELECT count(*) FROM iam_v2.auth_resolutions
		 WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`, f.tenant, f.site, first); n != 1 {
		t.Fatalf("a replayed success recorded %d resolutions, want 1", n)
	}

	// (c) a NEW id -- the next deliberate tap. It verifies (the guest really is that guest) and is then
	// refused a second grant.
	_, fresh := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 2)))
	if fresh.Outcome != outcomeVerified {
		t.Fatalf("a fresh submission by an already-connected guest failed to verify: %+v", fresh)
	}
	second := postGrant(t, f, fresh.AuthContextID, fresh.Offers[0].PackageRevisionID)
	if second.Outcome == outcomeVerified && second.EntitlementID != granted.EntitlementID {
		t.Fatalf("a second grant produced a NEW entitlement %s alongside %s", second.EntitlementID, granted.EntitlementID)
	}

	// THE INVARIANT, read from the database rather than from any response: one entitlement, one session.
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

// (5) CORRECTING A MISTYPED VALUE WORKS WHILE THE PMS TRANSPORT IS DOWN, and only for a Stay the mirror
// already holds. The correction is in the resolver and the portal, neither of which is supposed to care
// whether a socket is open -- and an outage is exactly when a guest is most likely to be retyping.
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

// (6) A STAY THE MIRROR DOES NOT HOLD IS STILL REFUSED WHILE OFFLINE, however many times it is submitted.
//
// This is the limit of what the correction can do, stated as a test so it is never mistaken for a defect in
// it: room sign-in answers from locally mirrored Stay data, and a room created or changed in the PMS after
// the last sync is not in that data. Retrying a correct value that the mirror has never seen is refused, and
// correctly so -- the alternative is admitting someone on evidence nothing can vouch for.
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

// (7) NOTHING OUTSIDE THIS GUEST'S TENANT, SITE OR MAPPED INTERFACE CAN BE MATCHED.
//
// Minting a fresh id per submission means an attacker can now submit as often as they like without the page
// freezing on its own first refusal -- which is the correct behaviour and also the reason to restate the
// confinement explicitly. Room numbers repeat constantly across properties; the only thing that stops room
// 777 in another tenant, another site, or an interface this network is not mapped to from answering for this
// guest is scope.
//
// Each decoy is asserted at the RESOLUTION, not just at the response: a determinate NO_MATCH naming no Stay
// means the probe never saw it. A refusal that arrived later -- at eligibility, at the offer engine -- would
// look identical to the guest and would mean the row had in fact been reachable.
func TestIntegration_Phase3Auth_ConfinedToTheTenantSiteAndMappedInterface(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	// (a) ANOTHER TENANT, complete with its own site, interface and network.
	other := newAuthFixture(t)
	if _, err := other.pool.Exec(ctx,
		`UPDATE iam_v2.stays SET normalized_room_number='777' WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		other.tenant, other.site, other.stay); err != nil {
		t.Fatalf("seed the other tenant's decoy room: %v", err)
	}

	// (b) ANOTHER SITE under THIS tenant, and (c) another ACTIVE interface under this tenant AND site that is
	// simply not mapped to this guest's network. Both hold room 777 with the same surname as the real stay,
	// so the only difference between them and a match is scope.
	if _, err := f.pool.Exec(ctx, `WITH
	  si AS (INSERT INTO public.sites(id,tenant_id) VALUES (gen_random_uuid(),$1) RETURNING id,tenant_id),
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
	if _, err := f.pool.Exec(ctx, `WITH
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

	// Nothing was granted anywhere, including in the other tenant.
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
