#!/usr/bin/env bash
# PHASE-7 — THE LEDGER BACKFILL, REVALIDATED BY COMPLETE MATERIAL EFFECT.
#
# public.schema_migrations on the appliance was missing rows for 0003, 0004 and 0006 while the schema plainly
# contained their objects. The rows were backfilled. A backfill is a CLAIM -- "this migration ran" -- and the
# only honest way to support it is to show that EVERYTHING the migration does is present, which is why this
# script exists and why it does not accept an anchor.
#
# WHAT AN ANCHOR PROVES, AND WHY IT IS NOT ENOUGH. The first pass checked one object per migration: the table
# system_network_audit exists, therefore 0003 ran. But a migration is not its first statement. A partially
# applied migration -- interrupted, or applied from an older revision of the same file -- creates the table and
# not the index, and answers an anchor exactly as a complete one does. The anchor cannot tell the two apart,
# and it is the partial case that a backfilled ledger row would permanently conceal.
#
# So every effect is derived FROM THE MIGRATION FILE ITSELF and checked individually:
#   - every table it creates, and every column of that table
#   - every index it creates, and whether a UNIQUE index is actually unique
#   - every column it adds to an existing table
#   - every constraint it drops -- which must be ABSENT, the one case where presence is the failure
#
# Deriving them from the file rather than listing them here matters: a hand-written list silently stops being
# complete the day the migration changes, and nothing would say so.
#
# IT RE-RUNS NOTHING. Re-applying an idempotent migration would make its objects appear and prove only that the
# script can create them. Every statement here is a SELECT against the catalog.
#
#   usage: phase7_ledger_material_effect.sh              # against the appliance, read-only
#          PHASE7_TARGET=local phase7_ledger_material_effect.sh
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

TARGET="${PHASE7_TARGET:-appliance}"
APPLIANCE="${PHASE7_APPLIANCE:-172.21.60.23}"
LOCAL_C="${PHASE7_CONTAINER:-phase7-recon}"
LOCAL_DB="${PHASE7_RECON_DB:-iam_recon}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

if [ "$TARGET" = "appliance" ]; then
  # ssh MUST have -n. Without it ssh reads the CALLER'S stdin, and every loop below feeds itself from a
  # here-string -- so the first query swallowed the remaining migration effects and each loop ran exactly
  # once. It cost three unchecked columns and a whole index before the claim counts gave it away. This is
  # the same defect as `docker exec -i` eating a loop's input, in a different command.
  q(){ ssh -n -o BatchMode=yes "root@$APPLIANCE" \
        "docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect_site -tAqc \"$1\"" 2>&1; }
else
  q(){ docker exec -i "$LOCAL_C" psql -U postgres -d "$LOCAL_DB" -tAqc "$1" </dev/null 2>&1; }
fi

echo "== Phase-7: the ledger backfill, revalidated by complete material effect (target=$TARGET) =="

probe="$(q "SELECT 1")"
[ "$probe" = "1" ] || { echo "cannot reach the target database: $probe"; exit 2; }

# ---- the ledger rows the backfill added ---------------------------------------------------------------------
for v in 0003 0004 0006; do
  eq "the ledger records a migration $v row" \
     "$(q "SELECT (count(*) = 1)::text FROM public.schema_migrations WHERE version LIKE '${v}%'")" "true"
done

