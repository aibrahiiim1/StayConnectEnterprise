//go:build integration

package main

// THE RESTORE DRILL (GDPR final mission): the real restore code, against a real PostgreSQL in a container named
// stayconnect-pg (the name restore.go uses), with real files for both ledger copies and a real encrypted secondary copy.
// It is skipped unless LEDGER_DRILL_DSN points at that database (scripts/ledger-restore-drill.sh builds it and runs this).
//
// Every scenario is the same story: a backup is taken; afterwards a guest is erased and a request is answered; then the
// backup is restored. The question each time is what the restored appliance serves.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

type drill struct {
	t       *testing.T
	s       *server
	dsn     string
	pool    *pgxpool.Pool
	ext     string
	local   string
	id      *age.X25519Identity
	tenant  string
	site    string
	stamp   int
	accts   map[string]string // label -> account id
	emails  map[string]string // label -> username
	lastRes map[string]any
}

func newDrill(t *testing.T) *drill {
	t.Helper()
	dsn := os.Getenv("LEDGER_DRILL_DSN")
	if dsn == "" {
		t.Skip("LEDGER_DRILL_DSN not set")
	}
	if out, err := exec.Command("docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc", "select 1").CombinedOutput(); err != nil {
		t.Skipf("no database container %s: %s", pgContainer, out)
	}
	dir := t.TempDir()
	oldBackup, oldMaint, oldResult, oldInd, oldEtc := scdBackupDir, maintenanceFile, restoreResultFile, ledgerIndependence, etcStayconnectDir
	scdBackupDir = filepath.Join(dir, "backups")
	maintenanceFile = filepath.Join(dir, "maintenance")
	restoreResultFile = filepath.Join(dir, "last-restore.json")
	ledgerIndependence = func() func(string) error { return nil }
	etcStayconnectDir = filepath.Join(dir, "etc")
	oldStop, oldState, oldAdvance := stopService, serviceState, advanceMarker
	stopService = func(context.Context, string) error { return nil }
	serviceState = func(context.Context, string) string { return "inactive" }
	advanceMarker = func(_ context.Context, gen int64, sha string) error { return writeMarker(gen, sha) }
	t.Cleanup(func() {
		scdBackupDir, maintenanceFile, restoreResultFile, ledgerIndependence, etcStayconnectDir = oldBackup, oldMaint, oldResult, oldInd, oldEtc
		stopService, serviceState, advanceMarker = oldStop, oldState, oldAdvance
	})
	if err := os.MkdirAll(scdBackupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	id, _ := age.GenerateX25519Identity()
	d := &drill{t: t, dsn: dsn, pool: pool, ext: filepath.Join(dir, "secondary"), local: filepath.Join(dir, "primary", "erasure-ledger.jsonl"),
		id: id, accts: map[string]string{}, emails: map[string]string{}}
	if err := os.MkdirAll(d.ext, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `WITH t AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 's' FROM t, gen_random_uuid() g RETURNING id, tenant_id)
	  SELECT (SELECT tenant_id FROM si)::text, (SELECT id FROM si)::text`).Scan(&d.tenant, &d.site); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	srvPool := pool
	if rd := os.Getenv("SCD_ROLE_TEST_DSN"); rd != "" {
		// scd connects as its real runtime role, so what only a superuser may do fails here and not on an appliance
		rp, err := pgxpool.New(ctx, rd)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(rp.Close)
		srvPool = rp
	}
	d.s = &server{db: srvPool, tenID: d.tenant, siteID: d.site}
	d.s.ledgerRT.store = ledger.NewStore(ledger.Config{LocalPath: d.local, Cipher: func(context.Context) (*ledger.Cipher, error) {
		return &ledger.Cipher{Recipients: []age.Recipient{id.Recipient()}, Fingerprint: "drill"}, nil
	}})
	d.setDestination(d.ext)
	if st := d.s.ledgerReconcile(ctx); st.State != "ok" {
		t.Fatalf("the drill's ledger is not healthy to begin with: %+v", st)
	}
	return d
}

func (d *drill) setDestination(dir string) {
	cfg, _ := json.Marshal(map[string]any{"path": dir})
	if _, err := d.pool.Exec(context.Background(), `SELECT public.ledger_destination_set('dir', $1::jsonb, NULL, NULL, NULL, NULL, 'drill')`, string(cfg)); err != nil {
		d.t.Fatal(err)
	}
}

// live opens a fresh connection (the pool may still hold connections to the renamed-aside database).
func (d *drill) live() *pgx.Conn {
	c, err := pgx.Connect(context.Background(), d.dsn)
	if err != nil {
		d.t.Fatal(err)
	}
	d.t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func (d *drill) str(sql string, args ...any) string {
	var v string
	if err := d.live().QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (d *drill) seedAccount(label string) {
	d.stamp++
	email := fmt.Sprintf("drill-%s-%d-%d@example.com", label, time.Now().UnixNano(), d.stamp)
	var id string
	if err := d.live().QueryRow(context.Background(), `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, display_name, notes, created_at, self_registered)
		VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash','Drill Guest','room 101 guest', now() - interval '1 day', true) RETURNING id::text`,
		d.tenant, d.site, email).Scan(&id); err != nil {
		d.t.Fatalf("seed account: %v", err)
	}
	d.accts[label], d.emails[label] = id, email
}

func (d *drill) username(label string) string {
	return d.str(`SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, d.accts[label])
}

func (d *drill) erased(label string) bool { return strings.HasPrefix(d.username(label), "anon-") }

// withLedger runs fn in a transaction and commits it only after the new ledger entries are durable in both copies: edged's
// ledgerQuery, step for step.
func (d *drill) withLedger(fn func(tx pgx.Tx) error) error {
	ctx := context.Background()
	if st := d.s.ledgerReconcile(ctx); !st.ErasureAvailable {
		return fmt.Errorf("the ledger is not available: %s", st.Message)
	}
	conn := d.live()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_pending()::text`).Scan(&raw); err != nil {
		return err
	}
	var entries []ledger.Entry
	_ = json.Unmarshal(raw, &entries)
	if err := d.s.ledgerRT.store.Commit(ctx, entries); err != nil {
		return err
	}
	hashes := make([]string, len(entries))
	for i, e := range entries {
		hashes[i] = e.Hash
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.ledger_confirm($1)`, hashes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (d *drill) erase(label string) string {
	var ref string
	err := d.withLedger(func(tx pgx.Tx) error {
		var res map[string]any
		if err := tx.QueryRow(context.Background(), `SELECT public.guest_data_erase($1::uuid,$2::uuid,'account',$3::uuid,'drill','guest asked')`,
			d.tenant, d.site, d.accts[label]).Scan(&res); err != nil {
			return err
		}
		ref, _ = res["erasure_ref"].(string)
		return nil
	})
	if err != nil {
		d.t.Fatalf("erase %s: %v", label, err)
	}
	return ref
}

func (d *drill) openRequest(who string) (id, ref string) {
	var res map[string]any
	if err := d.live().QueryRow(context.Background(), `SELECT iam_v2.request_register_open($1::uuid,$2::uuid,'drill','ERASURE',current_date - 2,$3,'')`,
		d.tenant, d.site, who).Scan(&res); err != nil {
		d.t.Fatalf("open request: %v", err)
	}
	return res["id"].(string), res["ref"].(string)
}

func (d *drill) answerRequest(id string) {
	err := d.withLedger(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `SELECT iam_v2.request_register_answer($1::uuid,$2::uuid,$3::uuid,'drill','PARTLY_REFUSED','LEGAL_RETENTION','','','')`, d.tenant, d.site, id)
		return err
	})
	if err != nil {
		d.t.Fatalf("answer request: %v", err)
	}
}

func (d *drill) requestState(id string) string {
	return d.str(`SELECT status||'/'||requester_ref FROM iam_v2.guest_data_request_register WHERE id=$1`, id)
}

// takeBackup makes a backup of the live database the way the appliance does, marks it verified, and writes the note that
// records the ledger head it contains.
func (d *drill) takeBackup() string {
	d.t.Helper()
	time.Sleep(1100 * time.Millisecond) // backup names carry the second they were taken
	// the same engine as every other backup; the drill key is a recipient through the identity, as a restoring key holder's is
	meta, err := d.s.takeBackup(context.Background(), backupOpts{Kind: kindManual, By: "drill", Identity: d.id.String()})
	if err != nil {
		d.t.Fatalf("backup: %v", err)
	}
	if !meta.Restorable || meta.LedgerSeq == 0 {
		d.t.Fatalf("backup note is missing its proof or the ledger head: %+v", meta)
	}
	return meta.Name
}

// restore runs the real restore and returns its recorded outcome.
func (d *drill) restore(name, identity string) map[string]any {
	d.t.Helper()
	rec, err := readVerifiedMarker(name)
	if err != nil {
		d.t.Fatalf("marker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	d.s.runRestore(ctx, name, identity, "drill", rec, time.Now().UTC())
	b, err := os.ReadFile(restoreResultFile)
	if err != nil {
		d.t.Fatalf("no restore result: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal(b, &res)
	d.lastRes = res
	if _, err := os.Stat(maintenanceFile); err == nil {
		d.t.Errorf("the appliance was left in maintenance")
	}
	return res
}

func stepOK(res map[string]any, step string) (ok, found bool, detail string) {
	steps, _ := res["steps"].([]any)
	for _, s := range steps {
		m := s.(map[string]any)
		if m["step"] == step {
			ok, _ = m["ok"].(bool)
			detail, _ = m["detail"].(string)
			found = true
		}
	}
	return
}

func (d *drill) stagingFromBackup(name, db string) {
	d.t.Helper()
	ctx := context.Background()
	if out, err := exec.Command("docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc", "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)").CombinedOutput(); err != nil {
		d.t.Fatalf("drop: %s", out)
	}
	if out, err := exec.Command("docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc", "CREATE DATABASE "+db).CombinedOutput(); err != nil {
		d.t.Fatalf("create: %s", out)
	}
	if out, err := loadBackupInto(ctx, filepath.Join(scdBackupDir, name), d.identity(), db); err != nil {
		d.t.Fatalf("load: %v %s", err, tailLine(string(out)))
	}
	d.t.Cleanup(func() {
		_ = exec.Command("docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc", "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)").Run()
	})
}

func (d *drill) stagingUsername(db, label string) string {
	out, err := execPsql(context.Background(), db, fmt.Sprintf(`SELECT username FROM iam_v2.guest_access_accounts WHERE id='%s'`, d.accts[label]))
	if err != nil {
		d.t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func (d *drill) identity() string { return d.id.String() }

// ---- the scenarios ---------------------------------------------------------------------------------------------------------

// BOTH COPIES AVAILABLE. An older backup is restored; the erasure and the answered request made after it are applied to the
// restored data before it is served, and the proof the guest was given is back.
func TestDrill_RestoreFromAnOlderBackupWithBothLedgerCopies(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	d.seedAccount("b")
	reqID, reqRef := d.openRequest("Drill Requester")
	backup := d.takeBackup() // the backup knows neither the erasure nor the answer
	ref := d.erase("a")
	d.answerRequest(reqID)
	if d.username("b") == "" || !d.erased("a") {
		t.Fatal("setup")
	}
	headBefore := d.str(`SELECT (iam_v2.ledger_head() ->> 'seq')`)

	res := d.restore(backup, d.identity())
	if res["ok"] != true {
		t.Fatalf("the restore failed: %v", res)
	}
	if !d.erased("a") {
		t.Errorf("RESTORED DATA BROUGHT AN ERASED GUEST BACK: %s", d.username("a"))
	}
	if d.erased("b") {
		t.Errorf("the restore erased a guest who was never erased")
	}
	if got := d.requestState(reqID); got != "PARTLY_REFUSED/[removed]" {
		t.Errorf("the answered request %s is %s after the restore", reqRef, got)
	}
	if got := d.str(`SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE erasure_ref=$1`, ref); got != "1" {
		t.Errorf("the erasure proof %s was not recovered", ref)
	}
	if got := d.str(`SELECT (iam_v2.ledger_head() ->> 'seq')`); got != headBefore {
		t.Errorf("the ledger after the restore stops at %s, before it was %s", got, headBefore)
	}
	if ok, found, detail := stepOK(res, "ledger"); !ok || !found || !strings.Contains(detail, "before it went live") {
		t.Errorf("ledger step = %v %v %q", ok, found, detail)
	}
	if ok, _, detail := stepOK(res, "ledger_synced"); !ok {
		t.Errorf("both copies are not in step after the restore: %s", detail)
	}
	if st := d.s.ledgerReconcile(context.Background()); st.State != "ok" || !st.External.InSync {
		t.Errorf("status after the restore: %+v", st)
	}
	// the destination setting survived the swap
	if got := d.str(`SELECT config->>'path' FROM public.ledger_destination`); got != d.ext {
		t.Errorf("the secondary storage setting did not survive the restore: %q", got)
	}
	d.assertFinancialGuarantees(res, 1)
}

// assertFinancialGuarantees: what the retired financial-restore script guaranteed, now part of every restore. gen is the
// marker generation this restore must have advanced to.
func (d *drill) assertFinancialGuarantees(res map[string]any, gen int64) {
	d.t.Helper()
	for _, step := range []string{"quiesce", "financial_marker", "hypertables", "financial_recovery"} {
		if ok, found, detail := stepOK(res, step); !found || !ok {
			d.t.Errorf("restore step %s: found=%v ok=%v %s", step, found, ok, detail)
		}
	}
	if got, err := readMarkerGeneration(); err != nil || got != gen {
		d.t.Errorf("the restore marker is at generation %d (%v), want %d", got, err, gen)
	}
	if got := d.str(`SELECT coalesce(max(restore_generation),-1)::text FROM iam_v2.financial_epochs WHERE tenant_id=$1 AND site_id=$2`, d.tenant, d.site); got != fmt.Sprint(gen) {
		d.t.Errorf("the database records restore generation %s, want %d", got, gen)
	}
	// money movement is HELD: the newest financial epoch is open (not released)
	if got := d.str(`SELECT (released_at IS NULL)::text FROM iam_v2.financial_epochs WHERE tenant_id=$1 AND site_id=$2 ORDER BY epoch DESC LIMIT 1`, d.tenant, d.site); got != "true" {
		d.t.Errorf("the site is not in financial recovery mode after the restore")
	}
	// the database-level grant Gate-P gives iam_v2_owner is still there (a dump cannot carry it)
	if got := d.str(`SELECT has_database_privilege('iam_v2_owner', current_database(), 'CREATE')::text`); got != "true" {
		d.t.Errorf("GRANT CREATE ON DATABASE was lost in the restore")
	}
}

// LOCAL ONLY: the secondary storage is unplugged. The restore proceeds on the primary copy, because the backup and the live
// database vouch for its completeness, and the secondary is rebuilt when it returns.
func TestDrill_RestoreWithTheSecondaryStorageUnavailable(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	backup := d.takeBackup()
	d.erase("a")
	if err := os.Rename(d.ext, d.ext+".unplugged"); err != nil {
		t.Fatal(err)
	}
	res := d.restore(backup, d.identity())
	if res["ok"] != true {
		t.Fatalf("a restore needs only one ledger copy: %v", res)
	}
	if !d.erased("a") {
		t.Errorf("the erased guest came back")
	}
	if ok, found, detail := stepOK(res, "ledger_synced"); found && ok {
		t.Errorf("the missing secondary was reported in step: %s", detail)
	} else if !found {
		t.Errorf("the missing secondary storage was not reported")
	}
	// the disk comes back: the secondary copy is rebuilt and synchronised with no manual file handling
	if err := os.Rename(d.ext+".unplugged", d.ext); err != nil {
		t.Fatal(err)
	}
	if st := d.s.ledgerReconcile(context.Background()); st.State != "ok" || !st.External.InSync {
		t.Fatalf("the secondary copy was not synchronised: %+v", st)
	}
}

// EXTERNAL ONLY: the appliance's own copy is gone and the staging data comes from an older backup. One verified secondary copy
// is enough, but only with a key holder's key, because it is encrypted.
func TestDrill_ExternalCopyAloneIsEnoughAndNeedsAKeyHolder(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	backup := d.takeBackup()
	d.erase("a")
	d.stagingFromBackup(backup, "restore_drill_ext")
	if got := d.stagingUsername("restore_drill_ext", "a"); strings.HasPrefix(got, "anon-") {
		t.Fatal("the staging copy was already erased")
	}
	if err := os.Remove(d.local); err != nil {
		t.Fatal(err)
	}
	if detail, ok := d.s.restoreLedgerApply(context.Background(), "restore_drill_ext", ""); ok {
		t.Fatalf("an encrypted secondary copy was read without a key: %s", detail)
	}
	if got := d.stagingUsername("restore_drill_ext", "a"); strings.HasPrefix(got, "anon-") {
		t.Fatal("a refused ledger still changed the staging data")
	}
	detail, ok := d.s.restoreLedgerApply(context.Background(), "restore_drill_ext", d.identity())
	if !ok {
		t.Fatalf("the secondary copy alone was refused: %s", detail)
	}
	if got := d.stagingUsername("restore_drill_ext", "a"); !strings.HasPrefix(got, "anon-") {
		t.Errorf("the erasure was not applied from the secondary copy: %s", got)
	}
	if !strings.Contains(detail, "external copy") {
		t.Errorf("detail = %s", detail)
	}
}

// INCONSISTENT COPIES: they disagree about an entry they both have. Nothing is guessed: the restore is refused and the live
// database is left as it was.
func TestDrill_ForkedLedgerCopiesRefuseTheRestoreAndLeaveTheLiveDataAlone(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	d.seedAccount("b")
	backup := d.takeBackup()
	d.erase("a")
	// the secondary now holds a chain that differs from the primary at the last entry
	p := ledger.ParseChain(readBytes(t, d.local))
	forged := append([]ledger.Entry(nil), p.Entries...)
	last := len(forged) - 1
	forged[last].Body = `{"erasure_ref":"ER-000000000000","records":[]}`
	forged[last].Hash = forged[last].ComputeHash()
	fdir := filepath.Join(t.TempDir(), "forged.jsonl")
	fs := ledger.NewStore(ledger.Config{LocalPath: fdir, Dest: d.s.ledgerRT.store.Destination(), Cipher: d.s.ledgerRT.store.Cipher})
	if err := fs.RewriteLocal(forged); err != nil {
		t.Fatal(err)
	}
	if err := fs.ForceExternal(context.Background()); err != nil {
		t.Fatal(err)
	}
	res := d.restore(backup, d.identity())
	if res["ok"] == true {
		t.Fatalf("a restore went ahead on forked ledger copies: %v", res)
	}
	if ok, found, detail := stepOK(res, "ledger"); ok || !found || !strings.Contains(detail, "forked") && !strings.Contains(detail, "disagree") && !strings.Contains(detail, "contradicts") {
		t.Errorf("ledger step = %v %v %q", ok, found, detail)
	}
	if !d.erased("a") || d.erased("b") {
		t.Errorf("the live data changed although the restore was refused")
	}
	if got := d.str(`SELECT count(*)::text FROM pg_database WHERE datname LIKE 'restore\_%'`); got != "0" {
		t.Errorf("a staging database was left behind: %s", got)
	}
}

// NEITHER COPY TRUSTWORTHY: the primary is damaged and the secondary is unreachable. The restore stops with nothing replaced.
func TestDrill_NoTrustworthyCopyStopsTheRestore(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	backup := d.takeBackup()
	d.erase("a")
	d.stagingFromBackup(backup, "restore_drill_none")
	b := readBytes(t, d.local)
	b[len(b)/2] ^= 0x55
	if err := os.WriteFile(d.local, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(d.ext, d.ext+".gone"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(d.ext+".gone", d.ext)
	detail, ok := d.s.restoreLedgerApply(context.Background(), "restore_drill_none", d.identity())
	if ok || !strings.Contains(detail, "no complete, trustworthy copy") {
		t.Fatalf("detail = %q ok=%v", detail, ok)
	}
	if got := d.stagingUsername("restore_drill_none", "a"); strings.HasPrefix(got, "anon-") {
		t.Errorf("a refused ledger changed the staging data")
	}
}

// AN INTERRUPTED WRITE: the primary ends in half a line. It is still a good ledger up to there, and the half line (never
// acknowledged to anyone) is ignored.
func TestDrill_AnInterruptedWriteDoesNotInvalidateTheLedger(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	backup := d.takeBackup()
	d.erase("a")
	d.stagingFromBackup(backup, "restore_drill_torn")
	f, err := os.OpenFile(d.local, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"ledger":"x","seq":99,"pre`)
	f.Close()
	if err := os.Rename(d.ext, d.ext+".gone"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(d.ext+".gone", d.ext)
	detail, ok := d.s.restoreLedgerApply(context.Background(), "restore_drill_torn", "")
	if !ok {
		t.Fatalf("an unfinished last line stopped the restore: %s", detail)
	}
	if got := d.stagingUsername("restore_drill_torn", "a"); !strings.HasPrefix(got, "anon-") {
		t.Errorf("the erasure was not applied")
	}
}

// A COPY CUT SHORT is not mistaken for a complete one: the newest backup records how far the ledger had gone.
func TestDrill_ACopyCutShortIsRefusedAgainstTheNewestBackup(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	older := d.takeBackup()
	d.erase("a")
	d.seedAccount("b")
	d.takeBackup() // the newest backup contains the erasure of a: its note says the ledger reached that entry
	d.stagingFromBackup(older, "restore_drill_cut")
	p := ledger.ParseChain(readBytes(t, d.local))
	short, _ := ledger.Encode(p.Entries[:len(p.Entries)-1])
	if err := os.WriteFile(d.local, short, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(d.ext, d.ext+".gone"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(d.ext+".gone", d.ext)
	detail, ok := d.s.restoreLedgerApply(context.Background(), "restore_drill_cut", "")
	if ok || !strings.Contains(detail, "stops at entry") {
		t.Fatalf("a truncated copy was accepted: %q ok=%v", detail, ok)
	}
}

// A BACKUP OLDER THAN THE LEDGER cannot be replayed onto, so it is refused once the ledger holds erasures.
func TestDrill_ABackupOlderThanTheLedgerIsRefusedWhenErasuresExist(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	backup := d.takeBackup()
	d.erase("a")
	d.stagingFromBackup(backup, "restore_drill_old")
	if _, err := execPsql(context.Background(), "restore_drill_old", `DROP FUNCTION iam_v2.ledger_head()`); err != nil {
		t.Fatal(err)
	}
	detail, ok := d.s.restoreLedgerApply(context.Background(), "restore_drill_old", d.identity())
	if ok || !strings.Contains(detail, "older than the erasure ledger") {
		t.Fatalf("detail = %q ok=%v", detail, ok)
	}
}

// A REPLACEMENT APPLIANCE: it has a new, empty ledger of its own; the old appliance's own file is gone; the secondary storage holds
// the old ledger. One verified copy (the secondary) is enough, and a restore of the old backup applies every erasure made since.
func TestDrill_ReplacementApplianceRestoresFromTheSecondaryCopyAlone(t *testing.T) {
	d := newDrill(t)
	d.seedAccount("a")
	d.seedAccount("b")
	backup := d.takeBackup()
	ref := d.erase("a")
	oldHead := d.str(`SELECT (iam_v2.ledger_head() ->> 'seq')`)
	oldID := d.str(`SELECT (iam_v2.ledger_head() ->> 'ledger')`)
	// the new appliance: its own ledger (and no storage setting, no file); the same storage is then set up on it
	if _, err := d.live().Exec(context.Background(), `SELECT public.ledger_reset()`); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(d.local); err != nil {
		t.Fatal(err)
	}
	d.setDestination(d.ext)
	st := d.s.ledgerReconcile(context.Background())
	if st.State != "recovery_needed" || st.ErasureAvailable {
		t.Fatalf("a new appliance that finds an earlier appliance's ledger must say recovery is needed: %+v", st)
	}
	res := d.restore(backup, d.identity())
	if res["ok"] != true {
		t.Fatalf("the replacement restore failed: %v", res)
	}
	if !d.erased("a") || d.erased("b") {
		t.Errorf("after the replacement restore a=%q b=%q", d.username("a"), d.username("b"))
	}
	if got := d.str(`SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE erasure_ref=$1`, ref); got != "1" {
		t.Errorf("the erasure proof %s did not come back", ref)
	}
	if got := d.str(`SELECT (iam_v2.ledger_head() ->> 'ledger')`); got != oldID {
		t.Errorf("the appliance did not take over the old ledger: %s, want %s", got, oldID)
	}
	if got := d.str(`SELECT (iam_v2.ledger_head() ->> 'seq')`); got != oldHead {
		t.Errorf("ledger head %s, want %s", got, oldHead)
	}
	if st := d.s.ledgerReconcile(context.Background()); st.State != "ok" {
		t.Errorf("both copies are not in step after the replacement restore: %+v", st)
	}
}
