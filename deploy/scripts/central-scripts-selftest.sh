#!/usr/bin/env bash
# Self-test for the Central upgrade and backup safety checks (GDPR remediation review, 2026-10-20).
#
# Runs the REAL scripts and helpers against a sandbox and asserts:
#   central-backup.sh
#     - with no recipients file the dump is written plain, and the run says so;
#     - a recipients file holding only an SSH key (which age accepts) produces an encrypted .age dump, no plain one;
#     - a recipients file holding no usable key FAILS the backup and leaves no dump at all, never a plain one;
#   central-lib.sh (used by central-deploy.sh and central-install.sh)
#     - ensure_mfa_key creates a missing two-step sign-in key with the given binary, never replaces an existing
#       one, and refuses an empty one;
#     - check_mfa_key runs `<binary> check-mfa-key --key <path>` with the database from ctrlapi.env, and fails
#       when the binary refuses the key;
#   central-deploy.sh (by inspection)
#     - the key is ensured and checked, and the Caddyfile validated, BEFORE the backup and before anything is
#       switched; an invalid Caddyfile aborts rather than carrying on with the old configuration;
#     - the smoke test requires "signin":"available"; the rollback names encrypted pre-deploy dumps too.
# Needs bash and coreutils; docker, age and the ctrlapi binary are stubbed. Touches nothing outside its temp dir.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }

mkdir -p "$T/bin" "$T/opt" "$T/etc" "$T/backups"
# ---- stubs: root, docker (pg_dump / pg_restore -l), age, logger, install -d
cat > "$T/bin/id" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = -u ] && { echo 0; exit 0; }
exec /usr/bin/id "$@"
EOF
cat > "$T/bin/docker" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  inspect*) exit 0 ;;
  *pg_dump*) printf 'PGDMP-fake-archive' ;;
  *pg_restore*) cat >/dev/null ;;
  *) exit 1 ;;
esac
EOF
cat > "$T/bin/age" <<'EOF'
#!/usr/bin/env bash
out="" in=""
while [ $# -gt 0 ]; do case "$1" in -R) shift 2 ;; -o) out="$2"; shift 2 ;; *) in="$1"; shift ;; esac; done
{ printf 'age-encryption.org/v1\n'; cat "$in"; } > "$out"
EOF
printf '#!/bin/sh\nexit 0\n' > "$T/bin/logger"
# `install -d -m 0700 <dir>` only (Windows' Git Bash cannot chmod a temp dir; the mode is not under test here)
cat > "$T/bin/install" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = -d ] && { mkdir -p "${@: -1}"; exit 0; }
exec /usr/bin/install "$@"
EOF
chmod +x "$T/bin/"*

backup() { # backup <recipients-file-or-empty> -> exit status; output in $T/out
  rm -f "$T/backups"/central-*
  env PATH="$T/bin:$PATH" SC_OPT="$T/opt" SC_ETC="$T/etc" CENTRAL_DB_BACKUPS="$T/backups" CENTRAL_PG_CONTAINER=pg \
    CENTRAL_BACKUP_RECIPIENTS="$1" CENTRAL_BACKUP_STATUS="$T/opt/status.json" \
    bash "$HERE/central-backup.sh" --reason selftest > "$T/out" 2>&1
}
dumps() { ls -1 "$T/backups" 2>/dev/null | grep -c '^central-.*\.dump$' || true; }
aged()  { ls -1 "$T/backups" 2>/dev/null | grep -c '^central-.*\.dump\.age$' || true; }

backup "$T/etc/none.txt"; rc=$?
if [ "$rc" = 0 ] && [ "$(dumps)" = 1 ] && [ "$(aged)" = 0 ] && grep -q 'NOT encrypted' "$T/out"; then
  ok "no recipients file: a plain dump, and a warning"
else bad "no recipients file: rc=$rc dumps=$(dumps) aged=$(aged)"; cat "$T/out"; fi

