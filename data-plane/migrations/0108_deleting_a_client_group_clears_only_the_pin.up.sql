-- 0108 -- DELETING A CLIENT GROUP CLEARS ONLY THE PIN ON AN AUTH CONTEXT.
--
-- 0107 declared the pin's foreign key over the composite (tenant_id, site_id, client_group_id) with ON DELETE
-- SET NULL. PostgreSQL applies SET NULL to EVERY referencing column, so deleting a group tried to null
-- tenant_id and site_id on the auth context as well and failed their NOT NULL constraints -- every group that
-- had ever been pinned became undeletable (found live on PRE-LIVE, 2026-10-08). The group id is a primary key,
-- so the single-column reference is exactly as strong and nulls only what it should.
--
-- Additive: one constraint is replaced by its corrected form. No row is touched.

BEGIN;

ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_client_group_fk;
DO $fk$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ac_client_group_fk') THEN
    ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_client_group_fk
      FOREIGN KEY (client_group_id) REFERENCES iam_v2.client_groups (id) ON DELETE SET NULL;
  END IF;
END $fk$;

COMMENT ON COLUMN iam_v2.auth_contexts.client_group_id IS
  'The Client Group in force for this sign-in (lowest priority among the matches), decided by scd from the '
  'Client''s verified factors and pinned here. NULL = Public, or a group deleted since (the evidence keeps its '
  'name). Eligibility reads this pin and never re-derives it.';

COMMIT;
