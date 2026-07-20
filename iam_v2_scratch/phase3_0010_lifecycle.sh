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
# robust readiness: a real query must succeed (pg_isready can pass during initdb's transient server, which
# then restarts; running SQL in that window fails and can corrupt a mid-run gate on slow CI hosts).
for i in $(seq 1 60); do docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 && break; sleep 1; done
sleep 1
SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE bash "$ROOT/iam_v2_scratch/run.sh" fresh >/dev/null 2>&1
Q "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0009_phase2_commerce.up.sql" >/dev/null 2>&1
Q "INSERT INTO public.schema_migrations(version) VALUES ('0009_phase2_commerce') ON CONFLICT DO NOTHING;" >/dev/null
[ "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2';")" = 49 ] && ok "accepted iam_v2 schema built (49 tables)" || no "schema build failed"

# --- Part-A runner: mandatory positive-identity flags + helper ---
UPSHA="$(sha256sum "$UP" | awk '{print $1}')"
RUN(){ bash "$ROOT/scripts/edge-migrate.sh" --target-kind disposable --ack-target I_UNDERSTAND_DISPOSABLE_DATABASE "$@"; }
APPLY_ARGS=(--only 0010_phase3_stay_resolution --expect-db "$DB" --expect-sha256 "$UPSHA")

PRE="$(Q "$FP")"; echo "  pre-0010 catalog md5 = $PRE"

echo '== runner idempotency (scripts/edge-migrate.sh --only 0010, twice) =='
export EDGE_PSQL="docker exec -i $C psql -U postgres -d $DB -v ON_ERROR_STOP=1"
R1="$(RUN "${APPLY_ARGS[@]}" 2>&1)"; echo "$R1" | grep -q "apply 0010" && echo "$R1" | grep -q "EDGE_MIGRATE_OK applied=1" && ok "runner run#1 applied 0010 (positive-identity + pinned sha)" || { no "runner run#1 did not apply"; echo "$R1" | tail -3; }
POST="$(Q "$FP")"; echo "  post-0010 catalog md5 = $POST"
[ "$PRE" != "$POST" ] && ok "0010 changed the catalog" || no "0010 changed nothing"
R2="$(RUN "${APPLY_ARGS[@]}" 2>&1)"; echo "$R2" | grep -q "skip-after-lock 0010" && echo "$R2" | grep -q "applied=0" && ok "runner run#2 skipped 0010 (idempotent no-op)" || no "runner run#2 not a no-op"
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
  INSERT INTO iam_v2.pms_interface_secret_generations(id,tenant_id,site_id,pms_interface_id,generation_no,ciphertext,nonce,encryption_key_id,cipher_version) VALUES (gen_random_uuid(),t,s,i,1,'\x00'::bytea,'\x00'::bytea,gen_random_uuid(),1),(gen_random_uuid(),t,s,i2,1,'\x00'::bytea,'\x00'::bytea,gen_random_uuid(),1);
  INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version) VALUES (st,t,s,i,'R1','S1','RESERVED',1,0),(st2,t,s,i,'R2','S2','RESERVED',1,0);
  INSERT INTO iam_v2.pms_interface_runtime(tenant_id,site_id,pms_interface_id,pinned_revision_id) VALUES (t,s,i,r);
  INSERT INTO iam_v2.stay_events(id,tenant_id,site_id,pms_interface_id,stay_id,external_event_identity,event_type,pms_timestamp_raw,pms_timestamp_utc,source_timezone,sequence_version,normalization_version,clock_suspect,payload,processing_status) VALUES (gen_random_uuid(),t,s,i,NULL,'E1','GI','x',now(),'Africa/Cairo',1,1,false,'{}','PENDING');
END\$\$;" >/dev/null
# derive everything consistently from seeded stay S1 (runtime row + stays S1/S2 are all on interface I)
ST="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='S1';")"
T="$(Q "SELECT tenant_id FROM iam_v2.stays WHERE external_stay_identity='S1';")"
S="$(Q "SELECT site_id FROM iam_v2.stays WHERE external_stay_identity='S1';")"
I="$(Q "SELECT pms_interface_id FROM iam_v2.stays WHERE external_stay_identity='S1';")"
R="$(Q "SELECT id FROM iam_v2.pms_interface_revisions WHERE pms_interface_id='$I';")"
I2="$(Q "SELECT id FROM iam_v2.pms_interfaces WHERE id<>'$I' LIMIT 1;")"
R2="$(Q "SELECT id FROM iam_v2.pms_interface_revisions WHERE pms_interface_id='$I2';")"
SG="$(Q "SELECT id FROM iam_v2.pms_interface_secret_generations WHERE pms_interface_id='$I';")"
SG2="$(Q "SELECT id FROM iam_v2.pms_interface_secret_generations WHERE pms_interface_id='$I2';")"
[ -n "$ST" ] && [ -n "$I2" ] && [ -n "$R2" ] && [ -n "$SG" ] && ok "seed created stay $ST + 2 interfaces (I=$I I2=$I2) + secret gens" || no "seed failed"

