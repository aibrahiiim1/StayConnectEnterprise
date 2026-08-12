-- 0013 DOWN — reverse of 0013_phase4_reversal_ledger.up.sql. Reverses ONLY 0013.
--
-- Rolling back restores the 0012 posture, which REFUSED reversal ledger rows outright. That is a more
-- restrictive state, not a less safe one: it cannot produce financial traffic, it can only prevent an audit
-- record from being written. Any reversal rows created while 0013 was applied are left in place — they are
-- append-only ledger history and deleting them to satisfy a rollback would be destroying evidence.
BEGIN;

DELETE FROM public.schema_migrations WHERE version = '0013_phase4_reversal_ledger';

DROP TRIGGER IF EXISTS p4_reversal_never_attempted ON iam_v2.posting_attempts;
DROP TRIGGER IF EXISTS p4_reversal_never_queued ON iam_v2.posting_outbox;
DROP FUNCTION IF EXISTS iam_v2.p4_reversal_never_executes();
DROP TRIGGER IF EXISTS p4_reversal_ledger_guard ON iam_v2.pms_postings;
DROP FUNCTION IF EXISTS iam_v2.p4_reversal_ledger_guard();

DROP FUNCTION IF EXISTS iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int,bigint);

ALTER TABLE iam_v2.posting_review_state
  DROP CONSTRAINT IF EXISTS prs_reversal_needs_action,
  DROP COLUMN IF EXISTS reversal_posting_id;

-- restore the 0012 blanket refusal
CREATE OR REPLACE FUNCTION iam_v2.p4_no_programmatic_reversal() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF NEW.posting_type = 'REVERSAL' THEN
    RAISE EXCEPTION 'PROGRAMMATIC_REVERSAL_DISABLED: reversal is a manual, out-of-band correction in v1 '
                    '(capability = false); no reversal posting may be created programmatically'
      USING ERRCODE = 'feature_not_supported';
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p4_no_programmatic_reversal
  BEFORE INSERT ON iam_v2.pms_postings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_no_programmatic_reversal();

COMMIT;