printf '# the restorer\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl restorer@laptop\n' > "$T/etc/ssh.txt"
backup "$T/etc/ssh.txt"; rc=$?
if [ "$rc" = 0 ] && [ "$(dumps)" = 0 ] && [ "$(aged)" = 1 ]; then
  ok "SSH-key-only recipients file: encrypted, no plain copy"
else bad "SSH-key-only recipients file: rc=$rc dumps=$(dumps) aged=$(aged)"; cat "$T/out"; fi

printf 'ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC7 restorer\n' > "$T/etc/rsa.txt"
backup "$T/etc/rsa.txt"; rc=$?
[ "$rc" = 0 ] && [ "$(aged)" = 1 ] && [ "$(dumps)" = 0 ] && ok "ssh-rsa recipient: encrypted" || { bad "ssh-rsa recipient: rc=$rc"; cat "$T/out"; }

printf 'age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p\n' > "$T/etc/age.txt"
backup "$T/etc/age.txt"; rc=$?
[ "$rc" = 0 ] && [ "$(aged)" = 1 ] && [ "$(dumps)" = 0 ] && ok "age recipient: encrypted" || { bad "age recipient: rc=$rc"; cat "$T/out"; }

printf '# keys go here\n\nnot-a-key AAAA\n' > "$T/etc/empty.txt"
backup "$T/etc/empty.txt"; rc=$?
if [ "$rc" != 0 ] && [ "$(dumps)" = 0 ] && [ "$(aged)" = 0 ] && grep -q 'no key age can encrypt to' "$T/out"; then
  ok "recipients file with no usable key: the backup fails, nothing written in the clear"
else bad "recipients file with no usable key: rc=$rc dumps=$(dumps) aged=$(aged)"; cat "$T/out"; fi

# ---- the two-step sign-in key helpers
cat > "$T/bin/fake-ctrlapi" <<'EOF'
#!/usr/bin/env bash
echo "$*" >> "$FAKE_LOG"
case "$1" in
  gen-mfa-key) [ -e "$3" ] && exit 1; head -c 32 /dev/urandom > "$3" ;;
  check-mfa-key) echo "db=$CTRLAPI_DB_URL" >> "$FAKE_LOG"; [ "$(head -c 5 "$3")" != WRONG ] ;;
esac
EOF
chmod +x "$T/bin/fake-ctrlapi"
export FAKE_LOG="$T/fake.log"
(
  PROG=selftest DRY_RUN=0 SC_ETC="$T/etc" SC_OPT="$T/opt"
  # shellcheck source=central-lib.sh
  . "$HERE/central-lib.sh"
  chown() { :; }   # the sandbox is not root; the real call is exercised on the host
  printf 'CTRLAPI_DB_URL=postgres://u:p@127.0.0.1:5432/stayconnect\n' > "$CTRLAPI_ENV_PATH"
  key="$(mfa_key_path)"
  [ "$key" = "$T/etc/operator-mfa.key" ] && ok "key path defaults to SC_ETC/operator-mfa.key" || bad "key path $key"
  printf 'CTRLAPI_OPERATOR_MFA_KEY=/somewhere/else.key\n' >> "$CTRLAPI_ENV_PATH"
  [ "$(mfa_key_path)" = /somewhere/else.key ] && ok "key path follows CTRLAPI_OPERATOR_MFA_KEY" || bad "key path ignores ctrlapi.env"

  : > "$FAKE_LOG"
  if ensure_mfa_key "$T/bin/fake-ctrlapi" "$key" >/dev/null && [ "$(wc -c < "$key" | tr -d ' ')" = 32 ] && grep -q '^gen-mfa-key' "$FAKE_LOG"; then
    ok "a missing key is created with the given binary"
  else bad "a missing key was not created"; fi
  sum="$(sha256sum "$key")"; : > "$FAKE_LOG"
  if ensure_mfa_key "$T/bin/fake-ctrlapi" "$key" >/dev/null && [ "$(sha256sum "$key")" = "$sum" ] && ! grep -q gen-mfa-key "$FAKE_LOG"; then
    ok "an existing key is kept, never replaced"
  else bad "an existing key was touched"; fi
  : > "$T/etc/empty.key"; : > "$FAKE_LOG"
  if ! ensure_mfa_key "$T/bin/fake-ctrlapi" "$T/etc/empty.key" 2>/dev/null && [ ! -s "$T/etc/empty.key" ] && ! grep -q gen-mfa-key "$FAKE_LOG"; then
    ok "an empty key file is refused, not replaced"
  else bad "an empty key file was accepted or replaced"; fi

  : > "$FAKE_LOG"
  if check_mfa_key "$T/bin/fake-ctrlapi" "$key" && grep -q "^check-mfa-key --key $key\$" "$FAKE_LOG" \
     && grep -q '^db=postgres://u:p@127.0.0.1:5432/stayconnect$' "$FAKE_LOG"; then
    ok "check_mfa_key runs check-mfa-key with the database from ctrlapi.env"
  else bad "check_mfa_key did not run the check as expected: $(cat "$FAKE_LOG")"; fi
  printf 'WRONG-key-material-32-bytes-long' > "$T/etc/wrong.key"
  if ! check_mfa_key "$T/bin/fake-ctrlapi" "$T/etc/wrong.key"; then ok "a refused key fails check_mfa_key"
  else bad "a refused key passed check_mfa_key"; fi
  exit "$fail"
) || fail=1

