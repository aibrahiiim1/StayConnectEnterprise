package main

import (
	"context"
	"os"
	"path/filepath"
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
