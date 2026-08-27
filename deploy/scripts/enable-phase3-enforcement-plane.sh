#!/usr/bin/env bash
# Turn the Phase-3 ENFORCEMENT PLANE on, coherently, on an appliance that already runs a Phase-3 guest surface.
#
# WHY THIS IS A SCRIPT AND NOT A LINE IN A RUNBOOK.
#
# The plane is two daemons that must agree, plus one piece of authentication between them:
#
#   acctd  derives the shaping plan from durable state          — needs STAYCONNECT_PHASE3_MASTER
#   netd   applies it to the kernel and promotes the Session    — needs STAYCONNECT_PHASE3_MASTER
#   netd   accepts plans ONLY from the authenticated producer   — needs NETD_PHASE3_PRODUCER_UID
#
# Each daemon reads its own env file, so "enable Phase 3" done by hand is three edits in three files, and
# missing any one of them produces an appliance that authenticates guests, grants them entitlements and
# sessions, and enforces nothing. That is exactly what happened here: scd.env had the surface flag; netd.env
# and acctd.env had nothing; two real guests were granted access no kernel ever heard about.
#
# Idempotent: run it as often as you like. It only rewrites a file whose content would change, and it restarts
# only the services whose configuration it actually touched.
set -euo pipefail
ETC="${ETC:-/etc/stayconnect}"
UNITDIR="${UNITDIR:-/etc/systemd/system}"
APPLY="${APPLY:-1}" # APPLY=0 writes nothing and only reports what it would do

say() { printf '  %s\n' "$*"; }
die() { printf 'REFUSED: %s\n' "$*" >&2; exit 1; }

[ -d "$ETC" ] || die "$ETC does not exist"

# THE PRODUCER'S UID IS DERIVED, NEVER GUESSED. It is whichever user acctd's unit actually runs as, because
# that is the uid the kernel will report to netd over SO_PEERCRED. A hardcoded 0 would be right today and
# silently wrong the moment acctd is given its own account.
producer_user="$(sed -n 's/^User=\(.*\)$/\1/p' "$UNITDIR/stayconnect-acctd.service" 2>/dev/null | tail -1)"
producer_user="${producer_user:-root}"
producer_uid="$(id -u "$producer_user" 2>/dev/null || true)"
[ -n "$producer_uid" ] || die "cannot resolve a uid for acctd's user '$producer_user'"
say "acctd runs as $producer_user (uid $producer_uid) — that is the only producer netd will accept"

changed=""

# set_kv writes KEY=VALUE into an env file exactly once, replacing any existing line for that key.
set_kv() {
  local file="$1" key="$2" value="$3" path="$ETC/$1"
  [ -f "$path" ] || die "$path does not exist; this appliance is not provisioned"
  if grep -qE "^$key=" "$path"; then
    local current
    current="$(grep -hoE "^$key=.*" "$path" | tail -1 | cut -d= -f2-)"
    [ "$current" = "$value" ] && return 0
  fi
  say "$file: $key=$value"
  [ "$APPLY" = "1" ] || return 0
  local tmp
  tmp="$(mktemp)"
  grep -vE "^$key=" "$path" > "$tmp" || true
  printf '%s=%s\n' "$key" "$value" >> "$tmp"
  install -m 0640 -o root -g stayconnect "$tmp" "$path"
  rm -f "$tmp"
  case " $changed " in *" $file "*) ;; *) changed="$changed $file";; esac
}

set_kv netd.env  STAYCONNECT_PHASE3_MASTER true
set_kv netd.env  NETD_PHASE3_PRODUCER_UID  "$producer_uid"
set_kv acctd.env STAYCONNECT_PHASE3_MASTER true

# The coherence check is the same one the deployment runs, so this script cannot leave behind a state the
# checker would refuse.
bash "$(dirname "$0")/check-phase3-enforcement-plane.sh" "$ETC"

if [ "$APPLY" != "1" ]; then
  say "APPLY=0: nothing was written and nothing was restarted"
  exit 0
fi

# RESTART WHAT IS RUNNING STALE CONFIGURATION, which is not the same question as "did this run change a file".
#
# A daemon reads its environment once, at start. So the thing that decides whether it needs restarting is
# whether it started BEFORE its env file was last written — by this run, by a previous half-finished run, or by
# an operator's editor. Keying the restart to "did I just change something" leaves the worst case untouched: a
# file that was already correct, in front of a process that has never read it.
needs_restart() { # needs_restart <env-file> <unit>
  local envfile="$ETC/$1" unit="$2" started env_mtime
  systemctl is-active --quiet "$unit" || return 0
  started="$(systemctl show -p ActiveEnterTimestamp --value "$unit" 2>/dev/null)"
  [ -n "$started" ] || return 0
  started="$(date -d "$started" +%s 2>/dev/null || echo 0)"
  env_mtime="$(stat -c %Y "$envfile" 2>/dev/null || echo 0)"
  [ "$env_mtime" -ge "$started" ]
}

# netd before acctd: netd must be ready to accept a plan before the producer starts submitting one, or the
# first tick after the restart is a refusal in the log for no reason.
restarted=0
for pair in "netd.env stayconnect-netd" "acctd.env stayconnect-acctd"; do
  set -- $pair
  if needs_restart "$1" "$2"; then
    say "restarting $2 (running configuration predates $ETC/$1)"
    systemctl restart "$2"
    restarted=1
  fi
done
[ "$restarted" = "1" ] || say "both daemons are already running the current configuration"
say "enforcement plane enabled"
