//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The Backups screen's relays to scd for the erasure ledger and the daily backup: who may do what, what is forwarded (and what is
// not), and that credentials never reach the activity log.

type ledgerScdCall struct {
	Method, Path string
	Body         map[string]any
}

type recordingSCD struct {
	mu    sync.Mutex
	calls []ledgerScdCall
	reply map[string]string // path -> JSON reply
	code  map[string]int
}

func fakeLedgerSCD(t *testing.T) (*scdClient, *recordingSCD) {
	t.Helper()
	dir, err := os.MkdirTemp("", "scd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	rec := &recordingSCD{reply: map[string]string{}, code: map[string]int{}}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		rec.mu.Lock()
		rec.calls = append(rec.calls, ledgerScdCall{r.Method, r.URL.Path, body})
		rep, ok := rec.reply[r.URL.Path]
		code := rec.code[r.URL.Path]
		rec.mu.Unlock()
		if !ok {
			rep = `{"ok":true}`
		}
		if code == 0 {
			code = 200
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(rep))
	})}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })
	return newSCDClient(filepath.Join(dir, "s")), rec
}

func (r *recordingSCD) last(path string) *ledgerScdCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.calls) - 1; i >= 0; i-- {
		if r.calls[i].Path == path {
			return &r.calls[i]
		}
	}
	return nil
}

func TestIntegration_LedgerAdmin_SavingTheSecondaryStorageNeedsThePasswordAndKeepsCredentialsOutOfTheLog(t *testing.T) {
	f := newAPI(t, "site_admin")
	scd, rec := fakeLedgerSCD(t)
	f.app.scd = scd
	secret := "S3-SECRET-KEY-VALUE-" + fmt.Sprint(time.Now().UnixNano())
	body := map[string]any{"kind": "s3", "bucket": "b", "region": "eu-west-1", "access_key": "AK", "secret_key": secret}

	// without the password nothing is forwarded
	if st, _ := f.do(t, "PUT", "/backups/ledger/destination", body); st == 200 {
		t.Fatalf("the secondary storage was saved without the password")
	}
	if rec.last("/v1/ledger/destination") != nil {
		t.Fatalf("an unauthorised save reached scd")
	}
	// with it: forwarded, the password removed, the operator named, never a test
	body["password"] = f.password
	body["test_only"] = true // a caller cannot turn a save into a test or the other way
	if st, out := f.do(t, "PUT", "/backups/ledger/destination", body); st != 200 {
		t.Fatalf("save = %d %v", st, out)
	}
	c := rec.last("/v1/ledger/destination")
	if c == nil || c.Method != "PUT" {
		t.Fatalf("scd call = %+v", c)
	}
	if _, has := c.Body["password"]; has {
		t.Errorf("the operator's password was forwarded")
	}
	if _, has := c.Body["test_only"]; has {
		t.Errorf("a caller-supplied test_only reached scd on a save")
	}
	if c.Body["secret_key"] != secret || c.Body["kind"] != "s3" || c.Body["by"] == "" {
		t.Errorf("forwarded body = %v", c.Body)
	}
	// the activity log says WHAT was chosen, never the credentials
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM public.audit_log WHERE action='backup.ledger_storage_changed' AND payload::text NOT LIKE '%'||$1||'%'`, secret).Scan(&n); err != nil || n < 1 {
		t.Errorf("no clean audit entry: %d %v", n, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM public.audit_log WHERE payload::text LIKE '%'||$1||'%'`, secret).Scan(&n); err != nil || n != 0 {
		t.Errorf("the credential reached the activity log (%d rows) %v", n, err)
	}
}

func TestIntegration_LedgerAdmin_TestNeedsNoPasswordAndNeverSaves(t *testing.T) {
	f := newAPI(t, "site_admin")
	scd, rec := fakeLedgerSCD(t)
	f.app.scd = scd
	if st, out := f.do(t, "POST", "/backups/ledger/destination/test", map[string]any{"kind": "dir", "path": "/mnt/x"}); st != 200 {
		t.Fatalf("test = %d %v", st, out)
	}
	c := rec.last("/v1/ledger/destination")
	if c == nil || c.Body["test_only"] != true {
		t.Fatalf("a test was not forwarded as a test: %+v", c)
	}
	if st, _ := f.do(t, "POST", "/backups/ledger/destination/probe", map[string]any{"host": "nas", "port": 22}); st != 200 {
		t.Fatalf("probe = %d", st)
	}
	if rec.last("/v1/ledger/destination/probe") == nil {
		t.Fatal("the probe did not reach scd")
	}
}

