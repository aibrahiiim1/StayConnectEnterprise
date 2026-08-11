#!/usr/bin/env bash
# VERIFIED BINARY ROLLBACK / REDEPLOY.
#
# This exists because of a confirmed false PASS during Phase-3 Live Increment 9. The rehearsal replaced the
# service binaries with `cp`, which cannot overwrite a running executable — every copy failed with "Text file
# busy" — and then "verified" the rollback by checking that the services were `active` and that the units had no
# failures. They were active, and they had no failures, because they were still running the binaries the
# rehearsal believed it had just replaced. The rehearsal reported success while having changed nothing.
#
# Three rules follow from that, and this script exists to enforce all three:
#
#   1. USE install(1), NEVER cp. install unlinks the destination first, so a running executable is replaced
#      instead of refusing the write.
#   2. A FAILED REPLACEMENT IS FATAL, IMMEDIATELY. Not a warning, not a continue-and-report-at-the-end: the
#      remaining steps assume the file on disk is the one that was asked for.
#   3. "HEALTHY" IS NOT "ROLLED BACK". Verification compares the expected sha256 against the file on disk AND
#      against the executable the running process is actually backed by (/proc/<pid>/exe). Service state is
#      reported but never counted as evidence of identity.
#
# Usage:
#   scripts/binary-rollback.sh --bin-dir /opt/stayconnect/bin --source-suffix .bak-inc9 \
#       --unit scd=stayconnect-scd --unit netd=stayconnect-netd [...]
#   scripts/binary-rollback.sh --bin-dir /opt/stayconnect/bin --source-dir /root/inc9-stage/bin \
#       --unit scd=stayconnect-scd [...]
#
# Exactly one of --source-suffix (roll back to <bin>.<suffix> beside the target) or --source-dir (roll forward
# from a staging directory) is required.
#
# Test seams (used by scripts/ci/binary-rollback-tests.sh; unset in production):
#   SC_ROLLBACK_INSTALL_CMD   <src> <dst>  — replace the file
#   SC_ROLLBACK_RESTART_CMD   <unit>       — restart the service
#   SC_ROLLBACK_RUNNING_EXE   <unit>       — print the path of the executable the running service is backed by
#   SC_ROLLBACK_UNIT_STATE    <unit>       — print the unit's activation state
#   SC_ROLLBACK_NFT                        — the nft binary (a namespace-scoped wrapper in the kernel suite)
set -uo pipefail

BIN_DIR=""; SRC_SUFFIX=""; SRC_DIR=""; DRY=0
declare -a UNIT_KEYS=() UNIT_VALS=()

die() { printf 'binary-rollback: FATAL: %s\n' "$*" >&2; exit 1; }

PY=""
for cand in python3 python; do
  if [ "$("$cand" -c 'print(42)' 2>/dev/null)" = "42" ]; then PY="$cand"; break; fi
done

while [ $# -gt 0 ]; do
  case "$1" in
    --bin-dir)       BIN_DIR="$2"; shift 2;;
    --source-suffix) SRC_SUFFIX="$2"; shift 2;;
    --source-dir)    SRC_DIR="$2"; shift 2;;
    --unit)          UNIT_KEYS+=("${2%%=*}"); UNIT_VALS+=("${2#*=}"); shift 2;;
    --dry-run)       DRY=1; shift;;
    *) die "unknown argument: $1";;
  esac
done

[ -n "$BIN_DIR" ] || die "--bin-dir is required"
[ "${#UNIT_KEYS[@]}" -gt 0 ] || die "at least one --unit <binary>=<unit> is required"
if [ -n "$SRC_SUFFIX" ] && [ -n "$SRC_DIR" ]; then die "--source-suffix and --source-dir are mutually exclusive"; fi
if [ -z "$SRC_SUFFIX" ] && [ -z "$SRC_DIR" ]; then die "one of --source-suffix or --source-dir is required"; fi

sha_of() { sha256sum "$1" 2>/dev/null | cut -d' ' -f1; }