echo '== pms_interface_runtime constraints (§8) + Secret-Generation pin (PART A §1) =='
expect_err "UPDATE iam_v2.pms_interface_runtime SET runtime_generation=-1 WHERE pms_interface_id='$I';" && ok "runtime_generation >= 0" || no "negative generation allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET transport_status='CONNECTED', pinned_revision_id=NULL WHERE pms_interface_id='$I';" && ok "CONNECTED requires pinned revision" || no "CONNECTED without revision allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET transport_status='CONNECTED', pinned_secret_generation_id=NULL, last_connected_at=now() WHERE pms_interface_id='$I';" && ok "CONNECTED requires pinned Secret Generation (§1)" || no "CONNECTED without secret gen allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET pinned_secret_generation_id='$SG2' WHERE pms_interface_id='$I';" && ok "cross-interface Secret Generation pin rejected (composite FK, §1)" || no "cross-interface secret gen accepted"
expect_ok  "UPDATE iam_v2.pms_interface_runtime SET transport_status='CONNECTED', pinned_secret_generation_id='$SG', last_connected_at=now(), updated_at=now() WHERE pms_interface_id='$I';" && ok "CONNECTED with both pins (revision + same-scope secret gen) allowed (§1)" || no "valid CONNECTED blocked"
expect_ok  "UPDATE iam_v2.pms_interface_runtime SET transport_status='DISCONNECTED', updated_at=now() WHERE pms_interface_id='$I';" && ok "historical row may retain a (now-superseded) secret-gen pin after disconnect (§1)" || no "post-connect state blocked"
expect_err "UPDATE iam_v2.pms_interface_runtime SET last_heartbeat_at=now()+interval '1 day' WHERE pms_interface_id='$I';" && ok "heartbeat cannot be after updated_at" || no "future heartbeat allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET resync_started_at=now() WHERE pms_interface_id='$I';" && ok "resync_started requires resync_requested" || no "incoherent resync allowed"
expect_err "UPDATE iam_v2.pms_interface_runtime SET transport_error_code=repeat('x',201) WHERE pms_interface_id='$I';" && ok "error-code length bounded" || no "unbounded error code"
expect_ok  "UPDATE iam_v2.pms_interface_runtime SET transport_status='DISCONNECTED', continuity_status='GAP_DETECTED', sync_status='SYNC_FAILED', updated_at=now() WHERE pms_interface_id='$I';" && ok "four axes independently settable (no contradictory stored HEALTHY possible)" || no "axes not independent"

echo '== occupancy composite pin + all-or-none (§6) =='
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now() WHERE id='$ST';" && ok "partial occupancy tuple rejected (all-or-none)" || no "partial occupancy allowed"
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now(),occupancy_ingested_at=now(),occupancy_revision_id='$R',occupancy_normalization_version=0,occupancy_clock_suspect=false WHERE id='$ST';" && ok "occupancy normalization_version>0" || no "zero normalization allowed"
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now(),occupancy_ingested_at=now(),occupancy_revision_id='$R2',occupancy_normalization_version=1,occupancy_clock_suspect=false WHERE id='$ST';" && ok "cross-interface occupancy revision rejected (composite FK)" || no "cross-interface revision allowed"
expect_ok  "UPDATE iam_v2.stays SET occupancy_evidence_at=now(),occupancy_ingested_at=now(),occupancy_revision_id='$R',occupancy_normalization_version=1,occupancy_clock_suspect=false,occupancy_evidence_version=1 WHERE id='$ST';" && ok "full same-interface occupancy tuple (0->1 evidence version) allowed" || no "valid occupancy blocked"

echo '== occupancy-evidence version MONOTONIC + exactly-once (§6b) =='
# dedicated fresh stay with NO occupancy (default evidence_version=0) so the transitions are unentangled.
Q "INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version) VALUES (gen_random_uuid(),'$T','$S','$I','REV','SEV','RESERVED',1,0);" >/dev/null
SEV="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='SEV';")"
# no-evidence coherence: version must be 0 while evidence absent
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_version=1 WHERE id='$SEV';" && ok "no authoritative evidence => version stays 0 (coherence + no-material-change)" || no "version bumped without evidence"
# evidence present with version 0 rejected (coherence + exactly-once)
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now(),occupancy_ingested_at=now(),occupancy_revision_id='$R',occupancy_normalization_version=1,occupancy_clock_suspect=false,occupancy_evidence_version=0 WHERE id='$SEV';" && ok "evidence present with version 0 rejected" || no "evidence+version0 accepted"
# valid initial 0->1
expect_ok  "UPDATE iam_v2.stays SET occupancy_evidence_at=now(),occupancy_ingested_at=now(),occupancy_revision_id='$R',occupancy_normalization_version=1,occupancy_clock_suspect=false,occupancy_evidence_version=1 WHERE id='$SEV';" && ok "valid initial evidence 0->1" || no "0->1 blocked"
# arbitrary jump rejected (material change but not +1)
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now()+interval '1 second',occupancy_evidence_version=99 WHERE id='$SEV';" && ok "arbitrary version jump (1->99) rejected" || no "jump accepted"
# evidence mutation WITHOUT the version transition rejected
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now()+interval '1 second' WHERE id='$SEV';" && ok "material evidence mutation without version++ rejected" || no "silent evidence mutation accepted"
# valid subsequent N->N+1 (material change + exactly +1)
expect_ok  "UPDATE iam_v2.stays SET occupancy_evidence_at=now()+interval '1 second',occupancy_evidence_version=2 WHERE id='$SEV';" && ok "valid subsequent evidence update 1->2" || no "N->N+1 blocked"
# duplicate reapplication: identical material, only ingested_at metadata refreshed => NO uncontrolled increment
expect_ok  "UPDATE iam_v2.stays SET occupancy_ingested_at=now()+interval '5 seconds' WHERE id='$SEV';" && ok "duplicate reapplication (ingested_at refresh only) keeps version (no uncontrolled increment)" || no "metadata refresh blocked"
[ "$(Q "SELECT occupancy_evidence_version FROM iam_v2.stays WHERE id='$SEV';")" = 2 ] && ok "version unchanged at 2 after duplicate reapplication" || no "duplicate reapplication changed version"
# bumping version on a duplicate (no material change) rejected
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_version=3 WHERE id='$SEV';" && ok "version bump without material change rejected" || no "uncontrolled increment accepted"
# decrease rejected
expect_err "UPDATE iam_v2.stays SET occupancy_evidence_at=now()+interval '2 seconds',occupancy_evidence_version=1 WHERE id='$SEV';" && ok "version decrease (2->1) rejected" || no "decrease accepted"

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

echo '== stay_events composite FK proof (§1 - already in base mg4, not duplicated) =='
[ "$(Q "SELECT count(*) FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND c.relname='stay_events' AND con.contype='f' AND pg_get_constraintdef(con.oid) LIKE '%(tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays%';")" = 1 ] && ok "base composite FK stay_events(tenant,site,interface,stay)->stays exists (structural cross-interface protection)" || no "composite FK missing"

