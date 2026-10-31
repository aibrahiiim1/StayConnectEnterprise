package main

// Optional copy of the retained backup sets to the secondary storage (BACKUP_REPLICATE=1). A backup never depends on it: it is taken,
// verified and kept on the appliance whether or not a copy succeeds, and a failed copy changes no local file. Each file of a set
// (database dump, configuration archive, verified marker, note) is written, then read back IN FULL and its SHA-256 compared with
// the local file; the note goes last, so a set at the other end without its note is an unfinished copy. A set counts as copied
// only when every file read back right. A copy kept there is removed only when it is one this appliance copied (it is in the state
// file) and its set has left the appliance by rotation; nothing else at the storage is ever touched. Removing or replacing the
// storage setting deletes nothing there.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// mirrorStatePath records what was copied where; kept beside the backups so a restore of /etc does not rewrite it.
func mirrorStatePath() string { return filepath.Join(scdBackupDir, "mirror-state.json") }

type backupCopyFile struct {
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	VerifiedAt time.Time `json:"verified_at"`
}

type backupCopySet struct {
	Files      map[string]backupCopyFile `json:"files"`
	Complete   bool                  `json:"complete"`
	VerifiedAt *time.Time            `json:"verified_at,omitempty"`
	Error      string                `json:"error,omitempty"`
}

type backupCopyState struct {
	Destination string               `json:"destination"`
	Sets        map[string]backupCopySet `json:"sets"`
	LastOK      *time.Time           `json:"last_ok,omitempty"`
	LastError   string               `json:"last_error,omitempty"`
	LastTry     *time.Time           `json:"last_try,omitempty"`
}

var mirrorMu sync.Mutex

func readBackupCopyState() backupCopyState {
	st := backupCopyState{Sets: map[string]backupCopySet{}}
	if b, err := os.ReadFile(mirrorStatePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Sets == nil {
		st.Sets = map[string]backupCopySet{}
	}
	return st
}

func writeBackupCopyState(st backupCopyState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := mirrorStatePath() + ".partial"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, mirrorStatePath())
}

// setFiles are the files of one set, the note last.
func setFiles(m backupMeta) []string {
	out := []string{m.Name, m.Name + ".verified"}
	if m.EtcName != "" {
		out = append(out, m.EtcName)
	}
	return append(out, filepath.Base(metaPathFor(m.Name)))
}

// mirrorBackups brings the copy at the secondary storage in step with the retained sets. It never blocks a backup: a run in
// progress makes a second one return at once.
func (s *server) mirrorBackups(ctx context.Context) {
	if !readBackupSettings().Replicate || s.ledgerRT.store == nil {
		return
	}
	if !mirrorMu.TryLock() {
		return
	}
	defer mirrorMu.Unlock()
	st := readBackupCopyState()
	now := time.Now().UTC()
	st.LastTry = &now
	defer func() {
		if err := writeBackupCopyState(st); err != nil {
			slog.Error("backup copy: its state could not be saved", "err", err)
		}
	}()
	dest := s.ledgerRT.store.Destination()
	if dest == nil {
		st.LastError = "no secondary storage is set up (Admin Console → Backups → Erasure ledger)"
		return
	}
	fd, ok := dest.(ledger.FileDest)
	if !ok {
		st.LastError = "this kind of secondary storage cannot hold backups"
		return
	}
	if st.Destination != dest.Describe() {
		// A new or replaced storage: every retained set is copied to it from the start. What the previous one holds stays there.
		st.Destination, st.Sets = dest.Describe(), map[string]backupCopySet{}
	}
	metas := listBackupMetas()
	retained := map[string]bool{}
	failures := 0
	for _, m := range metas {
		retained[m.Name] = true
		ms := st.Sets[m.Name]
		if ms.Complete {
			continue
		}
		if ms.Files == nil {
			ms.Files = map[string]backupCopyFile{}
		}
		ms.Error = ""
		for _, f := range setFiles(m) {
			if _, done := ms.Files[f]; done {
				continue
			}
			if err := ctx.Err(); err != nil {
				ms.Error = "stopped before it finished"
				break
			}
			local := filepath.Join(scdBackupDir, f)
			sum, size, err := ledger.FileDigest(local)
			if err != nil {
				ms.Error = f + ": the local file could not be read: " + err.Error()
				break
			}
			if want := expectedDigest(m, f); want != "" && want != sum {
				ms.Error = f + ": the local file no longer matches its note, so it was not copied"
				break
			}
			if err := fd.PutFile(ctx, f, local); err != nil {
				ms.Error = f + ": " + err.Error()
				break
			}
			rsum, rsize, err := fd.Digest(ctx, f)
			if err != nil {
				ms.Error = f + ": it was written but could not be read back: " + err.Error()
				break
			}
			if rsum != sum || rsize != size {
				ms.Error = f + ": what was read back is not what was written"
				break
			}
			ms.Files[f] = backupCopyFile{SHA256: sum, Size: size, VerifiedAt: time.Now().UTC()}
		}
		if ms.Error == "" && len(ms.Files) == len(setFiles(m)) {
			t := time.Now().UTC()
			ms.Complete, ms.VerifiedAt = true, &t
		} else {
			failures++
		}
		st.Sets[m.Name] = ms
	}
	// Copies of sets that have left the appliance by rotation: removed only when every retained set is safely copied, and only
	// the files this appliance wrote.
	if failures == 0 {
		for name, ms := range st.Sets {
			if retained[name] {
				continue
			}
			left := false
			for f := range ms.Files {
				if err := dest.Remove(ctx, f); err != nil {
					left = true
					slog.Warn("backup copy: a rotated copy could not be removed from the secondary storage", "file", f, "err", err)
				}
			}
			if !left {
				delete(st.Sets, name)
			}
		}
	}
	if failures == 0 {
		t := time.Now().UTC()
		st.LastOK, st.LastError = &t, ""
	} else {
		st.LastError = "some backups could not be copied; they are retried on the next pass"
	}
}

