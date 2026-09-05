#!/usr/bin/env bash
# THE CERTIFICATE FAILURE THIS FILE REPRODUCES ACTUALLY HAPPENED.
#
# PRE-LIVE appliance, 2026-08-20 to 2026-09-05. HTTPS to the Hotel Admin failed certificate verification for
# every client for over a week, and every health surface said the certificate was fine:
#
#     status.json:  "status_threshold": "healthy",  "days_remaining": 729
#
# Both statements were true about the LEAF, which was minted on 2026-08-20 and runs to 2028. They were
# irrelevant, because the INTERMEDIATE pinned inside the same served fullchain expired on 2026-08-27. Caddy's
# local authority issues seven-day intermediates and rotates them in its own storage; a vhost that serves a
# static fullchain file captures whichever one existed at mint time, so the served chain rots weekly however
# long the leaf lives.
#
# `openssl x509 -in <fullchain>` reads only the FIRST certificate, so every check the manager made -- is it
# valid, is it near expiry, how many days remain -- was answered by the leaf alone and could not see this.
#
# And the renewal that would have re-minted the chain never ran: the unit names
# /usr/local/sbin/stayconnect-hotel-admin-cert-manager, nothing installed it, and the timer failed 203/EXEC
# nightly for two weeks. That half is covered by install-service-units-selftest.sh; this file covers the
# health model.
set -uo pipefail
# On a Git-Bash/MSYS host, an argument beginning with "/" is rewritten into a Windows path before a native
# program sees it, so `-subj "/CN=root"` reaches openssl as "C:/Program Files/Git/CN=root" and every mint
# fails. Harmless on Linux, where these run for real.
# NARROWLY, only the subject argument. Excluding everything ('*') stops MSYS translating the FILE paths too,
# and a native openssl then cannot write to /tmp/... at all -- so the fix for one Windows quirk created a
# worse one. Only "/CN=..." needs protecting.
export MSYS2_ARG_CONV_EXCL='/CN='
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MGR="$HERE/hotel-admin-cert-manager.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0
pass=0
ok() { echo "  ok: $1"; pass=$((pass+1)); }
no() { echo "  *** FAIL: $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/        /' | head -6; fail=1; }

command -v openssl >/dev/null 2>&1 || { echo "SKIP: openssl is required"; echo "HOTEL_ADMIN_CERT_SELFTEST = SKIP"; exit 0; }
[ -f "$MGR" ] || { echo "SKIP: no cert manager at $MGR"; echo "HOTEL_ADMIN_CERT_SELFTEST = SKIP"; exit 0; }

# ---- a miniature CA, so the cases are real certificates and not string fixtures ---------------------------
CA="$WORK/ca"; mkdir -p "$CA"
mkcert() { # mkcert <name> <days> <issuer-name|self> [CA:TRUE]
  local n="$1" days="$2" iss="$3" ca="${4:-}"
  openssl ecparam -name prime256v1 -genkey -noout -out "$CA/$n.key" 2>/dev/null
  local ext="$WORK/$n.ext"
  if [ -n "$ca" ]; then printf 'basicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign\n' > "$ext"
  else printf 'basicConstraints=critical,CA:FALSE\nsubjectAltName=DNS:hotel.stayconnect.local,IP:172.21.60.25\nextendedKeyUsage=serverAuth\n' > "$ext"; fi
  if [ "$iss" = "self" ]; then
    # `openssl req` takes -addext, never -extfile. Passing the latter produced no certificate at all and the
    # cases below then failed for want of a fixture rather than for anything they were asserting.
    openssl req -new -x509 -key "$CA/$n.key" -out "$CA/$n.crt" -days "$days" -subj "/CN=$n" \
      -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign" 2>/dev/null
  else
    openssl req -new -key "$CA/$n.key" -out "$CA/$n.csr" -subj "/CN=$n" 2>/dev/null
    openssl x509 -req -in "$CA/$n.csr" -CA "$CA/$iss.crt" -CAkey "$CA/$iss.key" -CAcreateserial \
      -out "$CA/$n.crt" -days "$days" -extfile "$ext" 2>/dev/null
  fi
}

# A root, a LIVE intermediate, and an intermediate that has already expired -- the exact shape found live.
mkcert root 3650 self CA:TRUE
mkcert live-intermediate 7 root CA:TRUE
# An intermediate INSIDE the two-day chain-renewal window: this is what Caddy's seven-day intermediate looks
# like on its sixth day, and it is the state a daily renewal must act on.
mkcert soon-intermediate 1 root CA:TRUE
mkcert leaf 730 live-intermediate

# openssl cannot mint an already-expired certificate directly, so the expired intermediate is produced by
# minting with a validity that has already elapsed via -not_before/-not_after where available, and otherwise
# by faking the clock with a 1-second life and waiting it out. The second path is slow but portable.
# AN ALREADY-EXPIRED INTERMEDIATE, PORTABLY.
#
# `openssl x509 -req` only learned -not_before/-not_after in 3.5, and the appliance and CI both run older
# builds - so the version-gated path skipped the single most important case on every machine that matters,
# and the suite reported PASS having never exercised the live condition. `openssl ca` accepts -startdate and
# -enddate in every version in use, so the fixture is built with that instead.
EXPIRED_OK=0
CADB="$WORK/ca-db"; mkdir -p "$CADB/newcerts"
: > "$CADB/index.txt"; echo 1000 > "$CADB/serial"
cat > "$CADB/openssl.cnf" <<CNF
[ ca ]
default_ca = CA_default
[ CA_default ]
dir               = $CADB
database          = \$dir/index.txt
new_certs_dir     = \$dir/newcerts
serial            = \$dir/serial
certificate       = $CA/root.crt
private_key       = $CA/root.key
default_md        = sha256
policy            = policy_any
email_in_dn       = no
rand_serial       = no
unique_subject    = no
copy_extensions   = none
[ policy_any ]
commonName        = supplied
[ v3_ca ]
basicConstraints  = critical,CA:TRUE
keyUsage          = critical,keyCertSign
CNF
openssl ecparam -name prime256v1 -genkey -noout -out "$CA/dead-intermediate.key" 2>/dev/null
openssl req -new -key "$CA/dead-intermediate.key" -out "$CA/dead-intermediate.csr"   -subj "/CN=dead-intermediate" 2>/dev/null
if openssl ca -batch -config "$CADB/openssl.cnf" -extensions v3_ca      -startdate 20260101000000Z -enddate 20260201000000Z      -in "$CA/dead-intermediate.csr" -out "$CA/dead-intermediate.crt" >/dev/null 2>&1    && [ -s "$CA/dead-intermediate.crt" ]; then
  EXPIRED_OK=1
fi

# Load the manager's functions without running its dispatcher.
CA_DIR="$CA"; CHAIN=""; RENEW_DAYS=45; CHAIN_RENEW_DAYS=2
# shellcheck disable=SC1090
eval "$(sed -n '/^chain_certs()/,/^}/p;/^chain_expiring()/,/^}/p;/^chain_verifies()/,/^}/p' "$MGR")"
type chain_expiring >/dev/null 2>&1 || { echo "SKIP: the manager exposes no chain helpers to test"; echo "HOTEL_ADMIN_CERT_SELFTEST = SKIP"; exit 0; }

echo "== the served chain, not just the leaf =="

# 1. THE LIVE FAILURE: a valid leaf above an EXPIRED intermediate.
if [ "$EXPIRED_OK" = "1" ]; then
  cat "$CA/leaf.crt" "$CA/dead-intermediate.crt" > "$WORK/rotten.fullchain"
  if which="$(chain_expiring "$WORK/rotten.fullchain" 0)"; then
    ok "a valid leaf above an EXPIRED intermediate is detected as expiring: ${which}"
  else
    no "an expired intermediate was NOT detected — this is the exact condition that broke HTTPS for two weeks"
  fi
  if chain_verifies "$WORK/rotten.fullchain"; then
    no "a chain with an expired intermediate was reported as verifying"
  else
    ok "a chain with an expired intermediate does NOT verify against the root"
  fi
  # And the old, leaf-only question still answers 'fine' — which is why it was the wrong question.
  if openssl x509 -in "$WORK/rotten.fullchain" -noout -checkend 0 >/dev/null 2>&1; then
    ok "the leaf-only check still reports the broken chain as healthy (why chain awareness was required)"
  else
    no "the fixture is wrong: its leaf is not valid, so it does not reproduce the live condition"
  fi
else
  echo "  skip: this openssl cannot mint a back-dated certificate, so the expired-intermediate case is unrunnable"
fi

# 2. A HEALTHY CHAIN passes, or the check is useless.
cat "$CA/leaf.crt" "$CA/live-intermediate.crt" > "$WORK/good.fullchain"
if chain_expiring "$WORK/good.fullchain" 0 >/dev/null; then
  no "a healthy chain was reported as expired"
else
  ok "a healthy chain is not reported as expired"
fi
if chain_verifies "$WORK/good.fullchain"; then
  ok "a healthy chain verifies against the root"
else
  no "a healthy chain failed to verify"
fi

# 3. THE RENEWAL WINDOW MUST TRACK THE CHAIN'S CLOCK, not the leaf's. Caddy's intermediate lives seven days;
#    the leaf lives two years. A window measured against the leaf renews years too late.
cat "$CA/leaf.crt" "$CA/soon-intermediate.crt" > "$WORK/soon.fullchain"
if chain_expiring "$WORK/soon.fullchain" $((CHAIN_RENEW_DAYS*86400)) >/dev/null; then
  ok "a chain whose intermediate is inside the ${CHAIN_RENEW_DAYS}-day window is flagged for renewal"
else
  no "an intermediate one day from expiry was not flagged — the renewal window is measured against the wrong certificate"
fi
# ...and a chain comfortably outside the window is NOT flagged, or the manager would re-mint every night.
if chain_expiring "$WORK/good.fullchain" $((CHAIN_RENEW_DAYS*86400)) >/dev/null; then
  no "a healthy seven-day intermediate was flagged inside a two-day window — renewal would never be a no-op"
else
  ok "a chain outside the renewal window is left alone, so an early run is a safe no-op"
fi

# 4. And the leaf alone would NOT have been flagged by the same window, which is the whole point.
if openssl x509 -in "$CA/leaf.crt" -noout -checkend $((2*86400)) >/dev/null 2>&1; then
  ok "the leaf alone is comfortably inside the window, so only chain awareness triggers the renewal"
else
  no "fixture drift: the leaf is itself near expiry, so this case proves nothing"
fi

echo "============================================================"
if [ "$fail" = "0" ]; then
  echo "HOTEL_ADMIN_CERT_SELFTEST = PASS ($pass cases)"
  exit 0
fi
echo "HOTEL_ADMIN_CERT_SELFTEST = FAIL"
exit 1
