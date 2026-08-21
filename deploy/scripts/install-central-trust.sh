#!/usr/bin/env bash
# TEACH AN APPLIANCE TO TRUST CENTRAL'S TLS CERTIFICATE.
#
# Central presents a certificate from a private CA (StayConnect Internal CA). An appliance that does not
# have that CA in its system trust store cannot verify it, and every outbound call fails.
#
# WHY THIS IS NOT OPTIONAL, AND WHY IT WAS EASY TO MISS
#
# The appliance's self-registration is deliberately non-fatal: if Central cannot be reached it gives up
# quietly and retries later, because a hotel's uplink being down at 3am is not a reason to refuse to boot.
# A TLS verification failure takes that same path. The result on a live appliance was a log line reading
# "awaiting enrollment: no appliance identity; run the local setup wizard" -- which is not wrong, but sends
# the operator to a wizard when the real problem is that the appliance never trusted Central at all. It had
# never registered, and nobody could tell from the appliance why.
#
# Every check here was found the hard way on a Production appliance that had been running for a day.
#
# Idempotent, and refuses to install anything that is not a CA certificate.
#
# Usage (as root on the appliance):
#   install-central-trust.sh <central-ca.crt>   install and refresh the system trust store
#   install-central-trust.sh --show             what is installed now
#   install-central-trust.sh --verify <url>     prove the appliance can now verify Central
set -euo pipefail

DEST="${CENTRAL_CA_PATH:-/usr/local/share/ca-certificates/stayconnect-central-ca.crt}"
say() { echo "[central-trust] $*"; }
die() { echo "[central-trust] ABORT: $*" >&2; exit 1; }

if [ "${1:-}" = "--show" ]; then
  [ -s "$DEST" ] || die "no Central CA installed at $DEST"
  say "installed: $DEST"
  say "subject: $(openssl x509 -in "$DEST" -noout -subject | sed 's/^subject=//')"
  say "expires: $(openssl x509 -in "$DEST" -noout -enddate | sed 's/^notAfter=//')"
  exit 0
fi

if [ "${1:-}" = "--verify" ]; then
  url="${2:-}"
  [ -n "$url" ] || die "usage: $0 --verify https://<central>"
  # No -k. The whole point is that verification succeeds on its own merits.
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$url/healthz" || true)"
  if [ "$code" = "200" ]; then
    say "VERIFIED: $url/healthz answered 200 with certificate verification ON"
    exit 0
  fi
  say "could not verify $url (http_code=$code)"
  curl -sv --max-time 10 "$url/healthz" 2>&1 | grep -iE "SSL certificate problem|unable to get local issuer|subjectAltName|does not match" | head -3
  exit 1
fi

SRC="${1:-}"
[ -n "$SRC" ] || die "usage: $0 <central-ca.crt> | --show | --verify <url>"
[ -f "$SRC" ] || die "no such file: $SRC"
[ "$(id -u)" = 0 ] || die "run as root"

openssl x509 -in "$SRC" -noout >/dev/null 2>&1 || die "$SRC is not a PEM certificate"
# A server certificate installed as a trust anchor would silently fail to validate anything.
certtext="$(openssl x509 -in "$SRC" -noout -text 2>/dev/null || true)"
printf '%s' "$certtext" | grep "CA:TRUE" >/dev/null 2>&1 || die "$SRC is not a CA certificate (basicConstraints CA:TRUE missing)"

subject="$(openssl x509 -in "$SRC" -noout -subject | sed 's/^subject=//')"
if [ -s "$DEST" ] && cmp -s "$SRC" "$DEST"; then
  say "already installed and identical: $subject"
else
  install -o root -g root -m 0644 "$SRC" "$DEST"
  say "installed $DEST"
  say "subject: $subject"
fi

command -v update-ca-certificates >/dev/null || die "update-ca-certificates not found (Debian/Ubuntu expected)"
update-ca-certificates >/dev/null 2>&1
say "system trust store refreshed"
say ""
say "Services already running keep their old trust store: restart the ones that dial Central."
say "  systemctl restart stayconnect-scd"
