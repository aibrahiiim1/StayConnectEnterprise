//go:build integration && phase6

package main

// THE FOUR CONTROL COMBINATIONS, end to end, over real HTTP, against a real PostgreSQL carrying the
// authoritative chain — and the local-first guarantee that goes with them.
//
// Phase 6 has TWO controls that decide whether a guest can manage their own devices, and they are checked in
// different places, at different times, by different mechanisms:
//
//   the DEPLOYMENT GATE   decides whether the routes are MOUNTED. Read once at startup. Off means the path
//                         is ABSENT — a 404 — not a handler that refuses.
//   the PRODUCT SETTING   decides whether this hotel OFFERS the capability. Read from the local database on
//                         every request, so an operator switching it off takes effect without a restart and
//                         so the decision survives with no Central Control Plane.
//
// Only the fourth combination may give a guest the capability. The other three must not, and each must fail
// in its OWN way: absent when the software does not carry the feature, uniformly unavailable when the hotel
// has not asked for it. Proving that requires all four to be exercised against the same real subject, which
// is what this file does — the entitlement, the devices and the session are created by the REAL Phase-3
// grant path, not seeded into a shape convenient for the assertion.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/deviceselfservice"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// ---- the fixture: one real guest with two real devices --------------------------------------------------

type p6E2E struct {
	*authFixture
	ent      string // the entitlement the grant produced
	online   string // device 1: the device that walked the Phase-3 path and holds a live session
	offline  string // device 2: authorized through the real admission primitive, no session
	operator string
}

// claimFreeFixtureNet points the shared fixture generator at a guest subnet that is NOT already present in
// this database.
//
// The generator numbers subnets from a process-local counter, which is correct within one run and wrong
// across runs: a disposable database that has been used before already holds 10.77.1.0/24 and its
// neighbours, and the appliance resolves a device's network by "which enabled subnet contains this address".
// Two networks with one subnet make that answer arbitrary, and the symptom is not a clear failure -- it is a
// resolution that lands in another run's tenant and then trips a foreign key.
//
// It is deliberately scoped to THIS file rather than pushed into the shared fixture. One of the Phase-3
// activation tests depends on two fixtures sharing a traffic class, so per-fixture subnets would break its
// premise -- and quietly changing what a Phase-3 regression tests is not a thing to do while landing Phase 6.
func claimFreeFixtureNet(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		return // the fixture itself will skip
	}
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return
	}
	defer p.Close()
	rows, err := p.Query(ctx,
		`SELECT DISTINCT split_part(host(network(subnet_cidr)), '.', 3)::int
		   FROM public.guest_networks WHERE subnet_cidr <<= '10.77.0.0/16'`)
	if err != nil {
		return
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err == nil {
			used[n] = true
		}
	}
	for n := 1; n < 200; n++ {
		if !used[n] {
			fixtureSeq.Store(int64(n - 1)) // the generator's next Add(1) yields n
			return
		}
	}
	t.Skip("no free 10.77.x fixture subnet remains in this database")
}

