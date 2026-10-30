#!/bin/bash
docker exec stayconnect-pg psql -X -U stayconnect -d stayconnect_site -c "select pg_stat_statements_reset()" >/dev/null
docker exec stayconnect-pg psql -X -U stayconnect -d stayconnect_site -c "select pg_stat_reset()" >/dev/null
echo reset
