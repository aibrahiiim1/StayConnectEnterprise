#!/usr/bin/env bash
# PHASE-7 — REBUILD THE ACCEPTED SCHEMA FROM REPOSITORY-CONTROLLED SOURCES ALONE.
#
# The committed appliance snapshot is an ORACLE, not a source. This script must reach the same accepted
# semantic state without copying it, or the repository cannot rebuild its own system.
#
# THE ACCEPTED HISTORY, and the identity each layer ran under. That identity is not a detail: it decides
# ownership, and ownership decides what SECURITY DEFINER functions may do.
#
#   L1  data-plane/migrations/0001..0008     the edge/public schema            as the SUPERUSER
#   L2  iam_v2_scratch/migrations/mg1..mg9   Phase 1A: CREATE SCHEMA iam_v2    as IAM_V2_OWNER
#       (+ the mg0 guest_networks anchor, which mg1's foreign keys require)
#   L2b deploy/gatep/gatep-roles.sql         Phase 1B Gate P: the svc_* roles  as the SUPERUSER
#       deploy/gatep/gatep-grants.sql        Phase 1B Gate P: least privilege  as the SUPERUSER
#   L3  data-plane/migrations/0009..0047     Phases 2 through 6                as the SUPERUSER
#
# WHY L2 IS DIFFERENT, and how that was established rather than guessed: the Phase-1A Live-Dark Acceptance
# record states "MG-1..MG-9 applied as iam_v2_owner" and "Schema + all 49 objects owned by iam_v2_owner".
# Applying them as the superuser instead produced a schema owned by postgres -- the ONE residual difference
# against the oracle after every other cause was eliminated, and the reason the reconstruction's definer
# functions could not read their own schema.
#
# WHY L3 IS NOT: six migrations (0028, 0030, 0033, 0034, 0039, 0040) create roles and grant privileges the
# schema owner does not administer. They fail under SET ROLE iam_v2_owner and always did; on the appliance
# they ran as the superuser.
#
#   usage:  phase7_reconstruct_from_sources.sh [database-name]     default: iam_recon
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

DB="${1:-${PHASE7_RECON_DB:-iam_recon}}"
SU="${PHASE7_SUPERUSER:-stayconnect}"

# ---- AN INDEPENDENTLY ISOLATED, DISPOSABLE CLUSTER ------------------------------------------------------------
#
# ROLES ARE CLUSTER-GLOBAL, NOT PER-DATABASE. While this build ran in the same container as the restored oracle,
# every role the oracle's restore created was already present before the reconstruction created anything, and
# every role the reconstruction created was equally visible to the oracle. The role, membership and attribute
# sections of the fidelity proof were therefore comparing a shared `pg_authid` against itself: they could not
# have disagreed, so their agreement proved nothing. That is a false green of the worst kind -- the surface most
# worth proving was the one structurally incapable of failing.
#
# So the reconstruction now brings up its OWN empty cluster, proves it is empty of the roles in question BEFORE
# building, and destroys it afterwards. The oracle stays untouched in its own cluster, immutable relative to the
# candidate.
ISOLATED="${PHASE7_ISOLATED:-1}"
IMAGE="${PHASE7_IMAGE:-postgres:16}"
if [ "$ISOLATED" = "1" ]; then
  C="${PHASE7_CONTAINER:-phase7-recon}"
  # A pre-existing container is REFUSED, never reused and never destroyed. Pointing this script at iamv2-p6 or
  # any other working container once destroyed hours of live state; the guard is why that cannot recur.
  if docker inspect "$C" >/dev/null 2>&1; then
    if [ "$(docker inspect "$C" --format '{{index .Config.Labels "phase7-disposable"}}' 2>/dev/null)" = "1" ]; then
      docker rm -f "$C" >/dev/null 2>&1
    else
      echo "REFUSING: container '$C' already exists and is not a phase7 disposable. Not touching it." >&2
      exit 2
    fi
  fi
  docker run -d --name "$C" --label phase7-disposable=1 \
    -e POSTGRES_PASSWORD=phase7 -e POSTGRES_HOST_AUTH_METHOD=trust "$IMAGE" >/dev/null 2>&1 \
    || { echo "REFUSING: could not start the disposable cluster ($IMAGE)" >&2; exit 2; }
  [ "${PHASE7_KEEP:-0}" = "1" ] || trap 'docker rm -f "$C" >/dev/null 2>&1' EXIT
  ready=0
  for _ in $(seq 1 60); do
    if docker exec "$C" pg_isready -U postgres >/dev/null 2>&1; then ready=1; break; fi
    sleep 1
  done
  [ "$ready" = "1" ] || { echo "REFUSING: the disposable cluster never became ready" >&2; exit 2; }
