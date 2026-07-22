//go:build integration

package main

// THE PHASE-3 GUEST PATH, end to end, through the handlers scd actually serves, against a real PostgreSQL 16.
//
// These are composition-root tests on purpose. Every individual piece — the resolver, the Auth Context, the
// grant — already has its own tests and they all pass; what those cannot show is whether the pieces are wired
// into a path that a guest can walk, and whether the wiring leaks anything. Two defects found earlier in this
// phase were exactly of that kind: correct operations called with the wrong ids.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

type authFixture struct {
	net                         fixtureNet
	pool                        *pgxpool.Pool
	srv                         *server
	p3                          *phase3Auth
	tenant, site, appliance     string
	iface, revision, network    string
	stay, otherStay             string
	pkgRev, gracePkgRev, priced string
}

// Each fixture gets its OWN subnet and MAC. Sharing one subnet across fixtures is not merely untidy: the
// appliance resolves a device's network by "which enabled subnet contains this address", so two fixtures on
// one subnet make that answer arbitrary and a test can silently resolve into another test's tenant.
// (On a real appliance this cannot happen — one appliance serves one tenant, and netd rejects overlapping
// enabled subnets — so the collision is an artefact of running many tenants in one disposable database.)
var fixtureSeq atomic.Int64

type fixtureNet struct {
	subnet, gateway, guestIP, otherIP, mac string
}

