package main

// THE ACCESS THE APPLIANCE'S SERVICES HAVE TO THE DATABASE, CARRIED ACROSS A RESTORE.
//
// WHY THIS EXISTS, in one sentence: the first successful live restore replaced the data correctly and left
// the appliance unable to serve, because a backup does not contain the grants.
//
// THE LONGER VERSION. Backups are taken with `pg_dump --no-owner --no-privileges`. That was a reasonable
// choice while a dump was only ever loaded into a scratch database to be verified -- ownership and grants
// are noise there, and stripping them makes the dump portable. It stops being reasonable the moment the
// dump is loaded into the database the hotel runs on. The restored database came up owned by the superuser
// with PUBLIC holding the default EXECUTE on every function, and:
//
//   * scd could not read pms_providers or social_oauth_providers and exited;
//   * edged could not reach schema iam_v2 and exited;
//   * once schema access was back, edged refused to serve anyway -- correctly -- because PUBLIC held EXECUTE
//     on iam_v2.apply_entitlement_transition, which is exactly the boundary it checks at startup;
//   * pmsd crash-looped until systemd gave up on it.
//
// Every one of those is a service behaving properly. The database was the thing that had lost its shape.
//
// WHY IT IS CAPTURED RATHER THAN WRITTEN DOWN. The grants are created by numbered migrations, and the
// migrations must stay the single source of truth. Re-deriving them in Go would be a second copy that drifts
// the first time a migration adds a table. So the privilege state is read out of the LIVE database moments
// before the swap and replayed onto the restored one afterwards -- which is also the semantically right
// answer: a restore replaces the hotel's DATA, not the appliance's wiring.
//
// It means any backup restores onto a working appliance, including every backup taken before this existed,
// and it means a backup carried to a different appliance picks up THAT appliance's privilege state rather
// than importing a stranger's.
//
// WHAT MAKES IT TRUSTWORTHY. The same capture is run again after the replay and the two are compared. If the
// restored database's privilege state is not identical to what the live one had, the restore rolls back. A
// replay that half-worked is not a restore; it is an appliance that will fail at an unpredictable moment
// later, which is worse than one that fails now and puts the original data back.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// privilegeCaptureSQL emits every ownership and access decision in the two schemas the appliance uses, as
// statements that can be replayed verbatim.
//
// THE REVOKES ARE THE PART THAT IS EASY TO FORGET. A privilege that is ABSENT is as deliberate as one that is
// present: PostgreSQL grants EXECUTE on every function to PUBLIC by default and the migrations revoke it, so
// a replay that only re-granted would leave the exact hole edged refuses to serve on. `grantee = 0` is
// PUBLIC; a NULL acl means the object still carries the default, so PUBLIC keeps whatever the default gives.
//
// Ownership is included because the owner is the GRANTOR recorded in every acl entry: a function owned by
// the superuser rather than iam_v2_owner is a different security object wearing the same name.
const privilegeCaptureSQL = `
SELECT 1 AS ord, 'ALTER SCHEMA ' || quote_ident(nspname) || ' OWNER TO ' || quote_ident(pg_get_userbyid(nspowner)) || ';' AS stmt
FROM pg_namespace WHERE nspname IN ('public','iam_v2') AND pg_get_userbyid(nspowner) <> 'pg_database_owner'
UNION ALL
SELECT 1, 'ALTER TABLE ' || quote_ident(schemaname) || '.' || quote_ident(tablename) || ' OWNER TO ' || quote_ident(tableowner) || ';'
FROM pg_tables WHERE schemaname IN ('public','iam_v2')
UNION ALL
SELECT 1, 'ALTER FUNCTION ' || quote_ident(n.nspname) || '.' || quote_ident(p.proname)
          || '(' || pg_get_function_identity_arguments(p.oid) || ') OWNER TO ' || quote_ident(pg_get_userbyid(p.proowner)) || ';'
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname IN ('public','iam_v2') AND p.prokind = 'f'
UNION ALL
SELECT 1, 'ALTER SEQUENCE ' || quote_ident(n.nspname) || '.' || quote_ident(c.relname)
          || ' OWNER TO ' || quote_ident(pg_get_userbyid(c.relowner)) || ';'
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname IN ('public','iam_v2') AND c.relkind = 'S'
UNION ALL
SELECT 2, 'REVOKE ALL ON FUNCTION ' || quote_ident(n.nspname) || '.' || quote_ident(p.proname)
          || '(' || pg_get_function_identity_arguments(p.oid) || ') FROM PUBLIC;'
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname IN ('public','iam_v2') AND p.proacl IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0)
UNION ALL
SELECT 2, 'REVOKE ALL ON SCHEMA ' || quote_ident(nspname) || ' FROM PUBLIC;'
FROM pg_namespace WHERE nspname IN ('public','iam_v2') AND nspacl IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM aclexplode(nspacl) a WHERE a.grantee = 0)
UNION ALL
SELECT 2, 'REVOKE ALL ON TABLE ' || quote_ident(n.nspname) || '.' || quote_ident(c.relname) || ' FROM PUBLIC;'
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname IN ('public','iam_v2') AND c.relkind IN ('r','v','m','p') AND c.relacl IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM aclexplode(c.relacl) a WHERE a.grantee = 0)
UNION ALL
SELECT 3, 'GRANT ' || a.privilege_type || ' ON SCHEMA ' || quote_ident(n.nspname)
          || ' TO ' || CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END || ';'
FROM pg_namespace n CROSS JOIN LATERAL aclexplode(n.nspacl) a
WHERE n.nspname IN ('public','iam_v2')
UNION ALL
SELECT 3, 'GRANT ' || a.privilege_type || ' ON TABLE ' || quote_ident(n.nspname) || '.' || quote_ident(c.relname)
          || ' TO ' || CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END || ';'
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) a
WHERE n.nspname IN ('public','iam_v2') AND c.relkind IN ('r','v','m','p')
  AND pg_get_userbyid(a.grantee) <> pg_get_userbyid(c.relowner)
UNION ALL
SELECT 3, 'GRANT ' || a.privilege_type || ' ON SEQUENCE ' || quote_ident(n.nspname) || '.' || quote_ident(c.relname)
          || ' TO ' || CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END || ';'
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) a
WHERE n.nspname IN ('public','iam_v2') AND c.relkind = 'S'
  AND pg_get_userbyid(a.grantee) <> pg_get_userbyid(c.relowner)
UNION ALL
-- COLUMN PRIVILEGES. These live in pg_attribute.attacl, nowhere near pg_class.relacl, and a capture that
-- reads only relacl is blind to them in BOTH directions: the replay drops them and the before/after
-- comparison reports everything identical, because neither side can see the thing that was lost.
SELECT 3, 'GRANT ' || a.privilege_type || ' (' || quote_ident(att.attname) || ') ON TABLE '
          || quote_ident(n.nspname) || '.' || quote_ident(c.relname)
          || ' TO ' || CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END || ';'
FROM pg_attribute att
JOIN pg_class c ON c.oid = att.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN LATERAL aclexplode(att.attacl) a
WHERE n.nspname IN ('public','iam_v2') AND att.attnum > 0 AND NOT att.attisdropped
UNION ALL
SELECT 3, 'GRANT ' || a.privilege_type || ' ON FUNCTION ' || quote_ident(n.nspname) || '.' || quote_ident(p.proname)
          || '(' || pg_get_function_identity_arguments(p.oid) || ')'
          || ' TO ' || CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END || ';'
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace CROSS JOIN LATERAL aclexplode(p.proacl) a
WHERE n.nspname IN ('public','iam_v2')
  AND pg_get_userbyid(a.grantee) <> pg_get_userbyid(p.proowner)
ORDER BY 1, 2`

