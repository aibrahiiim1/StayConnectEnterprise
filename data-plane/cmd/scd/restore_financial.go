package main

// THE FINANCIAL-RECOVERY GUARANTEES OF A RESTORE.
//
// There used to be two ways to put a database back: the Backups screen (this program) and stayconnect-financial-restore.sh, a
// command-line tool that was never wired in. The script carried the guarantees that make a restore safe for money; the screen did
// not. With one backup system, the restore carries all of them:
//
//   - a PROVEN quiesce: every service that can write financial state is stopped and then checked, and a service that is still
//     running (or whose state cannot be read) aborts the restore before anything is replaced;
//   - the management marker (/etc/stayconnect/financial-restore-generation.json) is advanced BEFORE the swap, so neither a database
//     restore nor an /etc restore can roll it back, and a restore that fails half way still leaves money movement held;
//   - TimescaleDB's hypertables must be registered after the swap (a database can restore "successfully" and refuse every write to
//     audit_log and accounting_records);
//   - the restore is recorded in the database (iam_v2.p4_record_supported_restore), which puts the site into FINANCIAL_RECOVERY_MODE:
//     nothing is transmitted or paid until an operator has reconciled what was in flight when the backup was taken.
//
// The database-level GRANT CREATE ON DATABASE that Gate-P gives iam_v2_owner is carried across the swap by the privilege capture
// (privileges.go), not here.
//
// Whether a scheduled backup is switched on has no bearing on any of this: it is part of restoring, not of the schedule.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// markerPath is where the marker lives: outside the database and outside the /etc archive.
func markerPath() string { return filepath.Join(etcStayconnectDir, financialMarker) }

