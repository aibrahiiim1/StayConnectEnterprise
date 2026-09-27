#!/usr/bin/env bash
# DEPLOY A ONEGATE CENTRAL RELEASE ONTO AN INSTALLED CENTRAL — and roll it back if it does not come up.
#
# The release is one built by central-build.sh; the host is one installed by central-install.sh (it reads the
# settings that recorded, /etc/stayconnect/central-install.env). Appliances are unaffected by a Central deploy:
# they retry, and guest service never waits on Central.
#
# Usage (as root on Central, from the EXTRACTED NEW release):
#   bash onegate-central-<sha>/deploy/scripts/central-deploy.sh [deploy]    deploy this release (default)
#   bash …/central-deploy.sh rollback     previous ctrlapi binary + previous console + previous tooling
#   bash …/central-deploy.sh smoke        the end-to-end checks, through Caddy by the configured name
#   bash …/central-deploy.sh status       what is deployed, read back from the installed files
# Options: --release <dir|tar.gz> (default: the release this script is part of) · --dry-run
#
# DEPLOY, IN ORDER (each step verified before the next):
#   1. verify the release (SHA256SUMS, ctrlapi embeds its commit, console manifest = its BUILD_ID)
#   2. database backup: pg_dump -Fc to /root/backups (retained by stayconnect-backup-cleanup), checked readable
#   3. backup the running ctrlapi binary and ctrlapi.env
#   4. stop ctrlapi; migrate (central-migrate.sh up + verify) with the NEW release's migrations
#   5. install the new binary + units; start --no-block; poll /readyz; the running binary must embed the commit
#   6. Caddy: re-render the Caddyfile from the release template; if it changed, validate + restart
#   7. console: new release dir, atomic symlink switch (previous kept), restart, wait for its BUILD_ID
#   8. smoke through the real site name (--resolve), then write DEPLOYED.json
# Any failure in 5-8 rolls the binary and the console back to what was running and exits non-zero.
# The DATABASE is not rolled back automatically: migrations are forward-only, and the pre-deploy dump path is
# printed for a deliberate restore (docs/DEPLOYMENT_CLOUD.md §7).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROG=central-deploy
# shellcheck source=central-lib.sh
. "$HERE/central-lib.sh"

CMD=deploy RELEASE_ARG=""
while [ $# -gt 0 ]; do
  case "$1" in
    deploy|rollback|smoke|status) CMD="$1"; shift ;;
    --release) RELEASE_ARG="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
export DRY_RUN
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"

setting() { envfile_get "$CENTRAL_INSTALL_ENV" "$1" || true; }