# ---- the runtime database role (0063, audit P3 M1): password file, role set-up, DSN switch, owner DSN
mkdir -p "$T/bin2"
cat > "$T/bin2/docker" <<'EOF'
#!/usr/bin/env bash
echo "$*" >> "$T_DOCKER_ARGV"
case "$*" in
  *"exec -e PGPASSWORD"*) cat > "$T_DOCKER_LOGINSQL"
    case "${RT_VERIFY:-good}" in
      good)  printf 'super=false\nread=3\nddl=false\n' ;;
      super) printf 'super=true\nread=3\nddl=false\n' ;;
      denied) echo 'FATAL:  password authentication failed'; exit 2 ;;
    esac ;;
  *"exec -i"*psql*) cat >> "$T_DOCKER_STDIN" ;;
  *psql*) : ;;
esac
EOF
chmod +x "$T/bin2/docker"
export T_DOCKER_ARGV="$T/docker.argv" T_DOCKER_STDIN="$T/docker.stdin" T_DOCKER_LOGINSQL="$T/docker.login"
(
  PATH="$T/bin2:$PATH"
  PROG=selftest DRY_RUN=0 SC_ETC="$T/etc2" SC_OPT="$T/opt2" WORK="$T"
  # shellcheck source=central-lib.sh
  . "$HERE/central-lib.sh"
  chown() { :; }
  mkdir -p "$CENTRAL_SECRETS_DIR" "$SC_ETC"
  printf 'olddbpassword' > "$CENTRAL_SECRETS_DIR/db_password"
  printf 'CTRLAPI_ENV=production\nCTRLAPI_DB_URL=postgres://stayconnect:olddbpassword@127.0.0.1:5432/stayconnect?sslmode=disable\nCTRLAPI_REDIS_URL=redis://:r@127.0.0.1:6379/0\n' > "$CTRLAPI_ENV_PATH"
  chmod 0600 "$CTRLAPI_ENV_PATH" 2>/dev/null || true

  central_runtime_role_apply >/dev/null 2>"$T/rt.err"; rc=$?
  pw="$(cat "$CENTRAL_RUNTIME_PW_FILE" 2>/dev/null)"
  if [ "$rc" = 0 ] && printf '%s' "$pw" | grep -Eq '^[0-9a-f]{64}$'; then ok "runtime role: a 64-hex password file is generated"
  else bad "runtime role: rc=$rc password file '$(printf %s "$pw" | cut -c1-4)…'"; cat "$T/rt.err"; fi
  if [ "$(wc -c < "$CENTRAL_RUNTIME_PW_FILE" | tr -d ' ')" = 64 ] && grep -q "ALTER ROLE ctrlapi_rt PASSWORD '$pw'" "$T_DOCKER_STDIN"; then
    ok "runtime role: the password reaches ALTER ROLE on stdin"
  else bad "runtime role: ALTER ROLE did not receive the password on stdin"; fi
  if ! grep -qF "$pw" "$T_DOCKER_ARGV"; then ok "runtime role: the password is on no docker command line"
  else bad "runtime role: the password appears in docker's arguments"; fi
  if grep -q 'central_apply_runtime_grants' "$T_DOCKER_ARGV"; then ok "runtime role: grants are re-asserted by central_apply_runtime_grants()"
  else bad "runtime role: central_apply_runtime_grants() was not called"; fi
  sum="$(sha256sum "$CENTRAL_RUNTIME_PW_FILE")"
  central_runtime_role_apply >/dev/null 2>&1
  [ "$(sha256sum "$CENTRAL_RUNTIME_PW_FILE")" = "$sum" ] && ok "runtime role: an existing password file is kept (idempotent upgrade)" \
    || bad "runtime role: a second run replaced the password file"
  if ! RT_VERIFY=super central_runtime_role_apply >/dev/null 2>"$T/rt.err" && grep -q 'login check FAILED' "$T/rt.err"; then
    ok "runtime role: a superuser login fails the verification"
  else bad "runtime role: a superuser login passed the verification"; fi
  if ! RT_VERIFY=denied central_runtime_role_apply >/dev/null 2>&1; then ok "runtime role: a refused login fails the verification"
  else bad "runtime role: a refused login passed the verification"; fi

  STAMP=20260101T000000Z central_runtime_dsn_switch >/dev/null
  now="$(envfile_get "$CTRLAPI_ENV_PATH" CTRLAPI_DB_URL)"
  if [ "$now" = "postgres://ctrlapi_rt:$pw@127.0.0.1:5432/stayconnect?sslmode=disable" ]; then ok "dsn switch: only the credentials change (host, port, database, parameters kept)"
  else bad "dsn switch: unexpected URL shape"; fi
  if grep -q '^CTRLAPI_ENV=production$' "$CTRLAPI_ENV_PATH" && grep -q '^CTRLAPI_REDIS_URL=redis://:r@127.0.0.1:6379/0$' "$CTRLAPI_ENV_PATH"; then ok "dsn switch: the other settings are untouched"
  else bad "dsn switch: other settings changed"; fi
  bak="$CTRLAPI_ENV_PATH.bak-dbrole-20260101T000000Z"
  if [ -f "$bak" ] && grep -q 'stayconnect:olddbpassword@' "$bak"; then ok "dsn switch: the previous env file is kept as a timestamped backup"
  else bad "dsn switch: no backup of the previous env file"; fi
  STAMP=20260102T000000Z central_runtime_dsn_switch >/dev/null
  if [ "$(ls "$CTRLAPI_ENV_PATH".bak-dbrole-* | wc -l | tr -d ' ')" = 1 ]; then ok "dsn switch: idempotent, no second backup once switched"
  else bad "dsn switch: a second run made another backup"; fi
  # A regenerated or rotated secret is set on the role first, so a URL that still carries the old password must be rewritten.
  newpw="$(printf 'b%.0s' $(seq 1 64))"
  printf '%s' "$newpw" > "$CENTRAL_RUNTIME_PW_FILE"
  STAMP=20260103T000000Z central_runtime_dsn_switch >/dev/null
  now="$(envfile_get "$CTRLAPI_ENV_PATH" CTRLAPI_DB_URL)"
  if [ "$now" = "postgres://ctrlapi_rt:$newpw@127.0.0.1:5432/stayconnect?sslmode=disable" ] && [ -f "$CTRLAPI_ENV_PATH.bak-dbrole-20260103T000000Z" ]; then
    ok "dsn switch: a rotated runtime password is written to the URL (the old URL is backed up)"
  else bad "dsn switch: the URL kept the old runtime password after the secret changed"; fi
  if [ "$(central_owner_dsn)" = "postgres://stayconnect:olddbpassword@127.0.0.1:5432/stayconnect?sslmode=disable" ]; then
    ok "owner DSN: built from db_password and the host/database of CTRLAPI_DB_URL"
  else bad "owner DSN has an unexpected shape"; fi
  DRY_RUN=1 central_runtime_role_apply 2>&1 | grep -q 'DRY-RUN' && ok "runtime role: dry-run prints and changes nothing" || bad "runtime role: dry-run did not announce itself"
  exit "$fail"
) || fail=1

