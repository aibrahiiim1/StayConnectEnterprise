-- Drop the narrow grace-config lock boundary.
--
-- Rolling this back means the Checkout Converter must go back to taking SELECT ... FOR UPDATE on
-- iam_v2.site_checkout_grace_config directly, which requires svc_pmsd to hold UPDATE on that table again — and
-- that is precisely what the D32 assertion in gatep-grants.sql refuses to complete alongside. A database
-- rolled back to here therefore needs the matching Gate-P grant restored as well, and its full reconcile will
-- abort until that contradiction is resolved some other way.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p3_lock_grace_config(uuid, uuid);

COMMIT;
