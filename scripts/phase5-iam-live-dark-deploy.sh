#!/usr/bin/env bash
# PHASE-5 (IAM) CONTROLLED LIVE-DARK DEPLOYMENT — DEVELOPMENT APPLIANCE ONLY.
#
# Runs ON the appliance. It applies migrations 0027-0029 to the site database and proves the result is DARK:
# every Phase-5 flag absent, every Phase-5 route 404 on the running services, zero rows in every Phase-5
# table, and the legacy public schema untouched.
#
# WHAT IT DOES NOT DO, and cannot: it enables no feature flag, restarts no service into a new configuration,
# touches no Production database, contacts no PMS or payment provider, and moves no money. The only writes
# are the additive migrations and their ledger rows.
#
# FAIL CLOSED: any failed precondition, backup, migration, darkness or health check aborts before the next
# step. Every step prints what it OBSERVED, not what it expected.
#
# NOTE ON THE NAME: this appliance already carries scripts/phase5-*.sh from the earlier enrollment/HA work,
# which is a different "phase 5" entirely. This one is named phase5-iam-* so the two cannot be confused by
# anyone reading a directory listing at 3am.
set -uo pipefail
CT=stayconnect-pg
DB=stayconnect_site
PGU=stayconnect
ROOT="${PHASE5_ROOT:-/opt/stayconnect}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="${PHASE5_BACKUP_DIR:-/opt/stayconnect/backups/phase5-iam-$STAMP}"
MIGRATIONS="0027_phase5_poststay_and_transfer 0028_phase5_poststay_throttle_method 0029_phase5_reveal_is_at_mint"

P(){ docker exec -i "$CT" psql -U "$PGU" -d "$DB" -v ON_ERROR_STOP=1 -qAt -c "$1"; }
say(){ echo "[$(date -u +%H:%M:%S)] $*"; }
die(){ echo "ABORT: $*" >&2; exit 1; }

read -r -d '' STRUCT_SQL <<'EOSQL' || true
SELECT md5(string_agg(line, E'\n' ORDER BY line)) FROM (
    SELECT format('COL %s %s %s %s', table_name, column_name, data_type, is_nullable) AS line
      FROM information_schema.columns WHERE table_schema='iam_v2'
    UNION ALL SELECT format('CON %s %s', conrelid::regclass::text, pg_get_constraintdef(oid))
      FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace
    UNION ALL SELECT format('IDX %s', indexdef) FROM pg_indexes WHERE schemaname='iam_v2'
    UNION ALL SELECT format('TRG %s %s', tgrelid::regclass::text, tgname)
      FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
     WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
    UNION ALL SELECT format('FUN %s(%s)', pr.proname, pg_get_function_arguments(pr.oid))
      FROM pg_proc pr JOIN pg_namespace n ON n.oid=pr.pronamespace WHERE n.nspname='iam_v2') x;
EOSQL

VERIFY_ONLY="${PHASE5_VERIFY_ONLY:-0}"

echo "===== PHASE-5 IAM LIVE-DARK DEPLOYMENT (development appliance) $STAMP ====="
[ "$VERIFY_ONLY" = "1" ] && echo "     (VERIFY ONLY: no backup, no migration, no write of any kind)"

# ---------------------------------------------------------------- preconditions
if [ "$VERIFY_ONLY" = "1" ]; then
  say "SKIPPING preconditions, backup and migration (verify-only)"
else
say "PRECONDITIONS"
[ -d "$ROOT/data-plane/migrations" ] || die "no migrations directory under $ROOT"
docker exec "$CT" pg_isready -U "$PGU" -d "$DB" >/dev/null 2>&1 || die "the site database is not ready"
say "  host=$(hostname) database=$DB"
[ "$DB" = "stayconnect_site" ] || die "refusing to run against anything but the site database"

BEFORE_TABLES="$(P "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE'")"
BEFORE_PUBLIC="$(P "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")"
BEFORE_STRUCT="$(P "$STRUCT_SQL")"
say "  before: iam_v2 base tables=$BEFORE_TABLES  public tables=$BEFORE_PUBLIC"
say "  before: iam_v2 structural fingerprint=$BEFORE_STRUCT"