func nextFixtureNet() fixtureNet {
	n := fixtureSeq.Add(1)
	third := int(n % 200)
	return fixtureNet{
		subnet:  fmt.Sprintf("10.77.%d.0/24", third),
		gateway: fmt.Sprintf("10.77.%d.1", third),
		guestIP: fmt.Sprintf("10.77.%d.25", third),
		otherIP: fmt.Sprintf("10.77.%d.99", third),
		mac:     fmt.Sprintf("02:00:00:aa:%02x:%02x", (n>>8)&0xff, n&0xff),
	}
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping scd Phase-3 auth integration")
	}
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)

	f := &authFixture{pool: p, net: nextFixtureNet()}
	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks
	           (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'p3-guests','ens192','br-p3',
	                ($1::text)::inet, ($2::text)::inet, ($3::text)::cidr, true FROM si RETURNING id,tenant_id,site_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), gn.tenant_id, gn.site_id,'protel-fias','ACTIVE' FROM gn RETURNING id,tenant_id,site_id),
	  pir AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	          SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,1,'UTC','{}'::jsonb FROM pi RETURNING id),
	  m AS (INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id,is_default)
	        SELECT gn.tenant_id, gn.site_id, gn.id, pi.id, true FROM gn, pi RETURNING guest_network_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
	                                  normalized_room_number,status,lifecycle_version,last_applied_event_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'RES-4001','STAY-4001','412','IN_HOUSE',1,0 FROM pi RETURNING id,tenant_id,site_id,pms_interface_id),
	  sg AS (INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
	         SELECT st.tenant_id, st.site_id, st.pms_interface_id, st.id,'OKONKWO',true FROM st RETURNING id),
	  -- st2 shares BOTH the room and a surname with st: that is what "ambiguous" means. Two stays that merely
	  -- share a surname in different rooms are not ambiguous, and a fixture like that would prove nothing.
	  st2 AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
	                                   normalized_room_number,status,lifecycle_version,last_applied_event_version)
	          SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'RES-4002','STAY-4002','412','IN_HOUSE',1,0 FROM pi RETURNING id,tenant_id,site_id,pms_interface_id),
	  sg2 AS (INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
	          SELECT st2.tenant_id, st2.site_id, st2.pms_interface_id, st2.id,'SHARED',true FROM st2 RETURNING id),
	  sg3 AS (INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
	          SELECT st.tenant_id, st.site_id, st.pms_interface_id, st.id,'SHARED',false FROM st RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'p3-plan',true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,
	                                                    max_concurrent_devices,device_limit_policy,time_accounting_mode)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,9000,4000,3,'REJECT_NEW_DEVICE','VALIDITY_WINDOW' FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'STAY_INCLUDED',false,true FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,
	                                                        package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id,1,spr.id,'FREE_STAY',0,ARRAY['NOT_REQUIRED']::text[],
	                 '{"mode":"VALIDITY_WINDOW","seconds":86400}'::jsonb FROM ip, spr RETURNING id),
	  pip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	          SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'PREMIUM_PAID',false,true FROM pi RETURNING id,tenant_id,site_id),
	  pipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,
	                                                         package_type,price_minor,settlement_methods,duration_policy)
	           SELECT gen_random_uuid(), pip.tenant_id, pip.site_id, pip.id,1,spr.id,'GENERAL',1500,ARRAY['PMS_CHARGE']::text[],
	                  '{"mode":"VALIDITY_WINDOW","seconds":86400}'::jsonb FROM pip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pir)::text, (SELECT guest_network_id FROM m)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM st2)::text,
	       (SELECT id FROM ipr)::text, (SELECT id FROM pipr)::text`,
		f.net.gateway+"/24", f.net.gateway, f.net.subnet).
		Scan(&f.tenant, &f.site, &f.iface, &f.revision, &f.network, &f.stay, &f.otherStay, &f.pkgRev, &f.priced); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A Stay can only authenticate a guest when it carries FRESH OCCUPANCY EVIDENCE produced by the same
	// Revision the resolution authenticated against — that is the whole point of the Auth Context, and a
	// fixture without it would be testing a path no real guest can take. The tuple is all-or-none, so every
	// column is set together. Separate statement because a data-modifying CTE cannot see a sibling's insert.
	if _, err := p.Exec(ctx, `
		UPDATE iam_v2.stays SET
		  occupancy_evidence_at = now() - interval '10 seconds',
		  occupancy_ingested_at = now() - interval '9 seconds',
		  occupancy_revision_id = $3,
		  occupancy_normalization_version = 1,
		  occupancy_clock_suspect = false,
		  occupancy_evidence_version = 1,
		  room_type = 'SUITE', rate_plan = 'CORP', travel_agent = 'ACME', vip = true,
		  arrival = (now() - interval '3 days')::date, departure = (now() + interval '1 day')::date
		 WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site, f.revision); err != nil {
		t.Fatalf("seed occupancy evidence: %v", err)
	}
	// PUBLISH the interface revision. scd pins the published pointer, never max(revision_no), so a fixture
	// that creates a revision without publishing it is correctly refused — which is the behaviour a Draft
	// mid-configuration must get.
	if _, err := p.Exec(ctx, `
		UPDATE iam_v2.pms_interfaces SET current_revision_id=$3
		 WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site, f.revision); err != nil {
		t.Fatalf("publish the interface revision: %v", err)
	}
	// The catalog's current-revision pointer is a separate statement: a data-modifying CTE cannot see a
	// sibling CTE's insert.
	if _, err := p.Exec(ctx, `
		UPDATE iam_v2.internet_packages ip SET current_revision_id = r.id
		  FROM iam_v2.internet_package_revisions r
		 WHERE r.package_id = ip.id AND ip.tenant_id=$1 AND ip.site_id=$2`, f.tenant, f.site); err != nil {
		t.Fatalf("point packages at their current revision: %v", err)
	}
	f.appliance = mustUUID(t, p)

	f.srv = &server{db: p, tenID: f.tenant, siteID: f.site, applID: f.appliance, legacyBridge: "br-lan"}
	f.p3 = newPhase3Auth(iamv2.PMSConfig{MasterEnabled: true, PMSAuthEnabled: true}, f.srv)
	if f.p3 == nil {
		t.Fatal("the Phase-3 auth arm was not constructed with the flags on")
	}
	return f
}

func mustUUID(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// post drives a handler exactly as the router would.
func post(t *testing.T, h http.HandlerFunc, body any) (*httptest.ResponseRecorder, phase3Response) {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3Response
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable response %q: %v", rec.Body.String(), err)
	}
	return rec, out
}

func (f *authFixture) resolveBody(room, last, res, reqID string) map[string]any {
	return map[string]any{
		"room": room, "last_name": last, "reservation_number": res, "request_id": reqID,
		"device": map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	}
}

// THE HAPPY PATH, and the ordering rule that matters most: the Session exists only because the Entitlement
// does, and both appear in the same commit.
func TestIntegration_Phase3Auth_GuestGetsAccess(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000004-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || res.AuthContextID == "" {
		t.Fatalf("a valid stay did not verify: %+v", res)
	}
	if len(res.Offers) != 1 || res.Offers[0].PackageRevisionID != f.pkgRev {
		// the priced package must NOT be offered: paid access is out of scope and must never be silently
		// granted for free
		t.Fatalf("offers = %+v, want exactly the included package", res.Offers)
	}
	if res.Offers[0].DownKbps != 9000 {
		t.Fatalf("the offer lost its plan rates: %+v", res.Offers[0])
	}

	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.pkgRev,
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var granted phase3GrantResp
	if err := json.Unmarshal(rec.Body.Bytes(), &granted); err != nil {
		t.Fatal(err)
	}
	if granted.Outcome != outcomeVerified || granted.SessionID == "" || granted.EntitlementID == "" {
		t.Fatalf("the grant produced no access: %s", rec.Body.String())
	}

	// the session belongs to that entitlement, that device and that stay — nothing was invented
	var entOfSession, deviceOfSession, stayOfEnt string
	if err := f.pool.QueryRow(ctx, `
		SELECT s.entitlement_id::text, s.device_id::text, e.stay_id::text
		  FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		 WHERE s.id=$1`, granted.SessionID).Scan(&entOfSession, &deviceOfSession, &stayOfEnt); err != nil {
		t.Fatal(err)
	}
	if entOfSession != granted.EntitlementID {
		t.Fatalf("the session is bound to %s, not the granted entitlement", entOfSession)
	}
	if stayOfEnt != f.stay {
		t.Fatalf("access was granted against stay %s, not the resolved one", stayOfEnt)
	}
	// and its attribution history opened at creation, so its usage can never be unattributable
	var bindings int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.session_entitlement_bindings WHERE session_id=$1 AND bound_until IS NULL`,
		granted.SessionID).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 1 {
		t.Fatalf("the session opened %d attribution intervals, want exactly 1", bindings)
	}
	// the whole chain committed: quote, purchase, entitlement history, device authorization
	for _, q := range []struct{ what, sql string }{
		{"purchase", `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND state='GRANTED'`},
		{"entitlement history", `SELECT count(*) FROM iam_v2.entitlement_state_transitions t
		    JOIN iam_v2.entitlements e ON e.id=t.entitlement_id WHERE e.stay_id=$1`},
		{"device authorization", `SELECT count(*) FROM iam_v2.entitlement_device_authorizations a
		    JOIN iam_v2.entitlements e ON e.id=a.entitlement_id WHERE e.stay_id=$1`},
	} {
		var n int
		if err := f.pool.QueryRow(ctx, q.sql, f.stay).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q.what, err)
		}
		if n == 0 {
			t.Fatalf("the grant committed no %s", q.what)
		}
	}
}

