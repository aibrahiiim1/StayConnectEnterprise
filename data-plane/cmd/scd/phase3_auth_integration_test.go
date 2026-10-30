//go:build integration

package main

// Phase-3 guest path end to end through scd's real handlers against PostgreSQL: composition-root tests of the
// wiring (correct operations called with the wrong ids) and of what it leaks.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
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

// Each fixture gets its own subnet and MAC: the appliance resolves a network by which enabled subnet contains
// the address, so a shared subnet could resolve into another test's tenant.
//
// THE ALLOCATOR IS THE DATABASE, NOT A COUNTER. The first version numbered fixtures from a process-wide counter over 200
// subnets (10.77.N.0/24) and never released one: every fixture left an enabled network behind. A second package, a second run, or
// one more test than the pool held then reused a subnet still enabled in another tenant, and a sign-in resolved into the wrong
// tenant ("no pending session to activate"); a later run on the same database found "no free 10.77.x fixture subnet remains".
// Now a fixture claims a /24 out of 8,192 (10.192.0.0/11, clear of every fixed fixture subnet in the other suites) under a
// session-level advisory lock held on its own connection for the life of the test, so concurrent PROCESSES cannot claim the same
// one, a crashed process releases its claim by itself, and an enabled network on the subnet also counts as taken. When the test
// ends its networks are disabled (a disabled network resolves no device) and the claim is released, so the pool recycles and a
// database can be shared by any number of runs and packages.
type fixtureNet struct {
	subnet, gateway, guestIP, otherIP, mac string
	index                                  int
}

const (
	fixtureNetSecondOctet = 192       // the pool is 10.192.0.0 .. 10.223.255.255
	fixtureNetPool        = 32 * 256  // /24 subnets in it
	fixtureNetLockBase    = 0x5CD0F000 // advisory-lock keys fixtureNetLockBase .. +fixtureNetPool-1
)

func fixtureNetAt(i int) fixtureNet {
	second, third := fixtureNetSecondOctet+i/256, i%256
	return fixtureNet{
		subnet:  fmt.Sprintf("10.%d.%d.0/24", second, third),
		gateway: fmt.Sprintf("10.%d.%d.1", second, third),
		guestIP: fmt.Sprintf("10.%d.%d.25", second, third),
		otherIP: fmt.Sprintf("10.%d.%d.99", second, third),
		mac:     fmt.Sprintf("02:00:00:aa:%02x:%02x", (i>>8)&0xff, i&0xff),
		index:   i,
	}
}

// host is the n-th address of the fixture's /24.
func (n fixtureNet) host(h int) string {
	return strings.TrimSuffix(n.subnet, ".0/24") + "." + strconv.Itoa(h)
}

