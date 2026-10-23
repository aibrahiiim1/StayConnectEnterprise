package main

// System-log retention: the appliance's logs hold device MAC/IP and sign-in identifiers (Caddy access log, Kea, code senders, journal) and are
// kept for the site's log_days (default 14), applied hourly by scd as root. The journal is vacuumed (journald archives at least daily).
// Each log file in the listed directories is CUT once a day (copied to <name>.<YYYYMMDD>, emptied in place; writers all append);
// cuts and other files older than the period are deleted, and a stale live file is emptied, not deleted, since a process may hold it.
// Never by name alone: the directories are writable by the logging services and this job is root, so each file is opened with O_NOFOLLOW,
// checked on the open descriptor (regular, same filesystem, one name) and unlinked only after the name is re-checked to be the same file.
// A planted symlink or hard link is left alone.

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const (
	logRetentionEvery       = time.Hour
	logRetentionDefaultDays = 14 // used only when the setting cannot be read (a database before 0134)
)

type logRetention struct {
	dirs    []string
	stamp   string
	journal func(ctx context.Context, days int) error
	now     func() time.Time
}

func defaultLogRetention() *logRetention {
	return &logRetention{
		dirs:  []string{"/var/log/stayconnect", "/var/log/caddy", "/var/log/kea"},
		stamp: "/var/lib/stayconnect/log-retention.last-cut",
		journal: func(ctx context.Context, days int) error {
			out, err := exec.CommandContext(ctx, "journalctl", "--vacuum-time="+strconv.Itoa(days)+"d").CombinedOutput()
			if err != nil {
				return fmt.Errorf("journalctl --vacuum-time: %w: %s", err, out)
			}
			return nil
		},
		now: time.Now,
	}
}

func (s *server) logRetentionLoop(ctx context.Context) {
	job := defaultLogRetention()
	t := time.NewTicker(logRetentionEvery)
	defer t.Stop()
	for {
		job.run(ctx, s.logRetentionDays(ctx))
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// logRetentionDays returns 0 (do nothing this hour) when the setting exists but cannot be read: a database restart at the tick must
// never fall back to the default and delete logs a hotel chose to keep longer.
func (s *server) logRetentionDays(ctx context.Context) int {
	if s.db == nil || s.tenID == "" || s.siteID == "" {
		return logRetentionDefaultDays
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var days int
	err := s.db.QueryRow(cctx, `SELECT log_days FROM iam_v2.data_retention_get($1::uuid,$2::uuid)`,
		s.tenID, s.siteID).Scan(&days)
	if err == nil && days >= 1 {
		return days
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "42883" || pgErr.Code == "3F000") {
		slog.Warn("log retention: this database predates the setting; using the default", "days", logRetentionDefaultDays)
		return logRetentionDefaultDays
	}
	if ctx.Err() == nil {
		slog.Warn("log retention: the setting could not be read, so nothing is deleted this hour", "err", err)
	}
	return 0
}

// cutName matches the copies this job makes (<name>.<YYYYMMDD>), the numbered ones Kea makes itself
// (kea-dhcp4.log.1) and the timestamped ones Caddy rolls (portal-2026-10-19T10-00-00.000.log).
var cutName = regexp.MustCompile(`\.(\d{8}|\d+)(\.gz)?$|-\d{4}-\d{2}-\d{2}T[\d.-]+\.log(\.gz)?$`)

func (j *logRetention) run(ctx context.Context, days int) {
	if days < 1 {
		return
	}
	now := j.now()
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	if err := j.journal(ctx, days); err != nil && ctx.Err() == nil {
		slog.Warn("log retention: journal not vacuumed", "err", err)
	}
	today := now.Format("20060102")
	cutToday := j.lastCut() != today
	removed, emptied, cut := 0, 0, 0
	for _, dir := range j.dirs {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("log retention: directory not readable", "dir", dir, "err", err)
			}
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			slog.Warn("log retention: directory not readable", "dir", dir, "err", err)
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			switch j.handle(dirInfo, path, cutName.MatchString(e.Name()), cutoff, cutToday, today) {
			case "removed":
				removed++
			case "emptied":
				emptied++
			case "cut":
				cut++
			}
		}
	}
	if cutToday {
		j.recordCut(today)
	}
	if removed+emptied+cut > 0 {
		slog.Info("log retention", "days", days, "cut", cut, "removed", removed, "emptied", emptied)
	}
}

// handle opens one file without following links and acts through the descriptor, not what the directory listing said; it returns what it did, or "" for nothing.
func (j *logRetention) handle(dirInfo fs.FileInfo, path string, isCut bool, cutoff time.Time, cutToday bool, today string) string {
	f, err := openLogNoFollow(path, os.O_RDWR)
	if err != nil {
		return "" // a symlink (ELOOP), or gone since the listing
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !logFileInPlace(dirInfo, info) {
		slog.Warn("log retention: not a log file of its own; left alone", "file", path)
		return ""
	}
	switch {
	case info.ModTime().Before(cutoff) && isCut:
		// Unlink only if the name still refers to the file just checked. Closed first: Windows, where the unit
		// tests run, cannot remove an open file; the identity check does not need the descriptor.
		f.Close()
		if now, err := os.Lstat(path); err == nil && os.SameFile(info, now) && os.Remove(path) == nil {
			return "removed"
		}
	case info.ModTime().Before(cutoff):
		if f.Truncate(0) == nil {
			return "emptied"
		}
	case !isCut && cutToday && info.Size() > 0:
		if err := cutLog(f, info, path+"."+today); err != nil {
			slog.Warn("log retention: log not cut", "file", path, "err", err)
			return ""
		}
		return "cut"
	}
	return ""
}

// cutLog copies the opened log to dst (refusing to overwrite or write through a symlink) and empties it in place, so a writer keeping it open goes on appending.
func cutLog(in *os.File, info fs.FileInfo, dst string) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return in.Truncate(0)
}

func (j *logRetention) lastCut() string {
	b, err := os.ReadFile(j.stamp)
	if err != nil {
		return ""
	}
	return string(b)
}

func (j *logRetention) recordCut(day string) {
	_ = os.MkdirAll(filepath.Dir(j.stamp), 0o700)
	if err := os.WriteFile(j.stamp, []byte(day), 0o644); err != nil {
		slog.Warn("log retention: the day of the cut was not recorded", "err", err)
	}
}
