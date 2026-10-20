package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

func TestDailyBackupDue(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 21, h, 0, 0, 0, time.Local) }
	cases := []struct {
		now, newest time.Time
		hour        int
		want        bool
	}{
		{at(1), time.Time{}, 2, false},                  // before the hour
		{at(2), time.Time{}, 2, true},                   // never taken
		{at(3), at(3).Add(-24 * time.Hour), 2, true},    // yesterday's
		{at(3), at(3).Add(-1 * time.Hour), 2, false},    // already taken today
		{at(23), at(23).Add(-19 * time.Hour), 2, false}, // inside 20 hours
		{at(23), at(23).Add(-21 * time.Hour), 2, true},  // a missed night is made up
	}
	for i, c := range cases {
		if got := dailyBackupDue(c.now, c.newest, c.hour); got != c.want {
			t.Errorf("case %d: due = %v, want %v", i, got, c.want)
		}
	}
}

// seedDaily writes a daily set (database, configuration, note, marker) as dailyBackup leaves it.
func seedDaily(t *testing.T, taken time.Time) dailyMeta {
	t.Helper()
	stamp := taken.UTC().Format("20060102T150405Z")
	m := dailyMeta{Kind: "daily", Name: "db-" + stamp + ".sql.gz" + backupcrypt.Suffix, TakenAt: taken, Restorable: true, Encrypted: true, EtcName: "etc-" + stamp + ".tgz" + backupcrypt.Suffix}
	for _, f := range []string{m.Name, m.EtcName} {
		if err := os.WriteFile(filepath.Join(scdBackupDir, f), []byte("content of "+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.SHA256, _ = fileDigest(filepath.Join(scdBackupDir, m.Name))
	m.EtcSHA256, _ = fileDigest(filepath.Join(scdBackupDir, m.EtcName))
	m.SizeBytes = int64(len("content of " + m.Name))
	m.EtcBytes = int64(len("content of " + m.EtcName))
	if err := writeMetaAtomically(&m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verifiedMarker(m.Name), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRotationKeepsTheNewestSevenSetsAndNothingElse(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	base := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	var all []dailyMeta
	for i := 0; i < 10; i++ {
		all = append(all, seedDaily(t, base.Add(time.Duration(i)*24*time.Hour)))
	}
	// a manual backup and a safety dump are not daily sets and are never touched
	manual := filepath.Join(scdBackupDir, "db-20260901T090000Z.sql.gz.age")
	safety := filepath.Join(scdBackupDir, "db-safety-20260902T090000Z.sql.gz")
	for _, f := range []string{manual, safety} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{}
	if n := s.rotateDailyBackups(7); n != 3 {
		t.Fatalf("removed %d sets, want 3", n)
	}
	// listDailyMetas is newest first: all[9] is the newest, so the three oldest (all[0..2]) go
	for i, m := range all {
		kept := i >= 3
		for _, f := range []string{m.Name, m.EtcName, m.Name + ".verified", filepath.Base(metaPathFor(m.Name))} {
			_, err := os.Stat(filepath.Join(scdBackupDir, f))
			if kept && err != nil {
				t.Errorf("set %d lost %s", i, f)
			}
			if !kept && err == nil {
				t.Errorf("set %d kept %s", i, f)
			}
		}
	}
	for _, f := range []string{manual, safety} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was removed by the daily rotation", f)
		}
	}
	// the newest is never removed, whatever the setting says
	if n := s.rotateDailyBackups(0); n != 6 {
		t.Fatalf("with keep=0 the rotation removed %d, want it to keep exactly one", n)
	}
	if len(listDailyMetas()) != 1 {
		t.Fatal("the newest set was not kept")
	}
}

func TestAStaleNoteWhoseDumpIsGoneIsRemoved(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	m := seedDaily(t, time.Now().UTC())
	if err := os.Remove(filepath.Join(scdBackupDir, m.Name)); err != nil {
		t.Fatal(err)
	}
	(&server{}).rotateDailyBackups(7)
	if len(listDailyMetas()) != 0 {
		t.Fatal("a note for a missing dump was kept")
	}
}

func TestStoredBackupsAreCheckedAgainstTheirNotes(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	m := seedDaily(t, time.Now().UTC())
	if p := verifyStoredDaily(); len(p) != 0 {
		t.Fatalf("a good set reported %v", p)
	}
	// bit rot: same size, one byte different
	full := filepath.Join(scdBackupDir, m.Name)
	b, _ := os.ReadFile(full)
	b[3] ^= 0x01
	_ = os.WriteFile(full, b, 0o600)
	p := verifyStoredDaily()
	if len(p) != 1 || p[0].Name != m.Name || !strings.Contains(p[0].Detail, "changed") {
		t.Fatalf("problems = %v", p)
	}
	_ = os.Remove(filepath.Join(scdBackupDir, m.EtcName))
	if p := verifyStoredDaily(); len(p) != 2 {
		t.Fatalf("a missing configuration archive was not reported: %v", p)
	}
}

func TestConfigurationArchiveIsEncryptedAndLeavesTheFinancialMarkerOut(t *testing.T) {
	dir := t.TempDir()
	old := etcStayconnectDir
	etcStayconnectDir = filepath.Join(dir, "etc", "stayconnect")
	defer func() { etcStayconnectDir = old }()
	if err := os.MkdirAll(filepath.Join(etcStayconnectDir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	for f, c := range map[string]string{"scd.env": "DB=secret-dsn", "secrets/notify_dek.key": "KEYMATERIAL", financialMarker: `{"generation":9}`} {
		if err := os.WriteFile(filepath.Join(etcStayconnectDir, f), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := age.GenerateX25519Identity()
	out := filepath.Join(dir, "etc-test.tgz.age")
	n, err := writeEtcArchive(out, []age.Recipient{id.Recipient()})
	if err != nil || n != 2 {
		t.Fatalf("archive = %d files, %v", n, err)
	}
	raw, _ := os.ReadFile(out)
	if bytes.Contains(raw, []byte("KEYMATERIAL")) || bytes.Contains(raw, []byte("secret-dsn")) {
		t.Fatal("the configuration archive is readable without a key")
	}
	r, err := backupcrypt.Decrypt(bytes.NewReader(raw), id)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	seen := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		seen[h.Name] = string(b)
	}
	if seen["stayconnect/scd.env"] != "DB=secret-dsn" || seen["stayconnect/secrets/notify_dek.key"] != "KEYMATERIAL" {
		t.Fatalf("archive content = %v", seen)
	}
	for name := range seen {
		if strings.HasSuffix(name, financialMarker) {
			t.Fatalf("the financial restore marker is in the archive")
		}
	}
	// a missing folder is not an error (an appliance under construction)
	etcStayconnectDir = filepath.Join(dir, "nope")
	if n, err := writeEtcArchive(filepath.Join(dir, "x"), []age.Recipient{id.Recipient()}); err != nil || n != -1 {
		t.Fatalf("missing folder = %d %v", n, err)
	}
}

func TestMetaNamesAreWhatTheLedgerWitnessScanLooksFor(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	m := seedDaily(t, time.Now().UTC())
	m.LedgerID, m.LedgerSeq, m.LedgerHash = "11111111-1111-4111-8111-111111111111", 5, strings.Repeat("a", 64)
	if err := writeMetaAtomically(&m); err != nil {
		t.Fatal(err)
	}
	w := backupLedgerWitnesses()
	if len(w) != 1 || w[0].Seq != 5 || w[0].Hash != m.LedgerHash {
		t.Fatalf("witnesses = %+v", w)
	}
	b, _ := os.ReadFile(metaPathFor(m.Name))
	var back dailyMeta
	if err := json.Unmarshal(b, &back); err != nil || back.Name != m.Name {
		t.Fatalf("note = %v %v", back, err)
	}
}