else
  C="${PHASE7_CONTAINER:-iamv2-p6}"
fi

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" </dev/null 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

# apply_as <role|-> <file>   -- '-' means the bootstrap superuser (postgres) with no SET ROLE
apply_as(){
  local role="$1" file="$2" out
  if [ "$role" = "-" ]; then
    out="$(docker exec -i "$C" psql -U postgres -d "$DB" -q -v ON_ERROR_STOP=1 < "$file" 2>&1)"
  else
    out="$( { printf 'SET ROLE %s;\n' "$role"; cat "$file"; } \
            | docker exec -i "$C" psql -U postgres -d "$DB" -q -v ON_ERROR_STOP=1 2>&1 )"
  fi
  [ $? -eq 0 ] || { printf '%s' "$out" | grep -i '^ERROR' | head -1; return 1; }
  return 0
}

echo "== Phase 7: reconstructing the accepted schema from repository sources ($DB) =="

if [ "$ISOLATED" = "1" ]; then
  eq "L0 the cluster is genuinely fresh: none of the roles under test pre-exist" \
     "$(docker exec -i "$C" psql -U postgres -d postgres -tAqc \
        "SELECT count(*) FROM pg_roles WHERE rolname NOT LIKE 'pg\\_%' AND rolname <> 'postgres'" </dev/null 2>&1)" "0"
else
  no "L0 NOT isolated: running in a shared cluster, so role-level agreement proves nothing" "PHASE7_ISOLATED=0"
fi

docker exec -i "$C" psql -U postgres -d postgres -qc "DROP DATABASE IF EXISTS $DB" </dev/null >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d postgres -qc "CREATE DATABASE $DB" </dev/null >/dev/null 2>&1 \
  || { echo "cannot create $DB"; exit 1; }

# ---- L0b: THE ROLE MODEL, FROM ITS ACTUAL SOURCE -------------------------------------------------------------
#
# iam_v2_scratch/roles.sql is the Phase-1A role model: it creates iam_v2_owner, iam_v2_migrator and the five
# iam_v2_svc_* roles, grants owner-to-migrator membership, strips CREATE from PUBLIC, and grants REFERENCES on
# public.guest_networks. Using it, rather than hand-creating the two identities the build happened to need, is
# what closed the last five parts: the isolated cluster had no iam_v2_svc_* roles at all, so five ROLE: lines
# were missing and every FNEXEC list was short by five grantees. The shared cluster hid that too.
#
# THE ONLY ADAPTATION is the database name: roles.sql says GRANT CREATE ON DATABASE iam_scratch because that is
# the database Phase 1A ran in. The name of the database is a property of the environment, not of the schema, so
# it is substituted and nothing else is.
docker exec -i "$C" psql -U postgres -d "$DB" -q -v ON_ERROR_STOP=1 </dev/null >/dev/null 2>&1 <<SQL
  DO \$\$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$SU') THEN
      CREATE ROLE $SU SUPERUSER LOGIN INHERIT CREATEDB CREATEROLE REPLICATION BYPASSRLS;
    ELSE ALTER ROLE $SU SUPERUSER LOGIN INHERIT CREATEDB CREATEROLE REPLICATION BYPASSRLS; END IF;
  END \$\$;
SQL
docker exec -i "$C" psql -U postgres -d postgres -qc \
  "GRANT CREATE, CONNECT ON DATABASE $DB TO iam_v2_owner, $SU" </dev/null >/dev/null 2>&1
