#!/usr/bin/env bash
# SHARED BY THE CENTRAL INSTALL / DEPLOY / EXPORT / CLEANUP SCRIPTS. Sourced, never run.
#
# One place for: the host layout, the list of trust material a Central carries for its whole life, the checks
# that prove a set of keys belongs together, dry-run plumbing, and the Postgres/compose helpers. Every script
# that moves a Central must agree on these, and two copies would eventually disagree about which file matters.
#
# Callers set PROG (for log prefixes) before sourcing. DRY_RUN=1 turns every mutating helper into a print.

# ---------------------------------------------------------------- the host layout (docs/DEPLOYMENT_CLOUD.md §3)
SC_OPT="${SC_OPT:-/opt/stayconnect}"
SC_ETC="${SC_ETC:-/etc/stayconnect}"
CENTRAL_ROOT="${CENTRAL_ROOT:-$SC_OPT/central}"
CENTRAL_SECRETS_DIR="${CENTRAL_SECRETS_DIR:-$CENTRAL_ROOT/secrets}"
CENTRAL_COMPOSE_FILE="${CENTRAL_COMPOSE_FILE:-$CENTRAL_ROOT/compose/central-infra.yml}"
CENTRAL_COMPOSE_PROJECT="${CENTRAL_COMPOSE_PROJECT:-stayconnect-central}"
CENTRAL_TOOLING="${CENTRAL_TOOLING:-$CENTRAL_ROOT/tooling}"
CENTRAL_TLS_DIR="${CENTRAL_TLS_DIR:-$CENTRAL_ROOT/tls}"
CENTRAL_PG_CONTAINER="${CENTRAL_PG_CONTAINER:-sc-central-pg}"
CENTRAL_REDIS_CONTAINER="${CENTRAL_REDIS_CONTAINER:-sc-central-redis}"
CENTRAL_DB="${CENTRAL_DB:-stayconnect}"
CENTRAL_DB_USER="${CENTRAL_DB_USER:-stayconnect}"
CENTRAL_INSTALL_ENV="${CENTRAL_INSTALL_ENV:-$SC_ETC/central-install.env}"
CENTRAL_DEPLOYED_JSON="${CENTRAL_DEPLOYED_JSON:-$CENTRAL_ROOT/DEPLOYED.json}"
CENTRAL_RESTORE_MARKER="${CENTRAL_RESTORE_MARKER:-$SC_ETC/central-restored-from.json}"
CENTRAL_PUBLIC_TRUST_DIR="${CENTRAL_PUBLIC_TRUST_DIR:-$CENTRAL_ROOT/appliance-trust}"
CTRLAPI_BIN_PATH="${CTRLAPI_BIN_PATH:-$SC_OPT/bin/ctrlapi}"
CTRLAPI_ENV_PATH="${CTRLAPI_ENV_PATH:-$SC_ETC/ctrlapi.env}"
CENTRAL_ENDPOINT_PATH="${CENTRAL_ENDPOINT_PATH:-$SC_ETC/central-endpoint.env}"
CONSOLE_RELEASES="${CONSOLE_RELEASES:-$SC_OPT/releases/cloud-admin}"
CONSOLE_CURRENT="${CONSOLE_CURRENT:-$SC_OPT/cloud-admin-current}"
CONSOLE_PREVIOUS="${CONSOLE_PREVIOUS:-$SC_OPT/cloud-admin-current.previous}"
CONSOLE_MANIFEST="cloud-admin-release.json"
# Central DB dumps live where stayconnect-backup-cleanup's Central role already retains them.
CENTRAL_DB_BACKUPS="${CENTRAL_DB_BACKUPS:-/root/backups}"
CADDYFILE_PATH="${CADDYFILE_PATH:-/etc/caddy/Caddyfile}"
CADDY_TLS_CRT="${CADDY_TLS_CRT:-/etc/caddy/tls/server.crt}"
CADDY_TLS_KEY="${CADDY_TLS_KEY:-/etc/caddy/tls/server.key}"
SC_RUN_USER="${SC_RUN_USER:-stayconnect}"

