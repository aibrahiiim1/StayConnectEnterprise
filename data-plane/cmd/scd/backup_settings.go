package main

// RETENTION AND SCHEDULE ARE SETTINGS, NOT CONSTANTS (§0C).
//
// How many builds to keep, how many database backups to keep, when the nightly sweep runs and at what disk
// pressure it warns are all operational values a hotel administrator may reasonably need to change. They
// lived in two places an operator cannot reach: defaults inside stayconnect-backup-cleanup, and an optional
// /etc/stayconnect/backup-retention.conf which did not exist on this appliance at all — so the Backups screen
// could show the policy in force but nobody could alter it without a shell.
//
// The file is the sweep's own contract — a shell-sourced KEY=value file it reads on every run — so writing it
// is the honest way to change behaviour. Nothing here re-implements retention; it configures the thing that
// already performs it. The schedule is a systemd drop-in for the same reason: systemd owns when the sweep
// runs, so that is what gets edited.

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
	retentionConf  = "/etc/stayconnect/backup-retention.conf"
	sweepTimerUnit = "stayconnect-backup-cleanup.timer"
	timerOverride  = "/etc/systemd/system/" + sweepTimerUnit + ".d/override.conf"
)

// retentionSetting is one knob with everything an operator needs to set it responsibly: what it means, its
// unit, its default, and the bounds it is held to.
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

// retentionSchema is the single source of truth for what may be set and to what. The lower bounds are not
// decoration: each is the point below which the appliance loses its own ability to recover.
var retentionSchema = []retentionSetting{
	{Key: "KEEP_BINARIES", Label: "Service binaries", Unit: "versions", Default: 5, Min: 2, Max: 30,
		Explains: "Previous builds of each service, kept so a bad deployment can be rolled back. Below two there is nothing to roll back to."},
	{Key: "KEEP_RELEASES", Label: "Hotel Admin releases", Unit: "releases", Default: 5, Min: 2, Max: 30,
		Explains: "Previous Hotel Admin bundles, on top of the current and previous ones, which are never removed."},
	{Key: "KEEP_DB", Label: "Database backups", Unit: "backups", Default: 7, Min: 1, Max: 365,
		Explains: "How many database backups to retain. The newest is always kept regardless of this number."},
	{Key: "KEEP_CONFIG", Label: "Configuration backups", Unit: "copies", Default: 3, Min: 1, Max: 30,
		Explains: "Saved copies of configuration files taken before a change."},
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

// readRetention returns the schema with each value resolved from the file, or its default when absent —
// which is exactly what the sweep does, so the screen shows what is genuinely in force rather than what
// happens to have been written down.
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

// readSchedule asks systemd when the sweep will next run, rather than reporting what we last wrote. A file we
// wrote and a timer that reloaded are two different facts, and only the second one happens.
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

// backupSettingsSet validates everything before writing anything.
//
// IT REFUSES THE WHOLE REQUEST rather than the offending field. A partially-applied retention policy is worse
// than a rejected one: the operator believes they set six values and the sweep acts on four.
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
	// A critical threshold at or below the warning threshold is not a stricter policy, it is a broken one:
	// the appliance could never warn before it alarmed.
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
	// WRITTEN IN PLACE, and that is a deliberate trade against a narrower sandbox.
	//
	// Write-to-temp-and-rename is the usual way to avoid a torn read, but it needs the DIRECTORY writable.
	// This service runs under ProtectSystem=full with an explicit ReadWritePaths allowlist, and widening that
	// to all of /etc/stayconnect to gain atomicity on a 300-byte file would hand it write access to the
	// identity, licence and certificate material sitting beside it. The allowlist names this one file.
	//
	// What is given up is small and bounded: the payload is a few hundred bytes written by a single
	// os.WriteFile, and the only reader is a nightly sweep that sources it once at start.
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
		// A drop-in override rather than an edit of the shipped unit: a package update replaces the unit file
		// and would silently discard an in-place change, leaving the operator's schedule quietly reverted.
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
	// Read back from systemd rather than echoing the request: what was asked for and what is scheduled are
	// different facts.
	sched, schedOK := readSchedule(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"retention": current, "schedule": sched, "schedule_known": schedOK, "config_present": true,
	})
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