eq "the installation superuser exists with the appliance's own attributes" \
   "$(q "SELECT rolsuper::text||rolbypassrls::text||rolcreaterole::text||rolcreatedb::text||rolreplication::text
           FROM pg_roles WHERE rolname='$SU'")" "truetruetruetruetrue"

# ---- L1: the edge/public schema, as the superuser ----------------------------------------------------------
n=0; bad=""
for f in "$ROOT"/data-plane/migrations/000[1-8]_*.up.sql; do
  if e="$(apply_as "$SU" "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .up.sql):$e"; fi
done
eq "L1 the edge/public schema applied (0001-0008)" "$n" "8"
[ -z "$bad" ] || no "L1 failures" "$bad"

# ---- L0b: THE ROLE MODEL, FROM ITS ACTUAL SOURCE, AFTER THE SCHEMA IT REFERENCES -----------------------------
#
# Placed HERE and not earlier for a reason discovered by running it: roles.sql ends with
# GRANT REFERENCES ON public.guest_networks TO iam_v2_owner, so the edge schema must already exist. Applied
# before L1 the file aborts at that statement under ON_ERROR_STOP, the grant never lands, and mg1 then fails
# with "permission denied for table guest_networks" -- which is exactly the ordering Phase 1A had to observe.
# The superuser must exist first: roles.sql assumes an installation identity is already present, and on the
# appliance that identity is the cluster owner. Its attributes are read from the appliance, not chosen -- all
# four of BYPASSRLS, CREATEROLE, CREATEDB and REPLICATION are true there, and omitting them was itself a
# fidelity difference until the role-security surface was widened enough to see it.
if e="$(sed "s/iam_scratch/$DB/g" "$HERE/roles.sql" | docker exec -i "$C" psql -U postgres -d "$DB" \
        -q -v ON_ERROR_STOP=1 2>&1)"; then
  ok "L0b roles.sql (the Phase-1A role model) executed without error"
else
  no "L0b roles.sql FAILED" "$(printf '%s' "$e" | grep -i '^ERROR' | head -1)"
fi
eq "L0b roles.sql created the schema owner and migrator" \
   "$(q "SELECT count(*) FROM pg_roles WHERE rolname IN ('iam_v2_owner','iam_v2_migrator')")" "2"
eq "L0b roles.sql created the five iam_v2_svc_* service roles" \
   "$(q "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'iam\_v2\_svc\_%'")" "5"

# MG-0, AS THE ACCEPTED HISTORY RAN IT. The Phase-1A record: "MG-0 (non-transactional): CREATE UNIQUE INDEX
# CONCURRENTLY guest_networks_tsi_anchor ON public.guest_networks (tenant_id, site_id, id)", executed by the
# superuser, and AC-03 records "public schema unchanged except the MG-0 anchor".
#
# THE PRIVILEGES ARE THE HISTORICAL MINIMUM, read from the appliance rather than chosen for convenience. It
# grants iam_v2_owner exactly REFERENCES on public.guest_networks and INSERT/SELECT on public.schema_migrations
# -- nothing else. An earlier version granted ALL ON SCHEMA public and ALL ON ALL TABLES to make the build
# pass; that is an over-grant which contradicts AC-03 and would have made the reconstruction a laxer system
# than the one it claims to reproduce.
docker exec -i "$C" psql -U postgres -d "$DB" -qc "
  SET ROLE $SU;
  CREATE UNIQUE INDEX IF NOT EXISTS guest_networks_tsi_anchor ON public.guest_networks (tenant_id, site_id, id);
  GRANT INSERT, SELECT ON public.schema_migrations TO iam_v2_owner;" </dev/null >/dev/null 2>&1
eq "L2 roles.sql granted ONLY REFERENCES on public.guest_networks (no more)" \
   "$(q "SELECT string_agg(privilege_type, ',' ORDER BY privilege_type)
           FROM information_schema.role_table_grants
          WHERE table_schema='public' AND table_name='guest_networks' AND grantee='iam_v2_owner'")" "REFERENCES"