// readMarkerGeneration is the generation the marker records; a missing marker is generation 0 (never restored). A marker that is
// there but cannot be read is an error: the restore refuses rather than guess.
func readMarkerGeneration() (int64, error) {
	b, err := os.ReadFile(markerPath())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("the restore marker %s could not be read: %w", markerPath(), err)
	}
	var m struct {
		Generation int64 `json:"restore_generation"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, fmt.Errorf("the restore marker %s is not readable: %w", markerPath(), err)
	}
	if m.Generation < 0 {
		return 0, fmt.Errorf("the restore marker %s records a negative generation", markerPath())
	}
	return m.Generation, nil
}

var reHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// writeMarker advances the marker to gen. It never moves it backwards or sideways, and it writes world-readable (0644, root-owned):
// edged, which reads it to detect a restore, is not root, and the marker holds a count, a time and a digest, nothing secret. Its
// protection is that only root can write it and that it lives outside everything a restore replaces.
func writeMarker(gen int64, manifestSHA string) error {
	if !reHex64.MatchString(manifestSHA) {
		return errors.New("the marker needs the SHA-256 of the backup being restored")
	}
	cur, err := readMarkerGeneration()
	if err != nil {
		return err
	}
	if gen <= cur {
		return fmt.Errorf("the restore marker already records generation %d; it can only advance", cur)
	}
	if err := os.MkdirAll(etcStayconnectDir, 0o755); err != nil {
		return err
	}
	body, _ := json.MarshalIndent(map[string]any{
		"restore_generation": gen,
		"advanced_at":        time.Now().UTC().Format(time.RFC3339),
		"manifest_sha256":    manifestSHA,
		"note": "Written by scd before a database restore. It lives on the management partition and is EXCLUDED from the configuration " +
			"backup, so neither a database restore nor a configuration restore can roll it back. Protected by file permissions only.",
	}, "", "  ")
	tmp := markerPath() + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, markerPath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// markerAdvanceMain is `scd marker-advance --generation N --manifest-sha HEX`. scd's own unit cannot write /etc/stayconnect, so the
// restore runs this as a transient systemd unit, outside scd's sandbox.
func markerAdvanceMain(args []string) int {
	fs := flag.NewFlagSet("marker-advance", flag.ContinueOnError)
	gen := fs.Int64("generation", 0, "the generation to advance the marker to")
	sha := fs.String("manifest-sha", "", "SHA-256 of the backup being restored")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := writeMarker(*gen, *sha); err != nil {
		fmt.Fprintln(os.Stderr, "marker-advance:", err)
		return 1
	}
	fmt.Printf("marker-advance: restore generation advanced to %d\n", *gen)
	return 0
}

// advanceMarker is a var so tests (and the restore drill, which has no systemd) can supply their own.
var advanceMarker = func(ctx context.Context, gen int64, manifestSHA string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "systemd-run", "--quiet", "--wait", "--collect", "--pipe", "-p", "Type=oneshot",
		exe, "marker-advance", "--generation", strconv.FormatInt(gen, 10), "--manifest-sha", manifestSHA).CombinedOutput()
	if err != nil {
		return fmt.Errorf("the restore marker could not be advanced: %s", tailLine(string(out)+" "+err.Error()))
	}
	return nil
}

// stopService and serviceState are vars so tests can stand in for systemd.
var stopService = func(ctx context.Context, svc string) error {
	return exec.CommandContext(ctx, "systemctl", "stop", svc).Run()
}

var serviceState = func(ctx context.Context, svc string) string {
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", svc).Output()
	return strings.TrimSpace(string(out))
}

// proveQuiesced stops every dependent service and then checks that each is no longer running. Stopping is not the same as being
// stopped: a service still active, or in a state that cannot be read, aborts the restore.
func proveQuiesced(ctx context.Context) error {
	var bad []string
	for _, svc := range dependentServices {
		if err := stopService(ctx, svc); err != nil {
			bad = append(bad, svc+" (stop failed)")
			continue
		}
		switch st := serviceState(ctx, svc); st {
		case "inactive", "failed", "":
		default:
			bad = append(bad, svc+" ("+st+")")
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("these services could not be proven stopped: %s", strings.Join(bad, ", "))
	}
	return nil
}

// assertHypertables: when TimescaleDB is installed, at least two hypertables (audit_log, accounting_records) must be registered.
func assertHypertables(ctx context.Context, db string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", db, "-tA", "-v", "ON_ERROR_STOP=1", "-c",
		"SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_extension WHERE extname='timescaledb') "+
			"THEN (SELECT count(*) FROM timescaledb_information.hypertables)::text ELSE 'none' END").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("the hypertables could not be counted: %s", tailLine(string(out)))
	}
	got := strings.TrimSpace(string(out))
	if got == "none" {
		return "TimescaleDB is not installed in this database", nil
	}
	n, _ := strconv.Atoi(got)
	if n < 2 {
		return "", fmt.Errorf("only %d hypertable(s) are registered after the restore; audit_log and accounting_records would refuse every write", n)
	}
	return fmt.Sprintf("%d hypertables registered", n), nil
}

var reUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// recordSupportedRestore tells the database a supported restore happened, which holds money movement (FINANCIAL_RECOVERY_MODE).
func recordSupportedRestore(ctx context.Context, db, tenant, site string, gen int64, manifestSHA string, takenAt time.Time, by string) error {
	if !reUUID.MatchString(tenant) || !reUUID.MatchString(site) || !reHex64.MatchString(manifestSHA) {
		return errors.New("the restore could not be recorded: the site identity or the backup digest is not valid")
	}
	taken := "NULL"
	if !takenAt.IsZero() {
		taken = "'" + takenAt.UTC().Format(time.RFC3339) + "'::timestamptz"
	}
	if strings.TrimSpace(by) == "" {
		by = "hotel-admin"
	}
	by = strings.ReplaceAll(by, "'", "''")
	sql := fmt.Sprintf("SELECT iam_v2.p4_record_supported_restore('%s'::uuid,'%s'::uuid,%d,'%s',%s,'%s')", tenant, site, gen, manifestSHA, taken, by)
	if out, err := exec.CommandContext(ctx, "docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", db, "-tA", "-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput(); err != nil {
		return fmt.Errorf("the restore could not be recorded in the database: %s", firstProblem(string(out)))
	}
	return nil
}
