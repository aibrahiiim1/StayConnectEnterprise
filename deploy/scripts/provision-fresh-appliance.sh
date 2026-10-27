#!/usr/bin/env bash
# PROVISION A FRESH PRODUCTION APPLIANCE, END TO END.
#
# Everything this script does was learned by doing it by hand on a real Production bring-up. Each step that
# looks obvious is here because its absence produced a specific, confusing failure in the Hotel Admin UI:
#
#   "Could not load guest accounts / the appliance did not answer"  -> tenant-scoped screens before assignment
#   "branding load failed"                                          -> same cause
#   "kea dial: ... no such file or directory"                       -> Kea not installed, LAN not configured
#   silent audit_log write failures                                 -> hypertables not registered by the baseline
#   Caddy crash-loop on start                                       -> /var/log/caddy ownership
#   scd crash-loop on first boot                                    -> voucher DEK never bootstrapped
#
# It is idempotent: safe to re-run on a partially provisioned box. Every step either converges or refuses with
# the reason; none of them regenerates an identity, a key, a password or a database that already exists. The
# database step is deploy/scripts/site-db-init.sh, which applies the baseline as ONE transaction and never
# re-applies it (proven by scripts/ci/site-db-init-selftest.sh).
#
# It does NOT invent identity or topology. It never sets a tenant or site, never writes a netplan file, and
# never starts Kea, because guest topology comes from the operator through the Admin Console. It stops at the
# furthest state a factory-clean appliance can legitimately reach on its own.
#
# THE SUPPORTED WAY TO RUN IT is deploy/scripts/appliance-install.sh from a release built by
# deploy/scripts/appliance-build.sh: that verifies the release, stages it under /opt/stayconnect, supplies the
# Central trust files, runs this script, installs the Admin Console and creates the first administrator.
#
# Usage (as root, on the appliance, with a release staged under /opt/stayconnect):
#   provision-fresh-appliance.sh
# Environment (all optional): CENTRAL_BASE CENTRAL_MTLS_BASE WAN_IFACE LAN_IFACE VENDOR_PUB ASSIGN_ROOT_PUB
#   CENTRAL_CA ALLOW_SPLIT_DNS=1 OFFLINE_ACTIVATION_ONLY=1 REINITIALISE_UNUSED_DB=1
set -euo pipefail

