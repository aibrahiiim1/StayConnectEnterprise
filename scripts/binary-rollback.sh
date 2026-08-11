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
set -uo pipefail

BIN_DIR=""; SRC_SUFFIX=""; SRC_DIR=""; DRY=0
declare -a UNIT_KEYS=() UNIT_VALS=()

die() { printf 'binary-rollback: FATAL: %s\n' "$*" >&2; exit 1; }

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
