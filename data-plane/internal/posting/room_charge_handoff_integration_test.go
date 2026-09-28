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

func pnOf(t *testing.T, ps string) string {
	return strconv.FormatInt(postinghandoff.PNumberOf(ps), 10)
}

func TestHandoff_PAOKOverTheOneLinkSettlesAndGrantsExactlyOnce(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
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
	s := seedRoomCharge(t, admin)
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
	s := seedRoomCharge(t, admin)
	createRoomCharge(t, admin, s)
	relay := pmsd.NewFinancialRelay(true, authoriserFrom(admin), nil)
	var verdict postinghandoff.Response
	// While the real attempt is SENDING, present pmsd a command for the same P# whose amount was changed. It is
	// self-consistent (its own hash), but not the bytes the financial path authorised.
	tr := &stubTransport{answer: func(pn int64) (*PA, error) {
		body := "PS|RN1421|G#5|TA999999|PT|SOOG|CTWIFI|P#" + strconv.FormatInt(pn, 10) + "|WSOG|"
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
	s := seedRoomCharge(t, admin)
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
