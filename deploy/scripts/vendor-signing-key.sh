#!/usr/bin/env bash
# THE VENDOR SIGNING IDENTITY, ACROSS THE LIFE OF THE PRODUCT. Runs on the CENTRAL host only.
#
# There is exactly one vendor keypair. Its private half signs licences and offline activation packages; its
# public half is pinned on every appliance before any of that is believed. The pairing is the product's root
# of commercial trust, and it has to survive things that happen to servers: reinstalls, migrations, disk
# failures, the person who set it up leaving.
#
# WHAT THIS SCRIPT EXISTS TO PREVENT
#
#   Silent replacement.   A redeployed Central that generates a "missing" key would look like it worked and
#                         would have quietly invalidated every appliance in the field: their pinned public
#                         key no longer matches anything, so every licence and every activation package is
#                         refused. `init` refuses to run when a key exists; `restore` refuses to overwrite a
#                         DIFFERENT key. Replacement is only ever `rotate --force`, typed on purpose.
#   No way back.          A private key that exists only on one server's disk is one disk failure away from
#                         a fleet that can never be licensed again. `backup` makes an encrypted escrow copy
#                         and refuses to write a plaintext one.
#   Leaking it.           `export-public` is the only thing that ever leaves this host. It emits 32 bytes and
#                         verifies they really are the public half of the installed private key.
#
# Usage (as root on Central):
#   vendor-signing-key.sh init                    create the ONE keypair, once, ever
#   vendor-signing-key.sh show                    fingerprint of the installed identity
#   vendor-signing-key.sh export-public <dir>     write vendor-license.pub for appliance provisioning
#   vendor-signing-key.sh backup <out.enc>        encrypted escrow copy (passphrase required)
#   vendor-signing-key.sh restore <in.enc>        install the EXISTING identity on a new Central host
#   vendor-signing-key.sh rotate --force          deliberately replace it (invalidates every pinned appliance)
set -euo pipefail

KEY="${CTRLAPI_VENDOR_KEY:-/etc/stayconnect/vendor-license.key}"
PUB="${CTRLAPI_VENDOR_PUB:-/etc/stayconnect/vendor-license.pub}"
CTRLAPI="${CTRLAPI_BIN:-/opt/stayconnect/bin/ctrlapi}"

say() { echo "[vendor-key] $*"; }
die() { echo "[vendor-key] ABORT: $*" >&2; exit 1; }

need_root() { [ "$(id -u)" = 0 ] || die "run as root"; }

# derive_public recomputes the public half FROM the private key rather than trusting whatever .pub happens
# to be sitting on disk. A mismatched pair is exactly the failure this tooling exists to make impossible,
# and it is invisible until an appliance in a hotel refuses a package.
derive_public() {
  python3 - "$1" <<'PY'
import sys
raw = open(sys.argv[1], 'rb').read()
if len(raw) != 64:
    sys.exit("not a 64-byte raw ed25519 private key")
# For raw ed25519 private keys the trailing 32 bytes ARE the public key (RFC 8032 seed||public layout, as
# written by Go's ed25519.GenerateKey — which is what produced this file).
pub = raw[32:]
sys.stdout.buffer.write(pub)
PY
}

# Both short ids, always. Activation packages name their signer in base64url and signed licences name theirs
# in hex — the same eight SHA-256 bytes, two encodings. Printing one of them is how an operator ends up
# unable to tell a matching key from a different one.
#
# These take a PATH rather than reading stdin: `python3 - <<'PY'` already uses stdin for the script itself,
# so a piped key would arrive at a stream the interpreter has consumed.
fingerprint_of() {
  python3 - "$1" "${2:-public}" <<'PY'
import sys, hashlib, base64
raw = open(sys.argv[1], 'rb').read()
kind = sys.argv[2]
if kind == 'private':
    if len(raw) != 64:
        sys.exit("not a 64-byte raw ed25519 private key")
    raw = raw[32:]   # seed || public, as written by Go's ed25519.GenerateKey
elif len(raw) != 32:
    sys.exit("not a 32-byte ed25519 public key")
d = hashlib.sha256(raw).digest()[:8]
print("%s  (licence key_id %s)" % (
    base64.urlsafe_b64encode(d).decode().rstrip('='), d.hex()))
PY
}

