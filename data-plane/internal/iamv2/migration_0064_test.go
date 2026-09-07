package iamv2

// MIGRATION 0064 AGAINST A REAL POSTGRES.
//
// The properties this migration claims are database properties -- a CHECK that refuses a malformed policy, a
// crossing function that prefers the entitlement's own quota, and above all that applying it changes NOTHING
// for rows that already exist. None of those can be established by reading the file, so this applies it to a
// throwaway database and asks.
//
// Set MIGRATION_TEST_DSN to run it; skipped otherwise, like every other database test here.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const mig0064Fixture = `
DROP SCHEMA IF EXISTS iam_v2 CASCADE;
CREATE SCHEMA iam_v2;
CREATE TABLE iam_v2.service_plan_revisions (id uuid PRIMARY KEY, data_quota_bytes bigint);
CREATE TABLE iam_v2.internet_package_revisions (
  id uuid PRIMARY KEY, package_id uuid NOT NULL, revision_no int NOT NULL);
CREATE TABLE iam_v2.entitlements (id uuid PRIMARY KEY, service_plan_revision_id uuid);
CREATE TABLE iam_v2.accounting_records (
  id bigserial PRIMARY KEY, session_id uuid, sampled_at timestamptz, bytes_up bigint, bytes_down bigint);
CREATE TABLE iam_v2.session_entitlement_bindings (
  session_id uuid, entitlement_id uuid, bound_from timestamptz, bound_until timestamptz);

-- A plan with a 10-byte allowance, one package revision, and one PRE-EXISTING entitlement on that plan.
INSERT INTO iam_v2.service_plan_revisions VALUES ('11111111-1111-1111-1111-111111111111', 10);
INSERT INTO iam_v2.internet_package_revisions VALUES
  ('22222222-2222-2222-2222-222222222222','33333333-3333-3333-3333-333333333333',1);
INSERT INTO iam_v2.entitlements VALUES
  ('44444444-4444-4444-4444-444444444444','11111111-1111-1111-1111-111111111111');
INSERT INTO iam_v2.session_entitlement_bindings VALUES
  ('55555555-5555-5555-5555-555555555555','44444444-4444-4444-4444-444444444444','2026-01-01','2027-01-01');
-- Four samples of 4 bytes each: the running total reaches 8 at the second and 12 at the third.
INSERT INTO iam_v2.accounting_records (session_id, sampled_at, bytes_up, bytes_down) VALUES
  ('55555555-5555-5555-5555-555555555555','2026-03-01T00:00:00Z',2,2),
  ('55555555-5555-5555-5555-555555555555','2026-03-01T01:00:00Z',2,2),
  ('55555555-5555-5555-5555-555555555555','2026-03-01T02:00:00Z',2,2),
  ('55555555-5555-5555-5555-555555555555','2026-03-01T03:00:00Z',2,2);