func newP6E2E(t *testing.T) *p6E2E {
	t.Helper()
	claimFreeFixtureNet(t)
	f := newAuthFixture(t)
	t.Cleanup(f.startEnforcementOwner(t))
	ctx := context.Background()

	// A REAL grant. Everything downstream operates on what the product actually produces.
	_, res := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000006-0000-4000-8000-000000000000"))
	if res.Outcome != outcomeVerified || res.AuthContextID == "" {
		t.Fatalf("the fixture guest could not resolve: %+v", res)
	}
	rec := httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id": res.AuthContextID, "package_revision_id": f.pkgRev,
		"device": map[string]string{"ip": f.net.guestIP, "mac": f.net.mac},
	})
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	var granted phase3GrantResp
	if err := json.Unmarshal(rec.Body.Bytes(), &granted); err != nil {
		t.Fatal(err)
	}
	if granted.Outcome != outcomeVerified || granted.EntitlementID == "" {
		t.Fatalf("the fixture guest got no access: %s", rec.Body.String())
	}

	e := &p6E2E{authFixture: f, ent: granted.EntitlementID}
	if err := f.pool.QueryRow(ctx,
		`SELECT device_id::text FROM iam_v2.sessions WHERE id=$1`, granted.SessionID).Scan(&e.online); err != nil {
		t.Fatalf("the granted session has no device: %v", err)
	}

	// The appliance and the operator this setting will be scoped to. Both are platform rows the appliance
	// already has in production; the composite foreign key on the setting is what makes "an appliance of
	// this tenant and site" a database fact rather than a convention.
	// The serial is built in Go: passing the same placeholder as both a uuid and a text would make
	// PostgreSQL deduce two types for one parameter, which is an error rather than a coercion.
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.appliances(id,tenant_id,site_id,serial,name)
		VALUES ($1,$2,$3,$4,'phase6-e2e') ON CONFLICT (id) DO NOTHING`,
		f.appliance, f.tenant, f.site, "E2E-"+f.appliance[:8]); err != nil {
		t.Fatalf("seed appliance: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO public.operators(id,tenant_id,email,name)
		VALUES (gen_random_uuid(),$1,'e2e-operator@example.invalid','E2E Operator') RETURNING id::text`,
		f.tenant).Scan(&e.operator); err != nil {
		t.Fatalf("seed operator: %v", err)
	}

	// A SECOND device on the SAME entitlement, admitted through the approved primitive and left OFFLINE
	// (no session). This is the device a guest is allowed to remove, and it exists because a fixture with
	// only an online device could never demonstrate a successful release.
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (id,tenant_id,site_id,appliance_id,mac,last_seen)
		VALUES (gen_random_uuid(),$1,$2,$3,$4::macaddr, now() - interval '2 hours') RETURNING id::text`,
		f.tenant, f.site, f.appliance, secondMAC(f.net.mac)).Scan(&e.offline); err != nil {
		t.Fatalf("seed the offline device: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, e.ent, e.offline); err != nil {
		t.Fatalf("admit the offline device: %v", err)
	}
	return e
}

// secondMAC derives a distinct MAC from the fixture's own, because device identity is
// (tenant, site, appliance, MAC) and a collision would silently make one device out of two.
func secondMAC(mac string) string {
	return strings.Replace(mac, "02:00:00:aa:", "02:00:00:bb:", 1)
}

// ---- the two controls -----------------------------------------------------------------------------------

// mount builds the router EXACTLY as main.go does: the constructor decides, and when it returns nil no route
// is registered at all. That is the difference between an absent capability and a refusing one, and it can
// only be observed if the test mounts the same way the service does.
func (e *p6E2E) mount(t *testing.T, gateOn bool) *httptest.Server {
	t.Helper()
	env := map[string]string{}
	if gateOn {
		env[iamv2.EnvPhase6Master] = "true"
		env[iamv2.EnvPhase6DeviceGuest] = "true"
	}
	cfg, err := iamv2.LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("phase6 config: %v", err)
	}
	r := chi.NewRouter()
	if p6 := newPhase6Devices(cfg, e.srv, e.p3, e.appliance); p6 != nil {
		r.Post("/v1/phase6/devices/list", p6.listHandler)
		r.Post("/v1/phase6/devices/release", p6.releaseHandler)
	} else if gateOn {
		t.Fatal("the deployment gate was ON and the surface still did not mount")
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// setSetting writes the per-appliance product setting through the real controlled boundary, then reads it
// back — so a combination can never pass merely because the write silently did nothing.
func (e *p6E2E) setSetting(t *testing.T, on bool) {
	t.Helper()
	ctx := context.Background()
	svc := deviceselfservice.New(e.pool)
	if err := svc.SetForAppliance(ctx, e.tenant, e.site, e.appliance, on,
		e.operator, "E2E Operator", "four-combination proof"); err != nil {
		t.Fatalf("set the product setting: %v", err)
	}
	got, err := svc.EnabledForAppliance(ctx, e.tenant, e.site, e.appliance)
	if err != nil {
		t.Fatalf("read the product setting back: %v", err)
	}
	if got != on {
		t.Fatalf("the product setting did not take: wanted %v, stored %v", on, got)
	}
}

type p6Reply struct {
	status int
	body   string
	parsed p6Response
}

func (e *p6E2E) call(t *testing.T, srv *httptest.Server, path string, body map[string]any) p6Reply {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := srv.Client().Post(srv.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	out := p6Reply{status: resp.StatusCode, body: buf.String()}
	_ = json.Unmarshal(buf.Bytes(), &out.parsed)
	return out
}

func (e *p6E2E) listCall(t *testing.T, srv *httptest.Server) p6Reply {
	return e.call(t, srv, "/v1/phase6/devices/list",
		map[string]any{"device": map[string]string{"ip": e.net.guestIP, "mac": e.net.mac}})
}

func (e *p6E2E) releaseCall(t *testing.T, srv *httptest.Server, target string) p6Reply {
	return e.call(t, srv, "/v1/phase6/devices/release",
		map[string]any{"device": map[string]string{"ip": e.net.guestIP, "mac": e.net.mac}, "device_id": target})
}

func (e *p6E2E) bindingStatus(t *testing.T, device string) string {
	t.Helper()
	var st string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT status FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND device_id=$2`,
		e.ent, device).Scan(&st); err != nil {
		t.Fatalf("binding status: %v", err)
	}
	return st
}