# ---- ordering, by inspection: the role is proven BEFORE ctrlapi is pointed at it, and before it starts
D="$HERE/central-deploy.sh"
if grep -qE '^if central_runtime_role_apply && central_runtime_dsn_switch; then' "$D"; then ok "deploy: the DSN is switched only when the role was applied and verified"
else bad "deploy: the DSN switch is not conditional on a verified role"; fi
a="$(grep -n -m1 'central_runtime_role_apply' "$D" | cut -d: -f1)"; b="$(grep -n -m1 'step "5/8 ctrlapi"' "$D" | cut -d: -f1)"
[ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] && ok "deploy: the role is set up before the new ctrlapi starts" || bad "deploy: role set-up is not before step 5"
I="$HERE/central-install.sh"
a="$(grep -n -m1 '^central_runtime_role_apply' "$I" | cut -d: -f1)"; b="$(grep -n -m1 'step "11/14 start ctrlapi"' "$I" | cut -d: -f1)"
[ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] && ok "install: the role is set up before ctrlapi starts" || bad "install: role set-up is not before step 11"
grep -q 'CTRLAPI_DB_URL="$(central_owner_dsn)"' "$I" && ok "install: the first admin is seeded as the database owner" || bad "install: seed-admin does not use the owner DSN"
grep -q 'pg_restore .*--no-acl' "$I" && ok "install: the restore does not import the old host's grants (they are re-asserted)" || bad "install: pg_restore keeps the dump's ACLs"
grep -q '^CTRLAPI_DB_URL=postgres://ctrlapi_rt:CHANGE_ME_DB_RUNTIME_PASSWORD@' "$HERE/../env/ctrlapi.env.example" \
  && ok "env example: CTRLAPI_DB_URL names the runtime role" || bad "env example: CTRLAPI_DB_URL still names the owner"