for m in $MIGRATIONS; do
  n="$(P "SELECT count(*) FROM public.schema_migrations WHERE version='$m'")"
  [ "$n" = "0" ] || die "$m is already applied; this script deploys, it does not re-apply"
done
say "  none of 0027/0028/0029 is applied yet"

# ---------------------------------------------------------------- backup
say "BACKUP"
mkdir -p "$BACKUP_DIR" || die "cannot create $BACKUP_DIR"
docker exec "$CT" pg_dump -U "$PGU" -d "$DB" -Fc > "$BACKUP_DIR/site.dump" 2>"$BACKUP_DIR/pg_dump.err" \
  || { cat "$BACKUP_DIR/pg_dump.err" >&2; die "pg_dump failed"; }
SZ="$(stat -c %s "$BACKUP_DIR/site.dump" 2>/dev/null || echo 0)"
[ "$SZ" -gt 100000 ] || die "the backup is implausibly small ($SZ bytes)"
( cd "$BACKUP_DIR" && sha256sum site.dump > SHA256SUMS )
say "  backup: $BACKUP_DIR/site.dump ($SZ bytes)"
say "  sha256: $(awk '{print $1}' "$BACKUP_DIR/SHA256SUMS")"

# ---------------------------------------------------------------- migrate
say "MIGRATE"
for m in $MIGRATIONS; do
  if ! docker exec -i "$CT" psql -U "$PGU" -d "$DB" -v ON_ERROR_STOP=1 -q \
        < "$ROOT/data-plane/migrations/$m.up.sql" >/dev/null 2>"$BACKUP_DIR/$m.err"; then
    tail -5 "$BACKUP_DIR/$m.err" >&2
    die "$m failed to apply (each migration is one transaction, so it applied nothing)"
  fi
  applied="$(P "SELECT count(*) FROM public.schema_migrations WHERE version='$m'")"
  [ "$applied" = "1" ] || die "$m is recorded $applied times, expected exactly once"
  say "  applied and recorded once: $m"
done

AFTER_TABLES="$(P "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE'")"
AFTER_PUBLIC="$(P "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")"
AFTER_STRUCT="$(P "$STRUCT_SQL")"
say "  after: iam_v2 base tables=$AFTER_TABLES  public tables=$AFTER_PUBLIC"
say "  after: iam_v2 structural fingerprint=$AFTER_STRUCT"
[ "$AFTER_TABLES" = "$BEFORE_TABLES" ] || die "the iam_v2 table count changed ($BEFORE_TABLES -> $AFTER_TABLES); Phase 5 creates no tables"
[ "$AFTER_PUBLIC" = "$BEFORE_PUBLIC" ] || die "the public schema gained or lost a table"
[ "$AFTER_STRUCT" != "$BEFORE_STRUCT" ] || die "the structural fingerprint did not change; the migrations did nothing"

fi
# ---------------------------------------------------------------- darkness
say "DARKNESS"
ROWS="$(P "SELECT (SELECT count(*) FROM iam_v2.post_stay_profiles)+(SELECT count(*) FROM iam_v2.entitlement_transfers)+(SELECT count(*) FROM iam_v2.stay_links)")"
[ "$ROWS" = "0" ] || die "the Phase-5 tables hold $ROWS row(s); a dark deployment creates none"
say "  every Phase-5 table holds 0 rows"

# The exclusion is the TABLE'S OWNER, resolved from the catalog -- not current_user. They are the same role
# on a scratch database and DIFFERENT on this appliance, where Gate P gives iam_v2 its own owner: the owner's
# implicit rights then appear as 21 "non-owner" grants and the check condemns a correct deployment. What the
# darkness claim actually means is that no role BESIDES the owner can touch these tables.
GRANTS="$(P "SELECT count(*) FROM information_schema.role_table_grants g JOIN pg_class c ON c.relname=g.table_name JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname=g.table_schema WHERE g.table_schema='iam_v2' AND g.table_name IN ('post_stay_profiles','entitlement_transfers','stay_links') AND g.grantee <> pg_get_userbyid(c.relowner) AND g.grantee <> 'PUBLIC'")"
[ "$GRANTS" = "0" ] || die "$GRANTS grant(s) to a role other than the table owner exist on Phase-5 tables"
say "  no role besides the schema owner holds any privilege on a Phase-5 table"