// claimFixtureNet reserves a client-network /24 for one test and returns its addresses. See fixtureNet.
func claimFixtureNet(t *testing.T, p *pgxpool.Pool) fixtureNet {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Fatal("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("fixture subnet claim: connect: %v", err)
	}
	start := mrand.IntN(fixtureNetPool)
	for k := 0; k < fixtureNetPool; k++ {
		i := (start + k) % fixtureNetPool
		n := fixtureNetAt(i)
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(fixtureNetLockBase+i)).Scan(&locked); err != nil {
			_ = conn.Close(ctx)
			t.Fatalf("fixture subnet claim: lock: %v", err)
		}
		if !locked {
			continue
		}
		var taken bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM public.guest_networks WHERE enabled AND subnet_cidr && $1::cidr)`, n.subnet).Scan(&taken); err != nil {
			_ = conn.Close(ctx)
			t.Fatalf("fixture subnet claim: read: %v", err)
		}
		if taken {
			_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, int64(fixtureNetLockBase+i))
			continue
		}
		t.Cleanup(func() {
			// Free the subnet for the next test: nothing resolves a device into a disabled network.
			if _, err := p.Exec(context.Background(),
				`UPDATE public.guest_networks SET enabled = false WHERE enabled AND subnet_cidr && $1::cidr`, n.subnet); err != nil {
				t.Logf("fixture subnet %s was not released (its networks stay enabled): %v", n.subnet, err)
			}
			_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(fixtureNetLockBase+i))
			_ = conn.Close(context.Background())
		})
		return n
	}
	_ = conn.Close(ctx)
	t.Fatalf("no free fixture subnet among %d: something holds every one of them enabled", fixtureNetPool)
	return fixtureNet{}
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

	f := &authFixture{pool: p, net: claimFixtureNet(t, p)}
	// The stay family is writable only inside a controlled operation, so the whole seed runs in one transaction
	// that opens it. Parent interface and bridge derive from the site id (both are unique in production).
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open controlled operation: %v", err)
	}
	if err := tx.QueryRow(ctx, `WITH
	  -- Room sign-in switched ON in the hotel's method settings, as a property using it has it: scd refuses
	  -- a switched-off method before it looks up any room.
	  t AS (INSERT INTO public.tenants(id, slug, name, auth_methods) SELECT g, g::text, 't',
	        '{"voucher":{"enabled":true},"pms":{"enabled":true,"mode":"room_any"}}'::jsonb FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks
	           (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'p3-guests','p'||substr(md5(si.id::text),1,12),'b'||substr(md5(si.id::text),1,12),
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
	           SELECT gen_random_uuid(), pip.tenant_id, pip.site_id, pip.id,1,spr.id,'GENERAL',1500,ARRAY['PMS_POSTING']::text[],
	                  '{"mode":"VALIDITY_WINDOW","seconds":86400}'::jsonb FROM pip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pir)::text, (SELECT guest_network_id FROM m)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM st2)::text,
	       (SELECT id FROM ipr)::text, (SELECT id FROM pipr)::text`,
		f.net.gateway+"/24", f.net.gateway, f.net.subnet).
		Scan(&f.tenant, &f.site, &f.iface, &f.revision, &f.network, &f.stay, &f.otherStay, &f.pkgRev, &f.priced); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A Stay authenticates only with fresh occupancy evidence from the same Revision; the tuple is
	// all-or-none. Separate statement because a data-modifying CTE cannot see a sibling's insert.
	if _, err := tx.Exec(ctx, `
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
	// scd pins the published pointer, never max(revision_no), so an unpublished revision would be refused.
	if _, err := tx.Exec(ctx, `
		UPDATE iam_v2.pms_interfaces SET current_revision_id=$3
		 WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site, f.revision); err != nil {
		t.Fatalf("publish the interface revision: %v", err)
	}
	// A healthy runtime row: authorising a guest also requires a connected, in-sync feed (tested in internal/authctx).
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode, published_resync_generation,
		 pinned_revision_id, transport_status, sync_status, continuity_status, last_connected_at, last_heartbeat_at)
		SELECT $1,$2,i.id,1,'NONE',0,$3,'CONNECTED','IN_SYNC','CONTINUOUS',now(),now()
		  FROM iam_v2.pms_interfaces i WHERE i.tenant_id=$1 AND i.site_id=$2
		ON CONFLICT DO NOTHING`, f.tenant, f.site, f.revision); err != nil {
		t.Fatalf("seed pms interface runtime: %v", err)
	}
	// Separate statement: a data-modifying CTE cannot see a sibling CTE's insert.
	if _, err := tx.Exec(ctx, `
		UPDATE iam_v2.internet_packages ip SET current_revision_id = r.id
		  FROM iam_v2.internet_package_revisions r
		 WHERE r.package_id = ip.id AND ip.tenant_id=$1 AND ip.site_id=$2`, f.tenant, f.site); err != nil {
		t.Fatalf("point packages at their current revision: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	f.appliance = mustUUID(t, p)

	f.srv = &server{db: p, tenID: f.tenant, siteID: f.site, applID: f.appliance, legacyBridge: "br-lan", lic: devLicence()}
	f.p3 = newPhase3Auth(iamv2.PMSConfig{MasterEnabled: true, PMSAuthEnabled: true}, f.srv)
	if f.p3 == nil {
		t.Fatal("the Phase-3 auth arm was not constructed with the flags on")
	}
	return f
}

// controlledExec runs a fixture write in a transaction that opened the controlled operation; the triggers do
// not let the superuser through as the owner.
func controlledExec(ctx context.Context, p *pgxpool.Pool, family, sql string, args ...any) (pgconn.CommandTag, error) {
	tx, err := p.Begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, family); err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return tag, err
	}
	return tag, tx.Commit(ctx)
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