// A one-time context is one-time: it can produce AT MOST ONE grant, ever.
//
// "One-time" is a statement about what gets written, not about what the second caller is told. Presenting a
// spent context twice must not produce a second Entitlement, a second Purchase, a second Quote or a second
// Session — that is the property, and it is what this test measures.
//
// What the second caller is told depends on WHO is asking, and the two cases are covered separately:
//
//	the same device   → the session its own earlier grant already created, because the reply to that grant
//	                    may simply have been lost (abandoned at portald's response-time budget, a dropped
//	                    connection). Refusing there would strand a guest who has working access. See
//	                    TestIntegration_Phase3Auth_ARetryAfterALostReplyReturnsTheSameSession.
//	another device    → the uniform non-success. See
//	                    TestIntegration_Phase3Auth_AnotherDeviceCannotClaimTheGrantedSession.
func TestIntegration_Phase3Auth_ContextIsConsumedExactlyOnce(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000005-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatal("setup: the stay did not verify")
	}
	grant := func() phase3GrantResp {
		rec := httptest.NewRecorder()
		raw, _ := json.Marshal(map[string]any{
			"auth_context_id":     res.AuthContextID,
			"package_revision_id": f.pkgRev,
			"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
		})
		f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		var out phase3GrantResp
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	first := grant()
	if first.Outcome != outcomeVerified {
		t.Fatal("the first grant failed")
	}
	second := grant()

	// THE PROPERTY: nothing was granted a second time. Every row family the grant writes is counted, because
	// a duplicate anywhere in the chain is a duplicate grant — an extra Purchase says the Folio was billed
	// twice, an extra Session is a second period of access nobody authorised.
	counts := map[string]int{}
	for name, sql := range map[string]string{
		"entitlements": `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`,
		"sessions": `SELECT count(*) FROM iam_v2.sessions s
			JOIN iam_v2.entitlements e ON e.id=s.entitlement_id WHERE e.stay_id=$1`,
		"purchases": `SELECT count(*) FROM iam_v2.purchases p
			JOIN iam_v2.auth_contexts c ON c.id=p.auth_context_id WHERE c.stay_id=$1`,
		"device authorizations": `SELECT count(*) FROM iam_v2.entitlement_device_authorizations a
			JOIN iam_v2.entitlements e ON e.id=a.entitlement_id WHERE e.stay_id=$1`,
	} {
		var n int
		if err := f.pool.QueryRow(ctx, sql, f.stay).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", name, err)
		}
		counts[name] = n
		if n != 1 {
			t.Fatalf("presenting the context twice produced %d %s, want exactly 1", n, name)
		}
	}

	// And the second answer names that same single grant rather than a new one. (A refusal here would also
	// satisfy "one-time", but would strand a guest whose first reply was lost — see the doc comment.)
	if second.SessionID != first.SessionID || second.EntitlementID != first.EntitlementID {
		t.Fatalf("the second presentation named different access:\n  first  %+v\n  second %+v", first, second)
	}
}

