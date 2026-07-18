#!/usr/bin/env bash
# Phase-3 migration 0010 lifecycle + behaviour gate. DISPOSABLE PostgreSQL only; self-contained
# (creates a fresh loopback container, builds the accepted iam_v2 schema, runs the gate, tears down).
# No Production/appliance access. Proves the full Increment-2 hardening set (see docs/evidence gap audit).
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C=iamv2-scratch; DB=iam_scratch; PORT=55432
UP="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql"
DOWN="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.down.sql"
pass=0; fail=0
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
Qf(){ docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1; }
ok(){ echo "  [PASS] $1"; pass=$((pass+1)); }
no(){ echo "  [FAIL] $1"; fail=$((fail+1)); }
expect_err(){ local o; o="$(docker exec "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAqc "$1" 2>&1)"; echo "$o" | grep -qiE "ERROR|EXCEPTION"; }
expect_ok(){ local o; o="$(docker exec "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAqc "$1" 2>&1)"; if echo "$o" | grep -qiE "ERROR|EXCEPTION"; then echo "    (unexpected: $o)"; return 1; fi; return 0; }

FP="SELECT md5(string_agg(x, E'\n' ORDER BY x)) FROM (
  SELECT 'C '||table_name||'.'||column_name||':'||data_type||':'||is_nullable||':'||coalesce(column_default,'') AS x FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT 'T '||c.relname||'.'||t.tgname FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT 'I '||indexname||':'||indexdef FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT 'K '||conname||':'||pg_get_constraintdef(con.oid) FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2'
) s;"

echo '== setup: fresh disposable PG16 + accepted schema (mg1..mg9 + 0009) =='
docker rm -f "$C" >/dev/null 2>&1 || true
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" -p 127.0.0.1:$PORT:5432 postgres:16-alpine >/dev/null
for i in $(seq 1 30); do docker exec "$C" pg_isready -U postgres -d "$DB" >/dev/null 2>&1 && break; sleep 1; done
SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE bash "$ROOT/iam_v2_scratch/run.sh" fresh >/dev/null 2>&1
Q "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz DEFAULT now());" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0009_phase2_commerce.up.sql" >/dev/null 2>&1
[ "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2';")" = 49 ] && ok "accepted iam_v2 schema built (49 tables)" || no "schema build failed"

PRE="$(Q "$FP")"; echo "  pre-0010 catalog md5 = $PRE"

echo '== runner idempotency (scripts/edge-migrate.sh --only 0010, twice) =='
export EDGE_PSQL="docker exec -i $C psql -U postgres -d $DB -v ON_ERROR_STOP=1"
R1="$(bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution 2>&1)"; echo "$R1" | grep -q "apply 0010" && echo "$R1" | grep -q "EDGE_MIGRATE_OK applied=1" && ok "runner run#1 applied 0010" || no "runner run#1 did not apply"
POST="$(Q "$FP")"; echo "  post-0010 catalog md5 = $POST"
[ "$PRE" != "$POST" ] && ok "0010 changed the catalog" || no "0010 changed nothing"
R2="$(bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution 2>&1)"; echo "$R2" | grep -q "skip  0010" && echo "$R2" | grep -q "applied=0" && ok "runner run#2 skipped 0010 (idempotent no-op)" || no "runner run#2 not a no-op"
[ "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0010_phase3_stay_resolution';")" = 1 ] && ok "ledger has exactly one 0010 record" || no "ledger record count wrong"
[ "$POST" = "$(Q "$FP")" ] && ok "catalog unchanged between runner invocations" || no "catalog changed between runner runs"

echo '== raw re-apply must ERROR and roll back =='
RAW="$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP" 2>&1)"
echo "$RAW" | grep -qi "already exists" && ok "raw re-apply errors 'already exists'" || no "raw re-apply did not error"
[ "$POST" = "$(Q "$FP")" ] && ok "catalog unchanged after failed raw re-apply (rollback)" || no "catalog changed after raw re-apply"

