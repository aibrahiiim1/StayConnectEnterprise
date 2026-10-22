//go:build integration

package main

// RESTORE AND RETENTION (GDPR): a restored backup must never serve personal data whose retention period has ended, whatever happens to the
// retention run -- it fails, it times out, it is interrupted -- and whichever period was in force when the backup was taken.
// Runs inside the restore drill (scripts/ledger-restore-drill.sh): the real restore against a real PostgreSQL.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// seedAgedAccount is a guest account whose last sign of life is days old.
func (d *drill) seedAgedAccount(label string, days int) {
	d.t.Helper()
	d.stamp++
	email := fmt.Sprintf("aged-%s-%d-%d@example.com", label, time.Now().UnixNano(), d.stamp)
	var id string
	if err := d.live().QueryRow(context.Background(), `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, display_name, notes, created_at, self_registered)
		VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash','Drill Guest','room 101 guest', now() - make_interval(days => $4), true) RETURNING id::text`,
		d.tenant, d.site, email, days).Scan(&id); err != nil {
		d.t.Fatalf("seed aged account: %v", err)
	}
	d.accts[label], d.emails[label] = id, email
}

// shortenInactive sets the live inactive-identity period (the hotel changing its policy after the backup was taken).
func (d *drill) shortenInactive(days int) {
	d.t.Helper()
	if _, err := d.pool.Exec(context.Background(), `SELECT iam_v2.data_retention_set($1::uuid,$2::uuid, r.pms_stay_days, r.client_activity_days, $3, r.expired_record_days,
		r.audit_log_days, r.sign_in_attempt_days, r.log_days, r.staff_note_days, 'drill', 'the hotel shortened a period after the backup')
		FROM iam_v2.data_retention_get($1::uuid,$2::uuid) r`, d.tenant, d.site, days); err != nil {
		d.t.Fatalf("data_retention_set: %v", err)
	}
}

