#!/usr/bin/env bash
# INSTALL THE VENDOR PUBLIC TRUST KEY ON AN APPLIANCE.
#
# WHY THIS IS A SEPARATE, DELIBERATE STEP
#
# Offline first activation hands an appliance a file and asks it to believe what is inside: which hotel it
# belongs to, which CA to trust, what it is licensed for. The only reason it may believe any of that is that
# the file is signed by a key the appliance ALREADY trusts, pinned before the file arrived.
#
# So the trust root cannot travel with the thing it authorises. An appliance that learned its verification
# key from the activation package would accept any package that brought its own key -- which is not a check,
# it is a formality. This script is how the key gets there first.
#
# WHAT IS AND IS NOT SECRET
#
#   PUBLIC verification key   installed here, on every appliance. Not secret. Publishing it costs nothing.
#   PRIVATE signing key       CENTRAL ONLY, and never on an appliance. Anyone holding it can mint an
#                             activation package for any appliance in the fleet.
#
# This script refuses a private key outright, because the most likely way to leak one is to copy the wrong
# file while doing exactly this.
#
# HOW THE KEY SHOULD REACH THE APPLIANCE
#
# By any channel whose INTEGRITY you can verify -- it needs no confidentiality. Ship it in the appliance
# image, carry it on the installer's USB stick, or fetch it over a channel you already trust. Then confirm
# the fingerprint this script prints against the fingerprint published by whoever holds the private key,
# out of band. That comparison is the whole security of the offline path: everything else follows from it.
#
# Usage:
#   install-vendor-trust-key.sh <vendor-license.pub>          install (refuses to overwrite a different key)
#   install-vendor-trust-key.sh --show                        print the installed key's fingerprint
#   install-vendor-trust-key.sh --force <vendor-license.pub>  replace an existing key (vendor key rotation)
set -euo pipefail

DEST="${VENDOR_PUB_PATH:-/etc/stayconnect/vendor-license.pub}"
say() { echo "[vendor-trust] $*"; }
die() { echo "[vendor-trust] ABORT: $*" >&2; exit 1; }

# fingerprint prints BOTH short ids the product uses for the same key, because it uses two.
#
# Activation packages name their signer in base64url (activation.KeyID); signed licences name theirs in hex
# (license.KeyIDFor). They are the same eight bytes of SHA-256 in two encodings, and an operator holding one
# string and reading the other has no way to tell whether the key matches or not. Both are shown, always, so
# whichever form the other side displays can be compared directly.
fingerprint() {
  python3 - "$1" <<'PY'
import base64, hashlib, sys
raw = open(sys.argv[1], 'rb').read()
if len(raw) != 32:
    sys.exit("not a 32-byte ed25519 public key")
d = hashlib.sha256(raw).digest()[:8]
print("%s  (licence key_id %s)" % (
    base64.urlsafe_b64encode(d).decode().rstrip('='), d.hex()))
PY
}

if [ "${1:-}" = "--show" ]; then
  [ -s "$DEST" ] || die "no vendor trust key is installed at $DEST"
  say "installed vendor trust key: $DEST"
  say "fingerprint: $(fingerprint "$DEST")"
  exit 0
fi

FORCE=0
if [ "${1:-}" = "--force" ]; then FORCE=1; shift; fi
SRC="${1:-}"
[ -n "$SRC" ] || die "usage: $0 [--force] <vendor-license.pub> | --show"
[ -f "$SRC" ] || die "no such file: $SRC"
[ "$(id -u)" = 0 ] || die "run as root"

size=$(wc -c < "$SRC")
# A raw ed25519 PUBLIC key is exactly 32 bytes. A PRIVATE key is 64. Refusing the 64-byte case is not
# pedantry: copying the signing key onto an appliance would hand every hotel the ability to mint its own
# activation packages.
if [ "$size" = "64" ]; then
  die "that is a 64-byte ed25519 PRIVATE key. The private signing key must never leave Central. Install the 32-byte PUBLIC key instead."
fi
[ "$size" = "32" ] || die "expected a 32-byte raw ed25519 public key, got $size bytes"

fpr="$(fingerprint "$SRC")" || die "could not read $SRC as an ed25519 public key"

if [ -s "$DEST" ]; then
  cur="$(fingerprint "$DEST" 2>/dev/null || echo '?')"
  if [ "$cur" = "$fpr" ]; then
    say "already installed and identical (fingerprint $fpr) — nothing to do"
    exit 0
  fi
  # REPLACING A TRUST ROOT IS NOT A REDEPLOY. Every package signed by the old key stops verifying the moment
  # this is swapped, so it happens only when somebody means it.
  [ "$FORCE" = "1" ] || die "a DIFFERENT vendor trust key is already installed (fingerprint $cur, new $fpr). Re-run with --force only if the vendor key is genuinely being rotated."
  cp -a "$DEST" "$DEST.previous.$(date -u +%Y%m%dT%H%M%SZ)"
  say "kept the previous key alongside for audit"
fi

install -o root -g root -m 0644 "$SRC" "$DEST.tmp"
mv -f "$DEST.tmp" "$DEST"
say "installed $DEST"
say "fingerprint: $fpr"
say ""
say "CONFIRM THIS FINGERPRINT out of band against the one published by the holder of the private signing"
say "key. If it does not match, this appliance will trust activation packages it should refuse."