`

func mig0064Conn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("MIGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("MIGRATION_TEST_DSN not set; skipping the 0064 migration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func applySQLFile(t *testing.T, c *pgx.Conn, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if _, err := c.Exec(context.Background(), string(b)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

const mig0064Up = "0064_the_allowance_a_stay_earned_is_frozen_when_it_is_granted.up.sql"
const mig0064Down = "0064_the_allowance_a_stay_earned_is_frozen_when_it_is_granted.down.sql"

func TestMigration0064(t *testing.T) {
	c := mig0064Conn(t)
	ctx := context.Background()
	if _, err := c.Exec(ctx, mig0064Fixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// The crossing BEFORE the migration: the plan's 10-byte quota is reached at the third sample (running 12).
	var before *time.Time
	baseline := `SELECT min(x.sampled_at) FROM iam_v2.entitlements e
	   JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
	   CROSS JOIN LATERAL (
	     SELECT ar.sampled_at, sum(ar.bytes_up+ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
	       FROM iam_v2.accounting_records ar
	       JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
	        AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
	        AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)) x
	  WHERE e.id = $1 AND spr.data_quota_bytes IS NOT NULL AND x.running >= spr.data_quota_bytes`
	if err := c.QueryRow(ctx, baseline, "44444444-4444-4444-4444-444444444444").Scan(&before); err != nil {
		t.Fatalf("baseline crossing: %v", err)
	}

	applySQLFile(t, c, mig0064Up)

	// 1. NOTHING WAS BACKFILLED. The migration's own DO block asserts this, but asserting it here means the
	//    test fails loudly rather than the migration merely refusing to apply.
	var backfilled int
	if err := c.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.entitlements WHERE data_quota_bytes IS NOT NULL`).Scan(&backfilled); err != nil {
		t.Fatal(err)
	}
	if backfilled != 0 {
		t.Fatalf("%d existing entitlements were given a quota", backfilled)
	}

	// 2. AN EXISTING ENTITLEMENT CROSSES AT EXACTLY THE SAME INSTANT AS BEFORE.
	var after *time.Time
	if err := c.QueryRow(ctx, `SELECT iam_v2.p6_data_crossing($1)`,
		"44444444-4444-4444-4444-444444444444").Scan(&after); err != nil {
		t.Fatalf("p6_data_crossing: %v", err)
	}
	if before == nil || after == nil || !before.Equal(*after) {
		t.Fatalf("the crossing moved: was %v, now %v", before, after)
	}

	// 3. A SNAPSHOT OVERRIDES THE PLAN, and terminates earlier when it is smaller. 4 bytes is reached at the
	//    FIRST sample, where the plan's 10 was not reached until the third -- so the snapshot is genuinely
	//    what the crossing reads, not merely stored.
	if _, err := c.Exec(ctx,
		`UPDATE iam_v2.entitlements SET data_quota_bytes = 4 WHERE id = $1`,
		"44444444-4444-4444-4444-444444444444"); err != nil {
		t.Fatal(err)
	}
	var snapped *time.Time
	if err := c.QueryRow(ctx, `SELECT iam_v2.p6_data_crossing($1)`,
		"44444444-4444-4444-4444-444444444444").Scan(&snapped); err != nil {
		t.Fatal(err)
	}
	if snapped == nil || !snapped.Before(*after) {
		t.Fatalf("a 4-byte snapshot crossed at %v; expected earlier than the plan's %v", snapped, after)
	}

	// 4. A SNAPSHOT STILL TERMINATES WHEN THE PLAN HAS NO QUOTA OF ITS OWN. This is what the LEFT JOIN buys:
	//    a PER_STAY_NIGHT package may pin a plan that sets only the speed.
	if _, err := c.Exec(ctx,
		`UPDATE iam_v2.service_plan_revisions SET data_quota_bytes = NULL`); err != nil {
		t.Fatal(err)
	}
	var noPlanQuota *time.Time
	if err := c.QueryRow(ctx, `SELECT iam_v2.p6_data_crossing($1)`,
		"44444444-4444-4444-4444-444444444444").Scan(&noPlanQuota); err != nil {
		t.Fatal(err)
	}
	if noPlanQuota == nil {
		t.Fatal("an entitlement with its own quota stopped terminating once the plan had none")
	}

	// 5. A ZERO SNAPSHOT IS REFUSED: it would be exhausted the instant it was created.
	if _, err := c.Exec(ctx,
		`UPDATE iam_v2.entitlements SET data_quota_bytes = 0 WHERE id = $1`,
		"44444444-4444-4444-4444-444444444444"); err == nil {
		t.Error("a zero-byte allowance was accepted")
	}

	// 6. THE POLICY SHAPE IS ENFORCED by the database, not only by the writer.
	bad := []string{
		`{"mode":"PER_STAY_NIGHT"}`,                                  // no rate
		`{"mode":"PER_STAY_NIGHT","gb_per_night":0}`,                 // zero rate
		`{"mode":"PER_STAY_NIGHT","gb_per_night":1,"max_gb":0}`,      // zero ceiling
		`{"mode":"PER_STAY_NIGHT","gb_per_night":1,"min_gb":10,"max_gb":5}`, // ceiling under floor
		`{"mode":"PER_GUEST_MOOD"}`,                                  // unknown mode
		`"not an object"`,
	}
	for _, b := range bad {
		if _, err := c.Exec(ctx,
			`UPDATE iam_v2.internet_package_revisions SET data_allocation_policy = $1::jsonb WHERE id = $2`,
			b, "22222222-2222-2222-2222-222222222222"); err == nil {
			t.Errorf("the database accepted a malformed policy: %s", b)
		}
	}
	// ...and the approved policy is accepted.
	if _, err := c.Exec(ctx,
		`UPDATE iam_v2.internet_package_revisions SET data_allocation_policy = $1::jsonb WHERE id = $2`,
		`{"mode":"PER_STAY_NIGHT","gb_per_night":1,"min_gb":5,"max_gb":20}`,
		"22222222-2222-2222-2222-222222222222"); err != nil {
		t.Fatalf("the approved policy was refused: %v", err)
	}

	// 7. THE DOWN MIGRATION RESTORES THE PREVIOUS SHAPE.
	applySQLFile(t, c, mig0064Down)
	var stillThere int
	if err := c.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema='iam_v2' AND column_name IN ('data_allocation_policy','data_quota_bytes')`).
		Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 0 {
		t.Errorf("%d of the new columns survived the down migration", stillThere)
	}
}