// expectedDigest is the checksum the note recorded for a file of the set ("" for the marker and the note, which it does not cover).
func expectedDigest(m backupMeta, f string) string {
	switch f {
	case m.Name:
		return m.SHA256
	case m.EtcName:
		return m.EtcSHA256
	}
	return ""
}

// mirrorStatus is the copy as the Admin Console shows it: never "copied" without a read-back.
func (s *server) mirrorStatus(set backupSettings, metas []backupMeta) map[string]any {
	st := readBackupCopyState()
	mode := "local_only"
	if set.Replicate {
		mode = "local_and_secondary"
	}
	out := map[string]any{"mode": mode, "last_ok": st.LastOK, "last_error": nilIfEmpty(st.LastError)}
	var dest ledger.Destination
	if s.ledgerRT.store != nil {
		dest = s.ledgerRT.store.Destination()
	}
	sets := []map[string]any{}
	pending := 0
	for _, m := range metas {
		ms, ok := st.Sets[m.Name]
		state := "pending"
		switch {
		case ok && ms.Complete && dest != nil && st.Destination == dest.Describe():
			state = "copied"
		case ok && ms.Error != "":
			state = "failed"
		}
		if state != "copied" {
			pending++
		}
		row := map[string]any{"name": m.Name, "state": state}
		if ms.VerifiedAt != nil {
			row["verified_at"] = ms.VerifiedAt
		}
		if ms.Error != "" {
			row["error"] = ms.Error
		}
		sets = append(sets, row)
	}
	out["sets"] = sets
	switch {
	case !set.Replicate:
		out["state"], out["message"] = "off", "Backups are kept on the appliance only."
	case dest == nil:
		out["state"], out["message"] = "no_destination", "Copying backups is on, but no secondary storage is set up. Local backups are not affected."
	case st.LastError != "" && pending > 0:
		out["state"], out["message"] = "attention", st.LastError
	case pending > 0:
		out["state"], out["message"] = "pending", "Some backups have not been copied and read back yet."
	default:
		out["state"], out["message"] = "ok", "Every retained backup is copied to the secondary storage and was read back."
	}
	return out
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

var errMirrorOff = errors.New("copying backups to the secondary storage is off")

// mirrorNow runs one pass for an operator's "Copy now" and reports whether it ran.
func (s *server) mirrorNow(ctx context.Context) error {
	if !readBackupSettings().Replicate {
		return errMirrorOff
	}
	s.mirrorBackups(ctx)
	return nil
}
