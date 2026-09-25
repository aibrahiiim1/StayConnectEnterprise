-- 0091 down — back to "revisions are immutable, full stop".
--
-- Restores both revision triggers to the shared trg_reject_update_delete() and removes the four functions.
-- Deletions already performed are NOT undone: they removed only records that were never used, and there is
-- nothing a down-migration could honestly restore them from. Recreate such a package or plan if it is needed.
DROP TRIGGER IF EXISTS imm_pkg_rev ON iam_v2.internet_package_revisions;
CREATE TRIGGER imm_pkg_rev BEFORE DELETE OR UPDATE ON iam_v2.internet_package_revisions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();

DROP TRIGGER IF EXISTS imm_plan_rev ON iam_v2.service_plan_revisions;
CREATE TRIGGER imm_plan_rev BEFORE DELETE OR UPDATE ON iam_v2.service_plan_revisions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();

DROP FUNCTION IF EXISTS iam_v2.internet_package_delete_unused(uuid, uuid, uuid, uuid, text);
DROP FUNCTION IF EXISTS iam_v2.service_plan_delete_unused(uuid, uuid, uuid, uuid, text);
DROP FUNCTION IF EXISTS iam_v2.internet_package_deletion_blockers(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.service_plan_deletion_blockers(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.trg_catalogue_revision_immutable();