echo '== stay_events INSERT append-first (§1) =='
# NEV <stay_id-sql> <ext-identity> <STATUS> <processed_at-sql> <review_code-sql>
NEV(){ echo "INSERT INTO iam_v2.stay_events(id,tenant_id,site_id,pms_interface_id,stay_id,external_event_identity,event_type,pms_timestamp_raw,pms_timestamp_utc,source_timezone,sequence_version,normalization_version,clock_suspect,payload,processing_status,processed_at,review_code) VALUES (gen_random_uuid(),'$T','$S','$I',$1,'$2','GI','x',now(),'Africa/Cairo',1,1,false,'{}','$3',$4,$5);"; }
expect_err "$(NEV NULL EA APPLIED NULL NULL)" && ok "INSERT directly as APPLIED rejected" || no "insert APPLIED allowed"
expect_err "$(NEV NULL EF FAILED NULL NULL)" && ok "INSERT directly as FAILED rejected" || no "insert FAILED allowed"
expect_err "$(NEV "'$ST'" EP PENDING NULL NULL)" && ok "INSERT PENDING with stay_id rejected" || no "insert pending+stay_id allowed"
expect_err "$(NEV NULL EP2 PENDING now\(\) NULL)" && ok "INSERT PENDING with processed_at rejected" || no "insert pending+processed_at allowed"
expect_err "$(NEV NULL EP3 PENDING NULL \'X\')" && ok "INSERT PENDING with review_code rejected" || no "insert pending+review_code allowed"
expect_ok  "$(NEV NULL EOK PENDING NULL NULL)" && ok "INSERT clean PENDING accepted" || no "clean pending insert blocked"

echo '== stay_events terminal-result invariants (§2/§5) =='
EV="$(Q "SELECT id FROM iam_v2.stay_events WHERE external_event_identity='E1';")"
ST2="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='S2';")"
Q "INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version) VALUES (gen_random_uuid(),'$T','$S','$I2','RX','SX','IN_HOUSE',1,0);" >/dev/null
SX="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='SX';")"
expect_err "UPDATE iam_v2.stay_events SET id=gen_random_uuid() WHERE id='$EV';" && ok "event row id (primary identity) immutable (PART A §2)" || no "event id mutable"
expect_err "UPDATE iam_v2.stay_events SET external_event_identity='E1x' WHERE id='$EV';" && ok "event identity immutable" || no "identity mutable"
expect_err "DELETE FROM iam_v2.stay_events WHERE id='$EV';" && ok "event DELETE rejected" || no "delete allowed"
expect_err "UPDATE iam_v2.stay_events SET stay_id='$ST' WHERE id='$EV';" && ok "PENDING stay_id set without terminal rejected" || no "pending stay_id set allowed"
expect_err "UPDATE iam_v2.stay_events SET processing_status='APPLIED' WHERE id='$EV';" && ok "PENDING->APPLIED without stay_id rejected" || no "applied w/o stay_id allowed"
expect_err "UPDATE iam_v2.stay_events SET processing_status='APPLIED', stay_id='$ST2' WHERE id='$EV';" && ok "PENDING->APPLIED without processed_at rejected" || no "applied w/o processed_at allowed"
expect_err "UPDATE iam_v2.stay_events SET stay_id='$SX', processing_status='APPLIED', processed_at=now() WHERE id='$EV';" && ok "PENDING->APPLIED cross-interface Stay rejected (FK + trigger)" || no "cross-interface stay accepted"
expect_err "UPDATE iam_v2.stay_events SET processing_status='MANUAL_REVIEW', processed_at=now() WHERE id='$EV';" && ok "MANUAL_REVIEW without review_code rejected" || no "manual-review w/o code allowed"
expect_err "UPDATE iam_v2.stay_events SET processing_status='FAILED', processed_at=now(), review_code='room 101 guest smith' WHERE id='$EV';" && ok "free-text/PII-shaped review_code rejected" || no "PII review code allowed"
expect_err "UPDATE iam_v2.stay_events SET processing_status='APPLIED', stay_id='$ST2', processed_at=now(), review_code='OKCODE' WHERE id='$EV';" && ok "APPLIED must not carry review_code" || no "applied+review_code allowed"
expect_ok  "UPDATE iam_v2.stay_events SET stay_id='$ST2', processing_status='APPLIED', processed_at=now() WHERE id='$EV';" && ok "valid same-interface PENDING->APPLIED + processed_at accepted" || no "valid application blocked"
expect_err "UPDATE iam_v2.stay_events SET stay_id='$ST' WHERE id='$EV';" && ok "terminal stay_id substitution rejected" || no "terminal repoint allowed"
expect_err "UPDATE iam_v2.stay_events SET stay_id=NULL WHERE id='$EV';" && ok "terminal stay_id=NULL rejected" || no "terminal clear allowed"
expect_err "UPDATE iam_v2.stay_events SET processed_at=now() WHERE id='$EV';" && ok "terminal processed_at change rejected" || no "terminal processed_at mutated"
expect_err "UPDATE iam_v2.stay_events SET review_code='LATER' WHERE id='$EV';" && ok "terminal review_code change rejected" || no "terminal review_code mutated"
expect_err "UPDATE iam_v2.stay_events SET processing_status='FAILED' WHERE id='$EV';" && ok "terminal status change rejected" || no "terminal status mutated"
# MANUAL_REVIEW / FAILED happy paths on fresh events
E2="$(Q "SELECT id FROM iam_v2.stay_events WHERE external_event_identity='EOK';")"
expect_ok  "UPDATE iam_v2.stay_events SET processing_status='MANUAL_REVIEW', processed_at=now(), review_code='AMBIGUOUS_LOCAL' WHERE id='$E2';" && ok "MANUAL_REVIEW + bounded review_code accepted" || no "manual-review blocked"

