package main

// THE KEY-HOLDER REGISTER OUTLIVES A RESTORE (GDPR branch review 2026-10-20).
//
// A restore swaps the whole database, and the backup key holders (migration 0129) are rows in it. Restoring a
// backup taken before the first holder silently switched encryption off; restoring one taken before a holder
// was removed -- a departed IT manager -- made that holder active again, and new backups were encrypted to
// their key. Who may open this hotel's backups is a fact about now, not about the day the backup was taken.
//
// So before the swap the live register is copied into the staging database: holders and events the backup
// does not have are added, every holder takes its live status, and a holder the live register does not know
// is removed (recorded as removed by the restore). Nothing is deleted; both tables stay append-only.
//
// A backup that predates the register altogether (no table in staging) cannot carry it; the step says so, and
// Hotel Admin shows backups as unencrypted until a holder is set up again. Any other failure stops the restore
// before the swap, so the live database is never replaced by one that has lost its key holders.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const keyRegisterCaptureSQL = `SELECT CASE WHEN to_regclass('public.backup_key_holders') IS NULL THEN ''
  ELSE jsonb_build_object(
    'holders', (SELECT COALESCE(jsonb_agg(to_jsonb(h) ORDER BY h.created_at), '[]'::jsonb) FROM public.backup_key_holders h),
    'events',  (SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY e.at), '[]'::jsonb) FROM public.backup_key_holder_events e))::text END`

// keyRegisterCarryFn is created in the staging session only (pg_temp): the restored database may predate any
// function of ours, and nothing of this survives the session.
const keyRegisterCarryFn = `CREATE FUNCTION pg_temp.carry_key_register(snap jsonb) RETURNS text LANGUAGE plpgsql AS $carry$
DECLARE n integer;
BEGIN
  IF to_regclass('public.backup_key_holders') IS NULL THEN
    RETURN 'absent';
  END IF;
  INSERT INTO public.backup_key_holders
  SELECT r.* FROM jsonb_populate_recordset(NULL::public.backup_key_holders, snap->'holders') r
   WHERE NOT EXISTS (SELECT 1 FROM public.backup_key_holders h WHERE h.id = r.id);
  UPDATE public.backup_key_holders h
     SET status = r.status, check_code_hash = r.check_code_hash, confirmed_at = r.confirmed_at,
         removed_at = r.removed_at, removed_by = r.removed_by, remove_reason = r.remove_reason,
         last_tested_at = r.last_tested_at
    FROM jsonb_populate_recordset(NULL::public.backup_key_holders, snap->'holders') r
   WHERE h.id = r.id
     AND (h.status, h.confirmed_at, h.removed_at, h.last_tested_at)
         IS DISTINCT FROM (r.status, r.confirmed_at, r.removed_at, r.last_tested_at);
  INSERT INTO public.backup_key_holder_events
  SELECT r.* FROM jsonb_populate_recordset(NULL::public.backup_key_holder_events, snap->'events') r
   WHERE NOT EXISTS (SELECT 1 FROM public.backup_key_holder_events e WHERE e.id = r.id);
  WITH gone AS (
    UPDATE public.backup_key_holders h
       SET status = 'REMOVED', removed_at = now(), removed_by = 'restore',
           remove_reason = 'not in the live register when this backup was restored'
     WHERE h.status <> 'REMOVED'
       AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(snap->'holders') x WHERE (x->>'id')::uuid = h.id)
    RETURNING h.id, h.tenant_id, h.site_id)
  INSERT INTO public.backup_key_holder_events (tenant_id, site_id, holder_id, event, actor, reason)
  SELECT tenant_id, site_id, id, 'removed', 'restore', 'not in the live register when this backup was restored' FROM gone;
  SELECT count(*) INTO n FROM public.backup_key_holders WHERE status = 'ACTIVE';
  RETURN 'carried:' || n;
END $carry$;`

// keyRegisterCarrySQL runs it through psql, which passes the snapshot as a quoted variable.
const keyRegisterCarrySQL = "BEGIN;\n" + keyRegisterCarryFn + "\nSELECT pg_temp.carry_key_register(:'snap'::jsonb);\nCOMMIT;\n"

// captureKeyRegister reads the live register as JSON; "" when the live database has none (before 0129).
func captureKeyRegister(ctx context.Context, db string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", pgContainer,
		"psql", "-U", pgUser, "-d", db, "-tA", "-v", "ON_ERROR_STOP=1", "-c", keyRegisterCaptureSQL).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", tailLine(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// carryKeyRegister writes the live register into the staging database. It returns how many holders are
// active there afterwards, or absent=true when the backup predates the register.
func carryKeyRegister(ctx context.Context, staging, snapshot string) (active string, absent bool, err error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", pgContainer,
		"psql", "-U", pgUser, "-d", staging, "-tA", "-v", "ON_ERROR_STOP=1", "-v", "snap="+snapshot)
	cmd.Stdin = strings.NewReader(keyRegisterCarrySQL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("%s", tailLine(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "absent":
			return "", true, nil
		case strings.HasPrefix(line, "carried:"):
			return strings.TrimPrefix(line, "carried:"), false, nil
		}
	}
	return "", false, fmt.Errorf("the key register step reported nothing")
}

// keyRegisterStep is restore step 3b: the live register into staging, before the swap. ok=false stops the
// restore with detail; ok=true reports what happened.
func keyRegisterStep(ctx context.Context, live, staging string) (ok bool, detail string) {
	snap, err := captureKeyRegister(ctx, live)
	if err != nil {
		return false, "the backup key holders could not be read from the live database, so nothing was replaced: " + err.Error()
	}
	if snap == "" {
		return true, "this appliance had no backup key holders to carry over"
	}
	active, absent, err := carryKeyRegister(ctx, staging, snap)
	if err != nil {
		return false, "the backup key holders could not be carried into the restored database, so nothing was replaced: " + err.Error()
	}
	if absent {
		return true, "this backup predates backup encryption: set up a key holder again after the restore; until then backups are not encrypted"
	}
	return true, "the current backup key holders were kept (" + active + " active), whatever the backup held"
}
