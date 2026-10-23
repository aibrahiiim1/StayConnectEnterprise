package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Restore step 3d: retention runs on the staging data to the end, before it can be live. The backup holds expired data and the OLD
// retention periods, so this appliance's periods are carried into staging first; a run that does not finish stops the restore with nothing replaced.

var retentionGuestFns = []string{"iam_v2.run_data_retention", "public.run_data_retention_public", "iam_v2.run_staff_note_retention"}

// Bounds the whole step; the appliance is stopped meanwhile, so it is generous but not endless.
var restoreRetentionDeadline = 60 * time.Minute

// The overall deadline contains the retention budget: one that ran out between the renames would be worse than none.
const (
	restoreBeforeRetentionBudget = 45 * time.Minute
	restoreAfterRetentionBudget  = 30 * time.Minute
)

func restoreOverallDeadline() time.Duration {
	return restoreBeforeRetentionBudget + restoreRetentionDeadline + restoreAfterRetentionBudget
}

// Gives the step its own budget but leaves restoreAfterRetentionBudget for the later steps; enough=false means too little time remains.
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

var retentionPolicyTables = []struct {
	table, key, where string // where is a format with %[1]s tenant and %[2]s site; empty = every row
	history           string
	historyKey        string
	historyWhere      string
}{
	{"iam_v2.site_data_retention", "tenant_id, site_id", "tenant_id = '%[1]s' AND site_id = '%[2]s'",
		"iam_v2.data_retention_changes", "id", "tenant_id = '%[1]s' AND site_id = '%[2]s'"},
	{"public.operator_account_retention", "id", "", "public.operator_account_retention_changes", "id", ""},
}

var retentionPassFn = func(ctx context.Context, db, tenant, site, fn string) (int, error) {
	out, err := execPsql(ctx, db, fmt.Sprintf(
		`SELECT o_category || '|' || o_anonymised FROM %s('%s'::uuid,'%s'::uuid,%d)`, fn, tenant, site, dataRetentionBatch))
	if err != nil {
		return 0, err
	}
	return sumPassOutput(out)
}

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

// Unlike the hourly run, ANY failure ends it: a function that cannot run is an unworked backlog and the restore must not go on without it.
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

// Identifies the retention RULES a database holds (function definitions and the period tables' shape). Older schemas have same-named but older
// functions, so the comparison is against the LIVE database, not a schema version: the migration ledger is empty on factory-clean appliances.
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

// A backup older than the retention programme lacks the functions and is refused: restoring it would put back data no retention was applied to.
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

// The live row wins outright: if the live site has none the staging row is removed so defaults apply; missing history rows are added (append-only).
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

// ok=false stops the restore with nothing replaced.
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