# The neutral CA-bundle path handed to appliances in offline activation packages (CTRLAPI_CA_BUNDLE). The first
# host kept it as pki/nats-ca-bundle.crt, a name from the removed message bus; the content is the appliance CA
# chain and nothing else.
CENTRAL_CA_BUNDLE="${CENTRAL_CA_BUNDLE:-$SC_ETC/pki/ca-bundle.crt}"

# Node.js the console needs: next 14.2 requires >= 18.17. 20 LTS is what it is built with (central-build.sh).
CENTRAL_NODE_MIN="${CENTRAL_NODE_MIN:-18.17.0}"

# ---------------------------------------------------------------- THE TRUST MATERIAL, ONE LIST
#
# Paths are relative to /. "req" = a Central cannot be moved without it (every appliance is pinned to it or to
# something it signed); "opt" = carried when present. Regenerating ANY "req" item on a moved Central breaks every
# appliance already in the field; the Root CA may never be regenerated at all (CLAUDE.md §0A.5 — a trust-root
# change is a Product-Owner decision, not an installation step).
#
#   kind|path|mode|what
CENTRAL_TRUST_ITEMS=(
  "req|etc/stayconnect/vendor-license.key|0600|vendor licence signing key (appliances pin its public half)"
  "opt|etc/stayconnect/vendor-license.pub|0644|vendor licence public key"
  "req|etc/stayconnect/assignment-signing.key|0600|assignment signing key (listed in the signed registry)"
  "opt|etc/stayconnect/assignment-signing.pub|0644|assignment signing public key"
  "req|etc/stayconnect/assignment-registry-root.key|0600|assignment registry ROOT key (appliances pin its public half)"
  "opt|etc/stayconnect/assignment-registry-root.pub|0644|assignment registry root public key"
  "req|etc/stayconnect/pki/root-ca.crt|0644|appliance Root CA certificate (trust anchor on every appliance)"
  "req|etc/stayconnect/pki/intermediate-ca.crt|0644|appliance intermediate CA certificate"
  "req|etc/stayconnect/pki/intermediate-ca.key|0600|appliance intermediate CA key (the online signer)"
  "opt|etc/stayconnect/pki/root-ca.key|0600|appliance Root CA key, if never relocated offline"
  "opt|etc/stayconnect/pki-offline/root-ca.key|0600|appliance Root CA key (belongs in cold storage)"
  "opt|etc/stayconnect/pki/server-mtls.crt|0644|mTLS listener certificate (:9443)"
  "opt|etc/stayconnect/pki/server-mtls.key|0600|mTLS listener key"
  "opt|etc/stayconnect/pki/ca-bundle.crt|0644|CA bundle written into offline activation packages"
  "opt|etc/stayconnect/pki/nats-ca-bundle.crt|0644|the same bundle under its legacy name"
  "opt|opt/stayconnect/central/tls/ca.crt|0644|Central internal TLS CA certificate (appliances trust it for :443)"
  "opt|opt/stayconnect/central/tls/ca.key|0600|Central internal TLS CA key"
  "opt|opt/stayconnect/central/tls/ca.srl|0644|Central internal TLS CA serial"
  "opt|opt/stayconnect/central/tls/server.crt|0644|Central :443 certificate"
  "opt|opt/stayconnect/central/tls/server.key|0600|Central :443 key"
  "opt|opt/stayconnect/central/tls/san.cnf|0644|names the :443 certificate was issued for"
  "opt|etc/stayconnect/backup-retention.conf|0644|backup retention policy"
  "opt|etc/stayconnect/backup-retention.pins|0644|operator-pinned backup artefacts"
)
# Retired vendor identities are kept on purpose (vendor-signing-key.sh rotate): appliances still pinned to one
# can only be re-signed with it. Carried by glob.
CENTRAL_TRUST_GLOBS=("etc/stayconnect/vendor-license.key.rotated-*")

# ---------------------------------------------------------------- logging + dry-run plumbing
PROG="${PROG:-central}"
DRY_RUN="${DRY_RUN:-0}"
say()  { echo "[$PROG] $*"; }
warn() { echo "[$PROG] WARNING: $*" >&2; }
die()  { echo "[$PROG] ABORT: $*" >&2; exit 1; }
step() { echo; echo "[$PROG] ==== $* ===="; }