func TestIntegration_LedgerAdmin_RecoveryAndBackupOnRequestNeedThePassword(t *testing.T) {
	f := newAPI(t, "site_admin")
	scd, rec := fakeLedgerSCD(t)
	f.app.scd = scd
	for _, tc := range []struct{ method, path, scdPath string }{
		{"POST", "/backups/ledger/recover", "/v1/ledger/recover"},
		{"POST", "/backups/daily/run", "/v1/backup/daily/run"},
	} {
		if st, _ := f.do(t, tc.method, tc.path, map[string]any{"identity": "AGE-SECRET-KEY-1X"}); st == 200 {
			t.Errorf("%s was allowed without the password", tc.path)
		}
		if rec.last(tc.scdPath) != nil {
			t.Errorf("%s reached scd without the password", tc.path)
		}
		if st, out := f.do(t, tc.method, tc.path, map[string]any{"identity": "AGE-SECRET-KEY-1X", "password": f.password}); st != 200 {
			t.Errorf("%s = %d %v", tc.path, st, out)
		}
	}
	// recovery forwards the key and nothing else
	if c := rec.last("/v1/ledger/recover"); c == nil || c.Body["identity"] != "AGE-SECRET-KEY-1X" || len(c.Body) != 1 {
		t.Errorf("recover body = %+v", c)
	}
	// the key is never logged
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM public.audit_log WHERE payload::text LIKE '%AGE-SECRET-KEY%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a backup key reached the activity log: %d %v", n, err)
	}
}

func TestIntegration_LedgerAdmin_StatusIsRelayedAsItIs(t *testing.T) {
	f := newAPI(t, "site_admin")
	scd, rec := fakeLedgerSCD(t)
	f.app.scd = scd
	rec.reply["/v1/ledger/status"] = `{"state":"attention","erasure_available":false,"message":"disk gone"}`
	st, out := f.do(t, "GET", "/backups/ledger", nil)
	if st != 200 || out["state"] != "attention" || out["erasure_available"] != false {
		t.Fatalf("status = %d %v", st, out)
	}
	rec.reply["/v1/backup/daily"] = `{"state":"ok","keep":7}`
	if st, out := f.do(t, "GET", "/backups/daily", nil); st != 200 || out["keep"] != float64(7) {
		t.Fatalf("daily = %d %v", st, out)
	}
}

// ---- the ledger itself, in the database ---------------------------------------------------------------------------------