do_install() {
  if [ -n "${SC_ROLLBACK_INSTALL_CMD:-}" ]; then $SC_ROLLBACK_INSTALL_CMD "$1" "$2"; return $?; fi
  # install(1), not cp: it unlinks the destination, so a RUNNING executable is replaced rather than refused.
  install -m 0755 "$1" "$2"
}
do_restart() {
  if [ -n "${SC_ROLLBACK_RESTART_CMD:-}" ]; then $SC_ROLLBACK_RESTART_CMD "$1"; return $?; fi
  systemctl restart "$1"
}
running_exe() {
  if [ -n "${SC_ROLLBACK_RUNNING_EXE:-}" ]; then $SC_ROLLBACK_RUNNING_EXE "$1"; return $?; fi
  local pid; pid="$(systemctl show -p MainPID --value "$1" 2>/dev/null)"
  [ -n "$pid" ] && [ "$pid" != "0" ] || return 1
  readlink -f "/proc/$pid/exe" 2>/dev/null
}
unit_state() {
  if [ -n "${SC_ROLLBACK_UNIT_STATE:-}" ]; then $SC_ROLLBACK_UNIT_STATE "$1"; return 0; fi
  systemctl is-active "$1" 2>/dev/null
}

nft_bin() { printf '%s' "${SC_ROLLBACK_NFT:-nft}"; }

# ---- THE PRE-nftconverge COMPATIBILITY BOUNDARY ---------------------------------------------------------------
#
# A netd built before ADR-0003 does not reconcile; it re-asserts the stored bundle, and that bundle begins with
# `delete table inet stayconnect`. Starting one is therefore a FULL-TABLE REPLACEMENT that recreates the
# authorization sets EMPTY — every guest currently online loses access in the same instant.
#
# That is harmless when nobody is authorized, which is exactly the situation the Increment-9 rehearsal ran in,
# and it is a property-wide outage when somebody is. The rollback command cannot tell those apart by looking at
# the binaries, so it looks at the LIVE AUTHORIZATION instead:
#
#   target is convergence-capable  -> no boundary; roll back normally.
#   target predates convergence and legacy auth_ipv4 is EMPTY  -> nothing to lose; proceed.
#   target predates convergence and legacy auth_ipv4 is POPULATED  -> STOP, before anything is replaced.
#   the live set cannot be read at all  -> STOP. "Cannot prove empty" is not "is empty".
#
# There is deliberately NO override flag. A force switch on the ordinary rollback command is a force switch
# somebody uses at 3am during an incident; deauthorizing a whole property has to be a separate, deliberate
# decision, so this prints the operator action instead of offering a way past itself.
converges() {
  # A netd that knows how to reconcile carries the render-marker prefix in its binary; one that does not, does
  # not. This asks the artifact itself rather than trusting a version string or a filename.
  grep -qa 'netd-render-fp=' "$1" 2>/dev/null
}

legacy_auth_count() {
  local out
  out="$("$(nft_bin)" -j list set inet stayconnect auth_ipv4 2>/dev/null)" || return 1
  printf '%s' "$out" | "$PY" -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(1)
n=0
for o in d.get("nftables",[]):
    s=o.get("set")
    if s: n=len(s.get("elem",[]) or [])
print(n)
' 2>/dev/null || return 1
}