// THE UNIFORM CONTRACT. Wrong room, wrong name, an ambiguous match, an off-network device and a malformed
// body must be indistinguishable — same status, same body, byte for byte. Anything else makes the endpoint an
// occupancy oracle.
func TestIntegration_Phase3Auth_EveryNonSuccessIsIdentical(t *testing.T) {
	f := newAuthFixture(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"no such room", f.resolveBody("999", "Okonkwo", "", "00000008-0000-4000-8000-000000000000")},
		{"right room, wrong name", f.resolveBody("412", "Nobody", "", "00000009-0000-4000-8000-000000000000")},
		{"ambiguous: one surname, two stays", f.resolveBody("412", "Shared", "", "0000000a-0000-4000-8000-000000000000")},
		{"no evidence at all", f.resolveBody("", "", "", "0000000b-0000-4000-8000-000000000000")},
		{"missing request id", f.resolveBody("412", "Okonkwo", "", "")},
		{"malformed request id", f.resolveBody("412", "Okonkwo", "", "not-a-uuid")},
		{"device not on a guest network", map[string]any{
			"room": "412", "last_name": "Okonkwo", "request_id": "0000000c-0000-4000-8000-000000000000",
			"device": map[string]string{"ip": "192.0.2.10", "mac": f.net.mac}}},
		{"unusable hardware address", map[string]any{
			"room": "412", "last_name": "Okonkwo", "request_id": "0000000d-0000-4000-8000-000000000000",
			"device": map[string]string{"ip": f.net.guestIP, "mac": "not-a-mac"}}},
	}

	var canonicalStatus int
	var canonicalBody string
	for i, c := range cases {
		raw, _ := json.Marshal(c.body)
		rec := httptest.NewRecorder()
		f.p3.resolveHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		if i == 0 {
			canonicalStatus, canonicalBody = rec.Code, rec.Body.String()
			var out phase3Response
			if json.Unmarshal([]byte(canonicalBody), &out) != nil || out.Outcome != outcomeNotVerified {
				t.Fatalf("the canonical non-success is not a clean NOT_VERIFIED: %s", canonicalBody)
			}
			if out.AuthContextID != "" || len(out.Offers) != 0 {
				t.Fatalf("a non-success leaked a context or an offer: %s", canonicalBody)
			}
			continue
		}
		if rec.Code != canonicalStatus || rec.Body.String() != canonicalBody {
			t.Fatalf("%s is distinguishable from a plain no-match:\n  got  %d %s\n  want %d %s",
				c.name, rec.Code, rec.Body.String(), canonicalStatus, canonicalBody)
		}
	}
}

