package main

// THE EARLIER BACKUPS: WHAT THE RETIRED MECHANISMS LEFT, PRESERVED UNTIL AN ADMINISTRATOR SAYS OTHERWISE.
//
// Before there was one backup system there were two:
//
//   - the nightly site backup (a pg_dump -Fc of the database and a tar of /etc/stayconnect, written to /var/backups/stayconnect by a
//     systemd timer), and
//   - on-demand dumps on the Backups screen, made before backups had notes (and the restore safety dumps), in the same folder as
//     today's backups.
//
// The first is retired. Both are recovery material the hotel may still need, so nothing here deletes them on its own: rotation and
// the retention sweep only ever touch backups that carry a note. They are listed on the Backups screen as earlier backups, with
// whether this appliance can restore them, and they go only when an administrator removes them -- and only once a verified backup
// of the new kind exists, so removing the old never leaves the hotel with less than it had.
//
// A nightly set is a pg_dump custom-format archive, which the restore on the Backups screen cannot load; it is kept for the
// documented manual restore (docs/BACKUP_AND_RESTORE.md, "Earlier backups") until it is removed.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type earlierSet struct {
	Name         string    `json:"name"`
	Kind         string    `json:"kind"` // "nightly set" | "earlier dump" | "safety dump"
	Files        []string  `json:"files"`
	SizeBytes    int64     `json:"size_bytes"`
	Taken        time.Time `json:"taken"`
	Encrypted    bool      `json:"encrypted"`
	RestorableOn bool      `json:"restorable_here"`
}

var reNightly = regexp.MustCompile(`^site-([0-9]{8}-[0-9]{6})(-etc\.tgz|\.dump|\.dump\.meta\.json)(\.age)?$`)

// earlierSets finds every earlier backup, newest first. Nothing is read from inside a file.
func earlierSets() []earlierSet {
	var out []earlierSet
	// nightly sets: group the dump, its configuration archive and its note by stamp
	groups := map[string]*earlierSet{}
	if entries, err := os.ReadDir(nightlyBackupDir); err == nil {
		for _, e := range entries {
			m := reNightly.FindStringSubmatch(e.Name())
			if m == nil || e.IsDir() {
				continue
			}
			g := groups[m[1]]
			if g == nil {
				t, _ := time.ParseInLocation("20060102-150405", m[1], time.UTC)
				g = &earlierSet{Name: "site-" + m[1], Kind: "nightly set", Taken: t}
				groups[m[1]] = g
			}
			if fi, err := e.Info(); err == nil {
				g.SizeBytes += fi.Size()
			}
			g.Files = append(g.Files, e.Name())
			if m[3] == ".age" {
				g.Encrypted = true
			}
		}
	}
	for _, g := range groups {
		sort.Strings(g.Files)
		out = append(out, *g)
	}
	// earlier dumps in the backup folder: a dump that has no note was not made by the engine
	noted := map[string]bool{}
	for _, m := range listBackupMetas() {
		noted[m.Name] = true
	}
	if entries, err := os.ReadDir(scdBackupDir); err == nil {
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !safeBackupName.MatchString(n) || noted[n] {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			kind := "earlier dump"
			if strings.HasPrefix(n, "db-safety-") {
				kind = "safety dump"
			}
			files := []string{n}
			size := fi.Size()
			if mfi, err := os.Stat(filepath.Join(scdBackupDir, n+".verified")); err == nil {
				files = append(files, n+".verified")
				size += mfi.Size()
			}
			out = append(out, earlierSet{Name: n, Kind: kind, Files: files, SizeBytes: size, Taken: fi.ModTime().UTC(),
				Encrypted: strings.HasSuffix(n, ".age"), RestorableOn: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Taken.After(out[j].Taken) })
	return out
}

// earlierBackups summarises them for the screen.
func earlierBackups() map[string]any {
	sets := earlierSets()
	var bytes int64
	plain := 0
	var oldest time.Time
	for _, s := range sets {
		bytes += s.SizeBytes
		if !s.Encrypted {
			plain++
		}
		if oldest.IsZero() || s.Taken.Before(oldest) {
			oldest = s.Taken
		}
	}
	var oldestOut *time.Time
	if !oldest.IsZero() {
		oldestOut = &oldest
	}
	return map[string]any{"sets": sets, "count": len(sets), "size_bytes": bytes, "unencrypted": plain, "oldest": oldestOut}
}

var errNoVerifiedBackup = errors.New("no verified backup of the current kind exists yet: take one first, so removing the earlier ones never leaves less than there was")

// removeEarlierBackups deletes the earlier backups. It refuses unless a backup the engine made (restorable, and every stored backup
// still matches its note) exists, and it removes only files it listed itself, by exact name, inside the two backup folders.
func removeEarlierBackups() (int, int64, error) {
	var have bool
	for _, m := range listBackupMetas() {
		if m.Restorable && poolOf(m.Kind) == "regular" {
			have = true
			break
		}
	}
	if !have {
		return 0, 0, errNoVerifiedBackup
	}
	if probs := verifyStoredBackups(); len(probs) > 0 {
		return 0, 0, errors.New("a stored backup no longer matches its checksum: resolve that before removing the earlier backups")
	}
	removedSets := 0
	var removedBytes int64
	for _, set := range earlierSets() {
		dir := scdBackupDir
		if set.Kind == "nightly set" {
			dir = nightlyBackupDir
		}
		for _, f := range set.Files {
			full := filepath.Join(dir, f)
			if filepath.Dir(full) != filepath.Clean(dir) {
				continue
			}
			if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("an earlier backup could not be removed", "file", full, "err", err)
				continue
			}
		}
		removedSets++
		removedBytes += set.SizeBytes
	}
	return removedSets, removedBytes, nil
}

// earlierBackupsRemove serves POST /v1/backup/earlier/remove. edged has already asked for the password, the typed confirmation and
// written the audit entry; this is the filesystem half.
func (s *server) earlierBackupsRemove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		By string `json:"by"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
	if readMaintenance().Active || restoreInFlight.Load() {
		httpErr(w, http.StatusConflict, "a restore is running")
		return
	}
	n, b, err := removeEarlierBackups()
	if err != nil {
		code := http.StatusConflict
		writeJSON(w, code, map[string]any{"error": "refused", "message": err.Error()})
		return
	}
	slog.Warn("earlier backups removed by an administrator", "by", in.By, "sets", n, "bytes", b)
	writeJSON(w, http.StatusOK, map[string]any{"removed_sets": n, "removed_bytes": b})
}