echo '== grace device-policy (§4) + all-or-none (§5) + bounds (§9) =='
expect_err "INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id,grace_duration_seconds,grace_down_kbps,grace_up_kbps,grace_data_quota_bytes,grace_device_limit,grace_device_limit_policy) VALUES ('$T','$S',3600,5000,2000,524288000,2,'DISCONNECT_OLDEST');" && ok "grace DISCONNECT_OLDEST rejected (§4)" || no "DISCONNECT_OLDEST accepted"
expect_err "INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id,grace_duration_seconds,grace_down_kbps,grace_up_kbps,grace_data_quota_bytes,grace_device_limit,grace_device_limit_policy) VALUES ('$T','$S',3600,5000,2000,524288000,2,'ADMIN_APPROVAL');" && ok "grace ADMIN_APPROVAL rejected (§4)" || no "ADMIN_APPROVAL accepted"
expect_err "INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id,grace_data_quota_bytes,grace_device_limit_policy) VALUES ('$T','$S',524288000,'REJECT_NEW_DEVICE');" && ok "partial grace policy rejected (all-or-none)" || no "partial grace accepted"
expect_ok  "INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id,grace_duration_seconds,grace_down_kbps,grace_up_kbps,grace_data_quota_bytes,grace_device_limit,grace_device_limit_policy) VALUES ('$T','$S',3600,5000,2000,524288000,2,'REJECT_NEW_DEVICE');" && ok "fully-configured grace (REJECT_NEW_DEVICE, bytes) accepted" || no "full grace rejected"
expect_ok  "INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id) VALUES (gen_random_uuid(),gen_random_uuid());" && ok "unconfigured grace (all policy NULL, default window) accepted" || no "unconfigured grace rejected"
expect_err "INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id,config) VALUES (gen_random_uuid(),gen_random_uuid(),'{\"grace_duration_seconds\":3600}'::jsonb);" && ok "config jsonb duplicate authoritative key rejected (§5)" || no "jsonb dup key accepted"
expect_err "UPDATE iam_v2.site_checkout_grace_config SET eligibility_window_seconds=0 WHERE eligibility_window_seconds=86400;" && ok "grace eligibility_window must be >0" || no "grace bound not enforced"

echo '== checkout_grace_audit append-only + one-per-episode + coherence + boundary/config provenance (§4c/§11) =='
# CGA <episode> <trigger> <is_emergency> <policy_version> <alert_code|NULL> <reason_code> <grace_ent|NULL> <boundary_reason>
CGA(){ echo "INSERT INTO iam_v2.checkout_grace_audit(tenant_id,site_id,pms_interface_id,stay_id,lifecycle_version,trigger,is_emergency,policy_version,alert_code,reason_code,grace_entitlement_id,boundary_reason_code,config_version,boundary_at) VALUES ('$T','$S','$I','$ST',$1,'$2',$3,'$4',$5,'$6',$7,'$8',1,now());"; }
# cga_coherent (§11): grace triggers require a grace_entitlement_id + matching policy version; NO_GRACE requires none
expect_err "$(CGA 5 CHECKOUT_GRACE false CHECKOUT_GRACE_V1 NULL ELIGIBLE NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "CHECKOUT_GRACE without grace_entitlement_id rejected (§11)" || no "grace w/o entitlement accepted"
expect_err "$(CGA 5 EMERGENCY_GRACE false EMERGENCY_GRACE_V1 \'CHECKOUT_GRACE_CONFIG_INVALID\' ELIGIBLE NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "EMERGENCY_GRACE with is_emergency=false rejected (§11)" || no "emergency non-emergency accepted"
expect_err "$(CGA 5 NO_GRACE false CHECKOUT_GRACE_V1 NULL X NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "NO_GRACE with non-NONE policy_version rejected (§11)" || no "no-grace wrong policy accepted"
expect_err "$(CGA 5 NO_GRACE true NONE NULL X NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "NO_GRACE with is_emergency=true rejected (§11)" || no "no-grace emergency accepted"
expect_err "$(CGA 5 NO_GRACE false NONE \'CHECKOUT_GRACE_CONFIG_INVALID\' X NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "NO_GRACE with alert_code rejected (§11)" || no "no-grace alert accepted"
# bounded machine codes + episode + boundary provenance
expect_err "$(CGA 5 NO_GRACE false none NULL X NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "lowercase policy_version rejected (bounded machine code)" || no "unbounded policy_version accepted"
expect_err "$(CGA 5 NO_GRACE false NONE NULL 'room 101' NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "free-text reason_code rejected (no PII shape)" || no "PII reason accepted"
expect_err "$(CGA 5 NO_GRACE false NONE NULL X NULL 'server clock')" && ok "free-text boundary_reason_code rejected (bounded)" || no "unbounded boundary reason accepted"
expect_err "$(CGA 0 NO_GRACE false NONE NULL NO_ACTIVE_ENTITLEMENT NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "lifecycle_version>0 enforced" || no "zero episode accepted"
# valid NO_GRACE row (grace_entitlement NULL, policy NONE, bounded reason + boundary reason + config_version)
expect_ok  "$(CGA 5 NO_GRACE false NONE NULL NO_ACTIVE_ENTITLEMENT NULL TRUSTED_PMS_CHECKOUT_TS)" && ok "valid NO_GRACE audit row (provenance + config version) accepted" || no "valid audit rejected"
expect_err "$(CGA 5 NO_GRACE false NONE NULL NO_ACTIVE_ENTITLEMENT NULL EVENT_CLOCK_SUSPECT)" && ok "second audit for same (stay,episode) rejected (one-per-episode)" || no "duplicate episode audit accepted"
CGAID="$(Q "SELECT id FROM iam_v2.checkout_grace_audit WHERE stay_id='$ST' AND lifecycle_version=5;")"
expect_err "UPDATE iam_v2.checkout_grace_audit SET reason_code='TAMPERED' WHERE id='$CGAID';" && ok "audit UPDATE rejected (append-only)" || no "audit mutable"
expect_err "DELETE FROM iam_v2.checkout_grace_audit WHERE id='$CGAID';" && ok "audit DELETE rejected (append-only)" || no "audit deletable"
# active_operational_alerts view surfaces only alert-bearing rows (this NO_GRACE row has none)
[ "$(Q "SELECT count(*) FROM iam_v2.active_operational_alerts WHERE stay_id='$ST';")" = 0 ] && ok "NO_GRACE row not surfaced as an operational alert (§11 view)" || no "no-grace surfaced as alert"

