#!/usr/bin/env bash
# A-series EXTRA: migration lifecycle, MG-0 recovery, concurrency races, restart persistence, PII scan.
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"; set +e +o pipefail
Q(){ docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt -c "$1" 2>&1; }
PASSN=0; FAILN=0
ok(){ echo "PASS  $1"; PASSN=$((PASSN+1)); }
no(){ echo "FAIL  $1  :: $2"; FAILN=$((FAILN+1)); }
eq(){ local l="$1" got="$2" exp="$3"; [ "$got" = "$exp" ] && ok "$l" || no "$l" "got '$got' want '$exp'"; }

echo "===== A-SERIES EXTRA (scratch) ====="

# MIG lifecycle: up / down / re-up
bash "$HERE/run.sh" fresh >/dev/null 2>&1
eq "MIG-01 migration up -> 49 iam_v2 tables" "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';")" "49"
bash "$HERE/run.sh" down >/dev/null 2>&1
eq "MIG-02 migration down -> 0 iam_v2 tables" "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2';")" "0"
bash "$HERE/run.sh" fresh >/dev/null 2>&1
eq "MIG-03 migration re-up -> 49 iam_v2 tables (deterministic rebuild)" "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';")" "49"

# MG-0 interruption recovery is a MG-0-TIME operation (before MG-1+ FKs depend on the anchor).
Q "DROP SCHEMA IF EXISTS iam_v2 CASCADE;" >/dev/null 2>&1     # remove FK dependents on the anchor
Q "UPDATE pg_index SET indisvalid=false WHERE indexrelid='public.guest_networks_tsi_anchor'::regclass;" >/dev/null
inv="$(Q "SELECT indisvalid::text FROM pg_index WHERE indexrelid='public.guest_networks_tsi_anchor'::regclass;")"
bash "$HERE/mg0.sh" >/tmp/mg0rec.out 2>&1
val="$(Q "SELECT indisvalid::text FROM pg_index WHERE indexrelid='public.guest_networks_tsi_anchor'::regclass;")"
eq "MG0-REC-01 invalid index detected before recovery" "$inv" "false"
eq "MG0-REC-02 MG-0 recovery yields VALID index (via DROP+rebuild CONCURRENTLY)" "$val" "true"

# restore MG-1..9 (anchor now valid) + reseed for the race/restart tests
bash "$HERE/run.sh" up >/dev/null 2>&1
psqlf "$HERE/seed.sql" >/dev/null 2>&1

# CONCURRENCY: device admission race, max=1, two parallel txns, distinct devices, same credential.
Q "UPDATE iam_v2.service_plan_revisions SET max_concurrent_devices=1 WHERE id='bbbb0000-0000-0000-0000-0000000000d1';" >/dev/null 2>&1 || true
ENT="'12340000-0000-0000-0000-000000000001'"; APP="'ab000000-0000-0000-0000-000000000001'"
D1="'d1d10000-0000-0000-0000-000000000001'"; D2="'d1d10000-0000-0000-0000-000000000002'"
race_one(){ Q "BEGIN; SELECT iam_v2.reserve_device_slot($ENT,$1,'race-cred',$APP::text,1); SELECT pg_sleep(0.4); COMMIT;"; }
r1="$(race_one "$D1" & race_one "$D2" & wait)"
auth=$(echo "$r1" | grep -c AUTHORIZED); maxd=$(echo "$r1" | grep -c MAX_DEVICES_REACHED)
# clean the committed bindings
Q "DELETE FROM iam_v2.entitlement_devices WHERE entitlement_id=$ENT;" >/dev/null 2>&1
eq "RACE-01 device race max=1: exactly one AUTHORIZED" "$auth" "1"
eq "RACE-02 device race max=1: exactly one MAX_DEVICES_REACHED" "$maxd" "1"

