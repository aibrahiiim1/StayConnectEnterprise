package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// GDPR plan F5 (migration 0134): a log is cut once a day, cuts and other files older than the period are deleted,
// a live file nobody has written for longer than the period is emptied in place, and the journal is vacuumed to
// the same period.
func TestLogRetention_CutsDeletesAndEmpties(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	write := func(name, body string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	live := write("portal.log", "today's lines\n", time.Hour)
	quiet := write("otp-sms.log", "old lines\n", 20*24*time.Hour)
	oldCut := write("portal.log.20261001", "old\n", 18*24*time.Hour)
	recentCut := write("portal.log.20261015", "recent\n", 4*24*time.Hour)
	keaRolled := write("kea-dhcp4.log.1", "old\n", 15*24*time.Hour)
	caddyRolled := write("portal-2026-10-01T03-00-00.000.log", "old\n", 17*24*time.Hour)

	vacuumed := 0
	j := &logRetention{dirs: []string{dir, filepath.Join(dir, "missing")}, stamp: filepath.Join(dir, "state", "last-cut"),
		journal: func(_ context.Context, days int) error { vacuumed = days; return nil }, now: func() time.Time { return now }}
	j.run(context.Background(), 14)

	if vacuumed != 14 {
		t.Fatalf("journal vacuumed to %d days, want 14", vacuumed)
	}
	if b, _ := os.ReadFile(live); len(b) != 0 {
		t.Fatalf("the live log was not cut: %q", b)
	}
	if b, err := os.ReadFile(live + ".20261019"); err != nil || string(b) != "today's lines\n" {
		t.Fatalf("the cut does not hold the day's lines: %q %v", b, err)
	}
	if b, err := os.ReadFile(quiet); err != nil || len(b) != 0 {
		t.Fatalf("a quiet live log older than the period was not emptied in place: %q %v", b, err)
	}
	for _, p := range []string{oldCut, keaRolled, caddyRolled} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s outlived the period", filepath.Base(p))
		}
	}
	if _, err := os.Stat(recentCut); err != nil {
		t.Fatal("a cut inside the period was removed")
	}

	// The same day again: no second cut, and the day's new lines stay in the live file.
	if err := os.WriteFile(live, []byte("more\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	j.run(context.Background(), 14)
	if b, _ := os.ReadFile(live); string(b) != "more\n" {
		t.Fatalf("the log was cut twice in one day: %q", b)
	}
}

// The log directories are writable by the services that log there and this job runs as root: a symlink -- or a
// hard link -- planted in one must never lead it to empty, copy or remove a file elsewhere. handle is called
// directly as well as through run, because the dangerous case is a name swapped for a link AFTER the listing.
func TestLogRetention_NeverFollowsALink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	target := func(name string, age time.Duration) string {
		p := filepath.Join(outside, name)
		if err := os.WriteFile(p, []byte("not a log\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		_ = os.Chtimes(p, at, at)
		return p
	}
	recent, old, oldCut := target("recent", time.Hour), target("old", 30*24*time.Hour), target("old-cut", 30*24*time.Hour)
	links := map[string]string{
		"evil.log":          recent, // would be cut
		"quiet.log":         old,    // would be emptied
		"evil.log.20261001": oldCut, // would be removed
	}
	for name, to := range links {
		if err := os.Symlink(to, filepath.Join(dir, name)); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
	}
	j := &logRetention{dirs: []string{dir}, stamp: filepath.Join(t.TempDir(), "last-cut"),
		journal: func(context.Context, int) error { return nil }, now: func() time.Time { return now }}
	dirInfo, _ := os.Stat(dir)
	cutoff := now.Add(-14 * 24 * time.Hour)
	for name := range links {
		if got := j.handle(dirInfo, filepath.Join(dir, name), cutName.MatchString(name), cutoff, true, "20261019"); got != "" {
			t.Fatalf("handle(%s) = %q through a symlink", name, got)
		}
	}
	j.run(context.Background(), 14)

	for _, p := range []string{recent, old, oldCut} {
		if b, err := os.ReadFile(p); err != nil || string(b) != "not a log\n" {
			t.Fatalf("%s was touched through a symlink: %q %v", filepath.Base(p), b, err)
		}
	}
	for name := range links {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("the planted link %s was removed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.log.20261019")); err == nil {
		t.Fatal("a symlinked file was copied into a cut")
	}

	// A hard link to a file elsewhere has a second name: left alone too (Linux checks the link count).
	if runtime.GOOS == "linux" {
		hard := filepath.Join(dir, "hard.log")
		if err := os.Link(old, hard); err != nil {
			t.Fatalf("hard link: %v", err)
		}
		if got := j.handle(dirInfo, hard, false, cutoff, true, "20261019"); got != "" {
			t.Fatalf("handle(hard link) = %q", got)
		}
		if b, _ := os.ReadFile(old); string(b) != "not a log\n" {
			t.Fatal("a hard-linked file was emptied")
		}
	}
}

// A setting that could not be read must never delete anything: a hotel that kept logs for a year would lose them
// to a database restart at the hourly tick.
func TestLogRetention_AnUnreadableSettingDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	old := filepath.Join(dir, "portal.log.20250101")
	if err := os.WriteFile(old, []byte("kept\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	at := now.Add(-300 * 24 * time.Hour)
	_ = os.Chtimes(old, at, at)
	vacuumed := -1
	j := &logRetention{dirs: []string{dir}, stamp: filepath.Join(dir, "state", "last-cut"),
		journal: func(_ context.Context, days int) error { vacuumed = days; return nil }, now: func() time.Time { return now }}
	j.run(context.Background(), 0) // what logRetentionDays returns when the setting exists but cannot be read
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("an old log was deleted although the setting could not be read: %v", err)
	}
	if vacuumed != -1 {
		t.Fatalf("the journal was vacuumed to %d days although the setting could not be read", vacuumed)
	}
}
