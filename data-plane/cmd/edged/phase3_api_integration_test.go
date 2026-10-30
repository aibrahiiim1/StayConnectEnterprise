//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

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

// controlled runs fn in one transaction that first opens each named controlled-writer family; the superuser does
// not own the iam_v2 guards, so seeding a guarded table must open the same operation the product's writer opens.
// Seeding only: the code under test opens its own operations.
func controlled(ctx context.Context, p *pgxpool.Pool, families []string, fn func(tx pgx.Tx) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, fam := range families {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, fam); err != nil {
			return err
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// asOwner runs fn under SET LOCAL ROLE iam_v2_owner, only to seed a row in an owner-guarded table that has no
// product operation a test can call; never on a code path under test.
func asOwner(ctx context.Context, p *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE iam_v2_owner`); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// seedGuestNetwork derives bridge_name (globally unique) and parent_interface (unique among enabled untagged
// networks) from the row's uuid, since several packages share one database. Returns the id and bridge name.
func seedGuestNetwork(t *testing.T, p *pgxpool.Pool, tenant, site, name, subnet string) (string, string) {
	t.Helper()
	var id, bridge string
	if err := p.QueryRow(context.Background(), `
		INSERT INTO public.guest_networks(id,tenant_id,site_id,name,enabled,parent_interface,bridge_name,
		  gateway_cidr,gateway_ip,subnet_cidr)
		SELECT g,$1,$2,$3,true,'p'||substr(md5(g::text),1,12),'b'||substr(md5(g::text),1,12),
		       set_masklen($4::cidr::inet + 1, masklen($4::cidr)), host($4::cidr::inet + 1)::inet, $4::cidr
		FROM gen_random_uuid() g RETURNING id::text, bridge_name`, tenant, site, name, subnet).Scan(&id, &bridge); err != nil {
		t.Fatalf("seed guest network %q: %v", name, err)
	}
	return id, bridge
}

type apiFixture struct {
	srv *httptest.Server
	// app is the server the routes are mounted on, so a test can drive an internal step directly or supply a
	// collaborator main.go would wire (such as the scd client).
	app      *server
	pool     *pgxpool.Pool
	tenant   string
	site     string
	operator string
	password string
	sessTok  string
	mfa      *fakeMFA
	ledger   *ledger.Store
	ledgerC  *storeLedger
}

func newAPI(t *testing.T, roles ...string) *apiFixture { return newAPIIn(t, "", roles...) }

// newAPIIn builds a fixture for a new site; a non-empty tenant creates it under that existing tenant (the
// multi-property arrangement a tenant-only scope check would miss).
func newAPIIn(t *testing.T, tenant string, roles ...string) *apiFixture {
	t.Helper()
	p := testPool(t)
	ctx := context.Background()
	f := &apiFixture{pool: p, password: "operator-step-up-pw"}

	if tenant == "" {
		if err := p.QueryRow(ctx, `WITH
		  t AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
		  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id)
		SELECT (SELECT tenant_id FROM si)::text, (SELECT id FROM si)::text`).Scan(&f.tenant, &f.site); err != nil {
			t.Fatalf("seed tenant/site: %v", err)
		}
	} else if err := p.QueryRow(ctx,
		`INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, $1, g::text, 's' FROM gen_random_uuid() g
		 RETURNING tenant_id::text, id::text`,
		tenant).Scan(&f.tenant, &f.site); err != nil {
		t.Fatalf("seed second site under the same tenant: %v", err)
	}
	// The disposable fixture builds only iam_v2; the appliance's operator identity tables are provisioned here
	// because the controlled operations validate a real actor against them.
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
	// The handlers append to the local audit log; losing those writes silently would hide a broken audit path.
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
	f.mfa = newFakeMFA(p)
	s.mfa = f.mfa
	f.ledger = sharedLedger(t, p)
	sl := &storeLedger{st: f.ledger}
	var keyHex string
	if err := p.QueryRow(ctx, `SELECT iam_v2.ledger_mac_key_get()`).Scan(&keyHex); err == nil {
		sl.key, _ = ledger.ParseMACKey(keyHex) // scd's view of the database key (AD-3): it issues the durability receipts
	}
	f.ledgerC = sl
	s.ledgerC = f.ledgerC
	// Publishing a checkout-grace policy derives plan and package revisions, so without this repository the route answers 503.
	s.commerceRepo = iamv2.NewPgCommerceAdminRepository(p)
	f.app = s
	f.sessTok = s.sessions.create(&session{OperatorID: f.operator, Email: "op@test.local", Roles: roles})

	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			// A no-op until a test gives the server a read recorder.
			r.Use(s.recordGuestDataReads)
			mountResource(r, s, "checkout-grace", s.checkoutGraceConfigRoutes)
			mountResource(r, s, "after-checkout-access", s.afterCheckoutAccessRoutes)
			mountResource(r, s, "operational-alerts", s.operationalAlertsRoutes)
			mountResource(r, s, "pms-stays", s.pmsStaysRoutes)
			mountResource(r, s, "pms-events", s.pmsEventsRoutes)
			mountResource(r, s, "pms-resolutions", s.pmsResolutionsRoutes)
			mountResource(r, s, "pms-interfaces", s.pmsInterfacesRoutes)
			mountResource(r, s, "pms-financial-onboarding", s.pmsFinancialOnboardingRoutes)
			mountResource(r, s, "pms-routing", s.pmsRoutingRoutes)
			mountResource(r, s, "pms-source-conflicts", s.pmsSourceConflictsRoutes)
			mountResource(r, s, "pms-event-decisions", s.pmsEventDecisionsRoutes)
			mountResource(r, s, "pms-roster-reconciliation", s.rosterReconciliationRoutes)
			mountResource(r, s, "stay-transfers", s.stayTransfersRoutes)
			mountResource(r, s, "guest-signin-attempts", s.signInAttemptsRoutes)
			mountResource(r, s, "guest-signin-credentials", s.signInCredentialsRoutes)
			mountResource(r, s, "guest-signin-protection", s.signInProtectionRoutes)
			mountResource(r, s, "guest-signin-restrictions", s.signInRestrictionsRoutes)
			mountResource(r, s, "reports", s.reportsRoutes)
			mountResource(r, s, "financial-review", s.financialReviewRoutes)
			mountResource(r, s, "financial-ops", s.financialOpsRoutes)
			mountResource(r, s, "network", s.networkRoutes)
			mountResource(r, s, "walled-garden", s.walledGardenRoutes)
			mountResource(r, s, "portal-branding", s.brandingRoutes)
			mountResource(r, s, "guest-accounts", s.guestAccountsRoutes)
			mountResource(r, s, "account-password-settings", s.accountPasswordSettingsRoutes)
			mountResource(r, s, "audit", s.auditRoutes)
			mountResource(r, s, "sessions", s.sessionsRoutes)
			r.Route("/client-devices", func(r chi.Router) {
				r.Use(s.resourcePermission("sessions"))
				r.Mount("/", s.clientDevicesAdminRoutes())
			})
			r.Route("/self-service-settings", func(r chi.Router) {
				r.Use(s.resourcePermission("auth-methods"))
				r.Mount("/", s.selfServiceSettingsRoutes())
			})
			r.Route("/device-settings", func(r chi.Router) {
				r.Use(s.resourcePermission("auth-methods"))
				r.Mount("/", s.deviceSettingsRoutes())
			})
			r.Route("/signin-capacity-settings", func(r chi.Router) {
				r.Use(s.resourcePermission("auth-methods"))
				r.Mount("/", s.signinCapacityRoutes())
			})
			r.Route("/accounting-retention", func(r chi.Router) {
				r.Use(s.resourcePermission("backups"))
				r.Mount("/", s.accountingRetentionRoutes())
			})
			r.Route("/data-retention", func(r chi.Router) {
				r.Use(s.resourcePermission("backups"))
				r.Mount("/", s.dataRetentionRoutes())
			})
			mountResource(r, s, "guest-data-rights", s.guestDataRightsRoutes)
			mountResource(r, s, "pms-reconciliation", s.pmsReconciliationRoutes)
			r.Route("/backup-keys", func(r chi.Router) {
				r.Use(s.resourcePermission("backups"))
				r.Mount("/", s.backupKeysRoutes())
			})
			mountResource(r, s, "backups", s.backupsRoutes)
		})
	})
	f.srv = httptest.NewServer(r)
	t.Cleanup(func() {
		f.srv.Close()
		// Deliberate wrong passwords leave step-up throttle rows for this operator and the loopback address; left
		// behind, a second suite on the same database failed with 429 (audit P9 CC4).
		_, _ = p.Exec(context.Background(), `DELETE FROM public.operator_signin_throttle
		     WHERE (scope = $1 AND key_hash = $2) OR (scope = $3 AND key_hash = $4)`,
			scopeReauth, throttleKey(scopeReauth, f.operator), scopeIP, throttleKey(scopeIP, "127.0.0.1"))
		p.Close()
	})
	return f
}

// doRaw returns the exact response bytes: non-enumeration ("out-of-scope looks like absent") can only be proved from raw
// bodies, since two responses can share a status and differ in a message that confirms existence.
func (f *apiFixture) doRaw(t *testing.T, method, path string, body any) (int, string) {
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
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
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
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
	           status,lifecycle_version,last_applied_event_version,effective_checkout_at)
	         SELECT gen_random_uuid(),$1,$2,pi.id,$3,$3,'CHECKED_OUT',1,0, now() - interval '1 hour' FROM pi RETURNING id)
	SELECT (SELECT id FROM pi)::text, (SELECT id FROM st)::text`, f.tenant, f.site, res).Scan(&iface, &stay)
	}); err != nil {
		t.Fatalf("seed stay: %v", err)
	}
	// Events are admitted PENDING and only the engine moves one to a terminal state, so apply it in a separate
	// statement (a data-modifying CTE cannot see a sibling's insert).
	var event string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events
		(tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,pms_timestamp_utc,
		 admission_kind,admission_runtime_generation,resync_generation,received_at)
		VALUES ($1,$2,$3,$4,'GO','{}'::jsonb, now() - interval '1 hour','LIVE',1,0,now()) RETURNING id::text`,
			f.tenant, f.site, iface, res).Scan(&event); err != nil {
			return fmt.Errorf("seed event: %w", err)
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events
		SET processing_status='APPLIED', processed_at=now(), stay_id=$2 WHERE id=$1`, event, stay)
		return err
	}); err != nil {
		t.Fatalf("seed/apply event: %v", err)
	}
	// An emergency alert must accompany a real emergency Grace entitlement (the audit's coherence check), so provision the catalog and grant one.
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
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
		t.Fatalf("open commerce_intent: %v", err)
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
	if err := controlled(ctx, f.pool, []string{"checkout_conversion"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.checkout_grace_audit
		(tenant_id,site_id,pms_interface_id,stay_id,lifecycle_version,trigger,is_emergency,policy_version,
		 alert_code,reason_code,boundary_at,boundary_clock_suspect,grace_entitlement_id,
		 boundary_event_id,boundary_event_seq,boundary_normalization_version,boundary_reason_code,config_version)
		SELECT $1,$2,$3,$4,1,'EMERGENCY_GRACE',true,'EMERGENCY_GRACE_V1','CHECKOUT_GRACE_CONFIG_INVALID','POLICY_MISMATCH',
		       e.pms_timestamp_utc, false, $6::uuid, e.id, e.sequence_version, e.normalization_version,
		       'TRUSTED_PMS_CHECKOUT_TS', 1
		FROM iam_v2.stay_events e WHERE e.id=$5
		RETURNING id::text`, f.tenant, f.site, iface, stay, event, graceEnt).Scan(&auditID)
	}); err != nil {
		t.Fatalf("seed alert: %v", err)
	}
	return auditID
}

func TestIntegration_API_AlertLifecycleOpenAckResolve(t *testing.T) {
	f := newAPI(t)
	audit := f.seedAlert(t)

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

	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge", map[string]any{}); status != 400 {
		t.Fatalf("missing expected_state got %d, want 400", status)
	}
	if status, _ := f.do(t, "POST", "/operational-alerts/"+audit+"/acknowledge",
		map[string]any{"expected_state": "ACKNOWLEDGED", "reason_code": "REVIEWED"}); status != 409 {
		t.Fatalf("stale expected_state got %d, want 409", status)
	}
	if status, _ := f.do(t, "POST", "/operational-alerts/00000000-0000-0000-0000-000000000000/acknowledge",
		map[string]any{"expected_state": "OPEN", "reason_code": "REVIEWED"}); status != 404 {
		t.Fatalf("unknown alert got %d, want 404", status)
	}
	// An alert from another site is invisible and unactionable.
	other := newAPI(t)
	foreign := other.seedAlert(t)
	if status, _ := f.do(t, "POST", "/operational-alerts/"+foreign+"/acknowledge",
		map[string]any{"expected_state": "OPEN", "reason_code": "REVIEWED"}); status != 404 {
		t.Fatalf("cross-site alert got %d, want 404", status)
	}
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

// Concurrent acknowledgements: exactly one wins, the others get a clean conflict.
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

func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Every read-only collection must answer: mounting a route proves it exists, not that its SQL is valid, and a
// handler that swallows the driver error turns a schema mismatch into an opaque 500.
func TestIntegration_API_EveryReadOnlyCollectionAnswers(t *testing.T) {
	f := newAPI(t)
	for _, path := range []string{
		"/pms-stays",
		"/pms-events",
		"/pms-resolutions",
		"/pms-interfaces",
		"/pms-routing",
		"/pms-source-conflicts",
		"/operational-alerts",
		"/checkout-grace",
	} {
		status, body := f.do(t, "GET", path, nil)
		if status != 200 {
			t.Errorf("GET %s = %d (%v) -- the collection must answer, not fail", path, status, body)
		}
	}
}

// validRevisionBody is the smallest configuration the authoring endpoint accepts. The server stamps
// folio_identity_strategy, normalization_version, credential_mode, read_only and resync_supported itself, so they
// are not sent.
func validRevisionBody(endpoint string) map[string]any {
	return map[string]any{
		"endpoint": endpoint, "source_timezone": "Africa/Cairo",
		"dial_timeout_ms": 5000, "read_timeout_ms": 15000, "write_timeout_ms": 15000,
		"heartbeat_interval_ms": 30000, "heartbeat_timeout_ms": 90000,
		"feed_freshness_ms": 120000, "complete_sync_ms": 600000,
	}
}

// Creating an interface and authoring a revision must be write-gated; asserted rather than assumed, since a route
// mounted elsewhere would silently lose the resource middleware's POST-to-write mapping.
func TestIntegration_API_ViewerCannotCreateOrConfigurePMSInterfaces(t *testing.T) {
	f := newAPI(t, "site_viewer")
	if status, _ := f.do(t, "GET", "/pms-interfaces", nil); status != 200 {
		t.Fatal("a viewer must still be able to read the interface list")
	}
	if status, body := f.do(t, "POST", "/pms-interfaces",
		map[string]any{"connector_kind": "protel-fias", "display_label": "viewer attempt"}); status != 403 {
		t.Fatalf("a viewer created a PMS interface: %d %v", status, body)
	}
	if status, body := f.do(t, "POST", "/pms-interfaces/00000000-0000-4000-8000-000000000000/revisions",
		validRevisionBody("pms.local:5010")); status != 403 {
		t.Fatalf("a viewer authored a PMS revision: %d %v", status, body)
	}
}

// Concurrent authoring must not 500: a transaction-scoped advisory lock keyed on the interface gives every caller
// its own revision_no (under READ COMMITTED a MAX()+1 subquery takes no lock), so numbers are a contiguous run
// with no duplicates and nobody loses a draft.
func TestIntegration_API_ConcurrentRevisionAuthoringIsDeterministic(t *testing.T) {
	f := newAPI(t)
	status, body := f.do(t, "POST", "/pms-interfaces",
		map[string]any{"connector_kind": "protel-fias", "display_label": "concurrency"})
	if status != 201 {
		t.Fatalf("create interface: %d %v", status, body)
	}
	id, _ := body["id"].(string)

	const n = 8
	codes := make([]int, n)
	nos := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, b := f.do(t, "POST", "/pms-interfaces/"+id+"/revisions",
				validRevisionBody(fmt.Sprintf("pms%d.local:5010", i)))
			codes[i] = st
			if v, ok := b["revision_no"].(float64); ok {
				nos[i] = int(v)
			}
		}(i)
	}
	wg.Wait()

	seen := map[int]bool{}
	for i, st := range codes {
		if st != 201 {
			t.Errorf("concurrent authoring #%d returned %d, want 201 -- a race must not surface as an error", i, st)
			continue
		}
		if seen[nos[i]] {
			t.Errorf("revision_no %d was handed out twice", nos[i])
		}
		seen[nos[i]] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct revision numbers, got %d", n, len(seen))
	}
	for want := 1; want <= n; want++ {
		if !seen[want] {
			t.Errorf("revision_no %d is missing: the run is not contiguous", want)
		}
	}
	if got := count(t, f.pool,
		`SELECT count(*) FROM iam_v2.pms_interface_revisions WHERE pms_interface_id=$1`, id); got != n {
		t.Fatalf("%d revisions persisted, want %d", got, n)
	}
}

// A decommissioned interface is terminal and must refuse configuration: a revision authored against it could never
// be published or dialled, and accepting it would leave the operator believing a retired interface was reconfigured.
func TestIntegration_API_DecommissionedInterfaceRefusesConfiguration(t *testing.T) {
	f := newAPI(t)
	status, body := f.do(t, "POST", "/pms-interfaces",
		map[string]any{"connector_kind": "protel-fias", "display_label": "lifecycle"})
	if status != 201 {
		t.Fatalf("create interface: %d %v", status, body)
	}
	id, _ := body["id"].(string)

	if st, b := f.do(t, "POST", "/pms-interfaces/"+id+"/revisions",
		validRevisionBody("live.local:5010")); st != 201 {
		t.Fatalf("authoring on a live interface: %d %v", st, b)
	}

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.pms_interfaces SET lifecycle_state='DECOMMISSIONED' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}

	st, b := f.do(t, "POST", "/pms-interfaces/"+id+"/revisions", validRevisionBody("dead.local:5010"))
	if st != 409 {
		t.Fatalf("authoring on a decommissioned interface = %d, want 409: %v", st, b)
	}
	if got := count(t, f.pool,
		`SELECT count(*) FROM iam_v2.pms_interface_revisions WHERE pms_interface_id=$1`, id); got != 1 {
		t.Fatalf("%d revisions after a refused authoring, want 1", got)
	}
}

func TestIntegration_API_AuthoringAnUnknownInterfaceIsNotFound(t *testing.T) {
	f := newAPI(t)
	if st, b := f.do(t, "POST", "/pms-interfaces/00000000-0000-4000-8000-000000000000/revisions",
		validRevisionBody("nowhere.local:5010")); st != 404 {
		t.Fatalf("authoring against an unknown interface = %d, want 404: %v", st, b)
	}
}
