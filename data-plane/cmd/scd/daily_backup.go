package main

// THE DAILY BACKUP (GDPR final mission).
//
// Once a day scd takes a backup of everything the appliance needs to be rebuilt: the database, and the configuration and keys
// under /etc/stayconnect. Both are encrypted to the hotel's backup key holders AS THEY ARE WRITTEN (the appliance holds public
// keys only, so it cannot read them back; any one holder's key can), and neither touches the disk in the clear. A backup that
// cannot be encrypted is not taken: the hotel is told, and nothing weaker is written instead.
//
// A BACKUP THAT CANNOT BE RESTORED IS NOT A BACKUP. The encrypted dump cannot be opened here, so it is verified while it is
// made: the same plain SQL that is being encrypted is also loaded into a scratch database, which must accept it without one
// error, hold the appliance's tables, and carry an intact erasure ledger. Only then is the backup kept, marked verified, and
// described in a small plain note (name, size, SHA-256 of what is stored, tables, and the ledger head it contains).
//
// The ledger head in that note is one of the witnesses a restore uses to tell whether a copy of the erasure ledger is complete.
//
// ROTATION. The newest KEEP_DB (default 7) daily backups are kept; older sets (database, configuration, note, marker) are
// removed only after a newer one has been verified. A manual backup or a safety dump is never touched here. Every day the
// stored files are checked against the digests in their notes, so silent corruption is noticed while there are still six other days.

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

// financialMarker is deliberately NOT archived: it must survive a restore and keep counting (see stayconnect-site-backup.sh).
const financialMarker = "financial-restore-generation.json"