installed_fingerprint() {
  [ -s "$KEY" ] || die "no vendor signing key at $KEY"
  fingerprint_of "$KEY" private
}

require_passphrase() {
  if [ -z "${VENDOR_KEY_PASSPHRASE:-}" ]; then
    # A private key must not be written to disk unprotected, not even "just to copy it over".
    [ -t 0 ] || die "set VENDOR_KEY_PASSPHRASE (no terminal available to prompt on)"
    printf 'Passphrase for the escrow copy: ' >&2
    read -rs VENDOR_KEY_PASSPHRASE; echo >&2
    [ -n "$VENDOR_KEY_PASSPHRASE" ] || die "empty passphrase"
  fi
}

cmd="${1:-}"
case "$cmd" in

init)
  need_root
  # THE ONE-TIME EVENT. Refusing here is what makes a reinstall safe: a server rebuild cannot become an
  # accidental key rotation, because this path never overwrites.
  [ ! -s "$KEY" ] || die "a vendor signing key already EXISTS at $KEY (fingerprint $(installed_fingerprint)).
    A reinstall must reuse it: restore the escrow copy with '$0 restore <file>'.
    Generating a new one would invalidate every appliance already pinned to the old one."
  [ -x "$CTRLAPI" ] || die "ctrlapi binary not found at $CTRLAPI (set CTRLAPI_BIN)"
  mkdir -p "$(dirname "$KEY")"
  "$CTRLAPI" gen-vendor-key --out "$KEY" --pub-out "$PUB"
  chmod 600 "$KEY"; chmod 644 "$PUB"
  fpr="$(installed_fingerprint)"
  say "vendor signing identity created"
  say "  private (NEVER leaves this host): $KEY"
  say "  public  (goes to appliances):     $PUB"
  say "  fingerprint:                      $fpr"
  say ""
  say "DO THESE TWO THINGS NOW, before this host can be lost:"
  say "  1. $0 backup /secure/media/vendor-key.enc     — escrow, off this machine"
  say "  2. publish the fingerprint $fpr where installers can check it out of band"
  ;;

show)
  [ -s "$KEY" ] || die "no vendor signing key at $KEY — Central cannot sign licences or activation packages"
  say "vendor signing identity: $KEY"
  say "fingerprint: $(installed_fingerprint)"
  if [ -s "$PUB" ]; then
    ondisk="$(fingerprint_of "$PUB" 2>/dev/null || echo '?')"
    if [ "$ondisk" = "$(installed_fingerprint)" ]; then
      say "public key at $PUB matches"
    else
      say "WARNING: $PUB does NOT match the private key (it says $ondisk)."
      say "         Re-export it: $0 export-public $(dirname "$PUB")"
    fi
  fi
  ;;

export-public)
  dest="${2:-}"
  [ -n "$dest" ] || die "usage: $0 export-public <directory>"
  [ -d "$dest" ] || die "no such directory: $dest"
  [ -s "$KEY" ] || die "no vendor signing key at $KEY"
  out="$dest/vendor-license.pub"
  derive_public "$KEY" > "$out.tmp"
  chmod 644 "$out.tmp"; mv -f "$out.tmp" "$out"
  say "wrote $out"
  say "fingerprint: $(fingerprint_of "$out")"
  say ""
  say "This file is NOT secret. Install it on appliances with deploy/scripts/install-vendor-trust-key.sh,"
  say "or drop it in deploy/pki/ so provisioning pins it automatically. Confirm the fingerprint above"
  say "out of band against what the appliance reports."
  ;;

