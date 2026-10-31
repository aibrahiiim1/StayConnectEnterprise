package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetentionDefaultsMatchTheSweepsOwn(t *testing.T) {
	// The sweep falls back to these numbers when the file is absent, so the schema must agree.
	want := map[string]int{
		"KEEP_BINARIES": 5, "KEEP_RELEASES": 5, "KEEP_CONFIG": 3, "DISK_WARN": 80, "DISK_CRIT": 90,
		"RESTORE_TEST_MONTHS": 6, "SET_ASIDE_DAYS": 14, "BACKUP_ENABLED": 0, "BACKUP_HOUR": 2, "BACKUP_EVERY_DAYS": 1,
		"BACKUP_KEEP": 3, "SAFETY_BACKUP_KEEP": 2, "BACKUP_REPLICATE": 0,
	}
	for _, s := range retentionSchema {
		if want[s.Key] != s.Default {
			t.Errorf("%s defaults to %d here and %d in stayconnect-backup-cleanup", s.Key, s.Default, want[s.Key])
		}
		if s.Min < 0 || s.Max <= s.Min {
			t.Errorf("%s has nonsensical bounds %d..%d", s.Key, s.Min, s.Max)
		}
		if s.Unit == "" || s.Explains == "" {
			t.Errorf("%s ships without a unit or an explanation; an operator cannot set it responsibly", s.Key)
		}
	}
}

func TestRecoveryFloorsCannotBeSetToZero(t *testing.T) {
	// Keeping fewer than two binaries means there is nothing to roll back TO, and keeping zero database
	// backups means the newest-always-kept rule is the only thing standing between the hotel and total loss.
	byKey := map[string]retentionSetting{}
	for _, s := range retentionSchema {
		byKey[s.Key] = s
	}
	if byKey["KEEP_BINARIES"].Min < 2 {
		t.Error("service binaries may be reduced below two, leaving nothing to roll back to")
	}
	if byKey["KEEP_RELEASES"].Min < 2 {
		t.Error("releases may be reduced below two")
	}
	if byKey["BACKUP_KEEP"].Min < 1 {
		t.Error("backups may be set to zero")
	}
	if byKey["SAFETY_BACKUP_KEEP"].Min < 2 {
		t.Error("the safety backups floor may be set below two")
	}
}

func TestScheduledBackupsAreOffByDefaultAndRetentionIsOneCount(t *testing.T) {
	byKey := map[string]retentionSetting{}
	for _, s := range retentionSchema {
		byKey[s.Key] = s
	}
	if byKey["BACKUP_ENABLED"].Default != 0 {
		t.Fatal("scheduled backups are on by default")
	}
	if byKey["BACKUP_KEEP"].Default != 3 || byKey["BACKUP_KEEP"].Max > maxBackupSets {
		t.Errorf("backups kept: default %d, ceiling %d (want 3 and at most the five-set ceiling)", byKey["BACKUP_KEEP"].Default, byKey["BACKUP_KEEP"].Max)
	}
	for _, retired := range []string{"KEEP_DB", "DB_BACKUP_DAYS", "SITE_BACKUP_DAYS", "SITE_BACKUP_KEEP", "DAILY_BACKUP_HOUR"} {
		if _, ok := byKey[retired]; ok {
			t.Errorf("%s still exists beside the one retention policy", retired)
		}
	}
	if got := readBackupSettings(); got.Enabled || got.Keep != 3 || got.SafetyKeep != 2 || got.Replicate || got.EveryDays != 1 || got.Hour != 2 {
		t.Errorf("settings with no file = %+v", got)
	}
}

