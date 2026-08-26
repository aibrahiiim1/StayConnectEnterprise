//go:build integration && prodprivilege

// BUILD TAG: prodprivilege. These tests need the REAL Gate-P service roles with their REAL grants, which the
// ordinary integration database deliberately does not have — it asserts the DARK posture, in which svc_scd
// holds no runtime grants at all. Granting them there would break that assertion, and skipping these when the
// role is absent is what let two privilege defects reach production green. So they get their own harness:
// scripts/prod-privilege-integration.sh builds a PRODUCTION-LIKE database from the Gate-P files and runs only
// this tag against it. Neither posture has to pretend to be the other.

package main

// THE GRANT MUST WORK AS THE ROLE THAT ACTUALLY RUNS IT.
//
// The first real guest Room Login on the PRE-LIVE appliance verified correctly, minted an Auth Context and
// recorded exactly one offer — and then refused at the grant. The offer row satisfied every condition the
// grant checks. What failed was the row LOCK:
//
//	as svc_scd, SELECT ... FOR UPDATE  ->  ERROR: permission denied for table auth_context_offers
//	as svc_scd, the same SELECT        ->  returns the row
//
// PostgreSQL requires SELECT *and* UPDATE for a row lock, and svc_scd holds SELECT only. Two things let that
// reach a live guest. The suites here run as a superuser, so no privilege requirement is ever exercised; and
// the handler turned every error into "package_not_offered_to_this_context", so a permission failure was
// reported as an authorisation decision.
//
// These tests close both. They run the grant's own statements AS svc_scd, and they assert that an error which
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

// requireServiceRole fails rather than skips. The least-privilege tests used to skip when svc_scd was absent,
// which is exactly why two privilege defects reached production with CI green — a skipped assertion is not an
// assertion. The harness provisions the Gate-P roles; if that stopped working, this must say so loudly.
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

// THE REGRESSION, end to end and under the real role: verify -> offered package -> grant.
//
// The whole point is the last step. A guest who verifies and is offered a package must be able to redeem it
// while scd is running as svc_scd, which is what production does.
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

	// The grant's offer lock, run exactly as the handler runs it, but AS svc_scd. Before the fix this was an
	// inline SELECT ... FOR UPDATE and raised "permission denied for table auth_context_offers".
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

// THE PRIVILEGE MODEL, asserted rather than assumed.
//
// svc_scd must be able to LOCK an offer and must NOT be able to rewrite one. Granting UPDATE on the table
// would give it both, and the fields it could then rewrite — matched tier, evidence version, expiry — are the
// fields the grant validates against, so the role being checked would gain the ability to edit the check.
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

// A MISSING OFFER IS AN AUTH ANSWER; A BROKEN DATABASE IS NOT.
//
// The handler collapsed every error into package_not_offered_to_this_context, so a permission failure was
// reported to the operator as an authorisation decision. The helper returns NO ROW for a genuinely absent
// offer, which is the only case that may become that reason code.
func TestIntegration_Phase3Grant_AbsentOfferReturnsNoRowNotAnError(t *testing.T) {
	f := newProdAuthFixture(t)
	requireServiceRole(t, f, "svc_scd")
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000062-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}

	// A package that was never offered to this context: the priced one the offer engine excludes.
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

// AN EXPIRED OFFER IS ALSO NO-ROW, not an error — the expiry test lives inside the locked read, so an offer
// that lapses between resolve and grant is refused as an authorisation decision rather than a fault.
func TestIntegration_Phase3Grant_ExpiredOfferIsNoRow(t *testing.T) {
	f := newProdAuthFixture(t)
	requireServiceRole(t, f, "svc_scd")
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", "00000063-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || len(res.Offers) != 1 {
		t.Fatalf("setup: %+v", res)
	}

	// THE OFFER IS MADE THROUGH THE APPROVED WRITER, then allowed to lapse on its own.
	//
	// An earlier version backdated the row with a direct UPDATE. The real schema refuses that — auth_context_offers
	// is guarded, and only iam_v2.record_auth_context_offer may write it — and disabling the guard would have made
	// this database less like production, which is the one thing this harness must not do. So the offer is
	// recorded exactly as the offer engine records it, with a short life, and the test waits for it to expire.
	// aco_expiry_after_offer requires expires_at > offered_at, so the expiry is computed in SQL against the same
	// clock the insert stamps offered_at from.
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

// THE WHOLE ROOM LOGIN, AS THE ROLE THAT ACTUALLY SERVES IT.
//
// Everything above tests the offer lock in isolation. This runs the real chain a guest walks — verify, the
// offer, the grant, and the Purchase, Entitlement and Session the grant creates — with scd's own pool running
// as svc_scd, which is what the appliance does. Nothing here is granted for the test: if a statement in that
// chain needs a privilege svc_scd does not hold, this fails, and the fix belongs in deploy/gatep.
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

	// The census runs as the owner, so it reads what really landed rather than what svc_scd can see.
	c := f.census(t, res.AuthContextID)
	if c.purchases != 1 || c.entitlements != 1 || c.sessions != 1 {
		t.Fatalf("purchases=%d entitlements=%d sessions=%d, want exactly one of each", c.purchases,
			c.entitlements, c.sessions)
	}
	if !c.contextConsumed {
		t.Fatal("a successful grant left the Auth Context unconsumed, so the proof was never spent")
	}
}
