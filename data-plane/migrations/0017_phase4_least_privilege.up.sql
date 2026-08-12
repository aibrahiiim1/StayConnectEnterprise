-- 0017 — PHASE-4 LEAST-PRIVILEGE FINANCIAL MUTATION BOUNDARY
--
-- Every financial rule Phase 4 has built so far is enforced by triggers and controlled functions. That is
-- necessary and not sufficient: a trigger constrains HOW a row changes, while a GRANT decides WHO may
-- attempt the change at all. Without the second, a compromised or merely buggy service can still write a
-- CAPTURED status, a SETTLED settlement or a callback event directly, and the only thing standing between
-- it and free money is that the triggers happen to be comprehensive.
--
-- This migration creates the real PostgreSQL roles and grants the minimum each one needs.
--
--   sc_payment_runtime     the online-payment service. It may CREATE intents and CALL the two controlled
--                          functions. It may NOT move a status, settle a settlement, write a callback
--                          event, create an entitlement, or touch the review ledger.
--   sc_financial_operator  the Manual Review operator surface. It may READ the financial record and CALL
--                          the controlled review function. It may NOT write any financial row directly.
--   sc_financial_readonly  reporting. SELECT and nothing else, ever.
--
-- WHAT THIS DOES NOT YET DO, stated plainly rather than implied: the appliance's runtime DSN still connects
-- as the schema owner. Switching the payment service to sc_payment_runtime additionally requires the
-- Phase-2 entitlement grant to be reachable from a non-owner role, and the entitlement writer is a direct
-- INSERT today. The roles below are created, granted and PROVEN at the database boundary; the connection
-- change is a deployment step that is deliberately not taken while Phase 4 is DARK.
--
-- Roles are cluster-wide, so this uses DO blocks: a role that already exists is not an error.

BEGIN;

DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='sc_payment_runtime') THEN
    CREATE ROLE sc_payment_runtime NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='sc_financial_operator') THEN
    CREATE ROLE sc_financial_operator NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='sc_financial_readonly') THEN
    CREATE ROLE sc_financial_readonly NOLOGIN;
  END IF;
END $$;

-- Start from nothing. A grant that arrives by inheritance from PUBLIC is not least privilege.
REVOKE ALL ON SCHEMA iam_v2 FROM sc_payment_runtime, sc_financial_operator, sc_financial_readonly;
GRANT USAGE ON SCHEMA iam_v2 TO sc_payment_runtime, sc_financial_operator, sc_financial_readonly;

-- ---------------------------------------------------------------- sc_payment_runtime
--
-- INSERT on payment_transactions is the one direct write it holds, and it is safe because the durable
-- intent is exactly the row a payment service must be able to create before it does anything else: the
-- admission gate, the amount pin and the status machine all constrain what that INSERT may contain.
--
-- There is deliberately NO UPDATE. Every status movement goes through begin_payment_execution or
-- apply_payment_callback_v2, which are SECURITY DEFINER and carry the settlement with them, so the runtime
-- cannot move a payment without moving the money it belongs to.
GRANT SELECT, INSERT ON iam_v2.payment_transactions TO sc_payment_runtime;
GRANT SELECT ON iam_v2.settlements, iam_v2.purchases, iam_v2.offer_quotes, iam_v2.auth_contexts,
                iam_v2.payment_transaction_events
  TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.begin_payment_execution(uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION
  iam_v2.apply_payment_callback_v2(uuid,text,uuid,text,text,text,text,text,jsonb) TO sc_payment_runtime;

-- ---------------------------------------------------------------- sc_financial_operator
GRANT SELECT ON iam_v2.payment_transactions, iam_v2.payment_transaction_events, iam_v2.settlements,
                iam_v2.purchases, iam_v2.pms_postings, iam_v2.posting_outbox,
                iam_v2.posting_review_actions, iam_v2.posting_review_state
  TO sc_financial_operator;

-- ---------------------------------------------------------------- sc_financial_readonly
GRANT SELECT ON ALL TABLES IN SCHEMA iam_v2 TO sc_financial_readonly;

-- The controlled review function, if this chain carries it. It is named defensively because the review
-- surface landed in an earlier migration and a missing function must not fail the whole boundary.
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
            WHERE n.nspname='iam_v2' AND p.proname IN ('apply_financial_review_action','p4_apply_review_action')
  LOOP
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO sc_financial_operator', r.sig);
  END LOOP;
END $$;

-- ---------------------------------------------------------------- the explicit denials
--
-- These REVOKEs are redundant against a fresh database, where nothing was granted in the first place. They
-- are written anyway because this file is the readable statement of the boundary: someone auditing it
-- should see the prohibitions named, not have to infer them from what is absent. They also matter on a
-- cluster where an earlier grant existed.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON iam_v2.entitlements FROM
  sc_payment_runtime, sc_financial_operator, sc_financial_readonly;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON iam_v2.settlements FROM
  sc_payment_runtime, sc_financial_operator, sc_financial_readonly;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON iam_v2.payment_transaction_events FROM
  sc_payment_runtime, sc_financial_operator, sc_financial_readonly;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON iam_v2.posting_review_actions, iam_v2.posting_review_state FROM
  sc_payment_runtime, sc_financial_operator, sc_financial_readonly;
REVOKE UPDATE, DELETE, TRUNCATE ON iam_v2.payment_transactions FROM
  sc_payment_runtime, sc_financial_operator, sc_financial_readonly;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON iam_v2.pms_postings, iam_v2.posting_outbox FROM
  sc_payment_runtime, sc_financial_operator, sc_financial_readonly;

-- A SECURITY DEFINER function is a privilege escalation by design, so PUBLIC must never hold EXECUTE on
-- one: a grant to PUBLIC would hand every role in the cluster the owner's rights over money. Re-asserted
-- here for the whole Phase-4 definer set, because a later CREATE OR REPLACE restores the default grant.
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
            WHERE n.nspname='iam_v2' AND p.prosecdef
  LOOP
    EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', r.sig);
  END LOOP;
END $$;
-- and re-grant the two the runtime legitimately needs, since the sweep above just removed everything
GRANT EXECUTE ON FUNCTION iam_v2.begin_payment_execution(uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION
  iam_v2.apply_payment_callback_v2(uuid,text,uuid,text,text,text,text,text,jsonb) TO sc_payment_runtime;

INSERT INTO public.schema_migrations (version) VALUES ('0017_phase4_least_privilege') ON CONFLICT DO NOTHING;

COMMIT;
