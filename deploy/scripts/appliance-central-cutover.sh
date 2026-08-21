#!/usr/bin/env bash
# MOVE AN APPLIANCE FROM A CENTRAL IP TO THE CENTRAL FQDN — gated on real DNS, and reversible.
#
# An appliance provisioned before the fleet had a name dials an address. This switches it to the name in
# deploy/config/central-endpoint.env, so the next time Central moves nobody has to visit the hotel.
#
# WHY IT REFUSES MORE THAN IT DOES
#
# The dangerous version of this script is the one that edits the configuration and restarts. If the name
# does not resolve, or resolves somewhere unexpected, or Central's certificate does not carry it, the
# appliance loses Central the moment scd restarts -- and it loses it in the quiet way: registration and
# heartbeat simply stop, the appliance keeps serving guests, and nobody finds out until someone asks why a
# site went dark in the fleet view. So every precondition is checked first, and the switch is rolled back if
# the appliance cannot reach Central afterwards.
#
# AN /etc/hosts ENTRY IS NOT DNS, AND THIS SCRIPT SAYS SO.
#
# A hosts line makes the name work on exactly one machine. Cutting over on the strength of one would mean
# the next appliance -- which has no such line -- silently cannot reach Central at all. The resolution check
# therefore queries DNS directly, bypassing the hosts file, and refuses if the only thing making the name
# work is local.
#
# Usage (as root on the appliance):
#   appliance-central-cutover.sh --check     preconditions only, change nothing   (default)
#   appliance-central-cutover.sh --apply     cut over, verify, roll back on failure
#   appliance-central-cutover.sh --rollback  return to the previous configuration
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="${DEPLOY:-$(cd "$HERE/.." && pwd)}"
ENV_FILE="${SCD_ENV_FILE:-/etc/stayconnect/scd.env}"
HOSTS="${HOSTS_FILE:-/etc/hosts}"

say()  { echo "[cutover] $*"; }
die()  { echo "[cutover] ABORT: $*" >&2; exit 1; }
ok()   { printf '  \033[32mOK\033[0m    %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; }

MODE="${1:---check}"

# shellcheck source=lib-central-endpoint.sh
. "$HERE/lib-central-endpoint.sh"
sc_load_central_endpoint "$DEPLOY" >/dev/null
sc_validate_central_base "$CENTRAL_BASE"
MTLS="${CENTRAL_MTLS_BASE:-${CENTRAL_BASE}:9443}"

host_of() { local u="${1#https://}"; u="${u%%/*}"; echo "${u%%:*}"; }
port_of() { local u="${1#https://}"; u="${u%%/*}"; case "$u" in *:*) echo "${u##*:}" ;; *) echo 443 ;; esac; }

FQDN="$(host_of "$CENTRAL_BASE")"
MTLS_HOST="$(host_of "$MTLS")"
MTLS_PORT="$(port_of "$MTLS")"

# ---------------------------------------------------------------- rollback
if [ "$MODE" = "--rollback" ]; then
  [ "$(id -u)" = 0 ] || die "run as root"
  prev="$(ls -1t "$ENV_FILE".pre-cutover.* 2>/dev/null | head -1 || true)"
  [ -n "$prev" ] || die "no pre-cutover backup of $ENV_FILE to roll back to"
  cp -a "$prev" "$ENV_FILE"
  say "restored $ENV_FILE from $prev"
  systemctl restart stayconnect-scd
  say "scd restarted on the previous endpoint"
  exit 0
fi

# ---------------------------------------------------------------- preconditions
fail=0
say "target: $CENTRAL_BASE   (mTLS $MTLS)"
echo
echo "Resolution"

# 1. REAL DNS. sc_dns_lookup queries the upstream nameservers, never the local stub -- on a
#    systemd-resolved host the stub answers from /etc/hosts, so "dig" alone proves nothing.
sc_dns_lookup "$FQDN" || true
dns_ip="${SC_DNS_IP:-}"
case "${SC_DNS_ANSWERED_BY:-}" in
  none-identifiable)
    bad "cannot identify a real nameserver to query (only a loopback stub, which reads /etc/hosts).
        Set CENTRAL_DNS_SERVER=<ip> so a hosts entry cannot be mistaken for a published record."
    fail=1 ;;
  dig-missing)
    bad "dig is not installed — cannot distinguish DNS from an /etc/hosts entry. Install dnsutils."
    fail=1 ;;
esac

