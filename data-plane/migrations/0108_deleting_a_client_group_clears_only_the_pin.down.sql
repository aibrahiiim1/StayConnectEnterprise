-- 0108 down -- restores the 0107 composite constraint (with its SET NULL defect).
BEGIN;
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_client_group_fk;
DO $fk$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ac_client_group_fk') THEN
    ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_client_group_fk
      FOREIGN KEY (tenant_id, site_id, client_group_id)
      REFERENCES iam_v2.client_groups (tenant_id, site_id, id) ON DELETE SET NULL;
  END IF;
END $fk$;
COMMIT;