// An ambiguous match must never be resolved by picking. Two stays sharing a surname is precisely the case
// where guessing hands one guest another's access.
func TestIntegration_Phase3Auth_AmbiguityGrantsNothing(t *testing.T) {
	f := newAuthFixture(t)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Shared", "", "00000001-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeNotVerified {
		t.Fatalf("an ambiguous match verified: %+v", res)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.entitlements WHERE stay_id IN ($1,$2)`, f.stay, f.otherStay).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("an ambiguous resolution produced %d entitlements", n)
	}
}

// A PAID package is refused even when the guest names it directly. Paid access is out of scope for this
// phase, and the failure mode to prevent is granting it for free because nothing checked the price.
func TestIntegration_Phase3Auth_PricedPackageIsRefused(t *testing.T) {
	f := newAuthFixture(t)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000006-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatal("setup: the stay did not verify")
	}
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.priced, // never offered; named directly
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Outcome == outcomeVerified {
		t.Fatal("a priced package was granted for free")
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a refused paid grant still created %d entitlements", n)
	}
}

// A context pinned to one device is unusable from another. Otherwise a guest who verified on their phone
// could hand the context to any device on the network.
func TestIntegration_Phase3Auth_ContextIsBoundToItsDevice(t *testing.T) {
	f := newAuthFixture(t)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000002-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatal("setup: the stay did not verify")
	}
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.pkgRev,
		"device":              map[string]string{"ip": f.net.otherIP, "mac": "02:00:00:bb:00:02"}, // another device on the same network
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Outcome == outcomeVerified {
		t.Fatal("an Auth Context was redeemed from a different device")
	}
}

// Resolution is idempotent per request id: a retried submit (the guest's second tap, or a flaky network)
// records ONE resolution, not two, and returns the same answer.
func TestIntegration_Phase3Auth_RetryIsIdempotent(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	_, first := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000007-0000-4000-8000-000000000000"))
	_, second := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000007-0000-4000-8000-000000000000"))
	if first.Outcome != outcomeVerified || second.Outcome != outcomeVerified {
		t.Fatalf("a retried resolution changed its answer: %+v then %+v", first, second)
	}
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.auth_resolutions WHERE resolution_request_id=$1`, "00000007-0000-4000-8000-000000000000").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a retried resolution recorded %d resolutions, want 1", n)
	}
}

// While DARK the arm is not constructed at all, so scd mounts no Phase-3 route and issues no Phase-3 SQL.
func TestIntegration_Phase3Auth_DarkConstructsNothing(t *testing.T) {
	f := newAuthFixture(t)
	for _, cfg := range []iamv2.PMSConfig{
		{},
		{MasterEnabled: true},  // master alone is not the auth surface
		{PMSAuthEnabled: true}, // a child flag without its master
		{MasterEnabled: true, AdminEnabled: true}, // a different surface entirely
	} {
		if arm := newPhase3Auth(cfg, f.srv); arm != nil {
			t.Fatalf("the auth arm was constructed while dark: %+v", cfg)
		}
	}
}

