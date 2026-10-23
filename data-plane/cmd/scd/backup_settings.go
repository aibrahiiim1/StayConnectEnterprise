package main

// Retention and schedule are settings: the sweep sources /etc/stayconnect/backup-retention.conf (KEY=value) on every run and the
// schedule is a systemd drop-in, so Hotel Admin edits those rather than reimplementing retention.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	sweepTimerUnit = "stayconnect-backup-cleanup.timer"
	timerOverride  = "/etc/systemd/system/" + sweepTimerUnit + ".d/override.conf"
)

// retentionConf is the file Hotel Admin writes the backup and retention settings to; a var so tests can use a temporary one.
var retentionConf = "/etc/stayconnect/backup-retention.conf"

type retentionSetting struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Unit     string `json:"unit"`
	Default  int    `json:"default"`
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	Value    int    `json:"value"`
	Explains string `json:"explains"`
}

// retentionSchema is the single source of truth; each lower bound is the point below which the appliance cannot recover.
var retentionSchema = []retentionSetting{
	{Key: "KEEP_BINARIES", Label: "Service binaries", Unit: "versions", Default: 5, Min: 2, Max: 30,
		Explains: "Previous builds of each service, kept so a bad deployment can be rolled back. Below two there is nothing to roll back to."},
	{Key: "KEEP_RELEASES", Label: "Hotel Admin releases", Unit: "releases", Default: 5, Min: 2, Max: 30,
		Explains: "Previous Hotel Admin bundles, on top of the current and previous ones, which are never removed."},
	// The schedule is OFF until an administrator switches it on; pre-restore, pre-migration and pre-deployment backups are taken regardless. Counts only, no age rule.
	{Key: "BACKUP_ENABLED", Label: "Scheduled backups", Unit: "0 = off, 1 = on", Default: 0, Min: 0, Max: 1,
		Explains: "Off until you switch it on. When on, the appliance takes an encrypted backup on the schedule below. Manual backups, and the backups a restore, a migration or a deployment take first, do not depend on it."},
	{Key: "BACKUP_HOUR", Label: "Scheduled backups: hour of the day", Unit: "o'clock", Default: 2, Min: 0, Max: 23,
		Explains: "The scheduled backup is taken at or after this hour (the appliance's own clock)."},
	{Key: "BACKUP_EVERY_DAYS", Label: "Scheduled backups: every", Unit: "days", Default: 1, Min: 1, Max: 7,
		Explains: "How many days between scheduled backups. A backup taken by hand counts: the next scheduled one comes this many days after the newest."},
	{Key: "BACKUP_KEEP", Label: "Backups kept", Unit: "backups", Default: 7, Min: 1, Max: 30,
		Explains: "How many scheduled and manual backups to keep. The newest is always kept; older ones go only after a newer one has been verified. With a schedule of N days, the oldest backup is about (this number x N) days old, and an erased guest can be in it for that long."},
	{Key: "SAFETY_BACKUP_KEEP", Label: "Safety backups kept", Unit: "backups", Default: 3, Min: 2, Max: 20,
		Explains: "The backups taken automatically before a restore, a migration or a deployment: how many of the newest to keep."},
	// A restore keeps the database it replaced so it can be undone; that copy still holds guests erased since, so it is removed after this many days.
	{Key: "SET_ASIDE_DAYS", Label: "Database kept aside by a restore: keep for", Unit: "days", Default: 14, Min: 1, Max: 90,
		Explains: "A restore keeps the database it replaced so it can be undone. That copy still holds data erased or anonymised since, so it is removed after this many days."},
	{Key: "KEEP_CONFIG", Label: "Configuration backups", Unit: "copies", Default: 3, Min: 1, Max: 30,
		Explains: "Saved copies of configuration files taken before a change."},
	// Backups are encrypted to the hotel's keys and the appliance cannot tell whether they still exist, so a key holder must periodically prove it by opening the newest backup.
	{Key: "RESTORE_TEST_MONTHS", Label: "Backup key restore test every", Unit: "months", Default: 6, Min: 1, Max: 24,
		Explains: "How often a backup key holder must prove their key still opens the backups. Overdue tests show a warning."},
	{Key: "DISK_WARN", Label: "Disk warning threshold", Unit: "% used", Default: 80, Min: 50, Max: 95,
		Explains: "Disk usage at which the nightly sweep reports a warning."},
	{Key: "DISK_CRIT", Label: "Disk critical threshold", Unit: "% used", Default: 90, Min: 60, Max: 99,
		Explains: "Disk usage at which the sweep reports a critical alert. Must be above the warning threshold."},
}

var (
	reConfLine  = regexp.MustCompile(`^\s*([A-Z_]+)\s*=\s*"?([0-9]+)"?\s*(?:#.*)?$`)
	reCalendar  = regexp.MustCompile(`OnCalendar=\*-\*-\* (\d{2}:\d{2}):\d{2}`)
	reTimeOfDay = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
)

