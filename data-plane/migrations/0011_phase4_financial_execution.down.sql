-- 0011 DOWN — reverse of 0011_phase4_financial_execution.up.sql. Reverses ONLY 0011.
--
-- Nothing here touches mg7/mg9/0010: charge_gate, pa_oneway, ao_postings, ao_review, ao_pa_events,
-- outbox_one_active, the idempotency_key uniqueness and the 0010 stay/controlled-writer guards are not
-- named in this file and are left exactly as they were. Dropped in reverse dependency order: read model
-- first (it reads the new state table), then triggers and their functions, then the new table, then the
-- constraints and columns.
BEGIN;

DELETE FROM public.schema_migrations WHERE version = '0011_phase4_financial_execution';

-- (G3) derived read model — depends on posting_review_state, so it goes first
DROP VIEW IF EXISTS iam_v2.posting_execution_state;

-- (UNKNOWN) retry gate + its supporting index
DROP TRIGGER IF EXISTS p4_attempt_retry_gate ON iam_v2.posting_attempts;
DROP FUNCTION IF EXISTS iam_v2.p4_attempt_retry_gate();
DROP INDEX IF EXISTS iam_v2.posting_attempts_by_posting;

-- (P#) allocator
DROP FUNCTION IF EXISTS iam_v2.allocate_p_number(uuid,uuid,uuid);

-- (C21) review writer + concurrency state
DROP FUNCTION IF EXISTS iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int);
DROP TRIGGER IF EXISTS p4_review_writer_only ON iam_v2.posting_review_actions;
DROP FUNCTION IF EXISTS iam_v2.p4_review_writer_only();
DROP TABLE IF EXISTS iam_v2.posting_review_state;
DROP FUNCTION IF EXISTS iam_v2.ns_financial_review(text);

-- (G2) currency gate, then the columns it read
DROP TRIGGER IF EXISTS p4_posting_currency_gate ON iam_v2.pms_postings;
DROP FUNCTION IF EXISTS iam_v2.p4_posting_currency_gate();
ALTER TABLE iam_v2.pms_interface_revisions
  DROP CONSTRAINT IF EXISTS pmsrev_financial_currency_exponent_range,
  DROP CONSTRAINT IF EXISTS pmsrev_financial_currency_iso,
  DROP CONSTRAINT IF EXISTS pmsrev_financial_currency_pair;
ALTER TABLE iam_v2.pms_interface_revisions
  DROP COLUMN IF EXISTS financial_base_currency_exponent,
  DROP COLUMN IF EXISTS financial_base_currency;

-- (G1) verified RN + G#
ALTER TABLE iam_v2.posting_attempts
  DROP CONSTRAINT IF EXISTS attempt_pnumber_wire_safe,
  DROP CONSTRAINT IF EXISTS attempt_gnumber_wire_safe,
  DROP CONSTRAINT IF EXISTS attempt_rn_wire_safe,
  DROP CONSTRAINT IF EXISTS attempt_gnumber_verified,
  DROP CONSTRAINT IF EXISTS attempt_rn_verified;

COMMIT;