OBJ="$(P "SELECT (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname LIKE 'p5!_%' ESCAPE '!') + (SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal AND t.tgname LIKE 'p5!_%' ESCAPE '!')")"
say "  Phase-5 database objects present: $OBJ (7 functions + 6 triggers = 13, plus the 0029 replacement in place)"
[ "$OBJ" = "13" ] || die "expected 13 Phase-5 objects, found $OBJ"

FLAGS="$(grep -rhoE 'STAYCONNECT_PHASE5_[A-Z_]+=[^[:space:]]*' /etc/stayconnect/ /etc/systemd/system/stayconnect-*.service 2>/dev/null | sort -u || true)"
[ -z "$FLAGS" ] || die "a Phase-5 flag is present in the deployed configuration: $FLAGS"
say "  no Phase-5 flag appears in any env file or unit"

for svc in stayconnect-scd stayconnect-edged stayconnect-netd stayconnect-acctd; do
  state="$(systemctl is-active "$svc" 2>/dev/null || true)"
  say "  $svc = $state"
  [ "$state" = "active" ] || die "$svc is $state"
done

# ---------------------------------------------------------------- pmsd, whose healthy state is NOT running
# pmsd is the one StayConnect daemon that is HEALTHY WHEN DEAD. Its established Phase-3 DARK contract is: no
# Phase-3 flag is set, so LoadPMSConfigFromEnv resolves every flag OFF, pmsd.Run returns immediately without
# loading an assignment, building a database pool, decrypting a secret, starting a worker or opening a PMS
# socket -- it says so and exits 0. `Restart=on-failure` (not always) means that clean exit does not restart.
#
# So this must NOT be added to the loop above. `systemctl is-active` reads "inactive" on a perfectly correct
# appliance, and a criterion that demands "active" has exactly two outcomes: it fails a correct deployment,
# or somebody makes it green by starting a LIVE PMS worker -- which is real PMS traffic that no Phase-5
# authorization covers. The criterion is therefore the DARK contract itself, measured.
# PMSD_UNIT exists so this criterion can be shown to FAIL. A check that has only ever been watched passing
# has not been shown to check anything, and every arm below is an equality against a value this appliance
# happens to hold. Pointing it at a unit that violates one arm is the only honest way to demonstrate the arm
# bites, and it defaults to the real unit so a deployment run is unaffected.
PMSD_UNIT="${PHASE5_PMSD_UNIT:-stayconnect-pmsd}"
say "PMSD DARK CONTRACT ($PMSD_UNIT)"
pm(){ systemctl show "$PMSD_UNIT" -p "$1" --value 2>/dev/null || true; }

# 1. Installed and would start at boot. "Dark" must mean a deliberately quiescent daemon, not a missing or
#    masked unit -- those are indistinguishable from an appliance that lost its connector entirely.
PM_LOAD="$(pm LoadState)"; PM_FILE="$(pm UnitFileState)"
say "  unit: LoadState=$PM_LOAD UnitFileState=$PM_FILE"
[ "$PM_LOAD" = "loaded" ] || die "$PMSD_UNIT is $PM_LOAD; a dark daemon is installed, not absent"
[ "$PM_FILE" = "enabled" ] || die "$PMSD_UNIT is $PM_FILE; the dark contract is enabled-and-quiescent"

# 2. Clean disabled behaviour: inactive/dead by way of a SUCCESSFUL exit. A failed unit is also "not
#    running", and the difference between "it decided it had nothing to do" and "it crashed" is the whole
#    claim being made here.
PM_ACT="$(pm ActiveState)"; PM_SUB="$(pm SubState)"; PM_RES="$(pm Result)"; PM_EXIT="$(pm ExecMainStatus)"
say "  state: $PM_ACT ($PM_SUB) Result=$PM_RES ExecMainStatus=$PM_EXIT"
[ "$PM_ACT" = "inactive" ] || die "$PMSD_UNIT is $PM_ACT; the dark contract is a clean exit, and an active pmsd means a live connector"
[ "$PM_SUB" = "dead" ]     || die "$PMSD_UNIT substate is $PM_SUB, expected dead"
[ "$PM_RES" = "success" ]  || die "$PMSD_UNIT Result=$PM_RES; it did not exit cleanly, it failed"
[ "$PM_EXIT" = "0" ]       || die "$PMSD_UNIT exited $PM_EXIT; a flags-OFF exit is 0"

