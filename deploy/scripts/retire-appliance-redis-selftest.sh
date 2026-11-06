#!/usr/bin/env bash
# Self-test of retire_appliance_redis (appliance-install.sh), taken from the installer itself and run under its shell options with a
# fake docker: a container it cannot establish as unused is kept, only an unused one is removed, and nothing aborts the upgrade.
# Run: bash deploy/scripts/retire-appliance-redis-selftest.sh
set -Eeuo pipefail
trap 'echo "ABORTED at line $LINENO"; exit 99' ERR
say() { echo "say: $*"; }
eval "$(sed -n '/^retire_appliance_redis() {/,/^}/p' "${1:-$(dirname "${BASH_SOURCE[0]}")/appliance-install.sh}")"
run() {
  export FAKE_STATE="$1" FAKE_DBSIZE="$2" FAKE_CLIENTS="$3" FAKE_EXEC_FAIL="${4:-0}"
  docker() {
    case "$1" in
      inspect) [ "$FAKE_STATE" = absent ] && return 1; echo "$FAKE_STATE";;
      exec) [ "$FAKE_EXEC_FAIL" = 1 ] && return 1; case "$4" in dbsize) echo "$FAKE_DBSIZE";; info) printf 'connected_clients:%s\r\n' "$FAKE_CLIENTS";; esac;;
      stop|rm) echo "fake $1" >&2;;
    esac
  }
  retire_appliance_redis
}
echo "== absent";            run absent 0 1
echo "== stopped";           run false 0 1
echo "== paused/exec fails"; run true 0 1 1
echo "== holds keys";        run true 5 1
echo "== has a client";      run true 0 2
echo "== unused";            run true 0 1
echo FINISHED