SC=/opt/stayconnect
DEPLOY="${SC}/deploy"
ETC=/etc/stayconnect
say() { echo "[provision] $*"; }
die() { echo "[provision] ABORT: $*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root"
[ -d "$DEPLOY" ] || die "missing $DEPLOY"

# ---------------------------------------------------------------- 0. preconditions, before anything changes
# A BASELINE THAT DOES NOT EMBODY THE NEWEST MIGRATION IS A RELEASE DEFECT (K1). Refuse before installing a
# single package, so a bad release never leaves a half-provisioned box behind.
# shellcheck source=lib-baseline.sh
. "$DEPLOY/scripts/lib-baseline.sh"
why="$(baseline_is_current "$SC/data-plane/migrations/baseline/0000_production_baseline.sql" "$SC/data-plane/migrations")" \
  || die "$why"
for b in scd edged acctd netd portald pmsd keybootstrap svc-run; do
  [ -x "$SC/bin/$b" ] || die "missing $SC/bin/$b — stage a complete release first (deploy/scripts/appliance-install.sh)"
done
# The Central trust files are checked here too, so a missing one stops the install before it changes anything
# (steps 5 and 6 install them and repeat the reason).
have_trust() { [ -n "${2:-}" ] || [ -s "$DEPLOY/pki/$1" ] || [ -s "$3" ]; }
have_trust assignment-registry-root.pub "${ASSIGN_ROOT_PUB:-}" "$ETC/assignment-registry-root.pub" \
  || die "no assignment registry root anchor ($DEPLOY/pki/assignment-registry-root.pub or ASSIGN_ROOT_PUB=); see step 5 below for why it is required"
if [ "${OFFLINE_ACTIVATION_ONLY:-0}" != 1 ]; then
  have_trust central-ca.crt "${CENTRAL_CA:-}" /usr/local/share/ca-certificates/stayconnect-central-ca.crt \
    || die "no Central CA ($DEPLOY/pki/central-ca.crt or CENTRAL_CA=); set OFFLINE_ACTIVATION_ONLY=1 for an offline-activated site"
else
  have_trust vendor-license.pub "${VENDOR_PUB:-}" "$ETC/vendor-license.pub" \
    || die "OFFLINE_ACTIVATION_ONLY=1 needs the vendor's public key ($DEPLOY/pki/vendor-license.pub or VENDOR_PUB=)"
fi
# AN APPLIANCE CENTRAL REMOVED AFTER IT HELD A CUSTOMER still carries that customer's data (scd's
# removed_from_central.go). Re-running this script over it would not make it factory-clean -- the database and
# /etc/stayconnect would survive -- so it refuses. A factory-clean install starts from a blank disk:
# docs/DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md.
[ -e "$ETC/removed-from-central.json" ] && die "this appliance was removed from OneGate Central after holding a customer; reinstall it from a blank disk (docs/DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md) instead of re-provisioning over its data"

# ---------------------------------------------------------------- 1. packages
say "packages"
export DEBIAN_FRONTEND=noninteractive
# A FRESHLY BOOTED Ubuntu runs unattended-upgrades within minutes, and it holds the dpkg/apt locks; apt-get then
# fails at once ("Could not get lock /var/lib/dpkg/lock-frontend ... unattended-upgr" — measured on a fresh VM).
# Wait for the locks (up to 20 min) instead, and let every apt-get below wait for the dpkg lock as well.
for _ in $(seq 1 240); do
  if command -v fuser >/dev/null 2>&1; then
    fuser /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock /var/lib/apt/lists/lock >/dev/null 2>&1 || break
  else
    pgrep -x 'unattended-upgr|apt-get|apt|dpkg' >/dev/null 2>&1 || break
  fi
  [ "${_waited:-0}" = 1 ] || { say "waiting for another package manager (unattended-upgrades?) to release the apt/dpkg locks"; _waited=1; }
  sleep 5
done
apt-get() { command apt-get -o DPkg::Lock::Timeout=1200 "$@"; }
apt-get update -qq
# conntrack: the fallback netd uses to end a revoked address's open flows when ctnetlink is unavailable (audit B-1).
# dnsutils: dig, which the Central-endpoint split-DNS check needs (without it the check silently degraded).
# age: decrypting a backup or configuration archive by hand during a restore. openssl: the certificate scripts.
apt-get install -y -qq ca-certificates curl gnupg lsb-release nftables conntrack unbound jq python3 dnsutils age openssl >/dev/null
if ! command -v docker >/dev/null; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin >/dev/null
  systemctl enable --now docker
fi
if ! command -v caddy >/dev/null; then
  curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq && apt-get install -y -qq caddy >/dev/null
  systemctl disable --now caddy >/dev/null 2>&1 || true   # the stock unit; we ship stayconnect-caddy
fi
# KEA IS INSTALLED BUT NOT STARTED. It binds the LAN bridge, which does not exist until guest networking is
# configured, so starting it here would only produce a crash-loop. Installing it means the DHCP screen can
# work the moment the operator configures the LAN, instead of failing with a missing-socket error.
# ONLY WHEN THIS RUN INSTALLED IT. On a re-run, netd may already have enabled and started Kea for a confirmed
# client network; disabling it then would cut DHCP for every Client on the site.
if ! command -v kea-dhcp4 >/dev/null; then
  apt-get install -y -qq kea-dhcp4-server >/dev/null
  systemctl disable --now kea-dhcp4-server >/dev/null 2>&1 || true
fi
# scd Requires=nftables.service; enabled so the boot order is the unit graph's, not whichever unit asks first.
systemctl enable nftables >/dev/null 2>&1 || true
command -v node >/dev/null || { curl -fsSL https://deb.nodesource.com/setup_20.x | bash - >/dev/null; apt-get install -y -qq nodejs >/dev/null; }
say "packages ok"

# ---------------------------------------------------------------- 2. layout
say "layout"
mkdir -p "$SC"/{bin,releases} "$ETC"/{identity,license,assignment,certs,secrets} /var/log/stayconnect /run/kea /var/lib/kea
chmod 700 "$ETC/secrets"
# THE SERVICE ACCOUNTS, ALL OF THEM.
#
# This script created exactly one -- stayconnect -- and two units in deploy/systemd name accounts it never
# made: stayconnect-pmsd (since Phase 3) and now stayconnect-portald. On this appliance both exist because
# somebody ran useradd by hand, so the omission was invisible; a factory-clean provision would install the
# units and then fail to start those services with "Failed to determine user credentials". That is the same
# class of defect as a grant only a migration performs: a live-only artefact that no install path recreates.
#
# WHY EACH ONE IS SEPARATE, rather than one account for the whole product:
#
#	stayconnect           edged, and the OWNER of /run/stayconnect/scd.sock's group. Reaching that socket
#	                      is what the group means, and scd's administrative routes now additionally
#	                      require the peer to be THIS uid (cmd/scd/admin_surface.go).
#	stayconnect-portald   the captive portal. It listens on *:8380 -- on every guest VLAN -- so it is the
#	                      process most likely to be attacked and the one that must own the least. It keeps
#	                      the stayconnect SUPPLEMENTARY group because guest authentication runs through
#	                      scd's socket. That group also opens netd's socket; what keeps portald out there
#	                      is netd's per-route caller check (SO_PEERCRED against NETD_ADMIN_CALLERS /
#	                      NETD_FLOW_CALLERS in stayconnect-netd.service), which does not name it. Group
#	                      membership alone is not the boundary on either socket.
#	stayconnect-pmsd      the PMS interface, which holds the credentials for the hotel's Protel connection.
#
# Sharing a uid makes the separation cosmetic: a process can ptrace another running under the same uid, so
# a compromised portal reaches edged's memory whatever any in-process check does. That is why this is a
# provisioning change and not only a code change.
id stayconnect >/dev/null 2>&1 || useradd --system --home "$SC" --shell /usr/sbin/nologin stayconnect
for svcuser in stayconnect-portald stayconnect-pmsd; do
  id "$svcuser" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$svcuser"
done
# scd's socket group (audit N-26): opens /run/stayconnect/scd.sock and nothing else. edged and portald receive it
# through SupplementaryGroups= in their units; nobody is a persistent member.
getent group stayconnect-scd >/dev/null 2>&1 || groupadd --system stayconnect-scd
# NO usermod HERE, DELIBERATELY. portald needs the stayconnect-scd group to open scd's socket, and its unit
# says so with SupplementaryGroups=stayconnect-scd -- which systemd applies to the process at start time. It
# does not need, and does not get, a persistent membership in /etc/group: a permanent grant would also
# apply to anything else ever run as that account, while the unit's grant applies to that service only.
# Verified on the appliance: `id stayconnect-portald` shows its own group alone, and portald reaches the
# socket regardless.

chown -R stayconnect:stayconnect /var/log/stayconnect
# GROUP-WRITABLE, because more than one account writes here now. portald's unit lists
# /var/log/stayconnect in ReadWritePaths, and under its own uid a 0755 directory owned by stayconnect
# refuses it -- systemd would grant the mount and the kernel would still deny the write.
chmod 775 /var/log/stayconnect
# CADDY'S LOG DIRECTORY *AND ITS FILES*. Caddy runs unprivileged; a root-owned log file inside a
# caddy-owned directory still fails to open, and Caddy then refuses the whole config on start AND on reload.
mkdir -p /var/log/caddy && chown -R caddy:caddy /var/log/caddy && chmod 755 /var/log/caddy
say "layout ok"

# ---------------------------------------------------------------- 3. infra containers
say "infra containers"
# THE DATABASE SUPERUSER GETS A RANDOM PASSWORD, NOT "stayconnect" (audit N-10). Postgres listens on loopback
# only, but every local account can reach loopback, and the compose file's default password was the same on
# every appliance. A fresh install generates one here, keeps it 0600 in the secrets directory, and hands it to
# compose (POSTGRES_PASSWORD only takes effect when the cluster is first initialised). An existing database
# volume is left exactly as it is: its password was fixed at its initdb, and changing it is an operation of its
# own (docs/SECURITY_HARDENING.md, "Database superuser password").
PG_SECRET="$ETC/secrets/pg-superuser.env"
if [ ! -s "$PG_SECRET" ] && ! docker volume inspect stayconnect-infra_pgdata >/dev/null 2>&1; then
  ( umask 077; printf 'STAYCONNECT_PG_PASSWORD=%s\n' "$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')" > "$PG_SECRET" )
  say "generated a random database superuser password ($PG_SECRET)"
fi
if [ -s "$PG_SECRET" ]; then set -a; . "$PG_SECRET"; set +a; fi
[ -n "${STAYCONNECT_PG_PASSWORD:-}" ] || say "NOTE: this appliance's database predates the generated superuser password; see docs/SECURITY_HARDENING.md"
( cd "$DEPLOY/compose" && docker compose -f infra.yml up -d )
for _ in $(seq 1 60); do
  docker exec stayconnect-pg pg_isready -U stayconnect >/dev/null 2>&1 && break
  sleep 2
done
docker exec stayconnect-pg pg_isready -U stayconnect >/dev/null 2>&1 || die "postgres never became ready"
say "infra ok"

# ---------------------------------------------------------------- 4. database, from the FACTORY-CLEAN BASELINE
# deploy/scripts/site-db-init.sh: the baseline as ONE transaction on a blank database, never re-applied to an
# existing one, Gate-P converged every time, hypertables and iam_v2 ownership asserted. It refuses (and changes
# nothing) when the schema is not what this release installs; REINITIALISE_UNUSED_DB=1 rebuilds such a schema
# only after proving it holds no operators, tenants, clients, sessions or vouchers.
say "database"
DBINIT_OUT="$(mktemp)"
DBINIT_ARGS=(--migrations "$SC/data-plane/migrations")
[ "${REINITIALISE_UNUSED_DB:-0}" = 1 ] && DBINIT_ARGS+=(--reinitialise-unused)
if ! bash "$DEPLOY/scripts/site-db-init.sh" "${DBINIT_ARGS[@]}" 2>&1 | tee "$DBINIT_OUT"; then
  rm -f "$DBINIT_OUT"; die "the site database is not ready (see above)"
fi
# A SCHEMA BUILT FROM A BASELINE HAS AN EMPTY MIGRATION LEDGER, so the upgrade procedure must be told which
# migrations that baseline already contained. Recorded once, when the baseline was actually applied here.
if grep -q 'baseline applied' "$DBINIT_OUT" && [ ! -s "$ETC/schema-baseline" ]; then
  mkdir -p "$ETC" && baseline_through "$SC/data-plane/migrations/baseline/0000_production_baseline.sql" > "$ETC/schema-baseline"
  chmod 0644 "$ETC/schema-baseline"
fi
rm -f "$DBINIT_OUT"
say "database ok"

# ---------------------------------------------------------------- 5. credentials + local key material
say "credentials"
if [ ! -s "$ETC/.dsn" ]; then
  bash "$DEPLOY/gatep/gatep-set-passwords.sh" --pg-exec "docker exec -i stayconnect-pg" \
       --db stayconnect_site --dsn-out "$ETC/.dsn" >/dev/null
fi
# keybootstrap creates the durable-throttle key, the OTP generation-1 key AND the voucher code-encryption
# key. The last one is not optional on a production build: IAM-v2 VOUCHER cannot be configured off there, so
# scd refuses to start without it and crash-loops on first boot.
KEYBOOTSTRAP_DSN="postgres://stayconnect:${STAYCONNECT_PG_PASSWORD:-stayconnect}@127.0.0.1:5432/stayconnect_site?sslmode=disable" \
  "$SC/bin/keybootstrap" >/dev/null
for k in throttle.key voucher_dek.key otp_hmac_1.key; do
  [ -s "$ETC/secrets/$k" ] || die "keybootstrap did not produce $k"
done
# VENDOR TRUST KEY -- the PUBLIC verification half only.
#
# Optional for the online path, REQUIRED before this appliance may accept an offline activation package:
# the package is believable only because it is signed by a key pinned here FIRST, before it arrived.
#
# Two supply routes, both part of the normal install rather than a manual step somebody remembers:
#
#   deploy/pki/vendor-license.pub   the deployment slot. Whatever builds the deploy tree drops the vendor's
#                                   public key here and every appliance built from it is pinned.
#   VENDOR_PUB=/path/to/key.pub     an explicit override for a one-off or a different vendor key.
#
# Installing is idempotent and refuses to replace a DIFFERENT key without --force, so re-running
# provisioning on an appliance that is already in service can never silently re-point its trust root.
VENDOR_PUB_SLOT="$DEPLOY/pki/vendor-license.pub"
if [ -n "${VENDOR_PUB:-}" ]; then
  bash "$DEPLOY/scripts/install-vendor-trust-key.sh" "$VENDOR_PUB"
elif [ -s "$VENDOR_PUB_SLOT" ]; then
  bash "$DEPLOY/scripts/install-vendor-trust-key.sh" "$VENDOR_PUB_SLOT"
elif [ -s "$ETC/vendor-license.pub" ]; then
  say "vendor trust key already installed ($(bash "$DEPLOY/scripts/install-vendor-trust-key.sh" --show | tail -1))"
elif [ "${OFFLINE_ACTIVATION_ONLY:-0}" = 1 ]; then
  die "OFFLINE_ACTIVATION_ONLY=1 but no vendor trust key: an offline activation package is believable only because it is
       signed by a key pinned here first. Supply the vendor's PUBLIC key at $VENDOR_PUB_SLOT or VENDOR_PUB=/path."
else
  say "NOTE: no vendor trust key installed. Online activation is unaffected; OFFLINE first activation will"
  say "      refuse every package until one is pinned. Put the vendor's PUBLIC key at"
  say "      $VENDOR_PUB_SLOT and re-run, or run deploy/scripts/install-vendor-trust-key.sh directly."
fi
# ASSIGNMENT REGISTRY ROOT ANCHOR — required by BOTH activation paths.
#
# Without it the appliance logs "no trusted registry and no root anchor — assignment agent disabled" and
# then never adopts an assignment. Activation in the control panel appears to succeed and the appliance
# simply never converges: nothing errors, it stays awaiting-assignment forever. That is the worst shape a
# missing prerequisite can take, so it is installed here rather than discovered at a site.
ASSIGN_ROOT_SLOT="$DEPLOY/pki/assignment-registry-root.pub"
if [ -n "${ASSIGN_ROOT_PUB:-}" ]; then
  bash "$DEPLOY/scripts/install-assignment-root-anchor.sh" "$ASSIGN_ROOT_PUB"
elif [ -s "$ASSIGN_ROOT_SLOT" ]; then
  bash "$DEPLOY/scripts/install-assignment-root-anchor.sh" "$ASSIGN_ROOT_SLOT"
elif [ -s "$ETC/assignment-registry-root.pub" ]; then
  say "assignment root anchor already installed ($(bash "$DEPLOY/scripts/install-assignment-root-anchor.sh" --show | tail -1))"
else
  # Formerly a NOTE, and the install carried on: the appliance then registered, was activated in Central, and
  # never converged, with nothing anywhere saying why. Every activation path needs this anchor, so it is required.
  die "no assignment registry root anchor. Without it this appliance can be activated in Central but will NEVER
       adopt its assignment. Supply Central's assignment-registry-root.pub (from /opt/stayconnect/central/appliance-trust/
       on Central) at $ASSIGN_ROOT_SLOT or ASSIGN_ROOT_PUB=/path, verified against Central's FINGERPRINTS.txt."
fi
say "credentials ok"

# ---------------------------------------------------------------- 6. service environment (NO IDENTITY)
#
# CENTRAL IS A NAME, NOT AN ADDRESS, AND IT COMES FROM VERSIONED CONFIGURATION.
#
# CENTRAL_BASE is the endpoint this appliance will dial for the rest of its life. It is resolved by
# lib-central-endpoint.sh: the environment wins, then deploy/config/central-endpoint.env, then it stops.
# There is no fallback in code -- a plausible-looking default would point appliances at DNS that may not
# exist, and nobody would find out until a site was already installed.
#
#   bash provision-fresh-appliance.sh                                   # the shipped Production endpoint
#   CENTRAL_BASE=https://central.lab.internal bash provision-...        # a lab or staging install
#
# The connection is always appliance-initiated OUTBOUND over the WAN. Central never dials in, never needs a
# port-forward, and never needs to reach the hotel LAN. The appliance API is also a different surface from
# the human admin UI, so the UI can later require MFA/VPN/Zero-Trust without becoming a runtime dependency
# of a hotel's connectivity.
say "environment"
# shellcheck source=lib-central-endpoint.sh
. "$DEPLOY/scripts/lib-central-endpoint.sh"
sc_load_central_endpoint "$DEPLOY"
sc_validate_central_base "$CENTRAL_BASE"
if [ -n "${CENTRAL_MTLS_BASE:-}" ]; then
  sc_validate_central_base "${CENTRAL_MTLS_BASE}" CENTRAL_MTLS_BASE
fi
sc_report_central_dns "$CENTRAL_BASE"

# RESOLVERS THAT DISAGREE ABOUT CENTRAL ARE A HARD STOP.
#
# Three cases, and only one of them is a fault:
#
#   every resolver answers   fine.
#   no resolver answers      fine. That is an offline installation, or DNS published later; the appliance
#                            simply waits, and provisioning has already said so.
#   SOME answer, some say    NOT fine. systemd-resolved falls back on a timeout or SERVFAIL -- "no answer"
#   NXDOMAIN                 -- but NXDOMAIN is a real answer, so it is accepted and returned. The appliance
#                            then reaches Central or does not depending on which resolver resolved happens
#                            to be using, and it switches by itself. Registration and heartbeat stop and
#                            start for no visible reason, guests are unaffected, and nobody can reproduce it.
#
# Nothing here rewrites the hotel's WAN or DNS configuration: which resolvers a site uses is the site's
# decision. Provisioning only refuses to hand over an appliance whose Central endpoint resolves by luck.
if [ -n "${SC_DNS_IP:-}" ] && [ -n "${SC_DNS_DENIERS:-}" ]; then
  say ""
  say "STOP: this appliance's resolvers DISAGREE about $CENTRAL_BASE."
  say "  answers:      ${SC_DNS_ANSWERED_BY} -> ${SC_DNS_IP}"
  say "  say NXDOMAIN: ${SC_DNS_DENIERS}"
  say ""
  say "Resolution would be intermittent, and the failure it produces looks like a network fault rather"
  say "than a configuration one. Fix it at the site before this appliance goes into service: either remove"
  say "the resolvers that deny the name, or make them resolve it."
  say ""
  say "  deploy/scripts/appliance-dns-align.sh          shows exactly which resolvers disagree"
  say "  deploy/scripts/appliance-dns-align.sh --apply  removes the deniers (checks public DNS first)"
  say ""
  say "Deliberately continuing anyway (a staging box, or a resolver about to be fixed):"
  say "  ALLOW_SPLIT_DNS=1 bash provision-fresh-appliance.sh"
  [ "${ALLOW_SPLIT_DNS:-0}" = "1" ] || die "refusing to provision an appliance whose Central endpoint resolves by luck"
  say "ALLOW_SPLIT_DNS=1 set — continuing with intermittent resolution, on purpose."
fi

# CENTRAL'S CA MUST BE TRUSTED BEFORE THE APPLIANCE EVER DIALS IT.
#
# Central presents a certificate from a private CA. Without it in the system trust store every outbound
# call fails verification -- and self-registration treats that exactly like an unreachable Central: it gives
# up quietly and retries. The appliance then reports "awaiting enrollment: run the local setup wizard",
# which sends the operator to a wizard while the real problem is that it never trusted Central at all.
# A live Production appliance sat in that state for a day.
#
# Supply it the same way as the vendor key: the deployment slot, or an explicit path.
CENTRAL_CA_SLOT="$DEPLOY/pki/central-ca.crt"
if [ -n "${CENTRAL_CA:-}" ]; then
  bash "$DEPLOY/scripts/install-central-trust.sh" "$CENTRAL_CA"
elif [ -s "$CENTRAL_CA_SLOT" ]; then
  bash "$DEPLOY/scripts/install-central-trust.sh" "$CENTRAL_CA_SLOT"
elif [ -s /usr/local/share/ca-certificates/stayconnect-central-ca.crt ]; then
  say "Central CA already trusted (/usr/local/share/ca-certificates/stayconnect-central-ca.crt)"
elif [ "${OFFLINE_ACTIVATION_ONLY:-0}" = 1 ]; then
  say "NOTE: OFFLINE_ACTIVATION_ONLY=1 and no Central CA: this appliance will not self-register online; it is"
  say "      activated with an offline activation package (Admin Console -> Appliance & licence)."
else
  die "no Central CA supplied, so this appliance could never verify Central and would never self-register
       (it would look like an unreachable Central and retry silently). Supply Central's TLS CA (central-tls-ca.crt
       from /opt/stayconnect/central/appliance-trust/ on Central) at $CENTRAL_CA_SLOT or CENTRAL_CA=/path,
       or set OFFLINE_ACTIVATION_ONLY=1 for a site that is activated offline."
