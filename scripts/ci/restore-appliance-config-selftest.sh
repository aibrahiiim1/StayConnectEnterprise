#!/usr/bin/env bash
# Disposable proof for deploy/scripts/restore-appliance-config.sh, run inside a throwaway ubuntu:22.04 container
# (the appliance OS) so the real age, tar and coreutils are exercised. It never touches an appliance.
#
#   * the archive is laid out exactly as scd's writeEtcArchive writes it (paths relative to /etc)
#   * identity, licence and data-encryption keys come back from the archive (the old appliance), not the new machine
#   * database credentials stay the NEW machine's: *_DB_URL lines, .dsn and secrets/pg-superuser.env
#   * the previous configuration is saved; nothing ends up nested under /etc/stayconnect/stayconnect
#   * a wrong key changes nothing
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd -P)"
IMAGE="${RESTORE_SELFTEST_IMAGE:-ubuntu:22.04}"
SCRIPT="$(cygpath -w "$ROOT/deploy/scripts/restore-appliance-config.sh" 2>/dev/null || echo "$ROOT/deploy/scripts/restore-appliance-config.sh")"
MSYS_NO_PATHCONV=1 docker run --rm -i -v "$SCRIPT":/opt/restore.sh:ro "$IMAGE" bash -s <<'IN'
set -uo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null && apt-get install -y -qq age python3 >/dev/null || { echo "cannot install age"; exit 2; }
pass=0; fail=0
ok()  { pass=$((pass+1)); echo "  PASS  $1"; }
bad() { fail=$((fail+1)); echo "  FAIL  $1"; }
W=/w; mkdir -p $W/old/stayconnect/{identity,license,secrets} $W/new/stayconnect/{identity,secrets}
# the OLD appliance, as archived
echo OLD-IDENTITY > $W/old/stayconnect/identity/ed25519.key
echo OLD-LICENCE  > $W/old/stayconnect/license/current.json
echo OLD-DEK      > $W/old/stayconnect/secrets/voucher_dek.key
echo 'STAYCONNECT_PG_PASSWORD=OLDSUPER' > $W/old/stayconnect/secrets/pg-superuser.env
printf 'SCD_DB_URL=postgres://svc_scd:OLDPW@127.0.0.1/x\nSCD_SOCKET=/run/stayconnect/scd.sock\nSTAYCONNECT_PHASE2_PORTAL=true\n' > $W/old/stayconnect/scd.env
printf 'SCD_DB_URL=postgres://svc_scd:OLDPW@127.0.0.1/x\n' > $W/old/stayconnect/.dsn
age-keygen -o $W/holder.key 2>/dev/null; age-keygen -o $W/other.key 2>/dev/null
( cd $W/old && tar -czf - stayconnect ) | age -r "$(age-keygen -y $W/holder.key)" > $W/etc-20261026T020000Z.tgz.age
# the NEW machine, as provisioned
echo NEW-IDENTITY > $W/new/stayconnect/identity/ed25519.key
echo NEW-DEK      > $W/new/stayconnect/secrets/voucher_dek.key
echo 'STAYCONNECT_PG_PASSWORD=NEWSUPER' > $W/new/stayconnect/secrets/pg-superuser.env
printf 'SCD_DB_URL=postgres://svc_scd:NEWPW@127.0.0.1/x\nSCD_SOCKET=/run/stayconnect/scd.sock\n' > $W/new/stayconnect/scd.env
printf 'SCD_DB_URL=postgres://svc_scd:NEWPW@127.0.0.1/x\n' > $W/new/stayconnect/.dsn
export SC_ETC_DIR=$W/new/stayconnect

bash /opt/restore.sh --archive $W/etc-20261026T020000Z.tgz.age --identity $W/other.key --yes >/tmp/o 2>&1
[ $? != 0 ] && grep -qx NEW-IDENTITY $W/new/stayconnect/identity/ed25519.key && ok "a wrong key is refused and changes nothing" || bad "wrong key"

bash /opt/restore.sh --archive $W/etc-20261026T020000Z.tgz.age --identity $W/holder.key --yes >/tmp/o 2>&1 || { bad "restore failed"; cat /tmp/o; }
E=$W/new/stayconnect
grep -qx OLD-IDENTITY $E/identity/ed25519.key && ok "identity comes from the archive" || bad "identity not restored"
grep -qx OLD-LICENCE $E/license/current.json && ok "licence comes from the archive" || bad "licence not restored"
grep -qx OLD-DEK $E/secrets/voucher_dek.key && ok "data-encryption keys come from the archive" || bad "DEK not restored"
grep -q 'NEWPW' $E/scd.env && ! grep -q OLDPW $E/scd.env && ok "the env file keeps this machine's database credentials" || bad "scd.env credentials: $(grep DB_URL $E/scd.env)"
grep -q STAYCONNECT_PHASE2_PORTAL=true $E/scd.env && ok "the env file's other settings come from the archive" || bad "settings not restored"
grep -q NEWPW $E/.dsn && ok ".dsn is this machine's" || bad ".dsn replaced"
grep -q NEWSUPER $E/secrets/pg-superuser.env && ok "the database superuser password file is this machine's" || bad "pg-superuser.env replaced"
[ ! -e $E/stayconnect ] && ok "nothing is nested under /etc/stayconnect/stayconnect" || bad "nested restore"
ls -d $W/new/stayconnect.before-restore-* >/dev/null 2>&1 && grep -qx NEW-IDENTITY $W/new/stayconnect.before-restore-*/identity/ed25519.key \
  && ok "the previous configuration was saved first" || bad "no saved copy"
echo "restore-appliance-config selftest: $pass passed, $fail failed"
[ "$fail" = 0 ]
IN
