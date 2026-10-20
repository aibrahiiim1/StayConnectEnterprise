package main

// THE LEDGER AND A RESTORE (GDPR final mission).
//
// A restore loads the backup into a STAGING database first and swaps it in only when it is good. The ledger is checked and
// applied to that staging database, so by the time the restored data is the live data every erasure the hotel had already
// completed has been done again and every request it had already closed is closed again. Nothing identifying is ever
// served from a restored database that has not been through the ledger.
//
// ONE trustworthy, complete copy is enough. The primary copy (on the appliance) and the secondary copy (external) are read;
// those that cannot be reached or verified are simply not used. A copy is trusted only if it is an intact chain AND it reaches
// everything independent witnesses say the ledger had reached: the ledger inside the backup being restored, the live
// database, and the head recorded with every backup on the appliance (internal/ledger.Assess). A copy that merely exists, or
// that was silently cut short, is refused.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// execPsql runs SQL (on stdin) against one database in the database container and returns what it printed.
func execPsql(ctx context.Context, db, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", pgContainer, "psql", "-U", pgUser, "-d", db, "-tA", "-v", "ON_ERROR_STOP=1")
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", tailLine(string(out)))
	}
	return string(out), nil
}

type ledgerHeadInfo struct {
	Has    bool
	Intact bool
	Ledger string
	Seq    int64
	Hash   string
}

// psqlLedgerHead reads the ledger head of a database through psql. A database without the ledger (a backup older than the
// migration) answers Has=false.
func psqlLedgerHead(ctx context.Context, db string) (ledgerHeadInfo, error) {
	// Two statements, not one CASE: PostgreSQL resolves every function a statement names when it parses it, so a single
	// statement naming iam_v2.ledger_head() fails on a backup older than the ledger instead of answering "no ledger".
	has, err := execPsql(ctx, db, `SELECT to_regprocedure('iam_v2.ledger_head()') IS NOT NULL AND to_regprocedure('iam_v2.ledger_verify()') IS NOT NULL`)
	if err != nil {
		return ledgerHeadInfo{}, err
	}
	if strings.TrimSpace(has) != "t" {
		return ledgerHeadInfo{}, nil
	}
	out, err := execPsql(ctx, db, `SELECT iam_v2.ledger_head()::text || '|' || iam_v2.ledger_verify()::text`)
	if err != nil {
		return ledgerHeadInfo{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return ledgerHeadInfo{}, nil
	}
	h, v, _ := strings.Cut(out, "|")
	var head struct {
		Ledger string `json:"ledger"`
		Seq    int64  `json:"seq"`
		Hash   string `json:"hash"`
	}
	var ver struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(h), &head); err != nil {
		return ledgerHeadInfo{}, fmt.Errorf("the ledger head could not be read: %w", err)
	}
	_ = json.Unmarshal([]byte(v), &ver)
	return ledgerHeadInfo{Has: true, Intact: ver.OK, Ledger: head.Ledger, Seq: head.Seq, Hash: head.Hash}, nil
}

