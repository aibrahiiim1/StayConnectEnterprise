package main

// A restore checks the backup's schema against the software that is installed (audit P9 M3). The live database IS the schema this
// appliance's software expects (a deployment brings it forward before the software starts), so the backup, loaded into the staging
// database, is compared with it object for object: tables with their columns, types and nullability, and functions with their
// argument lists. The financial guards, the posting authoriser and the sign-in roster rules live in migrations, so a backup from
// before a migration restored under newer code is newer code over older rules, and one from a newer level is the reverse.
// Either way nothing is replaced and the restore says which it is. This is the same rule the retention check already applies to the
// retention functions (restore_retention.go), made general.

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// schemaInventory lists the database's objects as one comparable line each; tests replace it.
var schemaInventory = func(ctx context.Context, db string) ([]string, error) {
	out, err := execPsql(ctx, db, `
SELECT 'column ' || c.table_schema || '.' || c.table_name || '.' || c.column_name || ' ' || c.data_type || ' ' || c.is_nullable
  FROM information_schema.columns c
  JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
 WHERE c.table_schema IN ('public', 'iam_v2')
UNION ALL
SELECT 'function ' || n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname IN ('public', 'iam_v2') AND p.prokind IN ('f', 'p')
   AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// schemaVerdict says how a backup's schema stands against the installed software's.
type schemaVerdict struct {
	Missing []string // in the installed schema, not in the backup: the backup is OLDER
	Extra   []string // in the backup, not in the installed schema: the backup is NEWER (or from elsewhere)
}

func (v schemaVerdict) Same() bool { return len(v.Missing) == 0 && len(v.Extra) == 0 }

func compareSchemas(live, backup []string) schemaVerdict {
	l, b := map[string]bool{}, map[string]bool{}
	for _, x := range live {
		l[x] = true
	}
	for _, x := range backup {
		b[x] = true
	}
	var v schemaVerdict
	for x := range l {
		if !b[x] {
			v.Missing = append(v.Missing, x)
		}
	}
	for x := range b {
		if !l[x] {
			v.Extra = append(v.Extra, x)
		}
	}
	sort.Strings(v.Missing)
	sort.Strings(v.Extra)
	return v
}

// examples names a few objects (never data: only table, column and function names) so the operator and support can see what differs.
func examples(items []string) string {
	const max = 4
	var shown []string
	for i, x := range items {
		if i == max {
			break
		}
		shown = append(shown, x)
	}
	s := strings.Join(shown, "; ")
	if len(items) > max {
		s += fmt.Sprintf("; and %d more", len(items)-max)
	}
	return s
}

// restoreSchemaCheck is step 3a: ok=false stops the restore with nothing replaced.
func (s *server) restoreSchemaCheck(ctx context.Context, staging string) (detail string, ok bool) {
	live, err := schemaInventory(ctx, pgDatabase)
	if err != nil {
		return "the schema this appliance's software expects could not be read, so nothing was replaced: " + err.Error(), false
	}
	if len(live) == 0 {
		return "the schema this appliance's software expects could not be read (it is empty), so nothing was replaced", false
	}
	backup, err := schemaInventory(ctx, staging)
	if err != nil {
		return "the schema inside the backup could not be read, so nothing was replaced: " + err.Error(), false
	}
	v := compareSchemas(live, backup)
	switch {
	case v.Same():
		return fmt.Sprintf("the backup's schema (%d objects) is the one this appliance's software expects", len(backup)), true
	case len(v.Extra) == 0:
		return fmt.Sprintf("this backup was taken by older software than the one installed: it lacks %d object(s) the installed software needs (%s). "+
			"Restoring it would run the current software over an older schema, with the financial, posting and roster rules of the older version. "+
			"Nothing was replaced. Restore a backup taken since the appliance was last brought forward; the pre-change backup of a deployment is "+
			"restored by rolling that deployment back, not from the screen.", len(v.Missing), examples(v.Missing)), false
	case len(v.Missing) == 0:
		return fmt.Sprintf("this backup was taken by newer software than the one installed: it holds %d object(s) the installed software does not know (%s). "+
			"Nothing was replaced. Install the matching software version first, then restore it.", len(v.Extra), examples(v.Extra)), false
	default:
		return fmt.Sprintf("this backup's schema does not match the installed software in either direction (it lacks %d and adds %d object(s); for example %s). "+
			"It was not made by this version of the software, so nothing was replaced.", len(v.Missing), len(v.Extra), examples(append(append([]string{}, v.Missing...), v.Extra...))), false
	}
}