# ================================================================= smoke
# Through Caddy BY NAME. Caddy answers a Host it has no site for (127.0.0.1, an unlisted IP) with an empty 200,
# so a probe that does not send the configured name proves nothing.
smoke() {
  local primary tls cacert base bid fail=0 code body
  primary="$(setting CENTRAL_PRIMARY_NAME)"; tls="$(setting CENTRAL_TLS_MODE)"
  [ -n "$primary" ] || die "no CENTRAL_PRIMARY_NAME in $CENTRAL_INSTALL_ENV — is this host installed with central-install.sh?"
  base="https://$primary"
  local -a C=(curl -sS --max-time 8 --resolve "$primary:443:127.0.0.1")
  if [ "$tls" = internal ]; then cacert="$CENTRAL_TLS_DIR/ca.crt"; C+=(--cacert "$cacert"); fi
  _pass() { printf '  PASS  %s\n' "$*"; }
  _fail() { printf '  FAIL  %s\n' "$*"; fail=1; }

  say "smoke test (via Caddy as $primary)"
  body="$(curl -fsS --max-time 5 http://127.0.0.1:8080/readyz 2>/dev/null || true)"
  case "$body" in *'"ready"'*) _pass "ctrlapi loopback /readyz: $body" ;; *) _fail "ctrlapi loopback /readyz: ${body:-no answer}" ;; esac

  body="$("${C[@]}" "$base/readyz" 2>&1 || true)"
  if [ "$tls" = acme ] && ! printf '%s' "$body" | grep -q '"ready"'; then
    warn "public certificate not verifiable yet (ACME needs DNS pointing here); retrying without verification"
    C+=(-k); body="$("${C[@]}" "$base/readyz" 2>&1 || true)"
  fi
  case "$body" in *'"ready"'*) _pass "https://$primary/readyz via Caddy (TLS verified: $([ "$tls" = internal ] && echo "internal CA" || echo "see above"))" ;;
    *) _fail "https://$primary/readyz via Caddy: $body" ;; esac

  code="$("${C[@]}" -o /dev/null -w '%{http_code}' "$base/login" || true)"
  [ "$code" = 200 ] && _pass "console /login 200" || _fail "console /login answered $code"

  if [ -f "$CONSOLE_CURRENT/.next/BUILD_ID" ]; then
    bid="$(cat "$CONSOLE_CURRENT/.next/BUILD_ID")"
    code="$("${C[@]}" -o /dev/null -w '%{http_code}' "$base/_next/static/$bid/_buildManifest.js" || true)"
    [ "$code" = 200 ] && _pass "console serves the deployed build ($bid)" || _fail "console does not serve build $bid (got $code)"
  fi

  body="$("${C[@]}" "$base/metrics" 2>/dev/null || true)"
  if printf '%s' "$body" | grep -q '^# HELP'; then _fail "/metrics is reachable through Caddy — it must be loopback-only"
  else _pass "/metrics not exposed through Caddy"; fi
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:8080/metrics || true)"
  [ "$code" = 200 ] && _pass "/metrics answers on loopback for a local scraper" || warn "/metrics on loopback answered $code"

  local mport; mport="$(envfile_get "$CTRLAPI_ENV_PATH" CTRLAPI_MTLS_ADDR 2>/dev/null || echo :9443)"; mport="${mport##*:}"
  if ss -ltnH 2>/dev/null | awk '{print $4}' | grep -qE "[:.]$mport\$"; then
    _pass "appliance mTLS listener on :$mport"
    if [ -s "$CENTRAL_CA_BUNDLE" ]; then
      if openssl s_client -connect "127.0.0.1:$mport" -servername "$primary" -verify_hostname "$primary" \
           -CAfile "$CENTRAL_CA_BUNDLE" </dev/null 2>/dev/null | grep -q 'Verify return code: 0'; then
        _pass ":$mport certificate verifies for $primary against the appliance CA bundle"
      else
        _fail ":$mport certificate does not verify for $primary against $CENTRAL_CA_BUNDLE"
      fi
    fi
  else
    _fail "nothing listens on :$mport — appliances cannot complete mTLS (is the appliance CA present?)"
  fi
  return "$fail"
}

status() {
  say "deployed (read back from the installed files):"
  [ -f "$CENTRAL_DEPLOYED_JSON" ] && sed 's/^/    /' "$CENTRAL_DEPLOYED_JSON"
  say "  ctrlapi binary revision: $(binary_revision "$CTRLAPI_BIN_PATH")  modified=$(binary_modified "$CTRLAPI_BIN_PATH")  sha256=$(sha256_of "$CTRLAPI_BIN_PATH")"
  say "  console current:  $(readlink -f "$CONSOLE_CURRENT")  BUILD_ID $(cat "$CONSOLE_CURRENT/.next/BUILD_ID" 2>/dev/null)"
  say "  console previous: $(readlink -f "$CONSOLE_PREVIOUS")  BUILD_ID $(cat "$CONSOLE_PREVIOUS/.next/BUILD_ID" 2>/dev/null)"
  say "  tooling:          $(readlink -f "$CENTRAL_TOOLING")"
  say "  readyz:           $(curl -fsS --max-time 3 http://127.0.0.1:8080/readyz 2>/dev/null || echo unreachable)"
}