// backupLedgerWitnesses are the ledger heads recorded with every backup on the appliance: each says how far the ledger had
// gone when that backup was taken, so a copy that stops earlier than the most recent one has lost entries.
func backupLedgerWitnesses() []ledger.Witness {
	var out []ledger.Witness
	matches, _ := filepath.Glob(filepath.Join(scdBackupDir, "db-*.meta.json"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var meta struct {
			LedgerID   string `json:"ledger_id"`
			LedgerSeq  int64  `json:"ledger_seq"`
			LedgerHash string `json:"ledger_hash"`
		}
		if json.Unmarshal(b, &meta) != nil || meta.LedgerSeq <= 0 || meta.LedgerHash == "" {
			continue
		}
		out = append(out, ledger.Witness{Source: "the backup " + strings.TrimSuffix(filepath.Base(m), ".meta.json"),
			Ledger: meta.LedgerID, Seq: meta.LedgerSeq, Hash: meta.LedgerHash})
	}
	return out
}

// restoreLedgerPrepare runs before anything is touched: the live database's own ledger entries are made durable in the files,
// so a restore can never lose an erasure made since the last reconciliation. The secondary storage need not be reachable.
func (s *server) restoreLedgerPrepare(ctx context.Context) (string, bool) {
	rt := &s.ledgerRT
	if rt.store == nil {
		return "the erasure ledger is not available on this appliance", false
	}
	head, err := psqlLedgerHead(ctx, pgDatabase)
	if err != nil {
		return "the live database's erasure ledger could not be read, so nothing was changed: " + err.Error(), false
	}
	if !head.Has {
		return "the live database has no erasure ledger yet", true
	}
	if !head.Intact {
		return "the live database's erasure ledger is damaged, so nothing was changed", false
	}
	st := s.ledgerReconcile(ctx)
	switch {
	case st.State == "blocked" || st.State == "unavailable":
		return "the erasure ledger could not be made durable before the restore (" + st.Message + "), so nothing was changed", false
	case !st.Local.Present || !st.Local.Intact || int64(st.Local.Entries) < head.Seq:
		return "the erasure ledger on the appliance does not hold every erasure of the live database, so nothing was changed", false
	}
	if st.External.Configured && !st.External.InSync {
		return fmt.Sprintf("the erasure ledger is safe on the appliance (%d entries); the secondary storage is not reachable or not in step and will be brought up to date afterwards", st.Local.Entries), true
	}
	return fmt.Sprintf("the erasure ledger (%d entries) is safe in both places", st.Local.Entries), true
}

// restoreLedgerApply verifies the ledger against the staging database and applies it there. good=false stops the restore with
// nothing replaced.
func (s *server) restoreLedgerApply(ctx context.Context, staging, identityText string) (string, bool) {
	rt := &s.ledgerRT
	if rt.store == nil {
		return "the erasure ledger is not available on this appliance", false
	}
	var ident age.Identity
	if strings.TrimSpace(identityText) != "" {
		if id, err := backupcrypt.ParseIdentity(identityText); err == nil {
			ident = id
		}
	}
	sh, err := psqlLedgerHead(ctx, staging)
	if err != nil {
		return "the ledger in the backup could not be read: " + err.Error(), false
	}
	if sh.Has && !sh.Intact {
		return "the erasure ledger inside this backup is damaged", false
	}
	var wit []ledger.Witness
	if sh.Has {
		wit = append(wit, ledger.Witness{Source: "this backup", Ledger: sh.Ledger, Seq: sh.Seq, Hash: sh.Hash})
	}
	if lh, err := psqlLedgerHead(ctx, pgDatabase); err == nil && lh.Has && lh.Intact {
		wit = append(wit, ledger.Witness{Source: "the live database", Ledger: lh.Ledger, Seq: lh.Seq, Hash: lh.Hash})
	}
	wit = append(wit, backupLedgerWitnesses()...)
	// A backup (or database) that has only its own first entry says nothing about another appliance's ledger, and a witness
	// from a different ledger is dropped when this database holds nothing yet: a replacement appliance restoring the old one's data.
	local := rt.store.ReadLocal()
	ext := rt.store.ReadExternal(ctx, ident)
	copies, wit := settleFreshLedgers([]ledger.Copy{local, ext}, wit)
	v := ledger.Assess(copies, wit)
	reasons := strings.Join(v.Reasons, "; ")
	if v.Conflict {
		return "the erasure ledger contradicts itself, so the restore was not attempted: " + reasons, false
	}
	if v.Best == nil || !v.Complete {
		return "no complete, trustworthy copy of the erasure ledger is available, so the restore was not attempted (restoring would bring erased guests back): " + reasons, false
	}
	entries := v.Best.Parse.Entries
	if !sh.Has {
		if len(entries) > 1 {
			return fmt.Sprintf("this backup is older than the erasure ledger and the ledger holds %d entries: restoring it would bring erased guests back, so the restore was not attempted. Restore a newer backup.", len(entries)-1), false
		}
		return "this backup predates the erasure ledger; the ledger holds no erasure yet, so there is nothing to apply", true
	}
	adopt := sh.Seq == 1 && entries[0].Ledger != sh.Ledger
	feed := entries
	if !adopt {
		if int(sh.Seq) > len(entries) {
			return "the ledger copy is shorter than the ledger inside the backup", false
		}
		feed = entries[sh.Seq:]
	}
	applied := 0
	if len(feed) > 0 {
		doc, _ := json.Marshal(feed)
		if bytes.Contains(doc, []byte("$led$")) {
			return "a ledger entry contains an unexpected delimiter", false
		}
		out, err := execPsql(ctx, staging, fmt.Sprintf("SELECT public.ledger_apply($led$%s$led$::jsonb, %t)::text", doc, adopt))
		if err != nil {
			return "the erasures could not be applied to the restored data: " + err.Error(), false
		}
		var r struct {
			Applied  int             `json:"applied"`
			Failed   json.RawMessage `json:"failed"`
			Conflict json.RawMessage `json:"conflict"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); err != nil {
			return "the answer to the ledger replay could not be read: " + err.Error(), false
		}
		if len(r.Conflict) > 0 && string(r.Conflict) != "null" {
			return "the ledger does not continue the one inside the backup: " + string(r.Conflict), false
		}
		if len(r.Failed) > 0 && string(r.Failed) != "null" {
			return "an erasure could not be applied to the restored data, so nothing was replaced: " + string(r.Failed), false
		}
		applied = r.Applied
	}
	after, err := psqlLedgerHead(ctx, staging)
	if err != nil {
		return "the restored data's ledger could not be read back: " + err.Error(), false
	}
	bs, bh := v.Best.Parse.Head()
	if after.Seq != bs || after.Hash != bh {
		return fmt.Sprintf("the restored data's ledger stops at entry %d, not %d", after.Seq, bs), false
	}
	src := v.Best.Name
	return fmt.Sprintf("the ledger (%d entries) was verified from the %s copy and %d completed erasures and closed requests were applied to the restored data before it went live: %s",
		bs, src, applied, reasons), true
}

// carryLedgerDestination keeps the secondary storage setting across the swap: it belongs to this appliance, not to the backup.
func (s *server) carryLedgerDestination(ctx context.Context, staging string) (string, bool) {
	out, err := execPsql(ctx, pgDatabase, `SELECT COALESCE((SELECT to_jsonb(d)::text FROM public.ledger_destination d), '')`)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return "no secondary storage is set up yet", true
		}
		return "the secondary storage setting could not be read from the live database: " + err.Error(), false
	}
	doc := strings.TrimSpace(out)
	if doc == "" {
		return "no secondary storage is set up on the live database", true
	}
	if strings.Contains(doc, "$dst$") {
		return "the secondary storage setting contains an unexpected delimiter", false
	}
	sql := fmt.Sprintf(`DO $do$ BEGIN
  IF to_regclass('public.ledger_destination') IS NULL THEN RETURN; END IF;
  DELETE FROM public.ledger_destination;
  INSERT INTO public.ledger_destination SELECT * FROM jsonb_populate_record(null::public.ledger_destination, $dst$%s$dst$::jsonb);
END $do$`, doc)
	if _, err := execPsql(ctx, staging, sql); err != nil {
		return "the secondary storage setting could not be carried into the restored data: " + err.Error(), false
	}
	return "the secondary storage setting of this appliance was kept", true
}

// ledgerReconcileAfterRestore brings both copies into step with the restored database and returns "" when they are, or a
// plain statement of what is not (never an error that undoes the restore).
func (s *server) ledgerReconcileAfterRestore(ctx context.Context) string {
	s.resetPool()
	st := s.ledgerReconcile(ctx)
	switch {
	case st.State == "ok":
		return ""
	case st.State == "not_configured":
		return "the secondary storage for the erasure ledger is not set up on this appliance: set it up in Hotel Admin (erasure requests are refused until it is)"
	case st.State == "recovery_needed":
		return st.Message
	default:
		if st.Message != "" {
			return st.Message + ": it is retried every hour"
		}
		return "the erasure ledger is not yet in step in both places: it is retried every hour"
	}
}

// settleFreshLedgers sets aside what a brand-new appliance says about a ledger it has never had. A replacement appliance starts
// with its own ledger holding only its first entry; the ledger of the appliance it replaces is somewhere else, and is the real
// one. A copy or a witness that holds nothing but a first entry of a DIFFERENT ledger says nothing about erasures, so it is
// ignored rather than treated as a contradiction. (A first entry cannot hide an erasure.) Only a longer intact ledger can
// make another one "fresh".
func settleFreshLedgers(copies []ledger.Copy, wit []ledger.Witness) ([]ledger.Copy, []ledger.Witness) {
	id, longest := "", 1
	for _, c := range copies {
		if c.Intact() && len(c.Parse.Entries) > longest {
			id, longest = c.Parse.LedgerID(), len(c.Parse.Entries)
		}
	}
	if id == "" {
		return copies, wit
	}
	out := make([]ledger.Copy, len(copies))
	for i, c := range copies {
		out[i] = c
		if c.Intact() && len(c.Parse.Entries) == 1 && c.Parse.LedgerID() != id {
			out[i] = ledger.Copy{Name: c.Name}
		}
	}
	var w []ledger.Witness
	for _, x := range wit {
		if x.Seq <= 1 && x.Ledger != "" && x.Ledger != id {
			continue
		}
		w = append(w, x)
	}
	return out, w
}
