#!/usr/bin/env bash
# The SFTP false-success regression on the real services: a chrooted SFTP account (oglab-sftp, atmoz/sftp, lab only) is configured
# with a RELATIVE folder; Save must answer ok only after the ledger is in that folder (checked from the SFTP server's own disk, by
# SHA-256); a read-only folder must be refused with "not saved"; Remove must keep the remote copy and make erasure refused again.
. "$(dirname "$0")/lib.sh"
SFTP=oglab-sftp
docker inspect "$SFTP" >/dev/null 2>&1 || docker run -d --name "$SFTP" --network oglab-wan --ip 172.30.10.60 atmoz/sftp:alpine \
  'ledger:lab-sftp-pw:1001:1001:upload' >/dev/null
sleep 3
fp="$(api POST /backups/ledger/destination/probe '{"host":"172.30.10.60","port":22}' | sed -n 's/.*"fingerprint": "\([^"]*\)".*/\1/p')"
[ -n "$fp" ] || fail "the SFTP server's host key could not be read"
body() { printf '{"kind":"sftp","host":"172.30.10.60","port":22,"user":"ledger","dir":"%s","sftp_password":"lab-sftp-pw","fingerprint":"%s","password":"@PW"}' "$1" "$fp"; }

out="$(api POST /backups/ledger/destination/test "$(body upload/onegate)")"
echo "$out" | grep -q '"replicated": false' || fail "a test was not labelled a connectivity test"

out="$(api PUT /backups/ledger/destination "$(body upload/onegate)")"
echo "$out" | head -c 600; echo
echo "$out" | grep -q '^200' || fail "save did not answer 200"
remote="$(docker exec "$SFTP" sh -c 'sha256sum /home/ledger/upload/onegate/erasure-ledger.jsonl* | head -1' | awk '{print $1}')"
[ -n "$remote" ] || fail "save answered ok but the folder on the server is empty (the reported defect)"
note="$(docker exec "$SFTP" cat /home/ledger/upload/onegate/erasure-ledger.head.json)"
echo "$note" | grep -q "\"object_sha256\":\"$remote\"" || fail "the object on the server is not the one the note describes"
log "ok: the ledger is in the folder the server shows, and it matches its note"

docker exec "$SFTP" sh -c 'mkdir -p /home/ledger/upload/ro && chown root:root /home/ledger/upload/ro && chmod 555 /home/ledger/upload/ro'
out="$(api PUT /backups/ledger/destination "$(body upload/ro)")"
echo "$out" | head -c 400; echo
echo "$out" | grep -q '"saved": false' || fail "an unwritable folder was saved"
api GET /backups/ledger?fresh=1 | grep -q '"in_sync": true' || fail "a refused replacement disturbed the destination in use"

out="$(api POST /backups/ledger/destination/remove '{"password":"@PW","confirm":"remove secondary storage"}')"
echo "$out" | grep -q '"state": "not_configured"' || fail "removal did not make erasure refused"
docker exec "$SFTP" test -s /home/ledger/upload/onegate/erasure-ledger.head.json || fail "removing the setting deleted the remote copy"
log "ok: removal kept the remote copy"