// dailyMeta is the note kept beside each daily backup. It holds no personal data.
type dailyMeta struct {
	Kind       string    `json:"kind"`
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

var errNoBackupKeyHolder = errors.New("no backup key holder is set up: a daily backup is always encrypted, so it was not taken (Hotel Admin → Backups → Backup keys)")

var dailyMu sync.Mutex

// dailyBackup takes, verifies and records one daily backup, then rotates. It returns the note of the new backup.
func (s *server) dailyBackup(ctx context.Context) (*dailyMeta, error) {
	dailyMu.Lock()
	defer dailyMu.Unlock()
	if restoreInFlight.Load() || readMaintenance().Active {
		return nil, errors.New("a restore is running")
	}
	recipients, err := s.backupRecipients(ctx)
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
	name := "db-" + stamp + ".sql.gz" + backupcrypt.Suffix
	dest := filepath.Join(scdBackupDir, name)
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
	meta := &dailyMeta{Kind: "daily", Name: name, SizeBytes: fi.Size(), SHA256: sum, TakenAt: taken, Tables: tables, Encrypted: true,
		Restorable: true, VerifiedAt: time.Now().UTC()}
	if head.Has {
		meta.LedgerID, meta.LedgerSeq, meta.LedgerHash = head.Ledger, head.Seq, head.Hash
	}

	// The configuration and keys.
	etcName := "etc-" + stamp + ".tgz" + backupcrypt.Suffix
	if n, err := writeEtcArchive(filepath.Join(scdBackupDir, etcName), recipients); err != nil {
		slog.Error("daily backup: the configuration and keys could not be archived", "err", err)
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
	s.rotateDailyBackups(readKeepDB())
	slog.Info("daily backup complete and verified", "backup", name, "tables", tables, "ledger_entries", meta.LedgerSeq)
	return meta, nil
}

func writeMetaAtomically(m *dailyMeta) error {
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

// readKeepDB is how many daily backups to keep: the KEEP_DB retention setting (default 7).
func readKeepDB() int {
	for _, r := range readRetention() {
		if r.Key == "KEEP_DB" {
			if r.Value >= r.Min && r.Value <= r.Max {
				return r.Value
			}
			return r.Default
		}
	}
	return 7
}

func listDailyMetas() []dailyMeta {
	matches, _ := filepath.Glob(filepath.Join(scdBackupDir, "db-*.meta.json"))
	var out []dailyMeta
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var d dailyMeta
		if json.Unmarshal(b, &d) == nil && d.Kind == "daily" && d.Name != "" {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TakenAt.After(out[j].TakenAt) })
	return out
}

// rotateDailyBackups removes daily sets beyond the newest keep. Only sets that have a note saying they are daily; never the
// newest one; never a manual backup or a safety dump. Returns how many sets were removed.
func (s *server) rotateDailyBackups(keep int) int {
	if keep < 1 {
		keep = 1
	}
	metas := listDailyMetas()
	removed := 0
	for i, m := range metas {
		// A note whose dump is gone (another sweep removed it) is stale: it is removed with the rest of its set.
		if _, err := os.Stat(filepath.Join(scdBackupDir, m.Name)); i < keep && err == nil {
			continue
		}
		for _, f := range []string{m.Name, m.Name + ".verified", m.EtcName, filepath.Base(metaPathFor(m.Name))} {
			if f == "" {
				continue
			}
			if err := os.Remove(filepath.Join(scdBackupDir, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("daily backup rotation: a file could not be removed", "file", f, "err", err)
			}
		}
		removed++
	}
	return removed
}

// dailyProblem is a stored daily backup that no longer matches its note.
type dailyProblem struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// verifyStoredDaily checks every kept daily backup against the digests in its note: silent corruption (a failing disk) is found
// while there are other days to restore from.
func verifyStoredDaily() []dailyProblem {
	var out []dailyProblem
	for _, m := range listDailyMetas() {
		check := func(file, want string, size int64) {
			full := filepath.Join(scdBackupDir, file)
			fi, err := os.Stat(full)
			if err != nil {
				out = append(out, dailyProblem{file, "the file is missing"})
				return
			}
			if size > 0 && fi.Size() != size {
				out = append(out, dailyProblem{file, "its size changed"})
				return
			}
			sum, err := fileDigest(full)
			if err != nil || sum != want {
				out = append(out, dailyProblem{file, "its contents changed since it was verified"})
			}
		}
		check(m.Name, m.SHA256, m.SizeBytes)
		if m.EtcName != "" {
			check(m.EtcName, m.EtcSHA256, m.EtcBytes)
		}
	}
	return out
}

// ---- the schedule and the screen ---------------------------------------------------------------------------------------

var dailyState struct {
	mu      sync.Mutex
	lastErr string
	lastTry time.Time
}

func dailyBackupHour() int {
	for _, r := range readRetention() {
		if r.Key == "DAILY_BACKUP_HOUR" {
			if r.Value >= r.Min && r.Value <= r.Max {
				return r.Value
			}
			return r.Default
		}
	}
	return 2
}

// dailyBackupDue: at or after the configured hour, when the newest daily backup is more than 20 hours old.
func dailyBackupDue(now time.Time, newest time.Time, hour int) bool {
	if now.Hour() < hour {
		return false
	}
	return newest.IsZero() || now.Sub(newest) > 20*time.Hour
}

func (s *server) dailyBackupLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		s.runDailyBackupIfDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *server) runDailyBackupIfDue(ctx context.Context) {
	if s.db == nil {
		return
	}
	var newest time.Time
	if m := listDailyMetas(); len(m) > 0 {
		newest = m[0].TakenAt
	}
	dailyState.mu.Lock()
	recent := time.Since(dailyState.lastTry) < 55*time.Minute && dailyState.lastErr != ""
	dailyState.mu.Unlock()
	if recent || !dailyBackupDue(time.Now(), newest, dailyBackupHour()) {
		return
	}
	bctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	_, err := s.dailyBackup(bctx)
	dailyState.mu.Lock()
	dailyState.lastTry = time.Now()
	if err != nil {
		dailyState.lastErr = err.Error()
		slog.Error("daily backup failed", "err", err)
	} else {
		dailyState.lastErr = ""
	}
	dailyState.mu.Unlock()
	if probs := verifyStoredDaily(); len(probs) > 0 {
		slog.Error("daily backups that no longer match their notes", "problems", probs)
	}
}

// dailyBackupStatus serves GET /v1/backup/daily for the Backups screen.
func (s *server) dailyBackupStatus(w http.ResponseWriter, r *http.Request) {
	metas := listDailyMetas()
	dailyState.mu.Lock()
	lastErr, lastTry := dailyState.lastErr, dailyState.lastTry
	dailyState.mu.Unlock()
	keyHolders := false
	if keys, err := s.backupRecipientKeys(r.Context()); err == nil && len(keys) > 0 {
		keyHolders = true
	}
	probs := verifyStoredDaily()
	var newest *dailyMeta
	if len(metas) > 0 {
		newest = &metas[0]
	}
	state := "ok"
	msg := ""
	switch {
	case !keyHolders:
		state, msg = "needs_key_holder", errNoBackupKeyHolder.Error()
	case len(probs) > 0:
		state, msg = "attention", "a stored backup no longer matches its checksum"
	case newest == nil:
		state, msg = "waiting", "the first daily backup has not run yet"
	case time.Since(newest.TakenAt) > 36*time.Hour:
		state, msg = "attention", "the newest daily backup is more than a day and a half old"
	case lastErr != "":
		state, msg = "attention", "the last attempt failed: "+lastErr
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": state, "message": msg, "keep": readKeepDB(), "hour": dailyBackupHour(), "key_holders": keyHolders,
		"backups": metas, "problems": probs, "last_error": lastErr, "last_attempt": lastTry,
	})
}

// dailyBackupRunNow serves POST /v1/backup/daily/run: a daily-style backup on request (also what the tests use).
func (s *server) dailyBackupRunNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Hour)
	defer cancel()
	meta, err := s.dailyBackup(ctx)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errNoBackupKeyHolder) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]any{"error": "backup_failed", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

