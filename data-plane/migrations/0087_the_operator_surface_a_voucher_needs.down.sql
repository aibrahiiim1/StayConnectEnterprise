-- Removing the voucher operator surface's database half.
--
-- WHAT THIS COSTS, and the first item is the reason to think before running it.
--
--   THE REVEAL AUDIT IS DESTROYED. iam_v2.voucher_code_reveals is the only record of who recovered which
--   guest credentials in the clear, and why. It is append-only precisely so that it cannot be edited; this
--   direction drops it whole, which is the one way it can be lost. TAKE A COPY FIRST if the record is
--   wanted. This direction exists so that a bad forward migration can be undone, not so that a record can
--   be tidied away -- and an operator who runs it to make a reveal disappear is doing the thing the table
--   was built to prevent.
--
--   Revocation stops working: iam_v2.voucher_revoke is the only path to state REVOKED, because svc_scd
--   deliberately holds no UPDATE on iam_v2.vouchers. Vouchers already revoked STAY revoked -- the state is
--   in the row, not in the function.
--
--   The operator list loses created_at and issued_by, so it can no longer say when a card was printed or by
--   whom. Those columns are dropped with their data.
--
-- WHAT IT DOES NOT COST. No voucher is deleted and no code becomes unreadable: the ciphertext, the blind
-- index and the key generation are untouched, so redemption and authentication behave exactly as before.
-- batch_id keeps whatever was written into it; it existed before this migration and is not dropped.
--
-- ORDER MATTERS. The trigger goes with its table, the function after the grant that depends on nothing, and
-- the columns last, because the indexes on them go with them.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.voucher_revoke(uuid, uuid, uuid, uuid, text);
DROP FUNCTION IF EXISTS iam_v2.voucher_code_generation_supersede(uuid, uuid, uuid, uuid, text);

DROP TRIGGER IF EXISTS voucher_code_reveals_append_only ON iam_v2.voucher_code_reveals;
DROP TABLE IF EXISTS iam_v2.voucher_code_reveals;
DROP FUNCTION IF EXISTS iam_v2.voucher_code_reveals_append_only();

DROP INDEX IF EXISTS iam_v2.vouchers_batch_lookup;
DROP INDEX IF EXISTS iam_v2.vouchers_created_lookup;

ALTER TABLE iam_v2.vouchers
  DROP COLUMN IF EXISTS issued_by,
  DROP COLUMN IF EXISTS created_at;

-- Rotation becomes impossible again, and superseded_at reverts to a column nothing writes. Generations
-- ALREADY retired stay retired: superseded_at is in the row, not in the function. Who retired them and why
-- is lost with these two columns, which is the cost of this direction.
ALTER TABLE iam_v2.voucher_code_key_generations
  DROP COLUMN IF EXISTS supersede_reason,
  DROP COLUMN IF EXISTS superseded_by;

COMMIT;