// ---- combinations 1 and 2: the deployment gate is OFF ---------------------------------------------------

// With the gate off the routes DO NOT EXIST, and the product setting cannot change that. Combination 2 is
// the one that matters: an operator can switch the property setting on — the screen even lets them — and no
// guest capability appears, because the setting is a hotel decision and the gate is a deployment fact.
func TestIntegration_Phase6_GateOff_NoCapability(t *testing.T) {
	for _, settingOn := range []bool{false, true} {
		name := "setting_off"
		if settingOn {
			name = "setting_on"
		}
		t.Run(name, func(t *testing.T) {
			e := newP6E2E(t)
			e.setSetting(t, settingOn)
			srv := e.mount(t, false)

			for _, path := range []string{"/v1/phase6/devices/list", "/v1/phase6/devices/release"} {
				got := e.call(t, srv, path, map[string]any{
					"device": map[string]string{"ip": e.net.guestIP, "mac": e.net.mac}, "device_id": e.offline})
				if got.status != http.StatusNotFound {
					t.Fatalf("%s answered %d while the deployment gate was OFF; the route must be ABSENT, "+
						"not present-and-refusing: %s", path, got.status, got.body)
				}
			}
			// Nothing moved, whatever the hotel's setting says.
			if st := e.bindingStatus(t, e.offline); st != "AUTHORIZED" {
				t.Fatalf("a device binding became %s while the gate was off", st)
			}
		})
	}
}

// ---- combination 3: deployed, but this hotel has not asked for it ---------------------------------------

func TestIntegration_Phase6_GateOnSettingOff_UniformlyUnavailable(t *testing.T) {
	e := newP6E2E(t)
	e.setSetting(t, false)
	srv := e.mount(t, true)

	list := e.listCall(t, srv)
	if list.status != http.StatusOK || list.parsed.Outcome != p6OutcomeUnavailable {
		t.Fatalf("LIST with the setting off answered %d %s", list.status, list.body)
	}
	if len(list.parsed.Devices) != 0 {
		t.Fatalf("LIST returned devices while the hotel has the capability switched off: %s", list.body)
	}

	rel := e.releaseCall(t, srv, e.offline)
	if rel.status != http.StatusOK || rel.parsed.Outcome != p6OutcomeUnavailable {
		t.Fatalf("RELEASE with the setting off answered %d %s", rel.status, rel.body)
	}
	if st := e.bindingStatus(t, e.offline); st != "AUTHORIZED" {
		t.Fatalf("a device was released while the hotel had the capability switched off (status %s)", st)
	}
	// The refusal is the SAME refusal a guest with no entitlement would get: one answer for every reason.
	unknown := e.call(t, srv, "/v1/phase6/devices/list",
		map[string]any{"device": map[string]string{"ip": e.net.otherIP, "mac": secondMAC(e.net.mac)}})
	if unknown.body != list.body {
		t.Fatalf("a switched-off hotel is distinguishable from an unknown device: %q vs %q",
			list.body, unknown.body)
	}
}