backup)
  need_root
  out="${2:-}"
  [ -n "$out" ] || die "usage: $0 backup <out-file>"
  [ -s "$KEY" ] || die "no vendor signing key at $KEY"
  command -v openssl >/dev/null || die "openssl is required to encrypt the escrow copy"
  [ ! -e "$out" ] || die "refusing to overwrite $out"
  require_passphrase
  # ENCRYPTED OR NOT AT ALL. An escrow copy travels — to a safe, to another site, onto removable media —
  # and a plaintext one is simply the signing key lying around somewhere nobody is watching.
  VENDOR_KEY_PASSPHRASE="$VENDOR_KEY_PASSPHRASE" \
    openssl enc -aes-256-cbc -pbkdf2 -iter 600000 -salt -in "$KEY" -out "$out" -pass env:VENDOR_KEY_PASSPHRASE
  chmod 600 "$out"
  say "encrypted escrow copy written: $out"
  say "fingerprint of the identity inside: $(installed_fingerprint)"
  say "Store it away from this host, and store the passphrase separately from the file."
  ;;

restore)
  need_root
  in="${2:-}"
  [ -n "$in" ] || die "usage: $0 restore <encrypted-backup>"
  [ -s "$in" ] || die "no such file: $in"
  command -v openssl >/dev/null || die "openssl is required to decrypt the escrow copy"
  require_passphrase
  tmp="$(mktemp)"; chmod 600 "$tmp"
  trap 'rm -f "$tmp"' EXIT
  VENDOR_KEY_PASSPHRASE="$VENDOR_KEY_PASSPHRASE" \
    openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -in "$in" -out "$tmp" -pass env:VENDOR_KEY_PASSPHRASE \
    || die "could not decrypt $in (wrong passphrase?)"
  [ "$(wc -c < "$tmp")" = "64" ] || die "decrypted content is not a 64-byte ed25519 private key"
  new_fpr="$(fingerprint_of "$tmp" private)"

  if [ -s "$KEY" ]; then
    cur_fpr="$(installed_fingerprint)"
    if [ "$cur_fpr" = "$new_fpr" ]; then
      say "the same vendor identity is already installed (fingerprint $new_fpr) — nothing to do"
      exit 0
    fi
    # A DIFFERENT key already being here means one of two things, and both need a human: either this is the
    # wrong backup, or someone is trying to rotate. Neither may happen as a side effect of a restore.
    die "a DIFFERENT vendor signing key is already installed (fingerprint $cur_fpr; the backup holds $new_fpr).
    Restoring over it would invalidate every appliance pinned to $cur_fpr.
    If you genuinely mean to change the fleet's trust root, use '$0 rotate --force'."
  fi

  mkdir -p "$(dirname "$KEY")"
  install -o root -g root -m 0600 "$tmp" "$KEY"
  derive_public "$KEY" > "$PUB"; chmod 644 "$PUB"
  say "vendor signing identity restored on this host"
  say "fingerprint: $new_fpr"
  say "Appliances already pinned to this fingerprint keep working with no change at all."
  ;;

rotate)
  need_root
  [ "${2:-}" = "--force" ] || die "rotation is explicit: '$0 rotate --force'.
    It replaces the fleet's root of commercial trust. Every appliance pinned to the current key will refuse
    every new licence and every new activation package until its pinned public key is replaced BY HAND."
  [ -x "$CTRLAPI" ] || die "ctrlapi binary not found at $CTRLAPI (set CTRLAPI_BIN)"
  if [ -s "$KEY" ]; then
    old="$(installed_fingerprint)"
    stamp="$(date -u +%Y%m%dT%H%M%SZ)"
    cp -a "$KEY" "$KEY.rotated-$stamp"
    chmod 600 "$KEY.rotated-$stamp"
    # The old key is KEPT. Appliances still pinned to it exist until someone visits them, and re-signing for
    # those appliances requires the key they trust.
    say "previous identity ($old) kept at $KEY.rotated-$stamp — appliances still pinned to it need it"
    rm -f "$KEY"
  fi
  "$CTRLAPI" gen-vendor-key --out "$KEY" --pub-out "$PUB"
  chmod 600 "$KEY"; chmod 644 "$PUB"
  say "NEW vendor signing identity: $(installed_fingerprint)"
  say "Every appliance must now be re-pinned with install-vendor-trust-key.sh --force before it will accept"
  say "anything signed by this key. Back the new identity up before anything else: $0 backup <file>"
  ;;

*)
  echo "usage: $0 {init|show|export-public <dir>|backup <out>|restore <in>|rotate --force}" >&2
  exit 2
  ;;
esac
