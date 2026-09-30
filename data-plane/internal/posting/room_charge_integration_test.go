//go:build integration

package posting

// PMS ROOM CHARGE, end to end on the FULL appliance schema (migrations 0095-0100), with a scripted transport:
// onboarding, posting creation from the settlement alone, PA=OK -> settled and granted, any other PA -> failed,
// UNKNOWN -> manual review and never retried, and the accepted review actions. Set ROOMCHARGE_TEST_DSN to a
// database built by scripts/sitedb-dev.sh; ROOMCHARGE_WORKER_DSN (svc_posting) proves the worker role suffices.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func rcPool(t *testing.T, env string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s not set", env)
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

type rcScope struct {
	tenant, site, iface, stay, settle, purchase, ac string
	// what a further purchase for the same stay needs, and the reservation the charge targets
	rev, pkgRev, mapping, dev, gn, op, reservation, planRev string
}

func rcGuarded(t *testing.T, p *pgxpool.Pool, family, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, family); err != nil {
		t.Fatalf("open %s: %v", family, err)
	}
	var id string
	if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v -- %s", family, err, sql)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedRoomCharge builds an interface onboarded for reservation-targeted room charge (Amendment A1), an in-house
// stay with a reservation number, a priced Room-charge package with its mapping, a verified Room sign-in, and a
// REQUIRED PMS_POSTING settlement. There is no folio anywhere.
func seedRoomCharge(t *testing.T, p *pgxpool.Pool) rcScope {
	t.Helper()
	ctx := context.Background()
	u := time.Now().UnixNano()
	var s rcScope
	if err := p.QueryRow(ctx, `WITH
	  t  AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM (SELECT gen_random_uuid() g) x RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, (SELECT gen_random_uuid() g) x RETURNING id, tenant_id)
	SELECT tenant_id::text, id::text FROM si`).Scan(&s.tenant, &s.site); err != nil {
		t.Fatal(err)
	}
	op := scan1[string](t, p, `INSERT INTO public.operators (tenant_id, email, status) VALUES ($1,$2,'active') RETURNING id::text`,
		s.tenant, fmt.Sprintf("fin%d@example.test", u))
	s.iface = scan1[string](t, p, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind) VALUES ($1,$2,'protel-fias') RETURNING id::text`, s.tenant, s.site)
	rev := scan1[string](t, p, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,config)
		VALUES ($1,$2,$3,1,'UTC','UNSET','{"heartbeat_timeout_ms":300000,"feed_freshness_ms":900000,"complete_sync_ms":86400000}') RETURNING id::text`,
		s.tenant, s.site, s.iface)
	mustExec(t, p, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`, s.iface, rev)
	// ONBOARDING, through the audited definer.
	onboarded := scan1[string](t, p, `SELECT iam_v2.pms_interface_financial_onboard($1,$2,$3,$4,'RESERVATION','USD',2::smallint,
		'Vendor confirmed in writing: reservation numbers are unique and never reused.','onboarding test',$5)::text`,
		s.tenant, s.site, s.iface, rev, op)
	s.rev, s.op = onboarded, op
	mustExec(t, p, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id,site_id,pms_interface_id,pinned_revision_id,credential_mode,runtime_generation,
		 transport_status,last_connected_at,last_heartbeat_at,continuity_status,last_valid_event_at,
		 sync_status,last_complete_sync_at,resync_generation_seq,published_resync_generation)
		VALUES ($1,$2,$3,$4,'NONE',1,'CONNECTED',now(),now(),'CONTINUOUS',now(),'IN_SYNC',now(),0,0)`,
		s.tenant, s.site, s.iface, onboarded)
	plan := scan1[string](t, p, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code) VALUES ($1,$2,$3) RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("P%d", u))
	planRev := scan1[string](t, p, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'plan',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, s.tenant, s.site, plan)
	pkg := scan1[string](t, p, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,active) VALUES ($1,$2,$3,true) RETURNING id::text`, s.tenant, s.site, fmt.Sprintf("K%d", u))
	s.planRev = planRev
	s.pkgRev = scan1[string](t, p, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
		VALUES ($1,$2,$3,1,$4,'GENERAL',1000,'USD',2,'{PMS_POSTING}') RETURNING id::text`, s.tenant, s.site, pkg, planRev)
	s.mapping = scan1[string](t, p, `INSERT INTO iam_v2.package_settlement_mappings
		(tenant_id,site_id,package_revision_id,pms_interface_id,mapping_revision,posting_code)
		VALUES ($1,$2,$3,$4,1,'WIFI') RETURNING id::text`, s.tenant, s.site, s.pkgRev, s.iface)
	// posting_allowed is not set here: the database evaluates it (IN_HOUSE, reservation present, no block).
	s.reservation = fmt.Sprintf("%d", 5000+u%1000000)
	s.stay = rcGuarded(t, p, "stay", `INSERT INTO iam_v2.stays
		(tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,normalized_room_number,status)
		VALUES ($1,$2,$3,$4,$4,'1421','IN_HOUSE') RETURNING id::text`, s.tenant, s.site, s.iface, s.reservation)
	s.gn = scan1[string](t, p, `INSERT INTO public.guest_networks (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr)
		VALUES (gen_random_uuid(),$1,$2,'net',$3,$4,'10.98.0.1/24','10.98.0.1','10.98.0.0/24') RETURNING id::text`,
		s.tenant, s.site, fmt.Sprintf("eth%d", u%100000000), fmt.Sprintf("b%x", u)[:15])
	s.dev = scan1[string](t, p, `INSERT INTO iam_v2.devices(tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,gen_random_uuid(),$3) RETURNING id::text`,
		s.tenant, s.site, fmt.Sprintf("02:00:01:%02x:%02x:%02x", u&0xff, (u>>8)&0xff, (u>>16)&0xff))
	addRoomChargePurchase(t, p, &s)
	return s
}

