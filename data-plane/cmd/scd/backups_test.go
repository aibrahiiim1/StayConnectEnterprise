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

func TestScheduledBackupDue(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 21, h, 0, 0, 0, time.Local) }
	on := func(hour, every int) backupSettings {
		return backupSettings{Enabled: true, Hour: hour, EveryDays: every, Keep: 7, SafetyKeep: 3}
	}
	off := on(2, 1)
	off.Enabled = false
	cases := []struct {
		name        string
		set         backupSettings
		now, newest time.Time
		want        bool
	}{
		{"before the hour", on(2, 1), at(1), time.Time{}, false},
		{"never taken", on(2, 1), at(2), time.Time{}, true},
		{"yesterday", on(2, 1), at(3), at(3).Add(-24 * time.Hour), true},
		{"already taken today", on(2, 1), at(3), at(3).Add(-1 * time.Hour), false},
		{"inside 20 hours", on(2, 1), at(23), at(23).Add(-19 * time.Hour), false},
		{"a missed night is made up", on(2, 1), at(23), at(23).Add(-21 * time.Hour), true},
		{"every three days: too soon", on(2, 3), at(3), at(3).Add(-48 * time.Hour), false},
		{"every three days: due", on(2, 3), at(3), at(3).Add(-69 * time.Hour), true},
		{"OFF is never due", off, at(3), time.Time{}, false},
	}
	for _, c := range cases {
		if got := scheduledBackupDue(c.set, c.now, c.newest); got != c.want {
			t.Errorf("%s: due = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNextScheduledBackup(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.Local)
	set := backupSettings{Enabled: true, Hour: 2, EveryDays: 1, Keep: 7, SafetyKeep: 3}
	if got := nextScheduledBackup(backupSettings{}, now, time.Time{}); !got.IsZero() {
		t.Errorf("OFF has a next backup: %v", got)
	}
	// taken at 08:00: the next one is a full interval (20 hours) later, tomorrow at 04:00
	got := nextScheduledBackup(set, now, now.Add(-2*time.Hour))
	if want := time.Date(2026, 10, 22, 4, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("next = %v, want %v", got, want)
	}
	if got := nextScheduledBackup(set, now, time.Time{}); !got.Equal(now) {
		t.Errorf("never taken: next = %v, want now", got)
	}
}

func useRetentionConf(t *testing.T, content string) {
	t.Helper()
	old := retentionConf
	retentionConf = filepath.Join(t.TempDir(), "backup-retention.conf")
	if content != "" {
		if err := os.WriteFile(retentionConf, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { retentionConf = old })
}

func seedBackup(t *testing.T, kind string, taken time.Time) backupMeta {
	t.Helper()
	stamp := taken.UTC().Format("20060102T150405Z")
	prefix := "db-"
	if kind == kindPreRestore {
		prefix = "db-safety-"
	}
	m := backupMeta{Kind: kind, Name: prefix + stamp + ".sql.gz" + backupcrypt.Suffix, TakenAt: taken, Restorable: true, Encrypted: true, EtcName: "etc-" + stamp + ".tgz" + backupcrypt.Suffix}
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

func TestRotationKeepsTheNewestSevenRegularAndThreeProtectiveAndNothingElse(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	useRetentionConf(t, "")
	base := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	var regular, protective []backupMeta
	for i := 0; i < 10; i++ {
		kind := kindScheduled
		if i%2 == 1 {
			kind = kindManual
		}
		regular = append(regular, seedBackup(t, kind, base.Add(time.Duration(i)*24*time.Hour)))
	}
	for i := 0; i < 5; i++ {
		kind := kindPreChange
		if i%2 == 1 {
			kind = kindPreRestore
		}
		protective = append(protective, seedBackup(t, kind, base.Add(time.Duration(i)*time.Hour+30*time.Minute)))
	}
	earlier := filepath.Join(scdBackupDir, "db-20260901T090000Z.sql.gz.age")
	if err := os.WriteFile(earlier, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &server{}
	if n := s.rotateBackups(); n != 3+2 {
		t.Fatalf("removed %d sets, want 5", n)
	}
	check := func(label string, all []backupMeta, keepFrom int) {
		for i, m := range all {
			kept := i >= keepFrom
			for _, f := range []string{m.Name, m.EtcName, m.Name + ".verified", filepath.Base(metaPathFor(m.Name))} {
				_, err := os.Stat(filepath.Join(scdBackupDir, f))
				if kept && err != nil {
					t.Errorf("%s %d lost %s", label, i, f)
				}
				if !kept && err == nil {
					t.Errorf("%s %d kept %s", label, i, f)
				}
			}
		}
	}
	check("regular", regular, 3)
	check("protective", protective, 2)
	if _, err := os.Stat(earlier); err != nil {
		t.Errorf("an earlier backup was removed by rotation")
	}
	useRetentionConf(t, "BACKUP_KEEP=1\nSAFETY_BACKUP_KEEP=2\n")
	s.rotateBackups()
	var nreg, nprot int
	for _, m := range listBackupMetas() {
		if poolOf(m.Kind) == "regular" {
			nreg++
		} else {
			nprot++
		}
	}
	if nreg != 1 || nprot != 2 {
		t.Fatalf("after tightening: %d regular, %d protective", nreg, nprot)
	}
}

func TestEarlierBackupsAreListedKeptAndRemovedOnlyWithAVerifiedNewOne(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	useRetentionConf(t, "")
	oldNightly := nightlyBackupDir
	nightlyBackupDir = t.TempDir()
	defer func() { nightlyBackupDir = oldNightly }()
	for _, f := range []string{"site-20260801-020000.dump", "site-20260801-020000-etc.tgz", "site-20260801-020000.dump.meta.json", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(nightlyBackupDir, f), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldDump := filepath.Join(scdBackupDir, "db-20260802T020000Z.sql.gz")
	if err := os.WriteFile(oldDump, []byte("old dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sets := earlierSets(); len(sets) != 2 {
		t.Fatalf("earlier sets = %+v", sets)
	}
	(&server{}).rotateBackups()
	if _, err := os.Stat(oldDump); err != nil {
		t.Fatal("rotation removed an earlier dump")
	}
	if _, _, err := removeEarlierBackups(); err == nil {
		t.Fatal("earlier backups were removed with no verified backup of the new kind")
	}
	if len(earlierSets()) != 2 {
		t.Fatal("a refused removal removed something")
	}
	m := seedBackup(t, kindManual, time.Now().UTC())
	n, _, err := removeEarlierBackups()
	if err != nil || n != 2 {
		t.Fatalf("removal = %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(nightlyBackupDir, "unrelated.txt")); err != nil {
		t.Error("a file that is not an earlier backup was removed")
	}
	if _, err := os.Stat(filepath.Join(scdBackupDir, m.Name)); err != nil {
		t.Error("the new backup was removed with the earlier ones")
	}
	_ = os.WriteFile(oldDump, []byte("old dump"), 0o600)
	b, _ := os.ReadFile(filepath.Join(scdBackupDir, m.Name))
	b[3] ^= 1
	_ = os.WriteFile(filepath.Join(scdBackupDir, m.Name), b, 0o600)
	if _, _, err := removeEarlierBackups(); err == nil {
		t.Fatal("removal went ahead although a stored backup no longer matches its note")
	}
}

func TestRecipientsIncludeTheRestoringHoldersKey(t *testing.T) {
	a, _ := age.GenerateX25519Identity()
	b, _ := age.GenerateX25519Identity()
	got, err := recipientsFromKeys([]string{a.Recipient().String()}, b.String())
	if err != nil || len(got) != 2 {
		t.Fatalf("recipients = %d, %v", len(got), err)
	}
	if got, err := recipientsFromKeys(nil, ""); err != nil || got != nil {
		t.Fatalf("no key holder = %v, %v", got, err)
	}
	if got, _ := recipientsFromKeys([]string{a.Recipient().String()}, a.String()); len(got) != 1 {
		t.Fatalf("the same key twice = %d", len(got))
	}
}

func TestAStaleNoteWhoseDumpIsGoneIsRemoved(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	m := seedBackup(t, kindManual, time.Now().UTC())
	if err := os.Remove(filepath.Join(scdBackupDir, m.Name)); err != nil {
		t.Fatal(err)
	}
	(&server{}).rotateBackups()
	if len(listBackupMetas()) != 0 {
		t.Fatal("a note for a missing dump was kept")
	}
}

func TestStoredBackupsAreCheckedAgainstTheirNotes(t *testing.T) {
	restore := scdBackupDirForTest(t, t.TempDir())
	defer restore()
	m := seedBackup(t, kindManual, time.Now().UTC())
	if p := verifyStoredBackups(); len(p) != 0 {
		t.Fatalf("a good set reported %v", p)
	}
	full := filepath.Join(scdBackupDir, m.Name)
	b, _ := os.ReadFile(full)
	b[3] ^= 0x01
	_ = os.WriteFile(full, b, 0o600)
	p := verifyStoredBackups()
	if len(p) != 1 || p[0].Name != m.Name || !strings.Contains(p[0].Detail, "changed") {
		t.Fatalf("problems = %v", p)
	}
	_ = os.Remove(filepath.Join(scdBackupDir, m.EtcName))
	if p := verifyStoredBackups(); len(p) != 2 {
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
	m := seedBackup(t, kindManual, time.Now().UTC())
	m.LedgerID, m.LedgerSeq, m.LedgerHash = "11111111-1111-4111-8111-111111111111", 5, strings.Repeat("a", 64)
	if err := writeMetaAtomically(&m); err != nil {
		t.Fatal(err)
	}
	w := backupLedgerWitnesses()
	if len(w) != 1 || w[0].Seq != 5 || w[0].Hash != m.LedgerHash {
		t.Fatalf("witnesses = %+v", w)
	}
	b, _ := os.ReadFile(metaPathFor(m.Name))
	var back backupMeta
	if err := json.Unmarshal(b, &back); err != nil || back.Name != m.Name {
		t.Fatalf("note = %v %v", back, err)
	}
}
