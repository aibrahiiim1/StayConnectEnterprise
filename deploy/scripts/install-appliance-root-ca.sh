#!/usr/bin/env bash
# PIN CENTRAL'S APPLIANCE ROOT CA ON AN APPLIANCE (audit decision AD-2).
#
# The appliance reaches Central over mutual TLS. The CA bundle it trusts for that (certs/ca.crt) used to be written
# from whatever Central's certificate response carried, on every reconcile and rotation. With this file installed at
# provisioning the appliance accepts only bundles and client certificates that chain to THIS root, so a wrong or
# hostile response can no longer replace its trust anchor.
#
# It is public material. Central publishes it as appliance-root-ca.crt in its public trust directory
# (/opt/stayconnect/central/appliance-trust/, written by central-install.sh).
#
# An appliance WITHOUT this file adopts the root of the CA bundle it already holds (or of the first bundle it
# installs) and pins that; installing the file at provisioning is what makes the pin independent of Central's reply.
#
# Usage (as root on the appliance):
#   install-appliance-root-ca.sh <appliance-root-ca.crt>
#   install-appliance-root-ca.sh --sha256 <fingerprint> <crt>   pin only if the certificate has EXACTLY this SHA-256
#                                                  fingerprint (the one the operator holds from Central's FINGERPRINTS.txt,
#                                                  read out of band); a mismatch pins nothing
#   install-appliance-root-ca.sh --force <crt>     replace a DIFFERENT pin (Root CA rotation, a Product-Owner decision)
#   install-appliance-root-ca.sh --show
set -euo pipefail

DEST="${SCD_APPLIANCE_ROOT_CA:-/etc/stayconnect/appliance-root-ca.crt}"
say() { echo "[appliance-root-ca] $*"; }
die() { echo "[appliance-root-ca] ABORT: $*" >&2; exit 1; }

fpr() { openssl x509 -in "$1" -noout -fingerprint -sha256 | sed 's/^.*=//'; }

if [ "${1:-}" = "--show" ]; then
  [ -s "$DEST" ] || die "no appliance Root CA pinned at $DEST"
  say "pinned: $DEST"
  say "subject: $(openssl x509 -in "$DEST" -noout -subject | sed 's/^subject=//')"
  say "sha256: $(fpr "$DEST")"
  exit 0
fi

FORCE=0; WANT=""
while [ "${1:-}" = "--force" ] || [ "${1:-}" = "--sha256" ]; do
  if [ "$1" = "--force" ]; then FORCE=1; shift
  else [ -n "${2:-}" ] || die "--sha256 needs a fingerprint"; WANT="$2"; shift 2; fi
done
SRC="${1:-}"
[ -n "$SRC" ] || die "usage: $0 [--force] <appliance-root-ca.crt> | --show"
[ -f "$SRC" ] || die "no such file: $SRC"
[ "$(id -u)" = 0 ] || die "run as root"

openssl x509 -in "$SRC" -noout >/dev/null 2>&1 || die "$SRC is not a PEM certificate"
if grep -q "PRIVATE KEY" "$SRC"; then
  die "$SRC contains a private key. Only the public Root CA certificate belongs on an appliance."
fi
printf '%s' "$(openssl x509 -in "$SRC" -noout -text)" | grep -q "CA:TRUE" || die "$SRC is not a CA certificate (CA:TRUE missing)"
subj="$(openssl x509 -in "$SRC" -noout -subject)"
iss="$(openssl x509 -in "$SRC" -noout -issuer)"
[ "${subj#subject=}" = "${iss#issuer=}" ] || die "$SRC is not a self-signed root (subject and issuer differ)"

new="$(fpr "$SRC")"
norm() { printf '%s' "$1" | tr -d ': \t\r\n' | tr 'a-f' 'A-F'; }
if [ -n "$WANT" ]; then
  [ "$(norm "$WANT")" = "$(norm "$new")" ] \
    || die "the certificate's SHA-256 fingerprint is $new, not the expected $WANT. Nothing was pinned."
  say "fingerprint matches the one supplied"
fi
if [ -s "$DEST" ]; then
  cur="$(fpr "$DEST" 2>/dev/null || echo '?')"
  if [ "$cur" = "$new" ]; then
    say "already pinned and identical ($new) -- nothing to do"
    exit 0
  fi
  [ "$FORCE" = "1" ] || die "a DIFFERENT appliance Root CA is already pinned (has $cur, new $new).
    Replacing it changes who this appliance trusts. Use --force only for a deliberate Root CA rotation."
  cp -a "$DEST" "$DEST.previous.$(date -u +%Y%m%dT%H%M%SZ)"
  say "kept the previous pin alongside for audit"
fi

install -o root -g root -m 0644 "$SRC" "$DEST.tmp"
mv -f "$DEST.tmp" "$DEST"
say "pinned $DEST"
say "sha256: $new"
say "Restart scd so it uses the pin:  systemctl restart stayconnect-scd"
