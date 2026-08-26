//go:build integration

package main

// THE WHOLE ENFORCEMENT CYCLE, END TO END, AGAINST A REAL DATABASE AND A REAL SOCKET.
//
// Every piece of this cycle was already tested. The cycle was not, and that is what failed live:
//
//	the grant committed a Session as PENDING_ENFORCEMENT   — proven, by the grant tests
//	the producer derived a plan from durable state         — proven, by the enforce tests
//	the applier put a plan in force and promoted a Session — proven, by the shaping tests
//	...and on the appliance, nothing joined them up, because the producer and the writer were both gated on
//	an unrelated feature flag. Two real guests were granted access that no kernel ever heard about, and every
//	green test in the repository stayed green.
//
// So these tests own the JOIN. They run the real producer (internal/shapeproducer, the same call acctd makes)
// against durable state in a real PostgreSQL, submit over a REAL unix socket so the producer is authenticated
// by SO_PEERCRED exactly as it is in production, drive the real applier, and then read the Session back from
// the database to see whether it says `active`.
//
// The kernel halves are the existing fakes, deliberately: what is proven here is the CONVERGENCE — that a
// pending session becomes an active one through the real contract, that it survives a restart, and that an
// unauthenticated producer cannot do it. That real nft admission and real tc metering behave as the applier
// believes is proven against a live kernel in internal/kerneltest, which is the only place it can be.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeproducer"
)

type cycleFixture struct {
	pool                         *pgxpool.Pool
	tenant, site, appliance      string
	iface, stay, device, svcRev  string
	pkgRev, entitlement, session string
	bridge, ip, mac              string
}

func cyclePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// SO_PEERCRED is a Linux socket option and this cycle is authenticated by it, so the suite can only mean
	// anything on the platform the appliance runs. On anything else the peer identity is unknowable (see
	// phase3_peer_other.go) and a pass would prove the opposite of what it claims.
	if runtime.GOOS != "linux" {
		t.Skip("the enforcement cycle is authenticated by SO_PEERCRED; it runs on Linux, as the appliance does")
	}
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping the enforcement-cycle integration")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// cycleOctet keeps concurrent fixtures on separate addresses. Two fixtures sharing one IP would exercise the
// address-supersession rule instead of the cycle, which is a different test (internal/enforce).
var cycleOctet = 40