grep -q 'pg_dump -U "\$CENTRAL_DB_USER"' "$HERE/central-backup.sh" && ok "backup: pg_dump still runs as the owner" || bad "backup: pg_dump is not run as the owner"

# ---- central-deploy.sh, by inspection
line() { grep -n -m1 -- "$1" "$D" | cut -d: -f1; }
backup_at="$(line 'step "2/8 database backup"')"
for what in 'ensure_mfa_key "$REL/bin/ctrlapi"' 'check_mfa_key "$REL/bin/ctrlapi"' 'caddy validate --config "$WORK/Caddyfile"'; do
  at="$(line "$what")"
  [ -n "$at" ] && [ "$at" -lt "$backup_at" ] && ok "deploy: $what runs before the backup and the switch" \
    || bad "deploy: $what is not before step 2 (line ${at:-none}, backup at $backup_at)"
done
grep -q 'the running configuration was left in place' "$D" && bad "deploy: an invalid Caddyfile still only warns" \
  || ok "deploy: an invalid Caddyfile aborts the deploy"
grep -q "'\"signin\":\"available\"'" "$D" && ok "deploy: the smoke test requires sign-in to be available" \
  || bad "deploy: the smoke test does not check sign-in"
grep -q 'central-\*-pre-\*.dump.age' "$D" && ok "deploy: the rollback names encrypted pre-deploy dumps" \
  || bad "deploy: the rollback ignores .age dumps"

exit "$fail"