fi
# NO TENANT OR SITE ID IS EVER WRITTEN HERE. Identity arrives only through enrollment -> claim -> signed
# assignment. Seeding it would be indistinguishable from cloning another appliance.
. "$ETC/.dsn"
WAN_IFACE="${WAN_IFACE:-$(ip -o -4 route show default | awk '{print $5}' | head -1)}"
LAN_IFACE="${LAN_IFACE:-$(ls /sys/class/net | grep -vE '^(lo|docker|br-|veth)' | grep -v "^${WAN_IFACE}$" | head -1)}"
[ -n "$WAN_IFACE" ] || die "cannot determine the WAN interface"
umask 027
[ -f "$ETC/scd.env" ] || cat > "$ETC/scd.env" <<EOF
SCD_DB_URL=${SCD_DB_URL}
SCD_SOCKET=/run/stayconnect/scd.sock
SCD_CTRLAPI_BASE=${CENTRAL_BASE}
SCD_MTLS_BASE=${CENTRAL_MTLS_BASE:-${CENTRAL_BASE}:9443}
SCD_AUTO_REGISTER=true
SCD_METRICS_ADDR=127.0.0.1:9101
STAYCONNECT_IAMV2_MASTER=true
STAYCONNECT_IAMV2_OTP=true
STAYCONNECT_IAMV2_SOCIAL=true
SCD_PAYMENT_DB_URL=${SCD_PAYMENT_DB_URL}
STAYCONNECT_PHASE4_PAYMENT_OUTCOME_DSN=${SCD_PAYMENT_OUTCOME_DB_URL}
SCD_POSTING_DB_URL=${SCD_POSTING_DB_URL}
# The durable sign-in throttle and keyed one-time codes (audit N-27). keybootstrap above has just created
# throttle.key and otp_hmac_1.key; without these two lines the keys were made and never used, and guest
# sign-in was throttled only in memory (lost on every scd restart) although the contracts rely on the durable
# throttle (ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md "Throttles"). Both fail closed if their key is missing.
SCD_SECRETS_DIR=${ETC}/secrets
SCD_DURABLE_THROTTLE=true
SCD_OTP_HMAC=true
EOF
[ -f "$ETC/edged.env" ] || cat > "$ETC/edged.env" <<EOF
EDGED_DB_URL=${EDGED_DB_URL}
EDGED_SCD_SOCKET=/run/stayconnect/scd.sock
EDGED_COOKIE_SECURE=true
STAYCONNECT_IAMV2_MASTER=true
EOF
[ -f "$ETC/acctd.env" ] || printf 'ACCTD_DB_URL=%s\nACCTD_SCD_SOCKET=/run/stayconnect/scd.sock\n' "$ACCTD_DB_URL" > "$ETC/acctd.env"
# netd needs the interface names to exist before the Hotel Admin certificate can be minted for the
# management IP: the cert manager reads NETD_MGMT_IFACE/NETD_WAN_IFACE and refuses without them.
[ -f "$ETC/netd.env" ] || cat > "$ETC/netd.env" <<EOF
NETD_DB_URL=${NETD_DB_URL}
NETD_WAN_IFACE=${WAN_IFACE}
NETD_MGMT_IFACE=${WAN_IFACE}
NETD_MGMT_ADDR=auto
NETD_LAN_PHYS=${LAN_IFACE}
EOF
# THE ADMIN CONSOLE'S PORT 443 IS PINNED TO THE WAN ADDRESS (docs/HOTEL_ADMIN_IP_ALIAS.md). The WAN is also the
# management interface (two-NIC topology, docs/DEPLOYMENT_APPLIANCE.md), so without a pin 443 was accepted on
# EVERY address the WAN NIC carries. "auto" pins it to the WAN's current primary IPv4 and follows a change made
# through System network (cmd/netd/nft_watch.go); a literal address would lock the console out at the first WAN
# readdressing. Added to an existing netd.env too, because this script never rewrites one.
grep -qE '^NETD_MGMT_ADDR=' "$ETC/netd.env" || printf 'NETD_MGMT_ADDR=auto\n' >> "$ETC/netd.env"
[ -f "$ETC/portald.env" ] || printf 'PORTALD_SCD_SOCKET=/run/stayconnect/scd.sock\n' > "$ETC/portald.env"
[ -f "$ETC/pmsd.env" ] || cp "$DEPLOY/env/pmsd.env.dark" "$ETC/pmsd.env"

