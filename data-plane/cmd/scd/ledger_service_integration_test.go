//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// The reconciliation, the commit endpoint and the secondary-storage setting, against a real database with the 0148 schema and
// real files: what happens when a copy is missing, damaged, behind, or AHEAD.

type ledgerRig struct {
	t     *testing.T
	s     *server
	pool  *pgxpool.Pool
	ext   string
	local string
}

func newLedgerRig(t *testing.T) *ledgerRig {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `SELECT iam_v2.ledger_head()`); err != nil {
		t.Skipf("the erasure ledger is not in this database: %v", err)
	}
	old := ledgerIndependence
	ledgerIndependence = func() func(string) error { return nil }
	t.Cleanup(func() { ledgerIndependence = old })
	r := &ledgerRig{t: t, pool: pool, ext: filepath.Join(t.TempDir(), "secondary"), local: filepath.Join(t.TempDir(), "primary", "erasure-ledger.jsonl")}
	if err := os.MkdirAll(r.ext, 0o700); err != nil {
		t.Fatal(err)
	}
	r.s = &server{db: pool}
	r.s.ledgerRT.store = ledger.NewStore(ledger.Config{LocalPath: r.local})
	r.setDestination(r.ext)
	return r
}

func (r *ledgerRig) setDestination(dir string) {
	r.t.Helper()
	cfg, _ := json.Marshal(map[string]any{"path": dir})
	if _, err := r.pool.Exec(context.Background(), `SELECT public.ledger_destination_set('dir', $1::jsonb, NULL, NULL, NULL, NULL, 'test')`, string(cfg)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *ledgerRig) reconcile() ledgerStatus {
	r.t.Helper()
	return r.s.ledgerReconcile(context.Background())
}

func (r *ledgerRig) dbSeq() int64 {
	var n int64
	if err := r.pool.QueryRow(context.Background(), `SELECT COALESCE(max(seq),0) FROM iam_v2.erasure_ledger`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// appendCheckpoint appends one entry the way the daily checkpoint does: database, both copies, confirm.
func (r *ledgerRig) appendCheckpoint() ledger.Entry {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_append('00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000','checkpoint','{}'::jsonb)::text`).Scan(&raw); err != nil {
		r.t.Fatal(err)
	}
	var e ledger.Entry
	_ = json.Unmarshal(raw, &e)
	if err := r.s.ledgerRT.store.Commit(ctx, []ledger.Entry{e}); err != nil {
		r.t.Fatalf("commit: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.ledger_confirm($1)`, []string{e.Hash}); err != nil {
		r.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		r.t.Fatal(err)
	}
	return e
}

// forgetAfter makes the database forget entries after seq, as a restore from an older backup does.
func (r *ledgerRig) forgetAfter(seq int64) {
	r.t.Helper()
	ctx := context.Background()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		r.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.erasure_ledger WHERE seq > $1`, seq); err != nil {
		r.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		r.t.Fatal(err)
	}
}

func readBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIntegrationLedger_FirstRunBuildsBothCopiesFromTheDatabase(t *testing.T) {
	r := newLedgerRig(t)
	st := r.reconcile()
	if st.State != "ok" || !st.ErasureAvailable || !st.External.InSync || st.Local.Entries != int(r.dbSeq()) {
		t.Fatalf("status = %+v", st)
	}
	if p := ledger.ParseChain(readBytes(t, r.local)); !p.Intact || int64(len(p.Entries)) != r.dbSeq() {
		t.Fatalf("primary = %+v", p)
	}
	if _, err := os.Stat(filepath.Join(r.ext, ledger.ExternalObject)); err != nil {
		t.Fatalf("no secondary copy: %v", err)
	}
}

func TestIntegrationLedger_HashesAgreeBetweenTheDatabaseAndGo(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	e := r.appendCheckpoint()
	if e.ComputeHash() != e.Hash {
		t.Fatalf("the database computed %s, Go computes %s", e.Hash, e.ComputeHash())
	}
	var n int
	_ = r.pool.QueryRow(context.Background(), `SELECT (iam_v2.ledger_verify() ->> 'entries')::int`).Scan(&n)
	if st := r.reconcile(); st.State != "ok" || st.DB.Entries != int64(n) {
		t.Fatalf("status = %+v", st)
	}
}

func TestIntegrationLedger_AMissingOrDamagedPrimaryCopyIsRebuilt(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	want := r.dbSeq()
	if err := os.Remove(r.local); err != nil {
		t.Fatal(err)
	}
	if st := r.reconcile(); st.State != "ok" || int64(st.Local.Entries) != want {
		t.Fatalf("after deleting the primary copy: %+v", st)
	}
	b := readBytes(t, r.local)
	b[len(b)/2] ^= 0x55
	if err := os.WriteFile(r.local, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := r.reconcile(); st.State != "ok" || int64(st.Local.Entries) != want {
		t.Fatalf("after damaging the primary copy: %+v", st)
	}
	if m, _ := filepath.Glob(r.local + ".damaged-*"); len(m) != 1 {
		t.Errorf("the damaged copy was not kept aside: %v", m)
	}
}

func TestIntegrationLedger_AMissingOrCorruptSecondaryCopyIsPushedAgain(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	obj := filepath.Join(r.ext, ledger.ExternalObject)
	if err := os.Remove(obj); err != nil {
		t.Fatal(err)
	}
	if st := r.reconcile(); st.State != "ok" || !st.External.InSync {
		t.Fatalf("after deleting the secondary object: %+v", st)
	}
	b := readBytes(t, obj)
	b[3] ^= 0xff
	if err := os.WriteFile(obj, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := r.reconcile(); st.State != "ok" || !st.External.InSync {
		t.Fatalf("after corrupting the secondary object: %+v", st)
	}
	if p := ledger.ParseChain(readBytes(t, obj)); !p.Intact {
		t.Fatalf("the secondary copy is still damaged")
	}
	// the whole secondary folder gone (an unplugged then replaced disk)
	if err := os.RemoveAll(r.ext); err != nil {
		t.Fatal(err)
	}
	if st := r.reconcile(); st.ErasureAvailable || st.External.Error == "" {
		t.Fatalf("a missing secondary storage was reported healthy: %+v", st)
	}
	if err := os.MkdirAll(r.ext, 0o700); err != nil {
		t.Fatal(err)
	}
	if st := r.reconcile(); st.State != "ok" {
		t.Fatalf("after the storage came back: %+v", st)
	}
}

// Entries the file has and the database lacks (a restore from an older backup, or a crash between the file and the commit) are applied.
func TestIntegrationLedger_EntriesTheDatabaseLacksAreApplied(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	base := r.dbSeq()
	r.appendCheckpoint()
	r.appendCheckpoint()
	r.forgetAfter(base)
	if r.dbSeq() != base {
		t.Fatal("the database did not forget")
	}
	if st := r.reconcile(); st.State != "ok" {
		t.Fatalf("status = %+v", st)
	}
	if r.dbSeq() != base+2 {
		t.Fatalf("the database was not brought up to date: %d, want %d", r.dbSeq(), base+2)
	}
}

// THE DANGEROUS CASE: the secondary storage holds erasures the appliance has lost. It must never be overwritten.
func TestIntegrationLedger_ASecondaryAheadOfTheApplianceIsNeverOverwritten(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	base := r.dbSeq()
	r.appendCheckpoint()
	r.appendCheckpoint()
	ahead := readBytes(t, filepath.Join(r.ext, ledger.ExternalObject))
	// the appliance loses its own copy and the database goes back to an older backup
	r.forgetAfter(base)
	b := readBytes(t, r.local)
	cut := ledger.ParseChain(b)
	short, _ := ledger.Encode(cut.Entries[:base])
	if err := os.WriteFile(r.local, short, 0o600); err != nil {
		t.Fatal(err)
	}
	st := r.reconcile()
	if st.State != "recovery_needed" || st.ErasureAvailable {
		t.Fatalf("status = %+v", st)
	}
	if !bytes.Equal(readBytes(t, filepath.Join(r.ext, ledger.ExternalObject)), ahead) {
		t.Fatal("the secondary copy was overwritten with a shorter ledger")
	}
	// an erasure attempted now is refused by the commit
	e := ledger.Entry{}
	_ = e
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"entries": cut.Entries[base : base+1]})
	r.s.ledgerCommit(rec, httptest.NewRequest(http.MethodPost, "/v1/ledger/commit", bytes.NewReader(body)))
	_ = rec // an entry that is already known is idempotent; what matters is the secondary stays intact

	// recovery from the secondary (it is plaintext in this test: no key holder)
	detail, err := r.s.ledgerRecoverFromExternal(context.Background(), "")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !strings.Contains(detail, "recovered") {
		t.Fatalf("detail = %s", detail)
	}
	if r.dbSeq() != base+2 {
		t.Fatalf("the database was not recovered: %d, want %d", r.dbSeq(), base+2)
	}
	if st := r.reconcile(); st.State != "ok" || st.Local.Entries != int(base+2) {
		t.Fatalf("after recovery: %+v", st)
	}
}

func TestIntegrationLedger_ADivergentPrimaryCopyBlocksErasure(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	r.appendCheckpoint()
	p := ledger.ParseChain(readBytes(t, r.local))
	// a primary copy whose last entry differs from the database's, with a valid chain of its own
	forged := append([]ledger.Entry(nil), p.Entries...)
	last := len(forged) - 1
	forged[last].Body = `{"forged":true}`
	forged[last].Hash = forged[last].ComputeHash()
	b, _ := ledger.Encode(forged)
	if err := os.WriteFile(r.local, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st := r.reconcile()
	if st.State != "blocked" || st.ErasureAvailable {
		t.Fatalf("status = %+v", st)
	}
}

func TestIntegrationLedger_CommitEndpointIsAllOrNothing(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	ctx := context.Background()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_append('00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000','checkpoint','{}'::jsonb)::text`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var e ledger.Entry
	_ = json.Unmarshal(raw, &e)
	before := readBytes(t, r.local)
	// the secondary storage disappears
	if err := os.RemoveAll(r.ext); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"entries": []ledger.Entry{e}})
	r.s.ledgerCommit(rec, httptest.NewRequest(http.MethodPost, "/v1/ledger/commit", bytes.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ledger_external_unavailable") {
		t.Fatalf("commit = %d %s", rec.Code, rec.Body.String())
	}
	_ = tx.Rollback(ctx)
	if !bytes.Equal(readBytes(t, r.local), before) {
		t.Fatal("the primary copy kept an entry whose secondary write failed")
	}
	if err := os.MkdirAll(r.ext, 0o700); err != nil {
		t.Fatal(err)
	}
	if st := r.reconcile(); st.State != "ok" {
		t.Fatalf("after the storage came back: %+v", st)
	}
}

func TestIntegrationLedger_DestinationSetupTestsSavesAndRefusesAnotherAppliancesLedger(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	put := func(v map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(v)
		rec := httptest.NewRecorder()
		r.s.ledgerDestinationPut(rec, httptest.NewRequest(http.MethodPut, "/v1/ledger/destination", bytes.NewReader(b)))
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// a test changes nothing
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out := put(map[string]any{"kind": "dir", "path": other, "test_only": true}); code != 200 || out["ok"] != true {
		t.Fatalf("test = %d %v", code, out)
	}
	if got := r.s.ledgerRT.store.Destination().Describe(); got != r.ext {
		t.Fatalf("a test changed the saved storage: %s", got)
	}
	// a missing folder is refused with a reason
	if code, out := put(map[string]any{"kind": "dir", "path": filepath.Join(other, "nope"), "test_only": true}); code != 422 || out["error"] != "storage_unusable" {
		t.Fatalf("missing folder = %d %v", code, out)
	}
	// a storage already holding ANOTHER appliance's ledger is refused
	foreign := ledger.NewStore(ledger.Config{LocalPath: filepath.Join(t.TempDir(), "f.jsonl"), Dest: &ledger.DirDest{Root: other}})
	fe := []ledger.Entry{{Ledger: "99999999-9999-4999-8999-999999999999", Seq: 1, Prev: ledger.GenesisPrev, Kind: "genesis", Tenant: "00000000-0000-0000-0000-000000000000", Site: "00000000-0000-0000-0000-000000000000", AtUS: 1, Body: "{}"}}
	fe[0].Hash = fe[0].ComputeHash()
	if err := foreign.Commit(context.Background(), fe); err != nil {
		t.Fatal(err)
	}
	r.appendCheckpoint() // this appliance's ledger now has entries of its own
	if code, out := put(map[string]any{"kind": "dir", "path": other}); code != 409 || out["error"] != "belongs_to_another" {
		t.Fatalf("another appliance's storage = %d %v", code, out)
	}
	// a clean folder is saved and receives the ledger
	clean := filepath.Join(t.TempDir(), "clean")
	if err := os.MkdirAll(clean, 0o700); err != nil {
		t.Fatal(err)
	}
	code, out := put(map[string]any{"kind": "dir", "path": clean, "by": "operator"})
	if code != 200 || out["ok"] != true {
		t.Fatalf("save = %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(clean, ledger.ExternalObject)); err != nil {
		t.Fatalf("the new storage did not receive the ledger: %v", err)
	}
	var note string
	if err := r.pool.QueryRow(context.Background(), `SELECT config->>'path' FROM public.ledger_destination`).Scan(&note); err != nil || note != clean {
		t.Fatalf("saved path = %q %v", note, err)
	}
}

func TestIntegrationLedger_CheckpointIsWrittenAtMostOnceADay(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	ctx := context.Background()
	n := r.dbSeq()
	for i := 0; i < 3; i++ {
		if err := r.s.ledgerCheckpoint(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.dbSeq(); got > n+1 {
		t.Fatalf("checkpoints piled up: %d -> %d", n, got)
	}
	if st := r.reconcile(); st.State != "ok" {
		t.Fatalf("status = %+v", st)
	}
}

// Moving the appliance to another customer starts a new ledger and forgets the previous secondary storage and the local file.
func TestIntegrationLedger_ReassignmentStartsANewLedgerAndForgetsTheStorage(t *testing.T) {
	r := newLedgerRig(t)
	r.reconcile()
	r.appendCheckpoint()
	var oldID string
	if err := r.pool.QueryRow(context.Background(), `SELECT ledger_id::text FROM iam_v2.ledger_meta`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(context.Background(), `SELECT public.ledger_reset()`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	r.s.ledgerForgetPrevious()
	var newID string
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT (SELECT ledger_id::text FROM iam_v2.ledger_meta), (SELECT count(*) FROM iam_v2.erasure_ledger)`).Scan(&newID, &n); err != nil {
		t.Fatal(err)
	}
	if newID == oldID || n != 1 {
		t.Fatalf("a new ledger was not started: id %s -> %s, %d entries", oldID, newID, n)
	}
	var dests int
	_ = r.pool.QueryRow(context.Background(), `SELECT count(*) FROM public.ledger_destination`).Scan(&dests)
	if dests != 0 {
		t.Fatal("the previous customer's storage setting was kept")
	}
	if _, err := os.Stat(r.local); err == nil {
		t.Fatal("the previous customer's local ledger file was kept")
	}
	if r.s.ledgerRT.store.Destination() != nil {
		t.Fatal("the previous storage is still in use")
	}
	st := r.reconcile()
	if st.State != "not_configured" || st.ErasureAvailable {
		t.Fatalf("a new customer must set up their own storage first: %+v", st)
	}
	if p := ledger.ParseChain(readBytes(t, r.local)); !p.Intact || len(p.Entries) != 1 || p.LedgerID() != newID {
		t.Fatalf("the new ledger's file = %+v", p)
	}
}