# 3. No restart storm. Restart=on-failure plus a clean exit should mean the unit starts once per boot and
#    stays down. A loop would burn CPU, spam the journal and -- once the connector is eventually enabled --
#    hammer a real PMS.
PM_NR="$(pm NRestarts)"
STARTS="$(journalctl -u "$PMSD_UNIT" -b --no-pager 2>/dev/null | grep -c 'Started StayConnect PMS' || true)"
STORM="$(journalctl -u "$PMSD_UNIT" -b --no-pager 2>/dev/null | grep -Eic 'scheduled restart|start request repeated too quickly|failed with result' || true)"
say "  restarts: NRestarts=$PM_NR starts-this-boot=$STARTS storm-indicators=$STORM"
[ "${PM_NR:-0}" = "0" ] || die "$PMSD_UNIT restarted $PM_NR time(s); a clean flags-OFF exit must not restart"
[ "${STARTS:-0}" = "1" ] || die "$PMSD_UNIT started ${STARTS:-0} time(s) this boot; the dark contract is exactly one start"
[ "${STORM:-0}" = "0" ] || die "$PMSD_UNIT shows $STORM restart-storm indicator(s) in this boot's journal"

# 4. The daemon's OWN statement of what it did not do, read from its journal rather than asserted here. This
#    is the difference between "the process is gone" and "the process reached its dark path and took it".
if journalctl -u "$PMSD_UNIT" -b --no-pager 2>/dev/null \
     | grep -q 'no assignment, DB, secret, worker or PMS socket'; then
  say "  journal: pmsd logged the dark path — no assignment, DB, secret, worker or PMS socket"
else
  die "$PMSD_UNIT: the pmsd dark statement is absent from this boot's journal; it did not take the flags-OFF path"
fi
journalctl -u "$PMSD_UNIT" -b --no-pager 2>/dev/null | grep -q 'stopped cleanly' \
  || die "$PMSD_UNIT never logged a clean stop this boot"

# 5. No PMS socket and no PMS traffic. Measured as absence of the process and of any socket owned by it,
#    rather than inferred from the flags.
PM_PROCS="$(pgrep -c -u stayconnect-pmsd 2>/dev/null || true)"
PM_SOCKS="$(ss -tanp 2>/dev/null | grep -c 'pmsd' || true)"
say "  footprint: processes=${PM_PROCS:-0} sockets=${PM_SOCKS:-0}"
[ "${PM_PROCS:-0}" = "0" ] || die "${PM_PROCS} process(es) are running as the pmsd user; the dark contract runs none"
[ "${PM_SOCKS:-0}" = "0" ] || die "${PM_SOCKS} socket(s) belong to pmsd; the dark contract opens none"

# 6. No database activity. pmsd builds no pool at all when its flags are off, so no backend in the site
#    database may be attributable to it.
PM_DB="$(P "SELECT count(*) FROM pg_stat_activity WHERE usename ILIKE '%pms%' OR application_name ILIKE '%pms%'")"
say "  database backends attributable to pmsd: $PM_DB"
[ "$PM_DB" = "0" ] || die "$PM_DB database backend(s) are attributable to pmsd; the dark contract opens no pool"

# The routes must be ABSENT, not present-and-refusing. 404 is the dark posture; anything else means the
# surface exists on a deployment that never enabled it.
for probe in /v1/phase5/poststay/issue /v1/phase5/auth/post-stay-pin /v1/phase5/poststay/convert; do
  code="$(curl -s -o /dev/null -w '%{http_code}' --unix-socket /run/stayconnect/scd.sock -X POST \
          -H 'Content-Type: application/json' -d '{}' "http://unix$probe" 2>/dev/null || echo 000)"
  say "  scd $probe -> $code"
  [ "$code" = "404" ] || die "scd $probe answered $code; a dark surface answers 404"
done

say "ALL CHECKS PASSED — Phase 5 is deployed DARK"
say "backup: $BACKUP_DIR"