write_deployed() { # write_deployed <release-commit> <how>
  [ "$DRY_RUN" = 1 ] && return 0
  [ -f "$CENTRAL_DEPLOYED_JSON" ] && cp -f "$CENTRAL_DEPLOYED_JSON" "$CENTRAL_DEPLOYED_JSON.previous"
  python3 - "$CENTRAL_DEPLOYED_JSON" "$1" "$(sha256_of "$CTRLAPI_BIN_PATH")" "$(binary_revision "$CTRLAPI_BIN_PATH")" \
    "$(cat "$CONSOLE_CURRENT/.next/BUILD_ID")" "$(jget "$CONSOLE_CURRENT/$CONSOLE_MANIFEST" 'd["source_commit"]')" "$2" <<'PY'
import json, sys, datetime
out, commit, csum, crev, bid, ccommit, how = sys.argv[1:8]
json.dump({"release_commit": commit, "ctrlapi_sha256": csum, "ctrlapi_vcs_revision": crev,
           "console_build_id": bid, "console_source_commit": ccommit, "installed_by": "central-deploy.sh " + how,
           "at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")},
          open(out, "w"), indent=2)
PY
}

switch_link() { run ln -sfn "$1" "$2.tmp"; run mv -Tf "$2.tmp" "$2"; }

console_wait() { # console_wait <build-id>
  local i
  for i in $(seq 1 45); do
    if curl -fsS -o /dev/null --max-time 2 "http://127.0.0.1:3000/_next/static/$1/_buildManifest.js" 2>/dev/null; then
      say "console serving BUILD_ID $1 after ${i}s"; return 0
    fi
    sleep 1
  done
  return 1
}

rollback() {
  need_root
  step "rollback"
  local bak; bak="$(ls -1t "$CTRLAPI_BIN_PATH".bak-* 2>/dev/null | head -1 || true)"
  [ -n "$bak" ] || die "no ctrlapi backup (${CTRLAPI_BIN_PATH}.bak-*) to roll back to"
  say "ctrlapi -> $bak (revision $(binary_revision "$bak"))"
  run systemctl stop stayconnect-ctrlapi || true
  run cp -a "$CTRLAPI_BIN_PATH" "$CTRLAPI_BIN_PATH.failed-$STAMP"
  run install -m 0755 "$bak" "$CTRLAPI_BIN_PATH"
  unit_restart_noblock stayconnect-ctrlapi
  [ "$DRY_RUN" = 1 ] || ctrlapi_ready 90 || die "the previous ctrlapi did not become ready either — journalctl -u stayconnect-ctrlapi"
  local prev; prev="$(readlink -f "$CONSOLE_PREVIOUS" 2>/dev/null || true)"
  if [ -n "$prev" ] && [ -d "$prev" ] && [ "$prev" != "$(readlink -f "$CONSOLE_CURRENT")" ]; then
    local cur; cur="$(readlink -f "$CONSOLE_CURRENT")"
    switch_link "$prev" "$CONSOLE_CURRENT"; switch_link "$cur" "$CONSOLE_PREVIOUS"
    unit_restart_noblock stayconnect-cloud-admin
    [ "$DRY_RUN" = 1 ] || console_wait "$(cat "$prev/.next/BUILD_ID")" || die "the previous console did not come up"
  fi
  if [ -L "$CENTRAL_TOOLING.previous" ]; then
    local tc; tc="$(readlink -f "$CENTRAL_TOOLING")"
    switch_link "$(readlink -f "$CENTRAL_TOOLING.previous")" "$CENTRAL_TOOLING"; switch_link "$tc" "$CENTRAL_TOOLING.previous"
  fi
  write_deployed "$(binary_revision "$CTRLAPI_BIN_PATH")" rollback
  [ "$DRY_RUN" = 1 ] || smoke || die "rolled back, but the smoke test still fails"
  say "rolled back. The database was NOT touched; the newest pre-deploy dump is:"
  say "  $(ls -1t "$CENTRAL_DB_BACKUPS"/central-*.dump 2>/dev/null | head -1)"
}