// The Auth Context expires. A verified identity left unused is not a standing permission.
func TestIntegration_Phase3Auth_ExpiredContextGrantsNothing(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000003-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatal("setup: the stay did not verify")
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE iam_v2.auth_contexts SET expires_at = now() - interval '1 second' WHERE id=$1`, res.AuthContextID); err != nil {
		t.Fatalf("age the context: %v", err)
	}
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.pkgRev,
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Outcome == outcomeVerified {
		t.Fatal("an expired Auth Context still granted access")
	}
	_ = time.Now
}

// ---- offers, published revisions, and grant binding -------------------------

// addPackage creates a current, visible, free package with the given eligibility rules, and returns its
// revision id. It is the shape a property actually configures: a package plus the rules that decide who sees
// it.
func (f *authFixture) addPackage(t *testing.T, code string, rules []map[string]any) string {
	t.Helper()
	ctx := context.Background()
	var pkgRev string
	if err := f.pool.QueryRow(ctx, `WITH
	  sp AS (SELECT id FROM iam_v2.service_plan_revisions WHERE tenant_id=$1 AND site_id=$2 LIMIT 1),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,$3,false,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions
	            (id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,
	             price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,sp.id,'FREE_STAY',0,ARRAY['NOT_REQUIRED']::text[],
	                 '{"mode":"VALIDITY_WINDOW","seconds":86400}'::jsonb FROM ip, sp RETURNING id)
	SELECT (SELECT id FROM ipr)::text`, f.tenant, f.site, code).Scan(&pkgRev); err != nil {
		t.Fatalf("add package %s: %v", code, err)
	}
	if _, err := f.pool.Exec(ctx, `
		UPDATE iam_v2.internet_packages ip SET current_revision_id = r.id
		  FROM iam_v2.internet_package_revisions r
		 WHERE r.id=$1 AND ip.id = r.package_id`, pkgRev); err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		raw, _ := json.Marshal(r["value"])
		if _, err := f.pool.Exec(ctx, `
			INSERT INTO iam_v2.package_eligibility_rules
			  (tenant_id,site_id,package_revision_id,rule_type,rule_value)
			VALUES ($1,$2,$3,$4,$5::jsonb)`,
			f.tenant, f.site, pkgRev, r["type"], string(raw)); err != nil {
			t.Fatalf("add rule %v: %v", r["type"], err)
		}
	}
	return pkgRev
}

// THE OFFER SET IS DECIDED BY THE RULES, not by the catalogue. The suite package is offered because this
// Stay is in a SUITE; the standard-only package is not offered at all, even though it is free and visible.
func TestIntegration_Phase3Auth_OffersFollowStayEligibility(t *testing.T) {
	f := newAuthFixture(t)

	suiteOnly := f.addPackage(t, "SUITE_ONLY", []map[string]any{
		{"type": "ROOM_TYPE", "value": map[string]any{"room_types": []string{"SUITE"}}}})
	standardOnly := f.addPackage(t, "STANDARD_ONLY", []map[string]any{
		{"type": "ROOM_TYPE", "value": map[string]any{"room_types": []string{"STANDARD"}}}})

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000ff01-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("the stay did not verify: %+v", res)
	}
	offered := map[string]bool{}
	for _, o := range res.Offers {
		offered[o.PackageRevisionID] = true
	}
	if !offered[suiteOnly] {
		t.Fatal("a package this SUITE stay qualifies for was not offered")
	}
	if offered[standardOnly] {
		t.Fatal("a package restricted to STANDARD rooms was offered to a SUITE stay")
	}
	if !offered[f.pkgRev] {
		t.Fatal("the unrestricted included package was not offered")
	}
}

// A guest may only redeem what was OFFERED TO THEM. Naming another free, generally-grantable package on the
// site — one whose rules they do not satisfy — must fail closed, because "grantable" is not "authorised".
func TestIntegration_Phase3Auth_GrantIsBoundToTheOfferedSet(t *testing.T) {
	f := newAuthFixture(t)
	standardOnly := f.addPackage(t, "STANDARD_ONLY", []map[string]any{
		{"type": "ROOM_TYPE", "value": map[string]any{"room_types": []string{"STANDARD"}}}})

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000ff02-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatal("setup: the stay did not verify")
	}
	for _, o := range res.Offers {
		if o.PackageRevisionID == standardOnly {
			t.Fatal("setup: the ineligible package was offered")
		}
	}

	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": standardOnly, // free, visible, current — but never offered to this Stay
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Outcome == outcomeVerified {
		t.Fatal("a package that was never offered to this Stay was granted")
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("an unoffered grant created %d entitlements", n)
	}
}

// An UNPUBLISHED higher-numbered Draft must never be pinned. Somebody mid-way through configuring a connector
// change has not authorised anything, and the Auth Context would record their draft as the authority for a
// guest's access.
func TestIntegration_Phase3Auth_PinsThePublishedRevisionNotTheHighest(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	var draft string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.pms_interface_revisions
		  (id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
		VALUES (gen_random_uuid(),$1,$2,$3,99,'UTC','{}'::jsonb) RETURNING id::text`,
		f.tenant, f.site, f.iface).Scan(&draft); err != nil {
		t.Fatalf("create the draft: %v", err)
	}
	// the publication pointer still names revision 1; the draft is not current
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "0000ff03-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatalf("the stay did not verify: %+v", res)
	}
	var pinned string
	if err := f.pool.QueryRow(ctx,
		`SELECT authentication_interface_revision_id::text FROM iam_v2.auth_contexts WHERE id=$1`,
		res.AuthContextID).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if pinned == draft {
		t.Fatal("the Auth Context pinned an unpublished Draft revision")
	}
	if pinned != f.revision {
		t.Fatalf("pinned %s, want the published revision %s", pinned, f.revision)
	}
}

