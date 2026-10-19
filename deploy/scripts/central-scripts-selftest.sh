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

# ---- central-deploy.sh, by inspection
D="$HERE/central-deploy.sh"
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
