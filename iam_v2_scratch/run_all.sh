#!/usr/bin/env bash
# Master evidence runner (sequential; no concurrent DB access). Produces EVIDENCE.txt + review/.
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"; set +e +o pipefail
export SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE
R="$HERE/review"; mkdir -p "$R"; EV="$HERE/EVIDENCE.txt"; LOG="$R/COMMAND_LOG.txt"
: > "$LOG"
log(){ echo ">>> $*" | tee -a "$LOG"; }

{
echo "iam_v2 SCRATCH — A-SERIES + REVIEW EVIDENCE (rev 2, corrected per PO evidence review)"
echo "Disposable container 'iamv2-scratch' 127.0.0.1:55432 db iam_scratch, PostgreSQL 16.14, Docker 29.5.2."
echo "ALLOWLIST safety guard (marker + ack + loopback + port + prefix; no fallback). NOT live; NOT cut over."
echo "=========================================================================================="
} > "$EV"

log "run.sh fresh (marker + MG-0..MG-9)"; bash "$HERE/run.sh" fresh >>"$LOG" 2>&1
log "seed.sql"; SCRATCH_DB=iam_scratch bash -c 'source "'"$HERE"'/lib.sh"; SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE; psqlf "'"$HERE"'/seed.sql"' >>"$LOG" 2>&1
echo "### CORE SUITE ###" >>"$EV"; log "tests.sh"; bash "$HERE/tests.sh" >>"$EV" 2>&1
echo "" >>"$EV"; echo "### EXTRA SUITE ###" >>"$EV"; log "tests_extra.sh"; bash "$HERE/tests_extra.sh" >>"$EV" 2>&1
echo "" >>"$EV"; echo "### GUARD ALLOWLIST TESTS ###" >>"$EV"; log "guard_tests.sh"; bash "$HERE/guard_tests.sh" >>"$EV" 2>&1
log "roles_apply.sh (owner-owned build)"; bash "$HERE/roles_apply.sh" >>"$LOG" 2>&1
echo "" >>"$EV"; echo "### ROLE / LEAST-PRIVILEGE TESTS ###" >>"$EV"; log "role_tests.sh"; bash "$HERE/role_tests.sh" >>"$EV" 2>&1
log "inventory.sh"; bash "$HERE/inventory.sh" >>"$LOG" 2>&1
echo "" >>"$EV"; echo "### IDEMPOTENCY / CATALOG EQUALITY ###" >>"$EV"; log "idempotency.sh"; bash "$HERE/idempotency.sh" >>"$EV" 2>&1
echo "" >>"$EV"; echo "### OFFLINE REAL-SCHEMA COMPATIBILITY ###" >>"$EV"; log "offline_real.sh"; bash "$HERE/offline_real.sh" >>"$EV" 2>&1

# secret / guest-PII scans over committed artifacts (exclude scanners)
echo "" >>"$EV"; echo "### SECRET / GUEST-PII SCAN ###" >>"$EV"
sc=$(grep -rniE "BEGIN (RSA|OPENSSH) PRIVATE|ssh-ed25519 AAAA|ProofAdmin|sk_live|whsec_|passport|14215|262224" "$HERE" --include=*.sql --include=*.sh --include=*.md --include=*.txt --exclude=tests.sh --exclude=tests_extra.sh --exclude=guard_tests.sh --exclude=run_all.sh 2>/dev/null | wc -l)
pw=$(grep -rniE "POSTGRES_PASSWORD=" "$HERE" --exclude=tests.sh --exclude=tests_extra.sh --exclude=guard_tests.sh --exclude=run_all.sh 2>/dev/null | wc -l)
echo "SEC-01 secrets/guest-PII patterns in committed artifacts: $sc (want 0)" >>"$EV"
echo "SEC-02 POSTGRES_PASSWORD assignments committed: $pw (want 0)" >>"$EV"

# totals
echo "" >>"$EV"; echo "### TOTALS ###" >>"$EV"
P=$(grep -c '^PASS' "$EV"); F=$(grep -c '^FAIL' "$EV")
echo "OVERALL: PASS=$P FAIL=$F  (Core + Extra + Guard + Role + Idempotency + Offline-real)" | tee -a "$EV"
