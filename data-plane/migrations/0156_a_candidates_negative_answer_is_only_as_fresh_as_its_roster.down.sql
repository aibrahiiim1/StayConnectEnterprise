-- Down for 0156: p3_interface_can_authorise returns to its 0155 definition (evidence time now(), so the roster age ceiling is not applied to a candidate's answer).
-- Do this only with no Shared STRICT policy in use; see 0155.down.sql.

BEGIN;

DO $guard$
DECLARE n int;
BEGIN
  IF to_regclass('iam_v2.guest_network_pms_policy') IS NOT NULL THEN
    SELECT count(*) INTO n FROM iam_v2.guest_network_pms_policy WHERE mode = 'SHARED_STRICT';
    IF n > 0 THEN
      RAISE EXCEPTION 'refusing to roll back 0156: % network(s) are served by a Shared STRICT set, and without it a disconnected candidate with an expired roster could clear another interface''s guest. Change each to a dedicated or no-room-sign-in policy under PMS routing first.', n
        USING ERRCODE = 'P0001';
    END IF;
  END IF;
END
$guard$;

CREATE OR REPLACE FUNCTION iam_v2.p3_interface_can_authorise(p_tenant uuid, p_site uuid, p_interface uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(bool_or(iam_v2.p3_feed_authorizes(pi.tenant_id, pi.site_id, pi.id, pi.current_revision_id, now())), false)
    FROM iam_v2.pms_interfaces pi
   WHERE pi.tenant_id = p_tenant AND pi.site_id = p_site AND pi.id = p_interface AND pi.lifecycle_state = 'ACTIVE';
$fn$;

COMMIT;
