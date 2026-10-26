#!/usr/bin/env bash
# INSTALL OR UPGRADE A ONEGATE APPLIANCE FROM A RELEASE — the one supported entry point.
#
# Run as root from an extracted release built by deploy/scripts/appliance-build.sh:
#
#   FRESH INSTALL (a blank Ubuntu 22.04 VM with exactly two NICs)
#     bash onegate-appliance-<sha>/deploy/scripts/appliance-install.sh \
#          --trust-dir /root/central-trust [--admin-email admin@example.com] [--wan-iface ens160 --lan-iface ens192]
#
#   RE-RUN (same release): converges; regenerates nothing that exists (identity, keys, passwords, database).
#
#   UPGRADE (an installed appliance, newer release)
#     bash onegate-appliance-<sha>/deploy/scripts/appliance-install.sh --upgrade
#
# What a fresh install does, in order, stopping at the first failure with the reason:
#   1. verify the release (SHA256SUMS; every binary embeds the release commit, unmodified, production-tagged)
#   2. check the platform (Ubuntu 22.04 amd64, exactly two physical NICs, a default route, memory, disk, clock)
#   3. stage it under /opt/stayconnect (the previous tree kept in /opt/stayconnect/.previous/<stamp>)
#   4. install Central's PUBLIC trust files from --trust-dir, checked against FINGERPRINTS.txt when present
#   5. run deploy/scripts/provision-fresh-appliance.sh (packages, accounts, database, keys, env, units, Caddy, services)
#   6. install the Admin Console bundle (deploy-hotel-admin.sh install), unless that exact build is already live
#   7. create the first administrator (deploy/scripts/create-first-admin.sh) when --admin-email is given
#   8. record what is installed (/opt/stayconnect/DEPLOYED_BINARIES.sha256, RELEASE.json, SHA256SUMS) and verify units
#
# An upgrade: verify the release; stage the new tree beside the old; apply pending migrations through edge-migrate.sh
# (each takes a verified pre-change backup and refuses without one); reconcile Gate-P; install the binaries with
# install(1), keeping the old ones as <bin>.bak-<stamp>; install units; restart in dependency order; install the
# console; verify. Rollback of binaries: scripts/binary-rollback.sh --source-suffix .bak-<stamp> (printed at the end).
#
# Options:
#   --trust-dir DIR            vendor-license.pub, assignment-registry-root.pub, central-tls-ca.crt (or central-ca.crt),
#                              and optionally FINGERPRINTS.txt — the files Central publishes in
#                              /opt/stayconnect/central/appliance-trust/. Required on a fresh install unless already pinned.
#   --admin-email E            create the first site administrator (password asked on the terminal, or --admin-password-file)
#   --admin-password-file F    a 0600 file whose first line is the password
#   --wan-iface X --lan-iface Y   override interface detection
#   --central-base URL         override the release's Central endpoint (labs only; https + FQDN)
#   --offline-activation-only  a site activated with an offline package (no Central CA needed; vendor key required)
#   --reinitialise-unused-db   rebuild a half-built site database that provably holds no data (interrupted old install)
#   --allow-unsupported-platform   proceed on another OS/version/NIC count (NOT a supported installation)
#   --upgrade                  upgrade an installed appliance to this release
#   --yes                      do not ask for confirmation
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REL="$(cd "$HERE/../.." && pwd)"
SC=/opt/stayconnect
ETC=/etc/stayconnect
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
TRUST=""; ADMIN_EMAIL=""; ADMIN_PWFILE=""; WAN=""; LAN=""; CBASE=""; OFFLINE=0; REINIT=0; UNSUPPORTED=0; UPGRADE=0; YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --trust-dir) TRUST="${2:?}"; shift 2;;
    --admin-email) ADMIN_EMAIL="${2:?}"; shift 2;;
    --admin-password-file) ADMIN_PWFILE="${2:?}"; shift 2;;
    --wan-iface) WAN="${2:?}"; shift 2;;
    --lan-iface) LAN="${2:?}"; shift 2;;
    --central-base) CBASE="${2:?}"; shift 2;;
    --offline-activation-only) OFFLINE=1; shift;;
    --reinitialise-unused-db) REINIT=1; shift;;
    --allow-unsupported-platform) UNSUPPORTED=1; shift;;
    --upgrade) UPGRADE=1; shift;;
    --yes) YES=1; shift;;
    -h|--help) sed -n '2,45p' "$0"; exit 0;;
    *) echo "appliance-install: unknown argument $1 (see --help)" >&2; exit 1;;
  esac
