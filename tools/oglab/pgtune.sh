#!/bin/bash
# lab: make the database memory settings what timescaledb-tune would pick on a 4 GB VM, plus diagnostics (pg_stat_statements)
P="docker exec stayconnect-pg psql -X -U stayconnect -d postgres -c"
$P "ALTER SYSTEM SET shared_buffers='1GB'"
$P "ALTER SYSTEM SET effective_cache_size='3GB'"
$P "ALTER SYSTEM SET work_mem='5MB'"
$P "ALTER SYSTEM SET maintenance_work_mem='256MB'"
$P "ALTER SYSTEM SET shared_preload_libraries='timescaledb,pg_stat_statements'"
$P "ALTER SYSTEM SET pg_stat_statements.max=2000"
$P "ALTER SYSTEM SET track_io_timing=on"
$P "ALTER SYSTEM SET log_lock_waits=on"
$P "ALTER SYSTEM SET deadlock_timeout='500ms'"
$P "ALTER SYSTEM SET log_min_duration_statement='800ms'"
docker restart stayconnect-pg
sleep 8
docker exec stayconnect-pg psql -X -U stayconnect -d stayconnect_site -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"
docker exec stayconnect-pg psql -X -U stayconnect -d stayconnect_site -Atc "show shared_buffers; show shared_preload_libraries"
