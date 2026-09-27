#!/usr/bin/env bash
# REMOVE THE OBSOLETE MESSAGE-BUS / TELEMETRY / REMOTE-CONTROL STATE FROM AN EXISTING CENTRAL HOST.
#
# Central serves appliances for LICENSING ONLY (CLAUDE.md §0E). The first Central host still carries the
# machinery of what it used to be: two NATS containers, the NATS auth-callout service and its account, NATS
# credentials and certificates, and the signing keys for the remote command channel and the software-update
# agent. None of it is used by the current ctrlapi. Left in place it is attack surface, a firewall hole, and a
# standing invitation to "reconnect" something that was switched off on purpose.
#
# DEFAULT IS A DRY RUN: it prints what it would remove and changes nothing. --apply removes it.
# Idempotent: a second --apply finds nothing to do.
# Before deleting any FILE it archives it into /root/central-obsolete-<stamp>.tar.gz (0600), so nothing is lost
# to a mistaken classification — delete that archive once you are satisfied.
#
# It NEVER touches live trust material: the vendor key, the assignment signing key, the registry root key, the
# appliance Root/Intermediate CA, the mTLS server certificate, the Central TLS CA/certificates, or the database.
# The CA bundle is handled carefully: nats-ca-bundle.crt is the file offline activation packages are built from,
# under an old name. It is COPIED to the neutral ca-bundle.crt (if absent) and itself removed only with
# --remove-legacy-bundle, and only when ctrlapi.env no longer names it and the copy is byte-identical.
#
# Usage (as root on Central):
#   central-cleanup-obsolete.sh                       dry run
#   central-cleanup-obsolete.sh --apply
#   central-cleanup-obsolete.sh --apply --remove-legacy-bundle
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROG=central-cleanup
# shellcheck source=central-lib.sh
. "$HERE/central-lib.sh"

APPLY=0 RM_LEGACY_BUNDLE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=1; shift ;;
    --dry-run) APPLY=0; shift ;;
    --remove-legacy-bundle) RM_LEGACY_BUNDLE=1; shift ;;
    -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[ "$(id -u)" = 0 ] || die "run as root"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
ARCHIVE="/root/central-obsolete-$STAMP.tar.gz"
[ "$APPLY" = 1 ] && say "APPLY — removing obsolete state" || say "DRY RUN — nothing will change (use --apply)"

# Anything below that matches a live trust path is refused, whatever the lists say.
PROTECTED_RE='^/etc/stayconnect/(vendor-license\.(key|pub)|assignment-signing\.(key|pub)|assignment-registry-root\.(key|pub)|pki/(root-ca|intermediate-ca|server-mtls|ca-bundle)\.|pki-offline/)|^/opt/stayconnect/central/(tls|secrets/(db|redis)_password)'

found=0
files=()
note() { found=$((found+1)); printf '  -  %s\n' "$*"; }

# ---------------------------------------------------------------- containers
step "containers"
for c in sc-central-nats sc-central-nats-mtls; do
  if docker inspect "$c" >/dev/null 2>&1; then
    note "container $c ($(docker inspect -f '{{.Config.Image}} {{.State.Status}}' "$c"))"
    if [ "$APPLY" = 1 ]; then docker rm -f "$c" >/dev/null && say "     removed"; fi
  fi
done
for f in "$CENTRAL_ROOT/compose/infra.yml" "$CENTRAL_COMPOSE_FILE"; do
  if [ -f "$f" ] && grep -qE '^[[:space:]]+nats[a-z-]*:[[:space:]]*$|image:[[:space:]]*nats' "$f"; then
    warn "$f still declares a NATS service: 'docker compose up' with it would re-create the container.
    Move this host to the versioned deploy/compose/central-infra.yml (docs/DEPLOYMENT_CLOUD.md §8)."
  fi
done