// readRetention resolves each value from the file or its default, exactly as the sweep does, so the screen shows what is in force.
func readRetention() []retentionSetting {
	out := make([]retentionSetting, len(retentionSchema))
	copy(out, retentionSchema)
	for i := range out {
		out[i].Value = out[i].Default
	}
	b, err := os.ReadFile(retentionConf)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		m := reConfLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(m[2], "%d", &v); err != nil {
			continue
		}
		for i := range out {
			if out[i].Key == m[1] {
				out[i].Value = v
			}
		}
	}
	return out
}

// readSchedule asks systemd when the sweep next runs: a written file and a reloaded timer are different facts.
func readSchedule(ctx context.Context) (string, bool) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", sweepTimerUnit, "--property=TimersCalendar").Output()
	if err != nil {
		return "", false
	}
	m := reCalendar.FindStringSubmatch(string(out))
	if m == nil {
		return "", false
	}
	return m[1], true
}

func (s *server) backupSettingsGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	sched, schedOK := readSchedule(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"retention":      readRetention(),
		"schedule":       sched,
		"schedule_known": schedOK,
		"config_present": fileExists(retentionConf),
	})
}

type backupSettingsReq struct {
	Retention map[string]int `json:"retention"`
	Schedule  string         `json:"schedule"` // HH:MM, appliance local time
}

// backupSettingsSet validates everything before writing and refuses the whole request: a partly applied policy is worse than a rejected one.
func (s *server) backupSettingsSet(w http.ResponseWriter, r *http.Request) {
	var in backupSettingsReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, "malformed request body")
		return
	}
	current := readRetention()
	byKey := map[string]*retentionSetting{}
	for i := range current {
		byKey[current[i].Key] = &current[i]
	}
	for k, v := range in.Retention {
		set, ok := byKey[k]
		if !ok {
			httpErr(w, http.StatusBadRequest, "unknown retention setting: "+k)
			return
		}
		if v < set.Min || v > set.Max {
			httpErr(w, http.StatusBadRequest, fmt.Sprintf(
				"%s must be between %d and %d %s", set.Label, set.Min, set.Max, set.Unit))
			return
		}
		set.Value = v
	}
	// A critical threshold at or below the warning threshold is a broken policy.
	if byKey["DISK_CRIT"].Value <= byKey["DISK_WARN"].Value {
		httpErr(w, http.StatusBadRequest, "the critical disk threshold must be above the warning threshold")
		return
	}
	if in.Schedule != "" && !reTimeOfDay.MatchString(in.Schedule) {
		httpErr(w, http.StatusBadRequest, "the schedule must be a time of day as HH:MM")
		return
	}

	var b strings.Builder
	b.WriteString("# StayConnect backup retention policy.\n")
	b.WriteString("# Written by Hotel Admin. Sourced by stayconnect-backup-cleanup on every run.\n")
	for _, set := range current {
		fmt.Fprintf(&b, "%s=%d\n", set.Key, set.Value)
	}
	// Written in place, not temp-and-rename: ProtectSystem=full with a one-file ReadWritePaths allowlist leaves the directory
	// unwritable, and widening it would expose identity, licence and certificate material. The payload is a few hundred bytes read once by the sweep.
	if err := os.WriteFile(retentionConf, []byte(b.String()), 0o644); err != nil {
		slog.Error("retention policy not written", "path", retentionConf, "err", err)
		httpErr(w, http.StatusInternalServerError,
			"the retention policy could not be written to "+retentionConf+": "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	if in.Schedule != "" {
		if err := os.MkdirAll(filepath.Dir(timerOverride), 0o755); err != nil {
			httpErr(w, http.StatusInternalServerError, "the schedule directory could not be created")
			return
		}
		// A drop-in, not an edit of the shipped unit: a package update would replace the unit and silently revert the schedule.
		ov := "[Timer]\n# Set from Hotel Admin.\nOnCalendar=\nOnCalendar=*-*-* " + in.Schedule + ":00\n"
		if err := os.WriteFile(timerOverride, []byte(ov), 0o644); err != nil {
			httpErr(w, http.StatusInternalServerError, "the schedule could not be written")
			return
		}
		if out, err := exec.CommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput(); err != nil {
			slog.Error("daemon-reload after schedule change", "detail", tailLine(string(out)))
		}
		if out, err := exec.CommandContext(ctx, "systemctl", "restart", sweepTimerUnit).CombinedOutput(); err != nil {
			httpErr(w, http.StatusInternalServerError,
				"the schedule was written but the timer did not restart: "+tailLine(string(out)))
			return
		}
	}
	// Read back from systemd rather than echoing the request.
	sched, schedOK := readSchedule(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"retention": current, "schedule": sched, "schedule_known": schedOK, "config_present": true,
	})
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
