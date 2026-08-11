//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Real HTTP + real PostgreSQL 16 contract tests for the Phase-3 Hotel-Admin mutations. These exist because a
// mocked browser test cannot catch an API that disagrees with its own schema: the handler is exercised through
// the actual chi router, with the actual session/RBAC middleware, against a disposable database that enforces
// every constraint and controlled operation. Nothing here touches an appliance, a production database or a PMS.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping edged API+PG integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return p
}

type apiFixture struct {
	srv      *httptest.Server
	pool     *pgxpool.Pool
	tenant   string
	site     string
	operator string
	password string
	sessTok  string
}

// newAPI builds the REAL Phase-3 routes with the real auth middleware, backed by a disposable database and a
// site/tenant seeded for this test.
func newAPI(t *testing.T, roles ...string) *apiFixture {
	t.Helper()
	p := testPool(t)
	ctx := context.Background()
	f := &apiFixture{pool: p, password: "operator-step-up-pw"}

	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id)
	SELECT (SELECT tenant_id FROM si)::text, (SELECT id FROM si)::text`).Scan(&f.tenant, &f.site); err != nil {
		t.Fatalf("seed tenant/site: %v", err)
	}
	// The disposable fixture builds the iam_v2 schema only. The appliance's own operator identity tables come
	// from migration 0001; they are provisioned here (same shape) because the controlled operations validate a
	// REAL actor against them — which is exactly the contract these tests exist to prove.
	if _, err := p.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.operators (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id uuid,
			email text NOT NULL,
			display_name text,
			password_hash text,
			status text NOT NULL DEFAULT 'active',
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			auth_method text NOT NULL DEFAULT 'local')`); err != nil {
		t.Fatalf("provision operators: %v", err)
	}
	// the appliance's local audit log (0001) — the handlers append to it, and a test that silently loses those
	// writes would hide a broken audit path.
	if _, err := p.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.audit_log (
			id bigserial PRIMARY KEY,
			tenant_id uuid, actor_type text, actor_id text, action text NOT NULL,
			target_type text, target_id text, ip inet, user_agent text, payload jsonb,
			created_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("provision audit_log: %v", err)
	}
	if _, err := p.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.operator_roles (
			operator_id uuid NOT NULL REFERENCES public.operators(id) ON DELETE CASCADE,
			role text NOT NULL,
			PRIMARY KEY (operator_id, role))`); err != nil {
		t.Fatalf("provision operator_roles: %v", err)
	}

	hash, err := hashPassword(f.password)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO operators(tenant_id,email,display_name,password_hash,status)
		VALUES ($1,$2,'Test Operator',$3,'active') RETURNING id::text`,
		f.tenant, fmt.Sprintf("op-%d@test.local", time.Now().UnixNano()), hash).Scan(&f.operator); err != nil {
		t.Fatalf("seed operator: %v", err)
	}
	if len(roles) == 0 {
		roles = []string{"site_admin"}
	}
	for _, role := range roles {
		if _, err := p.Exec(ctx, `INSERT INTO operator_roles(operator_id, role) VALUES ($1,$2)
			ON CONFLICT DO NOTHING`, f.operator, role); err != nil {
			t.Fatalf("seed role: %v", err)
		}
	}

	s := &server{db: p, sessions: newSessionStore(2 * time.Hour), tenantID: f.tenant, siteID: f.site}
	// the Phase-3 admin surface is mounted explicitly below; this fixture exercises the routes themselves.
	f.sessTok = s.sessions.create(&session{OperatorID: f.operator, Email: "op@test.local", Roles: roles})

	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			mountResource(r, s, "checkout-grace", s.checkoutGraceConfigRoutes)
			mountResource(r, s, "operational-alerts", s.operationalAlertsRoutes)
			mountResource(r, s, "pms-stays", s.pmsStaysRoutes)
			mountResource(r, s, "pms-events", s.pmsEventsRoutes)
			mountResource(r, s, "pms-resolutions", s.pmsResolutionsRoutes)
			mountResource(r, s, "pms-interfaces", s.pmsInterfacesRoutes)
			mountResource(r, s, "pms-routing", s.pmsRoutingRoutes)
			mountResource(r, s, "pms-source-conflicts", s.pmsSourceConflictsRoutes)
		})
	})
	f.srv = httptest.NewServer(r)
	t.Cleanup(func() { f.srv.Close(); p.Close() })
	return f
}

