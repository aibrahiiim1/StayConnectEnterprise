//go:build integration && phase6

package main

// The four gate/setting combinations over real HTTP, plus the local-first guarantee. The deployment gate
// (read at startup) decides whether routes are mounted (off is a 404); the product setting (read from the
// local database per request) decides whether the hotel offers the feature. Only the fourth combination
// gives a guest the capability, and the others fail differently (absent vs uniformly unavailable). All four
// run against one subject created by the real grant path.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/deviceselfservice"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

type p6E2E struct {
	*authFixture
	ent      string
	online   string // holds a live session
	offline  string // authorized through the admission primitive, no session
	operator string
}

func newP6E2E(t *testing.T) *p6E2E {
	t.Helper()
	f := newAuthFixture(t)
	t.Cleanup(f.startEnforcementOwner(t))
	ctx := context.Background()

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

	// The setting's composite foreign key makes "an appliance of this tenant and site" a database fact.
	// The serial is built in Go: one placeholder used as both uuid and text makes PostgreSQL deduce two types.
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

	// A second, offline device on the same entitlement: the one a guest may remove.
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

// secondMAC derives a distinct MAC: device identity is (tenant, site, appliance, MAC), so a collision merges two devices.
func secondMAC(mac string) string {
	return strings.Replace(mac, "02:00:00:aa:", "02:00:00:bb:", 1)
}

// mount builds the router exactly as main.go does: a nil constructor result registers no route at all.
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

// setSetting writes the setting through the real controlled boundary and reads it back, so a no-op write cannot pass.
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

// Combinations 1 and 2, gate off: the routes do not exist, whatever the property setting says.
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
			if st := e.bindingStatus(t, e.offline); st != "AUTHORIZED" {
				t.Fatalf("a device binding became %s while the gate was off", st)
			}
		})
	}
}

// Combination 3: deployed, but the hotel has not asked for it.

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
	// the same refusal a guest with no entitlement gets
	unknown := e.call(t, srv, "/v1/phase6/devices/list",
		map[string]any{"device": map[string]string{"ip": e.net.otherIP, "mac": secondMAC(e.net.mac)}})
	if unknown.body != list.body {
		t.Fatalf("a switched-off hotel is distinguishable from an unknown device: %q vs %q",
			list.body, unknown.body)
	}
}

// Combination 4: the only one that gives a guest the capability.

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
	// no MAC or internal identity on the wire
	low := strings.ToLower(list.body)
	for _, forbidden := range []string{"mac", "02:00:00", "entitlement", "stay", "room", "pms", "tenant", "site"} {
		if strings.Contains(low, forbidden) {
			t.Fatalf("the guest listing exposes %q: %s", forbidden, list.body)
		}
	}

	// an online device is never removable
	if rel := e.releaseCall(t, srv, e.online); rel.parsed.Outcome != p6OutcomeUnavailable {
		t.Fatalf("an ONLINE device was released: %s", rel.body)
	}
	if st := e.bindingStatus(t, e.online); st != "AUTHORIZED" {
		t.Fatalf("the online device's binding became %s", st)
	}

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

	// Identity, accounting and audit survive the release; only the slot is freed.
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

	after := e.listCall(t, srv)
	if len(after.parsed.Devices) != 1 || after.parsed.Devices[0].ID != e.online {
		t.Fatalf("the listing after the release is %s", after.body)
	}
}

// Local-first: with every Central-facing endpoint pointed at a closed port, the full flow must still work
// under a deadline (a path waiting on Central would blow it).
func TestIntegration_Phase6_WorksWithCentralUnreachable(t *testing.T) {
	// A closed listener gives a real address nothing serves, so refusal is immediate and local.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	t.Setenv("SCD_CTRLAPI_BASE", deadURL)
	t.Setenv("SCD_MTLS_BASE", strings.Replace(deadURL, "http://", "https://", 1))

	e := newP6E2E(t)
	srv := e.mount(t, true)

	done := make(chan string, 1)
	go func() {
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

// A missing setting row means OFF: the capability is opt-in.
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
