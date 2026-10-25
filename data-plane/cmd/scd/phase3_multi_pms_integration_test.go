//go:build integration

package main

// Room sign-in with an explicit routing policy: NONE, DEDICATED and SHARED_STRICT. A second, independent PMS Interface is added to the Phase-3 fixture; the
// fixture's own interface keeps room 412 (Okonkwo / Shared). Every shared case here is the STRICT contract (Phase 0, section 8): exactly one candidate returns exactly
// one VERIFIED stay and every other candidate returns a determinate NO_MATCH, with no fallback and no first responder.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type secondPMS struct {
	iface, stay string
}

// addPMS adds an independent interface with one in-house stay. healthy=false leaves its feed with a continuity gap, so it cannot answer; lifecycle is its state.
func (f *authFixture) addPMS(t *testing.T, room, last, reservation string, healthy bool, lifecycle string) secondPMS {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatal(err)
	}
	continuity := "CONTINUOUS"
	if !healthy {
		continuity = "GAP_DETECTED"
	}
	var out secondPMS
	var rev string
	if err := tx.QueryRow(ctx, `
	WITH pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	            VALUES (gen_random_uuid(), $1, $2, 'protel-fias', $6) RETURNING id),
	     pir AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	             SELECT gen_random_uuid(), $1, $2, pi.id, 1, 'UTC', '{}'::jsonb FROM pi RETURNING id),
	     st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,normalized_room_number,status,lifecycle_version,last_applied_event_version)
	            SELECT gen_random_uuid(), $1, $2, pi.id, $5, $5 || '-ID', $3, 'IN_HOUSE', 1, 0 FROM pi RETURNING id, pms_interface_id),
	     sg AS (INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
	            SELECT $1, $2, st.pms_interface_id, st.id, $4, true FROM st RETURNING id)
	SELECT (SELECT id FROM pi)::text, (SELECT id FROM st)::text, (SELECT id FROM pir)::text`,
		f.tenant, f.site, room, strings.ToUpper(last), reservation, lifecycle).Scan(&out.iface, &out.stay, &rev); err != nil {
		t.Fatalf("second interface: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_evidence_at = now() - interval '10 seconds', occupancy_ingested_at = now() - interval '9 seconds',
		occupancy_revision_id = $2, occupancy_normalization_version = 1, occupancy_clock_suspect = false, occupancy_evidence_version = 1,
		arrival = (now() - interval '3 days')::date, departure = (now() + interval '1 day')::date WHERE id = $1`, out.stay, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`, out.iface, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode, published_resync_generation, pinned_revision_id,
		 transport_status, sync_status, continuity_status, last_connected_at, last_heartbeat_at, last_complete_sync_at)
		VALUES ($1,$2,$3,1,'NONE',0,$4,'CONNECTED','IN_SYNC',$5,now(),now(),now())`, f.tenant, f.site, out.iface, rev, continuity); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return out
}

// route sets the network's policy and exactly the named interfaces, the way the admin API does: in one transaction.
func (f *authFixture) route(t *testing.T, mode string, ifaces ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, f.network); err != nil {
		t.Fatal(err)
	}
	for n, id := range ifaces {
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id,is_default) VALUES ($1,$2,$3,$4,$5)`,
			f.tenant, f.site, f.network, id, n == 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_policy(guest_network_id,tenant_id,site_id,mode) VALUES ($1,$2,$3,$4)
		ON CONFLICT (guest_network_id) DO UPDATE SET mode=EXCLUDED.mode`, f.network, f.tenant, f.site, mode); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("route %s %v: %v", mode, ifaces, err)
	}
}

func (f *authFixture) resolveAs(t *testing.T, room, last, res string) phase3Response {
	t.Helper()
	_, out := post(t, f.p3.resolveHandler, f.resolveBody(room, last, res, mustUUID(t, f.pool)))
	return out
}

func (f *authFixture) entitlementsFor(t *testing.T, stays ...string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id = ANY($1::uuid[])`, stays).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The same room number exists in two independent PMS; each guest resolves to their own PMS, and no PMS is chosen by the room number alone.
func TestIntegration_MultiPMS_DuplicateRoomNumbersResolveInTheirOwnInterface(t *testing.T) {
	f := newAuthFixture(t)
	b := f.addPMS(t, "412", "Delacroix", "RES-B1", true, "ACTIVE")
	f.route(t, policyShared, f.iface, b.iface)

	a := f.resolveAs(t, "412", "Okonkwo", "")
	if a.Outcome != outcomeVerified || a.AuthContextID == "" {
		t.Fatalf("a guest of PMS A in room 412 did not verify on a shared network: %+v", a)
	}
	var stay string
	if err := f.pool.QueryRow(context.Background(), `SELECT stay_id::text FROM iam_v2.auth_contexts WHERE id=$1`, a.AuthContextID).Scan(&stay); err != nil || stay != f.stay {
		t.Fatalf("PMS A's guest resolved to stay %q (err %v), want %s", stay, err, f.stay)
	}
	bb := f.resolveAs(t, "412", "Delacroix", "")
	if bb.Outcome != outcomeVerified {
		t.Fatalf("a guest of PMS B in the SAME room number did not verify: %+v", bb)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT stay_id::text, pms_interface_id::text FROM iam_v2.auth_contexts WHERE id=$1`, bb.AuthContextID).Scan(&stay, new(string)); err != nil || stay != b.stay {
		t.Fatalf("PMS B's guest resolved to stay %q (err %v), want %s", stay, err, b.stay)
	}
}

// Nobody who is in neither PMS is let in, and the refusal does not say which PMS was asked.
func TestIntegration_MultiPMS_AGuestInNeitherPMSIsRefusedUniformly(t *testing.T) {
	f := newAuthFixture(t)
	b := f.addPMS(t, "412", "Delacroix", "RES-B1", true, "ACTIVE")
	f.route(t, policyShared, f.iface, b.iface)

	one, two := f.resolveAs(t, "412", "Nobody", ""), f.resolveAs(t, "999", "Nobody", "")
	if one.Outcome != outcomeNotVerified || two.Outcome != outcomeNotVerified || one.AuthContextID != "" || two.AuthContextID != "" {
		t.Fatalf("%+v %+v", one, two)
	}
	if one.FailureClass != two.FailureClass {
		t.Fatalf("a room that exists in the PMS and one that does not are distinguishable: %q vs %q", one.FailureClass, two.FailureClass)
	}
}

// The same surname in the same room number in BOTH PMS is not resolved by picking one.
func TestIntegration_MultiPMS_AmbiguityAcrossInterfacesGrantsNothingAndPicksNoPMS(t *testing.T) {
	f := newAuthFixture(t)
	b := f.addPMS(t, "412", "Okonkwo", "RES-B1", true, "ACTIVE")
	f.route(t, policyShared, f.iface, b.iface)

	res := f.resolveAs(t, "412", "Okonkwo", "")
	if res.Outcome != outcomeNotVerified || res.AuthContextID != "" || len(res.Offers) != 0 {
		t.Fatalf("an ambiguous match across two PMS verified: %+v", res)
	}
	if n := f.entitlementsFor(t, f.stay, b.stay); n != 0 {
		t.Fatalf("%d entitlements were created for an ambiguous resolution", n)
	}
	// the reservation number is the discriminator the guest can supply: it resolves to exactly one PMS
	res = f.resolveAs(t, "412", "", "RES-B1")
	if res.Outcome != outcomeVerified {
		t.Fatalf("the reservation number did not resolve the ambiguity: %+v", res)
	}
}

// STRICT: one candidate that cannot answer makes "no PMS has this guest" unprovable, so a guest who A alone would verify is NOT admitted.
func TestIntegration_MultiPMS_AnUnavailableCandidateFailsTheWholeNetworkClosed(t *testing.T) {
	f := newAuthFixture(t)
	b := f.addPMS(t, "700", "Delacroix", "RES-B1", false, "ACTIVE")
	f.route(t, policyShared, f.iface, b.iface)

	res := f.resolveAs(t, "412", "Okonkwo", "")
	if res.Outcome != outcomeNotVerified || res.AuthContextID != "" {
		t.Fatalf("a guest was admitted although a selected PMS could not answer: %+v", res)
	}
	if n := f.entitlementsFor(t, f.stay); n != 0 {
		t.Fatalf("%d entitlements", n)
	}
	// Failure isolation: the same guest on a DEDICATED route to the healthy PMS is not affected by the other PMS being down.
	f.route(t, policyDedicated, f.iface)
	if res := f.resolveAs(t, "412", "Okonkwo", ""); res.Outcome != outcomeVerified {
		t.Fatalf("a dedicated network was affected by an interface it does not use: %+v", res)
	}
}

// A candidate that is part of the explicit set but out of service is not silently dropped from the vector.
func TestIntegration_MultiPMS_AnOutOfServiceCandidateIsNotSilentlyDropped(t *testing.T) {
	f := newAuthFixture(t)
	b := f.addPMS(t, "700", "Delacroix", "RES-B1", true, "AUTH_DISABLED")
	f.route(t, policyShared, f.iface, b.iface)

	res := f.resolveAs(t, "412", "Okonkwo", "")
	if res.Outcome != outcomeNotVerified || res.AuthContextID != "" {
		t.Fatalf("the network admitted a guest while one of its explicitly selected PMS was out of service: %+v", res)
	}
}

// DEDICATED asks exactly its one interface: a guest who exists only in another, healthy, active PMS is not found, so no "every active interface" fallback exists.
func TestIntegration_MultiPMS_DedicatedNeverConsultsAnotherActiveInterface(t *testing.T) {
	f := newAuthFixture(t)
	b := f.addPMS(t, "700", "Delacroix", "RES-B1", true, "ACTIVE")
	f.route(t, policyDedicated, f.iface)

	if res := f.resolveAs(t, "700", "Delacroix", ""); res.Outcome != outcomeNotVerified {
		t.Fatalf("a guest of an interface this network is not routed to verified: %+v", res)
	}
	if n := f.entitlementsFor(t, b.stay); n != 0 {
		t.Fatalf("%d entitlements for a stay outside the network's routing", n)
	}
	f.route(t, policyDedicated, b.iface)
	if res := f.resolveAs(t, "700", "Delacroix", ""); res.Outcome != outcomeVerified {
		t.Fatalf("re-routing to B did not let B's guest in: %+v", res)
	}
}

// NONE offers no room sign-in; the other methods are untouched.
func TestIntegration_MultiPMS_NoneOffersNoRoomSignIn(t *testing.T) {
	f := newAuthFixture(t)
	f.route(t, policyNone)
	res := f.resolveAs(t, "412", "Okonkwo", "")
	if res.Outcome != outcomeNotVerified || res.AuthContextID != "" {
		t.Fatalf("a PMS-free network admitted a room sign-in: %+v", res)
	}
	ov, err := f.srv.networkSigninFor(context.Background(), f.network)
	if err != nil || !ov.disabled[keyPMS] {
		t.Fatalf("the room method must not be offered on a PMS-free network: %+v err %v", ov, err)
	}
	if ov.disabled[keyVoucher] {
		t.Fatalf("NONE must not switch off any other method: %+v", ov)
	}
}

// A policy that disagrees with the interfaces actually mapped is refused rather than guessed.
func TestIntegration_MultiPMS_APolicyThatDisagreesWithItsMappingIsRefused(t *testing.T) {
	f := newAuthFixture(t)
	b := f.addPMS(t, "700", "Delacroix", "RES-B1", true, "ACTIVE")
	f.route(t, policyShared, f.iface, b.iface)
	// a mapping row removed behind the policy's back (the consistency check runs on writes of a policy or a mapping, not on a delete)
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1 AND pms_interface_id=$2`, f.network, b.iface); err != nil {
		t.Fatal(err)
	}
	if res := f.resolveAs(t, "412", "Okonkwo", ""); res.Outcome != outcomeNotVerified {
		t.Fatalf("a shared policy with one interface left admitted a guest: %+v", res)
	}
}