# ---------------------------------------------------------------- the auth-callout service and its account
step "services and accounts"
accounts=""
for u in stayconnect-nats-authz.service stayconnect-nats.service; do
  unit="/etc/systemd/system/$u"
  if [ -f "$unit" ] || systemctl list-unit-files "$u" 2>/dev/null | grep -q "^$u"; then
    user="$(sed -n 's/^User=//p' "$unit" 2>/dev/null | head -1)"
    bin="$(sed -n 's/^ExecStart=\([^ ]*\).*/\1/p' "$unit" 2>/dev/null | head -1)"
    note "unit $u (User=${user:-?}, ExecStart=${bin:-?})"
    if [ "$APPLY" = 1 ]; then
      systemctl disable --now "$u" >/dev/null 2>&1 || true
      [ -f "$unit" ] && { files+=("$unit"); }
    fi
    case "$bin" in /opt/stayconnect/bin/*) [ -f "$bin" ] && { note "binary $bin"; files+=("$bin"); } ;; esac
    case "$user" in ""|root|stayconnect|caddy) ;; *) accounts="$accounts $user" ;; esac
  fi
done
# The service's own account (from its unit, if the unit was still there) and the names it has been given.
for u in $(printf '%s\n' $accounts nats-authz stayconnect-nats-authz | awk '!seen[$0]++'); do
  if getent passwd "$u" >/dev/null; then
    note "account $u"
    [ "$APPLY" = 1 ] && { userdel "$u" 2>/dev/null && say "     removed account $u" || warn "could not remove account $u"; }
  fi
done

# ---------------------------------------------------------------- files and directories
step "files"
for p in "$CENTRAL_ROOT/nats" "$CENTRAL_ROOT/nats-mtls" "$SC_ETC/nats-authz" \
         "$SC_ETC"/pki/ctrlapi-nats.* "$SC_ETC"/pki/nats-*.crt "$SC_ETC"/pki/nats-*.key \
         "$SC_ETC/command-signing.key" "$SC_ETC/command-signing.pub" \
         "$SC_ETC/update-signing.key" "$SC_ETC/update-signing.pub" \
         "$CENTRAL_SECRETS_DIR/nats_password" "$SC_ETC/assignment-trust.json"; do
  [ -e "$p" ] || continue
  case "$p" in "$SC_ETC/pki/nats-ca-bundle.crt") continue ;; esac   # handled below
  if printf '%s' "$p" | grep -qE "$PROTECTED_RE"; then warn "refusing protected path $p"; continue; fi
  if [ "$p" = "$SC_ETC/assignment-trust.json" ] && [ -f "$CTRLAPI_ENV_PATH" ] && grep -qE '^[^#]*assignment-trust.json' "$CTRLAPI_ENV_PATH"; then
    warn "$p is still named in $CTRLAPI_ENV_PATH — kept"; continue
  fi
  note "$p"
  files+=("$p")
done

# ---------------------------------------------------------------- firewall
step "firewall"
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
  for port in 4222 4223 8222; do
    if ufw status 2>/dev/null | grep -qE "^$port(/tcp)?[[:space:]]"; then
      note "ufw rule(s) for $port"
      if [ "$APPLY" = 1 ]; then
        while ufw status numbered 2>/dev/null | grep -qE "\] $port(/tcp)?[[:space:]]"; do
          n="$(ufw status numbered | grep -E "\] $port(/tcp)?[[:space:]]" | head -1 | sed 's/^\[ *\([0-9]*\)\].*/\1/')"
          ufw --force delete "$n" >/dev/null
        done
        say "     removed"
      fi
    fi
  done
fi

# ---------------------------------------------------------------- the CA bundle, carefully
step "CA bundle"
LEGACY="$SC_ETC/pki/nats-ca-bundle.crt"
if [ -f "$LEGACY" ]; then
  if [ ! -f "$CENTRAL_CA_BUNDLE" ]; then
    note "copy $LEGACY -> $CENTRAL_CA_BUNDLE (neutral name; content unchanged)"
    [ "$APPLY" = 1 ] && install -m 0644 "$LEGACY" "$CENTRAL_CA_BUNDLE"
  fi
  if [ -f "$CTRLAPI_ENV_PATH" ] && grep -qE '^[^#]*nats-ca-bundle' "$CTRLAPI_ENV_PATH"; then
    say "  $CTRLAPI_ENV_PATH still names nats-ca-bundle.crt. Point CTRLAPI_CA_BUNDLE at $CENTRAL_CA_BUNDLE and"
    say "  restart ctrlapi, then re-run with --remove-legacy-bundle."
  elif [ "$RM_LEGACY_BUNDLE" = 1 ]; then
    if [ "$APPLY" = 0 ] || cmp -s "$LEGACY" "$CENTRAL_CA_BUNDLE"; then
      note "$LEGACY (identical copy at $CENTRAL_CA_BUNDLE)"; files+=("$LEGACY")
    else
      warn "$CENTRAL_CA_BUNDLE differs from $LEGACY — legacy file kept"
    fi
  else
    say "  $LEGACY kept (pass --remove-legacy-bundle to remove it once nothing names it)"
  fi
fi

# ---------------------------------------------------------------- archive, then delete
if [ "${#files[@]}" -gt 0 ] && [ "$APPLY" = 1 ]; then
  step "archive + remove"
  tar -czf "$ARCHIVE" --absolute-names "${files[@]}" 2>/dev/null || tar -czPf "$ARCHIVE" "${files[@]}"
  chmod 0600 "$ARCHIVE"
  say "archived ${#files[@]} path(s) to $ARCHIVE (0600) — it holds old private keys; delete it once satisfied"
  for p in "${files[@]}"; do rm -rf -- "$p" && say "  removed $p"; done
  systemctl daemon-reload
fi

echo
if [ "$found" = 0 ]; then say "nothing obsolete found — this host is clean"
elif [ "$APPLY" = 1 ]; then say "done. Verify Central still activates: deploy/scripts/central-preflight.sh"
else say "$found item(s) would be removed. Re-run with --apply."; fi
