#!/usr/bin/env bash
# Bounded readiness gate for the containerised site database.
#
# WHY THIS EXISTS
# ---------------
# netd (and pmsd) connect to the site DB during startup and exit 1 if that
# connect fails. The units ordered themselves with:
#
#     After=network-online.target docker.service postgresql.service
#
# but there is NO postgresql.service on this appliance -- Postgres runs in the
# stayconnect-pg container. systemd silently ignores an After= on a unit that
# does not exist, so that clause bought exactly nothing, and the only real
# ordering was docker.service, which means "the Docker daemon started", not
# "the database inside the container is accepting queries".
#
# At cold boot docker-proxy binds 127.0.0.1:5432 as soon as the container is
# created, so the TCP connect SUCCEEDS while Postgres is still initialising and
# the backend then drops it -- "unexpected EOF" / "connection reset by peer".
# netd exited 1 and systemd restarted it every 2s until Postgres caught up:
# 2 restarts on the 2026-08-16 22:01 boot, 1 on the boot before it. It always
# converged, but it converged by crash-looping, and a slower disk makes that
# burst arbitrarily long.
#
# NO NEW BOOT FAILURE MODE
# ------------------------
# This script ALWAYS exits 0. Ready -> return immediately. Not ready within the
# timeout -> warn and return 0 anyway, which hands control back to exactly the
# Restart=always retry loop that runs today. It can remove a crash-loop burst;
# it can never wedge a boot that would otherwise have succeeded. That property
# is the whole point of the design, so do not "harden" this into exit 1.
set -uo pipefail

CONTAINER="${SC_DB_CONTAINER:-stayconnect-pg}"
TIMEOUT="${SC_DB_WAIT_TIMEOUT:-60}"
INTERVAL="${SC_DB_WAIT_INTERVAL:-1}"
HOST="${SC_DB_HOST:-127.0.0.1}"
PORT="${SC_DB_PORT:-5432}"

ready() {
  # Preferred probe: ask Postgres itself, inside the container. This is the one
  # check that distinguishes "listening" from "accepting queries" -- the exact
  # distinction the docker-proxy race turns on.
  if command -v docker >/dev/null 2>&1; then
    docker exec "$CONTAINER" pg_isready -q -h 127.0.0.1 -p 5432 >/dev/null 2>&1 && return 0
    return 1
  fi
  # Fallback for a host without docker in PATH: a bare TCP open. Weaker (it is
  # satisfied by docker-proxy alone) but strictly better than no gate.
  (exec 3<>"/dev/tcp/$HOST/$PORT") >/dev/null 2>&1 && return 0
  return 1
}

waited=0
while [ "$waited" -lt "$TIMEOUT" ]; do
  if ready; then
    [ "$waited" -gt 0 ] && echo "site db ready after ${waited}s"
    exit 0
  fi
  sleep "$INTERVAL"
  waited=$(( waited + INTERVAL ))
done

echo "WARNING: site db ($CONTAINER) not ready after ${TIMEOUT}s; starting anyway and leaving recovery to Restart=always" >&2
exit 0
