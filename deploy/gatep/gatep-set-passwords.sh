#!/usr/bin/env bash
# Gate P — set svc_* role passwords securely (SCRAM-SHA-256; no cleartext in SQL/argv/logs).
#
# For each runtime role: generate an independent cryptographically-secure password on the appliance,
# compute its SCRAM verifier locally (scram_verifier.py, password on STDIN), and set it via
# `ALTER ROLE ... PASSWORD '<verifier>'` piped over STDIN (never argv). The cleartext password is
# written ONLY into the 0600 DSN output file for the caller to consume, then removed by the caller.
#
# Aborts if PostgreSQL statement logging could capture DDL cleartext.
#
# Usage: gatep-set-passwords.sh --pg-exec "docker exec -i stayconnect-pg" --db <db> --dsn-out <0600 file>
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
PGEXEC=""; DB=""; DSN_OUT=""
while [ $# -gt 0 ]; do case "$1" in
  --pg-exec) PGEXEC="$2"; shift 2;; --db) DB="$2"; shift 2;; --dsn-out) DSN_OUT="$2"; shift 2;;
  *) echo "unknown arg: $1" >&2; exit 2;; esac; done
[ -n "$PGEXEC" ] && [ -n "$DB" ] && [ -n "$DSN_OUT" ] || { echo "need --pg-exec --db --dsn-out" >&2; exit 2; }

psql_db() { $PGEXEC psql -v ON_ERROR_STOP=1 -U stayconnect -d "$DB" "$@"; }

# 1. Prove logging cannot capture DDL cleartext.
ls=$(psql_db -Atc "show log_statement"); lmd=$(psql_db -Atc "show log_min_duration_statement")
if [ "$ls" != "none" ] || [ "$lmd" != "-1" ]; then
  echo "REFUSE: log_statement=$ls log_min_duration_statement=$lmd could capture credentials" >&2; exit 3
fi
echo "log-safety: log_statement=$ls log_min_duration_statement=$lmd (no capture)"

umask 077
: > "$DSN_OUT"; chmod 600 "$DSN_OUT"
declare -A PORTMAP=( [svc_scd]=SCD [svc_edged]=EDGED [svc_acctd]=ACCTD [svc_netd]=NETD )
for role in svc_scd svc_edged svc_acctd svc_netd; do
  pw="$(python3 -c 'import secrets;print(secrets.token_urlsafe(24))')"
  verifier="$(printf '%s' "$pw" | python3 "$HERE/scram_verifier.py")"
  # verifier (a hash) only — cleartext never enters SQL/argv; statement piped via STDIN.
  printf "ALTER ROLE %s PASSWORD '%s';\n" "$role" "$verifier" | psql_db -q
  # cleartext -> 0600 DSN line for the caller (service env), URL-encoded-safe token_urlsafe.
  printf '%s_DB_URL=postgres://%s:%s@127.0.0.1:5432/%s?sslmode=disable\n' \
    "${PORTMAP[$role]}" "$role" "$pw" "$DB" >> "$DSN_OUT"
  unset pw verifier
done
echo "passwords set for svc_scd/svc_edged/svc_acctd/svc_netd (SCRAM); DSNs -> $DSN_OUT (0600)"
