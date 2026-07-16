#!/usr/bin/env bash
# Allowlist safety-guard NEGATIVE tests: every non-scratch target must be REFUSED.
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"; set +e +o pipefail
PASSN=0; FAILN=0
ok(){ echo "PASS  $1"; PASSN=$((PASSN+1)); }
no(){ echo "FAIL  $1 :: $2"; FAILN=$((FAILN+1)); }
# a guard call is expected to ABORT (non-zero) under a bad target
refuses(){ local l="$1"; shift; ( "$@" ) >/dev/null 2>&1; [ $? -ne 0 ] && ok "$l" || no "$l" "guard did NOT refuse"; }
allows(){  local l="$1"; shift; ( "$@" ) >/dev/null 2>&1; [ $? -eq 0 ] && ok "$l" || no "$l" "guard wrongly refused"; }

export SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE
echo "===== SAFETY-GUARD ALLOWLIST TESTS ====="

# positive control: correct scratch target is allowed
allows   "GUARD-00 correct disposable scratch target ALLOWED" bash -c 'source "'"$HERE"'/lib.sh"; require_scratch'

# negatives — each must be refused
refuses  "GUARD-01 live db name 'stayconnect_site' refused"        bash -c 'source "'"$HERE"'/lib.sh"; SCRATCH_DB=stayconnect_site require_scratch'
refuses  "GUARD-02 alternate live-looking 'stayconnect' refused"   bash -c 'source "'"$HERE"'/lib.sh"; SCRATCH_DB=stayconnect require_scratch'
refuses  "GUARD-03 non-scratch-prefix db 'prod_iam' refused"       bash -c 'source "'"$HERE"'/lib.sh"; SCRATCH_DB=prod_iam require_scratch'
refuses  "GUARD-04 non-local host (wrong container) refused"       bash -c 'source "'"$HERE"'/lib.sh"; SCRATCH_CONTAINER=nonexistent-host require_scratch'
refuses  "GUARD-05 wrong port refused"                             bash -c 'source "'"$HERE"'/lib.sh"; SCRATCH_PORT_ALLOW=5432 require_scratch'
refuses  "GUARD-06 missing ack flag refused"                       bash -c 'source "'"$HERE"'/lib.sh"; unset SCRATCH_ACK; require_scratch'
refuses  "GUARD-07 empty db name (empty DSN) refused"              bash -c 'source "'"$HERE"'/lib.sh"; SCRATCH_DB= require_scratch'
refuses  "GUARD-08 malformed db name refused"                      bash -c 'source "'"$HERE"'/lib.sh"; SCRATCH_DB="iam scratch; drop" require_scratch'

# marker negatives: false/missing marker must be refused
docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -qAt -c "UPDATE public._scratch_marker SET marker='FALSE_MARKER';" >/dev/null 2>&1
refuses  "GUARD-09 false marker value refused"                     bash -c 'source "'"$HERE"'/lib.sh"; require_scratch'
docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -qAt -c "DELETE FROM public._scratch_marker;" >/dev/null 2>&1
refuses  "GUARD-10 missing marker row refused"                     bash -c 'source "'"$HERE"'/lib.sh"; require_scratch'
# restore the correct marker
docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -qAt -c "INSERT INTO public._scratch_marker VALUES ('DISPOSABLE_SCRATCH_ONLY') ON CONFLICT DO NOTHING; UPDATE public._scratch_marker SET marker='DISPOSABLE_SCRATCH_ONLY';" >/dev/null 2>&1
allows   "GUARD-11 restored correct marker ALLOWED again" bash -c 'source "'"$HERE"'/lib.sh"; require_scratch'

echo "===== GUARD RESULT: PASS=$PASSN FAIL=$FAILN ====="
[ "$FAILN" = "0" ]
