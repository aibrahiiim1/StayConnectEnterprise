-- AD-46 (audit P6 L11): the operator's reason for a cross-PMS transfer was required but never stored with the transfer.
--
-- iam_v2.entitlement_transfers.reason is pinned by the 0027 trigger to the constant 'CROSS_PMS_TRANSFER' (it is the kind of
-- lineage row, not free text), so the operator's text needs its own column. operator_reason is that column, under the SAME
-- retention as every other thing staff typed about a guest (migration 0147): it is a registered 'note' column, so the
-- staff-notes period scrubs it, and wherever the existing erasure paths scrub entitlement_transfers.reason (a stay's
-- anonymisation, a guest's erasure) a trigger scrubs it in the same statement. Nothing is stored for a transfer made before
-- this migration (NULL), and nothing is invented for it.

ALTER TABLE iam_v2.entitlement_transfers ADD COLUMN IF NOT EXISTS operator_reason text;
DO $$ BEGIN
    ALTER TABLE iam_v2.entitlement_transfers ADD CONSTRAINT entitlement_transfers_operator_reason_bounded
        CHECK (operator_reason IS NULL OR length(btrim(operator_reason)) BETWEEN 4 AND 500);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

INSERT INTO iam_v2.scrub_registry (table_name, column_name, kind, ts_column, handled, jsonb_key) VALUES
  ('iam_v2.entitlement_transfers', 'operator_reason', 'note', 'created_at', false, NULL)
ON CONFLICT (table_name, column_name) DO NOTHING;

-- The erasure paths blank `reason` on this table with a plain UPDATE. This follows that statement: whenever the scrub is
-- on and `reason` is being replaced, operator_reason goes with it. It sorts before the scrub guard (zz_scrub_guard), which
-- then sees only registered columns changed.
CREATE OR REPLACE FUNCTION iam_v2.entitlement_transfers_scrub_operator_reason() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF NEW.reason IS DISTINCT FROM OLD.reason AND NEW.operator_reason IS NOT NULL
     AND NEW.operator_reason <> 'Removed after the staff-notes period' THEN
    NEW.operator_reason := 'Removed after the staff-notes period';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS zy_scrub_operator_reason ON iam_v2.entitlement_transfers;
CREATE TRIGGER zy_scrub_operator_reason BEFORE UPDATE ON iam_v2.entitlement_transfers
    FOR EACH ROW WHEN (current_setting('iam_v2.scrub', true) = 'on')
    EXECUTE FUNCTION iam_v2.entitlement_transfers_scrub_operator_reason();

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.entitlement_transfers_scrub_operator_reason() OWNER TO iam_v2_owner';
  END IF;
END $own$;
