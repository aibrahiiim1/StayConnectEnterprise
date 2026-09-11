-- REVERSE 0066: put back exactly the values that were there before.
--
-- This restores from iam_v2.backfill_0066_identity_text, which 0066.up.sql captured row-by-row. It does NOT
-- try to invert the transformation -- upper-casing is not invertible, and guessing the original casing would
-- be fabricating guest data. A row that was never captured was never changed and is left alone.
--
-- WHAT REVERSING COSTS, STATED PLAINLY. Rolling back reinstates the defect: the ~1-in-7 guests whose PMS
-- surname is not stored upper-case go back to being unable to sign in with room + surname. That is the
-- correct behaviour for a rollback -- it returns the system to its prior state -- but it is a reason to fix
-- forward rather than to reverse, unless the correction itself is what is wrong.
--
-- The code fix (internal/namenorm) is independent of this migration. Reversing this data correction without
-- also reverting the code leaves NEW rows canonical and OLD rows raw, which is a coherent state: the
-- authentication query simply matches the new ones and not the old ones, exactly as it did before 0066.

BEGIN;

UPDATE iam_v2.stay_guests g
   SET first_name_norm = b.prior_first_name,
       last_name_norm  = b.prior_last_name
  FROM iam_v2.backfill_0066_identity_text b
 WHERE b.kind = 'stay_guest'
   AND b.row_id = g.id
   AND b.tenant_id = g.tenant_id
   AND b.site_id   = g.site_id;

UPDATE iam_v2.stays s
   SET normalized_room_number = b.prior_room
  FROM iam_v2.backfill_0066_identity_text b
 WHERE b.kind = 'stay_room'
   AND b.row_id = s.id
   AND b.tenant_id = s.tenant_id
   AND b.site_id   = s.site_id;

-- The evidence table holds guest names and has no purpose once the restore is done.
DROP TABLE IF EXISTS iam_v2.backfill_0066_identity_text;

DROP FUNCTION IF EXISTS iam_v2.norm_identity_text(text);

COMMIT;
