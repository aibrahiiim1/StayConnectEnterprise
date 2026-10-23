package main

// A restore keeps the database it replaced (<db>_before_<stamp>) for undo; it is removed hourly once older than SET_ASIDE_DAYS, never while a restore runs.
// *.partial files in the backup folders older than a day are dead writes and removed too.

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

var reStaging = regexp.MustCompile(`^restore_(\d{8}t\d{6}z)$`)

// How long a staging database may exist before it counts as the remains of an interrupted restore (restores take minutes; the longest step is bounded to an hour).
const stagingOrphanAfter = 6 * time.Hour

// A name that is not exactly ours is never orphaned.
func stagingOrphaned(name string, now time.Time) bool {
	m := reStaging.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	at, err := time.Parse("20060102t150405z", m[1])
	if err != nil {
		return false
	}
	return now.Sub(at) > stagingOrphanAfter
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

// Removes the staging database of an interrupted restore: a FULL COPY of personal data with no retention applied. It does not wait for the
// maintenance flag (a killed restore leaves it too). Safe at any time because a running restore is bounded far below stagingOrphanAfter and it
// only runs while the live database exists: between the swap renames staging IS the restored data. Returns names that may belong to a running restore.
func sweepOrphanStaging(ctx context.Context) (remaining []string, liveExists bool, state string) {
	state, err := execPsql(ctx, "postgres", "SELECT datname FROM pg_database WHERE datname = '"+pgDatabase+`' OR datname LIKE 'restore\_%' OR datname LIKE '`+pgDatabase+`\_before\_%';`)
	if err != nil {
		return nil, false, ""
	}
	for _, n := range strings.Fields(state) {
		if n == pgDatabase {
			liveExists = true
		}
	}
	for _, n := range strings.Fields(state) {
		if !strings.HasPrefix(n, "restore_") {
			continue
		}
		if liveExists && stagingOrphaned(n, time.Now().UTC()) {
			if _, derr := execPsql(ctx, "postgres", "DROP DATABASE IF EXISTS "+n+" WITH (FORCE);"); derr == nil {
				slog.Warn("the staging database of an interrupted restore was removed", "database", n)
				continue
			}
			slog.Warn("the staging database of an interrupted restore could not be removed", "database", n)
		}
		remaining = append(remaining, n)
	}
	return remaining, liveExists, state
}

func (s *server) sweepSetAside(ctx context.Context) int {
	remaining, live, state := sweepOrphanStaging(ctx)
	if readMaintenance().Active {
		return 0
	}
	// Only when the live database is where it should be and no restore has left a staging database: after a restore that
	// failed half way, the set-aside database may be the only copy of the data, and no age makes it disposable.
	if state == "" {
		return 0
	}
	if len(remaining) > 0 {
		slog.Warn("set-aside databases are kept: a restore staging database exists", "database", remaining[0])
		return 0
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

// The name is the contract: a writer renames its .partial away on success, so an old one is a write that never finished.
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

// Folders an interrupted write can leave a .partial in: scdBackupDir (crashed backup dump and config partials) and those of earlier backups, the compliance archive and the ledger.
func leftoverDirs() []string {
	return []string{scdBackupDir, nightlyBackupDir, complianceArchiveDir, filepath.Dir(ledgerLocalPath)}
}

func (s *server) runLeftovers(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s.sweepSetAside(cctx)
	for _, dir := range leftoverDirs() {
		sweepPartials(dir, time.Now(), 24*time.Hour)
	}
}
