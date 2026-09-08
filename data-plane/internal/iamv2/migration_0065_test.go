package iamv2

// THE CONNECTOR GETS THE ANSWER, NEVER THE TABLE.
//
// iam_v2.entitlement_usage_bytes read iam_v2.accounting_records with the CALLER's rights. svc_acctd holds
// SELECT on that table so it never noticed; svc_pmsd does not, deliberately -- Gate-P lists accounting_records
// among the financial/metering tables the PMS connector must never hold. So pmsd's checkout-boundary call
// failed with "permission denied for table accounting_records", the stay-event applier stopped, and
// p3_feed_authorizes then refused every PMS Auth Context on the interface, which surfaced to guests as the
// uniform "we could not verify your stay".
//
// The fix is the pattern p3_entitlement_data_usage already uses: SECURITY DEFINER, owned by iam_v2_owner, with
// a pinned search_path. These prove both halves -- the call now succeeds AND the table is still forbidden --
// because a fix that quietly granted the table would make the first half pass and the system less safe.
//
// Set MIGRATION_TEST_DSN to run; skipped otherwise.

import (
	"context"
	"os"
	"testing"
	"time"
)

const mig0065Fixture = `
DROP SCHEMA IF EXISTS iam_v2 CASCADE;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='iam_v2_owner') THEN CREATE ROLE iam_v2_owner; END IF;
END $$;
-- OWNED BY iam_v2_owner, mirroring production. The migration makes the helper SECURITY DEFINER owned by that
-- role, so the body runs AS it: if the role cannot reach the schema, the function fails with "permission
-- denied for schema iam_v2" and the test blames the migration for a fixture that does not resemble the real
-- database. It did exactly that on the first run.
CREATE SCHEMA iam_v2 AUTHORIZATION iam_v2_owner;
CREATE TABLE iam_v2.sessions (id uuid PRIMARY KEY, entitlement_id uuid);
CREATE TABLE iam_v2.session_entitlement_bindings (
  session_id uuid, entitlement_id uuid, bound_from timestamptz, bound_until timestamptz);
CREATE TABLE iam_v2.accounting_records (
  id bigserial PRIMARY KEY, session_id uuid, sampled_at timestamptz, bytes_up bigint, bytes_down bigint);

ALTER TABLE iam_v2.sessions OWNER TO iam_v2_owner;
ALTER TABLE iam_v2.session_entitlement_bindings OWNER TO iam_v2_owner;
ALTER TABLE iam_v2.accounting_records OWNER TO iam_v2_owner;

-- One entitlement, one bound session, four samples of 1 up / 3 down.
INSERT INTO iam_v2.sessions VALUES ('55555555-5555-5555-5555-555555555555', NULL);
INSERT INTO iam_v2.session_entitlement_bindings VALUES
  ('55555555-5555-5555-5555-555555555555','44444444-4444-4444-4444-444444444444','2026-01-01','2027-01-01');
INSERT INTO iam_v2.accounting_records (session_id, sampled_at, bytes_up, bytes_down) VALUES
  ('55555555-5555-5555-5555-555555555555','2026-03-01T00:00:00Z',1,3),
  ('55555555-5555-5555-5555-555555555555','2026-03-01T01:00:00Z',1,3),
  ('55555555-5555-5555-5555-555555555555','2026-03-01T02:00:00Z',1,3),
  ('55555555-5555-5555-5555-555555555555','2026-03-01T03:00:00Z',1,3);

-- A SECOND entitlement whose session has NO binding row: the fallback arm.
INSERT INTO iam_v2.sessions VALUES
  ('66666666-6666-6666-6666-666666666666','77777777-7777-7777-7777-777777777777');
INSERT INTO iam_v2.accounting_records (session_id, sampled_at, bytes_up, bytes_down) VALUES
  ('66666666-6666-6666-6666-666666666666','2026-03-01T00:00:00Z',5,7);

-- The pre-0065 definition: SECURITY INVOKER, exactly as it was on the appliance.
CREATE OR REPLACE FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamptz)
RETURNS TABLE(bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamptz)
LANGUAGE sql STABLE SET search_path = iam_v2, pg_temp
AS $$
  SELECT COALESCE(sum(ar.bytes_up),0)::bigint, COALESCE(sum(ar.bytes_down),0)::bigint,
         count(*)::bigint, max(ar.sampled_at)
  FROM iam_v2.accounting_records ar
  JOIN iam_v2.sessions s ON s.id = ar.session_id
  WHERE ar.sampled_at <= p_at
    AND (
      EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
              WHERE b.session_id = ar.session_id AND b.entitlement_id = p_ent
                AND b.bound_from <= ar.sampled_at AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at))
      OR (s.entitlement_id = p_ent
          AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = ar.session_id))
    );
$$;
`

