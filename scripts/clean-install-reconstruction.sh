#!/usr/bin/env bash
# FACTORY-CLEAN INSTALL RECONSTRUCTION AND SEMANTIC VERIFICATION — repository sources only.
#
# WHY THIS EXISTS
# ---------------
# Production is a FRESH APPLIANCE DEPLOYMENT, so one question is load-bearing: can a blank PostgreSQL cluster
# be built into the complete current schema from the authoritative repository ALONE -- no database dump, no
# /etc/stayconnect, no service state, no identity copied from any existing machine?
#
# It answers that by building the schema in a disposable container that has never seen an appliance, and then
# emitting a SEMANTIC catalog: columns, primary/unique/foreign keys, checks, indexes, triggers, functions,
# ownership, memberships, table grants and function privileges. Table counts are not acceptance evidence --
# two schemas can agree on the number of tables and disagree about everything that matters.
#
# It seeds no tenant, site, operator, plan, account or voucher. Those arrive through enrollment, claim,
# signed assignment, licensing and Hotel-Admin configuration, and an installer that quietly seeds them is how
# a "clean" install ends up carrying somebody's test identities into production.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIG="$ROOT/data-plane/migrations"
BASE="$MIG/iam_base"
GATEP="$ROOT/deploy/gatep"
# CLEANROOM_NAME lets a caller (the baseline generator) name the container it will dump from.
C="${CLEANROOM_NAME:-sc-cleanroom-$$}"
OUT="${CLEANROOM_OUT:-$ROOT/.cleanroom}"
REF="${CLEANROOM_REFERENCE:-}"
mkdir -p "$OUT"