// ONE RESOLUTION, ONE LIVE CONTEXT. A guest tapping Connect repeatedly on a bad connection must not leave a
// pile of independently redeemable credentials behind for one identity proof.
func TestIntegration_Phase3Auth_OneResolutionYieldsOneLiveContext(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	const reqID = "0000ff04-0000-4000-8000-000000000000"

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
		if res.Outcome != outcomeVerified {
			t.Fatalf("attempt %d did not verify: %+v", i, res)
		}
		seen[res.AuthContextID] = true
	}
	if len(seen) != 1 {
		t.Fatalf("five retries of one resolution minted %d distinct contexts", len(seen))
	}
	var live int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM iam_v2.auth_contexts
		 WHERE resolution_request_id=$1::uuid AND consumed_at IS NULL`, reqID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live contexts for one resolution = %d, want exactly 1", live)
	}

	// once REDEEMED, a further retry must not silently hand back a fresh credential for the spent proof
	var ctxID string
	for id := range seen {
		ctxID = id
	}
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     ctxID,
		"package_revision_id": f.pkgRev,
		"device":              map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var granted phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &granted)
	if granted.Outcome != outcomeVerified {
		t.Fatalf("the grant failed: %s", rec.Body.String())
	}
	_, after := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", reqID))
	if after.Outcome == outcomeVerified && after.AuthContextID == ctxID {
		t.Fatal("a consumed context was handed back for reuse")
	}
}

// Concurrent replays of one resolution converge on a single context: the uniqueness is enforced by the
// database, not by the handler happening to be called sequentially.
func TestIntegration_Phase3Auth_ConcurrentReplaysConvergeOnOneContext(t *testing.T) {
	f := newAuthFixture(t)
	const reqID = "0000ff05-0000-4000-8000-000000000000"

	var mu sync.Mutex
	ids := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(f.resolveBody("412", "Okonkwo", "", reqID))
			rec := httptest.NewRecorder()
			f.p3.resolveHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
			var out phase3Response
			if json.Unmarshal(rec.Body.Bytes(), &out) == nil && out.AuthContextID != "" {
				mu.Lock()
				ids[out.AuthContextID] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(ids) > 1 {
		t.Fatalf("24 concurrent replays produced %d distinct contexts", len(ids))
	}
	var live int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM iam_v2.auth_contexts
		 WHERE resolution_request_id=$1::uuid AND consumed_at IS NULL`, reqID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live > 1 {
		t.Fatalf("live contexts after concurrent replay = %d", live)
	}
}
