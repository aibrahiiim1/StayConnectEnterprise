#!/usr/bin/env bash
# REMOVE RESOLVERS THAT DENY THE CENTRAL ENDPOINT.
#
# An appliance reaches Central by name. If one of its configured resolvers knows that name and another
# returns NXDOMAIN for it, the appliance resolves Central or does not depending on which server
# systemd-resolved happens to be using -- and resolved switches between them by itself.
#
# THIS IS NOT A FAILOVER SITUATION. resolved falls back to the next server on a timeout or SERVFAIL, because
# those mean "no answer". NXDOMAIN means "there is no such name", which is a real answer, so it is accepted
# and returned. A public resolver is therefore not a harmless extra entry for an internal name: it is an
# authoritative denial competing with the truth, and it wins about half the time.
#
# The symptom is the worst kind. Registration and heartbeat stop, guests are unaffected, the appliance logs
# a connection error that looks like a network blip, and it comes back on its own when resolved rotates
# servers. Nobody can reproduce it.
#
# So: any resolver that cannot resolve the Central endpoint is removed from this appliance's netplan. The
# ones that remain must be able to resolve public names too, and that is checked before anything is written.
#
# Usage (as root on the appliance):
#   appliance-dns-align.sh            report only          (default)
#   appliance-dns-align.sh --apply    remove the deniers, verify, roll back if DNS breaks
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="${DEPLOY:-$(cd "$HERE/.." && pwd)}"
NETPLAN_DIR="${NETPLAN_DIR:-/etc/netplan}"
PROBE_PUBLIC="${DNS_PROBE_PUBLIC:-github.com}"

say() { echo "[dns-align] $*"; }
die() { echo "[dns-align] ABORT: $*" >&2; exit 1; }
ok()  { printf '  \033[32mOK\033[0m    %s\n' "$*"; }
bad() { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; }

MODE="${1:---report}"

# shellcheck source=lib-central-endpoint.sh
. "$HERE/lib-central-endpoint.sh"
sc_load_central_endpoint "$DEPLOY" >/dev/null
sc_validate_central_base "$CENTRAL_BASE"
FQDN="${CENTRAL_BASE#https://}"; FQDN="${FQDN%%/*}"; FQDN="${FQDN%%:*}"

sc_dns_lookup "$FQDN" || true
say "Central endpoint: $FQDN"
if [ -n "${SC_DNS_IP:-}" ]; then
  ok "resolves to ${SC_DNS_IP} via ${SC_DNS_ANSWERED_BY}"
else
  die "no configured resolver can resolve $FQDN. Publish the record first — removing resolvers here would
    leave this appliance with no way to reach Central at all."
fi

DENIERS="${SC_DNS_DENIERS:-}"
if [ -z "$DENIERS" ]; then
  ok "every configured resolver answers for $FQDN — nothing to align"
  exit 0
fi

say ""
say "these resolvers DENY $FQDN and would compete with the ones that do not:"
for d in $DENIERS; do printf '    %s\n' "$d"; done

# The keepers must still serve the public internet: apt, container registries and ACME all depend on it.
KEEP=""
for s in $(printf '%s\n' "${SC_DNS_ANSWERED_BY}" ); do KEEP="$KEEP $s"; done
say ""
say "checking that the remaining resolver(s) can still resolve public names ($PROBE_PUBLIC):"
for s in $KEEP; do
  if dig "@$s" +short +time=3 +tries=1 "$PROBE_PUBLIC" A 2>/dev/null | grep -qE '^[0-9]+\.'; then
    ok "$s resolves $PROBE_PUBLIC"
  else
    bad "$s cannot resolve $PROBE_PUBLIC. Removing the public resolvers would break apt, container pulls
        and certificate issuance on this appliance. Fix forwarding on $s first."
    exit 1
  fi
done

[ "$MODE" = "--apply" ] || { say ""; say "report only — re-run with --apply to remove them."; exit 0; }
[ "$(id -u)" = 0 ] || die "run as root"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
changed=0
for f in "$NETPLAN_DIR"/*.yaml; do
  [ -f "$f" ] || continue
  for d in $DENIERS; do
    if grep -qE "^[[:space:]]*-[[:space:]]*${d}[[:space:]]*$" "$f"; then
      [ -f "$f.pre-dns-align.$STAMP" ] || cp -a "$f" "$f.pre-dns-align.$STAMP"
      # Commented, not deleted: the next person needs to see that it was removed on purpose.
      sed -i "s|^\([[:space:]]*\)-[[:space:]]*${d}[[:space:]]*$|\1# removed ${STAMP} by appliance-dns-align.sh: returns NXDOMAIN for ${FQDN}\n\1#  - ${d}|" "$f"
      say "removed $d from $(basename "$f")"
      changed=1
    fi
  done
done

if [ "$changed" = 0 ]; then
  say "no netplan file lists those resolvers — they may come from DHCP. Check with: resolvectl status"
  exit 1
fi

say "applying netplan"
netplan apply
sleep 4
systemctl restart systemd-resolved 2>/dev/null || true
sleep 3

# VERIFY BOTH DIRECTIONS: the internal name and the public internet.
fail=0
sc_dns_lookup "$FQDN" || true
if [ -n "${SC_DNS_IP:-}" ] && [ -z "${SC_DNS_DENIERS:-}" ]; then
  ok "$FQDN resolves via ${SC_DNS_ANSWERED_BY}, with no resolver denying it"
else
  bad "$FQDN resolution is still not unanimous after the change"
  fail=1
fi
if getent hosts "$PROBE_PUBLIC" >/dev/null 2>&1; then
  ok "public DNS still works ($PROBE_PUBLIC)"
else
  bad "public DNS is broken after the change"
  fail=1
fi

if [ "$fail" != 0 ]; then
  say "rolling back rather than leaving this appliance with broken DNS"
  for f in "$NETPLAN_DIR"/*.pre-dns-align."$STAMP"; do
    [ -f "$f" ] || continue
    cp -a "$f" "${f%.pre-dns-align.$STAMP}"
  done
  netplan apply; sleep 3
  die "rolled back. Investigate before retrying."
fi

say ""
say "ALIGNED. Every resolver this appliance uses can resolve $FQDN."
resolvectl status 2>/dev/null | grep -iE "DNS Servers|Current DNS Server" | head -3 | sed 's/^/  /'