func (f *apiFixture) do(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, f.srv.URL+"/edge/v1"+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.sessTok})
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// seedAlert creates a real alert-bearing checkout audit row. Its OPEN lifecycle action is created by the
// database itself, which is precisely the property under test.
func (f *apiFixture) seedAlert(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	res := fmt.Sprintf("R%d", time.Now().UnixNano())

	var iface, stay string
	if err := f.pool.QueryRow(ctx, `WITH
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
	           status,lifecycle_version,last_applied_event_version,effective_checkout_at,posting_allowed)
	         SELECT gen_random_uuid(),$1,$2,pi.id,$3,$3,'CHECKED_OUT',1,0, now() - interval '1 hour', false FROM pi RETURNING id)
	SELECT (SELECT id FROM pi)::text, (SELECT id FROM st)::text`, f.tenant, f.site, res).Scan(&iface, &stay); err != nil {
		t.Fatalf("seed stay: %v", err)
	}
	// events are admitted PENDING; only the engine moves one to a terminal state, so the fixture applies it in
	// a separate statement exactly as the engine would (a data-modifying CTE cannot see a sibling's insert).
	var event string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,pms_timestamp_utc,
		 admission_kind,admission_runtime_generation,resync_generation,received_at)
		VALUES ($1,$2,$3,$4,'GO','{}'::jsonb, now() - interval '1 hour','LIVE',1,0,now()) RETURNING id::text`,
		f.tenant, f.site, iface, res).Scan(&event); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.stay_events
		SET processing_status='APPLIED', processed_at=now(), stay_id=$2 WHERE id=$1`, event, stay); err != nil {
		t.Fatalf("apply event: %v", err)
	}
	// An emergency alert always accompanies a real emergency Grace entitlement (the audit's coherence check
	// enforces exactly that), so the fixture provisions the canonical Emergency catalog and grants one — the
	// same shape the converter produces.
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.bootstrap_emergency_grace($1,$2)`, f.tenant, f.site); err != nil {
		t.Fatalf("bootstrap emergency catalog: %v", err)
	}
	var pkgRev, svcRev string
	if err := f.pool.QueryRow(ctx, `SELECT ip.current_revision_id::text, ipr.service_plan_revision_id::text
		FROM iam_v2.internet_packages ip
		JOIN iam_v2.internet_package_revisions ipr ON ipr.id = ip.current_revision_id
		WHERE ip.tenant_id=$1 AND ip.site_id=$2 AND ip.code='__sys_emergency_grace_pkg__'`,
		f.tenant, f.site).Scan(&pkgRev, &svcRev); err != nil {
		t.Fatalf("emergency catalog: %v", err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var purchase, graceEnt string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state,checkout_episode)
		VALUES ($1,$2,$3,$4,$5,'EMERGENCY_GRACE',0,'GRANTED',1) RETURNING id::text`,
		f.tenant, f.site, pkgRev, iface, stay).Scan(&purchase); err != nil {
		t.Fatalf("seed grace purchase: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,
		 package_revision_id,time_accounting_mode,end_mode,status,window_ends_at)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','GRACE_AFTER_CHECKOUT','ACTIVE', now() + interval '1 hour')
		RETURNING id::text`, f.tenant, f.site, stay, iface, purchase, svcRev, pkgRev).Scan(&graceEnt); err != nil {
		t.Fatalf("seed grace entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now() - interval '1 hour','GRACE_CONVERSION')`, graceEnt); err != nil {
		t.Fatalf("seed grace history: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var auditID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.checkout_grace_audit
		(tenant_id,site_id,pms_interface_id,stay_id,lifecycle_version,trigger,is_emergency,policy_version,
		 alert_code,reason_code,boundary_at,boundary_clock_suspect,grace_entitlement_id,
		 boundary_event_id,boundary_event_seq,boundary_normalization_version,boundary_reason_code,config_version)
		SELECT $1,$2,$3,$4,1,'EMERGENCY_GRACE',true,'EMERGENCY_GRACE_V1','CHECKOUT_GRACE_CONFIG_INVALID','POLICY_MISMATCH',
		       e.pms_timestamp_utc, false, $6::uuid, e.id, e.sequence_version, e.normalization_version,
		       'TRUSTED_PMS_CHECKOUT_TS', 1
		FROM iam_v2.stay_events e WHERE e.id=$5
		RETURNING id::text`, f.tenant, f.site, iface, stay, event, graceEnt).Scan(&auditID); err != nil {
		t.Fatalf("seed alert: %v", err)
	}
	return auditID
}

// ---------------------------------------------------------------- alerts

// The lifecycle an operator actually drives, end to end, through HTTP into PostgreSQL.
func TestIntegration_API_AlertLifecycleOpenAckResolve(t *testing.T) {
	f := newAPI(t)
	audit := f.seedAlert(t)

	// the alert appears in the queue as OPEN — created by the database with the audit, not by the API
	status, body := f.do(t, "GET", "/operational-alerts", nil)
	if status != 200 {
		t.Fatalf("list: %d %v", status, body)
	}
	rows := body["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("queue has %d alerts, want 1", len(rows))
	}
	first := rows[0].(map[string]any)
	if first["state"] != "OPEN" || first["seq"].(float64) != 1 {
		t.Fatalf("alert state=%v seq=%v, want OPEN/1", first["state"], first["seq"])
	}

	// acknowledge, then resolve
	status, body = f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge",
		map[string]any{"expected_state": "OPEN", "reason_code": "REVIEWED"})
	if status != 200 || body["seq"].(float64) != 2 {
		t.Fatalf("acknowledge: %d %v", status, body)
	}
	status, body = f.do(t, "POST", "/operational-alerts/"+audit+"/resolve",
		map[string]any{"expected_state": "ACKNOWLEDGED", "reason_code": "FIXED"})
	if status != 200 || body["seq"].(float64) != 3 {
		t.Fatalf("resolve: %d %v", status, body)
	}

	// a resolved alert leaves the queue, and the history is complete and attributed
	status, body = f.do(t, "GET", "/operational-alerts", nil)
	if len(body["data"].([]any)) != 0 {
		t.Fatal("a RESOLVED alert is still in the operator queue")
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.checkout_grace_alert_actions
		WHERE audit_id=$1 AND actor IS NOT NULL`, audit).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 { // OPEN has no actor; the two operator actions do
		t.Fatalf("attributed actions = %d, want 2", n)
	}
}