func (d *drill) stagingDatabases() string {
	out, err := execPsql(context.Background(), "postgres", `SELECT coalesce(string_agg(datname, ','), '') FROM pg_database WHERE datname LIKE 'restore\_%'`)
	if err != nil {
		d.t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// THE PERIOD SHORTENED AFTER THE BACKUP. The backup carries the old, longer period. The restored data is judged by the period in force NOW,
// so an account the hotel's current policy says is expired is anonymised before the data is live -- and the hotel's current period survives.
func TestDrill_RetentionPeriodShortenedAfterTheBackupAppliesToTheRestoredData(t *testing.T) {
	d := newDrill(t)
	d.seedAgedAccount("old", 100) // older than the 30 days the hotel will choose, younger than the 365 default
	d.seedAgedAccount("young", 1)
	backup := d.takeBackup() // the backup holds the default (365) period
	d.shortenInactive(30)
	if d.erased("old") {
		t.Fatal("setup: the live data was already anonymised")
	}

	res := d.restore(backup, d.identity())
	if res["ok"] != true {
		t.Fatalf("the restore failed: %v", res)
	}
	if ok, found, detail := stepOK(res, "retention"); !ok || !found || !strings.Contains(detail, "BEFORE it became the live data") {
		t.Errorf("retention step = %v %v %q", ok, found, detail)
	}
	if !d.erased("old") {
		t.Errorf("RESTORED DATA SERVES AN ACCOUNT PAST THE HOTEL'S CURRENT RETENTION PERIOD: %s", d.username("old"))
	}
	if d.erased("young") {
		t.Errorf("the restore anonymised an account that is inside the period")
	}
	if got := d.str(`SELECT inactive_identity_days::text FROM iam_v2.data_retention_get($1::uuid,$2::uuid)`, d.tenant, d.site); got != "30" {
		t.Errorf("the hotel's current period did not survive the restore: %s days", got)
	}
	if got := d.str(`SELECT count(*)::text FROM iam_v2.data_retention_changes WHERE tenant_id=$1 AND site_id=$2 AND change_reason LIKE 'the hotel shortened%'`, d.tenant, d.site); got != "1" {
		t.Errorf("the change history of the period was not carried across: %s", got)
	}
	d.assertFinancialGuarantees(res, 1)
}

// A hotel with no period set (the defaults apply) restores a backup that held a custom, longer one: the defaults apply to the restored data.
func TestDrill_NoPeriodSetHereMeansTheDefaultsNotTheBackupsPeriod(t *testing.T) {
	d := newDrill(t)
	d.seedAgedAccount("old", 400) // past the 365 default
	d.shortenInactive(3650)       // the backup holds a ten-year period
	backup := d.takeBackup()
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM iam_v2.site_data_retention WHERE tenant_id=$1 AND site_id=$2`, d.tenant, d.site); err != nil {
		t.Fatal(err)
	}
	res := d.restore(backup, d.identity())
	if res["ok"] != true {
		t.Fatalf("the restore failed: %v", res)
	}
	if got := d.str(`SELECT count(*)::text FROM iam_v2.site_data_retention WHERE tenant_id=$1 AND site_id=$2`, d.tenant, d.site); got != "0" {
		t.Errorf("the backup's period survived although this appliance uses the defaults")
	}
	if !d.erased("old") {
		t.Errorf("RESTORED DATA SERVES AN ACCOUNT PAST THE DEFAULT PERIOD: %s", d.username("old"))
	}
}

// RETENTION FAILS (or times out, or is interrupted) ON THE STAGING DATA: nothing is replaced. The live database is exactly as it was, the
// restore marker has not moved, the staging copy of the backup's personal data is gone, and the appliance serves again.
func TestDrill_RetentionThatFailsStopsTheRestoreAndReplacesNothing(t *testing.T) {
	d := newDrill(t)
	d.seedAgedAccount("old", 100)
	backup := d.takeBackup()
	d.seedAccount("after") // exists only in the live data
	gen, _ := readMarkerGeneration()

	old := retentionPassFn
	defer func() { retentionPassFn = old }()
	for name, fault := range map[string]func(ctx context.Context) (int, error){
		"fails":       func(context.Context) (int, error) { return 0, errors.New("the function failed") },
		"interrupted": func(ctx context.Context) (int, error) { <-ctx.Done(); return 0, ctx.Err() },
	} {
		fault := fault
		retentionPassFn = func(ctx context.Context, _, _, _, _ string) (int, error) { return fault(ctx) }
		oldDeadline := restoreRetentionDeadline
		restoreRetentionDeadline = 2 * time.Second
		res := d.restore(backup, d.identity())
		restoreRetentionDeadline = oldDeadline

		if res["ok"] != false {
			t.Fatalf("%s: a restore whose retention did not complete reported success: %v", name, res)
		}
		if ok, found, detail := stepOK(res, "retention"); ok || !found || !strings.Contains(detail, "nothing was replaced") {
			t.Errorf("%s: retention step = %v %v %q", name, ok, found, detail)
		}
		if _, found, _ := stepOK(res, "swap"); found {
			t.Errorf("%s: the swap was reached although retention had not completed", name)
		}
		if got := d.str(`SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, d.accts["after"]); got != d.emails["after"] {
			t.Errorf("%s: THE LIVE DATA WAS CHANGED BY A RESTORE THAT FAILED: %q", name, got)
		}
		if got, _ := readMarkerGeneration(); got != gen {
			t.Errorf("%s: the restore marker moved to %d without a swap (was %d)", name, got, gen)
		}
		if left := d.stagingDatabases(); left != "" {
			t.Errorf("%s: the staging copy of the backup's personal data was left behind: %s", name, left)
		}
	}

	// RECOVERY: with the fault gone the same restore goes through, and the live data is then the (retention-clean) backup.
	retentionPassFn = old
	res := d.restore(backup, d.identity())
	if res["ok"] != true {
		t.Fatalf("the restore did not succeed once retention could run: %v", res)
	}
	if ok, found, _ := stepOK(res, "retention"); !ok || !found {
		t.Errorf("retention step after recovery: %v %v", ok, found)
	}
	d.assertFinancialGuarantees(res, gen+1)
}

// A backup that predates the retention functions cannot have retention applied, so it is refused with nothing replaced.
func TestDrill_ABackupThatPredatesRetentionIsRefused(t *testing.T) {
	d := newDrill(t)
	ctx := context.Background()
	if out, err := exec.Command("docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc", "DROP DATABASE IF EXISTS retention_probe").CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
	if out, err := exec.Command("docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc", "CREATE DATABASE retention_probe").CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", pgContainer, "psql", "-U", pgUser, "-d", "postgres", "-tAc", "DROP DATABASE IF EXISTS retention_probe WITH (FORCE)").Run()
	})
	if present, err := retentionFunctionsPresent(ctx, "retention_probe"); err != nil || present {
		t.Fatalf("an empty database claims to have the retention functions: %v %v", present, err)
	}
	if present, err := retentionFunctionsPresent(ctx, pgDatabase); err != nil || !present {
		t.Fatalf("the appliance schema lacks the retention functions: %v %v", present, err)
	}
	detail, ok := d.s.restoreRetention(ctx, "retention_probe")
	if ok || !strings.Contains(detail, "predates data retention") {
		t.Fatalf("restoreRetention on a database without the functions = %v %q", ok, detail)
	}
}

