-- Down for 0157: the delete function goes away and the revision and secret guards are exactly what they were before (no DELETE under any circumstance).
-- Connections already deleted by it stay deleted; they carried no history.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.delete_unused_pms_interface(uuid, uuid, uuid);

DROP TRIGGER IF EXISTS imm_pms_rev ON iam_v2.pms_interface_revisions;
CREATE TRIGGER imm_pms_rev BEFORE DELETE OR UPDATE ON iam_v2.pms_interface_revisions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();

DROP TRIGGER IF EXISTS sg_guard ON iam_v2.pms_interface_secret_generations;
CREATE TRIGGER sg_guard BEFORE DELETE OR UPDATE ON iam_v2.pms_interface_secret_generations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_secret_gen_guard();

DROP FUNCTION IF EXISTS iam_v2.trg_reject_update_delete_unless_unused_delete();
DROP FUNCTION IF EXISTS iam_v2.trg_secret_gen_guard_unless_unused_delete();

COMMIT;