# CONCURRENCY: one-live-entitlement race, two parallel inserts same voucher (distinct purchases) -> exactly one wins
Q "INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,amount_minor,currency,currency_exponent,state) VALUES ('99990000-0000-0000-0000-0000000000c1','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','cccc0000-0000-0000-0000-0000000000d1','aaaa0000-0000-0000-0000-000000000001','eeee0000-0000-0000-0000-000000000001','dddd0000-0000-0000-0000-000000000001','RENEWAL',100,'USD',2,'GRANTED');" >/dev/null 2>&1
Q "INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,amount_minor,currency,currency_exponent,state) VALUES ('99990000-0000-0000-0000-0000000000c2','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','cccc0000-0000-0000-0000-0000000000d1','aaaa0000-0000-0000-0000-000000000001','eeee0000-0000-0000-0000-000000000001','dddd0000-0000-0000-0000-000000000001','RENEWAL',100,'USD',2,'GRANTED');" >/dev/null 2>&1
# delete the seed entitlement (+ its dependent session/bindings) so both racers contend for the single live-voucher slot
Q "DELETE FROM iam_v2.sessions WHERE entitlement_id='12340000-0000-0000-0000-000000000001';" >/dev/null 2>&1
Q "DELETE FROM iam_v2.entitlement_devices WHERE entitlement_id='12340000-0000-0000-0000-000000000001';" >/dev/null 2>&1
Q "DELETE FROM iam_v2.entitlements WHERE voucher_id='ffff0000-0000-0000-0000-0000000000d1';" >/dev/null 2>&1
ins(){ Q "INSERT INTO iam_v2.entitlements(tenant_id,site_id,voucher_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status) VALUES ('11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','ffff0000-0000-0000-0000-0000000000d1','aaaa0000-0000-0000-0000-000000000001','$1','{}','bbbb0000-0000-0000-0000-0000000000d1','cccc0000-0000-0000-0000-0000000000d1','VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE') RETURNING 'INSERTED';"; }
r2="$(ins '99990000-0000-0000-0000-0000000000c1' & ins '99990000-0000-0000-0000-0000000000c2' & wait)"
wins=$(echo "$r2" | grep -c INSERTED); dup=$(echo "$r2" | grep -c ent_live_voucher)
eq "RACE-03 one-live-entitlement race: exactly one INSERT wins" "$wins" "1"
eq "RACE-04 one-live-entitlement race: exactly one rejected by ent_live_voucher" "$dup" "1"

# RESTART persistence: window_ends_at must survive a container restart unchanged (validity-window immutable across restart)
# stamp a deterministic window (NULL->value allowed once) on the surviving voucher entitlement
Q "UPDATE iam_v2.entitlements SET window_ends_at='2030-01-01T00:00:00Z' WHERE voucher_id='ffff0000-0000-0000-0000-0000000000d1' AND window_ends_at IS NULL;" >/dev/null 2>&1
before="$(Q "SELECT window_ends_at::text FROM iam_v2.entitlements WHERE voucher_id='ffff0000-0000-0000-0000-0000000000d1' LIMIT 1;")"
docker restart "$SCRATCH_CONTAINER" >/dev/null 2>&1
for i in $(seq 1 30); do docker exec "$SCRATCH_CONTAINER" pg_isready -U postgres -d "$SCRATCH_DB" >/dev/null 2>&1 && break; sleep 1; done
after="$(Q "SELECT window_ends_at::text FROM iam_v2.entitlements WHERE voucher_id='ffff0000-0000-0000-0000-0000000000d1' LIMIT 1;")"
eq "RESTART-01 validity window unchanged across container restart" "$after" "$before"
eq "RESTART-02 iam_v2 schema intact after restart (49 tables)" "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';")" "49"

# SECRET / PII scan of committed scratch artifacts (exclude the scanner scripts, which contain the pattern strings themselves)
scan=$(grep -rniE "BEGIN (RSA|OPENSSH) PRIVATE|ssh-ed25519 AAAA|ProofAdmin|sk_live|whsec_|passport|[0-9]{16}" "$HERE" --include=*.sql --include=*.sh --exclude=tests.sh --exclude=tests_extra.sh 2>/dev/null | wc -l)
eq "SEC-01 no secrets/PII patterns in committed scratch artifacts" "$scan" "0"
# the disposable container password must NOT be assigned in any committed file (it lived only in a one-off docker-run command)
pwleak=$(grep -rniE "POSTGRES_PASSWORD=" "$HERE" --exclude=tests.sh --exclude=tests_extra.sh 2>/dev/null | wc -l)
eq "SEC-02 no POSTGRES_PASSWORD assignment committed in scratch artifacts" "$pwleak" "0"

echo "===== EXTRA RESULT: PASS=$PASSN FAIL=$FAILN ====="
[ "$FAILN" = "0" ]
