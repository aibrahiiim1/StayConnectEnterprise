//go:build integration

package enforce

// AUDIT B-7 (migration 0124): THE ENTITLEMENT GUARD CANNOT BE SWITCHED OFF BY ITS CALLER, AND THE RUNTIME ROLES
// HOLD ONLY WHAT THEIR CODE USES.
//
// Before 0124 any role that could UPDATE iam_v2.entitlements could `SET LOCAL iam_v2.allow_adjust = 'on'` and
// then give a guest their data back or move their window with no adjustment recorded, and could raise
// data_quota_bytes or swap service_plan_revision_id with no setting at all -- svc_scd and svc_pmsd held exactly
// that UPDATE. These cases attempt each write as the roles that matter and assert the database's answer.
//
// Runs against the full Gate-P schema (scripts/lib-fullschema-testdb.sh), where the svc_* roles hold exactly the
// deploy/gatep privileges. A role that is not provisioned skips its case rather than passing it.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// asRole runs fn inside a transaction that has assumed role (SET LOCAL ROLE) and is ALWAYS rolled back, so a
// write that the test expects to be refused can never leave state behind if it is wrongly accepted.
func asRole(t *testing.T, p *pgxpool.Pool, role string, fn func(tx pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatalf("assume %s: %v", role, err)
	}
	return fn(tx)
}

func roleExists(t *testing.T, p *pgxpool.Pool, role string) bool {
	t.Helper()
	return count(t, p, `SELECT count(*) FROM pg_roles WHERE rolname=$1`, role) == 1
}

func wantRefused(t *testing.T, what string, err error, fragment string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ACCEPTED, want refused (%q)", what, fragment)
	}
	if fragment != "" && !strings.Contains(err.Error(), fragment) {
		t.Fatalf("%s: refused for the wrong reason: %v (want %q)", what, err, fragment)
	}
}

// A writer role with table-level UPDATE: the shape svc_scd and svc_pmsd had before 0124. Created inside the
// test's own transaction and gone with its rollback.
func withLegacyWriter(t *testing.T, p *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, s := range []string{
		`CREATE ROLE b7_legacy_writer NOLOGIN`,
		`GRANT USAGE ON SCHEMA iam_v2 TO b7_legacy_writer`,
		`GRANT SELECT, UPDATE ON iam_v2.entitlements TO b7_legacy_writer`,
		`SET LOCAL ROLE b7_legacy_writer`,
	} {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return fn(tx)
}

func TestIntegration_B7_TheCallerCannotSwitchTheGuardOff(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 8000, 3000, 1_000_000)
	win := time.Now().Add(24 * time.Hour)
	ent, _ := grant(t, p, f, &win, time.Now().Add(-time.Hour))

	// The bypass the audit found: a writer sets the switch itself and moves the window.
	err := withLegacyWriter(t, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL iam_v2.allow_adjust = 'on'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET window_ends_at = window_ends_at + interval '30 days' WHERE id=$1`, ent)
		return err
	})
	wantRefused(t, "self-switched window move by a runtime writer", err, "terms of a grant")

	// ...and the unguarded terms: the quota and the plan an entitlement was sold under.
	err = withLegacyWriter(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET data_quota_bytes = 9223372036854775807 WHERE id=$1`, ent)
		return err
	})
	wantRefused(t, "raise data_quota_bytes", err, "terms of a grant")
	err = withLegacyWriter(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET service_plan_revision_id = gen_random_uuid() WHERE id=$1`, ent)
		return err
	})
	wantRefused(t, "swap service_plan_revision_id", err, "terms of a grant")

	// A row LOCK is still available to such a writer -- FOR UPDATE fires no trigger -- and so is a no-op write.
	if err := withLegacyWriter(t, p, func(tx pgx.Tx) error {
		var id string
		return tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.entitlements WHERE id=$1 FOR UPDATE`, ent).Scan(&id)
	}); err != nil {
		t.Fatalf("row lock by a writer: %v", err)
	}

	// The OWNER -- whose definers the switch used to unlock when a caller set it first -- is refused too unless
	// an adjustment was recorded in this transaction.
	err = asRole(t, p, "iam_v2_owner", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL iam_v2.allow_adjust = 'on'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET window_ends_at = window_ends_at + interval '30 days' WHERE id=$1`, ent)
		return err
	})
	wantRefused(t, "owner window move with a forged switch", err, "only via entitlement_adjustments")

	// ...and a recorded adjustment of a DIFFERENT value does not cover this write.
	err = asRole(t, p, "iam_v2_owner", func(tx pgx.Tx) error {
		var adj string
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlement_adjustments
			(tenant_id,site_id,entitlement_id,field,old_value,new_value,actor,reason)
			SELECT tenant_id, site_id, id, 'window_ends_at', window_ends_at::text, (window_ends_at + interval '1 day')::text, NULL, 'b7 test'
			  FROM iam_v2.entitlements WHERE id=$1 RETURNING id::text`, ent).Scan(&adj); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('iam_v2.allow_adjust', $1, true)`, adj); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET window_ends_at = window_ends_at + interval '30 days' WHERE id=$1`, ent)
		return err
	})
	wantRefused(t, "window move that does not match the recorded adjustment", err, "only via entitlement_adjustments")

	// The sanctioned path: apply_adjustment records first and the guard admits exactly that write.
	var before, after time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		tx, err := p.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE iam_v2_owner`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_adjustment($1,'window_ends_at',$2,NULL,'b7 sanctioned adjustment')`,
			ent, before.Add(48*time.Hour).Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}(); err != nil {
		t.Fatalf("sanctioned adjustment refused: %v", err)
	}
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before.Add(48 * time.Hour)) {
		t.Fatalf("window after the sanctioned adjustment = %v, want %v", after, before.Add(48*time.Hour))
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_adjustments WHERE entitlement_id=$1 AND field='window_ends_at'`, ent); n != 1 {
		t.Fatalf("recorded adjustments = %d, want exactly the sanctioned one", n)
	}

	// The maintenance path is unchanged: a superuser with the switch on may still correct a window...
	if err := func() error {
		tx, err := p.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL iam_v2.allow_adjust = 'on'`); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE iam_v2.entitlements SET window_ends_at = window_ends_at - interval '1 hour' WHERE id=$1`, ent)
		return err
	}(); err != nil {
		t.Fatalf("superuser maintenance correction refused: %v", err)
	}
	// ...and without it is refused, as before 0124.
	_, err = p.Exec(ctx, `UPDATE iam_v2.entitlements SET window_ends_at = window_ends_at - interval '1 hour' WHERE id=$1`, ent)
	wantRefused(t, "superuser window move without the switch", err, "only via entitlement_adjustments")

	// apply_adjustment is no longer anybody's but the owner's.
	if roleExists(t, p, "svc_scd") {
		err = asRole(t, p, "svc_scd", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT iam_v2.apply_adjustment($1,'window_ends_at',now()::text,NULL,'x')`, ent)
			return err
		})
		wantRefused(t, "svc_scd calling apply_adjustment", err, "permission denied")
	}
}

