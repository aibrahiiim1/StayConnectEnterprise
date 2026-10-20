package main

// AN ERASURE SURVIVES A RESTORE (migration 0147; GDPR final audit).
//
// A restore brings the database back as it was on the day of the backup, with every guest erased since in it again,
// names and all. Erasure is therefore recorded twice: in the database, as the erasure journal (the kind and the id of
// each record that was anonymised -- random uuids that identify nobody once the record is anonymised), and in a file
// next to the backups, outside the database. After any restore the file is replayed: every erasure the restored
// database does not know of is applied again, and only then is it recorded as known.
//
//   - Every hour scd copies new journal entries into the file (syncErasureJournal) and replays the file onto the
//     database (reconcileErasureJournal). On a database that has every entry that does nothing.
//   - A restore (restore.go) copies the LIVE journal into the file before it replaces the database, so an in-place
//     restore loses nothing, and replays the file onto the restored database before the appliance serves again. If the
//     restored database predates the journal, the replay waits for the migration and the hourly run applies it.
//   - A disaster restore onto a new machine needs the file: it lives in the backup directory, so a copy of the backups
//     carries it. The hotel copies that directory off the appliance as it already must.
//
// The file holds no personal data: ids and kinds only. It is written atomically with mode 0600.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const erasureJournalName = "erasure-journal.jsonl"

func erasureJournalFile() string { return filepath.Join(scdBackupDir, erasureJournalName) }

type journalRow interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type journalEntry struct {
	ID        string `json:"id"`
	ErasedAt  string `json:"erased_at"`
	TenantID  string `json:"tenant_id"`
	SiteID    string `json:"site_id"`
	Ref       string `json:"erasure_ref"`
	Kind      string `json:"kind"`
	SubjectID string `json:"subject_id"`
	Detail    string `json:"detail,omitempty"`
}

// readJournalFile returns the file's entries; a missing file is an empty journal.
func readJournalFile(path string) ([]json.RawMessage, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e journalEntry
		if json.Unmarshal(line, &e) != nil || e.ID == "" {
			return nil, fmt.Errorf("the erasure journal file holds a line that is not an entry")
		}
		out = append(out, append(json.RawMessage(nil), line...))
	}
	return out, sc.Err()
}

// mergeJournal adds the entries of incoming that existing does not have, by id, in erased_at order. It returns how
// many were added.
func mergeJournal(existing, incoming []json.RawMessage) ([]json.RawMessage, int) {
	have := map[string]bool{}
	type keyed struct {
		at, id string
		raw    json.RawMessage
	}
	var all []keyed
	add := func(raw json.RawMessage) bool {
		var e journalEntry
		if json.Unmarshal(raw, &e) != nil || e.ID == "" || have[e.ID] {
			return false
		}
		have[e.ID] = true
		all = append(all, keyed{e.ErasedAt, e.ID, raw})
		return true
	}
	for _, r := range existing {
		add(r)
	}
	added := 0
	for _, r := range incoming {
		if add(r) {
			added++
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].at != all[j].at {
			return all[i].at < all[j].at
		}
		return all[i].id < all[j].id
	})
	out := make([]json.RawMessage, len(all))
	for i, k := range all {
		out[i] = k.raw
	}
	return out, added
}

// writeJournalFile replaces the file in one step, 0600, so a reader never sees half of it.
func writeJournalFile(path string, entries []json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, e := range entries {
		buf.Write(e)
		buf.WriteByte('\n')
	}
	f, err := os.CreateTemp(filepath.Dir(path), erasureJournalName+".*.partial")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// journalMu serialises every change of the journal file: the hourly run and a restore are different code paths of one
// process, and each reads the file, merges and writes it back -- two at once would lose what the loser merged.
var journalMu sync.Mutex

// mergeIntoFile adds incoming entries to the file and returns how many the file gained and how many it holds. A file that
// cannot be read (a damaged line) is not lost and not trusted: it is set aside as <file>.corrupt-<time> for hand recovery
// and the file is rebuilt from the database's own journal, so one bad line can never block a restore for good.
func mergeIntoFile(path string, incoming []json.RawMessage, writeIfUnchanged bool) (added, total int, err error) {
	journalMu.Lock()
	defer journalMu.Unlock()
	existing, rerr := readJournalFile(path)
	if rerr != nil {
		aside := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
		if os.Rename(path, aside) != nil {
			return 0, 0, rerr
		}
		slog.Error("erasure journal file was damaged and has been set aside; it is rebuilt from the database", "file", aside, "err", rerr)
		existing = nil
	}
	merged, added := mergeJournal(existing, incoming)
	if added == 0 && !writeIfUnchanged {
		if _, statErr := os.Stat(path); statErr == nil {
			return 0, len(merged), nil
		}
	}
	return added, len(merged), writeJournalFile(path, merged)
}

// journalMissing reports the errors that mean "this database has no journal yet" (it predates migration 0147).
func journalMissing(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "42883" || pgErr.Code == "42P01" || pgErr.Code == "3F000")
}

func parseEntries(doc string) ([]json.RawMessage, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(doc), &raws); err != nil {
		return nil, err
	}
	return raws, nil
}

// syncErasureJournal copies the database's journal into the file. It returns how many entries the file gained.
func syncErasureJournal(ctx context.Context, db journalRow, tenant, site, path string) (int, error) {
	var doc string
	if err := db.QueryRow(ctx, `SELECT public.erasure_journal_export($1::uuid,$2::uuid)::text`, tenant, site).Scan(&doc); err != nil {
		return 0, err
	}
	incoming, err := parseEntries(doc)
	if err != nil {
		return 0, err
	}
	added, _, err := mergeIntoFile(path, incoming, false)
	return added, err
}

