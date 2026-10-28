#!/usr/bin/env bash
# ADDS A MISSING SERVICE DATABASE URL TO AN EXISTING ENV FILE, NEVER CHANGES ONE (audit Phase 10 X3 / Phase 6 L14).
#
# scd, edged, netd and acctd still fall back to a built-in database address with the published password when their
# *_DB_URL is unset. A fresh install writes the variable, but only into an env file it creates: an appliance whose
# env file predates that line keeps running on the fallback without anyone noticing. This adds the line from this
# appliance's own generated credentials ($ETC/.dsn, written by gatep-set-passwords.sh) when, and only when, the key is
# absent. A present key is the operator's or the installer's and is left exactly as it is -- even an empty one, which
# is reported instead. Run by provision-fresh-appliance.sh and appliance-install.sh --upgrade; safe to re-run.
# It removes the reason the services need a fallback; removing the fallback itself is a code change for later.
#
# Usage (root): backfill-db-urls.sh [<etc-dir>]        Exit 0 done · 1 refused (no .dsn)
# Values are never printed.
set -uo pipefail
ETC="${1:-/etc/stayconnect}"
say() { echo "[db-urls] $*"; }
DSN="$ETC/.dsn"
[ -s "$DSN" ] || { echo "[db-urls] REFUSED: $DSN is missing or empty; nothing to take the URLs from" >&2; exit 1; }

dsn_val() { sed -n "s/^$1=//p" "$DSN" | tail -1; }

backfill() { # <env file name> <key>
  local f="$ETC/$1" key="$2" v
  if [ ! -f "$f" ]; then say "$1: absent (the installer creates it); skipped"; return 0; fi
  if grep -qE "^[[:space:]]*(export[[:space:]]+)?$key=" "$f"; then
    if grep -qE "^[[:space:]]*(export[[:space:]]+)?$key=[[:space:]]*$" "$f"; then
      say "WARNING: $1 sets $key to an empty value; left unchanged (the service would use its built-in fallback)"
    fi
    return 0
  fi
  v="$(dsn_val "$key")"
  if [ -z "$v" ]; then say "WARNING: $DSN has no $key; $1 left unchanged"; return 0; fi
  # A file that does not end in a newline would glue the new line onto its last one.
  [ -s "$f" ] && [ "$(tail -c1 "$f" | od -An -c | tr -d ' ')" != '\n' ] && printf '\n' >> "$f"
  printf '%s=%s\n' "$key" "$v" >> "$f"
  say "$1: $key added from $DSN"
}

backfill scd.env   SCD_DB_URL
backfill edged.env EDGED_DB_URL
backfill netd.env  NETD_DB_URL
backfill acctd.env ACCTD_DB_URL
exit 0