# run: execute, or in dry-run print what would be executed. For commands whose effect is a change.
run() {
  if [ "$DRY_RUN" = 1 ]; then
    printf '[%s] DRY-RUN would run:' "$PROG"; printf ' %q' "$@"; printf '\n'
    return 0
  fi
  "$@"
}

# write_file <dest> <mode> [owner:group] — content on stdin. Atomic (temp + rename) and dry-run aware.
write_file() {
  local dest="$1" mode="$2" owner="${3:-root:root}" tmp
  if [ "$DRY_RUN" = 1 ]; then
    case "$mode" in
      0600|600|0400|400)   # a secret-bearing file: never echo its content, not even in a dry run
        say "DRY-RUN would write $dest (mode $mode, $owner): $(wc -c | tr -d ' ') bytes, content withheld" ;;
      *)
        say "DRY-RUN would write $dest (mode $mode, $owner):"
        sed 's/^/        | /' | grep -viE 'password|secret|://[^:]*:[^@]*@' || true ;;
    esac
    return 0
  fi
  mkdir -p "$(dirname "$dest")"
  tmp="$(mktemp "$(dirname "$dest")/.tmp.XXXXXX")"
  cat > "$tmp"
  chmod "$mode" "$tmp"
  chown "$owner" "$tmp" 2>/dev/null || true
  mv -f "$tmp" "$dest"
}

need_root() { [ "$DRY_RUN" = 1 ] || [ "$(id -u)" = 0 ] || die "run as root"; }

# ---------------------------------------------------------------- small readers
# envfile_get <file> <KEY> — read an assignment as data (never source a file that holds secrets).
envfile_get() {
  [ -f "$1" ] || return 1
  local v
  v="$(grep -E "^[[:space:]]*$2=" "$1" | tail -1 | cut -d= -f2- | sed 's/^[[:space:]]*//; s/[[:space:]]*$//; s/^"//; s/"$//')"
  [ -n "$v" ] || return 1
  printf '%s' "$v"
}

# envfile_set <file> <KEY> <value> — replace or append one assignment in place.
envfile_set() {
  local f="$1" k="$2" v="$3" tmp
  if [ "$DRY_RUN" = 1 ]; then say "DRY-RUN would set $k in $f"; return 0; fi
  tmp="$(mktemp)"
  if grep -qE "^[[:space:]]*#?[[:space:]]*$k=" "$f" 2>/dev/null; then
    awk -v k="$k" -v v="$v" 'BEGIN{done=0}
      { if (!done && $0 ~ "^[[:space:]]*#?[[:space:]]*" k "=") { print k "=" v; done=1 } else print }' "$f" > "$tmp"
  else
    cat "$f" > "$tmp"; printf '%s=%s\n' "$k" "$v" >> "$tmp"
  fi
  cat "$tmp" > "$f"; rm -f "$tmp"
}

host_of_url() { local h="${1#https://}"; h="${h#http://}"; h="${h%%/*}"; printf '%s' "${h%%:*}"; }

sha256_of() { sha256sum "$1" | cut -d' ' -f1; }

version_ge() { # version_ge <have> <want>
  [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]
}

# ---------------------------------------------------------------- key identity (python3, stdlib only)
# ed25519 raw 64-byte private key files are seed||public (Go's ed25519.GenerateKey). Key ids are the first 8
# bytes of sha256(public), hex — the id Central stores (assignment.KeyID, license.KeyIDFor).
ed25519_keyid() { # ed25519_keyid <file> private|public
  python3 - "$1" "$2" <<'PY'
import sys, hashlib
raw = open(sys.argv[1], 'rb').read()
if sys.argv[2] == 'private':
    if len(raw) != 64: sys.exit("%s: not a 64-byte raw ed25519 private key" % sys.argv[1])
    raw = raw[32:]
elif len(raw) != 32:
    sys.exit("%s: not a 32-byte raw ed25519 public key" % sys.argv[1])
print(hashlib.sha256(raw).digest()[:8].hex())
PY
}