done
say()  { echo "[appliance-install] $*"; }
step() { echo; echo "[appliance-install] ==== $*"; }
die()  { echo "[appliance-install] ABORT: $*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || die "run as root"
case "$(pwd -P)" in /root|/root/*) cd /;; esac   # deploy-hotel-admin.sh refuses to run from /root

# ================================================================= 1. the release
step "1. release"
[ -f "$REL/RELEASE.json" ] && [ -f "$REL/SHA256SUMS" ] || die "$REL is not a release (no RELEASE.json/SHA256SUMS). Build one with deploy/scripts/appliance-build.sh."
( cd "$REL" && sha256sum -c --quiet SHA256SUMS ) || die "release files do not match SHA256SUMS — a damaged or edited release"
jget() { python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$REL/RELEASE.json" "$1"; }
COMMIT="$(jget 'd["source_commit"]')"
[ "$(jget 'd["source_state"]')" = clean ] || die "the release was not built from a clean tree"
for b in scd edged netd portald acctd pmsd keybootstrap svc-run; do
  f="$REL/bin/$b"; [ -f "$f" ] || die "release has no bin/$b"
  grep -aqo "vcs.revision=$COMMIT" "$f" || die "bin/$b does not embed the release commit $COMMIT"
  grep -aqo 'vcs.modified=false' "$f" || die "bin/$b was built from a modified tree"
  grep -aqo -- '-tags=stayconnect_production' "$f" || die "bin/$b is not a production build"
done
say "release $COMMIT verified ($(wc -l < "$REL/SHA256SUMS") files, 8 binaries, console $(jget 'd["hotel_admin"]["build_id"]'))"

# ================================================================= 2. the platform
step "2. platform"
. /etc/os-release
ARCH="$(uname -m)"
mapfile -t PHYS < <(for n in /sys/class/net/*; do [ -e "$n/device" ] && basename "$n"; done)
VIRT="$(systemd-detect-virt 2>/dev/null || echo unknown)"
MEM_MB="$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)"
FREE_GB="$(df -BG --output=avail / | tail -1 | tr -dc '0-9')"
say "os: $PRETTY_NAME ($ARCH), hypervisor: $VIRT, memory: ${MEM_MB} MiB, free on /: ${FREE_GB} GiB, physical NICs: ${PHYS[*]:-none}"
problems=()
[ "${ID:-}" = ubuntu ] && [ "${VERSION_ID:-}" = 22.04 ] || problems+=("the supported OS is Ubuntu 22.04 LTS (found ${PRETTY_NAME:-unknown})")
[ "$ARCH" = x86_64 ] || problems+=("the supported architecture is amd64 (found $ARCH)")
[ "${#PHYS[@]}" = 2 ] || problems+=("an appliance has exactly two NICs, WAN and LAN (found ${#PHYS[@]}: ${PHYS[*]:-none})")
if [ "${#problems[@]}" -gt 0 ]; then
  for p in "${problems[@]}"; do echo "  - $p" >&2; done
  [ "$UNSUPPORTED" = 1 ] || die "unsupported platform (above). --allow-unsupported-platform proceeds, but the result is not a supported installation."
  say "--allow-unsupported-platform: continuing on an UNSUPPORTED platform"
fi
[ "$MEM_MB" -ge 3500 ] || die "at least 4 GiB of memory is required (found ${MEM_MB} MiB)"
[ "${FREE_GB:-0}" -ge 15 ] || die "at least 15 GiB free on / is required (found ${FREE_GB} GiB)"
ip -o -4 route show default | grep -q . || die "no IPv4 default route: configure the WAN interface (address, gateway, DNS) first"
timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -qx yes \
  || say "WARNING: the clock is not NTP-synchronised. Licences and signed tokens depend on a correct clock."

INSTALLED=0; [ -s "$ETC/.dsn" ] && INSTALLED=1
if [ "$UPGRADE" = 1 ]; then
  [ "$INSTALLED" = 1 ] || die "--upgrade needs an installed appliance (no $ETC/.dsn); run a fresh install instead"
  [ -f "$SC/RELEASE.json" ] && say "upgrading from $(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["source_commit"])' "$SC/RELEASE.json") to $COMMIT" \
    || say "upgrading an appliance with no recorded release to $COMMIT"
else
  if [ "$INSTALLED" = 1 ] && [ -f "$SC/RELEASE.json" ]; then
    cur="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["source_commit"])' "$SC/RELEASE.json")"
    [ "$cur" = "$COMMIT" ] || die "this appliance runs release $cur; use --upgrade to move it to $COMMIT"
    say "re-running release $COMMIT (converges; regenerates nothing that exists)"
  elif [ "$INSTALLED" = 1 ]; then
    die "this appliance is already provisioned but has no recorded release; use --upgrade"
  fi
fi
if [ "$YES" != 1 ] && [ -t 0 ]; then
  read -r -p "Proceed with $([ "$UPGRADE" = 1 ] && echo upgrade || echo installation) of $COMMIT on $(hostname)? [y/N] " a
  [ "$a" = y ] || [ "$a" = Y ] || die "not confirmed"
fi

# ================================================================= 3. stage the tree
stage_tree() {
  step "3. stage /opt/stayconnect"
  install -d -m 0755 "$SC" "$SC/bin" "$SC/bundles"
  local prev="$SC/.previous/$STAMP"
  for d in deploy data-plane scripts; do
    if [ -e "$SC/$d" ]; then mkdir -p "$prev"; mv "$SC/$d" "$prev/$d"; fi
  done
  ( cd "$REL" && tar -cf - deploy data-plane scripts ) | ( cd "$SC" && tar -xf - --no-same-owner )
  # Trust files installed before are kept: they are this appliance's, not the release's.
  if [ -d "$prev/deploy/pki" ]; then
    find "$prev/deploy/pki" -maxdepth 1 -type f \( -name '*.pub' -o -name '*.crt' \) -exec cp -n {} "$SC/deploy/pki/" \;
  fi
  cp -f "$REL/bundles/"hotel-admin-deploy.tgz* "$SC/bundles/"
  # Keep the three newest previous trees; they are small, and only the last one is ever needed to compare.
  ls -1d "$SC/.previous"/*/ 2>/dev/null | sort | head -n -3 | xargs -r rm -rf
  say "tree staged (previous tree: ${prev})"
}
install_binaries() { # copy binaries, keeping a differing installed one as <bin>.bak-$STAMP
  local changed=0
  for b in scd edged netd portald acctd pmsd keybootstrap svc-run; do
    if [ -f "$SC/bin/$b" ] && cmp -s "$REL/bin/$b" "$SC/bin/$b"; then continue; fi
    [ -f "$SC/bin/$b" ] && cp -p "$SC/bin/$b" "$SC/bin/$b.bak-$STAMP"
    install -m 0755 "$REL/bin/$b" "$SC/bin/$b"   # install(1), never cp: a running executable is replaced, not refused
    changed=1
  done
  echo "$changed"
}
record_release() {
  cp -f "$REL/RELEASE.json" "$REL/SHA256SUMS" "$SC/"
  {
    echo "# service  sha256  vcs.revision  tags  modified  written $STAMP"
    for b in scd edged pmsd portald netd acctd keybootstrap svc-run; do
      f="$SC/bin/$b"
      printf '%-12s %s %s %s %s\n' "$b" "$(sha256sum "$f" | cut -d' ' -f1)" \
        "$(grep -aom1 'vcs.revision=[0-9a-f]\{40\}' "$f" | cut -d= -f2)" \
        "$(grep -aom1 -- '-tags=[A-Za-z0-9_,]*' "$f" | cut -d= -f2)" \
        "$(grep -aom1 'vcs.modified=[a-z]*' "$f" | cut -d= -f2)"
    done
  } > "$SC/DEPLOYED_BINARIES.sha256.new"
  [ -f "$SC/DEPLOYED_BINARIES.sha256" ] && cp -p "$SC/DEPLOYED_BINARIES.sha256" "$SC/DEPLOYED_BINARIES.sha256.bak-$STAMP"
  mv "$SC/DEPLOYED_BINARIES.sha256.new" "$SC/DEPLOYED_BINARIES.sha256"
  say "recorded the installed release and DEPLOYED_BINARIES.sha256 (read back from the installed files)"
}
install_console() {
  step "console (Admin Console)"
  local want cur=""
  want="$(jget 'd["hotel_admin"]["build_id"]')"
  [ -f "$SC/hotel-admin/hotel-admin-release.json" ] && \
    cur="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("build_id",""))' "$SC/hotel-admin/hotel-admin-release.json" 2>/dev/null || true)"
  if [ -n "$cur" ] && [ "$cur" = "$want" ] && systemctl is-active --quiet stayconnect-hotel-admin; then
    say "console build $want is already live — not reinstalled"
    return 0
  fi
  bash "$SC/deploy/scripts/deploy-hotel-admin.sh" install "$SC/bundles/hotel-admin-deploy.tgz"
}

