//go:build integration

package posting

// ROOM CHARGE ACROSS THE REAL BOUNDARY (decision D45), on the full appliance schema (0095-0098):
//
//	posting engine (financial authority, svc_posting)
//	  -> postinghandoff client -> root-only unix socket
//	  -> pmsd FinancialRelay (authorises the exact bytes via p4_posting_command_authorised, as svc_pmsd)
//	  -> the real FIAS adapter and its single writer -> a fake PMS over a pipe
//	  -> PA -> back through the relay -> engine -> settlement.
//
// Env: ROOMCHARGE_TEST_DSN (admin), optional ROOMCHARGE_WORKER_DSN (svc_posting) and ROOMCHARGE_PMSD_DSN
// (svc_pmsd) to prove each side needs only its own privileges.

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
	"github.com/stayconnect/enterprise/data-plane/internal/postinghandoff"
)

type nopSink struct{}

func (nopSink) OnConnected(time.Time) error                        { return nil }
func (nopSink) OnHeartbeat(time.Time) error                        { return nil }
func (nopSink) RequireInitialResync(time.Time) error               { return nil }
func (nopSink) OnResyncStart(time.Time) error                      { return nil }
func (nopSink) OnResyncComplete(time.Time, string) error           { return nil }
func (nopSink) OnDisconnected(time.Time, pmsd.Code) error          { return nil }
func (nopSink) OnDomainEvent(context.Context, pmsd.Event) error    { return nil }
func (nopSink) OnContinuityFault(context.Context, pmsd.Code) error { return nil }
func (nopSink) ClaimOperatorResync() *pmsd.ResyncCommand           { return nil }
func (nopSink) RecordSkipped(int64)                                {}
func (nopSink) RecordCoverage([]string, int, int, int)             {}
func (nopSink) OnFullSyncRequested()                               {}

// fakePMS answers each PS according to reply (return "" to stay silent, "CLOSE" to drop the link).
type fakePMS struct {
	mu    sync.Mutex
	seen  []string
	reply func(ps string) string
}

func (f *fakePMS) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.seen) }

type rig struct {
	engine *Engine
	pms    *fakePMS
	relay  *pmsd.FinancialRelay
	cancel context.CancelFunc
}

func authoriserFrom(p *pgxpool.Pool) pmsd.Authoriser {
	return func(ctx context.Context, iface string, pn int64, sha string) (string, error) {
		var v string
		err := p.QueryRow(ctx, `SELECT iam_v2.p4_posting_command_authorised($1::uuid, $2, $3)`,
			iface, strconv.FormatInt(pn, 10), sha).Scan(&v)
		return v, err
	}
}

