package main

// RETENTION IS A SETTING, AND A SETTING NEEDS BOUNDS THAT MEAN SOMETHING.
//
// These are not range checks for their own sake. Each lower bound is the point below which the appliance
// loses its own ability to recover, and the cross-field rule is the one an operator is most likely to get
// wrong in a way that looks stricter and is actually broken.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetentionDefaultsMatchTheSweepsOwn(t *testing.T) {
	// The screen must show what is IN FORCE. The sweep falls back to these exact numbers when the file is
	// absent, which is the state of a freshly onboarded appliance, so the schema has to agree with it or the
	// operator reads values the appliance is not using.
	want := map[string]int{
		"KEEP_BINARIES": 5, "KEEP_RELEASES": 5, "KEEP_CONFIG": 3, "DISK_WARN": 80, "DISK_CRIT": 90,
		"RESTORE_TEST_MONTHS": 6, "SET_ASIDE_DAYS": 14, "BACKUP_ENABLED": 0, "BACKUP_HOUR": 2, "BACKUP_EVERY_DAYS": 1,
		"BACKUP_KEEP": 7, "SAFETY_BACKUP_KEEP": 3,
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
	// the backups a restore, a migration or a deployment take first: at least two are always kept
	if byKey["SAFETY_BACKUP_KEEP"].Min < 2 {
		t.Error("the safety backups floor may be set below two")
	}
}

// SCHEDULED BACKUPS ARE OFF UNTIL AN ADMINISTRATOR SWITCHES THEM ON, and the retention is one count with a safe ceiling: no age rule.
func TestScheduledBackupsAreOffByDefaultAndRetentionIsOneCount(t *testing.T) {
	byKey := map[string]retentionSetting{}
	for _, s := range retentionSchema {
		byKey[s.Key] = s
	}
	if byKey["BACKUP_ENABLED"].Default != 0 {
		t.Fatal("scheduled backups are on by default")
	}
	if byKey["BACKUP_KEEP"].Default != 7 || byKey["BACKUP_KEEP"].Max > 60 {
		t.Errorf("backups kept: default %d, ceiling %d (want 7 and a safe ceiling)", byKey["BACKUP_KEEP"].Default, byKey["BACKUP_KEEP"].Max)
	}
	for _, retired := range []string{"KEEP_DB", "DB_BACKUP_DAYS", "SITE_BACKUP_DAYS", "SITE_BACKUP_KEEP", "DAILY_BACKUP_HOUR"} {
		if _, ok := byKey[retired]; ok {
			t.Errorf("%s still exists beside the one retention policy", retired)
		}
	}
	if got := readBackupSettings(); got.Enabled || got.Keep != 7 || got.SafetyKeep != 3 || got.EveryDays != 1 || got.Hour != 2 {
		t.Errorf("settings with no file = %+v", got)
	}
}

func TestReadRetentionUsesDefaultsWhenTheFileIsAbsent(t *testing.T) {
	// The appliance this was written for had no config file at all. Absent must mean "the defaults are in
	// force", not "nothing is in force".
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
	// The schedule is interpolated into a systemd OnCalendar line. A value that is not a time of day is a
	// value that ends up in a unit file.
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
	// The file is shell-sourced by the sweep, so it may legitimately contain comments and quoting. Reading it
	// back must agree with what the sweep would see.
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
