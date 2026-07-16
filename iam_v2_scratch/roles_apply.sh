#!/usr/bin/env bash
# Rebuild iam_v2 with objects OWNED BY iam_v2_owner (not the superuser), for least-privilege proof.
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"
export SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE
require_scratch
C="$SCRATCH_CONTAINER"; DB="$SCRATCH_DB"
run(){ docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -qAt "$@"; }

# reset iam_v2, (re)create roles + grants
run -c "DROP SCHEMA IF EXISTS iam_v2 CASCADE;" >/dev/null
run -f - < "$HERE/roles.sql" >/dev/null
# MG-0 anchor (privileged platform migration; superuser) — created if absent
bash "$HERE/mg0.sh" >/dev/null 2>&1
# schema owned by the owner + schema-scoped default privileges (deny PUBLIC/service by default)
run -c "CREATE SCHEMA IF NOT EXISTS iam_v2 AUTHORIZATION iam_v2_owner;" >/dev/null
run -c "ALTER DEFAULT PRIVILEGES FOR ROLE iam_v2_owner IN SCHEMA iam_v2 REVOKE ALL ON TABLES FROM PUBLIC;
        ALTER DEFAULT PRIVILEGES FOR ROLE iam_v2_owner IN SCHEMA iam_v2 REVOKE ALL ON SEQUENCES FROM PUBLIC;
        ALTER DEFAULT PRIVILEGES FOR ROLE iam_v2_owner IN SCHEMA iam_v2 REVOKE ALL ON FUNCTIONS FROM PUBLIC;
        REVOKE ALL ON SCHEMA iam_v2 FROM PUBLIC;" >/dev/null
# apply MG-1..MG-9 AS iam_v2_owner (SET ROLE) so every object is owned by iam_v2_owner
for f in mg1_pms_interface_core mg2_plans_packages mg3_identities_credentials mg4_stay_domain \
         mg5_auth_commerce mg6_entitlements_devices_sessions mg7_postings_payments \
         mg8_resolution_aux mg9_engine; do
  { echo "SET ROLE iam_v2_owner;"; cat "$HERE/migrations/$f.sql"; } \
    | run >/dev/null || { echo "APPLY FAILED at $f"; exit 1; }
done
echo "ROLES_APPLY_OK"