func TestAtMostFiveBackupSetsAreKeptInAll(t *testing.T) {
	byKey := map[string]retentionSetting{}
	for _, s := range retentionSchema {
		byKey[s.Key] = s
	}
	if d := byKey["BACKUP_KEEP"].Default + byKey["SAFETY_BACKUP_KEEP"].Default; d != maxBackupSets {
		t.Errorf("the defaults keep %d sets, want %d (three regular, two protective)", d, maxBackupSets)
	}
	if byKey["BACKUP_KEEP"].Max+byKey["SAFETY_BACKUP_KEEP"].Min > maxBackupSets {
		t.Error("the regular pool's ceiling with the safety floor exceeds five sets")
	}
	// An older file (the previous default was seven plus three) resolves to what is in force: a value out of bounds is its
	// default, and the two pools together never exceed five.
	useRetentionConf(t, "BACKUP_KEEP=7\nSAFETY_BACKUP_KEEP=3\nBACKUP_REPLICATE=1\n")
	got := readBackupSettings()
	if got.Keep+got.SafetyKeep > maxBackupSets || got.SafetyKeep < 2 || got.Keep < 1 {
		t.Errorf("an old seven-plus-three file resolved to %d + %d", got.Keep, got.SafetyKeep)
	}
	if !got.Replicate {
		t.Error("BACKUP_REPLICATE=1 was not read")
	}
	useRetentionConf(t, "BACKUP_KEEP=3\nSAFETY_BACKUP_KEEP=4\n")
	if got := readBackupSettings(); got.Keep != 1 || got.SafetyKeep != 4 {
		t.Errorf("three plus four resolved to %d + %d, want 1 + 4", got.Keep, got.SafetyKeep)
	}
}

func TestReadRetentionUsesDefaultsWhenTheFileIsAbsent(t *testing.T) {
	got := readRetention()
	if len(got) != len(retentionSchema) {
		t.Fatalf("read %d settings, want %d", len(got), len(retentionSchema))
	}
	for _, s := range got {
		if s.Value != s.Default {
			t.Errorf("%s resolved to %d with no file present; it should carry its default %d", s.Key, s.Value, s.Default)
		}
	}
}

func TestTimeOfDayIsBounded(t *testing.T) {
	// The schedule is interpolated into a systemd OnCalendar line; reject anything not a time of day.
	for _, bad := range []string{"24:00", "3:30", "03:60", "", "03:30:00", "*-*-* 03:30", "03:30; rm -rf /"} {
		if reTimeOfDay.MatchString(bad) {
			t.Errorf("%q was accepted as a time of day", bad)
		}
	}
	for _, good := range []string{"00:00", "03:30", "23:59"} {
		if !reTimeOfDay.MatchString(good) {
			t.Errorf("%q is a valid time of day and was rejected", good)
		}
	}
}

func TestConfLineParsingIgnoresWhatItShould(t *testing.T) {
	// The file is shell-sourced, so it may contain comments and quoting.
	dir := t.TempDir()
	f := filepath.Join(dir, "conf")
	if err := os.WriteFile(f, []byte(
		"# a comment\n\nBACKUP_KEEP=14\nKEEP_BINARIES=\"9\"\n  DISK_WARN = 75 # trailing\nNOT_A_SETTING=1\nBACKUP_KEEP=broken\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, line := range []struct {
		in       string
		key      string
		val      string
		matching bool
	}{
		{"BACKUP_KEEP=14", "BACKUP_KEEP", "14", true},
		{`KEEP_BINARIES="9"`, "KEEP_BINARIES", "9", true},
		{"  DISK_WARN = 75 # trailing", "DISK_WARN", "75", true},
		{"BACKUP_KEEP=broken", "", "", false},
		{"# a comment", "", "", false},
	} {
		m := reConfLine.FindStringSubmatch(line.in)
		if (m != nil) != line.matching {
			t.Errorf("%q: matched=%v, want %v", line.in, m != nil, line.matching)
			continue
		}
		if m != nil && (m[1] != line.key || m[2] != line.val) {
			t.Errorf("%q parsed as %s=%s, want %s=%s", line.in, m[1], m[2], line.key, line.val)
		}
	}
}