ed25519_pub_matches() { # ed25519_pub_matches <private> <public>
  python3 - "$1" "$2" <<'PY'
import sys
k = open(sys.argv[1], 'rb').read(); p = open(sys.argv[2], 'rb').read()
sys.exit(0 if len(k) == 64 and k[32:] == p else 1)
PY
}

ed25519_write_pub() { # ed25519_write_pub <private> <public-out>
  python3 - "$1" "$2" <<'PY'
import sys
k = open(sys.argv[1], 'rb').read()
if len(k) != 64: sys.exit("not a 64-byte raw ed25519 private key")
open(sys.argv[2], 'wb').write(k[32:])
PY
}

cert_fpr() { openssl x509 -in "$1" -noout -fingerprint -sha256 2>/dev/null | sed 's/^.*=//'; }

# key_matches_cert <key.pem> <cert.pem> — the private key really is the certificate's.
key_matches_cert() {
  local a b
  a="$(openssl pkey -in "$1" -pubout 2>/dev/null | sha256sum)"
  b="$(openssl x509 -in "$2" -noout -pubkey 2>/dev/null | sha256sum)"
  [ -n "$a" ] && [ "$a" = "$b" ]
}

# ---------------------------------------------------------------- the trust-set consistency check
#
# verify_trust_set <root-prefix> — proves the material under <prefix> belongs together, WITHOUT starting
# anything. Prints one line per finding; returns non-zero on any failure. <prefix> is "/" on a host, or an
# extracted export bundle's files/ directory.
#
# These are the mistakes that are silent until a hotel refuses something: a .pub that is not the half of its
# .key; the vendor and assignment keys being the same key; an intermediate that does not chain to the root the
# fleet trusts, or whose key is not the certificate's; a :443 certificate issued by some other CA.
verify_trust_set() {
  local p="${1%/}" bad=0 f
  _ok()  { printf '  OK    %s\n' "$*"; }
  _bad() { printf '  FAIL  %s\n' "$*"; bad=1; }
  _inf() { printf '  info  %s\n' "$*"; }

  local vk="$p/etc/stayconnect/vendor-license.key" ak="$p/etc/stayconnect/assignment-signing.key"
  local rk="$p/etc/stayconnect/assignment-registry-root.key"
  for f in "$vk" "$ak" "$rk"; do
    if [ ! -s "$f" ]; then _bad "missing $f"; continue; fi
    if [ "$(wc -c < "$f")" != 64 ]; then _bad "$f is not a 64-byte raw ed25519 private key"; continue; fi
    _ok "$(basename "$f") key_id $(ed25519_keyid "$f" private)"
    if [ -s "${f%.key}.pub" ]; then
      if ed25519_pub_matches "$f" "${f%.key}.pub"; then _ok "$(basename "${f%.key}.pub") is its public half"
      else _bad "$(basename "${f%.key}.pub") is NOT the public half of $(basename "$f")"; fi
    fi
  done
  if [ -s "$vk" ] && [ -s "$ak" ] && cmp -s "$vk" "$ak"; then _bad "vendor key and assignment key are the SAME key"; fi
  if [ -s "$rk" ] && { cmp -s "$rk" "$vk" || cmp -s "$rk" "$ak"; }; then _bad "registry root key reuses another key"; fi

  local rc="$p/etc/stayconnect/pki/root-ca.crt" ic="$p/etc/stayconnect/pki/intermediate-ca.crt"
  local ik="$p/etc/stayconnect/pki/intermediate-ca.key"
  if [ -s "$rc" ] && [ -s "$ic" ] && [ -s "$ik" ]; then
    if openssl verify -CAfile "$rc" "$ic" >/dev/null 2>&1; then _ok "intermediate CA chains to the Root CA ($(cert_fpr "$rc" | cut -c1-23)…)"
    else _bad "intermediate-ca.crt does NOT chain to root-ca.crt"; fi
    if key_matches_cert "$ik" "$ic"; then _ok "intermediate-ca.key is the intermediate certificate's key"
    else _bad "intermediate-ca.key does NOT match intermediate-ca.crt"; fi
    for f in "$p/etc/stayconnect/pki-offline/root-ca.key" "$p/etc/stayconnect/pki/root-ca.key"; do
      [ -s "$f" ] || continue
      if key_matches_cert "$f" "$rc"; then _ok "$(dirname "$f" | sed "s|^$p||")/root-ca.key matches the Root CA"
      else _bad "$f does NOT match root-ca.crt"; fi
    done
    local sm="$p/etc/stayconnect/pki/server-mtls.crt"
    if [ -s "$sm" ]; then
      if openssl verify -CAfile "$rc" -untrusted "$ic" "$sm" >/dev/null 2>&1; then _ok "server-mtls.crt chains to the appliance CA"
      else _bad "server-mtls.crt does not chain to the appliance CA"; fi
    fi
  else
    _bad "appliance CA incomplete (need pki/root-ca.crt, pki/intermediate-ca.crt, pki/intermediate-ca.key)"
  fi

  local tca="$p/opt/stayconnect/central/tls/ca.crt" tck="$p/opt/stayconnect/central/tls/ca.key"
  local tsc="$p/opt/stayconnect/central/tls/server.crt" tsk="$p/opt/stayconnect/central/tls/server.key"
  if [ -s "$tca" ]; then
    if [ -s "$tck" ] && key_matches_cert "$tck" "$tca"; then _ok "Central TLS CA key matches its certificate"
    elif [ -s "$tck" ]; then _bad "Central TLS ca.key does NOT match ca.crt"
    else _inf "Central TLS CA present without its key: the :443 certificate cannot be re-issued here"; fi
    if [ -s "$tsc" ]; then
      if openssl verify -CAfile "$tca" "$tsc" >/dev/null 2>&1; then _ok "Central :443 certificate chains to the Central TLS CA"
      else _bad "Central :443 server.crt does not chain to tls/ca.crt"; fi
      if [ -s "$tsk" ] && ! key_matches_cert "$tsk" "$tsc"; then _bad "Central :443 server.key does not match server.crt"; fi
    fi
  else
    _inf "no Central internal TLS CA (a public/ACME certificate, or a brand-new host)"
  fi
  return "$bad"
}