func startRig(t *testing.T, admin *pgxpool.Pool, s rcScope, f *fakePMS) *rig {
	t.Helper()
	pmsdPool := admin
	if os.Getenv("ROOMCHARGE_PMSD_DSN") != "" {
		pmsdPool = rcPool(t, "ROOMCHARGE_PMSD_DSN")
	}
	worker := admin
	if os.Getenv("ROOMCHARGE_WORKER_DSN") != "" {
		worker = rcPool(t, "ROOMCHARGE_WORKER_DSN")
	}
	// A short path: a unix socket path is limited to about 100 bytes, and a per-test temp dir is longer.
	dir, err := os.MkdirTemp("", "ho")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	for k, v := range map[string]string{
		"STAYCONNECT_PHASE4_MASTER": "1", "STAYCONNECT_PHASE4_POSTING": "1", "STAYCONNECT_PHASE4_OUTBOX_WORKER": "1",
		"STAYCONNECT_PHASE4_PMS_TRANSMIT": "1", "STAYCONNECT_PHASE4_FINANCIAL_REVIEW": "1", postinghandoff.EnvSocket: sock,
	} {
		t.Setenv(k, v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	relay := pmsd.NewFinancialRelay(pmsd.RelayTransmitDeployed(os.Getenv), authoriserFrom(pmsdPool), nil)
	go func() {
		if err := relay.ServeSocket(ctx, sock); err != nil {
			t.Errorf("relay socket: %v", err)
		}
	}()

	client, server := net.Pipe()
	dial := pmsd.NewFIASDialWithRelay(func(context.Context, string, string) (net.Conn, error) { return client, nil },
		pmsd.AdapterKeys{IdentityKey: []byte("identity-key-0123456789abcdef01"), IdentityKeyVersion: 1,
			EvidenceKey: []byte("evidence-key-0123456789abcdef01"), EvidenceKeyVersion: 1},
		time.Now, nil, relay)
	conn, derr := dial(ctx, pmsd.DialParams{
		Iface: pmsd.Interface{TenantID: s.tenant, SiteID: s.site, ID: s.iface, ConnectorKind: "protel-fias", LifecycleState: "ACTIVE"},
		Rev:   pmsd.Revision{Endpoint: "pipe", WriteTimeout: time.Second, HeartbeatInterval: 0, HeartbeatTimeout: 0},
	})
	if derr != nil {
		t.Fatal(derr)
	}
	go func() {
		br := bufio.NewReader(server)
		for i := 0; i < 6; i++ { // LS, LD, LR x3, DR
			if _, err := pms.ReadFramedRecord(br); err != nil {
				return
			}
		}
		_ = pms.WriteFramedRecord(server, "DS|")
		_ = pms.WriteFramedRecord(server, "DE|")
		for {
			got, err := pms.ReadFramedRecord(br)
			if err != nil {
				return
			}
			if pms.RecordID(got) != "PS" {
				continue
			}
			f.mu.Lock()
			f.seen = append(f.seen, got)
			f.mu.Unlock()
			switch r := f.reply(got); r {
			case "":
			case "CLOSE":
				_ = server.Close()
				return
			default:
				_ = pms.WriteFramedRecord(server, r)
			}
		}
	}()
	go func() { _ = conn.Serve(ctx, nopSink{}) }()
	t.Cleanup(func() { cancel(); _ = server.Close() })

	eng, err := ProductionEngineWithEnv(NewRepo(worker), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // socket up, handshake and the first DS..DE published
	return &rig{engine: eng, pms: f, relay: relay, cancel: cancel}
}

// run executes lane passes until the posting is concluded (a pass refused NOT_SENT -- e.g. the link still
// resyncing -- is legitimately queued again, which is exactly the case this loop exercises).
func (r *rig) run(t *testing.T, s rcScope) Outcome {
	t.Helper()
	for i := 0; i < 20; i++ {
		out, _ := r.engine.RunOnce(context.Background(), s.tenant, s.site, s.iface)
		if out.Claimed && out.Result != "NOT_SENT" {
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the posting was never carried")
	return Outcome{}
}

// seedHandoff builds, on the full appliance schema, exactly what a real room charge needs (Phase-0 Amendment
// A1): a FIAS interface financially ONBOARDED with the RESERVATION posting target through the audited definer,
// an in-house stay whose reservation number is the G# every PS carries (no folio exists anywhere), a priced
// Room-charge package with its mapping, a verified Room sign-in and a REQUIRED PMS_POSTING settlement.
// posting_allowed is not written: the database computes it.
func seedHandoff(t *testing.T, p *pgxpool.Pool) rcScope {
	t.Helper()
	var s rcScope
	if err := p.QueryRow(context.Background(), `WITH
	  t  AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id)
	SELECT tenant_id::text, id::text FROM si`).Scan(&s.tenant, &s.site); err != nil {
		t.Fatal(err)
	}
	op := scan1[string](t, p, `INSERT INTO public.operators (tenant_id, email, status)
		VALUES ($1, 'fin-'||gen_random_uuid()::text||'@example.test', 'active') RETURNING id::text`, s.tenant)
	s.iface = scan1[string](t, p, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind) VALUES ($1,$2,'protel-fias') RETURNING id::text`, s.tenant, s.site)
	rev := scan1[string](t, p, `INSERT INTO iam_v2.pms_interface_revisions
		(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,config)
		VALUES ($1,$2,$3,1,'UTC','UNSET','{"heartbeat_timeout_ms":60000,"feed_freshness_ms":300000,"complete_sync_ms":3600000}') RETURNING id::text`,
		s.tenant, s.site, s.iface)
	mustExec(t, p, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2 WHERE id=$1`, s.iface, rev)
	onboarded := scan1[string](t, p, `SELECT iam_v2.pms_interface_financial_onboard($1,$2,$3,$4,'RESERVATION','USD',2::smallint,
		'Vendor confirmed: reservation numbers are unique and never reused across stays.','onboarding test',$5)::text`,
		s.tenant, s.site, s.iface, rev, op)
	mustExec(t, p, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id,site_id,pms_interface_id,pinned_revision_id,credential_mode,runtime_generation,
		 transport_status,last_connected_at,last_heartbeat_at,continuity_status,last_valid_event_at,
		 sync_status,last_complete_sync_at,resync_generation_seq,published_resync_generation)
		VALUES ($1,$2,$3,$4,'NONE',1,'CONNECTED',now(),now(),'CONTINUOUS',now(),'IN_SYNC',now(),0,0)`,
		s.tenant, s.site, s.iface, onboarded)
	plan := scan1[string](t, p, `INSERT INTO iam_v2.service_plans(tenant_id,site_id,code) VALUES ($1,$2,'P-'||substr(gen_random_uuid()::text,1,8)) RETURNING id::text`, s.tenant, s.site)
	planRev := scan1[string](t, p, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
		VALUES ($1,$2,$3,1,'plan',2,'VALIDITY_WINDOW',1000000) RETURNING id::text`, s.tenant, s.site, plan)
	pkg := scan1[string](t, p, `INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,active) VALUES ($1,$2,'K-'||substr(gen_random_uuid()::text,1,8),true) RETURNING id::text`, s.tenant, s.site)
	pkgRev := scan1[string](t, p, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods)
		VALUES ($1,$2,$3,1,$4,'GENERAL',1000,'USD',2,'{PMS_POSTING}') RETURNING id::text`, s.tenant, s.site, pkg, planRev)
	mapping := scan1[string](t, p, `INSERT INTO iam_v2.package_settlement_mappings
		(tenant_id,site_id,package_revision_id,pms_interface_id,mapping_revision,posting_code)
		VALUES ($1,$2,$3,$4,1,'WIFI') RETURNING id::text`, s.tenant, s.site, pkgRev, s.iface)
	s.stay = p4Controlled(t, p, "stay", `INSERT INTO iam_v2.stays
		(tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,normalized_room_number,status)
		VALUES ($1,$2,$3,'R'||substr(md5(gen_random_uuid()::text),1,10),'S1','1421','IN_HOUSE') RETURNING id::text`,
		s.tenant, s.site, s.iface)
	gn := scan1[string](t, p, `INSERT INTO public.guest_networks
		(id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr)
		SELECT g,$1,$2,'net','p'||substr(md5(g::text),1,12),'b'||substr(md5(g::text),1,12),
		       '10.98.0.1/24','10.98.0.1','10.98.0.0/24' FROM gen_random_uuid() g RETURNING id::text`, s.tenant, s.site)
	dev := scan1[string](t, p, `INSERT INTO iam_v2.devices(tenant_id,site_id,appliance_id,mac)
		SELECT $1,$2,gen_random_uuid(),
		       ('02:'||substr(h,1,2)||':'||substr(h,3,2)||':'||substr(h,5,2)||':'||substr(h,7,2)||':'||substr(h,9,2))::macaddr
		  FROM md5(gen_random_uuid()::text) h RETURNING id::text`, s.tenant, s.site)
	s.ac = p4Controlled(t, p, "auth_context", `INSERT INTO iam_v2.auth_contexts
		(tenant_id,site_id,method,stay_id,pms_interface_id,authentication_interface_revision_id,device_id,guest_network_id,expires_at,consumed_at)
		VALUES ($1,$2,'PMS',$3,$4,$5,$6,$7, now()+interval '10 minutes', now()) RETURNING id::text`,
		s.tenant, s.site, s.stay, s.iface, onboarded, dev, gn)
	snap := `{"version":1,"service_plan_revision_id":"` + planRev + `","package_revision_id":"` + pkgRev +
		`","max_concurrent_devices":2,"time_accounting_mode":"VALIDITY_WINDOW","end_mode":"MANUAL_END","acquisition_method":"PMS_POSTING"}`
	quote := p4Controlled(t, p, "commerce_intent", `INSERT INTO iam_v2.offer_quotes
		(tenant_id,site_id,auth_context_id,package_revision_id,pms_interface_id,settlement_mapping_id,price_minor,currency,currency_exponent,grant_snapshot,expires_at,consumed_at)
		VALUES ($1,$2,$3,$4,$5,$6,1000,'USD',2,$7::jsonb, now()+interval '5 minutes', now()) RETURNING id::text`,
		s.tenant, s.site, s.ac, pkgRev, s.iface, mapping, snap)
	s.purchase = p4Controlled(t, p, "commerce_intent", `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,offer_quote_id,auth_context_id,pms_interface_id,stay_id,settlement_mapping_id,
		 authentication_interface_revision_id,trigger,amount_minor,currency,currency_exponent,state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'GUEST_SELECTION',1000,'USD',2,'AWAITING_SETTLEMENT') RETURNING id::text`,
		s.tenant, s.site, pkgRev, quote, s.ac, s.iface, s.stay, mapping, onboarded)
	s.settle = scan1[string](t, p, `INSERT INTO iam_v2.settlements(tenant_id,site_id,purchase_id,method,status)
		VALUES ($1,$2,$3,'PMS_POSTING','REQUIRED') RETURNING id::text`, s.tenant, s.site, s.purchase)
	return s
}

// reservationOf is the stay's reservation number: the G# a PS for it carries (A1).
func reservationOf(t *testing.T, p *pgxpool.Pool, s rcScope) string {
	return scan1[string](t, p, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, s.stay)
}

func pnOf(t *testing.T, ps string) string {
	return strconv.FormatInt(postinghandoff.PNumberOf(ps), 10)
}

func TestHandoff_PAOKOverTheOneLinkSettlesAndGrantsExactlyOnce(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedHandoff(t, admin)
	posting := createRoomCharge(t, admin, s)
	f := &fakePMS{reply: func(ps string) string { return "PA|RN1421|P#" + pnOf(t, ps) + "|ASOK|" }}
	r := startRig(t, admin, s, f)

	out := r.run(t, s)
	if out.Result != "POSTED" {
		t.Fatalf("want POSTED, got %+v", out)
	}
	if st, pu, n := rcState(t, admin, s); st != "SETTLED" || pu != "GRANTED" || n != 1 {
		t.Fatalf("PA=OK settles and grants: %s %s %d", st, pu, n)
	}
	// The PMS received exactly the bytes the financial path recorded, once.
	stored := scan1[string](t, admin, `SELECT ps_sha256 FROM iam_v2.posting_attempts WHERE internal_posting_id=$1`, posting)
	if f.count() != 1 || postinghandoff.BodyHash(f.seen[0]) != stored {
		t.Fatalf("one PS, byte-identical to the authorised command: sent=%d", f.count())
	}
	// A1: the PS targets the reservation -- the stay's current room and its reservation number as G#.
	if !strings.Contains(f.seen[0], "|RN1421|G#"+reservationOf(t, admin, s)+"|") {
		t.Fatalf("the PS must carry the stay's room and reservation number: %s", f.seen[0])
	}
	// Replaying the same command through a fresh relay (no in-memory memory of it) is refused by the database:
	// the attempt is no longer SENDING. Nothing reaches the PMS.
	fresh := pmsd.NewFinancialRelay(true, authoriserFrom(admin), nil)
	resp := fresh.Handle(context.Background(), postinghandoff.Request{Version: 1, InterfaceID: s.iface,
		PNumber: postinghandoff.PNumberOf(f.seen[0]), Body: f.seen[0], BodySHA256: postinghandoff.BodyHash(f.seen[0]), WaitMillis: 500})
	if resp.Result != postinghandoff.NotTransmitted {
		t.Fatalf("a concluded command can never be carried again: %+v", resp)
	}
	_, _ = r.engine.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if f.count() != 1 {
		t.Fatal("nothing further is sent")
	}
}

func TestHandoff_LinkLostAfterWriteIsUnknownAndNeverResent(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedHandoff(t, admin)
	posting := createRoomCharge(t, admin, s)
	f := &fakePMS{reply: func(string) string { return "CLOSE" }}
	r := startRig(t, admin, s, f)

	out := r.run(t, s)
	if out.Result != "UNKNOWN" {
		t.Fatalf("a PS written and never answered is UNKNOWN, got %+v", out)
	}
	if st, _, n := rcState(t, admin, s); st != "MANUAL_REVIEW" || n != 0 {
		t.Fatalf("UNKNOWN goes to manual review and grants nothing: %s %d", st, n)
	}
	if ob := scan1[string](t, admin, `SELECT state FROM iam_v2.posting_outbox WHERE posting_id=$1`, posting); ob != "HELD_RECOVERY" {
		t.Fatalf("outbox %s", ob)
	}
	for i := 0; i < 3; i++ {
		if o, _ := r.engine.RunOnce(context.Background(), s.tenant, s.site, s.iface); o.Claimed {
			t.Fatal("an UNKNOWN posting is never claimed again")
		}
	}
	if f.count() != 1 {
		t.Fatalf("exactly one PS, ever: %d", f.count())
	}
}

func TestHandoff_TamperedBytesAreNeverCarried(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedHandoff(t, admin)
	createRoomCharge(t, admin, s)
	// The stub's NG answer is a definite "not posted" only when the vendor confirmed NG on this interface (A1);
	// unconfirmed it would be UNKNOWN. The fixture confirms it, so the pass concludes as it did before A1.
	mustExec(t, admin, `SELECT iam_v2.pms_answer_confirmation_record($1,$2,$3,'NG','CONFIRM',
		'Vendor interface specification section 7: this status means the posting was not applied.','fixture',
		(SELECT id FROM public.operators WHERE tenant_id=$1 LIMIT 1))`, s.tenant, s.site, s.iface)
	g := reservationOf(t, admin, s)
	relay := pmsd.NewFinancialRelay(true, authoriserFrom(admin), nil)
	var verdict postinghandoff.Response
	// While the real attempt is SENDING, present pmsd a command for the same P# whose amount was changed. It is
	// self-consistent (its own hash), but not the bytes the financial path authorised.
	tr := &stubTransport{answer: func(pn int64) (*PA, error) {
		body := "PS|RN1421|G#" + g + "|TA999999|PT|SOOG|CTWIFI|P#" + strconv.FormatInt(pn, 10) + "|WSOG|"
		verdict = relay.Handle(context.Background(), postinghandoff.Request{Version: 1, InterfaceID: s.iface,
			PNumber: pn, Body: body, BodySHA256: postinghandoff.BodyHash(body), WaitMillis: 500})
		return &PA{PNumber: pn, AS: "NG"}, nil
	}}
	e := NewEngine(transmitCfg, NewRepo(admin), tr)
	if _, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface); err != nil {
		t.Fatal(err)
	}
	if verdict.Result != postinghandoff.NotTransmitted || verdict.Code != "COMMAND_BYTES_NOT_AUTHORISED" {
		t.Fatalf("changed bytes are refused by the database before any write: %+v", verdict)
	}
}

func TestHandoff_AttemptLeftSendingByAStoppedWorkerBecomesUnknown(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedHandoff(t, admin)
	posting := createRoomCharge(t, admin, s)
	// The worker records the attempt and dies before recording any outcome.
	stopped := make(chan struct{})
	tr := &stubTransport{answer: func(int64) (*PA, error) { close(stopped); select {} }}
	e := NewEngine(transmitCfg, NewRepo(admin), tr)
	go func() { _, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface) }()
	<-stopped
	// Age the attempt past any possible in-process send (the identity trigger forbids this outside a test).
	ctx := context.Background()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE iam_v2.posting_attempts DISABLE TRIGGER pa_oneway`,
		`UPDATE iam_v2.posting_attempts SET sent_at = now() - interval '10 minutes' WHERE internal_posting_id = '` + posting + `'`,
		`ALTER TABLE iam_v2.posting_attempts ENABLE TRIGGER pa_oneway`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := NewEngine(transmitCfg, NewRepo(admin), &stubTransport{}).RecoverOrphanedAttempts(ctx, s.tenant, s.site, 0)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	if o := scan1[string](t, admin, `SELECT outcome FROM iam_v2.posting_attempts WHERE internal_posting_id=$1`, posting); o != "UNKNOWN" {
		t.Fatalf("attempt %s", o)
	}
	if st, _, _ := rcState(t, admin, s); st != "MANUAL_REVIEW" {
		t.Fatalf("settlement %s", st)
	}
}