echo '== reserved Emergency-Grace namespace + bootstrap/health (§4g) =='
RT="$(Q "SELECT tenant_id FROM iam_v2.stays WHERE id='$ST';")"; RS="$(Q "SELECT site_id FROM iam_v2.stays WHERE id='$ST';")"
expect_err "INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,is_system) VALUES ('$RT','$RS','__sys_emergency_grace_pkg__',false);" && ok "non-system reserved-code package rejected (§7)" || no "non-system reserved package accepted"
[ "$(Q "SELECT iam_v2.emergency_grace_health('$RT','$RS');")" = "EMERGENCY_GRACE_CATALOG_ABSENT" ] && ok "health reports ABSENT before bootstrap (§6)" || no "health not absent pre-bootstrap"
Q "SELECT iam_v2.bootstrap_emergency_grace('$RT','$RS');" >/dev/null
[ "$(Q "SELECT iam_v2.emergency_grace_health('$RT','$RS');")" = "OK" ] && ok "bootstrap provisions canonical catalog; health OK (§6)" || no "health not OK after bootstrap"
Q "SELECT iam_v2.bootstrap_emergency_grace('$RT','$RS');" >/dev/null
[ "$(Q "SELECT iam_v2.emergency_grace_health('$RT','$RS');")" = "OK" ] && ok "bootstrap is idempotent (re-run stays OK)" || no "idempotent bootstrap broke health"
expect_err "DELETE FROM iam_v2.internet_packages WHERE tenant_id='$RT' AND site_id='$RS' AND code='__sys_emergency_grace_pkg__';" && ok "reserved system package delete rejected (§7)" || no "reserved package deletable"
expect_err "DELETE FROM iam_v2.service_plans WHERE tenant_id='$RT' AND site_id='$RS' AND code='__sys_emergency_grace_plan__';" && ok "reserved system plan delete rejected (§7)" || no "reserved plan deletable"

echo '== structural checkout boundary invariants (§10) =='
Q "INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version) VALUES (gen_random_uuid(),'$T','$S','$I','RB','SB','IN_HOUSE',1,0);" >/dev/null
SB="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='SB';")"
expect_err "UPDATE iam_v2.stays SET status='CHECKED_OUT' WHERE id='$SB';" && ok "CHECKED_OUT without effective_checkout_at rejected (§10)" || no "checkout without boundary accepted"
expect_ok  "UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now(), posting_allowed=false WHERE id='$SB';" && ok "checkout sets boundary + posting off (§10)" || no "valid checkout blocked"
expect_err "UPDATE iam_v2.stays SET effective_checkout_at=now()+interval '1 hour' WHERE id='$SB';" && ok "effective_checkout_at immutable within episode (§10)" || no "boundary moved within episode"
expect_err "UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1 WHERE id='$SB';" && ok "reinstatement without clearing boundary rejected (§10)" || no "reinstate kept boundary"
expect_ok  "UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1, effective_checkout_at=NULL WHERE id='$SB';" && ok "reinstatement clears boundary + bumps episode (§10)" || no "valid reinstate blocked"

echo '== entitlement history append-only (§4d/§4e) =='
[ "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_name IN ('entitlement_state_transitions','entitlement_device_authorizations');")" = 2 ] && ok "append-only history tables present" || no "history tables missing"

echo '== runner scope + mandatory positive-identity (PART A §1/§2/§4/§8) =='
o="$(bash "$ROOT/scripts/edge-migrate.sh" 2>&1 || true)"; echo "$o" | grep -q "REFUSED: EDGE_PSQL" || echo "$o" | grep -q "REFUSED: specify --only" && ok "runner refuses without scope" || no "runner ran with no scope"
o="$(RUN --only 'BAD NAME' --expect-db "$DB" --expect-sha256 "$UPSHA" 2>&1 || true)"; echo "$o" | grep -q "does not match" && ok "runner rejects invalid version name" || no "runner accepted bad name"
o="$(RUN --only 9999_absent_migration --expect-db "$DB" --expect-sha256 "$UPSHA" 2>&1 || true)"; echo "$o" | grep -q "resolves to 0 files" && ok "runner rejects absent migration" || no "runner accepted absent migration"
# §1/§8: mandatory identity params
o="$(bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution --target-kind disposable --ack-target I_UNDERSTAND_DISPOSABLE_DATABASE --expect-sha256 "$UPSHA" 2>&1 || true)"; echo "$o" | grep -q "REFUSED: --expect-db is mandatory" && ok "missing --expect-db refused (§1)" || no "missing --expect-db accepted"
o="$(bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution --expect-db "$DB" --expect-sha256 "$UPSHA" 2>&1 || true)"; echo "$o" | grep -q "target-kind" && ok "missing --target-kind refused (§1)" || no "missing target-kind accepted"
o="$(bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution --expect-db "$DB" --target-kind disposable --expect-sha256 "$UPSHA" 2>&1 || true)"; echo "$o" | grep -q "REFUSED: --ack-target is mandatory" && ok "missing --ack-target refused (§1)" || no "missing ack accepted"
o="$(bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution --expect-db "$DB" --target-kind disposable --ack-target WRONG_ACK --expect-sha256 "$UPSHA" 2>&1 || true)"; echo "$o" | grep -q "does not match target-kind" && ok "wrong --ack-target refused (§1)" || no "wrong ack accepted"
o="$(RUN --only 0010_phase3_stay_resolution --expect-db "$DB" 2>&1 || true)"; echo "$o" | grep -q "expect-sha256 is mandatory" && ok "missing --expect-sha256 refused (§4)" || no "missing sha accepted"
o="$(RUN --only 0010_phase3_stay_resolution --expect-db "$DB" --expect-sha256 deadbeef 2>&1 || true)"; echo "$o" | grep -q "checksum mismatch" && echo "$o" | grep -q "expected(--expect-sha256): deadbeef" && ok "checksum mismatch refused + prints expected/actual (§4)" || no "checksum mismatch accepted"
o="$(bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution --expect-db WRONGDB --target-kind disposable --ack-target I_UNDERSTAND_DISPOSABLE_DATABASE --expect-sha256 "$UPSHA" 2>&1 || true)"; echo "$o" | grep -q "but --expect-db" && ok "--expect-db mismatch refused (§1)" || no "expect-db mismatch accepted"
# §2: noncanonical directory rejected without disposable ack
o="$(RUN --only 0010_phase3_stay_resolution --expect-db "$DB" --expect-sha256 "$UPSHA" --dir /tmp 2>&1 || true)"; echo "$o" | grep -q "requires target-kind=disposable AND --ack-noncanonical-dir" && ok "noncanonical --dir refused without ack (§2)" || no "noncanonical dir accepted"
# §2: noncanonical/escaped dir (outside the repo) yields no migration even WITH the disposable dir-ack
ESC="${TMPDIR:-/tmp}/p3_noncanon_dir.$$"; rm -rf "$ESC"; mkdir -p "$ESC"
o="$(RUN --only 0010_phase3_stay_resolution --expect-db "$DB" --expect-sha256 "$UPSHA" --dir "$ESC" --ack-noncanonical-dir I_UNDERSTAND_NONCANONICAL_TEST_DIR 2>&1 || true)"; echo "$o" | grep -qE "resolves to 0 files|migration directory missing" && ok "noncanonical dir outside repo yields no migration (§2)" || no "escaped dir found a migration"
rmdir "$ESC" 2>/dev/null || true