func TestIntegration_Ledger_TheTablesCannotBeChangedAfterTheFact(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE iam_v2.erasure_ledger SET body = '{}' WHERE seq = 1`,
		`UPDATE iam_v2.erasure_ledger SET confirmed = false WHERE seq = 1`,
		`DELETE FROM iam_v2.erasure_ledger WHERE seq = 1`,
		`TRUNCATE iam_v2.erasure_ledger`,
		`UPDATE iam_v2.ledger_meta SET ledger_id = gen_random_uuid()`,
		`DELETE FROM iam_v2.ledger_meta`,
	} {
		if _, err := f.pool.Exec(ctx, q); err == nil {
			t.Errorf("%s was allowed", q)
		} else if !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: unexpected error %v", q, err)
		}
	}
	// the chain is intact
	if got := gdStr(t, f, `SELECT (iam_v2.ledger_verify() ->> 'ok')`); got != "true" {
		t.Errorf("the database ledger fails its own chain check: %s", got)
	}
}

// Two erasures at once serialise: both succeed, the chain has no gap, and both are in both copies.
func TestIntegration_Ledger_ConcurrentErasuresSerialise(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	var accts [4]string
	for i := range accts {
		accts[i], _, _ = seedEndedAccount(t, f, fmt.Sprintf("conc-%d-%s@example.com", i, sfx), 400*24*time.Hour, fmt.Sprintf("10.95.7.%d", 10+i))
	}
	base := ledgerSeq(t, f)
	var wg sync.WaitGroup
	codes := make([]int, len(accts))
	for i := range accts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", accts[i], "guest asked"))
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Errorf("erasure %d = %d", i, c)
		}
	}
	if got := ledgerSeq(t, f); got != base+int64(len(accts)) {
		t.Fatalf("ledger head %d, want %d", got, base+int64(len(accts)))
	}
	if p := f.ledger.ReadLocal(); !p.Intact() || int64(len(p.Parse.Entries)) != base+int64(len(accts)) {
		t.Errorf("the primary copy is not a gapless chain: %+v", p.Parse)
	}
	if info, ok, found, err := f.ledger.ReadExternalHead(context.Background()); err != nil || !ok || !found || info.Seq != base+int64(len(accts)) {
		t.Errorf("the secondary copy is not in step: %+v ok=%v found=%v err=%v", info, ok, found, err)
	}
	if got := gdStr(t, f, `SELECT (iam_v2.ledger_verify() ->> 'ok')`); got != "true" {
		t.Errorf("chain check: %s", got)
	}
}

// ---- privileges: the runtime roles reach the ledger only through its functions ----------------------------------------------

func TestIntegration_Ledger_TheRuntimeRolesReachOnlyTheFunctions(t *testing.T) {
	edsn, sdsn := os.Getenv("EDGED_ROLE_TEST_DSN"), os.Getenv("SCD_ROLE_TEST_DSN")
	if edsn == "" || sdsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN and SCD_ROLE_TEST_DSN are needed for the privilege proof")
	}
	ctx := context.Background()
	open := func(dsn string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	ep, sp := open(edsn), open(sdsn)
	tables := []string{`SELECT 1 FROM iam_v2.erasure_ledger`, `SELECT 1 FROM iam_v2.ledger_meta`, `SELECT 1 FROM public.ledger_destination`,
		`INSERT INTO iam_v2.erasure_ledger(seq) VALUES (999999)`, `UPDATE iam_v2.erasure_ledger SET confirmed = true`}
	for who, p := range map[string]*pgxpool.Pool{"svc_edged": ep, "svc_scd": sp} {
		for _, q := range tables {
			if _, err := p.Exec(ctx, q); err == nil {
				t.Errorf("%s could run: %s", who, q)
			}
		}
	}
	// svc_edged: may confirm what its own transaction appended and read the head; nothing that writes, replays or resets
	for _, q := range []string{
		`SELECT iam_v2.ledger_append(gen_random_uuid(),gen_random_uuid(),'checkpoint','{}'::jsonb)`,
		`SELECT public.ledger_apply('[]'::jsonb, false)`, `SELECT public.ledger_reset()`, `SELECT iam_v2.ledger_export(0)`, `SELECT iam_v2.ledger_verify()`,
		`SELECT public.ledger_destination_get()`, `SELECT public.ledger_destination_secret()`,
		`SELECT public.ledger_destination_set('dir','{}'::jsonb,NULL,NULL,NULL,NULL,'x')`, `SELECT iam_v2.ledger_replay_erase(gen_random_uuid(),gen_random_uuid(),'{}'::jsonb)`,
	} {
		if _, err := ep.Exec(ctx, q); err == nil {
			t.Errorf("svc_edged could run: %s", q)
		}
	}
	for _, q := range []string{`SELECT iam_v2.ledger_pending()`, `SELECT iam_v2.ledger_confirm(ARRAY[]::text[])`, `SELECT iam_v2.ledger_head()`} {
		if _, err := ep.Exec(ctx, q); err != nil {
			t.Errorf("svc_edged cannot run %q: %v", q, err)
		}
	}
	// svc_scd: the reconciliation, the replay and the setting; but the replay helpers and the transition reset are reached only
	// through ledger_apply and the transition itself
	for _, q := range []string{`SELECT iam_v2.ledger_replay_erase(gen_random_uuid(),gen_random_uuid(),'{}'::jsonb)`, `SELECT iam_v2.ledger_append_erasure(gen_random_uuid(),gen_random_uuid(),'ER-000000000000')`} {
		if _, err := sp.Exec(ctx, q); err == nil {
			t.Errorf("svc_scd could run: %s", q)
		}
	}
	for _, q := range []string{`SELECT iam_v2.ledger_head()`, `SELECT iam_v2.ledger_export(0)`, `SELECT iam_v2.ledger_verify()`, `SELECT public.ledger_apply('[]'::jsonb, false)`,
		`SELECT public.ledger_destination_get()`, `SELECT iam_v2.ledger_pending()`} {
		if _, err := sp.Exec(ctx, q); err != nil {
			t.Errorf("svc_scd cannot run %q: %v", q, err)
		}
	}

	// A WHOLE ERASURE AS THE RUNTIME ROLE: the statement, its ledger entry, the commit to both copies, the confirmation.
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "role-" + sfx + "@example.com"
	acct, _, _ := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.95.9.10")
	base := ledgerSeq(t, f)
	srv := &server{db: ep, tenantID: f.tenant, siteID: f.site, ledgerC: f.ledgerC}
	var res map[string]any
	if err := srv.ledgerQuery(ctx, &res, `SELECT public.guest_data_erase($1::uuid,$2::uuid,'account',$3::uuid,'role-test','guest asked')`, f.tenant, f.site, acct); err != nil {
		t.Fatalf("an erasure as svc_edged failed: %v", err)
	}
	if got := ledgerSeq(t, f); got != base+1 {
		t.Errorf("ledger head %d, want %d", got, base+1)
	}
	if p := f.ledger.ReadLocal(); int64(len(p.Parse.Entries)) != base+1 {
		t.Errorf("the primary copy does not hold the erasure")
	}
	if got := gdStr(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != "true" {
		t.Errorf("the account was not erased")
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.erasure_ledger WHERE NOT confirmed`); got != "0" {
		t.Errorf("an entry was left unconfirmed: %s", got)
	}
}
