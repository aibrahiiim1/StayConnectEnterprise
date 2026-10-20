package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func entry(id, at, ref, kind string) json.RawMessage {
	b, _ := json.Marshal(journalEntry{ID: id, ErasedAt: at, TenantID: "t", SiteID: "s", Ref: ref, Kind: kind, SubjectID: "x-" + id})
	return b
}

// The journal file is merged by id, kept in the order things were erased, written in one step with mode 0600, and
// holds ids and kinds only.
func TestErasureJournal_MergesByIdInOrderAndIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backups", erasureJournalName)
	if got, err := readJournalFile(path); err != nil || len(got) != 0 {
		t.Fatalf("a missing file is an empty journal: %v %v", got, err)
	}
	first, added := mergeJournal(nil, []json.RawMessage{entry("b", "2026-10-02T00:00:00Z", "ER-000000000002", "account"), entry("a", "2026-10-01T00:00:00Z", "ER-000000000001", "stay")})
	if added != 2 || len(first) != 2 {
		t.Fatalf("merge = %d entries, %d added", len(first), added)
	}
	if err := writeJournalFile(path, first); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("the journal file must be 0600: %v %v", fi, err)
		}
	}
	if _, err := os.Stat(path + ".partial"); err == nil {
		t.Fatal("a partial file was left behind")
	}
	read, err := readJournalFile(path)
	if err != nil || len(read) != 2 {
		t.Fatalf("read back: %v %v", read, err)
	}
	// A second merge with one known and one new entry adds only the new one, in time order.
	merged, added := mergeJournal(read, []json.RawMessage{entry("b", "2026-10-02T00:00:00Z", "ER-000000000002", "account"), entry("c", "2026-09-30T00:00:00Z", "ER-000000000003", "principal")})
	if added != 1 || len(merged) != 3 {
		t.Fatalf("second merge = %d entries, %d added", len(merged), added)
	}
	var firstID journalEntry
	_ = json.Unmarshal(merged[0], &firstID)
	if firstID.ID != "c" {
		t.Fatalf("entries are not in the order they were erased: first is %q", firstID.ID)
	}
}

func TestErasureJournal_RefusesAFileThatIsNotAJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), erasureJournalName)
	if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJournalFile(path); err == nil {
		t.Fatal("a file of something else was read as a journal")
	}
}

// A restore copies the live journal out before it replaces the database, and replays it afterwards: the steps are in
// the restore, in that order, and a failure to record stops it before anything is replaced.
func TestRestore_RecordsErasuresBeforeTheSwapAndReappliesThemAfter(t *testing.T) {
	src, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	rec := indexOf(s, "journalSyncFromLive(")
	swap := indexOf(s, "ALTER DATABASE \" + pgDatabase + \" RENAME TO \" + aside")
	reapply := indexOf(s, "journalReplayInto(")
	integrity := indexOf(s, "s.restoreIntegrity(ctx)")
	if rec < 0 || swap < 0 || reapply < 0 || integrity < 0 {
		t.Fatalf("restore.go lacks a step: record=%d swap=%d reapply=%d integrity=%d", rec, swap, reapply, integrity)
	}
	if !(rec < swap && swap < integrity && integrity < reapply) {
		t.Fatalf("the steps are out of order: record=%d swap=%d integrity=%d reapply=%d", rec, swap, integrity, reapply)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// One damaged line must not block the journal for good: the file is set aside and rebuilt from the database.
func TestMergeIntoFile_ADamagedFileIsSetAsideAndRebuilt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, erasureJournalName)
	if err := os.WriteFile(path, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := json.RawMessage(`{"id":"00000000-0000-4000-8000-000000000001","erased_at":"2026-10-01T00:00:00Z","tenant_id":"t","site_id":"s","erasure_ref":"ER-000000000001","kind":"account","subject_id":"00000000-0000-4000-8000-000000000002"}`)
	added, total, err := mergeIntoFile(path, []json.RawMessage{e}, true)
	if err != nil || added != 1 || total != 1 {
		t.Fatalf("merge = %d %d %v", added, total, err)
	}
	if got, err := readJournalFile(path); err != nil || len(got) != 1 {
		t.Fatalf("rebuilt file: %d entries, %v", len(got), err)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("the damaged file was not set aside: %v", matches)
	}
}