eq "L2 ...and only INSERT/SELECT on public.schema_migrations" \
   "$(q "SELECT string_agg(privilege_type, ',' ORDER BY privilege_type)
           FROM information_schema.role_table_grants
          WHERE table_schema='public' AND table_name='schema_migrations' AND grantee='iam_v2_owner'")" "INSERT,SELECT"
eq "L2 iam_v2_owner was NOT granted the public schema itself (AC-03: public unchanged)" \
   "$(q "SELECT (COALESCE(nspacl::text,'') NOT LIKE '%iam_v2_owner%')::text FROM pg_namespace WHERE nspname='public'")" "true"
eq "L2 the mg0 guest-network anchor exists" \
   "$(q "SELECT count(*) FROM pg_indexes WHERE indexname='guest_networks_tsi_anchor'")" "1"

# ---- L2: Phase 1A, AS iam_v2_owner --------------------------------------------------------------------------
n=0; bad=""
for f in "$HERE"/migrations/mg[1-9]_*.sql; do
  if e="$(apply_as iam_v2_owner "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .sql):$e"; fi
done
eq "L2 Phase 1A applied as iam_v2_owner (mg1-mg9)" "$n" "9"
[ -z "$bad" ] || no "L2 failures" "$bad"
eq "L2 the schema is owned by iam_v2_owner, as the Phase-1A record states" \
   "$(q "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='iam_v2'")" "iam_v2_owner"
eq "L2 Phase 1A left 49 iam_v2 tables, as the Phase-1A record states" \
   "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'")" "49"