func TestIntegration_API_AlertRefusals(t *testing.T) {
	f := newAPI(t)
	audit := f.seedAlert(t)

	// an action with no expected state is refused: acting on an unknown world is not allowed
	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge", map[string]any{}); status != 400 {
		t.Fatalf("missing expected_state got %d, want 400", status)
	}
	// a stale expected state conflicts
	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge",
		map[string]any{"expected_state": "ACKNOWLEDGED", "reason_code": "REVIEWED"}); status != 409 {
		t.Fatalf("stale expected_state got %d, want 409", status)
	}
	// an unknown alert is a 404
	if status, _ := f.do(t, "POST", "/operational-alerts/00000000-0000-0000-0000-000000000000/acknowledge",
		map[string]any{"expected_state": "OPEN", "reason_code": "REVIEWED"}); status != 404 {
		t.Fatalf("unknown alert got %d, want 404", status)
	}
	// an alert from ANOTHER site is invisible and unactionable
	other := newAPI(t)
	foreign := other.seedAlert(t)
	if status, _ := f.do(t, "POST", "/operational-alerts/"+foreign+"/acknowledge",
		map[string]any{"expected_state": "OPEN", "reason_code": "REVIEWED"}); status != 404 {
		t.Fatalf("cross-site alert got %d, want 404", status)
	}
	// acknowledge, then acknowledge again → conflict; and any action after RESOLVED → conflict
	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge",
		map[string]any{"expected_state": "OPEN", "reason_code": "REVIEWED"}); status != 200 {
		t.Fatal("first acknowledge should succeed")
	}
	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge",
		map[string]any{"expected_state": "ACKNOWLEDGED", "reason_code": "REVIEWED"}); status != 409 {
		t.Fatal("repeat acknowledge must conflict")
	}
	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/resolve",
		map[string]any{"expected_state": "ACKNOWLEDGED", "reason_code": "REVIEWED"}); status != 200 {
		t.Fatal("resolve should succeed")
	}
	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/resolve",
		map[string]any{"expected_state": "ACKNOWLEDGED", "reason_code": "REVIEWED"}); status != 409 {
		t.Fatal("action after RESOLVED must conflict")
	}
}

// Two operators clicking Acknowledge at the same instant: exactly one wins, the other is told why.
func TestIntegration_API_ConcurrentAcknowledgeHasOneWinner(t *testing.T) {
	f := newAPI(t)
	audit := f.seedAlert(t)
	const n = 12
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge",
				map[string]any{"expected_state": "OPEN", "reason_code": "REVIEWED"})
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, c := range codes {
		switch c {
		case 200:
			wins++
		case 409:
		default:
			t.Fatalf("unexpected status %d — a race must be a clean win or a clean conflict", c)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent acknowledgements succeeded, want exactly 1", wins)
	}
	var actions int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.checkout_grace_alert_actions WHERE audit_id=$1`, audit).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if actions != 2 {
		t.Fatalf("lifecycle rows = %d, want 2 (OPEN + one ACKNOWLEDGED)", actions)
	}
}

// ---------------------------------------------------------------- checkout-grace publication

func gracePolicy(expected int, pkgRev any) map[string]any {
	return map[string]any{
		"grace_package_revision_id":  pkgRev,
		"grace_duration_seconds":     3600,
		"grace_down_kbps":            4000,
		"grace_up_kbps":              1500,
		"grace_data_quota_bytes":     524288000,
		"grace_device_limit":         2,
		"grace_device_limit_policy":  "REJECT_NEW_DEVICE",
		"eligibility_window_seconds": 86400,
		"config_version":             0,
		"expected_config_version":    expected,
		"password":                   "operator-step-up-pw",
		"reason_code":                "INITIAL_POLICY",
	}
}

// A read-only role can see the policy and the queue but can change neither.
func TestIntegration_API_ReadOnlyRoleCannotMutate(t *testing.T) {
	f := newAPI(t, "site_viewer")
	audit := f.seedAlert(t)
	if status, _ := f.do(t, "GET", "/checkout-grace", nil); status != 200 {
		t.Fatal("a viewer must be able to read the policy")
	}
	if status, _ := f.do(t, "PUT", "/checkout-grace", gracePolicy(0, nil)); status != 403 {
		t.Fatal("a viewer must not be able to publish the policy")
	}
	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge",
		map[string]any{"expected_state": "OPEN", "reason_code": "REVIEWED"}); status != 403 {
		t.Fatal("a viewer must not be able to act on alerts")
	}
}

// count runs a scalar count/int query against the disposable database.
func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
