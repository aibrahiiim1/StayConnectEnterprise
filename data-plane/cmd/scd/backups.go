package main

// THE BACKUP SYSTEM: ONE ENGINE, ONE FORMAT, ONE RETENTION POLICY.
//
// This is the only way the appliance takes a backup. A backup is the database and the configuration and keys under
// /etc/stayconnect, both encrypted to the hotel's backup key holders AS THEY ARE WRITTEN (the appliance holds public keys only, so
// it cannot read them back; any one holder's key can). Nothing is ever written in the clear, and a backup that cannot be encrypted
// is not taken: the administrator is told, and nothing weaker is written instead.
//
// A BACKUP THAT CANNOT BE RESTORED IS NOT A BACKUP. The encrypted dump cannot be opened here, so it is verified while it is made:
// the same plain SQL that is being encrypted is also loaded into a scratch database, which must accept it without one error, hold
// the appliance's tables, and carry an intact erasure ledger. Only then is the backup kept, marked verified, and described in a
// small plain note (what it is for, who asked, the SHA-256 of what is stored, the tables, and the ledger head it contains -- one of
// the witnesses a restore uses to tell whether a copy of the erasure ledger is complete).
//
// KINDS. Four reasons to take one, one engine:
//
//	scheduled    the hotel switched the schedule on (OFF by default) and set when
//	manual       an administrator asked for one in Hotel Admin
//	pre-change   a controlled migration or deployment asked for one first (scripts/edge-migrate.sh refuses a live-site migration
//	             without a fresh one), through `scd backup-now`
//	pre-restore  a restore takes one before it touches anything; the restoring key holder's key is also a recipient, so a restore
//	             onto a fresh appliance that has no registered holder yet is still protected
//
// THE SCHEDULE IS A CONVENIENCE; THE PROTECTIONS ARE NOT. Switching scheduled backups off stops only the first kind. The last two
// are mandatory, are taken whatever the schedule says, and are kept by their own rule.
//
// ONE RETENTION POLICY, COUNTS ONLY. scheduled and manual backups are one pool: the newest BACKUP_KEEP are kept. pre-change and
// pre-restore backups are the other: the newest SAFETY_BACKUP_KEEP are kept. A set (dump, configuration archive, note, marker) is
// removed only after a newer one of its pool has been verified, and the newest of a pool is never removed. No age rule exists
// beside the count, so the two cannot contradict each other.
//
// NOTHING BEFORE THIS IS DELETED. Backups taken by the retired mechanisms (the nightly site backup in /var/backups/stayconnect, and
// on-demand dumps made before notes existed) have no note, are never touched by rotation or the sweep, are listed as "earlier
// backups", and go only when an administrator removes them, after a verified backup of the new kind exists.
//
// Every day the stored files are checked against the digests in their notes, so silent corruption is noticed while there are still
// other backups to restore from.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

// etcStayconnectDir is the configuration and key material archived with the database; a var so tests can use a folder.
var etcStayconnectDir = "/etc/stayconnect"

// financialMarker is deliberately NOT archived: it must survive a restore and keep counting (see the financial recovery notes in restore_financial.go).
const financialMarker = "financial-restore-generation.json"