// unhandledPrivilegeSQL counts the privilege kinds this file does NOT know how to reproduce.
//
// WHY A COUNT AND NOT A BEST EFFORT. A restore that carries most of the privileges and loses the rest is the
// worst outcome available: the appliance comes up, serves a while, and fails on whichever path needed the
// one that went missing -- and the before/after comparison cannot catch it, because a capture is only as
// complete as the catalogs it reads. That is not a hypothetical. Column privileges were missed exactly this
// way, and the restore reported "all rules reinstated and verified identical" while one had been dropped.
//
// So anything not handled here stops the restore BEFORE it touches anything, and says what it found. None of
// these exist on this appliance today; the point is that the day one does, it is a refusal rather than a
// discovery weeks later.
const unhandledPrivilegeSQL = `
SELECT coalesce(string_agg(what || ' (' || n::text || ')', ', '), '')
FROM (
  SELECT 'default privileges' AS what, count(*) AS n FROM pg_default_acl
  UNION ALL
  SELECT 'type privileges', count(*) FROM pg_type t JOIN pg_namespace ns ON ns.oid = t.typnamespace
   WHERE ns.nspname IN ('public','iam_v2') AND t.typacl IS NOT NULL
  UNION ALL
  SELECT 'row-level security policies', count(*) FROM pg_policy pol
     JOIN pg_class pc ON pc.oid = pol.polrelid JOIN pg_namespace pn ON pn.oid = pc.relnamespace
   WHERE pn.nspname IN ('public','iam_v2')
) k WHERE n > 0`