func TestIntegration_B7_RuntimeRolesHoldOnlyWhatTheirCodeUses(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	for _, r := range []string{"svc_scd", "svc_acctd", "svc_netd", "svc_edged", "svc_pmsd"} {
		if !roleExists(t, p, r) {
			t.Skipf("%s is not provisioned; this suite runs on the full Gate-P schema", r)
		}
	}
	f := seed(t, p, 8000, 3000, 0)
	win := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &win, time.Now().Add(-time.Hour))

	// svc_scd: the admission row lock still works (cmd/scd/iamv2_session_activate.go, internal/staygrant)...
	if err := asRole(t, p, "svc_scd", func(tx pgx.Tx) error {
		var st string
		return tx.QueryRow(ctx, `SELECT e.status FROM iam_v2.entitlements e
			LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
			WHERE e.id=$1 FOR UPDATE OF e`, ent).Scan(&st)
	}); err != nil {
		t.Fatalf("svc_scd admission lock: %v", err)
	}
	// ...a status change still has to go through the controlled writer...
	err := asRole(t, p, "svc_scd", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET status='SUSPENDED' WHERE id=$1`, ent)
		return err
	})
	wantRefused(t, "svc_scd raw status change", err, "")
	// ...and nothing else on the row or on sessions is writable.
	err = asRole(t, p, "svc_scd", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET window_ends_at = now() + interval '1 year' WHERE id=$1`, ent)
		return err
	})
	wantRefused(t, "svc_scd window write", err, "permission denied")
	err = asRole(t, p, "svc_scd", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now() WHERE id=$1`, sess)
		return err
	})
	wantRefused(t, "svc_scd raw session write", err, "permission denied")

	// svc_netd and svc_acctd: no entitlement state writer, no accounting table write.
	for _, r := range []string{"svc_netd", "svc_acctd"} {
		err = asRole(t, p, r, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now(),'ADMIN')`, ent)
			return err
		})
		wantRefused(t, r+" executing apply_entitlement_transition", err, "permission denied")
	}
	err = asRole(t, p, "svc_acctd", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.accounting_records SET bytes_up = 0 WHERE session_id=$1`, sess)
		return err
	})
	wantRefused(t, "svc_acctd raw accounting write", err, "permission denied")
	err = asRole(t, p, "svc_acctd", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, f.device)
		return err
	})
	wantRefused(t, "svc_acctd authorizing a device", err, "permission denied")

	// The PMS settings writers are the console's alone.
	for _, r := range []string{"svc_scd", "svc_acctd", "svc_netd", "svc_pmsd"} {
		err = asRole(t, p, r, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT iam_v2.pms_reconciliation_settings_set($1,$2,1,1,'x','x',1,1)`, f.tenant, f.site)
			return err
		})
		wantRefused(t, r+" writing PMS reconciliation settings", err, "permission denied")
	}

	// svc_edged: the transfer's one session column, nothing else.
	if err := asRole(t, p, "svc_edged", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET entitlement_id=entitlement_id WHERE id=$1`, sess)
		return err
	}); err != nil {
		t.Fatalf("svc_edged re-pointing a session (internal/transfer): %v", err)
	}
	err = asRole(t, p, "svc_edged", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now() WHERE id=$1`, sess)
		return err
	})
	wantRefused(t, "svc_edged ending a session by raw write", err, "permission denied")

	// svc_pmsd: the checkout conversion's lock and its three session columns.
	if err := asRole(t, p, "svc_pmsd", func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.entitlements WHERE id=$1 FOR UPDATE`, ent).Scan(&id); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT s.id::text FROM iam_v2.sessions s WHERE s.id=$1 FOR UPDATE OF s`, sess).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.sessions s SET state='ended', ended=GREATEST(now(), s.started),
			end_reason='CHECKOUT_BOUNDARY' FROM iam_v2.entitlements e WHERE e.id=s.entitlement_id AND s.id=$1`, sess)
		return err
	}); err != nil {
		t.Fatalf("svc_pmsd checkout conversion writes: %v", err)
	}
	err = asRole(t, p, "svc_pmsd", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET data_quota_bytes = 1 WHERE id=$1`, ent)
		return err
	})
	wantRefused(t, "svc_pmsd rewriting a quota", err, "permission denied")
}