hosts_line=""
if [ -f "$HOSTS" ]; then
  hosts_line="$(grep -nE "^[^#]*[[:space:]]${FQDN}([[:space:]]|$)" "$HOSTS" 2>/dev/null | head -1 || true)"
fi

if [ -n "$dns_ip" ]; then
  ok "$FQDN resolves in DNS -> $dns_ip (via ${SC_DNS_ANSWERED_BY})"
  # UNANIMITY IS REQUIRED, not a majority and not "at least one".
  #
  # systemd-resolved accepts NXDOMAIN as a valid answer and does not try the next server. If one configured
  # resolver knows this name and another denies it, the appliance resolves it or does not depending on which
  # server resolved is currently using -- and it changes that on its own. The cutover would appear to work,
  # and Central would drop out later for no visible reason.
  if [ -n "${SC_DNS_DENIERS:-}" ]; then
    bad "these configured resolvers return NXDOMAIN for $FQDN: ${SC_DNS_DENIERS}
        Resolution would be a coin toss: systemd-resolved treats NXDOMAIN as a real answer and does not
        fall back to the next server. Remove them from this appliance's resolver list, or make them
        resolve the name, before cutting over."
    fail=1
  else
    ok "every configured resolver agrees (no NXDOMAIN from any of them)"
  fi
  if [ -n "$hosts_line" ]; then
    hosts_ip="$(printf '%s' "$hosts_line" | sed 's/^[0-9]*://' | awk '{print $1}')"
    if [ "$hosts_ip" != "$dns_ip" ]; then
      bad "$HOSTS pins $FQDN to $hosts_ip but DNS says $dns_ip — the stopgap now DISAGREES with DNS.
        Whichever is wrong, the appliance is using the hosts file. Resolve this before cutting over."
      fail=1
    else
      ok "the $HOSTS stopgap agrees with DNS and will be removed"
    fi
  fi
elif [ "$fail" = 0 ]; then
  bad "$FQDN does NOT resolve in DNS."
  if [ -n "$hosts_line" ]; then
    say "        It currently works on this appliance ONLY because of $HOSTS:${hosts_line%%:*}"
    say "        That line exists on this machine and nowhere else. Cutting over on the strength of it"
    say "        would leave every future appliance unable to reach Central at all."
  fi
  say "        Publish an A record for $FQDN before cutting over."
  fail=1
fi

echo
echo "Reachability (certificate verification ON — no -k)"
if [ "$fail" = 0 ] || [ -n "$dns_ip" ]; then
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$CENTRAL_BASE/healthz" 2>/dev/null || true)"
  if [ "$code" = "200" ]; then
    ok "$CENTRAL_BASE/healthz -> 200, certificate verified"
  else
    bad "$CENTRAL_BASE/healthz -> ${code:-no response}"
    curl -sv --max-time 10 "$CENTRAL_BASE/healthz" 2>&1 | grep -iE "SSL certificate problem|unable to get local issuer|does not match|Could not resolve" | head -2 | sed 's/^/        /'
    fail=1
  fi
  # The mTLS surface is a different port and a different certificate; an appliance needs both.
  if echo | openssl s_client -connect "$MTLS_HOST:$MTLS_PORT" -servername "$FQDN" 2>/dev/null \
       | openssl x509 -noout -checkhost "$FQDN" 2>/dev/null | grep -q "does match"; then
    ok "mTLS listener on $MTLS_HOST:$MTLS_PORT presents a certificate valid for $FQDN"
  else
    bad "mTLS listener on $MTLS_HOST:$MTLS_PORT does not present a certificate valid for $FQDN.
        Re-issue it on Central: deploy/scripts/central-mint-tls.sh, and check CTRLAPI_APPLIANCE_BASE."
    fail=1
  fi
else
  say "  (skipped — the name does not resolve)"
fi

echo
if [ "$fail" != 0 ]; then
  say "NOT READY — nothing was changed."
  exit 1
fi
say "READY to cut over."
[ "$MODE" = "--apply" ] || { say "Re-run with --apply to make the change."; exit 0; }

# ---------------------------------------------------------------- apply
[ "$(id -u)" = 0 ] || die "run as root"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
cp -a "$ENV_FILE" "$ENV_FILE.pre-cutover.$STAMP"
say "kept $ENV_FILE.pre-cutover.$STAMP for rollback"

sed -i "s#^SCD_CTRLAPI_BASE=.*#SCD_CTRLAPI_BASE=${CENTRAL_BASE}#" "$ENV_FILE"
sed -i "s#^SCD_MTLS_BASE=.*#SCD_MTLS_BASE=${MTLS}#" "$ENV_FILE"
grep -E "^SCD_(CTRLAPI|MTLS)_BASE=" "$ENV_FILE" | sed 's/^/  /'

# The stopgap goes only now, and only because real DNS has been proven to answer with the same address.
if [ -n "$hosts_line" ]; then
  cp -a "$HOSTS" "$HOSTS.pre-cutover.$STAMP"
  sed -i "/^[^#]*[[:space:]]${FQDN}\([[:space:]]\|$\)/d" "$HOSTS"
  say "removed the $HOSTS stopgap (backup at $HOSTS.pre-cutover.$STAMP)"
fi

systemctl restart stayconnect-scd
say "scd restarted; waiting for it to reach Central"
sleep 12

# VERIFY, AND UNDO IF IT DID NOT WORK. Losing Central is silent, so it must not be left to a human noticing.
if journalctl -u stayconnect-scd --no-pager --since "-2min" 2>/dev/null \
     | grep -qE '"msg":"(identity loaded|assignment: agent started)"'; then
  say ""
  say "CUT OVER. This appliance now reaches Central by name."
  journalctl -u stayconnect-scd --no-pager --since "-2min" 2>/dev/null \
    | grep -oE '"msg":"(identity loaded|assignment: agent started)"[^}]*' | tail -2 | sed 's/^/  /'
  exit 0
fi

say "verification FAILED after the switch — rolling back rather than leaving this appliance cut off"
cp -a "$ENV_FILE.pre-cutover.$STAMP" "$ENV_FILE"
[ -f "$HOSTS.pre-cutover.$STAMP" ] && cp -a "$HOSTS.pre-cutover.$STAMP" "$HOSTS"
systemctl restart stayconnect-scd
die "rolled back to the previous endpoint. Investigate before retrying."