// The Session exists only because the Entitlement does, and both appear in the same commit.
func TestIntegration_Phase3Auth_GuestGetsAccess(t *testing.T) {
	f := newAuthFixture(t)
	// A grant succeeds only once the enforcement owner promotes the session.
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000004-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || res.AuthContextID == "" {
		t.Fatalf("a valid stay did not verify: %+v", res)
	}
	if len(res.Offers) != 1 || res.Offers[0].PackageRevisionID != f.pkgRev {
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
	var bindings int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.session_entitlement_bindings WHERE session_id=$1 AND bound_until IS NULL`,
		granted.SessionID).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 1 {
		t.Fatalf("the session opened %d attribution intervals, want exactly 1", bindings)
	}
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

// A spent context presented twice writes no second Entitlement, Purchase, Quote or Session. The same device
// gets its earlier session back (the reply may have been lost); another device gets the uniform non-success
// (see ...ARetryAfterALostReplyReturnsTheSameSession and ...AnotherDeviceCannotClaimTheGrantedSession).
func TestIntegration_Phase3Auth_ContextIsConsumedExactlyOnce(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
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

	// Every row family the grant writes is counted; a duplicate anywhere is a duplicate grant (e.g. double billing).
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

	// A refusal would also satisfy "one-time" but would strand a guest whose first reply was lost.
	if second.SessionID != first.SessionID || second.EntitlementID != first.EntitlementID {
		t.Fatalf("the second presentation named different access:\n  first  %+v\n  second %+v", first, second)
	}
}

// Everything that describes one room answers identically (same status, same bytes), or the lobby could
// enumerate occupied rooms. Only conditions true for every guest on the site are a different class
// (internal/signinattempt.GuestClass).
func TestIntegration_Phase3Auth_EveryPerRoomNonSuccessIsIdentical(t *testing.T) {
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
			if out.FailureClass != string(signinattempt.GuestCredential) {
				t.Fatalf("a per-room refusal is class %q, want CREDENTIAL", out.FailureClass)
			}
			continue
		}
		if rec.Code != canonicalStatus || rec.Body.String() != canonicalBody {
			t.Fatalf("%s is distinguishable from a plain no-match — submitting room numbers would reveal "+
				"which rooms are occupied:\n  got  %d %s\n  want %d %s",
				c.name, rec.Code, rec.Body.String(), canonicalStatus, canonicalBody)
		}
	}
}

// A device on no mapped guest network is device state, not room state, so it is TECHNICAL; "check your
// details" would send the guest in a loop.
func TestIntegration_Phase3Auth_AnOffNetworkDeviceIsTechnicalNotCredential(t *testing.T) {
	f := newAuthFixture(t)
	raw, _ := json.Marshal(map[string]any{
		"room": "412", "last_name": "Okonkwo", "request_id": "0000000c-0000-4000-8000-000000000000",
		"device": map[string]string{"ip": "192.0.2.10", "mac": f.net.mac}})
	rec := httptest.NewRecorder()
	f.p3.resolveHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))

	var out phase3Response
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("undecodable body %q", rec.Body.String())
	}
	if rec.Code != http.StatusOK || out.Outcome != outcomeNotVerified {
		t.Fatalf("unexpected answer: %d %s", rec.Code, rec.Body.String())
	}
	if out.FailureClass != string(signinattempt.GuestTechnical) {
		t.Fatalf("failure_class = %q, want TECHNICAL", out.FailureClass)
	}
	if out.AuthContextID != "" || len(out.Offers) != 0 {
		t.Fatalf("the refusal leaked a context or an offer: %s", rec.Body.String())
	}
}

// An ambiguous match is never resolved by picking; that would hand one guest another's access.
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

// A paid package is refused even when named directly, so nothing is granted free for lack of a price check.
func TestIntegration_Phase3Auth_PricedPackageIsRefused(t *testing.T) {
	f := newAuthFixture(t)
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000006-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatal("setup: the stay did not verify")
	}
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id":     res.AuthContextID,
		"package_revision_id": f.priced,
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

// A context pinned to one device is unusable from another.
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
		"device":              map[string]string{"ip": f.net.otherIP, "mac": "02:00:00:bb:00:02"},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var out phase3GrantResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Outcome == outcomeVerified {
		t.Fatal("an Auth Context was redeemed from a different device")
	}
}

// A retried submit with the same request id records one resolution and returns the same answer.
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
		// Scoped to this fixture: the request id is constant and a reused database holds other tenants' rows with it.
		`SELECT count(*) FROM iam_v2.auth_resolutions
		  WHERE resolution_request_id=$1 AND tenant_id=$2 AND site_id=$3`,
		"00000007-0000-4000-8000-000000000000", f.tenant, f.site).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a retried resolution recorded %d resolutions, want 1", n)
	}
}

// While dark the arm is not constructed: no Phase-3 route and no Phase-3 SQL.
func TestIntegration_Phase3Auth_DarkConstructsNothing(t *testing.T) {
	f := newAuthFixture(t)
	for _, cfg := range []iamv2.PMSConfig{
		{},
		{MasterEnabled: true},
		{PMSAuthEnabled: true},
		{MasterEnabled: true, AdminEnabled: true},
	} {
		if arm := newPhase3Auth(cfg, f.srv); arm != nil {
			t.Fatalf("the auth arm was constructed while dark: %+v", cfg)
		}
	}
}

// An expired Auth Context grants nothing.
func TestIntegration_Phase3Auth_ExpiredContextGrantsNothing(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000003-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified {
		t.Fatal("setup: the stay did not verify")
	}
	if _, err := controlledExec(ctx, f.pool, "auth_context",
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

// addPackage creates a current, visible, free package with the given eligibility rules and returns its revision id.
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

// The offer set follows the rules, not the catalogue: a STANDARD-only package is not offered to a SUITE stay.
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

// Naming a grantable package that was never offered must fail closed: "grantable" is not "authorised".
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
		"package_revision_id": standardOnly,
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

// An unpublished higher-numbered Draft must never be pinned.
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

// Repeated taps must not leave several redeemable credentials for one identity proof.
func TestIntegration_Phase3Auth_OneResolutionYieldsOneLiveContext(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
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
		 WHERE resolution_request_id=$1::uuid AND consumed_at IS NULL
		   AND tenant_id=$2 AND site_id=$3`, reqID, f.tenant, f.site).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live contexts for one resolution = %d, want exactly 1", live)
	}

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