if [ "$UPGRADE" != 1 ]; then
  stage_tree
  install_binaries >/dev/null

  # ================================================================= 4. Central's public trust
  step "4. Central trust"
  mkdir -p "$SC/deploy/pki"
  if [ -n "$TRUST" ]; then
    [ -d "$TRUST" ] || die "--trust-dir $TRUST is not a directory"
    for f in vendor-license.pub assignment-registry-root.pub; do
      [ -s "$TRUST/$f" ] && install -m 0644 "$TRUST/$f" "$SC/deploy/pki/$f"
    done
    if [ -s "$TRUST/central-tls-ca.crt" ]; then install -m 0644 "$TRUST/central-tls-ca.crt" "$SC/deploy/pki/central-ca.crt"
    elif [ -s "$TRUST/central-ca.crt" ]; then install -m 0644 "$TRUST/central-ca.crt" "$SC/deploy/pki/central-ca.crt"; fi
    if [ -s "$TRUST/FINGERPRINTS.txt" ]; then
      python3 - "$TRUST/FINGERPRINTS.txt" "$SC/deploy/pki" <<'PY' || die "the trust files do not match FINGERPRINTS.txt (above) — do not install them"
import hashlib, re, subprocess, sys, os
fp, d = sys.argv[1], sys.argv[2]
text = open(fp, encoding="utf-8").read()
def want(label):
    m = re.search(r"^" + re.escape(label) + r":\s*(\S+)", text, re.M)
    return m.group(1).strip() if m else None