// More than the supported number of interfaces cannot be stored under a policy, and an unclassified multi-interface mapping is refused at runtime.
func TestIntegration_MultiPMS_TheCandidateCapIsEnforcedAtSaveAndAtRuntime(t *testing.T) {
	f := newAuthFixture(t)
	ids := []string{f.iface}
	for _, r := range []string{"701", "702", "703"} {
		ids = append(ids, f.addPMS(t, r, "Guest"+r, "RES-"+r, true, "ACTIVE").iface)
	}
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, f.network); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id) VALUES ($1,$2,$3,$4)`, f.tenant, f.site, f.network, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_policy(guest_network_id,tenant_id,site_id,mode) VALUES ($1,$2,$3,'SHARED_STRICT')`, f.network, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("four interfaces were stored under a shared policy; the cap is three")
	}
	// the earlier shape: four rows and no policy at all is refused when a guest tries, not guessed at
	if _, err := f.pool.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_policy WHERE guest_network_id=$1`, f.network); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, f.tenant, f.site, f.network, id); err != nil {
			t.Fatal(err)
		}
	}
	if res := f.resolveAs(t, "412", "Okonkwo", ""); res.Outcome != outcomeNotVerified {
		t.Fatalf("an unclassified four-interface mapping admitted a guest: %+v", res)
	}
}

var _ = http.StatusOK

// scd reads the policy and asks each candidate's feed through grants alone; a superuser connection hides a missing one, and a missing grant would stop every
// sign-in on the property ("network sign-in settings unreadable; refusing").
func TestIntegration_MultiPMS_TheSessionControllerRoleHoldsWhatTheRoutingPolicyNeeds(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	for table, priv := range map[string]string{
		"iam_v2.guest_network_pms_policy": "SELECT", "iam_v2.guest_network_pms_map": "SELECT", "iam_v2.pms_interfaces": "SELECT",
		"iam_v2.stays": "SELECT", "iam_v2.stay_guests": "SELECT", "iam_v2.network_signin_overrides": "SELECT",
	} {
		var ok bool
		if err := f.pool.QueryRow(ctx, `SELECT has_table_privilege('svc_scd', $1, $2)`, table, priv).Scan(&ok); err != nil || !ok {
			t.Errorf("svc_scd lacks %s on %s (err %v)", priv, table, err)
		}
	}
	var fnOK, writes bool
	if err := f.pool.QueryRow(ctx, `SELECT has_function_privilege('svc_scd','iam_v2.p3_interface_can_authorise(uuid,uuid,uuid)','EXECUTE')`).Scan(&fnOK); err != nil || !fnOK {
		t.Errorf("svc_scd cannot ask a candidate's feed (err %v)", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT has_table_privilege('svc_scd','iam_v2.guest_network_pms_policy','INSERT') OR has_table_privilege('svc_scd','iam_v2.guest_network_pms_policy','UPDATE') OR has_table_privilege('svc_scd','iam_v2.guest_network_pms_policy','DELETE')`).Scan(&writes); err != nil || writes {
		t.Errorf("the role being authorised can write the policy it is authorised by (err %v)", err)
	}
}