const mig0065Up = "0065_the_usage_helper_reads_on_its_own_authority.up.sql"
const mig0065Down = "0065_the_usage_helper_reads_on_its_own_authority.down.sql"

// usageProbeRole stands in for svc_pmsd: it may execute the helper and holds NO privilege on the table.
const mig0065Roles = `
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='t_usage_caller') THEN CREATE ROLE t_usage_caller; END IF;
END $$;
GRANT USAGE ON SCHEMA iam_v2 TO t_usage_caller;
GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO t_usage_caller;
-- Deliberately NO grant on iam_v2.accounting_records, mirroring the real Gate-P prohibition.
`

func TestMigration0065(t *testing.T) {
	c := mig0064Conn(t) // same guard: refuses any database named for a real site
	ctx := context.Background()
	if _, err := c.Exec(ctx, mig0065Fixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := c.Exec(ctx, mig0065Roles); err != nil {
		t.Fatalf("roles: %v", err)
	}

	// 1. THE DEFECT REPRODUCES. As SECURITY INVOKER, a caller without the table is refused -- which is the
	//    error the appliance logged every seven seconds while stay events piled up.
	if _, err := c.Exec(ctx, `SET ROLE t_usage_caller`); err != nil {
		t.Fatal(err)
	}
	var up, down int64
	err := c.QueryRow(ctx,
		`SELECT bytes_up, bytes_down FROM iam_v2.entitlement_usage_bytes($1, now())`,
		"44444444-4444-4444-4444-444444444444").Scan(&up, &down)
	if err == nil {
		t.Fatal("the pre-0065 function let a caller without the table read it; the defect does not reproduce, " +
			"so this test would not prove the fix")
	}
	if _, e := c.Exec(ctx, `RESET ROLE`); e != nil {
		t.Fatal(e)
	}

	// The value the owner sees, before the migration, is the value the caller must see after it.
	var wantUp, wantDown, wantRecords int64
	var wantLatest time.Time
	if err := c.QueryRow(ctx,
		`SELECT bytes_up, bytes_down, records, latest_sampled_at FROM iam_v2.entitlement_usage_bytes($1, now())`,
		"44444444-4444-4444-4444-444444444444").Scan(&wantUp, &wantDown, &wantRecords, &wantLatest); err != nil {
		t.Fatal(err)
	}
	if wantUp != 4 || wantDown != 12 || wantRecords != 4 {
		t.Fatalf("fixture arithmetic is wrong: up=%d down=%d records=%d, want 4/12/4", wantUp, wantDown, wantRecords)
	}

	applySQLFile(t, c, mig0065Up)

	// 2. THE CALL NOW SUCCEEDS for a role that still has no table privilege.
	if _, err := c.Exec(ctx, `GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO t_usage_caller`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `SET ROLE t_usage_caller`); err != nil {
		t.Fatal(err)
	}
	var gotUp, gotDown, gotRecords int64
	var gotLatest time.Time
	if err := c.QueryRow(ctx,
		`SELECT bytes_up, bytes_down, records, latest_sampled_at FROM iam_v2.entitlement_usage_bytes($1, now())`,
		"44444444-4444-4444-4444-444444444444").Scan(&gotUp, &gotDown, &gotRecords, &gotLatest); err != nil {
		t.Fatalf("after 0065 the caller still cannot read its own answer: %v", err)
	}

	// 3. SEMANTICS ARE UNCHANGED -- same four numbers the owner got before the migration.
	if gotUp != wantUp || gotDown != wantDown || gotRecords != wantRecords || !gotLatest.Equal(wantLatest) {
		t.Errorf("the aggregate changed: got %d/%d/%d/%v, want %d/%d/%d/%v",
			gotUp, gotDown, gotRecords, gotLatest, wantUp, wantDown, wantRecords, wantLatest)
	}

	// 4. THE <= p_at BOUND still bounds. Two samples at or before 01:00.
	if err := c.QueryRow(ctx,
		`SELECT bytes_up, records FROM iam_v2.entitlement_usage_bytes($1, '2026-03-01T01:00:00Z'::timestamptz)`,
		"44444444-4444-4444-4444-444444444444").Scan(&gotUp, &gotRecords); err != nil {
		t.Fatal(err)
	}
	if gotUp != 2 || gotRecords != 2 {
		t.Errorf("the instant bound broke: up=%d records=%d at 01:00, want 2/2", gotUp, gotRecords)
	}

	// 5. THE NO-BINDING FALLBACK still applies, and only to its own entitlement.
	if err := c.QueryRow(ctx,
		`SELECT bytes_up, bytes_down FROM iam_v2.entitlement_usage_bytes($1, now())`,
		"77777777-7777-7777-7777-777777777777").Scan(&gotUp, &gotDown); err != nil {
		t.Fatal(err)
	}
	if gotUp != 5 || gotDown != 7 {
		t.Errorf("the no-binding fallback changed: got %d/%d, want 5/7", gotUp, gotDown)
	}

	// 6. CROSS-SCOPE READS STAY EMPTY. A definer function is a privilege boundary, so an unknown entitlement
	//    must yield zeroes -- never another entitlement's bytes.
	if err := c.QueryRow(ctx,
		`SELECT bytes_up, bytes_down, records FROM iam_v2.entitlement_usage_bytes($1, now())`,
		"00000000-0000-0000-0000-000000000000").Scan(&gotUp, &gotDown, &gotRecords); err != nil {
		t.Fatal(err)
	}
	if gotUp != 0 || gotDown != 0 || gotRecords != 0 {
		t.Errorf("an unknown entitlement returned %d/%d/%d, want zeroes", gotUp, gotDown, gotRecords)
	}

	// 7. THE TABLE IS STILL FORBIDDEN. The definer boundary must not have become a table grant.
	var n int
	if err := c.QueryRow(ctx, `SELECT count(*) FROM iam_v2.accounting_records`).Scan(&n); err == nil {
		t.Error("the caller can now SELECT accounting_records directly; the fix widened privilege instead of " +
			"narrowing the path to it")
	}
	if _, err := c.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}

	// 8. THE FUNCTION'S OWN SHAPE: definer, owned by the schema owner, pinned search_path, not public.
	var secdef bool
	var owner string
	var cfg []string
	if err := c.QueryRow(ctx, `
		SELECT p.prosecdef, pg_get_userbyid(p.proowner), p.proconfig
		  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname='iam_v2' AND p.proname='entitlement_usage_bytes'`).Scan(&secdef, &owner, &cfg); err != nil {
		t.Fatal(err)
	}
	if !secdef {
		t.Error("entitlement_usage_bytes is not SECURITY DEFINER")
	}
	if owner != "iam_v2_owner" {
		t.Errorf("owner is %q, not iam_v2_owner -- the owner IS the privilege boundary", owner)
	}
	var pinned bool
	for _, c := range cfg {
		if len(c) > 12 && c[:12] == "search_path=" {
			pinned = true
		}
	}
	if !pinned {
		t.Error("no pinned search_path: a definer function without one can be pointed at a caller's tables")
	}
	var publicExec bool
	if err := c.QueryRow(ctx, `
		SELECT has_function_privilege('public', p.oid, 'EXECUTE')
		  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname='iam_v2' AND p.proname='entitlement_usage_bytes'`).Scan(&publicExec); err != nil {
		t.Fatal(err)
	}
	if publicExec {
		t.Error("PUBLIC can execute the helper")
	}

	// 9. THE DOWN MIGRATION restores SECURITY INVOKER, and with it the original refusal.
	applySQLFile(t, c, mig0065Down)
	if err := c.QueryRow(ctx, `
		SELECT p.prosecdef FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname='iam_v2' AND p.proname='entitlement_usage_bytes'`).Scan(&secdef); err != nil {
		t.Fatal(err)
	}
	if secdef {
		t.Error("the down migration left the function SECURITY DEFINER")
	}
	if _, err := c.Exec(ctx, `SET ROLE t_usage_caller`); err != nil {
		t.Fatal(err)
	}
	if err := c.QueryRow(ctx,
		`SELECT bytes_up FROM iam_v2.entitlement_usage_bytes($1, now())`,
		"44444444-4444-4444-4444-444444444444").Scan(&gotUp); err == nil {
		t.Error("after the down migration the caller could still read; the rollback did not restore invoker rights")
	}
	if _, err := c.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Exec(ctx, `DROP OWNED BY t_usage_caller; DROP ROLE IF EXISTS t_usage_caller`)
	_ = os.Getenv("") // keep os imported for the DSN guard shared with 0064
}