echo '== rollback == pre, then CONCURRENT two-runner reapply (PART A §3) =='
Qf < "$DOWN" >/dev/null && ok "rollback 0010 (down)" || no "rollback failed"
[ "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0010_phase3_stay_resolution';")" = 0 ] && ok "ledger 0010 removed on down" || no "ledger not cleared"
[ "$(Q "$FP")" = "$PRE" ] && ok "catalog after rollback == pre-0010" || no "rollback catalog != pre"
# two real runner processes race the same fresh (post-rollback) state; the atomic lock-then-ledger
# design must serialize them: exactly one APPLIES, the other reports SKIP_AFTER_LOCK, one ledger row.
TMP="${TMPDIR:-/tmp}"; O1="$TMP/p3run1.$$"; O2="$TMP/p3run2.$$"
RUN "${APPLY_ARGS[@]}" >"$O1" 2>&1 & P1=$!
RUN "${APPLY_ARGS[@]}" >"$O2" 2>&1 & P2=$!
wait $P1; E1=$?; wait $P2; E2=$?
[ "$E1" = 0 ] && [ "$E2" = 0 ] && ok "both concurrent runners exit 0" || no "a concurrent runner failed (e1=$E1 e2=$E2)"
[ "$(cat "$O1" "$O2" | grep -c 'apply 0010_phase3_stay_resolution (under lock)')" = 1 ] && ok "exactly one runner applied under lock" || no "apply count != 1"
[ "$(cat "$O1" "$O2" | grep -c 'skip-after-lock 0010')" = 1 ] && ok "exactly one runner reported skip-after-lock" || no "skip-after-lock count != 1"
[ "$(cat "$O1" "$O2" | grep -ci 'already exists')" = 0 ] && ok "no 'already exists' (no partial DDL / no pre-lock race)" || no "'already exists' seen (racy DDL)"
[ "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0010_phase3_stay_resolution';")" = 1 ] && ok "exactly one ledger row after concurrent apply" || no "ledger row count != 1"
[ "$(Q "$FP")" = "$POST" ] && ok "catalog after concurrent reapply == first post-0010 (no partial DDL)" || no "concurrent reapply catalog != post"
rm -f "$O1" "$O2"