type replayResult struct {
	Entries      int `json:"entries"`
	AlreadyKnown int `json:"already_known"`
	Reapplied    int `json:"reapplied"`
	RecordsGone  int `json:"records_gone"`
	Failed       []struct {
		Ref   string `json:"erasure_ref"`
		Kind  string `json:"kind"`
		Error string `json:"error"`
	} `json:"failed"`
}

// reconcileErasureJournal replays the file onto the database: erasures the database does not know of are applied
// again. absent is true when the database has no journal yet.
func reconcileErasureJournal(ctx context.Context, db journalRow, path string) (res replayResult, absent bool, err error) {
	entries, err := readJournalFile(path)
	if err != nil {
		return res, false, err
	}
	if len(entries) == 0 {
		return res, false, nil
	}
	arr := append(append([]byte("["), bytes.Join(rawStrings(entries), []byte(","))...), ']')
	var out string
	if err := db.QueryRow(ctx, `SELECT public.erasure_journal_replay($1::jsonb)::text`, string(arr)).Scan(&out); err != nil {
		if journalMissing(err) {
			return res, true, nil
		}
		return res, false, err
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return res, false, err
	}
	return res, false, nil
}

func rawStrings(in []json.RawMessage) [][]byte {
	out := make([][]byte, len(in))
	for i, r := range in {
		out[i] = []byte(r)
	}
	return out
}

// runErasureJournal is the hourly run: the file gains what the database has, and the database gains what the file has.
func (s *server) runErasureJournal(ctx context.Context) {
	if s.db == nil || s.tenID == "" || s.siteID == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	path := erasureJournalFile()
	if _, err := syncErasureJournal(cctx, s.db, s.tenID, s.siteID, path); err != nil {
		if journalMissing(err) {
			slog.Warn("erasure journal unavailable: this database predates the migration that adds it")
		} else if ctx.Err() == nil {
			slog.Error("erasure journal: the file could not be brought up to date", "err", err)
		}
	}
	res, absent, err := reconcileErasureJournal(cctx, s.db, path)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			slog.Error("erasure journal: the erasures could not be replayed", "err", err)
		}
	case absent:
		slog.Warn("erasure journal: erasures wait for the migration that adds the journal to this database")
	case res.Reapplied > 0:
		slog.Warn("erasure journal: erasures the database had lost were applied again", "reapplied", res.Reapplied, "records_gone", res.RecordsGone)
	}
	for _, f := range res.Failed {
		slog.Error("erasure journal: an erasure could not be applied again; it is tried at every run", "erasure", f.Ref, "kind", f.Kind, "error", f.Error)
	}
}

// ---- the restore ----

const journalCaptureSQL = `SELECT CASE WHEN to_regclass('iam_v2.erasure_journal') IS NULL THEN ''
  ELSE (SELECT COALESCE(jsonb_agg(to_jsonb(j) - 'seq' ORDER BY j.erased_at, j.id), '[]'::jsonb)::text FROM iam_v2.erasure_journal j) END`

// journalSyncFromLive copies the LIVE database's journal into the file before a restore replaces the database.
// ok=false stops the restore (nothing has been replaced).
func journalSyncFromLive(ctx context.Context, live, path string) (ok bool, detail string) {
	out, err := execPsql(ctx, live, journalCaptureSQL)
	if err != nil {
		return false, "the erasure journal could not be read from the live database, so nothing was replaced: " + err.Error()
	}
	doc := strings.TrimSpace(out)
	if doc == "" {
		return true, "this appliance's database has no erasure journal yet"
	}
	incoming, err := parseEntries(doc)
	if err != nil {
		return false, "the erasure journal could not be read, so nothing was replaced: " + err.Error()
	}
	added, total, err := mergeIntoFile(path, incoming, true)
	if err != nil {
		return false, "the erasure journal file could not be written, so nothing was replaced: " + err.Error()
	}
	return true, fmt.Sprintf("%d erasures are on record outside the database (%d new)", total, added)
}

// journalReplayInto replays the file onto the database that is live after a restore.
func journalReplayInto(ctx context.Context, db, path string) (detail string, problem bool) {
	entries, err := readJournalFile(path)
	if err != nil {
		return "the erasure journal file could not be read: " + err.Error(), true
	}
	if len(entries) == 0 {
		return "no erasure is on record", false
	}
	arr := append(append([]byte("["), bytes.Join(rawStrings(entries), []byte(","))...), ']')
	sql := "SELECT CASE WHEN to_regprocedure('public.erasure_journal_replay(jsonb)') IS NULL THEN 'absent' " +
		"ELSE public.erasure_journal_replay($jrnl$" + string(arr) + "$jrnl$::jsonb)::text END"
	out, err := execPsql(ctx, db, sql)
	if err != nil {
		return "the erasures could not be applied again: " + err.Error() + ". They are tried again every hour.", true
	}
	out = strings.TrimSpace(out)
	if out == "absent" {
		return "the restored database predates the erasure journal: the erasures are applied again, from the file, at the first hourly run after the migrations are applied", false
	}
	var res replayResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return "the answer to the replay could not be read: " + err.Error(), true
	}
	if len(res.Failed) > 0 {
		return fmt.Sprintf("%d erasures were applied again; %d could not be and are tried again every hour", res.Reapplied, len(res.Failed)), true
	}
	return fmt.Sprintf("%d erasures the backup did not know of were applied again; %d were already in it", res.Reapplied, res.AlreadyKnown), false
}

// execPsql runs one statement against a database of the appliance's Postgres container, the statement on stdin.
func execPsql(ctx context.Context, db, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", pgContainer, "psql", "-U", pgUser, "-d", db, "-tA", "-v", "ON_ERROR_STOP=1")
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", tailLine(string(out)))
	}
	return string(out), nil
}