// unhandledPrivileges returns a description of any privilege kind the capture cannot reproduce, or "".
func unhandledPrivileges(ctx context.Context, db string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", pgContainer,
		"psql", "-U", pgUser, "-d", db, "-tA", "-v", "ON_ERROR_STOP=1",
		"-c", unhandledPrivilegeSQL).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", tailLine(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// capturePrivileges reads the ownership and access state of one database as replayable statements.
//
// Ordered by (phase, statement text) so two captures of the same state are byte-identical and can be compared
// directly -- which is how the restore proves it reinstated what it took away.
func capturePrivileges(ctx context.Context, db string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", pgContainer,
		"psql", "-U", pgUser, "-d", db, "-tA", "-F", "\x1f", "-v", "ON_ERROR_STOP=1",
		"-c", privilegeCaptureSQL).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", tailLine(string(out)))
	}
	var stmts []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Each row is "<phase>\x1f<statement>". The phase only exists to order the output.
		if i := strings.IndexByte(line, '\x1f'); i >= 0 {
			line = line[i+1:]
		}
		if line != "" {
			stmts = append(stmts, line)
		}
	}
	if len(stmts) == 0 {
		return nil, fmt.Errorf("the database reported no ownership or access rules at all")
	}
	return stmts, nil
}

// applyPrivileges replays captured statements onto a database, stopping at the first one that fails.
//
// ON_ERROR_STOP is not optional here. A partial privilege replay is the failure mode this whole file exists
// to prevent: it would leave an appliance that starts, serves some requests, and fails on whichever table
// the operator reaches last.
func applyPrivileges(ctx context.Context, db string, stmts []string) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", pgContainer,
		"psql", "-U", pgUser, "-d", db, "-v", "ON_ERROR_STOP=1", "-q")
	cmd.Stdin = strings.NewReader(strings.Join(stmts, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", firstProblem(string(out)))
	}
	return nil
}

// privilegesMatch reports the first difference between two captures, or "" if they are identical.
func privilegesMatch(want, got []string) string {
	if len(want) != len(got) {
		return fmt.Sprintf("the restored database has %d ownership/access rules where the live one had %d",
			len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			return fmt.Sprintf("the restored database differs at rule %d: expected %q, found %q",
				i+1, want[i], got[i])
		}
	}
	return ""
}
