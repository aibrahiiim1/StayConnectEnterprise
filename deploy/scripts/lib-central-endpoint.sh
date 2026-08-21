#!/usr/bin/env bash
# THE ONE PLACE THE CENTRAL ENDPOINT IS RESOLVED AND VALIDATED.
#
# Sourced by appliance provisioning and by Central preflight so both sides agree by construction. Two
# copies of this validation would eventually disagree, and the disagreement would be discovered at a hotel.
#
# Provides:
#   sc_load_central_endpoint [deploy-dir]   env wins, then deploy/config/central-endpoint.env, then fail
#   sc_validate_central_base <url>          https, a NAME, no path — the rules an appliance depends on
#   sc_report_central_dns <url>             warn-only: does this name actually resolve, and how
#
# Callers define say()/die() before sourcing; fallbacks are provided.

if ! command -v say >/dev/null 2>&1; then
  say() { echo "[central] $*"; }
fi
if ! command -v die >/dev/null 2>&1; then
  die() { echo "[central] ABORT: $*" >&2; exit 1; }
fi

sc_central_config_file() {
  # An explicit path wins, so a caller can validate a file before installing it.
  if [ -n "${CENTRAL_ENDPOINT_CONFIG:-}" ]; then
    echo "$CENTRAL_ENDPOINT_CONFIG"
    return
  fi
  local deploy="${1:-${DEPLOY:-/opt/stayconnect/deploy}}"
  echo "$deploy/config/central-endpoint.env"
}

# sc_load_central_endpoint sets CENTRAL_BASE / CENTRAL_MTLS_BASE / CTRLAPI_APPLIANCE_BASE unless they are
# already set in the environment. Precedence is deliberate: an explicit environment variable is an operator
# saying "this install is different", and the versioned file is what every normal install gets.
sc_load_central_endpoint() {
  local cfg; cfg="$(sc_central_config_file "${1:-}")"
  local from_env=""
  [ -n "${CENTRAL_BASE:-}" ] && from_env=" (CENTRAL_BASE from the environment)"

  if [ -f "$cfg" ]; then
    # Read assignments only. Sourcing the file would execute whatever is in it.
    local line key val
    while IFS= read -r line; do
      case "$line" in ''|'#'*) continue ;; esac
      case "$line" in *=*) ;; *) continue ;; esac
      key="${line%%=*}"; val="${line#*=}"
      key="$(printf '%s' "$key" | tr -d '[:space:]')"
      val="$(printf '%s' "$val" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//; s/^"//; s/"$//')"
      case "$key" in
        CENTRAL_BASE|CENTRAL_MTLS_BASE|CTRLAPI_APPLIANCE_BASE)
          [ -n "$(eval "printf '%s' \"\${$key:-}\"")" ] || export "$key=$val" ;;
      esac
    done < "$cfg"
  fi

  if [ -z "${CENTRAL_BASE:-}" ]; then
    die "no Central endpoint configured.
    Expected $cfg to define CENTRAL_BASE, or CENTRAL_BASE to be set in the environment.
    There is deliberately no built-in default: a guessed endpoint would point appliances at DNS that may
    not exist, and nobody would find out until a hotel was already installed."
  fi
  say "Central endpoint: ${CENTRAL_BASE}${from_env}"
}

# sc_validate_central_base enforces the properties an appliance's whole life depends on.
sc_validate_central_base() {
  local url="$1" what="${2:-CENTRAL_BASE}"
  case "$url" in
    https://*) ;;
    *) die "$what must be an https:// URL (got: $url). Appliances carry licences and identity over it." ;;
  esac
  local hostport="${url#https://}"; hostport="${hostport%%/*}"
  local host="${hostport%%:*}"
  [ -n "$host" ] || die "$what has no hostname: $url"
  # AN IP WOULD WORK TODAY AND STRAND THE FLEET LATER. Refused at deployment time, where one person can fix
  # it, rather than discovered during a Central migration, where every property would have to be visited.
  case "$host" in
    [0-9]*.[0-9]*.[0-9]*.[0-9]*)
      die "$what must be a hostname, not an IP address (got: $host).
      Appliances that learn an IP cannot be moved to new Central hosting without visiting every hotel." ;;
  esac
  case "$host" in
    *.*) ;;
    localhost) die "$what must not be localhost: appliances dial this from other networks" ;;
    *) die "$what must be a fully-qualified name (got: $host); a short name is not resolvable from a hotel" ;;
  esac
  # A path here would be silently concatenated with every API route.
  local rest="${url#https://$hostport}"
  case "$rest" in
    ''|'/') ;;
    *) die "$what must be a bare origin with no path (got: $url)" ;;
  esac
  case "$url" in
    */) die "$what must not end with a slash (got: $url): routes are appended to it" ;;
  esac
}

