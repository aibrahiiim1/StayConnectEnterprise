#!/usr/bin/env bash
# THE LEDGER'S SECONDARY-STORAGE DESTINATIONS AGAINST REAL SERVERS (GDPR final mission).
#
# Starts an S3 implementation that checks AWS Signature Version 4 itself (Scality CloudServer) and OpenSSH's own SFTP subsystem
# (atmoz/sftp) in disposable containers, then runs data-plane/internal/ledger/realservers_integration_test.go against them: a signed
# request round trip, a wrong secret and a missing bucket refused, a pinned SFTP host key (a different key refused), and a whole
# encrypted ledger committed and read back through each. The directory destination is exercised by the restore drill.
#
#   scripts/ledger-destinations-compat.sh
#
# Needs docker and Go. Containers are removed at the end. No appliance, no Central.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
S3_PORT="${S3_PORT:-38591}"
SFTP_PORT="${SFTP_PORT:-38592}"
cleanup() { docker rm -f ledger-compat-s3 ledger-compat-sftp >/dev/null 2>&1 || true; }
cleanup
trap cleanup EXIT

docker run -d --name ledger-compat-s3 -p "127.0.0.1:$S3_PORT:8000" -e S3BACKEND=mem -e REMOTE_MANAGEMENT_DISABLE=1 \
  -e SCALITY_ACCESS_KEY_ID=ledgerak -e SCALITY_SECRET_ACCESS_KEY=ledgersecret1234 zenko/cloudserver:latest >/dev/null || { echo "compat: S3 server did not start"; exit 2; }
docker run -d --name ledger-compat-sftp -p "127.0.0.1:$SFTP_PORT:22" atmoz/sftp:alpine ledger:ledgerpw:::upload >/dev/null || { echo "compat: SFTP server did not start"; exit 2; }

for i in $(seq 1 60); do
  curl -s -o /dev/null "http://127.0.0.1:$S3_PORT/" && break
  sleep 1
done
for i in $(seq 1 60); do
  (exec 3<>"/dev/tcp/127.0.0.1/$SFTP_PORT") 2>/dev/null && break
  sleep 1
done
sleep 3

export LEDGER_S3_ENDPOINT="http://127.0.0.1:$S3_PORT" LEDGER_S3_KEY=ledgerak LEDGER_S3_SECRET=ledgersecret1234
export LEDGER_SFTP_HOST=127.0.0.1 LEDGER_SFTP_PORT="$SFTP_PORT" LEDGER_SFTP_USER=ledger LEDGER_SFTP_PASSWORD=ledgerpw
cd "$ROOT/data-plane" || exit 1
go test -tags integration -count=1 -v -run 'TestReal' ./internal/ledger