// addRoomChargePurchase adds one more verified Room sign-in, quote, AWAITING_SETTLEMENT purchase and REQUIRED
// PMS_POSTING settlement for the same stay, so a test can make sequential purchases in one stay. Each further
// purchase is of a NEW package: commerce already allows only one live purchase of the same package revision
// per stay (C3, purchase_once_per_stay), which is a different rule from the one room charge per stay (E16).
func addRoomChargePurchase(t *testing.T, p *pgxpool.Pool, s *rcScope) {
	t.Helper()
	if s.purchase != "" {
		u := time.Now().UnixNano()
		pkg := scan1[string](t, p, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,active) VALUES ($1,$2,$3,true) RETURNING id::text`,
			s.tenant, s.site, fmt.Sprintf("K%d", u))
		s.pkgRev = scan1[string](t, p, `INSERT INTO iam_v2.internet_package_revisions
			(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
			VALUES ($1,$2,$3,1,$4,'GENERAL',1000,'USD',2,'{PMS_POSTING}') RETURNING id::text`, s.tenant, s.site, pkg, s.planRev)
		s.mapping = scan1[string](t, p, `INSERT INTO iam_v2.package_settlement_mappings
			(tenant_id,site_id,package_revision_id,pms_interface_id,mapping_revision,posting_code)
			VALUES ($1,$2,$3,$4,1,'WIFI') RETURNING id::text`, s.tenant, s.site, s.pkgRev, s.iface)
	}
	s.ac = rcGuarded(t, p, "auth_context", `INSERT INTO iam_v2.auth_contexts
		(tenant_id,site_id,method,stay_id,pms_interface_id,authentication_interface_revision_id,device_id,guest_network_id,expires_at,consumed_at)
		VALUES ($1,$2,'PMS',$3,$4,$5,$6,$7, now()+interval '10 minutes', now()) RETURNING id::text`,
		s.tenant, s.site, s.stay, s.iface, s.rev, s.dev, s.gn)
	planRev := scan1[string](t, p, `SELECT service_plan_revision_id::text FROM iam_v2.internet_package_revisions WHERE id=$1`, s.pkgRev)
	snap := `{"version":1,"service_plan_revision_id":"` + planRev + `","package_revision_id":"` + s.pkgRev +
		`","max_concurrent_devices":2,"time_accounting_mode":"VALIDITY_WINDOW","end_mode":"MANUAL_END","acquisition_method":"PMS_POSTING"}`
	quote := rcGuarded(t, p, "commerce_intent", `INSERT INTO iam_v2.offer_quotes
		(tenant_id,site_id,auth_context_id,package_revision_id,pms_interface_id,settlement_mapping_id,price_minor,currency,currency_exponent,grant_snapshot,expires_at,consumed_at)
		VALUES ($1,$2,$3,$4,$5,$6,1000,'USD',2,$7::jsonb, now()+interval '5 minutes', now()) RETURNING id::text`,
		s.tenant, s.site, s.ac, s.pkgRev, s.iface, s.mapping, snap)
	s.purchase = rcGuarded(t, p, "commerce_intent", `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,pms_interface_id,stay_id,settlement_mapping_id,
		 authentication_interface_revision_id,trigger,amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'GUEST_SELECTION',1000,'USD',2,'AWAITING_SETTLEMENT') RETURNING id::text`,
		s.tenant, s.site, s.pkgRev, quote, s.ac, s.iface, s.stay, s.mapping, s.rev)
	s.settle = rcGuarded(t, p, "commerce_intent", `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'PMS_POSTING','REQUIRED') RETURNING id::text`, s.tenant, s.site, s.purchase)
}

var transmitCfg = Config{MasterEnabled: true, PostingEnabled: true, OutboxEnabled: true, TransmitEnabled: true, ReviewEnabled: true}

func rcState(t *testing.T, p *pgxpool.Pool, s rcScope) (settle, purchase string, ents int) {
	t.Helper()
	settle = scan1[string](t, p, `SELECT status FROM iam_v2.settlements WHERE id=$1`, s.settle)
	purchase = scan1[string](t, p, `SELECT state FROM iam_v2.purchases WHERE id=$1`, s.purchase)
	ents = scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.entitlements WHERE purchase_id=$1 AND stay_id=$2`, s.purchase, s.stay)
	return
}

func createRoomCharge(t *testing.T, p *pgxpool.Pool, s rcScope) string {
	t.Helper()
	id := scan1[string](t, p, `SELECT iam_v2.p4_create_room_charge_posting($1,$2,$3)::text`, s.tenant, s.site, s.settle)
	if st, _, _ := rcState(t, p, s); st != "IN_PROGRESS" {
		t.Fatalf("creating the posting moves the settlement to IN_PROGRESS, got %s", st)
	}
	return id
}

func TestRoomChargePAOKSettlesAndGrants(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	worker := admin
	if os.Getenv("ROOMCHARGE_WORKER_DSN") != "" {
		worker = rcPool(t, "ROOMCHARGE_WORKER_DSN")
	}
	s := seedRoomCharge(t, admin)
	createRoomCharge(t, admin, s)
	if st, pu, n := rcState(t, admin, s); st != "IN_PROGRESS" || pu != "AWAITING_SETTLEMENT" || n != 0 {
		t.Fatalf("sending nothing yet grants nothing: %s %s %d", st, pu, n)
	}
	tr := &stubTransport{}
	e := NewEngine(transmitCfg, NewRepo(worker), tr)
	out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if err != nil || out.Result != "POSTED" {
		t.Fatalf("run: %+v %v", out, err)
	}
	if !strings.Contains(tr.bodies[0], "|RN1421|") || !strings.Contains(tr.bodies[0], "|G#"+s.reservation+"|") || !strings.Contains(tr.bodies[0], "|CTWIFI|") {
		t.Fatalf("PS must target the stay's current room and its reservation with the mapped posting code: %s", tr.bodies[0])
	}
	if st, pu, n := rcState(t, admin, s); st != "SETTLED" || pu != "GRANTED" || n != 1 {
		t.Fatalf("PA=OK settles and grants a stay entitlement: %s %s %d", st, pu, n)
	}
	// Exactly once: a second pass finds nothing and a re-apply grants nothing more.
	_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	_ = e.ApplySettlementOutcomes(context.Background(), s.tenant, s.site)
	if _, _, n := rcState(t, admin, s); n != 1 || tr.count() != 1 {
		t.Fatalf("one PS, one entitlement: sent=%d ents=%d", tr.count(), n)
	}
}

// A refusal the vendor has confirmed means "not posted" fails the charge (Amendment A1, rule 8). The same code
// NOT confirmed is UNKNOWN: see TestA1_UnconfirmedRefusalIsUnknown.
func TestRoomChargeRejectedFails(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	confirmAnswer(t, admin, s, "NG")
	createRoomCharge(t, admin, s)
	tr := &stubTransport{answer: func(pn int64) (*PA, error) { return &PA{PNumber: pn, AS: "NG"}, nil }}
	e := NewEngine(transmitCfg, NewRepo(admin), tr)
	if _, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface); err != nil {
		t.Fatal(err)
	}
	if st, pu, n := rcState(t, admin, s); st != "FAILED" || pu != "FAILED" || n != 0 {
		t.Fatalf("a refused posting fails and grants nothing: %s %s %d", st, pu, n)
	}
}

func TestRoomChargeUnknownIsNeverRetriedAndReviewDecides(t *testing.T) {
	for _, tc := range []struct {
		action   string
		settle   string
		purchase string
		ents     int
	}{
		{"CONFIRM_POSTED", "SETTLED", "GRANTED", 1},
		{"CONFIRM_NOT_POSTED_ABANDON", "FAILED", "FAILED", 0},
		{"CONFIRM_NOT_POSTED_RETRY", "SETTLED", "GRANTED", 1},
	} {
		t.Run(tc.action, func(t *testing.T) {
			admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
			s := seedRoomCharge(t, admin)
			posting := createRoomCharge(t, admin, s)
			first := true
			tr := &stubTransport{answer: func(pn int64) (*PA, error) {
				if first {
					first = false
					return nil, fmt.Errorf("connection torn after write")
				}
				return &PA{PNumber: pn, AS: "OK"}, nil
			}}
			e := NewEngine(transmitCfg, NewRepo(admin), tr)
			_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
			if st, pu, n := rcState(t, admin, s); st != "MANUAL_REVIEW" || pu != "MANUAL_REVIEW" || n != 0 {
				t.Fatalf("UNKNOWN goes to review and grants nothing: %s %s %d", st, pu, n)
			}
			// Nothing retries on its own.
			for i := 0; i < 3; i++ {
				_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
			}
			if tr.count() != 1 {
				t.Fatalf("UNKNOWN must never be re-sent automatically, sent %d", tr.count())
			}
			op := scan1[string](t, admin, `SELECT id::text FROM public.operators WHERE tenant_id=$1 LIMIT 1`, s.tenant)
			ver := scan1[int](t, admin, `SELECT COALESCE((SELECT review_version FROM iam_v2.posting_review_state WHERE posting_id=$1),0)`, posting)
			evidence := `{"pms_reference":"checked at front office","source":"FO","observed_at":"2026-09-28T10:00:00Z"}`
			if _, err := admin.Exec(context.Background(), `SELECT iam_v2.record_posting_review_action($1,$2,$3,$4,$5::jsonb,$6,NULL)`,
				posting, tc.action, op, "front office verified the folio", evidence, ver); err != nil {
				t.Fatalf("record %s: %v", tc.action, err)
			}
			if _, err := admin.Exec(context.Background(), `SELECT iam_v2.p4_posting_review_apply($1,$2,$3)`, s.tenant, s.site, posting); err != nil {
				t.Fatalf("apply %s: %v", tc.action, err)
			}
			if tc.action == "CONFIRM_NOT_POSTED_RETRY" {
				if _, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface); err != nil {
					t.Fatalf("the one authorised retry: %v", err)
				}
				if tr.count() != 2 {
					t.Fatalf("exactly one authorised retry, sent %d", tr.count())
				}
			}
			if st, pu, n := rcState(t, admin, s); st != tc.settle || pu != tc.purchase || n != tc.ents {
				t.Fatalf("%s: want %s/%s/%d, got %s/%s/%d", tc.action, tc.settle, tc.purchase, tc.ents, st, pu, n)
			}
		})
	}
}

// Without the posting target recorded, nothing is charged (E4b): a fresh revision is UNSET.
func TestRoomChargeRefusedWithoutOnboarding(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	unset := scan1[string](t, admin, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,config)
		VALUES ($1,$2,$3,99,'UTC','UNSET','{}') RETURNING id::text`, s.tenant, s.site, s.iface)
	mustExec(t, admin, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`, s.iface, unset)
	_, err := admin.Exec(context.Background(), `SELECT iam_v2.p4_create_room_charge_posting($1,$2,$3)`, s.tenant, s.site, s.settle)
	if err == nil || !strings.Contains(err.Error(), "NOT_ONBOARDED") {
		t.Fatalf("an interface whose posting target is UNSET must refuse: %v", err)
	}
	if n := scan1[int](t, admin, `SELECT count(*)::int FROM iam_v2.pms_postings WHERE stay_id=$1`, s.stay); n != 0 {
		t.Fatalf("nothing is created: %d", n)
	}
}
