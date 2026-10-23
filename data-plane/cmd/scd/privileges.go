package main

// Database access carried across a restore. Backups use `pg_dump --no-owner --no-privileges`, so a restored
// database comes up superuser-owned with PUBLIC holding default EXECUTE and services fail (correctly) at
// startup. Privileges are captured from the LIVE database just before the swap and replayed afterwards,
// because the migrations must stay the single source of truth (a Go copy would drift). A restore replaces
// the hotel's data, not the appliance's wiring, so any backup restores onto a working appliance and a
// backup from another appliance picks up this one's privileges.
//
// The capture is rerun after the replay and compared; any difference rolls the restore back, since a
// half-replayed state fails unpredictably later.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// privilegeCaptureSQL emits every ownership and access decision in the two schemas as replayable statements.
// The revokes matter: PostgreSQL grants EXECUTE to PUBLIC by default and migrations revoke it, so a
// grants-only replay would leave the hole edged refuses to serve on. `grantee = 0` is PUBLIC; a NULL acl means
// the default still applies. Ownership is included because the owner is the grantor in every acl entry.
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
-- DATABASE PRIVILEGES. Cluster-level, so a dump never carries them and a freshly created database has none. Gate-P gives
-- iam_v2_owner CREATE on the database; without this the next migration after a restore could not create anything.
SELECT 3, 'GRANT ' || a.privilege_type || ' ON DATABASE ' || quote_ident(d.datname)
          || ' TO ' || CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END || ';'
FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) a
WHERE d.datname = current_database() AND a.grantee <> d.datdba
UNION ALL
SELECT 3, 'GRANT ' || a.privilege_type || ' ON FUNCTION ' || quote_ident(n.nspname) || '.' || quote_ident(p.proname)
          || '(' || pg_get_function_identity_arguments(p.oid) || ')'
          || ' TO ' || CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END || ';'
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace CROSS JOIN LATERAL aclexplode(p.proacl) a
WHERE n.nspname IN ('public','iam_v2')
  AND pg_get_userbyid(a.grantee) <> pg_get_userbyid(p.proowner)
ORDER BY 1, 2`

// unhandledPrivilegeSQL counts the privilege kinds this file cannot reproduce. A count, not best effort: a
// capture is only as complete as the catalogs it reads, so the before/after comparison cannot catch a kind
// it never saw (column privileges were once dropped this way). Anything unhandled stops the restore before
// it touches anything.
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

// capturePrivileges reads a database's ownership and access state as replayable statements, ordered by
// (phase, statement text) so equal states are byte-identical and comparable.
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
		// Each row is "<phase>\x1f<statement>"; the phase only orders the output.
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

// applyPrivileges replays captured statements, stopping at the first failure. ON_ERROR_STOP is required: a
// partial replay leaves an appliance that serves some requests and fails on the rest.
func applyPrivileges(ctx context.Context, db string, stmts []string) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", pgContainer,
		"psql", "-U", pgUser, "-d", db, "-v", "ON_ERROR_STOP=1", "-q")
	cmd.Stdin = strings.NewReader(strings.Join(stmts, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", firstProblem(string(out)))
	}
	return nil
}

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