# THE SUPPORTED PRODUCT PROFILE: CLIENT ACCESS WITH ENFORCEMENT.
#
# Without these lines a provisioned appliance signed Clients in and let none of them online: every IAM-v2 session
# opens in PENDING_ENFORCEMENT and only the enforcement plane promotes it, and vouchers, Free packages and client
# accounts are redeemed through the commerce routes (STAYCONNECT_PHASE2_*), which were never switched on here.
# PRE-LIVE carries exactly these flags (set by hand). What is deliberately NOT here: the PMS connector and room
# sign-in (STAYCONNECT_PHASE3_PMS_* / _CHECKOUT_GRACE, a commissioned and separately authorised step,
# docs/runbooks/PMS-Interface-Commissioning.md) and anything financial (STAYCONNECT_PHASE4_*, real money).
#
# ensure_kv adds a key only when it is ABSENT and never changes an existing value, so an operator's choice on an
# appliance that is already in service survives a re-run.
ensure_kv() { # <file> <key> <value>
  grep -qE "^$2=" "$ETC/$1" 2>/dev/null && return 0
  printf '%s=%s\n' "$2" "$3" >> "$ETC/$1"
}
# The plane's producer is whichever account acctd runs as: the uid the kernel reports to netd over SO_PEERCRED.
ACCTD_USER="$(sed -n 's/^User=\(.*\)$/\1/p' "$DEPLOY/systemd/stayconnect-acctd.service" | tail -1)"
PRODUCER_UID="$(id -u "${ACCTD_USER:-root}")" || die "cannot resolve a uid for acctd's user '${ACCTD_USER:-root}'"
ensure_kv scd.env     STAYCONNECT_PHASE2_MASTER true
ensure_kv scd.env     STAYCONNECT_PHASE2_PORTAL true
ensure_kv portald.env STAYCONNECT_PHASE2_MASTER true
ensure_kv portald.env STAYCONNECT_PHASE2_PORTAL true
ensure_kv edged.env   STAYCONNECT_PHASE2_MASTER true
ensure_kv edged.env   STAYCONNECT_PHASE2_ADMIN  true
ensure_kv netd.env    STAYCONNECT_PHASE3_MASTER true
ensure_kv netd.env    NETD_PHASE3_PRODUCER_UID  "$PRODUCER_UID"
ensure_kv acctd.env   STAYCONNECT_PHASE3_MASTER true
bash "$DEPLOY/scripts/check-phase3-enforcement-plane.sh" "$ETC" >/dev/null \
  || die "the environment is not coherent: $(bash "$DEPLOY/scripts/check-phase3-enforcement-plane.sh" "$ETC" 2>&1 | head -3)"
