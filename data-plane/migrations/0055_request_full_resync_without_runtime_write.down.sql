-- Remove the operator full-resync request function.
--
-- Rolling this back leaves the Full Resync Now control with no supported way to record a request: edged has
-- no write privilege on iam_v2.pms_interface_runtime and must not be given any, so the button will refuse
-- rather than fall back to writing the row directly. That is the intended failure direction.
--
-- Nothing in flight is disturbed. A command already written stays on the row and the owning worker will still
-- claim it; only the ability to record NEW requests goes away.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.request_full_resync(uuid,uuid,uuid,text);

COMMIT;
