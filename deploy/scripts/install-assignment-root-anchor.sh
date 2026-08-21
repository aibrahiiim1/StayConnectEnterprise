#!/usr/bin/env bash
# INSTALL THE ASSIGNMENT-REGISTRY ROOT ANCHOR ON AN APPLIANCE.
#
# WHAT IT IS, AND WHY IT IS A THIRD SEPARATE KEY
#
# An appliance learns which hotel it belongs to from a SIGNED ASSIGNMENT. It will only believe one signed by
# a key in its local assignment trust registry -- and it will only believe that registry because the registry
# itself is signed by this root anchor, pinned here at provisioning time.
#
#   vendor key       licences and offline activation packages   /etc/stayconnect/vendor-license.pub
#   registry root    the list of keys allowed to sign assignments   <-- THIS FILE
#   Central CA       the TLS certificate Central presents       (system trust store)
#
# They are deliberately distinct. If one key signed all three, a licence could authorise a tenant change,
# and the whole point of the assignment channel is that it cannot.
#
# WITHOUT THIS FILE the appliance logs "assignment: no trusted registry and no root anchor — assignment
# agent disabled" and then simply never adopts an assignment. Activation in the control panel appears to
# succeed and the appliance never converges: nothing errors, it just stays awaiting-assignment forever.
#
# Get it from Central (it is NOT secret -- it is the public half):
#   scp root@<central>:/etc/stayconnect/assignment-registry-root.pub deploy/pki/
#
# Usage:
#   install-assignment-root-anchor.sh <assignment-registry-root.pub>
#   install-assignment-root-anchor.sh --show
#   install-assignment-root-anchor.sh --force <pub>     replace a DIFFERENT anchor (registry root rotation)
set -euo pipefail

DEST="${SCD_ASSIGNMENT_REGISTRY_ROOT:-/etc/stayconnect/assignment-registry-root.pub}"
say() { echo "[assign-anchor] $*"; }
die() { echo "[assign-anchor] ABORT: $*" >&2; exit 1; }

fingerprint() {
  python3 - "$1" <<'PY'
import base64, hashlib, sys
raw = open(sys.argv[1], 'rb').read()
if len(raw) != 32:
    sys.exit("not a 32-byte ed25519 public key")
d = hashlib.sha256(raw).digest()[:8]
print("%s  (hex %s)" % (base64.urlsafe_b64encode(d).decode().rstrip('='), d.hex()))
PY
}

if [ "${1:-}" = "--show" ]; then
  [ -s "$DEST" ] || die "no assignment registry root anchor at $DEST — this appliance cannot adopt an assignment"
  say "anchor: $DEST"
  say "fingerprint: $(fingerprint "$DEST")"
  exit 0
fi

FORCE=0
if [ "${1:-}" = "--force" ]; then FORCE=1; shift; fi
SRC="${1:-}"
[ -n "$SRC" ] || die "usage: $0 [--force] <assignment-registry-root.pub> | --show"
[ -f "$SRC" ] || die "no such file: $SRC"
[ "$(id -u)" = 0 ] || die "run as root"

size=$(wc -c < "$SRC")
# 64 bytes is the PRIVATE registry root key. On an appliance it would let that appliance mint its own trust
# registry, and therefore assign itself to any hotel it liked.
if [ "$size" = "64" ]; then
  die "that is the 64-byte PRIVATE registry root key. It must never leave Central. Install the 32-byte PUBLIC half."
fi
[ "$size" = "32" ] || die "expected a 32-byte raw ed25519 public key, got $size bytes"

fpr="$(fingerprint "$SRC")" || die "could not read $SRC as an ed25519 public key"

if [ -s "$DEST" ]; then
  cur="$(fingerprint "$DEST" 2>/dev/null || echo '?')"
  if [ "$cur" = "$fpr" ]; then
    say "already installed and identical (fingerprint $fpr) — nothing to do"
    exit 0
  fi
  [ "$FORCE" = "1" ] || die "a DIFFERENT anchor is already installed (has $cur, new $fpr).
    Replacing it invalidates the trust registry this appliance currently accepts. Use --force only if the
    registry root is genuinely being rotated."
  cp -a "$DEST" "$DEST.previous.$(date -u +%Y%m%dT%H%M%SZ)"
  say "kept the previous anchor alongside for audit"
fi

install -o root -g root -m 0644 "$SRC" "$DEST.tmp"
mv -f "$DEST.tmp" "$DEST"
say "installed $DEST"
say "fingerprint: $fpr"
say ""
say "Restart scd so the assignment agent starts:  systemctl restart stayconnect-scd"