case "$CMD" in
  smoke)  smoke && say "SMOKE PASS" || die "SMOKE FAIL"; exit 0 ;;
  status) status; exit 0 ;;
  rollback) rollback; exit 0 ;;
esac

# ================================================================= deploy
need_root
[ -f "$CENTRAL_INSTALL_ENV" ] || die "$CENTRAL_INSTALL_ENV not found: this host was not installed by central-install.sh"
WORK="$(mktemp -d /tmp/central-deploy.XXXXXX)"; trap 'rm -rf "$WORK"' EXIT

step "1/8 release"
if [ -n "$RELEASE_ARG" ] && [ -f "$RELEASE_ARG" ]; then
  tar -xzf "$RELEASE_ARG" -C "$WORK"; REL="$(find "$WORK" -mindepth 1 -maxdepth 1 -type d -name 'onegate-central-*' | head -1)"
elif [ -n "$RELEASE_ARG" ]; then REL="$(cd "$RELEASE_ARG" && pwd)"
else REL="$(cd "$HERE/../.." && pwd)"; fi
[ -f "$REL/RELEASE.json" ] || die "no release at $REL (no RELEASE.json)"
( cd "$REL" && sha256sum -c --quiet SHA256SUMS ) || die "release does not match its SHA256SUMS"
COMMIT="$(jget "$REL/RELEASE.json" 'd["source_commit"]')"
[ "$(jget "$REL/RELEASE.json" 'd["source_state"]')" = clean ] || die "release built from a dirty tree"
[ "$(binary_revision "$REL/bin/ctrlapi")" = "$COMMIT" ] || die "bin/ctrlapi does not embed $COMMIT"
NEW_BID="$(cat "$REL/cloud-admin/.next/BUILD_ID")"
[ "$(jget "$REL/cloud-admin/$CONSOLE_MANIFEST" 'd["build_id"]')" = "$NEW_BID" ] || die "console manifest does not describe the bundle"
NAME="onegate-central-${COMMIT:0:12}"
OLD_REV="$(binary_revision "$CTRLAPI_BIN_PATH" || true)"
say "deploying $NAME over ctrlapi ${OLD_REV:0:12} / console $(cat "$CONSOLE_CURRENT/.next/BUILD_ID" 2>/dev/null)"
T="$REL/deploy/scripts"

step "2/8 database backup"
run install -d -m 0700 "$CENTRAL_DB_BACKUPS"
DUMP="$CENTRAL_DB_BACKUPS/central-$STAMP-pre-${COMMIT:0:12}.dump"
if [ "$DRY_RUN" = 1 ]; then say "DRY-RUN would pg_dump -Fc to $DUMP"
else
  docker exec "$CENTRAL_PG_CONTAINER" pg_dump -U "$CENTRAL_DB_USER" -d "$CENTRAL_DB" -Fc > "$DUMP.tmp"
  docker exec -i "$CENTRAL_PG_CONTAINER" pg_restore -l < "$DUMP.tmp" >/dev/null || die "the backup just taken is not a readable archive"
  chmod 0600 "$DUMP.tmp"; mv -f "$DUMP.tmp" "$DUMP"
  say "backup $DUMP ($(du -h "$DUMP" | cut -f1))"
fi

step "3/8 binary + configuration backup"
BIN_BAK="$CTRLAPI_BIN_PATH.bak-$STAMP"
run cp -a "$CTRLAPI_BIN_PATH" "$BIN_BAK"
run cp -a "$CTRLAPI_ENV_PATH" "$CTRLAPI_ENV_PATH.bak-$STAMP"
say "kept $BIN_BAK and $CTRLAPI_ENV_PATH.bak-$STAMP"