# ---------------------------------------------------------------- Postgres (always through the container)
pg_q()   { docker exec "$CENTRAL_PG_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$CENTRAL_DB_USER" -d "$CENTRAL_DB" -tA "$@" </dev/null; }
pg_in()  { docker exec -i "$CENTRAL_PG_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$CENTRAL_DB_USER" -d "$CENTRAL_DB" -tA "$@"; }
pg_table_exists() { [ "$(pg_q -c "SELECT to_regclass('public.$1') IS NOT NULL")" = "t" ]; }
pg_public_table_count() {
  pg_q -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'"
}

# pg_rowcounts — "table<TAB>rows" for every public base table, sorted. Compared across a host move.
pg_rowcounts() {
  printf '%s\n' "SELECT format('SELECT %L || chr(9) || count(*) FROM public.%I', table_name, table_name)
                   FROM information_schema.tables
                  WHERE table_schema='public' AND table_type='BASE TABLE' ORDER BY table_name \\gexec" \
    | pg_in -q | sed '/^$/d' | sort
}

pg_hypertable_count() {
  pg_q -c "SELECT count(*) FROM timescaledb_information.hypertables" 2>/dev/null || echo 0
}

pg_wait_healthy() { # pg_wait_healthy [seconds]
  local i s1 s2
  for i in $(seq 1 "${1:-120}"); do
    s1="$(docker inspect -f '{{.State.Health.Status}}' "$CENTRAL_PG_CONTAINER" 2>/dev/null || echo missing)"
    s2="$(docker inspect -f '{{.State.Health.Status}}' "$CENTRAL_REDIS_CONTAINER" 2>/dev/null || echo missing)"
    # Healthy AND answering a real query twice, a few seconds apart: a first-start init cycle restarts the
    # server once, and "healthy" alone has been observed inside that window.
    if [ "$s1" = healthy ] && [ "$s2" = healthy ] && pg_q -c 'SELECT 1' >/dev/null 2>&1; then
      sleep 3
      pg_q -c 'SELECT 1' >/dev/null 2>&1 && return 0
    fi
    sleep 1
  done
  die "infra not healthy after ${1:-120}s (pg=$s1 redis=$s2) — docker compose -p $CENTRAL_COMPOSE_PROJECT -f $CENTRAL_COMPOSE_FILE logs"
}