// Concurrent replays converge on one context; the database enforces the uniqueness.
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
		 WHERE resolution_request_id=$1::uuid AND consumed_at IS NULL
		   AND tenant_id=$2 AND site_id=$3`, reqID, f.tenant, f.site).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live > 1 {
		t.Fatalf("live contexts after concurrent replay = %d", live)
	}
}

// startEnforcementOwner stands in for netd (there is no kernel here): it promotes PENDING_ENFORCEMENT sessions
// through the real controlled writer, exercising its coherence rules. The returned function stops it.
func (f *authFixture) startEnforcementOwner(t *testing.T) func() {
	t.Helper()
	return f.startEnforcementOwnerAtEpoch(t, 1)
}

// startEnforcementOwnerAtEpoch enforces under the given class generation; a second session on the same address
// needs a newer one, so a test stops the first owner and starts another at a higher epoch.
func (f *authFixture) startEnforcementOwnerAtEpoch(t *testing.T, epoch int64) func() {
	t.Helper()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
			rows, err := f.pool.Query(context.Background(), `
				SELECT s.id::text, COALESCE(s.ingress_interface,''), host(s.ip), s.device_id::text
				  FROM iam_v2.sessions s
				 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.state='PENDING_ENFORCEMENT' AND s.ended IS NULL`,
				f.tenant, f.site)
			if err != nil {
				continue
			}
			type pend struct{ id, bridge, ip, device string }
			var pending []pend
			for rows.Next() {
				var p pend
				if rows.Scan(&p.id, &p.bridge, &p.ip, &p.device) == nil {
					pending = append(pending, p)
				}
			}
			rows.Close()
			for _, p := range pending {
				minor, ok := shape.MinorForIP(net.ParseIP(p.ip))
				if !ok {
					continue
				}
				// Same order as netd: activation verifies the accounting origin, so register it first.
				_, _ = f.pool.Exec(context.Background(), `
					SELECT iam_v2.register_class_origin($1,$2,$3::uuid,$4::uuid,$5,$6,$7,0,0,now())`,
					f.tenant, f.site, p.id, p.device, p.bridge, minor, epoch)
				_, _ = f.pool.Exec(context.Background(),
					`SELECT iam_v2.activate_session_enforcement($1,$2,$3::uuid,$4,$5,$6)`,
					f.tenant, f.site, p.id, p.bridge, minor, epoch)
			}
		}
	}()
	return func() { close(done); <-stopped }
}