// seedPendingSession builds exactly what a successful Room Login leaves behind: an ACTIVE entitlement, an
// authorized device, and a Session in PENDING_ENFORCEMENT that no kernel has ever heard of.
func seedPendingSession(t *testing.T, p *pgxpool.Pool) cycleFixture {
	t.Helper()
	ctx := context.Background()
	cycleOctet++
	f := cycleFixture{pool: p, mac: fmt.Sprintf("02:00:00:00:%02x:01", cycleOctet)}
	f.bridge = fmt.Sprintf("br-cyc-%d", cycleOctet)
	f.ip = fmt.Sprintf("10.%d.0.7", cycleOctet)
	subnet := fmt.Sprintf("10.%d.0.0/24", cycleOctet)
	gateway := fmt.Sprintf("10.%d.0.1", cycleOctet)

	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks
	           (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'guests',$3,$4,($5::text)::inet,($6::text)::inet,($7::text)::cidr,true
	           FROM si RETURNING id,tenant_id,site_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), gn.tenant_id, gn.site_id,'protel-fias','ACTIVE' FROM gn RETURNING id,tenant_id,site_id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	                                  external_stay_identity,status,lifecycle_version,last_applied_event_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'RES-'||$1,'STAY-'||$1,'IN_HOUSE',1,0
	           FROM pi RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(), ($2::text)::macaddr FROM pi RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'plan-'||$1,true FROM pi RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,
	                                                    up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,9000,4000,4,'REJECT_NEW_DEVICE','VALIDITY_WINDOW'
	            FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id,'pkg-'||$1,false FROM pi RETURNING id,tenant_id,site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	                                                        service_plan_revision_id,package_type,price_minor,
	                                                        settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id,1,spr.id,'FREE_STAY',0,
	                 ARRAY['NOT_REQUIRED']::text[],'{}'::jsonb FROM ip, spr RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM st)::text, (SELECT id FROM dv)::text, (SELECT id FROM spr)::text, (SELECT id FROM ipr)::text`,
		fmt.Sprintf("%d", cycleOctet), f.mac, "ens-cyc-"+fmt.Sprintf("%d", cycleOctet), f.bridge,
		gateway+"/24", gateway, subnet).
		Scan(&f.tenant, &f.site, &f.iface, &f.stay, &f.device, &f.svcRev, &f.pkgRev); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f.appliance = mustText(t, p, `SELECT gen_random_uuid()::text`)

	// The entitlement and its history, through the same controlled writer the grant path uses.
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var purchase string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, f.pkgRev, f.iface, f.stay).Scan(&purchase); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,
		 package_revision_id,time_accounting_mode,end_mode,status)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE') RETURNING id::text`,
		f.tenant, f.site, f.stay, f.iface, purchase, f.svcRev, f.pkgRev).Scan(&f.entitlement); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),'GRANT')`,
		f.entitlement); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`,
		f.entitlement, f.device); err != nil {
		t.Fatalf("authorize device: %v", err)
	}
	// PENDING_ENFORCEMENT, with the addressing the applier will re-derive the accounting source from.
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,credential_method,state,started,ip,mac,ingress_interface)
		VALUES ($1,$2,$3,$4,'PMS','PENDING_ENFORCEMENT',now(),($5::text)::inet,($6::text)::macaddr,$7)
		RETURNING id::text`,
		f.tenant, f.site, f.entitlement, f.device, f.ip, f.mac, f.bridge).Scan(&f.session); err != nil {
		t.Fatalf("seed the pending session: %v", err)
	}
	return f
}

func mustText(t *testing.T, p *pgxpool.Pool, q string) string {
	t.Helper()
	var s string
	if err := p.QueryRow(context.Background(), q).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// forwarding asks the applier's own kernel surface whether this session's class is actually carrying traffic,
// rather than inspecting the fake's internals: a class that exists but forwards nothing is precisely the state
// the staged provisioning creates on purpose, and it is not enforcement.
func (f cycleFixture) forwarding(t *testing.T, tc *fakeTC) bool {
	t.Helper()
	on, err := tc.SessionForwarding(context.Background(), f.bridge, net.ParseIP(f.ip))
	if err != nil {
		t.Fatalf("read the forwarding state: %v", err)
	}
	return on
}

func (f cycleFixture) sessionState(t *testing.T) string {
	t.Helper()
	var state string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT state FROM iam_v2.sessions WHERE id=$1::uuid`, f.session).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// applier builds the REAL netd shaping writer for this fixture's scope, with the durable halves bound to the
// real database and the kernel halves faked. stateDir is separate per instance so a "restart" can be modelled
// by building a second one with an empty directory — a reboot loses the class inventory, and must not lose the
// ability to converge.
func (f cycleFixture) applier(t *testing.T, stateDir string, tc *fakeTC, g *fakeGate, allowedUID int) *phase3Shaping {
	t.Helper()
	return &phase3Shaping{
		shp:         tc,
		gate:        g,
		mode:        phase3Mode{Active: true, TenantID: f.tenant, SiteID: f.site, ApplianceID: f.appliance, AssignGen: 1},
		authz:       shapingAuthz{allowedUID: allowedUID, configured: true},
		store:       &planStore{path: filepath.Join(stateDir, "plan.json")},
		classStore:  &classStore{path: filepath.Join(stateDir, "classes.json")},
		journal:     &activationJournal{path: filepath.Join(stateDir, "journal.json")},
		origins:     &pgOrigins{pool: f.pool},
		generations: &pgGenerations{pool: f.pool},
		enforcement: &pgEnforcement{pool: f.pool, tenant: f.tenant, site: f.site},
		secClock:    newSecurityClock(),
	}
}