# ---- L1b: THE PUBLIC TABLES NO MIGRATION CREATES -------------------------------------------------------------
#
# Found only because Gate P's errors stopped being swallowed: gatep-grants.sql grants on public.edge_executed_
# commands, public.edge_installed_updates and public.edge_offline_packages, and NO migration creates any of
# them. They are created lazily by scd itself at first use --
#
#   data-plane/cmd/scd/commands.go:31        CREATE TABLE IF NOT EXISTS edge_executed_commands (...)
#   data-plane/cmd/scd/updates.go:39         CREATE TABLE IF NOT EXISTS edge_installed_updates (...)
#   data-plane/cmd/scd/offline_import.go:51  CREATE TABLE IF NOT EXISTS edge_offline_packages (...)
#
# -- and on the appliance all three are owned by the superuser, which is the identity scd connects as. So the
# accepted history is: scd ran, created them, and Gate P's grants then had something to grant on. That is a real
# property of this system, not a gap in the reconstruction: the schema is not fully described by its migrations.
# The DDL below is transcribed from those three source files and applied under the identity that owns them.
docker exec -i "$C" psql -U postgres -d "$DB" -q -v ON_ERROR_STOP=1 </dev/null >/dev/null 2>&1 <<SQL
SET ROLE $SU;
CREATE TABLE IF NOT EXISTS edge_executed_commands (
    command_id UUID PRIMARY KEY, command_type TEXT, status TEXT,
    result JSONB, completed_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS edge_installed_updates (
    update_id UUID PRIMARY KEY, component TEXT, version TEXT, status TEXT,
    installed_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS edge_offline_packages (
    package_id UUID PRIMARY KEY, nonce TEXT UNIQUE, consumed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reconciled_at TIMESTAMPTZ);
SQL
eq "L1b the three scd-created public tables exist, owned as on the appliance" \
   "$(q "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='public' AND c.relkind='r' AND pg_get_userbyid(c.relowner)='$SU'
            AND c.relname IN ('edge_executed_commands','edge_installed_updates','edge_offline_packages')")" "3"

# ---- L2b: Phase 1B Gate P, as the superuser ------------------------------------------------------------------
#
# `|| true` used to sit on these two lines. It meant a Gate-P file could fail outright -- a wrong path, a syntax
# error, a privilege the identity lacked -- and the build would still report success, because the only thing
# checked afterwards was that four roles EXIST. Roles are cluster-wide: they survive from any previous run and
# would have been found whether this run created them or not. Existence cannot prove execution, so the exit
# status is now believed, and the material effect is proved separately below.
for f in "$ROOT"/deploy/gatep/gatep-roles.sql "$ROOT"/deploy/gatep/gatep-grants.sql; do
  if e="$(apply_as "$SU" "$f")"; then ok "L2b $(basename "$f") executed without error"
  else no "L2b $(basename "$f") FAILED" "$e"; fi
done
eq "L2b Gate-P created the runtime service roles" \
   "$(q "SELECT count(*) FROM pg_roles WHERE rolname IN ('svc_scd','svc_edged','svc_acctd','svc_netd')")" "4"
# THE MATERIAL EFFECT, not the role names: Gate P exists to grant least privilege, so prove a privilege it
# grants actually landed in THIS database (grants are per-database and cannot survive from another run).
eq "L2b Gate-P's grants materially landed in this database" \
   "$(q "SELECT (count(*) > 0)::text FROM information_schema.role_table_grants
          WHERE grantee IN ('svc_scd','svc_edged','svc_acctd','svc_netd')")" "true"

# ---- L2c: THE ROLE BOOTSTRAP THE SCHEMA OWNER CANNOT PERFORM -------------------------------------------------
#
# Also found only once the shared cluster stopped hiding it. Migrations 0017 and 0024 contain CREATE ROLE for
# the five sc_* financial and commerce roles, and iam_v2_owner has rolcreaterole=false on the appliance -- so
# the owner CANNOT have executed those statements. Applying 0017-0026 as the owner failed here for exactly that
# reason, and passed earlier only because the oracle's restore had already put the roles in the shared cluster.
#
# The appliance says what really happened, twice over:
#   - 0024's functions (p4_entitlement_grant_kernel, p4_grant_paid_entitlement, p4_grant_quoted_entitlement)
#     are owned by iam_v2_owner, so 0024 ran AS THE OWNER;
#   - every iam_v2 grant to an sc_* role has grantor = iam_v2_owner, so the grants were issued by the owner too.
# Both are only possible if the roles already existed when the owner ran those migrations -- the same pattern as
# Gate P, where a superuser step creates roles and least-privilege migrations then reference them.
#
# So the CREATE ROLE statements are extracted FROM THE ACCEPTED MIGRATIONS THEMSELVES and executed as the
# superuser first. Nothing is invented: same statements, same source files, only the identity the accepted
# history must have used. The migrations' own IF NOT EXISTS guards then make them no-ops on replay, which is
# precisely the state the appliance shows.
roles_sql="$(grep -h -oE 'CREATE ROLE [a-z0-9_]+ NOLOGIN' \
             "$ROOT"/data-plane/migrations/001[7-9]_*.up.sql "$ROOT"/data-plane/migrations/002[0-6]_*.up.sql \
             2>/dev/null | sort -u)"
[ -n "$roles_sql" ] || no "L2c no CREATE ROLE statements found in 0017-0026" "extraction returned nothing"
while IFS= read -r stmt; do
  [ -n "$stmt" ] || continue
  r="${stmt#CREATE ROLE }"; r="${r% NOLOGIN}"
  docker exec -i "$C" psql -U postgres -d "$DB" -qc \
    "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$r') THEN $stmt; END IF; END \$\$;" \
    </dev/null >/dev/null 2>&1
done <<< "$roles_sql"
eq "L2c the five sc_* roles from 0017/0024 exist before the owner replays them" \
   "$(q "SELECT count(*) FROM pg_roles WHERE rolname IN ('sc_payment_runtime','sc_payment_outcome',
                                                         'sc_commerce_runtime','sc_financial_operator',
                                                         'sc_financial_readonly')")" "5"
eq "L2c ...and the schema owner provably could not have created them itself" \
   "$(q "SELECT rolcreaterole::text FROM pg_roles WHERE rolname='iam_v2_owner'")" "false"

# ---- L3: Phases 2 through 6, and the identity CHANGES AT PHASE 5 --------------------------------------------
#
# Established from the appliance, not assumed. Its iam_v2 objects are owned in exactly two groups:
#
#   iam_v2_owner   68 tables, 111 functions   -- everything up to and including Phase 4
#   stayconnect     6 tables,  25 functions   -- every Phase-5 and Phase-6 object, by name:
#                                                appliance_product_settings, appliance_product_setting_changes,
#                                                guest_device_actions, entitlement_termination_evidence,
#                                                online_time_skipped_intervals, session_online_watermarks,
#                                                and the p5_*/p6_* functions
#
# So the first historical layer where the divergence appears is PHASE 5 (0027): from there on the migrations
# were applied as the superuser directly, while 0009-0026 were applied as the schema owner. That also explains
# the 182 "missing" grants without inventing anything -- their grantor is iam_v2_owner itself, so they are
# IMPLICIT OWNER privileges that appear simply because the owner owns the table. Nothing granted them, and a
# blanket reassignment to reproduce them would have been reproducing a symptom.
n=0; bad=""
for f in "$ROOT"/data-plane/migrations/00[0-2][0-9]_*.up.sql; do
  case "$(basename "$f")" in 000[1-8]_*) continue ;; esac
  case "$(basename "$f")" in 002[7-9]_*) continue ;; esac      # 0027+ belongs to L3b
  if e="$(apply_as iam_v2_owner "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .up.sql):$e"; fi