check_compat_boundary() { # $1 = binary name, $2 = source path
  [ "$1" = "netd" ] || return 0
  if converges "$2"; then
    printf '  %-18s rollback target is convergence-capable; no compatibility boundary applies\n' "$1"
    return 0
  fi
  printf '  %-18s rollback target PREDATES current-render convergence (no render marker in the binary)\n' "$1"
  local n
  if ! n="$(legacy_auth_count)"; then
    die "the live legacy authorization set could not be read, so it cannot be shown to be empty. A pre-convergence netd would replace the whole table and deauthorize whoever is in it. Refusing to start it. Operator action: establish the live state (\`nft list set inet stayconnect auth_ipv4\`) and re-run once it is readable and empty, or perform the rollback in a maintenance window that accepts reauthorization."
  fi
  if [ "$n" -eq 0 ]; then
    printf '  %-18s live legacy authorization is EMPTY (%s elements); nothing can be lost, rollback may proceed\n' "$1" "$n"
    return 0
  fi
  die "the rollback target predates current-render convergence and $n legacy authorization(s) are LIVE.
         Starting it would re-assert the stored bundle, which begins with \`delete table inet stayconnect\`,
         recreating the authorization sets EMPTY and cutting off all $n guest(s) at once. That is a
         property-wide deauthorization, and it will not be performed as part of an ordinary rollback.
         NOTHING HAS BEEN CHANGED.
         Operator action, choose one:
           (a) roll back to a release that carries the render marker (convergence-capable), which preserves
               live authorization across the transition; or
           (b) schedule a maintenance window, let the $n session(s) end or reauthenticate, confirm
               \`nft list set inet stayconnect auth_ipv4\` is empty, and re-run this command; or
           (c) if the outage is genuinely acceptable now, deauthorize deliberately and visibly first, then
               re-run — this command will not do it silently on your behalf."
}

echo "== plan =="
declare -a WANT_SHA=()
i=0
while [ $i -lt "${#UNIT_KEYS[@]}" ]; do
  b="${UNIT_KEYS[$i]}"; u="${UNIT_VALS[$i]}"
  if [ -n "$SRC_SUFFIX" ]; then src="$BIN_DIR/$b$SRC_SUFFIX"; else src="$SRC_DIR/$b"; fi
  [ -f "$src" ] || die "source binary for '$b' does not exist: $src"
  s="$(sha_of "$src")"
  [ -n "$s" ] || die "cannot hash source binary: $src"
  WANT_SHA+=("$s")
  printf '  %-18s unit=%-26s source=%s\n                     expect-sha256=%s\n' "$b" "$u" "$src" "$s"
  i=$((i+1))
done

echo "== compatibility boundary =="
[ -n "$PY" ] || die "no working Python interpreter; the live authorization state cannot be read and the pre-convergence boundary cannot be evaluated"
i=0
while [ $i -lt "${#UNIT_KEYS[@]}" ]; do
  b="${UNIT_KEYS[$i]}"
  if [ -n "$SRC_SUFFIX" ]; then src="$BIN_DIR/$b$SRC_SUFFIX"; else src="$SRC_DIR/$b"; fi
  check_compat_boundary "$b" "$src"
  i=$((i+1))
done

[ "$DRY" -eq 1 ] && { echo "== dry run; nothing replaced =="; exit 0; }

echo "== replace =="
i=0
while [ $i -lt "${#UNIT_KEYS[@]}" ]; do
  b="${UNIT_KEYS[$i]}"
  if [ -n "$SRC_SUFFIX" ]; then src="$BIN_DIR/$b$SRC_SUFFIX"; else src="$SRC_DIR/$b"; fi
  dst="$BIN_DIR/$b"
  if ! do_install "$src" "$dst"; then
    # THE "Text file busy" CASE. Stop here: continuing would restart services that were never replaced and
    # then report whatever they happen to be running as a successful rollback.
    die "replacing '$dst' failed — nothing further was attempted. Binaries are in an UNKNOWN state; re-run after resolving the cause."
  fi
  got="$(sha_of "$dst")"
  [ "$got" = "${WANT_SHA[$i]}" ] || die "'$dst' hashes $got after replacement, expected ${WANT_SHA[$i]}"
  printf '  replaced %-18s on-disk sha256 OK\n' "$b"
  i=$((i+1))
done

echo "== restart =="
i=0
while [ $i -lt "${#UNIT_KEYS[@]}" ]; do
  u="${UNIT_VALS[$i]}"
  do_restart "$u" || die "restarting $u failed"
  printf '  restarted %s\n' "$u"
  i=$((i+1))
done

echo "== verify (identity, not health) =="
fail=0
i=0
while [ $i -lt "${#UNIT_KEYS[@]}" ]; do
  b="${UNIT_KEYS[$i]}"; u="${UNIT_VALS[$i]}"; want="${WANT_SHA[$i]}"
  disk="$(sha_of "$BIN_DIR/$b")"
  state="$(unit_state "$u")"
  exe="$(running_exe "$u" || true)"
  run=""
  [ -n "$exe" ] && run="$(sha_of "$exe")"

  ok=1
  [ "$disk" = "$want" ] || ok=0
  [ -n "$run" ] && [ "$run" = "$want" ] || ok=0

  if [ "$ok" -eq 1 ]; then
    printf '  PASS %-18s state=%-10s on-disk=%s running=%s\n' "$b" "$state" "${disk:0:16}" "${run:0:16}"
  else
    fail=$((fail+1))
    printf '  FAIL %-18s state=%-10s\n' "$b" "$state"
    printf '        expected  %s\n' "$want"
    printf '        on-disk   %s\n' "${disk:-<unreadable>}"
    printf '        running   %s%s\n' "${run:-<unreadable>}" \
      "$([ -z "$run" ] && echo '  (could not identify the running executable — this is a FAILURE, not a pass)')"
  fi
  i=$((i+1))
done

if [ "$fail" -ne 0 ]; then
  echo "BINARY_ROLLBACK = FAIL ($fail of ${#UNIT_KEYS[@]})"
  exit 1
fi
echo "BINARY_ROLLBACK = PASS (${#UNIT_KEYS[@]} binaries verified on disk and in the running process)"
