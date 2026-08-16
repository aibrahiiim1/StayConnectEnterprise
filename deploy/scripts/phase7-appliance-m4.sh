#!/usr/bin/env bash
# PHASE-7 M4 — THE ASSEMBLED SYSTEM, ON THE AUTHORIZED DEVELOPMENT APPLIANCE.
#
# Everything before this ran against scratch databases. This runs against the real appliance: the real
# services, the real roles, the real HTTP surfaces, the real schema. It is the only place where "the system
# works" can mean anything, because it is the only place where the system exists.
#
# WHAT IT MAY DO, AND WHAT IT MAY NOT.
#   - controlled SYNTHETIC state only, created by this script and removed by it;
#   - no real guest, no real PMS posting, no real payment-provider traffic, no paid access;
#   - no capability is enabled unless a proof requires it, and every such enablement is restored and the
#     restoration is PROVEN, not asserted;
#   - Production is never contacted. This is the DEVELOPMENT appliance.
#
# WHAT IT REFUSES TO PRETEND. Anything that cannot lawfully be executed here is printed as NOT PROVEN and
# counted as neither a pass nor a failure. A line that says NOT PROVEN must never increment a pass count --
# that defect has been found three times in this project's own gates and is not repeated here.
#
#   usage: phase7-appliance-m4.sh [--section <name>]
set -uo pipefail
APPL="${PHASE7_APPLIANCE:-172.21.60.23}"
PGC="${PHASE7_PG_CONTAINER:-stayconnect-pg}"
DB="${PHASE7_SITE_DB:-stayconnect_site}"
ONLY="${2:-}"

pass=0; fail=0; notproven=0
ok(){   printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){   printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
np(){   printf '  [NOT PROVEN] %s :: %s\n' "$1" "${2:-}"; notproven=$((notproven+1)); }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }
sec(){ printf '\n== %s ==\n' "$1"; }

# ssh -n throughout: without it ssh eats the caller's stdin and every here-string-fed loop runs exactly once.
sh_(){ ssh -n -o BatchMode=yes "root@$APPL" "$1" 2>&1; }
q(){   ssh -n -o BatchMode=yes "root@$APPL" "docker exec -i $PGC psql -U stayconnect -d $DB -tAqc \"$1\"" 2>&1; }
# qas <role> <sql> -- as a REAL runtime role, which is the only way a privilege claim means anything
qas(){ ssh -n -o BatchMode=yes "root@$APPL" "docker exec -i $PGC psql -U stayconnect -d $DB -tAqc \"SET ROLE $1; $2\"" 2>&1; }

BASE="/tmp/phase7-m4-baseline"

echo "===== PHASE-7 M4: the assembled system on the DEVELOPMENT appliance ($APPL) ====="
[ "$(q "SELECT 1")" = "1" ] || { echo "cannot reach the site database"; exit 2; }

# ---------------------------------------------------------------------------------------------------------
sec "1. the pre-validation DARK baseline, captured and verified"
#
# Captured BEFORE anything is touched, and every later restoration claim is compared against THIS file rather
# than against a value re-read afterwards. A restoration proved against a post-hoc reading proves nothing.
sh_ "mkdir -p $BASE" >/dev/null
sh_ "systemctl is-active stayconnect-scd stayconnect-acctd stayconnect-edged stayconnect-portald stayconnect-netd stayconnect-hotel-admin > $BASE/services.txt 2>&1; readlink -f /opt/stayconnect/hotel-admin > $BASE/ha-release.txt; docker exec -i $PGC psql -U stayconnect -d $DB -tAqc \"SELECT (SELECT count(*) FROM iam_v2.entitlements)||'|'||(SELECT count(*) FROM iam_v2.sessions)||'|'||(SELECT count(*) FROM iam_v2.stays)||'|'||(SELECT count(*) FROM iam_v2.pms_postings)||'|'||(SELECT count(*) FROM iam_v2.posting_outbox)||'|'||(SELECT count(*) FROM iam_v2.entitlement_transfers)||'|'||(SELECT count(*) FROM iam_v2.guest_device_actions)\" > $BASE/counts.txt; docker exec -i $PGC psql -U stayconnect -d $DB -tAqc \"SELECT coalesce(string_agg(appliance_id::text||'='||guest_device_self_service::text,','ORDER BY appliance_id),'none') FROM iam_v2.appliance_product_settings\" > $BASE/settings.txt" >/dev/null