bad = 0
for label, name in (("vendor licence key_id", "vendor-license.pub"), ("registry root key_id", "assignment-registry-root.pub")):
    p = os.path.join(d, name)
    w = want(label)
    if not os.path.exists(p) or w is None: continue
    got = hashlib.sha256(open(p, "rb").read()).digest()[:8].hex()
    print(f"  {name}: {got} {'OK' if got == w else 'MISMATCH (Central says ' + w + ')'}"); bad |= got != w
p = os.path.join(d, "central-ca.crt"); w = want("Central TLS CA sha256")
if os.path.exists(p) and w:
    out = subprocess.run(["openssl", "x509", "-in", p, "-noout", "-fingerprint", "-sha256"], capture_output=True, text=True).stdout
    got = out.strip().split("=", 1)[-1]
    print(f"  central-ca.crt: {got} {'OK' if got == w else 'MISMATCH (Central says ' + w + ')'}"); bad |= got != w
sys.exit(1 if bad else 0)
PY
      say "trust files match FINGERPRINTS.txt"
    else
      say "WARNING: no FINGERPRINTS.txt in $TRUST — compare the fingerprints below with the ones Central published, out of band:"
    fi
  fi
  for f in "$SC"/deploy/pki/*.pub; do [ -f "$f" ] && say "  $(basename "$f"): key_id $(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).digest()[:8].hex())' "$f")"; done
  [ -f "$SC/deploy/pki/central-ca.crt" ] && say "  central-ca.crt: $(openssl x509 -in "$SC/deploy/pki/central-ca.crt" -noout -fingerprint -sha256 2>/dev/null | sed 's/^.*=//')"

  # ================================================================= 5. provision
  step "5. provision"
  PENV=()
  [ -n "$WAN" ] && PENV+=("WAN_IFACE=$WAN")
  [ -n "$LAN" ] && PENV+=("LAN_IFACE=$LAN")
  [ -n "$CBASE" ] && PENV+=("CENTRAL_BASE=$CBASE")
  [ "$OFFLINE" = 1 ] && PENV+=("OFFLINE_ACTIVATION_ONLY=1")
  [ "$REINIT" = 1 ] && PENV+=("REINITIALISE_UNUSED_DB=1")
  env "${PENV[@]}" bash "$SC/deploy/scripts/provision-fresh-appliance.sh"

  # ================================================================= 6. console
  install_console

  # ================================================================= 7. first administrator
  step "7. first administrator"
  if [ -n "$ADMIN_EMAIL" ]; then
    args=(--email "$ADMIN_EMAIL"); [ -n "$ADMIN_PWFILE" ] && args+=(--password-file "$ADMIN_PWFILE")
    if ! bash "$SC/deploy/scripts/create-first-admin.sh" "${args[@]}"; then
      say "NOTE: no administrator was created (above). Create one later with:"
      say "      bash $SC/deploy/scripts/create-first-admin.sh --email <email>"
    fi
  else
    say "no --admin-email: create the first administrator with  bash $SC/deploy/scripts/create-first-admin.sh --email <email>"
  fi
else
  # ================================================================= UPGRADE
  step "3. stage the new tree"
  stage_tree
  step "4. database migrations (each behind a verified pre-change backup) + Gate-P"
  bash "$SC/deploy/scripts/apply-pending-migrations.sh" --root "$SC" \
    || die "migrations did not complete; the binaries were NOT replaced. Fix the cause and re-run --upgrade."
  step "5. binaries"
  changed="$(install_binaries)"
  step "6. units"
  bash "$SC/deploy/scripts/install-service-units.sh" "$SC/deploy"
  step "7. restart in dependency order"
  for u in stayconnect-netd stayconnect-scd stayconnect-acctd stayconnect-portald stayconnect-edged; do
    systemctl stop "$u" >/dev/null 2>&1 || true; systemctl reset-failed "$u" >/dev/null 2>&1 || true
    systemctl start --no-block "$u"
    for _ in $(seq 1 60); do systemctl is-active --quiet "$u" && break; [ "$u" = stayconnect-acctd ] && break; sleep 1; done
    say "  $u: $(systemctl is-active "$u" || true)"
  done
  if systemctl is-enabled --quiet stayconnect-pmsd 2>/dev/null; then
    systemctl stop stayconnect-pmsd; systemctl reset-failed stayconnect-pmsd 2>/dev/null || true; systemctl start --no-block stayconnect-pmsd
    say "  stayconnect-pmsd restarted (enabled on this appliance)"
  fi
  install_console
  [ "$changed" = 1 ] && say "binary rollback, if needed: bash $SC/scripts/binary-rollback.sh --bin-dir $SC/bin --source-suffix .bak-$STAMP \\
      --unit scd=stayconnect-scd --unit edged=stayconnect-edged --unit netd=stayconnect-netd --unit portald=stayconnect-portald --unit acctd=stayconnect-acctd"
fi

# ================================================================= 8. record + verify
step "8. record and verify"
record_release
bash "$SC/deploy/scripts/check-appliance-units.sh" || die "a unit is failed or names a missing program (above)"
for u in stayconnect-scd stayconnect-edged stayconnect-netd stayconnect-portald stayconnect-caddy stayconnect-hotel-admin; do
  printf '  %-30s %s\n' "$u" "$(systemctl is-active "$u" || true)"
done
say "installed release $COMMIT"
say "next: Central -> Appliances -> Activate this appliance; then Admin Console -> Network -> Client networks."