# sc_dns_lookup resolves a host against REAL nameservers and reports the outcome in globals:
#
#	SC_DNS_IP           the A record, empty when DNS does not have the name
#	SC_DNS_ANSWERED_BY  which server answered (or why no answer was possible)
#	SC_DNS_DENIERS      configured servers that returned NXDOMAIN for it
#
# Returns non-zero when nothing resolved.
#
# IT SETS GLOBALS RATHER THAN PRINTING because `x="$(sc_dns_lookup ...)"` runs the function in a subshell,
# where every variable it sets dies with that subshell. The caller then reads an unset variable -- under
# `set -u` that aborts, and without it the check silently sees an empty denier list and passes.
#
# THE TRAP THIS EXISTS TO AVOID, WHICH CAUGHT THIS SCRIPT ITSELF:
#
# "Use dig instead of getent, because dig bypasses /etc/hosts" is only true when dig talks to a real DNS
# server. On a systemd-resolved host /etc/resolv.conf points at the stub 127.0.0.53, and the stub answers
# from /etc/hosts -- so `dig sc-central.example` cheerfully returned the hosts entry and the check reported
# "resolves in DNS" for a name every actual nameserver called NXDOMAIN. The hosts file was being laundered
# through a DNS-shaped interface.
#
# So: enumerate the real upstream servers and query those. Never the stub, never the hosts file. If no
# non-loopback server can be identified, say so rather than guessing -- an unverifiable answer is not an
# answer.
sc_dns_lookup() {
  local host="$1" servers="" s ip
  SC_DNS_IP=""; SC_DNS_ANSWERED_BY=""; SC_DNS_DENIERS=""

  if [ -n "${CENTRAL_DNS_SERVER:-}" ]; then
    servers="$CENTRAL_DNS_SERVER"
  else
    if command -v resolvectl >/dev/null 2>&1; then
      servers="$(resolvectl status 2>/dev/null | sed -n 's/.*DNS Servers: //p; s/.*Current DNS Server: //p' | tr ' ' '\n')"
    fi
    if [ -z "$servers" ] && [ -f /etc/resolv.conf ]; then
      servers="$(awk '/^nameserver/ {print $2}' /etc/resolv.conf)"
    fi
  fi

  # Drop loopback: that is the stub, and the stub reads /etc/hosts.
  servers="$(printf '%s\n' $servers | grep -vE '^(127\.|::1$)' | grep -E '^[0-9a-fA-F:.]+$' | sort -u)"
  if [ -z "$servers" ]; then
    SC_DNS_ANSWERED_BY="none-identifiable"
    return 1
  fi

  command -v dig >/dev/null 2>&1 || { SC_DNS_ANSWERED_BY="dig-missing"; return 1; }

  # EVERY configured server is queried, not just until one answers.
  #
  # systemd-resolved does not fail over on NXDOMAIN -- that is a valid answer, so it is accepted and
  # returned. A host configured with one resolver that knows an internal name and another that
  # authoritatively denies it therefore resolves that name or not depending on which server resolved
  # happens to be using, and it switches between them on its own. "Some upstream knows it" is not what an
  # application experiences; it is a coin toss that looks fine whenever you test it and fails later.
  #
  # SC_DNS_DENIERS lists the servers that deny the name, so a caller can refuse rather than gamble.
  local found=""
  for s in $servers; do
    ip="$(dig "@$s" +short +time=3 +tries=1 "$host" A 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)"
    if [ -n "$ip" ]; then
      [ -n "$found" ] || { found="$ip"; SC_DNS_ANSWERED_BY="$s"; }
    else
      SC_DNS_DENIERS="${SC_DNS_DENIERS}${SC_DNS_DENIERS:+ }$s"
    fi
  done
  if [ -n "$found" ]; then
    SC_DNS_IP="$found"
    return 0
  fi
  SC_DNS_ANSWERED_BY="$(printf '%s' "$servers" | tr '\n' ' ')"
  return 1
}

# sc_report_central_dns is INFORMATIONAL and never fatal. An appliance is legitimately provisioned before
# DNS exists, and an offline-first-activation appliance may have no route to Central at all. What it must
# not do is let an /etc/hosts entry pass unremarked as if the name were really resolvable.
sc_report_central_dns() {
  local url="$1"
  local hostport="${url#https://}"; hostport="${hostport%%/*}"
  local host="${hostport%%:*}"

  local in_hosts=0
  if [ -f /etc/hosts ] && grep -qiE "^[^#]*[[:space:]]${host}([[:space:]]|$)" /etc/hosts 2>/dev/null; then
    in_hosts=1
  fi

  sc_dns_lookup "$host" || true
  local dns_ip="${SC_DNS_IP:-}"

  if [ -n "$dns_ip" ]; then
    say "$host resolves in DNS -> $dns_ip (answered by ${SC_DNS_ANSWERED_BY:-a real nameserver})"
    if [ -n "${SC_DNS_DENIERS:-}" ]; then
      say "NOTE: these resolvers DENY the name: ${SC_DNS_DENIERS}. systemd-resolved accepts NXDOMAIN as a"
      say "      real answer and does not try the next server, so resolution here is not deterministic."
    fi
    if [ "$in_hosts" = 1 ]; then
      say "NOTE: /etc/hosts also pins $host. DNS answers now, so the local line is redundant and will"
      say "      shadow DNS if the address ever changes. Remove it: appliance-central-cutover.sh"
    fi
  elif [ "$SC_DNS_ANSWERED_BY" = "none-identifiable" ] || [ "$SC_DNS_ANSWERED_BY" = "dig-missing" ]; then
    say "NOTE: cannot check DNS for $host (${SC_DNS_ANSWERED_BY/-/ }). Install dnsutils, or set"
    say "      CENTRAL_DNS_SERVER, so a hosts-file entry cannot be mistaken for a published record."
  elif [ "$in_hosts" = 1 ]; then
    say "NOTE: $host resolves via /etc/hosts on THIS machine only — DNS (${SC_DNS_ANSWERED_BY}) does NOT answer for it."
    say "      That is a local stopgap, not the product's dependency. The next appliance will not have it."
    say "      Publish $host in DNS before this fleet grows."
  else
    say "NOTE: $host does not resolve here yet. That is expected for an offline install, and expected"
    say "      before the DNS record is published. This appliance will reach Central once it does;"
    say "      nothing on the appliance needs to change when that happens."
  fi
}
