package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RESTORE STEP 3d: RETENTION IS APPLIED TO THE STAGING DATA, TO THE END, BEFORE IT CAN BE LIVE.
//
// A backup is older than the hotel's retention periods say it may be. Two things make restoring it unsafe unless both are dealt with:
//
//  1. The backup carries every record the retention run had anonymised since it was taken. Anonymising them again used to happen
//     AFTER the swap, so a retention run that failed, timed out or was interrupted left the restored data serving expired personal data
//     (the restore said so in a step, and went on). Now the run happens on the staging database, while the live database is untouched,
//     and a run that does not reach the end stops the restore with NOTHING replaced.
//  2. The retention periods themselves live in the database, so restoring an old backup brings back the OLD periods. A hotel that had
//     shortened a period since would have its data judged by the longer one and kept past the period it now promises. The periods in force
//     on THIS appliance are carried into the staging data first, exactly like the backup key holders and the secondary storage setting:
//     they belong to the appliance, not to the backup. The settings' change history is carried with them.
//
// Nothing here changes what retention does or when; only where the restore runs it and what it refuses to do without it.

// retentionGuestFns are the guest-data retention functions, run in batches until a whole pass changes nothing.
var retentionGuestFns = []string{"iam_v2.run_data_retention", "public.run_data_retention_public", "iam_v2.run_staff_note_retention"}

// restoreRetentionDeadline bounds the whole step. The appliance is stopped while it runs, so it is generous (a backlog of a year of stays
// is minutes) but not endless.
var restoreRetentionDeadline = 60 * time.Minute

// The restore as a whole has one deadline that CONTAINS the retention budget: the steps before it (safety backup, load, key holders,
// ledger) keep their 45 minutes, retention keeps its own, and the steps after it (quiesce, swap, privileges, integrity, services) keep 30
// more -- a deadline that ran out between the renames would be worse than no deadline.
const (
	restoreBeforeRetentionBudget = 45 * time.Minute
	restoreAfterRetentionBudget  = 30 * time.Minute
)

func restoreOverallDeadline() time.Duration {
	return restoreBeforeRetentionBudget + restoreRetentionDeadline + restoreAfterRetentionBudget
}

// retentionContext gives the step its own budget out of what the restore has left, and never so much that the steps after it have
// less than restoreAfterRetentionBudget. enough=false means there is not enough time left to run it safely.
func retentionContext(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	budget := restoreRetentionDeadline
	if dl, has := ctx.Deadline(); has {
		if left := time.Until(dl) - restoreAfterRetentionBudget; left < budget {
			budget = left
		}
	}
	if budget < time.Minute {
		return ctx, func() {}, false
	}
	c, cancel := context.WithTimeout(ctx, budget)
	return c, cancel, true
}

var uuidLiteral = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// retentionPolicyTables are the tables that hold the periods and the history of their changes. rows says how to select this site's rows.
var retentionPolicyTables = []struct {
	table, key, where string // where is a format with %[1]s tenant and %[2]s site; empty = every row
	history           string // the append-only history table and its key
	historyKey        string
	historyWhere      string
}{
	{"iam_v2.site_data_retention", "tenant_id, site_id", "tenant_id = '%[1]s' AND site_id = '%[2]s'",
		"iam_v2.data_retention_changes", "id", "tenant_id = '%[1]s' AND site_id = '%[2]s'"},
	{"public.operator_account_retention", "id", "", "public.operator_account_retention_changes", "id", ""},
}

// retentionPassFn is one batch of one function against a database; a var so a test can stand in for it.
var retentionPassFn = func(ctx context.Context, db, tenant, site, fn string) (int, error) {
	out, err := execPsql(ctx, db, fmt.Sprintf(
		`SELECT o_category || '|' || o_anonymised FROM %s('%s'::uuid,'%s'::uuid,%d)`, fn, tenant, site, dataRetentionBatch))
	if err != nil {
		return 0, err
	}
	return sumPassOutput(out)
}

// retentionStaffPassFn is one pass of the staff retention (accounts, audit addresses, assisted requests).
var retentionStaffPassFn = func(ctx context.Context, db, tenant, site string) (int, error) {
	out, err := execPsql(ctx, db, fmt.Sprintf(
		`SELECT COALESCE(sum(v::int),0) FROM jsonb_each_text(public.run_staff_retention('%s'::uuid,'%s'::uuid)) AS e(k,v)`, tenant, site))
	if err != nil {
		return 0, err
	}
	n, perr := strconv.Atoi(strings.TrimSpace(out))
	if perr != nil {
		return 0, fmt.Errorf("the staff retention run answered %q", strings.TrimSpace(out))
	}
	return n, nil
}