done
eq "L3a Phases 2-4 applied as iam_v2_owner (0009-0026)" "$n" "18"
[ -z "$bad" ] || no "L3a failures" "$(printf '%s' "$bad" | cut -c1-200)"

n=0; bad=""
for f in "$ROOT"/data-plane/migrations/002[7-9]_*.up.sql "$ROOT"/data-plane/migrations/00[3-4][0-9]_*.up.sql; do
  if e="$(apply_as "$SU" "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .up.sql):$e"; fi
done
eq "L3b Phases 5-6 applied as the superuser (0027-0047)" "$n" "21"
[ -z "$bad" ] || no "L3b failures" "$(printf '%s' "$bad" | cut -c1-200)"

eq "L3 ownership matches the appliance: Phase-6 tables belong to the superuser" \
   "$(q "SELECT pg_get_userbyid(relowner) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='iam_v2' AND c.relname='appliance_product_settings'")" "$SU"
eq "L3 ownership matches the appliance: Phase-3 tables belong to the schema owner" \
   "$(q "SELECT pg_get_userbyid(relowner) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='iam_v2' AND c.relname='stays'")" "iam_v2_owner"

# ---- L4: the accepted role graph -------------------------------------------------------------------------
#
# The Phase-1A record says "iam_v2_migrator is a member of the owner". That one membership accounts for TWO
# whole classes of difference: it is a MEMBER row, and because membership inherits EXECUTE it also puts
# iam_v2_migrator into the effective-privilege list of every owner-executable function -- 100 of them.
#
# An earlier version of this layer also reassigned EVERY iam_v2 object to iam_v2_owner, reading the Phase-1A
# invariant too widely. It holds for the 49 objects Phase 1A created, not for the Phase-2-to-6 tables, which
# the appliance leaves owned by the superuser that created them. Reassigning them invented 42 table grants
# the appliance does not have -- an owner holds every privilege implicitly, and information_schema reports it.
docker exec -i "$C" psql -U postgres -d "$DB" -qc "
  DO \$\$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='iam_v2_migrator') THEN
      CREATE ROLE iam_v2_migrator NOSUPERUSER NOLOGIN INHERIT;
    END IF;
    GRANT iam_v2_owner TO iam_v2_migrator;
  END \$\$;" </dev/null >/dev/null 2>&1
eq "L4 iam_v2_migrator is a member of iam_v2_owner, as the Phase-1A record states"    "$(q "SELECT count(*) FROM pg_auth_members am JOIN pg_roles m ON m.oid=am.member
          JOIN pg_roles g ON g.oid=am.roleid WHERE m.rolname='iam_v2_migrator' AND g.rolname='iam_v2_owner'")" "1"

