-- Down for 0105. Dropping the staging table loses only the record of replacement REQUESTS; the client
-- networks themselves live in public.guest_networks and are untouched here. A replacement that is APPLIED but
-- not yet CONFIRMED should be confirmed or rolled back before this runs, because afterwards nothing remembers
-- that the successor row replaced anything.

BEGIN;

DROP INDEX IF EXISTS iam_v2.gnr_site_state;
DROP INDEX IF EXISTS iam_v2.gnr_one_live_per_network;
DROP TABLE IF EXISTS iam_v2.guest_network_replacements;

COMMIT;