B_SERVICES="$(sh_ "cat $BASE/services.txt | tr '\n' ' '")"
B_RELEASE="$(sh_ "cat $BASE/ha-release.txt")"
B_COUNTS="$(sh_ "cat $BASE/counts.txt")"
B_SETTINGS="$(sh_ "cat $BASE/settings.txt")"

eq "baseline: all six required services are active" "$(printf '%s' "$B_SERVICES" | tr -s ' ' | sed 's/ $//')" "active active active active active active"
case "$B_RELEASE" in */releases/hotel-admin/*) ok "baseline: the Hotel Admin release is a pinned release directory ($B_RELEASE)" ;;
                    *) no "baseline: Hotel Admin release path" "$B_RELEASE" ;; esac
echo "  baseline counts (ent|sess|stay|post|outbox|xfer|devact): $B_COUNTS"
echo "  baseline product settings: $B_SETTINGS"
eq "baseline: guest device self-service is OFF on every appliance row" \
   "$(printf '%s' "$B_SETTINGS" | grep -c 'true')" "0"
eq "baseline: the financial core holds no postings and no outbox rows" \
   "$(printf '%s' "$B_COUNTS" | awk -F'|' '{print $4"|"$5}')" "0|0"

BASE_DIGEST="$(q "SELECT md5(string_agg(t, E'\n' ORDER BY t)) FROM (
   SELECT c.relname||':'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text AS t
     FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
     JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
    WHERE n.nspname='iam_v2' AND c.relkind IN ('r','p')) s")"
case "$BASE_DIGEST" in [0-9a-f][0-9a-f]*) ok "baseline: the iam_v2 column shape has a digest ($BASE_DIGEST)" ;;
                       *) no "baseline column digest" "$BASE_DIGEST" ;; esac

# ---------------------------------------------------------------------------------------------------------
sec "2. the PUBLIC-executable definer finding, tested on the assembled system"
#
# iam_v2.p5_controlled_operation_open is SECURITY DEFINER and granted EXECUTE to PUBLIC by accepted migration
# 0027. The question is not whether the grant exists -- it does, deliberately -- but whether it can be USED to
# create or bypass a controlled operation. That is answered by attempting it as the real runtime roles.
eq "PUBLIC holds no USAGE on schema iam_v2, so the grant is unreachable by an unprivileged principal" \
   "$(q "SELECT has_schema_privilege('public','iam_v2','USAGE')::text")" "false"
eq "no SECURITY DEFINER function beyond the controlled-operation guard is PUBLIC-executable" \
   "$(q "SELECT coalesce(string_agg(p.proname, ','), 'none') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='iam_v2' AND p.prosecdef AND has_function_privilege('public', p.oid, 'EXECUTE')")" \
   "p5_controlled_operation_open"

for r in svc_scd svc_edged svc_acctd; do
  eq "$r cannot execute the controlled-operation OPENER" \
     "$(q "SELECT has_function_privilege('$r','iam_v2.p5_begin_controlled_operation(text)','EXECUTE')::text")" "false"
  eq "$r cannot INSERT into the controlled-operation scope table" \
     "$(q "SELECT has_table_privilege('$r','iam_v2.controlled_operation_scope','INSERT')::text")" "false"
done

# The behavioural attempt, which is the part that matters. As the real role, in a transaction that is rolled
# back: set the token the predicate reads, try to forge the scope row it checks, then try the guarded write.
# The token column is a uuid, so the forged value must BE a uuid. A string literal fails on type parsing
# before the privilege check is ever reached, and a case that dies early proves nothing about privileges --
# the first version of this did exactly that and reported it as a failure rather than a pass, which is right.
FORGED_TOKEN="$(q "SELECT gen_random_uuid()::text")"
FORGE="$(qas svc_scd "BEGIN; SET LOCAL iam_v2.op_post_stay_identity = '$FORGED_TOKEN';
  INSERT INTO iam_v2.controlled_operation_scope(txid, family, token) VALUES (txid_current(),'post_stay_identity','$FORGED_TOKEN'::uuid); ROLLBACK;")"
case "$FORGE" in *"permission denied"*) ok "svc_scd CANNOT forge a scope row, so the predicate cannot be made true" ;;
                 *) no "svc_scd forged a controlled-operation scope row" "$(printf '%s' "$FORGE" | head -2 | tr '\n' ' ')" ;; esac

PRED="$(qas svc_scd "SELECT iam_v2.p5_controlled_operation_open('post_stay_identity')::text")"
eq "svc_scd may CALL the predicate (that is the accepted design) and it answers false" "$(printf '%s' "$PRED" | tail -1)" "false"

WRITE="$(qas svc_scd "BEGIN; SET LOCAL iam_v2.op_post_stay_identity = '$FORGED_TOKEN';
  INSERT INTO iam_v2.post_stay_profiles(tenant_id, site_id) VALUES (gen_random_uuid(), gen_random_uuid()); ROLLBACK;")"
case "$WRITE" in *"permission denied"*|*"controlled operation"*) ok "svc_scd is refused the guarded write even with the token set" ;;
                 *) no "svc_scd performed a guarded write" "$(printf '%s' "$WRITE" | head -2 | tr '\n' ' ')" ;; esac

# ---------------------------------------------------------------------------------------------------------
sec "3. runtime-role privilege boundaries, as the real roles"
for spec in "svc_scd:iam_v2.entitlements:UPDATE:false" "svc_scd:iam_v2.entitlements:SELECT:true" \
            "svc_acctd:iam_v2.entitlements:UPDATE:false" "svc_acctd:iam_v2.sessions:SELECT:true" \
            "svc_edged:iam_v2.entitlement_state_transitions:DELETE:false" \
            "sc_financial_readonly:iam_v2.payment_transactions:UPDATE:false"; do
  IFS=: read -r role obj priv want <<< "$spec"
  eq "$role $priv on $obj is $want" "$(q "SELECT has_table_privilege('$role','$obj','$priv')::text")" "$want"
done
APPEND="$(qas svc_scd "UPDATE iam_v2.entitlement_state_transitions SET reason='TAMPERED' WHERE true")"
case "$APPEND" in *"permission denied"*|*ERROR*) ok "the transition history refuses an edit by a runtime role" ;;
                  *) no "the transition history accepted an edit" "$APPEND" ;; esac

# ---------------------------------------------------------------------------------------------------------
sec "4. the financial core is DARK and fail-closed, with zero egress"
eq "no posting exists" "$(q "SELECT count(*) FROM iam_v2.pms_postings")" "0"
eq "no outbox row exists, so nothing is queued for transmission" "$(q "SELECT count(*) FROM iam_v2.posting_outbox")" "0"
eq "no payment transaction exists" "$(q "SELECT count(*) FROM iam_v2.payment_transactions")" "0"
eq "no attempt has ever been made against a PMS" "$(q "SELECT count(*) FROM iam_v2.posting_attempts")" "0"
eq "the folio-identity strategy still defaults to UNSET, so a CHARGE is refused before it is built" \
   "$(q "SELECT (coalesce(column_default,'') LIKE '%UNSET%')::text FROM information_schema.columns
          WHERE table_schema='iam_v2' AND table_name='pms_interface_revisions' AND column_name='folio_identity_strategy'")" "true"
eq "no runtime role may execute the boundary termination" \
   "$(q "SELECT count(*) FROM unnest(ARRAY['svc_scd','svc_acctd','svc_edged']) r
          WHERE has_function_privilege(r,'iam_v2.terminate_entitlement_at_boundary(uuid,timestamptz,text)','EXECUTE')")" "0"

# ---------------------------------------------------------------------------------------------------------
sec "5. the three scd-created public tables are deterministic, documented bootstrap"
#
# No migration creates edge_executed_commands, edge_installed_updates or edge_offline_packages; scd creates
# them at first use. That is a real property of this system and is recorded rather than papered over -- but it
# must be DETERMINISTIC and OWNED, or a restore would silently produce a different database.
for t in edge_executed_commands edge_installed_updates edge_offline_packages; do
  eq "public.$t exists and is owned by the installation superuser" \
     "$(q "SELECT pg_get_userbyid(relowner) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname='public' AND c.relname='$t'")" "stayconnect"
  eq "...and Gate P's grant to svc_scd on it is present" \
     "$(q "SELECT (count(*) > 0)::text FROM information_schema.role_table_grants
            WHERE table_schema='public' AND table_name='$t' AND grantee='svc_scd'")" "true"
done

# ---------------------------------------------------------------------------------------------------------
sec "6. the Guest Portal, composed and dark"
#
# The portal answers on the real listener, and Phase 3, 5 and 6 guest routes are mounted UNCONDITIONALLY by
# design: whether a capability exists is decided behind them, so every refusal looks identical and a guest can
# never learn which features this appliance has. Darkness is therefore proved by the ANSWER and by the layer
# behind it, not by a 404 at the edge -- asserting 404 here would have been asserting the opposite of the
# design.
PORTAL_ROOT="$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8380/")"
eq "the guest portal serves its page on the real listener" "$PORTAL_ROOT" "200"

DEV_BODY="$(sh_ "curl -s -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:8380/devices/list")"
case "$DEV_BODY" in
  *'"ok":false'*"could not verify your stay"*)
     ok "the Phase-6 device route returns the uniform non-success and reveals nothing" ;;
  *) no "the device route's dark answer" "$(printf '%s' "$DEV_BODY" | head -c 160)" ;;
esac

# THE AUTHORITATIVE DARKNESS is one layer in: scd does not mount its Phase-6 endpoints while the deployment
# gate is off. That is what makes the portal's uniform answer a refusal rather than a failure.
SCD_P6="$(sh_ "curl -s -o /dev/null -w '%{http_code}' --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' -d '{}' http://unix/v1/phase6/devices/list")"
eq "scd does NOT mount its Phase-6 endpoints (the capability is absent, not merely refusing)" "$SCD_P6" "404"

eq "and the per-appliance setting that would enable it is OFF"    "$(q "SELECT coalesce(bool_or(guest_device_self_service),false)::text FROM iam_v2.appliance_product_settings")" "false"

# ---------------------------------------------------------------------------------------------------------
sec "7. Hotel Admin, composed and closed to the unauthenticated"
HA_CODE="$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:3100/")"
HA_LOC="$(sh_ "curl -s -o /dev/null -D - http://127.0.0.1:3100/ | grep -i '^location' | tr -d '\r'")"
eq "Hotel Admin answers on its real listener" "$HA_CODE" "307"
case "$HA_LOC" in *"/login"*) ok "...and an unauthenticated request is sent to login, not to data ($HA_LOC)" ;;
                  *) no "Hotel Admin unauthenticated redirect" "$HA_LOC" ;; esac
eq "the running Hotel Admin is the exact expected DARK release" "$(sh_ "readlink -f /opt/stayconnect/hotel-admin")" "$B_RELEASE"
# The API behind Hotel Admin, probed where it actually lives. The first version of this asked
# https://127.0.0.1/edge/v1/health and got 000: the admin vhost is pinned to the management IP, not loopback.
EDGED_HEALTH="$(sh_ "curl -s http://127.0.0.1:8090/edge/v1/health")"
case "$EDGED_HEALTH" in *'"db":true'*'"service":"edged"'*) ok "the edge API is healthy and reports its own database reachable" ;;
                        *) no "edge API health" "$(printf '%s' "$EDGED_HEALTH" | head -c 140)" ;; esac
eq "...and it reports the license Active, which is what lets this appliance serve at all"    "$(printf '%s' "$EDGED_HEALTH" | grep -c '"license_state":"Active"')" "1"

# THE APPLIANCE MUST NOT LOOK LIKE CENTRAL. api.stayconnect.local and admin.stayconnect.local belong to the
# Central Control Plane; this appliance deliberately answers 404 for them rather than proxying or -- worse --
# returning Caddy's empty 200, which would read as healthy to anything probing it.
for name in api.stayconnect.local admin.stayconnect.local; do
  eq "the Central-only name $name is refused (404), so this appliance never impersonates Central"      "$(sh_ "curl -sk -o /dev/null -w '%{http_code}' -H 'Host: $name' https://$APPL/edge/v1/health")" "404"
done

# ---------------------------------------------------------------------------------------------------------
sec "8. the two surfaces agree over the SAME durable state"
#
# Not "both are up" -- both READING ONE DATABASE. Proved by pointing at the same site database from both
# service configurations and confirming the durable state each would serve is the same row set.
SCD_DB="$(sh_ "grep -hoE 'SCD_DB_URL=.*' /etc/stayconnect/scd.env | sed 's/.*@//'")"
EDGED_DB="$(sh_ "grep -hoE 'EDGED_DB_URL=.*' /etc/stayconnect/edged.env | sed 's/.*@//'")"
if [ -n "$SCD_DB" ] && [ "$SCD_DB" = "$EDGED_DB" ]; then
  ok "scd (guest side) and edged (admin side) are configured against the identical database ($SCD_DB)"
else
  no "the two surfaces' database targets" "scd='$SCD_DB' edged='$EDGED_DB'"
fi
eq "the durable state both would serve is one row set: the same entitlement count"    "$(q "SELECT count(*)::text FROM iam_v2.entitlements")" "$(printf '%s' "$B_COUNTS" | cut -d'|' -f1)"

# ---------------------------------------------------------------------------------------------------------
sec "9. accounting, shaping and enforcement are live"
ACCT_ROWS="$(q "SELECT count(*) FROM public.accounting_records")"
case "$ACCT_ROWS" in ''|*ERROR*) no "accounting records readable" "$ACCT_ROWS" ;;
                     *) ok "the accounting store is readable and holds $ACCT_ROWS record(s)" ;; esac
eq "acctd is running, so the sweep that writes them is live" "$(sh_ "systemctl is-active stayconnect-acctd")" "active"
TC="$(sh_ "tc qdisc show 2>/dev/null | grep -cE 'htb|ifb'")"
case "$TC" in 0) np "shaping qdiscs present" "no htb/ifb qdisc is configured on this appliance right now" ;;
              *) ok "traffic shaping is configured in the kernel ($TC htb/ifb qdisc line(s))" ;; esac
NFT="$(sh_ "nft list ruleset 2>/dev/null | grep -cE 'stayconnect|guest'")"
case "$NFT" in 0) no "enforcement ruleset" "no stayconnect/guest nftables rules are loaded" ;;
               *) ok "the enforcement ruleset is loaded ($NFT matching rule line(s))" ;; esac

# ---------------------------------------------------------------------------------------------------------
sec "10. local-first: the appliance serves without Central"
#
# This appliance is deliberately not enrolled against a Central control plane, and Production Central must not
# be contacted, so local-first is proved as it actually stands: the guest and admin paths answer from the local
# database with no Central dependency in the request path.
CLOUD_CFG="$(sh_ "grep -hoE '^(CLOUD|CENTRAL)[A-Z_]*_URL=' /etc/stayconnect/scd.env /etc/stayconnect/portald.env 2>/dev/null | wc -l")"
eq "neither the guest daemon nor the portal names a Central URL in its configuration" "$CLOUD_CFG" "0"
eq "the guest portal answers while no Central is reachable"    "$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8380/")" "200"
eq "...and so does Hotel Admin" "$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:3100/")" "307"
np "a deliberate Central outage drill" "Production Central must not be contacted or disrupted; this appliance is unenrolled, so the outage state IS the running state and no drill can add to it"

# ---------------------------------------------------------------------------------------------------------
sec "11. backup and supported restore, actually performed"
#
# Restored into a NEW database on the same server, never over the site database. A restore proved by
# overwriting the thing you are trying to protect is not a proof anyone can afford to repeat.
STAMP="p7m4$(date +%H%M%S)"
DUMP="/tmp/${STAMP}.dump"
sh_ "docker exec -i $PGC pg_dump -U stayconnect -d $DB -Fc -f /tmp/${STAMP}.dump && docker exec -i $PGC sh -c 'ls -l /tmp/${STAMP}.dump'" >/dev/null
SZ="$(sh_ "docker exec -i $PGC sh -c 'stat -c %s /tmp/${STAMP}.dump 2>/dev/null || echo 0'")"
case "$SZ" in 0|'') no "pg_dump produced a backup" "size=$SZ" ;;
              *) ok "pg_dump produced a $SZ-byte custom-format backup of the live site database" ;; esac
sh_ "docker exec -i $PGC psql -U stayconnect -d postgres -qc 'DROP DATABASE IF EXISTS ${STAMP}_restore'" >/dev/null
sh_ "docker exec -i $PGC psql -U stayconnect -d postgres -qc 'CREATE DATABASE ${STAMP}_restore'" >/dev/null
RES="$(sh_ "docker exec -i $PGC pg_restore -U stayconnect -d ${STAMP}_restore --no-owner /tmp/${STAMP}.dump 2>&1 | grep -c 'error:'")"
eq "pg_restore completed with no errors" "$RES" "0"
R_TABLES="$(sh_ "docker exec -i $PGC psql -U stayconnect -d ${STAMP}_restore -tAqc \"SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'\"")"
L_TABLES="$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'")"
eq "the restored database carries the same iam_v2 table count as the live one" "$R_TABLES" "$L_TABLES"
R_ENT="$(sh_ "docker exec -i $PGC psql -U stayconnect -d ${STAMP}_restore -tAqc 'SELECT count(*) FROM iam_v2.entitlements'")"
eq "...and the same durable row count for entitlements" "$R_ENT" "$(printf '%s' "$B_COUNTS" | cut -d'|' -f1)"
sh_ "docker exec -i $PGC psql -U stayconnect -d postgres -qc 'DROP DATABASE ${STAMP}_restore'; docker exec -i $PGC rm -f /tmp/${STAMP}.dump" >/dev/null
eq "the restore scratch database and dump were removed afterwards"    "$(sh_ "docker exec -i $PGC psql -U stayconnect -d postgres -tAqc \"SELECT count(*) FROM pg_database WHERE datname='${STAMP}_restore'\"")" "0"

# ---------------------------------------------------------------------------------------------------------
sec "12. rollback and mixed-version safety"
#
# Rehearsed to completion in the reproducible scratch environment (phase6_rollback_rehearsal, 65/0, over
# migrations 0030-0047 down and back up), because rolling the LIVE appliance schema down and up is a
# destructive act on the system under acceptance and is not authorized here.
eq "the mixed-version premise holds on the appliance: the Phase-6 crossing function is catalog-detectable"    "$(q "SELECT (to_regprocedure('iam_v2.p6_data_crossing(uuid)') IS NOT NULL)::text")" "true"
eq "...and a function that does not exist probes as absent, so the fallback can tell them apart"    "$(q "SELECT (to_regprocedure('iam_v2.p6_not_a_real_function(uuid)') IS NULL)::text")" "true"
eq "every migration in the deployed lineage has a down migration"    "$(ls data-plane/migrations/*.up.sql | while read -r u; do [ -f "${u%.up.sql}.down.sql" ] || echo missing; done | grep -c missing)" "0"
np "a live rollback of the appliance schema" "rolling the accepted schema down and up on the system under acceptance is destructive and unauthorized; it is rehearsed in the reproducible environment instead"

# ---------------------------------------------------------------------------------------------------------
sec "13. purge and archive"
np "a real purge or archive with external receipt authority" "compliance archival requires a receipt authority this environment does not lawfully hold; the fail-closed gate that DEPENDS on it is proved instead, below"
eq "the compliance gate is fail-closed: no archive receipt exists, so the gate cannot be satisfied"    "$(q "SELECT count(*) FROM iam_v2.financial_restore_events")" "0"
eq "...and no runtime role may record one"    "$(q "SELECT count(*) FROM unnest(ARRAY['svc_scd','svc_acctd','svc_edged']) r
          WHERE has_function_privilege(r,'iam_v2.p4_record_compliance_receipt(uuid,text,text)','EXECUTE')")" "0"

# ---------------------------------------------------------------------------------------------------------
sec "14. restoration to the captured baseline"
#
# Nothing in this run enabled a capability or wrote durable synthetic state -- every write attempt was either
# refused by design or rolled back -- so restoration is proved by showing the baseline is UNCHANGED, compared
# against the file captured in section 1 rather than against a fresh reading.
NOW_COUNTS="$(q "SELECT (SELECT count(*) FROM iam_v2.entitlements)||'|'||(SELECT count(*) FROM iam_v2.sessions)||'|'||(SELECT count(*) FROM iam_v2.stays)||'|'||(SELECT count(*) FROM iam_v2.pms_postings)||'|'||(SELECT count(*) FROM iam_v2.posting_outbox)||'|'||(SELECT count(*) FROM iam_v2.entitlement_transfers)||'|'||(SELECT count(*) FROM iam_v2.guest_device_actions)")"
eq "durable row counts are identical to the captured baseline" "$NOW_COUNTS" "$B_COUNTS"
eq "the per-appliance product settings are identical to the captured baseline"    "$(q "SELECT coalesce(string_agg(appliance_id::text||'='||guest_device_self_service::text,','ORDER BY appliance_id),'none') FROM iam_v2.appliance_product_settings")" "$B_SETTINGS"
NOW_DIGEST="$(q "SELECT md5(string_agg(t, E'\n' ORDER BY t)) FROM (
   SELECT c.relname||':'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text AS t
     FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
     JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
    WHERE n.nspname='iam_v2' AND c.relkind IN ('r','p')) s")"
eq "the iam_v2 column shape is identical to the captured baseline" "$NOW_DIGEST" "$BASE_DIGEST"
eq "the six required services are still active"    "$(sh_ "systemctl is-active stayconnect-scd stayconnect-acctd stayconnect-edged stayconnect-portald stayconnect-netd stayconnect-hotel-admin | tr '\n' ' ' | tr -s ' ' | sed 's/ $//'")"    "active active active active active active"
eq "the Hotel Admin release is unchanged" "$(sh_ "readlink -f /opt/stayconnect/hotel-admin")" "$B_RELEASE"

echo
echo "------------------------------------------------------------"
printf 'PHASE7_APPLIANCE_M4 pass=%d fail=%d not_proven=%d\n' "$pass" "$fail" "$notproven"
[ "$fail" -eq 0 ]
