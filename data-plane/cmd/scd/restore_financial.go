package main

// Financial-recovery guarantees of a restore: proven quiesce, marker advanced before the swap, hypertables registered after it,
// and the restore recorded in the database (FINANCIAL_RECOVERY_MODE).

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

func markerPath() string { return filepath.Join(etcStayconnectDir, financialMarker) }

// A missing marker is generation 0; an unreadable one is an error so the restore refuses rather than guess.
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

// The marker is world-readable (0644): edged reads it to detect a restore and is not root; it holds nothing secret.
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

// Runs as a transient systemd unit because scd's own unit cannot write /etc/stayconnect.
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

var stopService = func(ctx context.Context, svc string) error {
	return exec.CommandContext(ctx, "systemctl", "stop", svc).Run()
}

var serviceState = func(ctx context.Context, svc string) string {
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", svc).Output()
	return strings.TrimSpace(string(out))
}

// Stopping is not the same as being stopped: a service still active, or whose state cannot be read, aborts the restore.
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

// With TimescaleDB installed, audit_log and accounting_records must be hypertables or writes to them are refused.
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

// Puts the site into FINANCIAL_RECOVERY_MODE, which holds money movement until reconciled.
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