// THE REMAINS OF AN INTERRUPTED RESTORE: a staging database that is plainly dead is dropped (it is a full copy of personal data no retention
// was applied to); one that may still be in use is left alone.
func TestDrill_AnInterruptedRestoresStagingDatabaseIsSweptWhenDead(t *testing.T) {
	d := newDrill(t)
	ctx := context.Background()
	dead := "restore_" + strings.ToLower(time.Now().UTC().Add(-8*time.Hour).Format("20060102T150405Z"))
	fresh := "restore_" + strings.ToLower(time.Now().UTC().Add(-5*time.Minute).Format("20060102T150405Z"))
	for _, n := range []string{dead, fresh} {
		if _, err := execPsql(ctx, "postgres", "CREATE DATABASE "+n); err != nil {
			t.Fatal(err)
		}
		n := n
		t.Cleanup(func() {
			_, _ = execPsql(context.Background(), "postgres", "DROP DATABASE IF EXISTS "+n+" WITH (FORCE)")
		})
	}
	d.s.sweepSetAside(ctx)
	got := d.stagingDatabases()
	if strings.Contains(got, dead) {
		t.Errorf("the dead staging database is still there: %s", got)
	}
	if !strings.Contains(got, fresh) {
		t.Errorf("a staging database that may still be in use was removed: %s", got)
	}
}

// A backup whose retention functions differ from the ones in force has older rules: refused, nothing replaced. (The drill's migration ledger
// is empty, as on a factory-clean appliance, which is why the comparison is of definitions and not of a version number.)
func TestDrill_ABackupWithOlderRetentionRulesIsRefused(t *testing.T) {
	d := newDrill(t)
	backup := d.takeBackup()
	db := "restore_probe_rules"
	d.stagingFromBackup(backup, db)
	ctx := context.Background()
	same, err := retentionRulesFingerprint(ctx, db)
	live, err2 := retentionRulesFingerprint(ctx, pgDatabase)
	if err != nil || err2 != nil || same != live || same == "" {
		t.Fatalf("a backup of this very schema must carry the rules in force: %q vs %q (%v %v)", same, live, err, err2)
	}
	// make one retention function differ, as an older implementation would
	if _, err := execPsql(ctx, db, `ALTER FUNCTION iam_v2.run_staff_note_retention(uuid,uuid,integer) SET search_path TO 'iam_v2'`); err != nil {
		t.Fatal(err)
	}
	detail, ok := d.s.restoreRetention(ctx, db)
	if ok || !strings.Contains(detail, "older retention rules") || !strings.Contains(detail, "Nothing was replaced") {
		t.Fatalf("an older-rules backup was accepted: %v %q", ok, detail)
	}
}

// A process killed mid-restore leaves the maintenance flag behind as well: the dead staging copy is removed regardless, and what may still
// be a running restore is kept.
func TestDrill_AStaleMaintenanceFlagDoesNotKeepADeadStagingCopy(t *testing.T) {
	d := newDrill(t)
	ctx := context.Background()
	dead := "restore_" + strings.ToLower(time.Now().UTC().Add(-8*time.Hour).Format("20060102T150405Z"))
	if _, err := execPsql(ctx, "postgres", "CREATE DATABASE "+dead); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = execPsql(context.Background(), "postgres", "DROP DATABASE IF EXISTS "+dead+" WITH (FORCE)")
	})
	if err := maintenanceOn("restore", "x"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceOff)
	d.s.sweepSetAside(ctx)
	if got := d.stagingDatabases(); strings.Contains(got, dead) {
		t.Errorf("the maintenance flag kept the dead staging copy of personal data: %s", got)
	}
	if !readMaintenance().Active {
		t.Errorf("the sweep cleared the maintenance flag, which it must never do")
	}
}