step "4/8 stop ctrlapi, migrate"
run systemctl stop stayconnect-ctrlapi
MIG=(bash "$T/central-migrate.sh" --pg-exec "docker exec -i $CENTRAL_PG_CONTAINER" --db "$CENTRAL_DB")
restart_old() {
  warn "restarting the PREVIOUS ctrlapi"
  run install -m 0755 "$BIN_BAK" "$CTRLAPI_BIN_PATH"
  unit_restart_noblock stayconnect-ctrlapi
  [ "$DRY_RUN" = 1 ] || ctrlapi_ready 90 || warn "the previous ctrlapi is not ready either — journalctl -u stayconnect-ctrlapi"
}
if [ "$DRY_RUN" = 1 ]; then say "DRY-RUN would run ${MIG[*]} up && verify"
else
  if ! "${MIG[@]}" up || ! "${MIG[@]}" verify; then
    restart_old
    die "migration failed. Each migration is its own transaction, so the failing one left nothing behind, but
    earlier ones in this run are applied. The previous ctrlapi is running again. To return the database to its
    pre-deploy state deliberately, see docs/DEPLOYMENT_CLOUD.md §7 with $DUMP."
  fi
fi

step "5/8 ctrlapi"
STORED="$SC_OPT/releases/central/$NAME"
[ -d "$STORED" ] || { run install -d -m 0755 "$STORED"; run cp -a "$REL/RELEASE.json" "$REL/SHA256SUMS" "$REL/deploy" "$REL/control-plane" "$STORED/"; }
if [ "$(readlink -f "$CENTRAL_TOOLING")" != "$STORED" ]; then
  [ -L "$CENTRAL_TOOLING" ] && switch_link "$(readlink -f "$CENTRAL_TOOLING")" "$CENTRAL_TOOLING.previous"
  switch_link "$STORED" "$CENTRAL_TOOLING"
fi
units_changed=0
for pair in "systemd/stayconnect-ctrlapi.service:stayconnect-ctrlapi.service" \
            "systemd/stayconnect-cloud-admin.service:stayconnect-cloud-admin.service" \
            "caddy/stayconnect-caddy.central.service:stayconnect-caddy.service" \
            "systemd/stayconnect-backup-cleanup.service:stayconnect-backup-cleanup.service" \
            "systemd/stayconnect-backup-cleanup.timer:stayconnect-backup-cleanup.timer"; do
  src="$REL/deploy/${pair%%:*}"; dest="/etc/systemd/system/${pair##*:}"
  if ! cmp -s "$src" "$dest"; then
    [ -f "$dest" ] && run cp -a "$dest" "$dest.bak-$STAMP"
    run install -m 0644 "$src" "$dest"; units_changed=1; say "unit ${pair##*:} updated"
  fi
done
run install -m 0755 "$REL/deploy/scripts/stayconnect-backup-cleanup.sh" "$SC_OPT/bin/stayconnect-backup-cleanup"
[ "$units_changed" = 0 ] || run systemctl daemon-reload
if ! cmp -s "$CENTRAL_COMPOSE_FILE" "$REL/deploy/compose/central-infra.yml"; then
  warn "deploy/compose/central-infra.yml changed in this release — NOT applied automatically (it would recreate the
    database container). Review the diff and apply it in a maintenance window:
      diff $CENTRAL_COMPOSE_FILE $REL/deploy/compose/central-infra.yml"
fi
run install -m 0755 "$REL/bin/ctrlapi" "$CTRLAPI_BIN_PATH.new"
run mv -f "$CTRLAPI_BIN_PATH.new" "$CTRLAPI_BIN_PATH"
unit_restart_noblock stayconnect-ctrlapi
if [ "$DRY_RUN" = 0 ]; then
  if ! ctrlapi_ready 90; then
    journalctl -u stayconnect-ctrlapi -n 40 --no-pager >&2 || true
    restart_old; die "the new ctrlapi did not become ready — rolled back to ${OLD_REV:0:12}"
  fi
  [ "$(binary_revision "$CTRLAPI_BIN_PATH")" = "$COMMIT" ] || { restart_old; die "installed binary does not embed $COMMIT"; }
fi

