//go:build integration && prodprivilege

// Build tag prodprivilege: needs the real Gate-P service roles and grants, which the ordinary integration
// database lacks (it asserts svc_scd holds none). scripts/prod-privilege-integration.sh builds a
// production-like database and runs only this tag.

package main

// The grant must work as the role that runs it. PostgreSQL requires SELECT and UPDATE for a row lock but
// svc_scd holds SELECT only, so these run the grant's statements AS svc_scd and assert that an error that
// is not "no such row" never becomes an auth answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// requireServiceRole fails rather than skips: a skipped privilege assertion is not an assertion.
func requireServiceRole(t *testing.T, f *authFixture, role string) {
	t.Helper()
	var exists bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
		t.Fatalf("check role %s: %v", role, err)
	}
	if !exists {
		t.Fatalf("%s is not provisioned in this database, so this test would prove nothing. The integration "+
			"harness applies deploy/gatep/gatep-roles.sql for precisely this reason", role)
	}
}

// End to end under the real role: verify, offered package, grant.
func TestIntegration_Phase3Grant_SucceedsAsServiceRole(t *testing.T) {
	f := newProdAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	requireServiceRole(t, f, "svc_scd")
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000061-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || len(res.Offers) != 1 {
		t.Fatalf("setup: expected one offer on a clean verify, got %+v", res)
	}
	offered := res.Offers[0].PackageRevisionID

	// The offer lock as the handler runs it, but AS svc_scd (an inline FOR UPDATE is permission denied).
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE svc_scd`); err != nil {
		t.Fatalf("assume svc_scd: %v", err)
	}
	var tier *int
	var evidence int64
	if err := tx.QueryRow(ctx,
		`SELECT matched_tier_order, evidence_version
		   FROM iam_v2.lock_auth_context_offer($1,$2,$3::uuid,$4::uuid)`,
		f.tenant, f.site, res.AuthContextID, offered).Scan(&tier, &evidence); err != nil {
		t.Fatalf("svc_scd could not lock the offer it was just given: %v. This is the exact failure that "+
			"refused the first real guest Room Login, reported to the operator as "+
			"package_not_offered_to_this_context", err)
	}
}

// svc_scd must be able to lock an offer but not rewrite one: UPDATE would let it edit the fields (tier,
// evidence version, expiry) the grant validates against.
func TestIntegration_Phase3Grant_ServiceRoleCannotRewriteOffers(t *testing.T) {
	f := newProdAuthFixture(t)
	requireServiceRole(t, f, "svc_scd")
	ctx := context.Background()

	var canExec, canUpdate, canSelect bool
	if err := f.pool.QueryRow(ctx, `
		SELECT has_function_privilege('svc_scd',
		         'iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid)', 'EXECUTE'),
		       has_table_privilege('svc_scd', 'iam_v2.auth_context_offers', 'UPDATE'),
		       has_table_privilege('svc_scd', 'iam_v2.auth_context_offers', 'SELECT')`).
		Scan(&canExec, &canUpdate, &canSelect); err != nil {
		t.Fatal(err)
	}
	if !canExec {
		t.Fatal("svc_scd cannot execute the offer-lock helper, so the grant cannot take its row lock")
	}
	if !canSelect {
		t.Fatal("svc_scd cannot read auth_context_offers")
	}
	if canUpdate {
		t.Fatal("svc_scd holds UPDATE on auth_context_offers. That is the broad grant this design exists to " +
			"avoid: it would let the grant rewrite the matched tier, the evidence version and the expiry — " +
			"the very values it then validates against")
	}
}

// A missing offer is an auth answer; a broken database is not. Only NO ROW may become the
// package_not_offered_to_this_context reason.
func TestIntegration_Phase3Grant_AbsentOfferReturnsNoRowNotAnError(t *testing.T) {
	f := newProdAuthFixture(t)
	requireServiceRole(t, f, "svc_scd")
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000062-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}

	// the priced package the offer engine excludes
	var tier *int
	var evidence int64
	err := f.pool.QueryRow(ctx,
		`SELECT matched_tier_order, evidence_version
		   FROM iam_v2.lock_auth_context_offer($1,$2,$3::uuid,$4::uuid)`,
		f.tenant, f.site, res.AuthContextID, f.priced).Scan(&tier, &evidence)
	if err == nil {
		t.Fatal("a package that was never offered was lockable, so the offer check authorises nothing")
	}
	if !strings.Contains(err.Error(), "no rows") {
		t.Fatalf("an absent offer produced %v, not a no-row result. Only no-row may become "+
			"package_not_offered_to_this_context; anything else is an internal failure and must be logged "+
			"and surfaced as one", err)
	}
}

// An expired offer is also no-row (expiry is tested inside the locked read), an authorisation decision not a fault.
func TestIntegration_Phase3Grant_ExpiredOfferIsNoRow(t *testing.T) {
	f := newProdAuthFixture(t)
	requireServiceRole(t, f, "svc_scd")
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000063-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || len(res.Offers) != 1 {
		t.Fatalf("setup: %+v", res)
	}

	// auth_context_offers is guarded (only iam_v2.record_auth_context_offer may write it), so the offer is
	// recorded with a short life and left to expire. aco_expiry_after_offer needs expires_at > offered_at,
	// so expiry is computed in SQL against the same clock.
	if _, err := f.pool.Exec(ctx,
		`SELECT iam_v2.record_auth_context_offer($1,$2,$3::uuid,$4::uuid,1,1,now() + interval '1200 milliseconds')`,
		f.tenant, f.site, res.AuthContextID, f.priced); err != nil {
		t.Fatalf("record a short-lived offer: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	var tier *int
	var evidence int64
	err := f.pool.QueryRow(ctx,
		`SELECT matched_tier_order, evidence_version
		   FROM iam_v2.lock_auth_context_offer($1,$2,$3::uuid,$4::uuid)`,
		f.tenant, f.site, res.AuthContextID, f.priced).Scan(&tier, &evidence)
	if err == nil || !strings.Contains(err.Error(), "no rows") {
		t.Fatalf("an expired offer produced %v, want a no-row result", err)
	}
}

// The whole room login with scd's pool running as svc_scd; a missing privilege fails here and is fixed in deploy/gatep.
func TestIntegration_Phase3Grant_FullChainAsServiceRole(t *testing.T) {
	f := newProdAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	requireServiceRole(t, f, "svc_scd")
	p3 := f.serviceRolePhase3(t)

	_, res := post(t, p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000064-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || len(res.Offers) != 1 {
		t.Fatalf("svc_scd could not verify the stay and offer the included package: %+v", res)
	}
	if res.Offers[0].PackageRevisionID != f.pkgRev {
		t.Fatalf("offers = %+v, want exactly the included package", res.Offers)
	}

	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.pkgRev,
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var granted phase3GrantResp
	if err := json.Unmarshal(rec.Body.Bytes(), &granted); err != nil {
		t.Fatalf("undecodable grant response %q: %v", rec.Body.String(), err)
	}
	if granted.Outcome != outcomeVerified || granted.SessionID == "" || granted.EntitlementID == "" {
		t.Fatalf("the grant produced no access as svc_scd: %s. This is the shape of the live failure — a "+
			"clean verify followed by a refusal at the grant", rec.Body.String())
	}

	// The census runs as the owner to read what really landed.
	c := f.census(t, res.AuthContextID)
	if c.purchases != 1 || c.entitlements != 1 || c.sessions != 1 {
		t.Fatalf("purchases=%d entitlements=%d sessions=%d, want exactly one of each", c.purchases,
			c.entitlements, c.sessions)
	}
	if !c.contextConsumed {
		t.Fatal("a successful grant left the Auth Context unconsumed, so the proof was never spent")
	}
}

// Room charge is decided under the real role: svc_scd may not read pms_interface_runtime, so a permission
// error must not become "not applicable". Asserted three ways (the full statement short-circuits on an
// un-onboarded fixture):
//   - svc_scd can ask the narrow freshness reader (migration 0101);
//   - svc_scd cannot call the invoker function directly;
//   - the applicability statement asks only the reader.
func TestIntegration_Phase3Grant_RoomChargeApplicabilityRunsAsServiceRole(t *testing.T) {
	f := newProdAuthFixture(t)
	requireServiceRole(t, f, "svc_scd")
	p3 := f.serviceRolePhase3(t)
	ctx := context.Background()
	var fresh bool
	if err := p3.srv.db.QueryRow(ctx, `SELECT iam_v2.p4_room_charge_interface_fresh($1,$2,$3)`,
		f.tenant, f.site, f.iface).Scan(&fresh); err != nil {
		t.Fatalf("svc_scd cannot ask whether the interface is fresh for room charge: %v", err)
	}
	var block string
	err := p3.srv.db.QueryRow(ctx, `SELECT iam_v2.p4_interface_freshness_block($1,$2,$3,$4,now())`,
		f.tenant, f.site, f.iface, f.revision).Scan(&block)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("svc_scd read the feed health directly (err=%v); the Gate-P rule is that it may not", err)
	}
	if strings.Contains(roomChargeApplicableSQL, "p4_interface_freshness_block") ||
		strings.Contains(roomChargeApplicableSQL, "pms_interface_runtime") ||
		!strings.Contains(roomChargeApplicableSQL, "p4_room_charge_interface_fresh") {
		t.Fatal("the room-charge applicability statement reads feed health svc_scd may not read")
	}
	var ok bool
	if err := p3.srv.db.QueryRow(ctx, roomChargeApplicableSQL,
		f.tenant, f.site, f.iface, f.stay, "USD", 2, f.priced).Scan(&ok); err != nil {
		t.Fatalf("the room-charge applicability statement fails as svc_scd: %v", err)
	}
}

// A room sign-in records where the device was seen, as svc_scd; activation admits a device only on a network
// it has appeared on, and a paid charge activates later.
func TestIntegration_Phase3Grant_RoomSignInRecordsTheDeviceAppearance(t *testing.T) {
	f := newProdAuthFixture(t)
	requireServiceRole(t, f, "svc_scd")
	p3 := f.serviceRolePhase3(t)
	_, res := post(t, p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000065-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("resolve: %+v", res)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.device_network_appearances a
		JOIN iam_v2.devices d ON d.id = a.device_id
		WHERE d.mac = $1::macaddr AND a.guest_network_id = $2::uuid`, f.net.mac, f.network).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the signed-in device has %d appearance(s) on its guest network, want 1", n)
	}
}