// ---- combination 4: the only one that gives a guest the capability ---------------------------------------

func TestIntegration_Phase6_GateOnSettingOn_TheGuestCapability(t *testing.T) {
	e := newP6E2E(t)
	e.setSetting(t, true)
	srv := e.mount(t, true)
	ctx := context.Background()

	list := e.listCall(t, srv)
	if list.parsed.Outcome != p6OutcomeListed {
		t.Fatalf("LIST answered %s: %s", list.parsed.Outcome, list.body)
	}
	if len(list.parsed.Devices) != 2 {
		t.Fatalf("the guest sees %d devices, expected their own two: %s", len(list.parsed.Devices), list.body)
	}
	byID := map[string]p6Device{}
	for _, d := range list.parsed.Devices {
		byID[d.ID] = d
	}
	if on, ok := byID[e.online]; !ok || !on.Online || on.Removable {
		t.Fatalf("the ONLINE device is missing or presented as removable: %+v", on)
	}
	if off, ok := byID[e.offline]; !ok || off.Online || !off.Removable {
		t.Fatalf("the OFFLINE device is missing or not presented as removable: %+v", off)
	}
	// No MAC and no internal identity on the wire, whatever the database holds.
	low := strings.ToLower(list.body)
	for _, forbidden := range []string{"mac", "02:00:00", "entitlement", "stay", "room", "pms", "tenant", "site"} {
		if strings.Contains(low, forbidden) {
			t.Fatalf("the guest listing exposes %q: %s", forbidden, list.body)
		}
	}

	// AN ONLINE DEVICE IS NEVER REMOVABLE, and asking anyway is the ordinary refusal.
	if rel := e.releaseCall(t, srv, e.online); rel.parsed.Outcome != p6OutcomeUnavailable {
		t.Fatalf("an ONLINE device was released: %s", rel.body)
	}
	if st := e.bindingStatus(t, e.online); st != "AUTHORIZED" {
		t.Fatalf("the online device's binding became %s", st)
	}

	// The offline device is released, exactly once.
	rel := e.releaseCall(t, srv, e.offline)
	if rel.parsed.Outcome != p6OutcomeReleased {
		t.Fatalf("the guest could not release their own offline device: %s", rel.body)
	}
	if st := e.bindingStatus(t, e.offline); st != "DISCONNECTED" {
		t.Fatalf("the released binding is %s", st)
	}
	if again := e.releaseCall(t, srv, e.offline); again.parsed.Outcome != p6OutcomeUnavailable {
		t.Fatalf("releasing the same device twice succeeded twice: %s", again.body)
	}

	// The identity, the accounting and the audit all survive the release: the slot is freed, the history is
	// not erased.
	var devices, authorizations, actions int
	if err := e.pool.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM iam_v2.devices WHERE id=$1),
		 (SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$2 AND device_id=$1),
		 (SELECT count(*) FROM iam_v2.guest_device_actions WHERE entitlement_id=$2 AND device_id=$1 AND outcome='OK')`,
		e.offline, e.ent).Scan(&devices, &authorizations, &actions); err != nil {
		t.Fatalf("post-release state: %v", err)
	}
	if devices != 1 {
		t.Fatal("the released device's identity was deleted")
	}
	if authorizations == 0 {
		t.Fatal("the released device's authorization history was erased")
	}
	if actions != 1 {
		t.Fatalf("the release produced %d audit rows, expected exactly 1", actions)
	}

	// ...and the guest's own listing now shows only what they still hold.
	after := e.listCall(t, srv)
	if len(after.parsed.Devices) != 1 || after.parsed.Devices[0].ID != e.online {
		t.Fatalf("the listing after the release is %s", after.body)
	}
}

// ---- local-first: the whole capability works with Central unreachable -----------------------------------

// THE POINT OF THE PER-APPLIANCE SETTING IS THAT IT IS THE APPLIANCE'S. A hotel whose uplink is down must
// still offer, refuse and record device self-service exactly as before — the guest network does not depend
// on the internet.
//
// Every Central-facing endpoint is pointed at a port that is closed, so any code that reached for one would
// fail or hang rather than quietly succeed against a real service. The flow is then run in full, under a
// deadline: a path that waited on Central would blow the deadline even if it eventually recovered.
func TestIntegration_Phase6_WorksWithCentralUnreachable(t *testing.T) {
	// A listener that is opened and immediately closed gives an address nothing is serving — better than an
	// invented one, because the port is real and the refusal is immediate and local.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	t.Setenv("SCD_CTRLAPI_BASE", deadURL)
	t.Setenv("SCD_NATS_URL", strings.Replace(deadURL, "http://", "nats://", 1))
	t.Setenv("SCD_NATS_MTLS_URL", strings.Replace(deadURL, "http://", "tls://", 1))

	e := newP6E2E(t)
	srv := e.mount(t, true)

	done := make(chan string, 1)
	go func() {
		// The complete flow: the operator switches the property setting on, the guest lists, the guest
		// releases, the guest lists again.
		e.setSetting(t, true)
		if got := e.listCall(t, srv); got.parsed.Outcome != p6OutcomeListed {
			done <- "LIST failed with Central unreachable: " + got.body
			return
		}
		if got := e.releaseCall(t, srv, e.offline); got.parsed.Outcome != p6OutcomeReleased {
			done <- "RELEASE failed with Central unreachable: " + got.body
			return
		}
		if got := e.listCall(t, srv); len(got.parsed.Devices) != 1 {
			done <- "the listing did not settle: " + got.body
			return
		}
		// And the operator can switch it back off, offline, with the change durably recorded.
		e.setSetting(t, false)
		if got := e.listCall(t, srv); got.parsed.Outcome != p6OutcomeUnavailable {
			done <- "switching the setting off did not take effect: " + got.body
			return
		}
		done <- ""
	}()

	select {
	case msg := <-done:
		if msg != "" {
			t.Fatal(msg)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the device self-service flow did not complete with Central unreachable; " +
			"something in this path is waiting on the Control Plane")
	}

	// The durable record of both setting changes is on the APPLIANCE, which is where an operator will look
	// for it when the uplink comes back.
	var changes int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.appliance_product_setting_changes
		  WHERE appliance_id=$1 AND setting_key='guest_device_self_service'`, e.appliance).Scan(&changes); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if changes < 2 {
		t.Fatalf("only %d setting change(s) were recorded locally", changes)
	}
}

// A property that never had the setting written at all is OFF: the guest capability is opt-in, and a missing
// row is a decision the hotel has not made rather than a default in the guest's favour.
func TestIntegration_Phase6_UnwrittenSettingIsOff(t *testing.T) {
	e := newP6E2E(t)
	srv := e.mount(t, true)

	var rows int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.appliance_product_settings WHERE appliance_id=$1`, e.appliance).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("the fixture already has a setting row (%d); this test proves nothing", rows)
	}
	if got := e.listCall(t, srv); got.parsed.Outcome != p6OutcomeUnavailable {
		t.Fatalf("an appliance with no setting row served the capability: %s", got.body)
	}
	if got := e.releaseCall(t, srv, e.offline); got.parsed.Outcome != p6OutcomeUnavailable {
		t.Fatalf("an appliance with no setting row released a device: %s", got.body)
	}
	if st := e.bindingStatus(t, e.offline); st != "AUTHORIZED" {
		t.Fatalf("the binding became %s with no setting row", st)
	}
}
