package main

// WHAT A RESTORE LEAVES BEHIND (GDPR final mission).
//
// A restore keeps the database it replaced (<db>_before_<stamp>) so it can be undone. That copy still holds everything the
// restored database was cleaned of -- guests erased since, expired records -- and nothing ever removed the LAST one. It is
// removed here, hourly, once it is older than the SET_ASIDE_DAYS setting, never while a restore is running. And an
// interrupted write leaves a *.partial file in the backup folders; one older than a day is a dead write, and it is removed.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var reAside = regexp.MustCompile(`^` + regexp.QuoteMeta(pgDatabase) + `_before_(\d{8}t\d{6}z)$`)

// asideExpired says whether a set-aside database is older than days. A name that is not exactly ours is never expired.
func asideExpired(name string, now time.Time, days int) bool {
	m := reAside.FindStringSubmatch(name)
	if m == nil || days < 1 {
		return false
	}
	at, err := time.Parse("20060102t150405z", m[1])
	if err != nil {
		return false
	}
	return now.Sub(at) > time.Duration(days)*24*time.Hour
}

func setAsideDays() int {
	for _, r := range readRetention() {
		if r.Key == "SET_ASIDE_DAYS" {
			if r.Value >= r.Min && r.Value <= r.Max {
				return r.Value
			}
			return r.Default
		}
	}
	return 14
}

// sweepSetAside drops the set-aside databases that have expired. It returns how many were dropped.
func (s *server) sweepSetAside(ctx context.Context) int {
	if readMaintenance().Active {
		return 0
	}
	// Only when the live database is where it should be and no restore has left a staging database: after a restore that
	// failed half way, the set-aside database may be the only copy of the data, and no age makes it disposable.
	state, err := execPsql(ctx, "postgres", "SELECT datname FROM pg_database WHERE datname = '"+pgDatabase+`' OR datname LIKE 'restore\_%' OR datname LIKE '`+pgDatabase+`\_before\_%';`)
	if err != nil {
		return 0
	}
	names := strings.Fields(state)
	live := false
	for _, n := range names {
		if n == pgDatabase {
			live = true
		}
		if strings.HasPrefix(n, "restore_") {
			slog.Warn("set-aside databases are kept: a restore staging database exists", "database", n)
			return 0
		}
	}
	if !live {
		slog.Error("set-aside databases are kept: the live database is missing", "database", pgDatabase)
		return 0
	}
	out := state
	days, dropped := setAsideDays(), 0
	for _, name := range strings.Fields(out) {
		if !asideExpired(name, time.Now().UTC(), days) {
			continue
		}
		if _, err := execPsql(ctx, "postgres", "DROP DATABASE IF EXISTS "+name+" WITH (FORCE);"); err != nil {
			slog.Warn("an expired set-aside database could not be dropped", "database", name, "err", err)
			continue
		}
		slog.Info("set-aside database removed: it passed its retention period", "database", name, "days", days)
		dropped++
	}
	return dropped
}

// sweepPartials removes *.partial files older than maxAge from dir. The name is the contract: a writer renames its
// .partial file away on success, so one that is old is the remains of a write that never finished.
func sweepPartials(dir string, now time.Time, maxAge time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".partial") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}

// runLeftovers is the hourly sweep of what a restore or an interrupted write leaves behind.
func (s *server) runLeftovers(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s.sweepSetAside(cctx)
	for _, dir := range []string{nightlyBackupDir, filepath.Join(nightlyBackupDir, "backups", "db"), complianceArchiveDir, filepath.Dir(erasureJournalFile())} {
		sweepPartials(dir, time.Now(), 24*time.Hour)
	}
}
