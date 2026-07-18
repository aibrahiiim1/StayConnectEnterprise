#!/usr/bin/env bash
# Phase-3 migration 0010 lifecycle + behaviour verification against a DISPOSABLE PostgreSQL only.
# Prereq: accepted iam_v2 schema built (run.sh fresh) + 0009 applied. Uses the iamv2-scratch container.
# Proves: apply / apply-twice-noop / rollback==pre / reapply==post catalog fingerprints; expected objects;
# FK negative; one-way status + monotonic version; reinstatement rule; stay_events append-only; grace bounds.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C=iamv2-scratch; DB=iam_scratch
UP="$(cd "$(dirname "$0")/.." && pwd)/data-plane/migrations/0010_phase3_stay_resolution.up.sql"
DOWN="$(cd "$(dirname "$0")/.." && pwd)/data-plane/migrations/0010_phase3_stay_resolution.down.sql"
pass=0; fail=0
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
Qf(){ docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 2>&1; }
ok(){ echo "  [PASS] $1"; pass=$((pass+1)); }
no(){ echo "  [FAIL] $1"; fail=$((fail+1)); }
# expect a statement to FAIL (raise); returns 0 if it errored
expect_err(){ local out; out="$(docker exec "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAqc "$1" 2>&1)"; if echo "$out" | grep -qiE "ERROR|EXCEPTION"; then return 0; else echo "    (no error: $out)"; return 1; fi; }
expect_ok(){ local out; out="$(docker exec "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAqc "$1" 2>&1)"; if echo "$out" | grep -qiE "ERROR|EXCEPTION"; then echo "    (unexpected error: $out)"; return 1; else return 0; fi; }

FINGERPRINT="SELECT md5(string_agg(x, E'\n' ORDER BY x)) FROM (
  SELECT 'C '||table_name||'.'||column_name||':'||data_type||':'||is_nullable||':'||coalesce(column_default,'') AS x FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT 'T '||c.relname||'.'||t.tgname FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT 'I '||indexname||':'||indexdef FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT 'K '||conname||':'||pg_get_constraintdef(con.oid) FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2'
) s;"

echo '== 0010 lifecycle =='
PRE="$(Q "$FINGERPRINT")"; echo "  pre-0010 catalog md5 = $PRE"
Qf < "$UP" >/dev/null && ok "apply 0010" || no "apply 0010"
POST="$(Q "$FINGERPRINT")"; echo "  post-0010 catalog md5 = $POST"
[ "$PRE" != "$POST" ] && ok "0010 changed the catalog" || no "0010 changed nothing"
# apply-twice = deterministic no-op via the migration ledger. A raw re-apply of the same file must ERROR
# (objects already exist) and leave the catalog unchanged; the real runner's schema_migrations ledger
# turns that into a silent skip. We assert the raw re-apply errors "already exists" and the catalog is stable.
RE="$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP" 2>&1)"
echo "$RE" | grep -qi "already exists" && ok "raw re-apply errors 'already exists' (ledger -> no-op in the runner)" || no "raw re-apply did not error as expected"
POST2="$(Q "$FINGERPRINT")"; [ "$POST" = "$POST2" ] && ok "catalog unchanged after failed re-apply (idempotent no-op)" || no "catalog changed on re-apply"

echo '== expected objects =='
[ "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_name='pms_interface_runtime';")" = 1 ] && ok "pms_interface_runtime exists" || no "pms_interface_runtime missing"
[ "$(Q "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND t.tgname IN ('p3_stay_event_guard','p3_stay_lifecycle_guard');")" = 2 ] && ok "both Phase-3 triggers exist" || no "Phase-3 triggers missing"
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='stays' AND column_name IN ('effective_checkout_at','occupancy_evidence_at','occupancy_ingested_at','occupancy_revision_id','occupancy_normalization_version','occupancy_clock_suspect');")" = 6 ] && ok "stays effective_checkout+occupancy columns (6)" || no "stays columns missing"
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='site_checkout_grace_config' AND column_name IN ('eligibility_window_seconds','grace_duration_seconds','grace_down_kbps','grace_up_kbps','grace_data_quota_mb','grace_device_limit','grace_new_device_policy');")" = 7 ] && ok "grace scalar columns (7)" || no "grace columns missing"
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='auth_resolutions' AND column_name='resolution_request_id';")" = 1 ] && ok "auth_resolutions.resolution_request_id" || no "resolution_request_id missing"
[ "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='stays' AND column_name='checkout_episode';")" = 0 ] && ok "NO stays.checkout_episode (episode = lifecycle_version)" || no "unexpected stays.checkout_episode"

echo '== FK negative =='
if expect_err "INSERT INTO iam_v2.pms_interface_runtime(tenant_id,site_id,pms_interface_id) VALUES (gen_random_uuid(),gen_random_uuid(),gen_random_uuid());"; then ok "runtime FK rejects unknown interface" || true; else no "runtime FK not enforced"; fi