echo '== ledger structural verification + separated bootstrap (PART A §3/§5/§8) =='
# unexpected ledger column type: version made nullable -> refused
Q "CREATE TABLE public.sm_bad(version text, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
Q "ALTER TABLE public.schema_migrations RENAME TO schema_migrations_ok; ALTER TABLE public.sm_bad RENAME TO schema_migrations;" >/dev/null
o="$(RUN "${APPLY_ARGS[@]}" 2>&1 || true)"; echo "$o" | grep -qE "version' must be text NOT NULL|is not the PRIMARY KEY" && ok "unexpected ledger structure refused (§3)" || no "bad ledger structure accepted"
Q "DROP TABLE public.schema_migrations; ALTER TABLE public.schema_migrations_ok RENAME TO schema_migrations;" >/dev/null
# unexpected ledger owner -> refused
Q "DROP ROLE IF EXISTS rogue_owner; CREATE ROLE rogue_owner NOLOGIN; ALTER TABLE public.schema_migrations OWNER TO rogue_owner;" >/dev/null
o="$(RUN "${APPLY_ARGS[@]}" 2>&1 || true)"; echo "$o" | grep -q "ledger owner 'rogue_owner' not in allowlist" && ok "unexpected ledger owner refused (§3)" || no "bad ledger owner accepted"
Q "ALTER TABLE public.schema_migrations OWNER TO postgres;" >/dev/null
# missing 0009 baseline before 0010 -> refused
Q "DELETE FROM public.schema_migrations WHERE version='0009_phase2_commerce';" >/dev/null
o="$(RUN "${APPLY_ARGS[@]}" 2>&1 || true)"; echo "$o" | grep -q "baseline 0009_phase2_commerce must be applied before" && ok "missing 0009 baseline refused (§3)" || no "0010 allowed without 0009 baseline"
Q "INSERT INTO public.schema_migrations(version) VALUES ('0009_phase2_commerce') ON CONFLICT DO NOTHING;" >/dev/null
# ledger absent -> refused (no silent create); bootstrap standalone; bootstrap cannot combine with --only
Q "ALTER TABLE public.schema_migrations RENAME TO schema_migrations_bak;" >/dev/null
o="$(RUN "${APPLY_ARGS[@]}" 2>&1 || true)"; echo "$o" | grep -q "ledger absent" && ok "ledger absent refused in normal mode (no silent create, §5)" || no "normal mode silently proceeded"
o="$(bash "$ROOT/scripts/edge-migrate.sh" --bootstrap-ledger --only 0010_phase3_stay_resolution --expect-db "$DB" --target-kind disposable --ack-target I_UNDERSTAND_LEDGER_BOOTSTRAP --bootstrap-owner postgres 2>&1 || true)"; echo "$o" | grep -q "cannot be combined with --only" && ok "bootstrap combined with --only refused (§5)" || no "bootstrap+only accepted"
o="$(bash "$ROOT/scripts/edge-migrate.sh" --bootstrap-ledger --expect-db "$DB" --target-kind bogus_kind --ack-target I_UNDERSTAND_LEDGER_BOOTSTRAP --bootstrap-owner postgres 2>&1 || true)"; echo "$o" | grep -q "target-kind must be 'disposable' or 'live-site'" && ok "bootstrap arbitrary --target-kind refused (§5)" || no "bootstrap accepted arbitrary target-kind"
# §4 bootstrap-owner hardening
o="$(bash "$ROOT/scripts/edge-migrate.sh" --bootstrap-ledger --expect-db "$DB" --target-kind disposable --ack-target I_UNDERSTAND_LEDGER_BOOTSTRAP --bootstrap-owner 'evil; DROP TABLE x' 2>&1 || true)"; echo "$o" | grep -q "not a valid role identifier" && ok "SQL-shaped bootstrap owner refused (§4)" || no "SQL-shaped owner accepted"
o="$(LEDGER_OWNER_ALLOWLIST='ghost_role postgres' bash "$ROOT/scripts/edge-migrate.sh" --bootstrap-ledger --expect-db "$DB" --target-kind disposable --ack-target I_UNDERSTAND_LEDGER_BOOTSTRAP --bootstrap-owner ghost_role 2>&1 || true)"; echo "$o" | grep -q "does not exist" && ok "nonexistent bootstrap owner (allowlisted but absent) refused (§4)" || no "nonexistent owner accepted"
o="$(bash "$ROOT/scripts/edge-migrate.sh" --bootstrap-ledger --expect-db stayconnect_site --target-kind live-site --ack-target I_UNDERSTAND_LEDGER_BOOTSTRAP --bootstrap-owner postgres 2>&1 || true)"; echo "$o" | grep -q "fixed approved role" && ok "live-site bootstrap owner must be in fixed set, not env allowlist (§4)" || no "live owner env-allowlist accepted"
# §4 disposable marker required (rename it away, expect refusal, restore)
Q "ALTER TABLE public._scratch_marker RENAME TO _scratch_marker_bak;" >/dev/null
o="$(RUN "${APPLY_ARGS[@]}" 2>&1 || true)"; echo "$o" | grep -q "harness-generated marker" && ok "disposable apply requires harness marker (not caller assertion) (§4)" || no "disposable apply accepted without marker"
Q "ALTER TABLE public._scratch_marker_bak RENAME TO _scratch_marker;" >/dev/null
o="$(bash "$ROOT/scripts/edge-migrate.sh" --bootstrap-ledger --expect-db "$DB" --target-kind disposable --ack-target I_UNDERSTAND_LEDGER_BOOTSTRAP --bootstrap-owner postgres 2>&1 || true)"; echo "$o" | grep -q "EDGE_LEDGER_BOOTSTRAP_OK" && echo "$o" | grep -q "no migration applied" && ok "standalone bootstrap creates ledger + applies NO migration (§5)" || no "bootstrap mode failed"
[ "$(Q "SELECT count(*) FROM public.schema_migrations;")" = 0 ] && ok "bootstrapped ledger is empty (bootstrap applied no migration, §5)" || no "bootstrap wrote migration rows"
Q "DROP TABLE public.schema_migrations; ALTER TABLE public.schema_migrations_bak RENAME TO schema_migrations;" >/dev/null
[ "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0010_phase3_stay_resolution';")" = 1 ] && ok "original ledger restored (one 0010 row)" || no "ledger restore failed"

echo '== deployment-parity ownership: apply 0010 as non-superuser iam_v2_owner (PART A §5) =='
# reassign the whole iam_v2 schema to a NOSUPERUSER owner, roll 0010 back, then re-apply AS that owner.
Q "DROP ROLE IF EXISTS iam_v2_owner; CREATE ROLE iam_v2_owner LOGIN PASSWORD 'ownerpw' NOSUPERUSER NOCREATEDB NOCREATEROLE;" >/dev/null
Q "DO \$ro\$ DECLARE r record; BEGIN
     EXECUTE 'ALTER SCHEMA iam_v2 OWNER TO iam_v2_owner';
     FOR r IN SELECT format('ALTER TABLE iam_v2.%I OWNER TO iam_v2_owner', tablename) c FROM pg_tables WHERE schemaname='iam_v2' LOOP EXECUTE r.c; END LOOP;
     FOR r IN SELECT format('ALTER FUNCTION iam_v2.%I(%s) OWNER TO iam_v2_owner', p.proname, pg_get_function_identity_arguments(p.oid)) c FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' LOOP EXECUTE r.c; END LOOP;
     FOR r IN SELECT format('ALTER VIEW iam_v2.%I OWNER TO iam_v2_owner', viewname) c FROM pg_views WHERE schemaname='iam_v2' LOOP EXECUTE r.c; END LOOP;
   END \$ro\$;" >/dev/null
