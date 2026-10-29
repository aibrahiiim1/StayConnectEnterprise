package api

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

// What the Central RUNTIME role (ctrlapi_rt, migration 0063, audit P3 M1) can and cannot do. Meaningful only when the
// pool really is that role, so it runs only under scripts/ci/central-restricted-role-tests.sh.
func TestRuntimeRoleHasOnlyTheLeastPrivilege(t *testing.T) {
	if os.Getenv(testdb.RoleEnvVar) == "" {
		t.Skip(testdb.RoleEnvVar + " is not set: this test needs the pool to be the Central runtime role")
	}
	pool := testdb.New(t)
	owner := testdb.OwnerPool(t, pool)
	ctx := context.Background()

	var who string
	var super, createRole, createDB, bypass bool
	if err := pool.QueryRow(ctx, `SELECT current_user, r.rolsuper, r.rolcreaterole, r.rolcreatedb, r.rolbypassrls
	    FROM pg_roles r WHERE r.rolname = current_user`).Scan(&who, &super, &createRole, &createDB, &bypass); err != nil {
		t.Fatal(err)
	}
	if who != "ctrlapi_rt" || super || createRole || createDB || bypass {
		t.Fatalf("pool is %s super=%v createrole=%v createdb=%v bypassrls=%v", who, super, createRole, createDB, bypass)
	}

	// Seed one row in each append-only table's neighbour the test needs, as the owner.
	var opID string
	if err := owner.QueryRow(ctx, `INSERT INTO operators (email, display_name, password_hash, status)
	    VALUES ('rt-probe@central.test','Probe','x','active') RETURNING id::text`).Scan(&opID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO audit_log (actor_type, actor_id, action, payload)
	    VALUES ('system','rt-probe','probe.row','{"email":"RT-Probe@Central.test"}')`); err != nil {
		t.Fatalf("the runtime role must be able to append to audit_log: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO audit_log (ts, actor_type, actor_id, action) VALUES (now() + interval '400 days','system','rt-probe','probe.future-chunk')`); err != nil {
		t.Fatalf("appending to a new hypertable chunk: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO portal_tls_events (kind, actor, detail) VALUES ('probe','rt-probe','{}')`); err != nil {
		t.Fatalf("the runtime role must be able to append to portal_tls_events: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO appliance_lifecycle_events (appliance_id, to_state, actor)
	    SELECT gen_random_uuid(), 'active', 'rt-probe@central.test' WHERE false`); err != nil {
		t.Fatalf("appliance_lifecycle_events is not writable by the runtime role: %v", err)
	}

	// SQLSTATE 42501 insufficient_privilege unless noted.
	cases := []struct {
		name, sql, state string
		contains         string
	}{
		{"create a table", `CREATE TABLE rt_probe (id int)`, "42501", "permission denied for schema public"},
		{"create a table in a new schema", `CREATE SCHEMA rt_probe`, "42501", "permission denied for database"},
		{"alter a table", `ALTER TABLE operators ADD COLUMN rt_probe int`, "42501", "must be owner of table operators"},
		{"drop a table", `DROP TABLE operators`, "42501", "must be owner of table operators"},
		{"truncate a table", `TRUNCATE operators CASCADE`, "42501", "permission denied for table"},
		{"truncate the audit log", `TRUNCATE audit_log`, "42501", "permission denied for table audit_log"},
		{"disable a trigger", `ALTER TABLE audit_log DISABLE TRIGGER audit_log_append_only`, "42501", "must be owner of table audit_log"},
		{"disable all triggers", `ALTER TABLE portal_tls_events DISABLE TRIGGER ALL`, "42501", "must be owner of table portal_tls_events"},
		{"drop a trigger", `DROP TRIGGER audit_log_append_only ON audit_log`, "42501", "must be owner of relation audit_log"},
		{"replace a guard function", `CREATE OR REPLACE FUNCTION central_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`, "42501", "permission denied for schema public"},
		{"drop a guard function", `DROP FUNCTION central_history_guard() CASCADE`, "42501", "must be owner of function central_history_guard"},
		{"update an audit row", `UPDATE audit_log SET action = 'tampered' WHERE actor_id = 'rt-probe'`, "42501", "permission denied for table audit_log"},
		{"delete an audit row", `DELETE FROM audit_log WHERE actor_id = 'rt-probe'`, "42501", "permission denied for table audit_log"},
		{"update a portal certificate event", `UPDATE portal_tls_events SET actor = 'x'`, "42501", "permission denied for table portal_tls_events"},
		{"delete a portal certificate event", `DELETE FROM portal_tls_events`, "42501", "permission denied for table portal_tls_events"},
		{"update a lifecycle event", `UPDATE appliance_lifecycle_events SET actor = 'x'`, "42501", "permission denied for table appliance_lifecycle_events"},
		{"delete a certificate revocation", `DELETE FROM appliance_certificate_revocations`, "42501", "permission denied for table appliance_certificate_revocations"},
		{"write the migration ledger", `DELETE FROM schema_migrations`, "42501", "permission denied for table schema_migrations"},
		{"read the scrub queue", `SELECT * FROM central_forget_pending`, "42501", "permission denied for table central_forget_pending"},
		{"create a role", `CREATE ROLE rt_probe_role`, "42501", "permission denied to create role"},
		{"alter its own role", `ALTER ROLE ctrlapi_rt SUPERUSER`, "42501", "permission denied"},
		{"read pg_authid", `SELECT rolpassword FROM pg_authid`, "42501", "permission denied for table pg_authid"},
		{"copy to a program", `COPY (SELECT 1) TO PROGRAM 'id'`, "42501", "permission denied to COPY to or from an external program"},
		{"copy from a server file", `COPY operators FROM '/etc/passwd'`, "42501", "permission denied to COPY from a file"},
		{"read a server file", `SELECT pg_read_file('/etc/passwd')`, "42501", "permission denied for function pg_read_file"},
		{"create an extension", `CREATE EXTENSION IF NOT EXISTS hstore`, "42501", "permission denied"},
		{"run the grant function", `SELECT central_apply_runtime_grants()`, "42501", "permission denied for function central_apply_runtime_grants"},
		{"lift the retention flag and update", `SELECT set_config('central.retention','on',true); UPDATE audit_log SET action='tampered'`, "42501", "permission denied for table audit_log"},
		{"drop the database", `DROP DATABASE postgres`, "42501", "must be owner of database postgres"},
	}
	for _, c := range cases {
		_, err := pool.Exec(ctx, c.sql)
		pe, ok := err.(*pgconn.PgError)
		if !ok {
			// A multi-statement string comes back wrapped by pgx in the simple protocol only; anything else is a failure.
			t.Errorf("%s: want SQLSTATE %s, got %v", c.name, c.state, err)
			continue
		}
		if pe.Code != c.state || !strings.Contains(pe.Message, c.contains) {
			t.Errorf("%s: got %s %q, want %s containing %q", c.name, pe.Code, pe.Message, c.state, c.contains)
		}
	}

	// A GRANT by a role without grant option only warns ("no privileges were granted"); what counts is the result.
	_, _ = pool.Exec(ctx, `GRANT UPDATE, DELETE, TRUNCATE ON audit_log TO ctrlapi_rt`)
	var upd, del, trunc, schemaCreate bool
	if err := owner.QueryRow(ctx, `SELECT has_table_privilege('ctrlapi_rt','audit_log','UPDATE'),
	    has_table_privilege('ctrlapi_rt','audit_log','DELETE'), has_table_privilege('ctrlapi_rt','audit_log','TRUNCATE'),
	    has_schema_privilege('ctrlapi_rt','public','CREATE')`).Scan(&upd, &del, &trunc, &schemaCreate); err != nil {
		t.Fatal(err)
	}
	if upd || del || trunc || schemaCreate {
		t.Errorf("after a self-GRANT: update=%v delete=%v truncate=%v create-in-public=%v, want all false", upd, del, trunc, schemaCreate)
	}

	// The probe rows are intact, whatever the statements above tried.
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE actor_id='rt-probe' AND action LIKE 'probe.%'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("audit probe rows = %d (%v), want 2", n, err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM operators`).Scan(&n); err != nil || n < 1 {
		t.Errorf("operators = %d (%v)", n, err)
	}

	// The same rules hold for the OWNER outside the sanctioned functions: the guard needs the flag AND the owner.
	if _, err := owner.Exec(ctx, `UPDATE audit_log SET action='tampered' WHERE actor_id='rt-probe'`); err == nil ||
		!strings.Contains(err.Error(), "is append-only") {
		t.Errorf("the owner updating audit_log without the retention flag: %v", err)
	}

	// What it MUST be able to do: the sanctioned paths for history.
	if _, err := pool.Exec(ctx, `SELECT central_forget_actor('rt-probe@central.test','removed-user-probe')`); err != nil {
		t.Errorf("central_forget_actor as the runtime role: %v", err)
	}
	var scrubbed int64
	if err := pool.QueryRow(ctx, `SELECT central_forget_audit_apply()`).Scan(&scrubbed); err != nil || scrubbed != 1 {
		t.Errorf("central_forget_audit_apply = %d (%v), want 1 audit payload rewritten", scrubbed, err)
	}
	var queued int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM central_forget_pending`).Scan(&queued); err != nil || queued != 0 {
		t.Errorf("scrub queue after the scrub = %d (%v)", queued, err)
	}
	var payload string
	if err := owner.QueryRow(ctx, `SELECT payload::text FROM audit_log WHERE action='probe.row'`).Scan(&payload); err != nil ||
		strings.Contains(strings.ToLower(payload), "central.test") || !strings.Contains(payload, "removed-user-probe") {
		t.Errorf("case-insensitive scrub left payload %s (%v)", payload, err)
	}
	if _, err := pool.Exec(ctx, `SELECT central_retention_apply()`); err != nil {
		t.Errorf("central_retention_apply as the runtime role: %v", err)
	}
	// Hand the next statement a clean session state: the flag must not have leaked out of the functions.
	var flag string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(current_setting('central.retention', true), '')`).Scan(&flag); err != nil || flag == "on" {
		t.Errorf("retention flag after the functions = %q (%v)", flag, err)
	}
}
