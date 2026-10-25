-- Down for 0151. Dropping the table loses only the record of staged switch-on/off requests; public.guest_networks is untouched. A change that is APPLIED but not
-- yet CONFIRMED should be confirmed or rolled back first, because afterwards nothing remembers which flag a rollback should restore.

BEGIN;

DROP INDEX IF EXISTS iam_v2.gnc_group;
DROP INDEX IF EXISTS iam_v2.gnc_site_state;
DROP INDEX IF EXISTS iam_v2.gnc_one_live_per_network;
DROP TABLE IF EXISTS iam_v2.guest_network_changes;

COMMIT;