# APPLY role gets ONLY SELECT+INSERT on the ledger (no DELETE) -- rollback/admin is a separate operation (§4)
Q "GRANT USAGE ON SCHEMA public TO iam_v2_owner; GRANT INSERT, SELECT ON public.schema_migrations TO iam_v2_owner; GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO iam_v2_owner;" >/dev/null
# deployment-parity service roles (mirror the accepted live least-privilege model) — created with ZERO iam_v2 access
Q "DO \$sr\$ DECLARE r text; BEGIN FOREACH r IN ARRAY ARRAY['svc_scd','svc_edged','svc_portald','svc_acctd','svc_pmsd'] LOOP EXECUTE format('DROP ROLE IF EXISTS %I',r); EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE',r,'x'); EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),r); END LOOP; END \$sr\$;" >/dev/null
Qf < "$DOWN" >/dev/null
[ "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0010_phase3_stay_resolution';")" = 0 ] && ok "0010 rolled back before ownership re-apply" || no "pre-ownership rollback failed"
export EDGE_PSQL_OWNER="docker exec -i $C psql -U iam_v2_owner -d $DB -v ON_ERROR_STOP=1"
oo="$(EDGE_PSQL="$EDGE_PSQL_OWNER" RUN --only 0010_phase3_stay_resolution --expect-db "$DB" --expect-sha256 "$UPSHA" 2>&1)"; echo "$oo" | grep -q "apply 0010" && ok "0010 applies with the smallest approved role (NON-superuser iam_v2_owner; no superuser needed, §5/§6)" || { no "0010 failed under iam_v2_owner"; echo "$oo" | tail -3; }
OWN_BAD="$(Q "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_roles r ON r.oid=c.relowner WHERE n.nspname='iam_v2' AND c.relkind IN ('r','i','v') AND r.rolname<>'iam_v2_owner';")"
[ "$OWN_BAD" = 0 ] && ok "every iam_v2 relation (incl. new 0010 objects) owned by iam_v2_owner (§5)" || no "$OWN_BAD iam_v2 relations not owned by iam_v2_owner"
FUN_BAD="$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace LEFT JOIN pg_roles r ON r.oid=p.proowner WHERE n.nspname='iam_v2' AND r.rolname<>'iam_v2_owner';")"
[ "$FUN_BAD" = 0 ] && ok "every iam_v2 function (incl. p3_* triggers) owned by iam_v2_owner (§5)" || no "$FUN_BAD iam_v2 functions not owned by iam_v2_owner"
PUBG="$(Q "SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee='PUBLIC' AND table_name IN ('pms_interface_runtime','site_checkout_grace_config','auth_resolutions');")"
[ "$PUBG" = 0 ] && ok "no unexpected PUBLIC grants on new/altered 0010 tables (§5)" || no "unexpected PUBLIC grants ($PUBG)"
# §6: every runtime service role has ZERO iam_v2 privileges while DARK
SVCG="$(Q "SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee IN ('svc_scd','svc_edged','svc_portald','svc_acctd','svc_pmsd');")"
[ "$SVCG" = 0 ] && ok "runtime service roles (scd/edged/portald/acctd/pmsd) have ZERO iam_v2 table privileges while DARK (§6)" || no "service roles hold $SVCG iam_v2 grants"
SVCF="$(Q "SELECT count(*) FROM information_schema.role_routine_grants WHERE routine_schema='iam_v2' AND grantee IN ('svc_scd','svc_edged','svc_portald','svc_acctd','svc_pmsd');")"
[ "$SVCF" = 0 ] && ok "runtime service roles have ZERO iam_v2 function EXECUTE while DARK (§6)" || no "service roles hold $SVCF iam_v2 EXECUTE grants"
# §4: APPLY role holds ONLY SELECT+INSERT on the ledger -- never destructive rights (rollback/admin only)
AOK=1
for p in SELECT INSERT; do [ "$(Q "SELECT has_table_privilege('iam_v2_owner','public.schema_migrations','$p');")" = t ] || AOK=0; done
[ "$AOK" = 1 ] && ok "apply role holds required SELECT+INSERT on ledger (§4)" || no "apply role missing SELECT/INSERT"
ABAD=0
for p in UPDATE DELETE TRUNCATE REFERENCES TRIGGER; do [ "$(Q "SELECT has_table_privilege('iam_v2_owner','public.schema_migrations','$p');")" = t ] && ABAD=1; done
[ "$ABAD" = 0 ] && ok "apply role holds NO destructive ledger rights (no UPDATE/DELETE/TRUNCATE/REFERENCES/TRIGGER) (§4)" || no "apply role holds a destructive ledger privilege"
# §6: migration role (iam_v2_owner) holds no broad public-schema DDL (no public CREATE beyond ledger writes)
MDDL="$(Q "SELECT has_schema_privilege('iam_v2_owner','public','CREATE');")"
[ "$MDDL" = f ] && ok "migration role has no broad public-schema CREATE/DDL privilege (§6)" || no "migration role holds public CREATE"
# §6: default privileges do not grant future objects to PUBLIC/service roles
DEFP="$(Q "SELECT count(*) FROM pg_default_acl d JOIN pg_namespace n ON n.oid=d.defaclnamespace WHERE n.nspname='iam_v2' AND array_to_string(d.defaclacl,',') ~ '(=|svc_)';")"
[ "$DEFP" = 0 ] && ok "no default privileges grant future iam_v2 objects to PUBLIC/service roles (§6)" || no "default ACLs leak future objects"
[ "$(Q "$FP")" = "$POST" ] && ok "catalog after owner-parity re-apply == post-0010" || no "owner re-apply catalog != post"

echo '== teardown =='
docker rm -f "$C" >/dev/null 2>&1 && ok "disposable DB destroyed" || no "teardown failed"

echo "============================================================"
echo "PHASE3_0010_LIFECYCLE: pass=$pass fail=$fail  -> $([ $fail -eq 0 ] && echo PASS || echo FAIL)"
[ $fail -eq 0 ]
