#!/usr/bin/env bash
# CREATE THE FIRST ADMIN CONSOLE ADMINISTRATOR — the supported, secure first-run step.
#
# There is no setup wizard and no default account (both were removed on purpose: a default password is the same on
# every appliance). The first site administrator is created here, on the appliance, by root:
#
#   * the password never appears on a command line or in the process list: it is read with the terminal echo off
#     (asked twice), or from a 0600 file, and handed to `edged seed-admin --password-stdin`;
#   * at least 12 characters (edged's own floor is 10; this is the first account of a site, so the bar is higher);
#   * it refuses when an active site administrator already exists, unless --reset is given — which is then the
#     documented break-glass "reset this administrator's password" (it also clears that account's sign-in lockout);
#   * it connects with the Admin Console's own least-privilege database role from /etc/stayconnect/edged.env.
#
# Usage (root on the appliance):
#   create-first-admin.sh --email admin@example.com [--name "Site Admin"] [--password-file /root/pw] [--reset]
# Exit: 0 created · 1 refused · 2 failed
set -uo pipefail
EMAIL=""; NAME="Site Admin"; PWFILE=""; RESET=0
while [ $# -gt 0 ]; do
  case "$1" in
    --email) EMAIL="${2:?}"; shift 2;;
    --name) NAME="${2:?}"; shift 2;;
    --password-file) PWFILE="${2:?}"; shift 2;;
    --reset) RESET=1; shift;;
    -h|--help) sed -n '2,20p' "$0"; exit 0;;
    *) echo "create-first-admin: unknown argument $1" >&2; exit 1;;
  esac
done
say() { echo "[first-admin] $*"; }
refuse() { echo "[first-admin] REFUSED: $*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || refuse "run as root"
[ -n "$EMAIL" ] || refuse "--email is required"
# One @, a dotted domain, nothing else: a pasted "email: x@y" or a stray space would otherwise become the sign-in name.
printf '%s' "$EMAIL" | grep -Eq '^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$' \
  || refuse "--email must be a plain email address such as admin@example.com (it is the sign-in name); got: $EMAIL"
ENVF=/etc/stayconnect/edged.env
EDGED=/opt/stayconnect/bin/edged
[ -r "$ENVF" ] || refuse "$ENVF is missing — provision the appliance first"
[ -x "$EDGED" ] || refuse "$EDGED is missing"

existing="$(docker exec -i stayconnect-pg psql -X -tAq -U stayconnect -d stayconnect_site -c \
  "SELECT count(*) FROM operators o JOIN operator_roles r ON r.operator_id=o.id
    WHERE r.role IN ('site_admin','tenant_admin') AND o.status='active'" </dev/null 2>/dev/null)" \
  || refuse "cannot read the operator table (is the site database up?)"
# The address goes in as a psql variable on stdin (interpolated and quoted by psql, never spliced into SQL).
mine="$(printf '%s\n' "SELECT count(*) FROM operators WHERE lower(email)=lower(:'e');" \
  | docker exec -i stayconnect-pg psql -X -tAq -U stayconnect -d stayconnect_site -v e="$EMAIL" 2>/dev/null || echo 0)"
if [ "${existing:-0}" != 0 ] && [ "$RESET" != 1 ] && [ "${mine:-0}" != 0 ]; then
  # A re-run of the installer with the same administrator: nothing to do, and not an error.
  say "site administrator $EMAIL already exists — unchanged (use --reset to set a new password)"
  exit 0
fi
if [ "${existing:-0}" != 0 ] && [ "$RESET" != 1 ]; then
  refuse "this appliance already has $existing active site administrator(s). To reset one's password use --reset
         (break-glass, audited by the journal); further accounts are created in the Admin Console -> Operators."
fi
if [ "$RESET" = 1 ] && [ "${mine:-0}" = 0 ]; then
  refuse "--reset names $EMAIL, which is not an existing operator"
fi

if [ -n "$PWFILE" ]; then
  [ -f "$PWFILE" ] || refuse "$PWFILE does not exist"
  perm="$(stat -c '%a' "$PWFILE")"
  case "$perm" in 600|400) ;; *) refuse "$PWFILE is mode $perm; a password file must be 0600 or 0400";; esac
  PW="$(head -n1 "$PWFILE")"
else
  [ -t 0 ] || refuse "no terminal to ask for the password; use --password-file <0600 file>"
  read -r -s -p "New administrator password for $EMAIL: " PW; echo
  read -r -s -p "Repeat the password: " PW2; echo
  [ "$PW" = "$PW2" ] || refuse "the two passwords differ"
  unset PW2
fi
[ "${#PW}" -ge 12 ] || refuse "the password must be at least 12 characters"
lower="$(printf '%s' "$PW" | tr '[:upper:]' '[:lower:]')"
[ "$lower" != "$(printf '%s' "$EMAIL" | tr '[:upper:]' '[:lower:]')" ] || refuse "the password must not be the account name"

set -a; . "$ENVF"; set +a
if printf '%s\n' "$PW" | "$EDGED" seed-admin --email "$EMAIL" --name "$NAME" --password-stdin; then
  unset PW
  if [ "$RESET" = 1 ]; then say "password reset for $EMAIL (its sign-in lockout is cleared)"; else say "created site administrator $EMAIL"; fi
  say "sign in at https://<management address>/ — and turn on two-step sign-in (Account -> Security) right away"
  exit 0
fi
unset PW
echo "[first-admin] FAILED: edged seed-admin did not complete (journal: journalctl -t edged -n 20)" >&2
exit 2