chmod 640 "$ETC"/*.env && chown root:stayconnect "$ETC"/*.env
grep -qE '^(SCD|EDGED|ACCTD)_(TENANT|SITE)_ID=' "$ETC"/*.env && die "an identity was seeded into the environment"
say "environment ok (WAN=$WAN_IFACE, LAN=$LAN_IFACE, no identity pinned)"
# The restrictive umask was for the environment files only (they carry database passwords). Everything after
# this - units, Caddy configuration, the certificate manager files - must be readable by the accounts that use it.
umask 022

# ---------------------------------------------------------------- 7. units, proxy, certificate
say "units + proxy"
cp "$DEPLOY/tmpfiles"/*.conf /etc/tmpfiles.d/ 2>/dev/null || true
cp "$DEPLOY/sysctl"/*.conf /etc/sysctl.d/ 2>/dev/null || true
systemd-tmpfiles --create >/dev/null 2>&1 || true
sysctl --system >/dev/null 2>&1 || true
bash "$DEPLOY/scripts/install-service-units.sh" "$DEPLOY"
install -m 0644 "$DEPLOY/caddy/Caddyfile.edge" /etc/caddy/Caddyfile
install -m 0644 "$DEPLOY/caddy/stayconnect-caddy.edge.service" /etc/systemd/system/stayconnect-caddy.service
mkdir -p /etc/caddy/hotel-admin
# The Caddyfile imports a vhost the cert manager generates, and the cert manager needs Caddy's local CA,
# which only exists after Caddy has run once. An empty import breaks that cycle.
# Created with an explicit mode: this script runs under umask 027 from the environment step, which made the
# placeholder root-only, and Caddy (user caddy) then refused the whole configuration it imports. Measured on a
# fresh appliance VM.
chmod 0755 /etc/caddy/hotel-admin
[ -f /etc/caddy/hotel-admin/vhost.caddy ] || install -m 0644 /dev/null /etc/caddy/hotel-admin/vhost.caddy
[ -s /etc/caddy/hotel-admin/vhost.caddy ] || chmod 0644 /etc/caddy/hotel-admin/vhost.caddy
systemctl daemon-reload
systemctl enable --now stayconnect-caddy >/dev/null
for _ in $(seq 1 30); do [ -f /var/lib/caddy/.local/share/caddy/pki/authorities/local/intermediate.crt ] && break; sleep 2; done
# THE FIRST CERTIFICATE IS FORCED; later runs use renew, so a re-run never replaces a healthy certificate that
# staff browsers may already trust. install-service-units.sh enables the renewal timer with --now, and on a
# freshly booted machine its first run fires before Caddy's local CA exists: it fails, and the manager then backs
# off, so a plain renew here was skipped too and the Admin Console had no certificate at all (measured on a fresh
# VM). With no certificate yet, rotate issues one regardless of back-off; with one, renew acts only when due.
if [ -s /etc/caddy/hotel-admin/hotel-admin.crt ]; then cm=renew; else cm=rotate; fi
# The manager exits 0 (nothing due) or 2 (renewed — what a first rotate always returns); 10 and 20 are failures.
rc=0; bash "$DEPLOY/scripts/hotel-admin-cert-manager.sh" "$cm" >/dev/null 2>&1 || rc=$?
case "$rc" in 0|2) ;; *) die "the Admin Console certificate could not be issued ($cm, exit $rc); see: $DEPLOY/scripts/hotel-admin-cert-manager.sh status";; esac
# The early, expected failure of the timer's first run is history once a certificate exists; clear it so the
# unit health check reports the present state.
systemctl reset-failed stayconnect-hotel-admin-cert-renew.service >/dev/null 2>&1 || true
chown -R caddy:caddy /var/log/caddy   # the cert-manager health check can create a root-owned log file
systemctl reload stayconnect-caddy || systemctl restart stayconnect-caddy
say "units + proxy ok"

# ---------------------------------------------------------------- 8. services
say "services"
# pmsd is installed and NOT enabled: the PMS connector is commissioned deliberately (its own flags, keys and a
# PMS window), never as a side effect of provisioning. The Admin Console unit is enabled here and started by
# deploy-hotel-admin.sh once a console release exists.
for u in stayconnect-scd stayconnect-edged stayconnect-acctd stayconnect-netd stayconnect-portald stayconnect-hotel-admin; do
  systemctl enable "$u" >/dev/null 2>&1 || true
done
# stop / reset-failed / start --no-block, never a bare restart: with StartLimitIntervalSec=0 a `systemctl
# restart` of a unit whose start is refused never returns, and this script would hang instead of reporting it.
bounce() {
  systemctl stop "$@" >/dev/null 2>&1 || true
  systemctl reset-failed "$@" >/dev/null 2>&1 || true
  systemctl start --no-block "$@"
}
wait_active() { # <seconds> <unit...>
  local t="$1"; shift
  for _ in $(seq 1 "$t"); do
    local all=1; for u in "$@"; do systemctl is-active --quiet "$u" || all=0; done
    [ "$all" = 1 ] && return 0; sleep 1
  done
  return 1
}
bounce stayconnect-scd
wait_active 60 stayconnect-scd || true
bounce stayconnect-netd stayconnect-acctd stayconnect-edged stayconnect-portald
wait_active 60 stayconnect-scd stayconnect-edged stayconnect-netd stayconnect-portald stayconnect-caddy || true
# acctd exits (and is restarted) while the appliance has no signed assignment: "activating" is its normal state
# before activation, so it is reported, not required.
fail=0
for u in stayconnect-scd stayconnect-edged stayconnect-netd stayconnect-portald stayconnect-caddy stayconnect-acctd; do
  st="$(systemctl is-active "$u" || true)"
  printf '  %-32s %s\n' "$u" "$st"
  case "$u:$st" in
    *:active) ;;
    stayconnect-acctd:activating) ;;
    *) fail=1 ;;
  esac
done
[ "$fail" = 0 ] || die "one or more services are not active (journalctl -u <unit> -b)"
# The production build must say so out loud; a development binary here is the one thing that silently
# reintroduces a configurable guest-IAM authority.
# The journal is captured BEFORE it is searched: `journalctl | grep -q` under pipefail fails whenever grep
# finds the line and exits early, because journalctl then dies of SIGPIPE (measured on a fresh appliance VM).
prod=0
for _ in $(seq 1 20); do
  scdlog="$(journalctl -u stayconnect-scd -b --no-pager 2>/dev/null || true)"
  case "$scdlog" in *build=production*) prod=1; break;; esac
  sleep 1
done
[ "$prod" = 1 ] || die "scd is not a production build — rebuild with -tags stayconnect_production"
say "services ok (scd reports build=production)"

echo
WAN_IP="$(ip -o -4 addr show "$WAN_IFACE" | awk '{print $4}' | cut -d/ -f1 | head -1)"
say "PROVISIONED — factory clean: no tenant, no site, no licence, no Client network."
say ""
say "  Admin Console:  https://${WAN_IP}/   (certificate from this appliance's local CA)"
if [ "${OFFLINE_ACTIVATION_ONLY:-0}" = 1 ]; then
  say "  Activation:     OFFLINE — Admin Console -> Appliance & licence -> Offline activation -> download the"
  say "                  request, import it in Central (Appliances -> Import activation request), Activate, and"
  say "                  upload the activation package here."
else
  say "  Activation:     this appliance registers itself at $CENTRAL_BASE and waits. In Central:"
  say "                  Appliances -> the waiting appliance -> Activate (customer, site, licence terms)."
fi
say "  Then:           Admin Console -> Network -> Client networks (the prepared network, or a VLAN), Apply, Confirm."
say ""
say "Kea stays stopped until the first client network is confirmed; DHCP shows 'waiting' until then, by design."