echo '== expected objects + removals =='
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_runtime' AND column_name='derived_freshness';")" = 0 ] && ok "NO stored derived_freshness (§7)" || no "derived_freshness still present"
[ "$(Q "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='pms_interface_runtime_fresh';")" = 0 ] && ok "NO derived-freshness index (§7)" || no "derived index still present"
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='site_checkout_grace_config' AND column_name='grace_data_quota_bytes';")" = 1 ] && ok "grace quota stored in BYTES (§9)" || no "grace_data_quota_bytes missing"
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='site_checkout_grace_config' AND column_name='grace_data_quota_mb';")" = 0 ] && ok "NO grace_data_quota_mb (§9)" || no "grace_data_quota_mb still present"
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='stays' AND column_name='checkout_episode';")" = 0 ] && ok "NO stays.checkout_episode (episode = lifecycle_version)" || no "unexpected checkout_episode"
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='stay_events' AND column_name IN ('processed_at','review_code');")" = 2 ] && ok "stay_events result columns (processed_at, review_code)" || no "event result columns missing"

echo '== privilege hardening (§3) =='
[ "$(Q "SELECT has_function_privilege('public','iam_v2.p3_stay_lifecycle_guard()','EXECUTE');")" = f ] && ok "PUBLIC has NO EXECUTE on p3_stay_lifecycle_guard" || no "PUBLIC can execute lifecycle guard"
[ "$(Q "SELECT has_function_privilege('public','iam_v2.p3_stay_event_appendonly()','EXECUTE');")" = f ] && ok "PUBLIC has NO EXECUTE on p3_stay_event_appendonly" || no "PUBLIC can execute event guard"
[ "$(Q "SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND table_name='pms_interface_runtime' AND grantee <> current_user AND grantee <> 'PUBLIC';")" = 0 ] && ok "no non-owner grants on pms_interface_runtime (dark)" || no "unexpected runtime-table grants"
[ "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname LIKE 'p3_%' AND p.prosecdef;")" = 0 ] && ok "no SECURITY DEFINER on p3_* functions" || no "unexpected SECURITY DEFINER"

echo '== seed interface+revision (+2nd interface for cross-interface tests) =='
Q "DO \$\$DECLARE t uuid:=gen_random_uuid(); s uuid:=gen_random_uuid(); i uuid:=gen_random_uuid(); i2 uuid:=gen_random_uuid(); r uuid:=gen_random_uuid(); r2 uuid:=gen_random_uuid(); st uuid:=gen_random_uuid(); st2 uuid:=gen_random_uuid();
BEGIN
  INSERT INTO public.tenants(id) VALUES (t) ON CONFLICT DO NOTHING;
  INSERT INTO public.sites(id,tenant_id) VALUES (s,t) ON CONFLICT DO NOTHING;
  INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind) VALUES (i,t,s,'protel-fias'),(i2,t,s,'protel-fias');
  INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config) VALUES (r,t,s,i,1,'Africa/Cairo','{}'),(r2,t,s,i2,1,'Africa/Cairo','{}');
  INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version) VALUES (st,t,s,i,'R1','S1','RESERVED',1,0),(st2,t,s,i,'R2','S2','RESERVED',1,0);
  INSERT INTO iam_v2.pms_interface_runtime(tenant_id,site_id,pms_interface_id,pinned_revision_id) VALUES (t,s,i,r);
  INSERT INTO iam_v2.stay_events(id,tenant_id,site_id,pms_interface_id,stay_id,external_event_identity,event_type,pms_timestamp_raw,pms_timestamp_utc,source_timezone,sequence_version,normalization_version,clock_suspect,payload,processing_status) VALUES (gen_random_uuid(),t,s,i,NULL,'E1','GI','x',now(),'Africa/Cairo',1,1,false,'{}','PENDING');
END\$\$;" >/dev/null
T="$(Q "SELECT tenant_id FROM iam_v2.pms_interfaces LIMIT 1;")"; S="$(Q "SELECT site_id FROM iam_v2.pms_interfaces LIMIT 1;")"
I="$(Q "SELECT id FROM iam_v2.pms_interfaces ORDER BY id LIMIT 1;")"; I2="$(Q "SELECT id FROM iam_v2.pms_interfaces ORDER BY id OFFSET 1 LIMIT 1;")"
R="$(Q "SELECT id FROM iam_v2.pms_interface_revisions WHERE pms_interface_id='$I';")"; R2="$(Q "SELECT id FROM iam_v2.pms_interface_revisions WHERE pms_interface_id='$I2';")"
ST="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='S1';")"
[ -n "$ST" ] && ok "seed created stay $ST + 2 interfaces" || no "seed failed"