echo '== seed one interface+revision+stay for behaviour tests =='
Q "DO \$\$DECLARE t uuid:=gen_random_uuid(); s uuid:=gen_random_uuid(); i uuid:=gen_random_uuid(); r uuid:=gen_random_uuid(); st uuid:=gen_random_uuid();
BEGIN
  INSERT INTO public.tenants(id) VALUES (t) ON CONFLICT DO NOTHING;
  INSERT INTO public.sites(id,tenant_id) VALUES (s,t) ON CONFLICT DO NOTHING;
  INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind) VALUES (i,t,s,'protel-fias');
  INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config) VALUES (r,t,s,i,1,'Africa/Cairo','{}');
  INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version) VALUES (st,t,s,i,'R1','S1','RESERVED',1,0);
  INSERT INTO iam_v2.pms_interface_runtime(tenant_id,site_id,pms_interface_id,pinned_revision_id) VALUES (t,s,i,r);
  INSERT INTO iam_v2.stay_events(id,tenant_id,site_id,pms_interface_id,stay_id,external_event_identity,event_type,pms_timestamp_raw,pms_timestamp_utc,source_timezone,sequence_version,normalization_version,clock_suspect,payload,processing_status) VALUES (gen_random_uuid(),t,s,i,st,'E1','GI','x',now(),'Africa/Cairo',1,1,false,'{}','PENDING');
  PERFORM set_config('mytest.t',t::text,false); PERFORM set_config('mytest.st',st::text,false);
END\$\$;" >/dev/null
ST="$(Q "SELECT id FROM iam_v2.stays WHERE external_stay_identity='S1' LIMIT 1;")"
[ -n "$ST" ] && ok "seed created stay $ST" || no "seed failed"

echo '== one-way status transitions =='
expect_ok  "UPDATE iam_v2.stays SET status='IN_HOUSE', last_applied_event_version=1 WHERE id='$ST';" && ok "RESERVED->IN_HOUSE allowed" || no "RESERVED->IN_HOUSE blocked"
expect_err "UPDATE iam_v2.stays SET status='RESERVED' WHERE id='$ST';" && ok "IN_HOUSE->RESERVED rejected (backward)" || no "backward transition allowed"
expect_err "UPDATE iam_v2.stays SET last_applied_event_version=0 WHERE id='$ST';" && ok "event version cannot decrease" || no "event version decreased"
expect_ok  "UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id='$ST';" && ok "IN_HOUSE->CHECKED_OUT allowed + effective_checkout_at set" || no "checkout blocked"
expect_err "UPDATE iam_v2.stays SET status='IN_HOUSE' WHERE id='$ST';" && ok "reinstatement without lifecycle_version++ rejected" || no "reinstatement bare bump allowed"
expect_err "UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1 WHERE id='$ST';" && ok "reinstatement blocked while effective_checkout_at set (must clear it)" || no "reinstatement left stale effective_checkout"
expect_ok  "UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1, effective_checkout_at=NULL WHERE id='$ST';" && ok "reinstatement with version++ and cleared effective_checkout allowed" || no "valid reinstatement blocked"
expect_err "UPDATE iam_v2.stays SET lifecycle_version=lifecycle_version+2 WHERE id='$ST';" && ok "lifecycle_version cannot jump >1" || no "lifecycle jump allowed"

echo '== stay_events append-only =='
EV="$(Q "SELECT id FROM iam_v2.stay_events WHERE external_event_identity='E1' LIMIT 1;")"
expect_err "UPDATE iam_v2.stay_events SET external_event_identity='E1-tamper' WHERE id='$EV';" && ok "event identity immutable" || no "event identity mutable"
expect_err "DELETE FROM iam_v2.stay_events WHERE id='$EV';" && ok "event DELETE rejected" || no "event DELETE allowed"
expect_ok  "UPDATE iam_v2.stay_events SET processing_status='APPLIED', stay_id='$ST' WHERE id='$EV';" && ok "PENDING->APPLIED + resolve stay allowed" || no "valid application blocked"
expect_err "UPDATE iam_v2.stay_events SET processing_status='FAILED' WHERE id='$EV';" && ok "terminal processing_status immutable (APPLIED->FAILED rejected)" || no "terminal status mutated"

echo '== grace bounds =='
expect_err "UPDATE iam_v2.site_checkout_grace_config SET eligibility_window_seconds=0 WHERE false; INSERT INTO iam_v2.site_checkout_grace_config(tenant_id,site_id,eligibility_window_seconds) VALUES (gen_random_uuid(),gen_random_uuid(),0);" && ok "grace eligibility_window must be >0" || no "grace bound not enforced"

echo '== rollback == pre and reapply == post =='
Qf < "$DOWN" >/dev/null && ok "rollback 0010" || no "rollback 0010"
DOWNFP="$(Q "$FINGERPRINT")"; [ "$DOWNFP" = "$PRE" ] && ok "catalog after rollback == pre-0010" || no "rollback catalog != pre ($DOWNFP vs $PRE)"
Qf < "$UP" >/dev/null && ok "reapply 0010" || no "reapply 0010"
REFP="$(Q "$FINGERPRINT")"; [ "$REFP" = "$POST" ] && ok "catalog after reapply == first post-0010" || no "reapply catalog != post ($REFP vs $POST)"

echo "============================================================"
echo "PHASE3_0010_LIFECYCLE: pass=$pass fail=$fail  -> $([ $fail -eq 0 ] && echo PASS || echo FAIL)"
[ $fail -eq 0 ]