# ---- and, for each, EVERY effect the file describes ----------------------------------------------------------
for mig in "$ROOT"/data-plane/migrations/0003_*.up.sql \
           "$ROOT"/data-plane/migrations/0004_*.up.sql \
           "$ROOT"/data-plane/migrations/0006_*.up.sql; do
  name="$(basename "$mig" .up.sql)"
  echo "  -- $name"
  claims=0

  # tables created, and every column declared inside them
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    claims=$((claims+1))
    eq "$name creates table public.$t" \
       "$(q "SELECT (to_regclass('public.$t') IS NOT NULL)::text")" "true"
    # the column list is the body of the CREATE TABLE: lines up to the closing ');'
    cols="$(awk -v tbl="$t" '
      $0 ~ "CREATE TABLE IF NOT EXISTS "tbl" \\(" {inside=1; next}
      inside && /^\);/ {inside=0}
      inside {print}' "$mig" \
      | sed 's/--.*//' | grep -oE '^[[:space:]]+[a-z_]+' | tr -d ' ' \
      | grep -vE '^(primary|unique|check|foreign|constraint|references)$' | sort -u)"
    for c in $cols; do
      claims=$((claims+1))
      eq "$name    column public.$t.$c" \
         "$(q "SELECT count(*) FROM information_schema.columns
                WHERE table_schema='public' AND table_name='$t' AND column_name='$c'")" "1"
    done
  done <<< "$(grep -oE 'CREATE TABLE IF NOT EXISTS [a-z_]+' "$mig" | awk '{print $6}')"

  # columns added to existing tables
  while IFS= read -r spec; do
    [ -n "$spec" ] || continue
    # ALTER(1) TABLE(2) <table>(3) ADD(4) COLUMN(5) IF(6) NOT(7) EXISTS(8) <column>(9)
    set -- $spec; t="$3"; c="$9"
    claims=$((claims+1))
    eq "$name adds column public.$t.$c" \
       "$(q "SELECT count(*) FROM information_schema.columns
              WHERE table_schema='public' AND table_name='$t' AND column_name='$c'")" "1"
  done <<< "$(grep -oiE 'ALTER TABLE [a-z_]+ ADD COLUMN IF NOT EXISTS +[a-z_]+' "$mig")"

  # indexes created, with uniqueness checked where the file says UNIQUE
  while IFS= read -r spec; do
    [ -n "$spec" ] || continue
    case "$spec" in
      *UNIQUE*) uniq_want=true; i="$(printf '%s' "$spec" | awk '{print $7}')" ;;
      *)        uniq_want=false; i="$(printf '%s' "$spec" | awk '{print $6}')" ;;
    esac
    claims=$((claims+1))
    eq "$name creates index $i (unique=$uniq_want)" \
       "$(q "SELECT COALESCE((SELECT i.indisunique::text FROM pg_index i
                                JOIN pg_class c ON c.oid = i.indexrelid
                                JOIN pg_namespace n ON n.oid = c.relnamespace
                               WHERE n.nspname='public' AND c.relname='$i'), 'MISSING')")" "$uniq_want"
  done <<< "$(grep -oiE 'CREATE (UNIQUE )?INDEX IF NOT EXISTS +[a-z_]+' "$mig")"

  # constraints dropped -- the one case where PRESENCE is the failure
  while IFS= read -r spec; do
    [ -n "$spec" ] || continue
    k="$(printf '%s' "$spec" | awk '{print $8}')"
    claims=$((claims+1))
    eq "$name drops constraint $k (it must be ABSENT)" \
       "$(q "SELECT (count(*) = 0)::text FROM pg_constraint WHERE conname='$k'")" "true"
  done <<< "$(grep -oiE 'ALTER TABLE [a-z_]+ DROP CONSTRAINT IF EXISTS +[a-z_]+' "$mig")"

  # A migration that yielded no checkable claim would report a silent, meaningless pass.
  if [ "$claims" -eq 0 ]; then
    no "$name yielded NO checkable claims" "the extraction found nothing -- this proves nothing"
  else
    ok "$name: $claims material effects derived from the file and each checked individually"
  fi
done

# ---- the supersession, stated rather than glossed --------------------------------------------------------------
#
# 0003 creates system_network_audit with an inline CHECK on `action`, and 0004 DROPS that same constraint to
# widen the vocabulary. So the absence of system_network_audit_action_check is not evidence against 0003 -- it
# is evidence FOR 0004, and the two are only distinguishable in order. The sibling CHECK on `target`, which
# nothing supersedes, is what shows 0003's constraint work happened at all.
eq "0003's target CHECK survives (nothing supersedes it)" \
   "$(q "SELECT (count(*) = 1)::text FROM pg_constraint WHERE conname='system_network_audit_target_check'")" "true"
eq "...while 0003's action CHECK is gone, because 0004 drops it -- supersession, not absence of 0003" \
   "$(q "SELECT (count(*) = 0)::text FROM pg_constraint WHERE conname='system_network_audit_action_check'")" "true"
# Not a restatement of the line above: that one names a constraint, this one asks whether ANY check still
# constrains the action column under any name. A re-added constraint called something else would satisfy
# the first check and defeat the widening 0004 exists to perform.
eq "...and NO check constraint of any name still restricts the action column" \
   "$(q "SELECT count(*) FROM pg_constraint c
          WHERE c.conrelid = 'public.system_network_audit'::regclass AND c.contype = 'c'
            AND pg_get_constraintdef(c.oid) LIKE '%action%'")" "0"

echo "------------------------------------------------------------"
printf 'PHASE7_LEDGER_MATERIAL_EFFECT target=%s pass=%d fail=%d\n' "$TARGET" "$pass" "$fail"
[ "$fail" -eq 0 ]