func sumPassOutput(out string) (int, error) {
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.LastIndex(line, "|")
		if i < 0 {
			return 0, fmt.Errorf("a retention run answered %q", line)
		}
		n, err := strconv.Atoi(strings.TrimSpace(line[i+1:]))
		if err != nil {
			return 0, fmt.Errorf("a retention run answered %q", line)
		}
		total += n
	}
	return total, nil
}

// runRetentionToEnd runs every function in turn until one whole pass over all of them changes nothing. UNLIKE THE HOURLY RUN, ANY FAILURE
// ENDS IT: a function that cannot run is a backlog that was not worked off, and the restore must not go on without it. Only ctx ends it
// otherwise. It returns the number of records anonymised and the passes it took.
func runRetentionToEnd(ctx context.Context, pass func(ctx context.Context, fn string) (int, error), staff func(ctx context.Context) (int, error)) (records, passes int, err error) {
	for {
		if cerr := ctx.Err(); cerr != nil {
			return records, passes, cerr
		}
		changed := 0
		for _, fn := range retentionGuestFns {
			n, perr := pass(ctx, fn)
			if perr != nil {
				return records, passes, fmt.Errorf("%s: %w", fn, perr)
			}
			changed += n
		}
		n, serr := staff(ctx)
		if serr != nil {
			return records, passes, fmt.Errorf("public.run_staff_retention: %w", serr)
		}
		changed += n
		passes++
		records += changed
		if changed == 0 {
			return records, passes, nil
		}
	}
}