// serveOverUnixSocket runs the real handler on a real unix socket, through the real peer listener. The
// producer's identity therefore comes from the kernel (SO_PEERCRED), not from anything the request says.
func serveOverUnixSocket(t *testing.T, p3 *phase3Shaping) *http.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "netd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &server{phase3: p3}
	r := chi.NewRouter()
	r.Post("/v1/phase3/shaping", srv.phase3ShapingHandler)
	hs := &http.Server{
		Handler: r,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if pc, ok := c.(*peerConn); ok && pc.err == nil {
				return context.WithValue(ctx, peerConnKey{}, pc.id)
			}
			return ctx
		},
	}
	go func() { _ = hs.Serve(&peerListener{Listener: ln, authz: p3.authz}) }()
	t.Cleanup(func() { _ = hs.Close() })
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}, Timeout: 10 * time.Second}
}

// producerPass is one complete acctd tick: derive the plan from durable state, build the envelope with the
// SAME producer code acctd runs, and submit it over the socket.
func (f cycleFixture) producerPass(t *testing.T, c *http.Client, gen int64) (shapingPlanResponse, int) {
	t.Helper()
	ctx := context.Background()
	plan, err := enforce.New(f.pool).PlanForSite(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("derive the plan: %v", err)
	}
	env := shapeproducer.BuildEnvelope(plan, shapeproducer.Scope{
		TenantID: f.tenant, SiteID: f.site, ApplianceID: f.appliance, AssignmentGen: 1,
	}, gen, 1, []string{f.bridge}, f.bridge, time.Now(), 90*time.Second)

	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Post("http://netd/v1/phase3/shaping", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var res shapingPlanResponse
	_ = json.NewDecoder(resp.Body).Decode(&res)
	return res, resp.StatusCode
}

// THE REGRESSION. A Session that the grant left PENDING_ENFORCEMENT becomes `active` because the producer and
// the applier actually met — and it says active only once BOTH kernel halves are in force.
func TestIntegration_EnforcementCycle_PendingSessionBecomesActive(t *testing.T) {
	p := cyclePool(t)
	f := seedPendingSession(t, p)
	if got := f.sessionState(t); got != "PENDING_ENFORCEMENT" {
		t.Fatalf("fixture session state = %q", got)
	}

	tc, g := newFakeTC(), newFakeGate()
	client := serveOverUnixSocket(t, f.applier(t, t.TempDir(), tc, g, os.Getuid()))

	res, code := f.producerPass(t, client, 1)
	if code != http.StatusOK || !res.Accepted {
		t.Fatalf("the applier refused the producer's plan: code=%d res=%+v", code, res)
	}
	if res.Shaped != 1 || res.Failed != 0 {
		t.Fatalf("shaped=%d failed=%d problems=%v — one pending session must be enforced by one pass",
			res.Shaped, res.Failed, res.Problems)
	}
	if got := f.sessionState(t); got != "active" {
		t.Fatalf("session state = %q after a healthy pass, want active. This is the live failure: the rows "+
			"exist, the guest was told it worked, and nothing ever promoted them", got)
	}
	// BOTH halves, not one. A session metered but not authorized (or the reverse) is not enforcement.
	if !g.isAuthorized(f.bridge, f.ip) {
		t.Fatal("the session is active but holds no nft authorization — it would have no internet")
	}
	if !f.forwarding(t, tc) {
		t.Fatal("the session is active but has no accountable tc class — its traffic would be unmetered")
	}
}

// RESTART AND RECOVERY. The first attempt fails in the kernel and the process then dies, which is the state a
// crash or a deployment leaves behind: a pending session, no class, and no in-memory anything. A fresh
// instance with an EMPTY state directory must still converge it — recovery cannot depend on what the previous
// process remembered.
func TestIntegration_EnforcementCycle_RecoversAnAlreadyPendingSessionAfterRestart(t *testing.T) {
	p := cyclePool(t)
	f := seedPendingSession(t, p)

	failing := newFakeTC()
	failing.failPrepare[f.ip] = fmt.Errorf("kernel busy")
	first := serveOverUnixSocket(t, f.applier(t, t.TempDir(), failing, newFakeGate(), os.Getuid()))
	res, _ := f.producerPass(t, first, 1)
	if res.Shaped != 0 || res.Failed == 0 {
		t.Fatalf("the failing pass reported success: %+v", res)
	}
	if got := f.sessionState(t); got != "PENDING_ENFORCEMENT" {
		t.Fatalf("a session whose class could not be created claims %q", got)
	}

	// A NEW process: new state directory, empty kernel, no memory of the attempt.
	tc, g := newFakeTC(), newFakeGate()
	second := serveOverUnixSocket(t, f.applier(t, t.TempDir(), tc, g, os.Getuid()))
	res2, code := f.producerPass(t, second, 2)
	if code != http.StatusOK || res2.Shaped != 1 || res2.Failed != 0 {
		t.Fatalf("a restarted applier did not converge the pending session: code=%d res=%+v", code, res2)
	}
	if got := f.sessionState(t); got != "active" {
		t.Fatalf("session state = %q after recovery, want active", got)
	}
	if !g.isAuthorized(f.bridge, f.ip) || !f.forwarding(t, tc) {
		t.Fatal("the recovered session is active without both enforcement halves in force")
	}
}

// PRODUCER AUTHENTICATION IS REAL, and it is the kernel's answer rather than the request's.
//
// The submission below is byte-for-byte the one that succeeds above. The only difference is which uid the
// applier will accept, and SO_PEERCRED tells it the truth about who is calling — so the plan is refused and
// nothing at all is enforced.
func TestIntegration_EnforcementCycle_AnUnauthenticatedProducerEnforcesNothing(t *testing.T) {
	p := cyclePool(t)
	f := seedPendingSession(t, p)

	tc, g := newFakeTC(), newFakeGate()
	client := serveOverUnixSocket(t, f.applier(t, t.TempDir(), tc, g, os.Getuid()+1))

	res, code := f.producerPass(t, client, 1)
	if code != http.StatusForbidden {
		t.Fatalf("a submission from an unauthorized uid was answered %d (%+v), want 403", code, res)
	}
	if got := f.sessionState(t); got != "PENDING_ENFORCEMENT" {
		t.Fatalf("session state = %q after a refused submission — an unauthenticated producer moved "+
			"durable state", got)
	}
	if g.isAuthorized(f.bridge, f.ip) {
		t.Fatal("an unauthenticated producer got a guest authorized in the packet gate")
	}
	if f.forwarding(t, tc) {
		t.Fatal("an unauthenticated producer got an accountable class installed")
	}
}

// THE HEALTHY CYCLE HAS TO FIT INSIDE THE GUEST'S RESPONSE BUDGET.
//
// portald abandons a connect attempt at 2500ms and hands scd 2300ms of that, so a healthy convergence must
// complete well inside it — otherwise a guest whose grant is perfectly good is told it failed, which is
// exactly what the appliance did for two real guests. This measures the part that is ours: everything from
// "durable state says this session is pending" to "durable state says it is active", including the real
// socket, the real authentication and the real database writes.
func TestIntegration_EnforcementCycle_ConvergesWellInsideTheGuestBudget(t *testing.T) {
	p := cyclePool(t)
	f := seedPendingSession(t, p)
	client := serveOverUnixSocket(t, f.applier(t, t.TempDir(), newFakeTC(), newFakeGate(), os.Getuid()))

	start := time.Now()
	res, code := f.producerPass(t, client, 1)
	elapsed := time.Since(start)
	if code != http.StatusOK || res.Shaped != 1 {
		t.Fatalf("the pass did not converge: code=%d res=%+v", code, res)
	}
	if got := f.sessionState(t); got != "active" {
		t.Fatalf("session state = %q", got)
	}
	// The budget also has to absorb one full producer interval (the tick is 1s) and scd's 100ms polling
	// granularity, so the pass itself has roughly a second of the 2300ms to spend. Half of that is the alarm
	// threshold: anything slower means the arithmetic in portald's budget no longer holds.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("a healthy convergence took %s. portald abandons the guest at 2500ms and the budget assumes "+
			"one producer interval plus this pass fits inside it — re-derive the budget before shipping this",
			elapsed)
	}
	t.Logf("healthy convergence: %s (producer derive + submit + apply + promote)", elapsed)
}