central_compose() {
  CENTRAL_SECRETS_DIR="$CENTRAL_SECRETS_DIR" docker compose -p "$CENTRAL_COMPOSE_PROJECT" -f "$CENTRAL_COMPOSE_FILE" "$@"
}

# ---------------------------------------------------------------- ctrlapi
# ctrlapi_ready <timeout-seconds> — /readyz on loopback answers "ready" AND the unit did not restart meanwhile.
# `systemctl is-active` is not readiness: a crash-looping unit reads "active" between restarts.
ctrlapi_ready() {
  local i before after body
  before="$(systemctl show stayconnect-ctrlapi -p NRestarts --value 2>/dev/null || echo 0)"
  for i in $(seq 1 "${1:-60}"); do
    body="$(curl -fsS --max-time 2 http://127.0.0.1:8080/readyz 2>/dev/null || true)"
    case "$body" in *'"ready"'*)
      after="$(systemctl show stayconnect-ctrlapi -p NRestarts --value 2>/dev/null || echo 0)"
      [ "$after" = "$before" ] || warn "ctrlapi answered but restarted $before->$after while starting"
      say "ctrlapi ready after ${i}s: $body"; return 0 ;;
    esac
    sleep 1
  done
  return 1
}

# Start a unit without blocking on it: on these hosts `systemctl restart` of a unit that refuses to start can
# wait forever, so stop / reset-failed / start --no-block and then poll for the real readiness signal.
unit_restart_noblock() {
  run systemctl stop "$1" 2>/dev/null || true
  run systemctl reset-failed "$1" 2>/dev/null || true
  run systemctl start --no-block "$1"
}

# binary_revision <file> — the vcs.revision Go embedded in the binary, read without a Go toolchain.
binary_revision() { grep -a -o 'vcs.revision=[0-9a-f]\{40\}' "$1" 2>/dev/null | head -1 | cut -d= -f2; }
binary_modified() { grep -a -o 'vcs.modified=[a-z]*' "$1" 2>/dev/null | head -1 | cut -d= -f2; }

# ---------------------------------------------------------------- JSON (python3; jq is not guaranteed)
jget() { # jget <file> <python-expression on d>
  python3 -c 'import json,sys
d=json.load(open(sys.argv[1], encoding="utf-8"))
v=eval(sys.argv[2])
print("" if v is None else (v if isinstance(v,str) else json.dumps(v)))' "$1" "$2" 2>/dev/null
}

# ---------------------------------------------------------------- Caddy
# render_caddyfile <template> <site-addresses> <internal|acme> [acme-email] — the concrete Central Caddyfile.
render_caddyfile() {
  local tpl="$1" sites="$2" mode="$3" email="${4:-}" tls_line global_extra=""
  case "$mode" in
    internal) tls_line="	tls $CADDY_TLS_CRT $CADDY_TLS_KEY" ;;
    acme)     tls_line="	# TLS: automatic public certificate (ACME HTTP-01 / TLS-ALPN-01)"
              [ -n "$email" ] && global_extra="	email $email" ;;
    *) die "unknown TLS mode: $mode" ;;
  esac
  python3 - "$tpl" "$sites" "$tls_line" "$global_extra" <<'PY'
import sys
tpl, sites, tls, extra = sys.argv[1:5]
s = open(tpl, encoding="utf-8").read()
for token in ("__CENTRAL_SITE_ADDRESSES__", "__CENTRAL_TLS__", "__CENTRAL_GLOBAL_EXTRA__"):
    if token not in s:
        sys.exit("template %s has no %s" % (tpl, token))
s = s.replace("__CENTRAL_SITE_ADDRESSES__", sites)
s = s.replace("\t__CENTRAL_TLS__", tls).replace("__CENTRAL_TLS__", tls.strip())
s = s.replace("\t__CENTRAL_GLOBAL_EXTRA__\n", (extra + "\n") if extra else "")
sys.stdout.write(s)
PY
}