// retentionRulesFingerprint identifies the retention RULES a database holds: the definition of every function that decides what is
// anonymised and how (the retention runs, the anonymisation they call, the scrubbing, the assisted-request expiry) and the shape of the
// tables holding the periods. A backup whose schema is older still HAS functions of those names, but older implementations of them --
// 0143's run_data_retention, for one, never anonymises a device with no first/last seen, which 0144 and 0147 fixed -- so restoring it
// would serve identifiers the rules in force would have removed.
//
// The comparison is against the LIVE database, which is what is in force here, and not against a schema version: the migration ledger is
// empty on an appliance built from the factory-clean baseline, so it cannot be the authority, and a definition-level comparison needs no
// number to be kept up to date when the rules change.
func retentionRulesFingerprint(ctx context.Context, db string) (string, error) {
	out, err := execPsql(ctx, db, `SELECT md5(
  coalesce((SELECT string_agg(pg_get_functiondef(p.oid), E'\n' ORDER BY n.nspname, p.proname, pg_get_function_identity_arguments(p.oid))
              FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
             WHERE n.nspname IN ('iam_v2','public') AND p.prokind = 'f'
               AND p.proname ~ '(retention|anonymis|^scrub_|expire_assisted)'), '')
  || '#' ||
  coalesce((SELECT string_agg(table_schema || '.' || table_name || '.' || column_name || ':' || data_type || ':' || is_nullable || ':' || coalesce(column_default, ''),
                              E'\n' ORDER BY table_schema, table_name, ordinal_position)
              FROM information_schema.columns
             WHERE (table_schema, table_name) IN (('iam_v2','site_data_retention'), ('public','operator_account_retention'), ('iam_v2','retention_floor'))), ''))`)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// retentionFunctionsPresent says whether the staging database can run every retention function. A backup older than the retention
// programme cannot: restoring it would put back data no retention was ever applied to, so it is refused (the schema must be brought forward
// first, which is a deployment, not a restore from the screen).
func retentionFunctionsPresent(ctx context.Context, db string) (bool, error) {
	out, err := execPsql(ctx, db, `SELECT to_regprocedure('iam_v2.run_data_retention(uuid,uuid,integer)') IS NOT NULL
 AND to_regprocedure('public.run_data_retention_public(uuid,uuid,integer)') IS NOT NULL
 AND to_regprocedure('iam_v2.run_staff_note_retention(uuid,uuid,integer)') IS NOT NULL
 AND to_regprocedure('public.run_staff_retention(uuid,uuid)') IS NOT NULL
 AND to_regclass('iam_v2.site_data_retention') IS NOT NULL
 AND to_regclass('public.operator_account_retention') IS NOT NULL`)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "t", nil
}

// carryRetentionPolicy copies the periods in force and their history from the live database into staging. The live row wins outright: if
// the live site has no row (defaults apply there) the staging row is removed so the same defaults apply; otherwise it replaces the
// backup's. History rows the backup does not have are added (the history is append-only; none is changed or removed).
func carryRetentionPolicy(ctx context.Context, live, staging, tenant, site string) (string, error) {
	var carried []string
	for _, t := range retentionPolicyTables {
		where := ""
		histWhere := ""
		if t.where != "" {
			where = " WHERE " + fmt.Sprintf(t.where, tenant, site)
		}
		if t.historyWhere != "" {
			histWhere = " WHERE " + fmt.Sprintf(t.historyWhere, tenant, site)
		}
		rows, err := execPsql(ctx, live, `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb)::text FROM `+t.table+` x`+where)
		if err != nil {
			return "", fmt.Errorf("the retention periods could not be read from the live database: %w", err)
		}
		hist, err := execPsql(ctx, live, `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb)::text FROM `+t.history+` x`+histWhere)
		if err != nil {
			return "", fmt.Errorf("the history of the retention periods could not be read from the live database: %w", err)
		}
		rows, hist = strings.TrimSpace(rows), strings.TrimSpace(hist)
		if strings.Contains(rows, "$rp$") || strings.Contains(hist, "$rp$") {
			return "", fmt.Errorf("a retention setting contains an unexpected delimiter")
		}
		// One transaction: the periods and their history arrive together or not at all.
		sql := fmt.Sprintf(`BEGIN;
DELETE FROM %[1]s%[2]s;
INSERT INTO %[1]s SELECT * FROM jsonb_populate_recordset(NULL::%[1]s, $rp$%[3]s$rp$::jsonb);
INSERT INTO %[4]s SELECT * FROM jsonb_populate_recordset(NULL::%[4]s, $rp$%[5]s$rp$::jsonb) ON CONFLICT (%[6]s) DO NOTHING;
COMMIT;
SELECT count(*) FROM %[1]s%[2]s;`, t.table, where, rows, t.history, hist, t.historyKey)
		out, err := execPsql(ctx, staging, sql)
		if err != nil {
			return "", fmt.Errorf("the retention periods could not be carried into the restored data: %w", err)
		}
		carried = append(carried, t.table[strings.Index(t.table, ".")+1:]+"="+lastLine(out))
	}
	return strings.Join(carried, ", "), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// restoreRetention is the restore step. ok=false stops the restore with nothing replaced.
func (s *server) restoreRetention(ctx context.Context, staging string) (detail string, ok bool) {
	tenant, site := s.tenID, s.siteID
	if !uuidLiteral.MatchString(tenant) || !uuidLiteral.MatchString(site) {
		return "this appliance's site identity is not known, so retention could not be applied to the restored data and nothing was replaced", false
	}
	ctx, cancel, enough := retentionContext(ctx)
	defer cancel()
	if !enough {
		return "too little of the restore's time was left to apply retention safely, so nothing was replaced; the restore can be run again", false
	}
	present, err := retentionFunctionsPresent(ctx, staging)
	if err != nil {
		return "the restored data could not be examined for its retention functions, so nothing was replaced: " + err.Error(), false
	}
	if !present {
		return "this backup predates data retention, so restoring it would bring back personal data that no retention period was ever applied to. " +
			"Nothing was replaced. Restore a backup taken after the retention programme, or bring the schema forward first.", false
	}
	liveRules, err := retentionRulesFingerprint(ctx, pgDatabase)
	if err != nil {
		return "the retention rules in force could not be read, so nothing was replaced: " + err.Error(), false
	}
	backupRules, err := retentionRulesFingerprint(ctx, staging)
	if err != nil {
		return "the retention rules in the restored data could not be read, so nothing was replaced: " + err.Error(), false
	}
	if liveRules != backupRules {
		return "this backup holds older retention rules than the ones in force on this appliance (its retention functions differ), so restoring it would " +
			"leave personal data the current rules remove. Nothing was replaced. Restore a backup taken since the appliance's schema was last brought " +
			"forward; the pre-change backup of a deployment is restored by rolling that deployment back, not from the screen.", false
	}
	carried, err := carryRetentionPolicy(ctx, pgDatabase, staging, tenant, site)
	if err != nil {
		return err.Error() + ", so nothing was replaced", false
	}
	records, passes, err := runRetentionToEnd(ctx,
		func(c context.Context, fn string) (int, error) { return retentionPassFn(c, staging, tenant, site, fn) },
		func(c context.Context) (int, error) { return retentionStaffPassFn(c, staging, tenant, site) })
	if err != nil {
		return fmt.Sprintf("retention could not be completed on the restored data (%s), so nothing was replaced. The live data is exactly as it was; "+
			"the restore can be run again.", tailLine(err.Error())), false
	}
	return fmt.Sprintf("the retention periods in force on this appliance (%s) were applied to the restored data, to the end of the backlog "+
		"(%d record(s) anonymised in %d pass(es)), BEFORE it became the live data", carried, records, passes), true
}