// backupMeta is the note kept beside each backup. It holds no personal data.
type backupMeta struct {
	Kind       string    `json:"kind"` // scheduled | manual | pre-change | pre-restore
	Reason     string    `json:"reason,omitempty"`
	By         string    `json:"by,omitempty"`
	Name       string    `json:"name"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	EtcName    string    `json:"etc_name,omitempty"`
	EtcBytes   int64     `json:"etc_size_bytes,omitempty"`
	EtcSHA256  string    `json:"etc_sha256,omitempty"`
	EtcFiles   int       `json:"etc_files"`
	TakenAt    time.Time `json:"taken_at"`
	Tables     int       `json:"tables"`
	Encrypted  bool      `json:"encrypted"`
	Restorable bool      `json:"restorable"`
	VerifiedAt time.Time `json:"verified_at"`
	LedgerID   string    `json:"ledger_id,omitempty"`
	LedgerSeq  int64     `json:"ledger_seq"`
	LedgerHash string    `json:"ledger_hash,omitempty"`
}

func metaPathFor(name string) string {
	return filepath.Join(scdBackupDir, strings.TrimSuffix(strings.TrimSuffix(name, backupcrypt.Suffix), ".sql.gz")+".meta.json")
}

var errNoBackupKeyHolder = errors.New("no backup key holder is set up: a backup is always encrypted, so none was taken (Hotel Admin → Backups → Backup keys)")

var backupMu sync.Mutex

const (
	kindScheduled  = "scheduled"
	kindManual     = "manual"
	kindPreChange  = "pre-change"
	kindPreRestore = "pre-restore"
)

// poolOf says which retention pool a kind belongs to: "regular" (scheduled, manual) or "protective" (pre-change, pre-restore).
func poolOf(kind string) string {
	if kind == kindPreChange || kind == kindPreRestore {
		return "protective"
	}
	return "regular"
}

// backupOpts says why a backup is being taken.
type backupOpts struct {
	Kind   string
	Reason string
	By     string
	// Identity is the restoring key holder's key (pre-restore only): its public half is added to the recipients.
	Identity string
}

// recipientsFor is who a backup is encrypted to: the registered key holders, plus -- for a pre-restore backup -- the holder whose
// key is being used to restore, so that the safety copy exists even where no holder is registered yet (a replacement appliance).
func (s *server) recipientsFor(ctx context.Context, identity string) ([]age.Recipient, error) {
	keys, err := s.backupRecipientKeys(ctx)
	if err != nil {
		return nil, err
	}
	return recipientsFromKeys(keys, identity)
}

// recipientsFromKeys is recipientsFor without the database: the registered holders' public keys, and the restoring holder's.
func recipientsFromKeys(keys []string, identity string) ([]age.Recipient, error) {
	keys = append([]string(nil), keys...)
	if strings.TrimSpace(identity) != "" {
		if id, perr := backupcrypt.ParseIdentity(identity); perr == nil {
			pub := id.Recipient().String()
			seen := false
			for _, k := range keys {
				if k == pub {
					seen = true
				}
			}
			if !seen {
				keys = append(keys, pub)
			}
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return backupcrypt.ParseRecipients(keys)
}

// takeBackup takes, verifies and records one backup, then rotates. It returns the note of the new backup.
func (s *server) takeBackup(ctx context.Context, o backupOpts) (*backupMeta, error) {
	switch o.Kind {
	case kindScheduled, kindManual, kindPreChange, kindPreRestore:
	default:
		return nil, fmt.Errorf("unknown backup kind %q", o.Kind)
	}
	backupMu.Lock()
	defer backupMu.Unlock()
	// A pre-restore backup is the restore's own first step; every other kind waits for a restore to finish.
	if o.Kind != kindPreRestore && (restoreInFlight.Load() || readMaintenance().Active) {
		return nil, errors.New("a restore is running")
	}
	recipients, err := s.recipientsFor(ctx, o.Identity)
	if err != nil {
		return nil, err
	}
	if len(recipients) == 0 {
		return nil, errNoBackupKeyHolder
	}
	if err := os.MkdirAll(scdBackupDir, 0o750); err != nil {
		return nil, fmt.Errorf("the backup folder could not be created: %w", err)
	}
	taken := time.Now().UTC()
	stamp := taken.Format("20060102T150405Z")
	prefix := "db-"
	if o.Kind == kindPreRestore {
		prefix = "db-safety-"
	}
	name := prefix + stamp + ".sql.gz" + backupcrypt.Suffix
	dest := filepath.Join(scdBackupDir, name)
	if _, err := os.Stat(dest); err == nil {
		return nil, errors.New("a backup with this name was taken a moment ago")
	}
	partial := dest + ".partial"
	scratch := "verify_" + strings.ToLower(stamp)

	if out, err := exec.CommandContext(ctx, "docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc", "CREATE DATABASE "+scratch).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("a scratch database could not be created: %s", tailLine(string(out)))
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(dctx, "docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc",
			"DROP DATABASE IF EXISTS "+scratch+" WITH (FORCE)").CombinedOutput(); err != nil {
			slog.Error("scratch verification database not dropped", "db", scratch, "detail", tailLine(string(out)))
		}
	}()

	// The dump is encrypted to the file AND loaded into the scratch database, from the same stream.
	loader := exec.CommandContext(ctx, "docker", "exec", "-i", pgContainer, "psql", "-U", pgUser, "-d", scratch, "-q", "-v", "ON_ERROR_STOP=1")
	stdin, err := loader.StdinPipe()
	if err != nil {
		return nil, err
	}
	var loaderOut strings.Builder
	loader.Stdout, loader.Stderr = &loaderOut, &loaderOut
	if err := loader.Start(); err != nil {
		return nil, fmt.Errorf("the verification loader could not start: %w", err)
	}
	dumpErr := writeDumpTee(ctx, partial, recipients, stdin)
	_ = stdin.Close()
	loadErr := loader.Wait()
	if dumpErr != nil {
		_ = os.Remove(partial)
		return nil, fmt.Errorf("the dump failed: %w", dumpErr)
	}
	if loadErr != nil {
		_ = os.Remove(partial)
		return nil, fmt.Errorf("the backup could not be restored into a scratch database, so it was discarded: %s", firstProblem(loaderOut.String()))
	}
	if err := os.Rename(partial, dest); err != nil {
		_ = os.Remove(partial)
		return nil, fmt.Errorf("the completed dump could not be moved into place: %w", err)
	}
	_ = os.Chmod(dest, 0o640)
	if gid, ok := serviceGroupID(); ok {
		_ = os.Chown(dest, 0, gid)
	}
	fi, err := os.Stat(dest)
	if err != nil || fi.Size() < 1024 {
		_ = os.Remove(dest)
		return nil, errors.New("the dump was empty and has been discarded")
	}
	cnt, _ := exec.CommandContext(ctx, "docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", scratch, "-tAc",
		"SELECT count(*) FROM information_schema.tables WHERE table_schema IN ('public','iam_v2')").Output()
	tables := 0
	fmt.Sscanf(strings.TrimSpace(string(cnt)), "%d", &tables)
	if tables == 0 {
		_ = os.Remove(dest)
		return nil, errors.New("the backup loaded but produced no tables, so it was discarded")
	}
	head, herr := psqlLedgerHead(ctx, scratch)
	if herr == nil && head.Has && !head.Intact {
		_ = os.Remove(dest)
		return nil, errors.New("the erasure ledger inside the backup is damaged, so the backup was discarded")
	}
	sum, err := fileDigest(dest)
	if err != nil {
		return nil, fmt.Errorf("the backup could not be digested: %w", err)
	}
	meta := &backupMeta{Kind: o.Kind, Reason: strings.TrimSpace(o.Reason), By: strings.TrimSpace(o.By), Name: name, SizeBytes: fi.Size(),
		SHA256: sum, TakenAt: taken, Tables: tables, Encrypted: true, Restorable: true, VerifiedAt: time.Now().UTC()}
	if head.Has {
		meta.LedgerID, meta.LedgerSeq, meta.LedgerHash = head.Ledger, head.Seq, head.Hash
	}

	// The configuration and keys.
	etcName := "etc-" + stamp + ".tgz" + backupcrypt.Suffix
	if n, err := writeEtcArchive(filepath.Join(scdBackupDir, etcName), recipients); err != nil {
		slog.Error("backup: the configuration and keys could not be archived", "err", err)
		_ = os.Remove(dest)
		return nil, fmt.Errorf("the configuration and keys could not be archived, so the backup was discarded: %w", err)
	} else if n >= 0 {
		efi, _ := os.Stat(filepath.Join(scdBackupDir, etcName))
		esum, _ := fileDigest(filepath.Join(scdBackupDir, etcName))
		meta.EtcName, meta.EtcFiles, meta.EtcSHA256 = etcName, n, esum
		if efi != nil {
			meta.EtcBytes = efi.Size()
		}
	}
	if err := writeMetaAtomically(meta); err != nil {
		return nil, err
	}
	writeVerifiedMarker(name, tables)
	s.rotateBackups()
	slog.Info("backup complete and verified", "backup", name, "kind", o.Kind, "tables", tables, "ledger_entries", meta.LedgerSeq)
	return meta, nil
}

func writeMetaAtomically(m *backupMeta) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	p := metaPathFor(m.Name)
	tmp := p + ".partial"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if gid, ok := serviceGroupID(); ok {
		_ = os.Chown(p, 0, gid)
	}
	return nil
}

// writeEtcArchive writes /etc/stayconnect (without the financial restore marker) as a gzip tar, encrypted as it is written. It
// returns the number of files, or -1 when the folder does not exist (an appliance under construction).
func writeEtcArchive(dest string, recipients []age.Recipient) (int, error) {
	if _, err := os.Stat(etcStayconnectDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return -1, nil
		}
		return 0, err
	}
	partial := dest + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { f.Close(); _ = os.Remove(partial) }()
	w, err := backupcrypt.Encrypt(f, recipients)
	if err != nil {
		return 0, err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	files := 0
	base := filepath.Dir(etcStayconnectDir)
	walkErr := filepath.WalkDir(etcStayconnectDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == financialMarker {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // sockets, devices, symlinks are not configuration
		}
		rel, _ := filepath.Rel(base, p)
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, in)
			in.Close()
			if err != nil {
				return err
			}
			files++
		}
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(partial, dest); err != nil {
		return 0, err
	}
	_ = os.Chmod(dest, 0o640)
	if gid, ok := serviceGroupID(); ok {
		_ = os.Chown(dest, 0, gid)
	}
	return files, nil
}

// backupSettings are the operator's choices for scheduled backups and for how many of each kind are kept. They live with the other
// retention settings (the file Hotel Admin writes and the sweep reads), so there is one place and one source of truth.
type backupSettings struct {
	Enabled    bool
	Hour       int
	EveryDays  int
	Keep       int
	SafetyKeep int
}

func readBackupSettings() backupSettings {
	b := backupSettings{Enabled: false, Hour: 2, EveryDays: 1, Keep: 7, SafetyKeep: 3}
	for _, r := range readRetention() {
		v := r.Value
		if v < r.Min || v > r.Max {
			v = r.Default
		}
		switch r.Key {
		case "BACKUP_ENABLED":
			b.Enabled = v == 1
		case "BACKUP_HOUR":
			b.Hour = v
		case "BACKUP_EVERY_DAYS":
			b.EveryDays = v
		case "BACKUP_KEEP":
			b.Keep = v
		case "SAFETY_BACKUP_KEEP":
			b.SafetyKeep = v
		}
	}
	return b
}

// listBackupMetas returns every backup the engine made, newest first.
func listBackupMetas() []backupMeta {
	matches, _ := filepath.Glob(filepath.Join(scdBackupDir, "db-*.meta.json"))
	var out []backupMeta
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var d backupMeta
		if json.Unmarshal(b, &d) == nil && d.Name != "" && poolOf(d.Kind) != "" && validKind(d.Kind) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TakenAt.After(out[j].TakenAt) })
	return out
}

func validKind(k string) bool {
	return k == kindScheduled || k == kindManual || k == kindPreChange || k == kindPreRestore
}

// rotateBackups trims each pool to its count. A pool's newest backup is never removed; a note whose dump is gone is removed with
// what is left of its set; nothing without a note is ever touched. Returns how many sets were removed.
func (s *server) rotateBackups() int {
	set := readBackupSettings()
	keep := map[string]int{"regular": set.Keep, "protective": set.SafetyKeep}
	count := map[string]int{}
	removed := 0
	for _, m := range listBackupMetas() { // newest first
		pool := poolOf(m.Kind)
		count[pool]++
		_, statErr := os.Stat(filepath.Join(scdBackupDir, m.Name))
		if statErr == nil && count[pool] <= max(1, keep[pool]) {
			continue
		}
		for _, f := range []string{m.Name, m.Name + ".verified", m.EtcName, filepath.Base(metaPathFor(m.Name))} {
			if f == "" {
				continue
			}
			if err := os.Remove(filepath.Join(scdBackupDir, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("backup rotation: a file could not be removed", "file", f, "err", err)
			}
		}
		removed++
	}
	return removed
}

// backupProblem is a stored backup that no longer matches its note.
type backupProblem struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// verifyStoredBackups checks every kept backup against the digests in its note: silent corruption (a failing disk) is found while
// there are other backups to restore from.
func verifyStoredBackups() []backupProblem {
	var out []backupProblem
	for _, m := range listBackupMetas() {
		check := func(file, want string, size int64) {
			full := filepath.Join(scdBackupDir, file)
			fi, err := os.Stat(full)
			if err != nil {
				out = append(out, backupProblem{file, "the file is missing"})
				return
			}
			if size > 0 && fi.Size() != size {
				out = append(out, backupProblem{file, "its size changed"})
				return
			}
			sum, err := fileDigest(full)
			if err != nil || sum != want {
				out = append(out, backupProblem{file, "its contents changed since it was verified"})
			}
		}
		check(m.Name, m.SHA256, m.SizeBytes)
		if m.EtcName != "" {
			check(m.EtcName, m.EtcSHA256, m.EtcBytes)
		}
	}
	return out
}

// ---- the schedule ----------------------------------------------------------------------------------------------------------

var backupState struct {
	mu      sync.Mutex
	lastErr string
	lastTry time.Time
	running bool
}

// backupInterval is how far apart scheduled backups are: whole days, less four hours so that "every day at 02:00" is not skipped
// by a backup that took a little longer yesterday.
func backupInterval(everyDays int) time.Duration {
	return time.Duration(max(1, everyDays))*24*time.Hour - 4*time.Hour
}

// scheduledBackupDue: the schedule is on, it is at or after the chosen hour, and the newest regular backup (scheduled or manual) is
// older than the interval.
func scheduledBackupDue(set backupSettings, now, newestRegular time.Time) bool {
	if !set.Enabled || now.Hour() < set.Hour {
		return false
	}
	return newestRegular.IsZero() || now.Sub(newestRegular) >= backupInterval(set.EveryDays)
}

func newestRegularBackup() time.Time {
	for _, m := range listBackupMetas() {
		if poolOf(m.Kind) == "regular" {
			return m.TakenAt
		}
	}
	return time.Time{}
}

// nextScheduledBackup says when the next scheduled backup will be attempted (zero when the schedule is off): the first moment that is
// at or after the chosen hour and a full interval after the newest regular backup.
func nextScheduledBackup(set backupSettings, now, newestRegular time.Time) time.Time {
	if !set.Enabled {
		return time.Time{}
	}
	t := now
	if !newestRegular.IsZero() {
		if earliest := newestRegular.Add(backupInterval(set.EveryDays)); earliest.After(t) {
			t = earliest
		}
	}
	if t.Hour() < set.Hour {
		t = time.Date(t.Year(), t.Month(), t.Day(), set.Hour, 0, 0, 0, t.Location())
	}
	return t
}

func (s *server) backupLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		s.runScheduledBackupIfDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *server) runScheduledBackupIfDue(ctx context.Context) {
	if s.db == nil {
		return
	}
	set := readBackupSettings()
	backupState.mu.Lock()
	recent := time.Since(backupState.lastTry) < 55*time.Minute && backupState.lastErr != ""
	running := backupState.running
	backupState.mu.Unlock()
	// The stored backups are checked every pass, whatever the schedule says: corruption does not wait for a schedule.
	if probs := verifyStoredBackups(); len(probs) > 0 {
		slog.Error("backups that no longer match their notes", "problems", probs)
	}
	if running || recent || !scheduledBackupDue(set, time.Now(), newestRegularBackup()) {
		return
	}
	s.runBackupTracked(ctx, backupOpts{Kind: kindScheduled, Reason: "scheduled", By: "schedule"})
}

// runBackupTracked takes a backup and records the outcome for the screen.
func (s *server) runBackupTracked(ctx context.Context, o backupOpts) (*backupMeta, error) {
	backupState.mu.Lock()
	backupState.running = true
	backupState.mu.Unlock()
	bctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	meta, err := s.takeBackup(bctx, o)
	backupState.mu.Lock()
	backupState.running = false
	backupState.lastTry = time.Now()
	if err != nil {
		backupState.lastErr = err.Error()
		slog.Error("backup failed", "kind", o.Kind, "err", err)
	} else {
		backupState.lastErr = ""
	}
	backupState.mu.Unlock()
	return meta, err
}

// ---- the screen ------------------------------------------------------------------------------------------------------------

// backupStatus serves GET /v1/backup/status: the schedule, what exists, and whether anything needs attention.
func (s *server) backupStatus(w http.ResponseWriter, r *http.Request) {
	set := readBackupSettings()
	metas := listBackupMetas()
	backupState.mu.Lock()
	lastErr, lastTry, running := backupState.lastErr, backupState.lastTry, backupState.running
	backupState.mu.Unlock()
	keyHolders := false
	if keys, err := s.backupRecipientKeys(r.Context()); err == nil && len(keys) > 0 {
		keyHolders = true
	}
	probs := verifyStoredBackups()
	newestRegular := newestRegularBackup()
	now := time.Now()

	state, msg := "ok", ""
	switch {
	case len(probs) > 0:
		state, msg = "attention", "a stored backup no longer matches its checksum"
	case !set.Enabled:
		state, msg = "off", "Scheduled backups are off. Manual backups work, and the backups a restore, a migration or a deployment take first are always taken."
	case !keyHolders:
		state, msg = "needs_key_holder", errNoBackupKeyHolder.Error()
	case newestRegular.IsZero():
		state, msg = "waiting", "The first scheduled backup has not run yet."
	case now.Sub(newestRegular) > 2*backupInterval(set.EveryDays)+4*time.Hour:
		state, msg = "attention", "The newest backup is older than the schedule allows."
	case lastErr != "":
		state, msg = "attention", "The last attempt failed: "+lastErr
	}
	next := nextScheduledBackup(set, now, newestRegular)
	var nextOut *time.Time
	if !next.IsZero() {
		nextOut = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": state, "message": msg, "key_holders": keyHolders, "running": running,
		"schedule": map[string]any{"enabled": set.Enabled, "hour": set.Hour, "every_days": set.EveryDays, "keep": set.Keep, "safety_keep": set.SafetyKeep, "next": nextOut},
		"backups":  metas, "problems": probs, "last_error": lastErr, "last_attempt": lastTry,
		"earlier": earlierBackups(),
	})
}