echo '== pms_interface_runtime constraints (§8) =='
expect_err "UPDATE iam_v2.pms_interface_runtime SET runtime_generation=-1 WHERE pms_interface_id='$I';" && ok "runtime_generation >= 0" || no "negative generation allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET transport_status='CONNECTED', pinned_revision_id=NULL WHERE pms_interface_id='$I';" && ok "CONNECTED requires pinned revision" || no "CONNECTED without revision allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET last_heartbeat_at=now()+interval '1 day' WHERE pms_interface_id='$I';" && ok "heartbeat cannot be after updated_at" || no "future heartbeat allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET resync_started_at=now() WHERE pms_interface_id='$I';" && ok "resync_started requires resync_requested" || no "incoherent resync allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET transport_error_code=repeat('x',201) WHERE pms_interface_id='$I';" && ok "error-code length bounded" || no "unbounded error code"
expect_ok  "UPDATE iam_v2.pms_interface_runtime SET transport_status='DISCONNECTED', continuity_status='GAP_DETECTED', sync_status='SYNC_FAILED', updated_at=now() WHERE pms_interface_id='$I';" && ok "four axes independently settable (no contradictory stored HEALTHY possible)" || no "axes not independent"

echo '== occupancy composite pin + all-or-none (§6) =='
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now() WHERE id='$ST';" && ok "partial occupancy tuple rejected (all-or-none)" || no "partial occupancy allowed"
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now(),occupancy_ingested_at=now(),occupancy_revision_id='$R',occupancy_normalization_version=0,occupancy_clock_suspect=false WHERE id='$ST';" && ok "occupancy normalization_version>0" || no "zero normalization allowed"
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now(),occupancy_ingested_at=now(),occupancy_revision_id='$R2',occupancy_normalization_version=1,occupancy_clock_suspect=false WHERE id='$ST';" && ok "cross-interface occupancy revision rejected (composite FK)" || no "cross-interface revision allowed"
expect_ok  "UPDATE iam_v2.stays SET occupancy_evidence_at=now(),occupancy_ingested_at=now(),occupancy_revision_id='$R',occupancy_normalization_version=1,occupancy_clock_suspect=false WHERE id='$ST';" && ok "full same-interface occupancy tuple allowed" || no "valid occupancy blocked"

echo '== lifecycle_version strict episode (§2) + status matrix (§4) =='
expect_ok  "UPDATE iam_v2.stays SET status='IN_HOUSE', last_applied_event_version=1 WHERE id='$ST';" && ok "RESERVED->IN_HOUSE allowed" || no "RESERVED->IN_HOUSE blocked"
expect_err "UPDATE iam_v2.stays SET lifecycle_version=lifecycle_version+1 WHERE id='$ST';" && ok "IN_HOUSE->IN_HOUSE + lifecycle++ rejected" || no "bare lifecycle++ allowed"
expect_err "UPDATE iam_v2.stays SET normalized_room_number='299', lifecycle_version=lifecycle_version+1 WHERE id='$ST';" && ok "Room Move + lifecycle++ rejected" || no "room-move ++ allowed"
expect_ok  "UPDATE iam_v2.stays SET normalized_room_number='299' WHERE id='$ST';" && ok "Room Move (no lifecycle change) allowed" || no "room-move blocked"
expect_ok  "UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id='$ST';" && ok "IN_HOUSE->CHECKED_OUT allowed" || no "checkout blocked"
expect_err "UPDATE iam_v2.stays SET last_applied_event_version=lifecycle_version, lifecycle_version=lifecycle_version+1 WHERE id='$ST';" && ok "CHECKED_OUT->CHECKED_OUT + lifecycle++ rejected" || no "checked-out ++ allowed"
expect_err "UPDATE iam_v2.stays SET status='POST_STAY_ACTIVE' WHERE id='$ST';" && ok "CHECKED_OUT->POST_STAY_ACTIVE rejected (Phase 5)" || no "post-stay transition allowed"
expect_err "UPDATE iam_v2.stays SET status='IN_HOUSE' WHERE id='$ST';" && ok "reinstatement without lifecycle++ rejected" || no "bare reinstatement version-static allowed"
expect_ok  "UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1, effective_checkout_at=NULL WHERE id='$ST';" && ok "reinstatement (structural-only guard) allows CHECKED_OUT->IN_HOUSE + lifecycle++; TRUST is enforced in the domain (increment 4), NOT by this trigger" || no "valid reinstatement blocked"
# POST_STAY_ACTIVE -> CHECKED_OUT rejected (seed a post-stay row directly, then try)
Q "INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version) VALUES (gen_random_uuid(),'$T','$S','$I','RP','SP','POST_STAY_ACTIVE',1,0);" >/dev/null
PS="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='SP';")"
expect_err "UPDATE iam_v2.stays SET status='CHECKED_OUT' WHERE id='$PS';" && ok "POST_STAY_ACTIVE->CHECKED_OUT rejected (Phase 5)" || no "post-stay exit allowed"