# CLEANROOM_KEEP=1 leaves the container running so the built schema can be interrogated afterwards. The
# container name is printed at the end; it is the caller's job to remove it.
cleanup() {
  if [ "${CLEANROOM_KEEP:-0}" = "1" ]; then
    printf '  cleanroom container left running for inspection: %s
' "$C"
    return
  fi
  docker rm -f "$C" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail=0
note() { printf '  %s\n' "$*"; }
bad()  { printf '  FAIL: %s\n' "$*"; fail=1; }

# THE IMAGE IS PART OF THE INSTALL PATH. The appliance runs timescale/timescaledb:2.16.1-pg16, and two
# hypertable insert-blocker triggers on public.accounting_records and public.audit_log exist only because
# TimescaleDB is present. Reconstructing on plain postgres builds a schema that looks complete and silently
# lacks them, which is exactly the class of difference a table count cannot see.
echo "== blank PostgreSQL cluster (never saw any appliance) =="
# CLEANROOM_PORT publishes 5432 so a process on the host can reach the built schema. Unset by
# default: the reconstruction itself talks to the container through docker exec and needs no port.
PORTARG=""
[ -n "${CLEANROOM_PORT:-}" ] && PORTARG="-p ${CLEANROOM_PORT}:5432"
# shellcheck disable=SC2086  # PORTARG is empty or exactly two words; quoting would pass "" as an arg
docker run -d --name "$C" $PORTARG -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=stayconnect_site \
  "${CLEANROOM_IMAGE:-timescale/timescaledb:2.16.1-pg16}" >/dev/null
# The TimescaleDB image starts a TEMPORARY server to run its initialisation, then shuts it down and starts
# the real one. pg_isready -- and even a successful query -- can therefore hit a server that is about to
# disappear, which shows up later as "the database system is shutting down" in the middle of a migration.
# So: wait for the SECOND "ready to accept connections" in the container log (the first belongs to the
# temporary server), then require three consecutive successful queries.
ready=0
for _ in $(seq 1 180); do
  if [ "$(docker logs "$C" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ]; then
    ok=0
    for _ in 1 2 3; do
      docker exec "$C" psql -U postgres -d stayconnect_site -tAqc 'select 1' >/dev/null 2>&1 && ok=$((ok+1))
      sleep 1
    done
    [ "$ok" = "3" ] && { ready=1; break; }
  fi
  sleep 1
done
[ "$ready" = "1" ] || { echo "  FAIL: database never became ready"; exit 1; }
psql_run() { docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1 "$@"; }
# OWNERSHIP IS REPRODUCED, NOT INHERITED FROM WHOEVER RAN THE INSTALL. Numbered migrations build the platform
# as `stayconnect`; the IAM base builds as `iam_v2_migrator`, which SET ROLEs to `iam_v2_owner`. Running
# everything as the bootstrap superuser produces a schema whose SECURITY DEFINER functions execute as the
# wrong principal.
psql_as() { local role="$1"; shift; docker exec -i "$C" psql -U postgres -d stayconnect_site    -v ON_ERROR_STOP=1 -c "SET ROLE $role;" "$@"; }
psql_q()   { docker exec -i "$C" psql -U postgres -d stayconnect_site -tAq "$@"; }

echo "== Gate-P roles, from the repository's own role source =="
# gatep-roles.sql is the authoritative role definition and deliberately contains NO passwords -- those are
# set separately on a real appliance by gatep-set-passwords.sh, which computes a SCRAM verifier locally so
# cleartext never reaches SQL, argv or a log. Nothing here invents a stand-in role.
if psql_run < "$GATEP/gatep-roles.sql" >"$OUT/roles.log" 2>&1; then
  note "gatep-roles.sql applied (svc_scd, svc_edged, svc_acctd, svc_netd)"
else
  bad "gatep-roles.sql did not apply: $(tail -1 "$OUT/roles.log")"
fi
# The IAM domain roles come from their own repository-controlled Gate-P file, not from the scratch fixture
# and not from stand-ins invented here. Ownership decides what the SECURITY DEFINER boundary functions can
# reach, so it has to be built from the same source every time.
# `stayconnect` is the appliance's own bootstrap/migration role and owns the public platform there, so the
# reconstruction creates it with the same standing and runs the numbered migrations as it. SUPERUSER because
# the platform migrations create extensions; NOLOGIN because nothing may connect as it here.
psql_run -tAqc "DO \$\$ BEGIN CREATE ROLE stayconnect NOLOGIN SUPERUSER; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;" >/dev/null

echo
docker cp "$GATEP" "$C:/tmp/gatep" >/dev/null
echo "== the authoritative ordered install chain =="
# 0001..0008   public platform (0002 creates public.guest_networks)
# iam_base     MG-0 anchor, then mg1..mg9 -- BEFORE 0009, the first migration to address iam_v2 objects
# 0009..NNNN   everything that builds on the IAM domain
applied=0
apply_one() {
  local f="$1"
  local label="$2"
  local as="${3:-stayconnect}"
  local log="$OUT/$(echo "$label" | tr '/' '_').log"
  # SET ROLE decides OWNERSHIP, and ownership is the security model: the IAM boundary functions are SECURITY
  # DEFINER and execute as their owner. Running the install as whatever superuser happened to bootstrap the
  # cluster produces a schema that matches on structure and is wrong on authority.
  if { printf 'SET ROLE %s;
' "$as"; cat "$f"; } | psql_run >"$log" 2>&1; then
    applied=$((applied+1)); printf '  ok   %s\n' "$label"
    # LEDGER. Every install step is recorded, base steps included. An install path with unrecorded steps
    # cannot be audited afterwards -- which is exactly how the IAM base schema came to be invisible in
    # schema_migrations on the development appliance.
    psql_q -c "INSERT INTO schema_migrations(version) VALUES ('$label') ON CONFLICT DO NOTHING" >/dev/null 2>&1 || true
  else
    bad "$label -- $(tail -1 "$log")"
  fi
}

for f in $(ls "$MIG"/*.up.sql | sort); do
  n="$(basename "$f" .up.sql)"
  case "$n" in
    0009_*)
      # After 0002 has created public.guest_networks, so the REFERENCES grant has something to grant on.
      if MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1            -f /tmp/gatep/gatep-iam-roles.sql >"$OUT/iam-roles.log" 2>&1
      then note "gatep-iam-roles.sql applied (iam_v2_owner, iam_v2_migrator)"
      else bad "gatep-iam-roles.sql did not apply: $(tail -1 "$OUT/iam-roles.log")"; fi
      apply_one "$BASE/mg0_guest_networks_tsi_anchor.sql" "iam_base/mg0_guest_networks_tsi_anchor" stayconnect
      for b in $(ls "$BASE"/mg[1-9]*.sql | sort -V); do
        # As the OWNER itself. SET ROLE iam_v2_migrator would make the MIGRATOR the owner of every object
        # it creates, which is one role off and changes what the SECURITY DEFINER functions execute as.
        # iam_v2_migrator exists so a migration RUNNER can become the owner; the owner is who must own.
        apply_one "$b" "iam_base/$(basename "$b" .sql)" iam_v2_owner
      done
      ;;
  esac
  # The numbered migrations run as the PLATFORM role, because they maintain public.schema_migrations and
  # public platform objects as well as iam_v2 ones. Running them as iam_v2_owner fails immediately with
  # "permission denied for table schema_migrations" -- the IAM owner is deliberately not a platform writer.
  #
  # The consequence is that iam_v2 objects CREATED by migrations 0009+ are left owned by the platform role at
  # this point. That is closed by an explicit install step -- deploy/gatep/gatep-iam-ownership.sql, applied
  # below from the repository, exactly as a real install applies it -- and NOT by anything invented in this
  # verifier. The ownership that results is then compared like any other catalog fact, so a build that fails
  # to converge shows up as a difference rather than as silence.
  apply_one "$f" "$n" stayconnect
done
note "install steps applied: $applied"

echo
echo "== Gate-P ownership assertion, from the real path =="
# BEFORE the grants, so the privilege bootstrap is applied to the FINAL ownership state. Ownership is not a
# tidy-up: the IAM boundary functions are SECURITY DEFINER and execute as their owner, so this step decides
# what the whole boundary runs with. The file fails closed on anything it cannot converge.
if MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1      -f /tmp/gatep/gatep-iam-ownership.sql >"$OUT/iam-ownership.log" 2>&1; then
  note "gatep-iam-ownership.sql applied, including its fail-closed assertions"
else
  bad "gatep-iam-ownership.sql did not apply: $(tail -1 "$OUT/iam-ownership.log")"
fi
# APPLIED TWICE ON PURPOSE. The disaster-recovery procedure re-runs this step on a rebuild, and a step
# documented as idempotent that has only ever been run once is a claim, not a property.
if MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1      -f /tmp/gatep/gatep-iam-ownership.sql >"$OUT/iam-ownership-2.log" 2>&1; then
  note "re-applied cleanly: the ownership step is idempotent"
else
  bad "gatep-iam-ownership.sql is NOT idempotent: $(tail -1 "$OUT/iam-ownership-2.log")"
fi

echo
echo "== Gate-P grants and fail-closed assertions, from the real path =="
# Copied into the container and run with -f, not piped: gatep-grants.sql uses \ir to include the
# per-service grant files, and \ir resolves relative to the FILE being executed. Fed through stdin there is
# no such location and every include fails -- which would silently skip most of the privilege bootstrap.
# The tree was copied to /tmp/gatep once, before the install chain. Repeating `docker cp` into a directory
# that already exists NESTS it at /tmp/gatep/gatep and leaves the first copy in place, so a later edit ends
# up verified against whichever copy psql happened to read.
# MSYS_NO_PATHCONV: under Git Bash on Windows the container-side path /tmp/gatep/... is rewritten into a
# Windows path before docker ever sees it, and psql then reports a file that was never asked for.
if MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1      -f /tmp/gatep/gatep-grants.sql >"$OUT/grants.log" 2>&1; then
  note "gatep-grants.sql applied, including its closing assertions"
else
  bad "gatep-grants.sql did not apply: $(tail -1 "$OUT/grants.log")"
fi

echo
echo "== ledger completeness =="
unrecorded=0
for f in $(ls "$MIG"/*.up.sql | sort); do
  n="$(basename "$f" .up.sql)"
  [ "$(psql_q -c "SELECT count(*) FROM schema_migrations WHERE version='$n'")" = "1" ] || {
    bad "migration $n is not recorded in schema_migrations"; unrecorded=$((unrecorded+1)); }
done
for b in $(ls "$BASE"/mg*.sql | sort -V); do
  n="iam_base/$(basename "$b" .sql)"
  [ "$(psql_q -c "SELECT count(*) FROM schema_migrations WHERE version='$n'")" = "1" ] || {
    bad "base step $n is not recorded in schema_migrations"; unrecorded=$((unrecorded+1)); }
done
[ "$unrecorded" = "0" ] && note "every migration and base step is represented in the ledger"

echo
echo "== REQUIRED: the contract-defined guest-network anchor and its mapping FK =="
anchor="$(psql_q -c "SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
                      WHERE c.relname='guest_networks_tsi_anchor' AND i.indisvalid AND i.indisready")"
[ "$anchor" = "1" ] && note "guest_networks_tsi_anchor present and valid" \
                    || bad "guest_networks_tsi_anchor missing or invalid"
anchordef="$(psql_q -c "SELECT indexdef FROM pg_indexes WHERE indexname='guest_networks_tsi_anchor'")"
case "$anchordef" in
  *"UNIQUE INDEX"*guest_networks*"(tenant_id, site_id, id)"*) note "anchor definition matches the contract" ;;
  *) bad "anchor is not UNIQUE (tenant_id, site_id, id): ${anchordef:-<absent>}" ;;
esac
mapfk="$(psql_q -c "SELECT count(*) FROM pg_constraint
                     WHERE conrelid='iam_v2.guest_network_pms_map'::regclass AND contype='f'
                       AND confrelid='public.guest_networks'::regclass")"
[ "$mapfk" = "1" ] && note "guest_network_pms_map composite FK to guest_networks present" \
                   || bad "guest_network_pms_map composite FK to guest_networks is ABSENT"

echo
echo "== REQUIRED: deterministic IAM-v2 ownership =="
# Asserted here in the verifier as well as in the install file, and asserted SEMANTICALLY -- by asking the
# catalog who owns each object, not by trusting that the ownership step reported success. Any object left
# behind is named, because an ownership check whose failure mode is a count tells the next reader nothing.
badown="$(psql_q -c "SELECT COALESCE(string_agg(name||' -> '||owner, ', '), '') FROM (
    SELECT n.nspname||'.'||c.relname AS name, pg_get_userbyid(c.relowner) AS owner
      FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
     WHERE n.nspname='iam_v2' AND c.relkind IN ('r','v','m','p','S')
       AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner'
    UNION ALL
    SELECT n.nspname||'.'||p.proname, pg_get_userbyid(p.proowner)
      FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
     WHERE n.nspname='iam_v2' AND pg_get_userbyid(p.proowner) <> 'iam_v2_owner') x")"
[ -z "$badown" ] && note "every iam_v2 table, view, sequence, function and procedure is owned by iam_v2_owner"                  || bad "iam_v2 objects owned by the wrong role: $badown"
# The SECURITY DEFINER boundary specifically. Stated separately from the sweep above because this is the one
# that changes what the system can do, not merely what the catalog says.
secdef_total="$(psql_q -c "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
                            WHERE n.nspname='iam_v2' AND p.prosecdef")"
secdef_bad="$(psql_q -c "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
                          WHERE n.nspname='iam_v2' AND p.prosecdef
                            AND pg_get_userbyid(p.proowner) <> 'iam_v2_owner'")"
[ "$secdef_bad" = "0" ] && note "all $secdef_total SECURITY DEFINER functions in iam_v2 execute as iam_v2_owner"                         || bad "$secdef_bad of $secdef_total SECURITY DEFINER functions execute as the wrong owner"
schemaown="$(psql_q -c "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='iam_v2'")"
[ "$schemaown" = "iam_v2_owner" ] && note "schema iam_v2 is owned by iam_v2_owner"                                   || bad "schema iam_v2 is owned by $schemaown"

echo
echo "== REQUIRED: the superseded guest-auth authority cannot be selected by configuration =="
# A build is not free of the superseded authority merely because nothing currently selects it. This runs the
# real production-profile config loader against hostile environments and requires each one to REFUSE, so the
# proof is a property of the shipped binary rather than of the environment file it happens to be given.
# It is run against the PRODUCTION BUILD -- `-tags stayconnect_production`, the compilation a Production
# appliance ships -- and against the development build too, because the property being proven is that the two
# differ in exactly the intended way and that neither can be talked out of it by configuration.
if command -v go >/dev/null 2>&1; then
  if (cd "$ROOT/data-plane" && go build -tags stayconnect_production ./...         && go test -tags stayconnect_production ./internal/iamv2/ -count=1) >"$OUT/authority-lock.log" 2>&1; then
    note "PRODUCTION build: refuses every configuration that would select the superseded guest authority"
    note "  (explicit disable, master off, and unset all proven; see $OUT/authority-lock.log)"
  else
    bad "the guest authority lock did not hold on the production build: $(tail -3 "$OUT/authority-lock.log")"
  fi
  if (cd "$ROOT/data-plane" && go test ./internal/iamv2/ -count=1) >"$OUT/authority-dev.log" 2>&1; then
    note "DEVELOPMENT build: keeps the configurable behaviour the accepted trial evidence depends on"
  else
    bad "the development build regressed: $(tail -3 "$OUT/authority-dev.log")"
  fi
else
  bad "go toolchain unavailable: the guest-authority lock could not be proven, so this run is not acceptance evidence"
fi

echo
echo "== REQUIRED: zero superseded guest-IAM tables in the factory-clean schema =="
# The Production criterion is PHYSICAL absence, not unreachability. Asked of the built schema itself, so a
# migration that silently failed to drop one shows up here rather than in production.
legacy="$(psql_q -c "SELECT COALESCE(string_agg(tablename, ', ' ORDER BY tablename), '') FROM pg_tables
                      WHERE schemaname='public' AND tablename IN
                        ('sessions','guests','guest_accounts','vouchers','voucher_batches',
                         'ticket_templates','payments')")"
[ -z "$legacy" ] && note "none of the superseded guest-IAM tables exist"                  || bad "superseded guest-IAM table(s) still present: $legacy"
# And nothing may still point at them -- a surviving FK would mean the drop took a current dependency with it.
dangling="$(psql_q -c "SELECT count(*) FROM pg_constraint c
                        JOIN pg_class r ON r.oid=c.confrelid
                        JOIN pg_namespace n ON n.oid=r.relnamespace
                       WHERE c.contype='f' AND n.nspname='public' AND r.relname IN
                         ('sessions','guests','guest_accounts','vouchers','voucher_batches',
                          'ticket_templates','payments')")"
[ "$dangling" = "0" ] && note "no foreign key references any removed object"                       || bad "$dangling foreign key(s) still reference a removed object"
# The retained challenge/nonce stores must have lost the access-plan coupling that was their only tie to the
# superseded domain.
coupling="$(psql_q -c "SELECT count(*) FROM information_schema.columns
                        WHERE table_schema='public' AND column_name='template_id'
                          AND table_name IN ('auth_otps','social_oauth_states')")"
[ "$coupling" = "0" ] && note "auth_otps and social_oauth_states carry no access-plan coupling"                       || bad "$coupling retained table(s) still carry template_id"
# IAM-v2 must be the only guest-IAM structure left standing.
iamv2_guest="$(psql_q -c "SELECT count(*) FROM pg_tables WHERE schemaname='iam_v2' AND tablename IN
                            ('sessions','guest_principals','guest_access_accounts','vouchers',
                             'voucher_batches','entitlements','internet_packages')")"
[ "$iamv2_guest" = "7" ] && note "the IAM-v2 guest domain is present and is the only one"                          || bad "expected 7 core IAM-v2 guest tables, found $iamv2_guest"

echo
echo "== factory-clean: schema built, nothing seeded =="
psql_q -c "SELECT '  public tables : '||count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'"
psql_q -c "SELECT '  iam_v2 tables : '||count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE'"
# The IAM-v2 guest domain must also be EMPTY on a factory-clean install: no principal, account, voucher or
# session arrives with the schema. They arrive through enrolment, licensing and Hotel-Admin configuration.
for t in guest_principals guest_access_accounts vouchers sessions entitlements; do
  n="$(psql_q -c "SELECT count(*) FROM iam_v2.$t" 2>/dev/null || echo '?')"
  [ "$n" = "0" ] || bad "iam_v2.$t is not empty on a factory-clean install ($n rows)"
done
for t in tenants sites appliances operators; do
  n="$(psql_q -c "SELECT count(*) FROM public.$t" 2>/dev/null || echo '?')"
  [ "$n" = "0" ] || bad "public.$t is not empty on a factory-clean install ($n rows)"
done
note "identity-bearing tables are empty"

echo
echo "== semantic catalog (the actual acceptance evidence) =="
cat > "$OUT/catalog.sql" <<'SQL'
SELECT 'COLUMN|'||table_schema||'.'||table_name||'|'||column_name||'|'||data_type||'|'||is_nullable||'|'||COALESCE(column_default,'-')
  FROM information_schema.columns WHERE table_schema IN ('public','iam_v2')
UNION ALL SELECT 'CONSTRAINT|'||n.nspname||'.'||rel.relname||'|'||con.conname||'|'||con.contype::text||'|'||pg_get_constraintdef(con.oid)
  FROM pg_constraint con JOIN pg_class rel ON rel.oid=con.conrelid
  JOIN pg_namespace n ON n.oid=rel.relnamespace WHERE n.nspname IN ('public','iam_v2')
UNION ALL SELECT 'INDEX|'||schemaname||'.'||tablename||'|'||indexname||'|'||indexdef
  FROM pg_indexes WHERE schemaname IN ('public','iam_v2')
UNION ALL SELECT 'TRIGGER|'||n.nspname||'.'||c.relname||'|'||t.tgname||'|'||pg_get_triggerdef(t.oid)
  FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE NOT t.tgisinternal AND n.nspname IN ('public','iam_v2')
UNION ALL SELECT 'FUNCTION|'||n.nspname||'.'||p.proname||'|'||pg_get_function_identity_arguments(p.oid)||'|secdef='||p.prosecdef::text||'|owner='||pg_get_userbyid(p.proowner)
  FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname IN ('public','iam_v2')
UNION ALL SELECT 'OWNER|'||n.nspname||'.'||c.relname||'|'||pg_get_userbyid(c.relowner)
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname IN ('public','iam_v2') AND c.relkind IN ('r','v','m')
UNION ALL SELECT 'SCHEMAOWNER|'||nspname||'|'||pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname IN ('public','iam_v2')
UNION ALL SELECT 'TABLEGRANT|'||table_schema||'.'||table_name||'|'||grantee||'|'||privilege_type
  FROM information_schema.role_table_grants WHERE table_schema IN ('public','iam_v2')
UNION ALL SELECT 'FUNCGRANT|'||n.nspname||'.'||p.proname||'|'||r.rolname
  FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
  CROSS JOIN pg_roles r WHERE n.nspname IN ('public','iam_v2') AND NOT r.rolsuper
    AND has_function_privilege(r.rolname, p.oid, 'EXECUTE')
UNION ALL SELECT 'MEMBERSHIP|'||g.rolname||'|'||m.rolname
  FROM pg_auth_members am JOIN pg_roles g ON g.oid=am.roleid JOIN pg_roles m ON m.oid=am.member;
SQL
 psql_q -f /dev/stdin < "$OUT/catalog.sql" | sed '/^$/d' | sort > "$OUT/catalog.txt"
note "catalog facts: $(wc -l < "$OUT/catalog.txt")"
note "written to $OUT/catalog.txt"

if [ -n "$REF" ] && [ -f "$REF" ]; then
  echo
  echo "== semantic comparison against the reference catalog =="
  sed '/^$/d' "$REF" | sort > "$OUT/reference.sorted"
  if diff -u "$OUT/reference.sorted" "$OUT/catalog.txt" > "$OUT/catalog.diff"; then
    note "IDENTICAL across columns, constraints, indexes, triggers, functions, ownership and privileges"
  else
    note "only in reference: $(grep -c '^-[^-]' "$OUT/catalog.diff" || true)"
    note "only in clean build: $(grep -c '^+[^+]' "$OUT/catalog.diff" || true)"
    note "detail: $OUT/catalog.diff"
  fi
fi

echo
[ "$fail" = "0" ] || { echo "CLEAN_INSTALL_RECONSTRUCTION = FAIL"; exit 1; }
echo "CLEAN_INSTALL_RECONSTRUCTION = PASS (blank cluster -> complete schema, repository sources only, no ad-hoc SQL, nothing seeded)"
