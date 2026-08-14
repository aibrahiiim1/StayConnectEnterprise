-- Reverse 0017. The roles are dropped only after every grant they hold is revoked; a role that still owns a
-- privilege cannot be dropped, and leaving a half-revoked role behind would be worse than leaving it whole.
BEGIN;
DO $$
DECLARE r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['sc_payment_runtime','sc_financial_operator','sc_financial_readonly'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA iam_v2 FROM %I', r);
      EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA iam_v2 FROM %I', r);
      EXECUTE format('REVOKE ALL ON SCHEMA iam_v2 FROM %I', r);
      EXECUTE format('DROP ROLE %I', r);
    END IF;
  END LOOP;
END $$;
DELETE FROM public.schema_migrations WHERE version = '0017_phase4_least_privilege';
COMMIT;