echo '== stay_events lineage (§5) =='
EV="$(Q "SELECT id FROM iam_v2.stay_events WHERE external_event_identity='E1';")"
ST2="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='S2';")"
XS="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='SP';")"
expect_err "UPDATE iam_v2.stay_events SET external_event_identity='E1x' WHERE id='$EV';" && ok "event identity immutable" || no "identity mutable"
expect_err "DELETE FROM iam_v2.stay_events WHERE id='$EV';" && ok "event DELETE rejected" || no "delete allowed"
expect_err "UPDATE iam_v2.stay_events SET stay_id='$ST' WHERE id='$EV';" && ok "PENDING stay_id set without terminal rejected" || no "pending stay_id set allowed"
# PENDING event cannot attach to cross-interface stay even when going terminal (I2 has no stay here; use a stay on I2)
Q "INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version) VALUES (gen_random_uuid(),'$T','$S','$I2','RX','SX','IN_HOUSE',1,0);" >/dev/null
SX="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='SX';")"
expect_err "UPDATE iam_v2.stay_events SET stay_id='$SX', processing_status='APPLIED' WHERE id='$EV';" && ok "PENDING->APPLIED with cross-interface Stay rejected" || no "cross-interface stay accepted"
expect_ok  "UPDATE iam_v2.stay_events SET stay_id='$ST2', processing_status='APPLIED', processed_at=now() WHERE id='$EV';" && ok "PENDING->APPLIED with same-interface Stay + processed_at allowed" || no "valid application blocked"
expect_err "UPDATE iam_v2.stay_events SET stay_id='$ST' WHERE id='$EV';" && ok "terminal event stay_id substitution rejected" || no "terminal repoint allowed"
expect_err "UPDATE iam_v2.stay_events SET stay_id=NULL WHERE id='$EV';" && ok "terminal event stay_id=NULL rejected" || no "terminal clear allowed"
expect_err "UPDATE iam_v2.stay_events SET processing_status='FAILED' WHERE id='$EV';" && ok "terminal processing_status immutable" || no "terminal status mutated"

echo '== grace vocabulary + bounds (§9) =='
expect_err "INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id,grace_device_limit_policy) VALUES ('$T','$S','ALLOW_UNTIL_LIMIT');" && ok "grace_device_limit_policy rejects non-canonical value" || no "non-canonical policy accepted"
expect_ok  "INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id,grace_device_limit_policy,grace_data_quota_bytes) VALUES ('$T','$S','REJECT_NEW_DEVICE',524288000);" && ok "canonical REJECT_NEW_DEVICE + byte quota accepted" || no "canonical policy rejected"
expect_err "UPDATE iam_v2.site_checkout_grace_config SET eligibility_window_seconds=0 WHERE tenant_id='$T';" && ok "grace eligibility_window must be >0" || no "grace bound not enforced"

echo '== rollback == pre and reapply == post =='
Qf < "$DOWN" >/dev/null && ok "rollback 0010 (down)" || no "rollback failed"
[ "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0010_phase3_stay_resolution';")" = 0 ] && ok "ledger 0010 removed on down" || no "ledger not cleared"
[ "$(Q "$FP")" = "$PRE" ] && ok "catalog after rollback == pre-0010" || no "rollback catalog != pre"
bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution >/dev/null 2>&1
[ "$(Q "$FP")" = "$POST" ] && ok "catalog after reapply == first post-0010" || no "reapply catalog != post"

echo '== teardown =='
docker rm -f "$C" >/dev/null 2>&1 && ok "disposable DB destroyed" || no "teardown failed"

echo "============================================================"
echo "PHASE3_0010_LIFECYCLE: pass=$pass fail=$fail  -> $([ $fail -eq 0 ] && echo PASS || echo FAIL)"
[ $fail -eq 0 ]
