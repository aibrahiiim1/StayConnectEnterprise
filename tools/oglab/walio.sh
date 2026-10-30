#!/bin/bash
docker exec stayconnect-pg psql -X -U stayconnect -d postgres -c "ALTER SYSTEM SET track_wal_io_timing=on" -c "select pg_reload_conf()" >/dev/null
docker exec stayconnect-pg psql -X -U stayconnect -d stayconnect_site -c "select pg_stat_reset_shared('wal')" >/dev/null
echo reset