step "6/8 Caddy"
render_caddyfile "$REL/deploy/caddy/Caddyfile.central" "$(setting CENTRAL_SITE_ADDRESSES | sed 's/,/, /g')" \
  "$(setting CENTRAL_TLS_MODE)" "$(setting CENTRAL_ACME_EMAIL)" > "$WORK/Caddyfile"
if cmp -s "$WORK/Caddyfile" "$CADDYFILE_PATH" && [ "$units_changed" = 0 ]; then
  say "Caddyfile and unit unchanged — Caddy not restarted"
else
  run cp -a "$CADDYFILE_PATH" "$CADDYFILE_PATH.bak-$STAMP"
  if caddy validate --config "$WORK/Caddyfile" --adapter caddyfile >/dev/null 2>&1; then
    write_file "$CADDYFILE_PATH" 0644 < "$WORK/Caddyfile"
    unit_restart_noblock stayconnect-caddy
    say "Caddy restarted with the re-rendered configuration"
  else
    caddy validate --config "$WORK/Caddyfile" --adapter caddyfile || true
    warn "the re-rendered Caddyfile does not validate — the running configuration was left in place"
  fi
fi

step "7/8 console"
CUR_REL="$(readlink -f "$CONSOLE_CURRENT")"
if [ "$(cat "$CONSOLE_CURRENT/.next/BUILD_ID" 2>/dev/null)" = "$NEW_BID" ]; then
  say "console BUILD_ID $NEW_BID already current"; CONSOLE_SWITCHED=0
else
  CREL="$CONSOLE_RELEASES/$STAMP"
  run cp -a "$REL/cloud-admin" "$CREL"
  run chown -R root:root "$CREL"; run chmod -R a+rX,go-w "$CREL"
  switch_link "$CUR_REL" "$CONSOLE_PREVIOUS"
  switch_link "$CREL" "$CONSOLE_CURRENT"
  CONSOLE_SWITCHED=1
  unit_restart_noblock stayconnect-cloud-admin
  if [ "$DRY_RUN" = 0 ] && ! console_wait "$NEW_BID"; then
    journalctl -u stayconnect-cloud-admin -n 40 --no-pager >&2 || true
    switch_link "$CUR_REL" "$CONSOLE_CURRENT"; unit_restart_noblock stayconnect-cloud-admin
    restart_old
    die "the new console did not serve BUILD_ID $NEW_BID — console and ctrlapi rolled back"
  fi
fi

step "8/8 smoke"
if [ "$DRY_RUN" = 1 ]; then say "DRY-RUN would run the smoke test"
elif ! smoke; then
  warn "smoke test FAILED — rolling back ctrlapi and the console"
  [ "$CONSOLE_SWITCHED" = 1 ] && { switch_link "$CUR_REL" "$CONSOLE_CURRENT"; unit_restart_noblock stayconnect-cloud-admin; }
  restart_old
  die "deployment rolled back (database stays migrated; pre-deploy dump: $DUMP)"
fi
write_deployed "$COMMIT" deploy
[ -x "$SC_OPT/bin/stayconnect-backup-cleanup" ] && { run "$SC_OPT/bin/stayconnect-backup-cleanup" --apply >/dev/null 2>&1 || warn "backup retention cleanup reported a problem"; }
# Old stored tooling releases: keep the newest 5 plus whatever current/previous point at.
keep="$(readlink -f "$CENTRAL_TOOLING"),$(readlink -f "$CENTRAL_TOOLING.previous" 2>/dev/null || true)"
n=0
for d in $(ls -1dt "$SC_OPT"/releases/central/onegate-central-* 2>/dev/null); do
  case ",$keep," in *",$d,"*) continue ;; esac
  n=$((n+1)); [ "$n" -le 5 ] || run rm -rf "$d"
done
say "DEPLOYED $NAME — ctrlapi $(binary_revision "$CTRLAPI_BIN_PATH" | cut -c1-12), console $NEW_BID; pre-deploy dump $DUMP"
