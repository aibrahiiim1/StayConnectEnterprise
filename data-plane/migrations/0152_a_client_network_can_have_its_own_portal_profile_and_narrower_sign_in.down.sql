-- Down for 0152. Named profiles and per-network narrowing are lost; the site's default portal design (tenants.branding) is untouched, and every network
-- returns to it and to the site's sign-in methods.

BEGIN;

-- Back to stamping every sign-in with the site's notice only.
DROP TRIGGER IF EXISTS p9_z_profile_privacy_notice ON iam_v2.auth_contexts;
DROP FUNCTION IF EXISTS iam_v2.profile_privacy_notice_stamp();

DROP TABLE IF EXISTS iam_v2.network_signin_overrides;
DROP TABLE IF EXISTS iam_v2.portal_profile_assignments;
DROP TABLE IF EXISTS iam_v2.portal_profiles;

COMMIT;