# ---- THE 182 GRANTS AND 76 FUNCTION PRIVILEGES: RESOLVED, FROM EVIDENCE ------------------------------------
#
# An earlier build reported 182 table grants the appliance had and this one did not, plus 76 functions whose
# effective-privilege lists differed. Both are now closed, and neither was closed by reassignment.
#
# What they actually were: on the appliance those 182 rows all have grantor = iam_v2_owner and grantee =
# iam_v2_owner. They are IMPLICIT OWNER privileges. No statement ever granted them; they exist because the
# owner owns the table, and they appear the moment ownership is right. The 76 function rows are the same fact
# seen through EXECUTE. The first historical layer where the difference arises is PHASE 5 (0027), where the
# deployment identity changed from the schema owner to the superuser -- which is why the appliance has exactly
# two ownership groups and not one.
#
# The fix was therefore to reproduce the identity change (L3a as iam_v2_owner, L3b as the superuser), not to
# reassign anything. The blanket ALTER ... OWNER TO experiment that was tried first closed the 182 and then
# wrongly gave iam_v2_owner EXECUTE on 45 functions the appliance denies it -- a laxer system than the accepted
# one, arrived at by matching a number instead of reproducing a cause. It was discarded.

echo
# THE FIDELITY QUERY MUST NOT FAIL SILENTLY. If psql errors, `tail -1` returns an error string, and comparing
# an error string with an expected digest merely reports "not equal" -- indistinguishable from a real schema
# difference, and in the other direction an empty result compared against an empty expectation reads as equal.
# The shape of the answer is checked before the answer is used.
GOT="$(docker exec -i "$C" psql -U postgres -d "$DB" -tAq < "$HERE/phase7_fidelity.sql" 2>&1 | tail -1)"
case "$GOT" in
  [0-9a-f]*" parts="[0-9]*) ok "the fidelity query returned a well-formed digest" ;;
  *) no "the fidelity query did not return a digest" "$(printf '%s' "$GOT" | tr -d '\n' | cut -c1-140)"; GOT="" ;;
esac
echo "  reconstructed digest: ${GOT:-<none>}"

# ---- TWO VERDICTS, NEVER ONE ---------------------------------------------------------------------------------
#
# RECONSTRUCTION_BUILD says the accepted sources applied cleanly and produced the material effects each layer
# claims. It says NOTHING about whether the result matches the appliance. APPLIANCE_FIDELITY says only whether
# this digest equals the oracle's, and is NOT_PROVEN -- never PASS -- when no oracle was supplied to compare
# against. Collapsing the two is how "the build passed" becomes "fidelity is proven" in a later summary; they
# failed independently for most of this phase, which is exactly why they are printed independently.
echo "------------------------------------------------------------"
if [ "$fail" -eq 0 ]; then BUILD=PASS; else BUILD=FAIL; fi
printf 'RECONSTRUCTION_BUILD = %s (checks pass=%d fail=%d)\n' "$BUILD" "$pass" "$fail"

if [ -z "${GOT:-}" ]; then
  FID=NOT_PROVEN; why="the fidelity query produced no usable digest"
elif [ -z "${PHASE7_ORACLE_DIGEST:-}" ]; then
  FID=NOT_PROVEN; why="no oracle digest supplied; set PHASE7_ORACLE_DIGEST to compare"
elif [ "$GOT" = "$PHASE7_ORACLE_DIGEST" ] || [ "${GOT%% *}" = "$PHASE7_ORACLE_DIGEST" ]; then
  FID=PASS; why="equals the supplied appliance oracle"
else
  FID=FAIL; why="differs from the supplied appliance oracle ($PHASE7_ORACLE_DIGEST)"
fi
printf 'APPLIANCE_FIDELITY   = %s (%s)\n' "$FID" "$why"

# The exit status reflects both: a clean build that provably contradicts the oracle is not a success.
[ "$BUILD" = "PASS" ] && [ "$FID" != "FAIL" ]
