-- svc_pmsd: the Phase-3 PMS CONNECTOR + STAY INGEST boundary.
--
-- pmsd is the single owner of one PMS Interface socket, read-only towards the PMS, and the only writer of
-- the durable Stay inbox and the Stay projection. This file gives it exactly the iam_v2 surface the
-- implemented code touches and nothing else. It is a sibling of svc-scd-iamv2-guest-auth-grants.sql and
-- svc-netd-iamv2-enforcement-grants.sql, not part of gatep-grants.sql: that file's reconciler preamble
-- REVOKEs ALL on iam_v2 for every svc_* role as its first act, so an iam_v2 grant written there would be
-- revoked by the next Gate-P run. iam_v2 privilege lives in the per-service files, applied after it.
--
-- HOW THE SURFACE WAS DERIVED: from the non-test sources of internal/pmsd, internal/stayengine and
-- internal/checkout — every `INSERT INTO / UPDATE / DELETE FROM / FROM / JOIN iam_v2.x` and every
-- `SELECT iam_v2.f(...)` those three packages actually issue. Test fixtures were excluded deliberately;
-- they write tables (accounting_records, devices, service_plans, pms_interfaces) that the production path
-- never writes, and granting from a grep that included them is how a connector ends up holding the
-- financial privileges this file exists to withhold.
--
-- WHAT IS DELIBERATELY ABSENT, and must stay absent:
--   accounting_records, accounting_checkpoints, delayed_accounting_records  — metered usage
--   pms_postings, posting_attempts, posting_execution_state, posting_review_*  — PMS financial posting
--   payments, settlements, offer_quotes                                     — payment / settlement / quoting
--   INSERT/UPDATE on pms_interfaces and pms_interface_revisions             — interface AUTHORING is edged's
-- The connector reads its configuration and cannot author it, and it cannot post a charge, move money or
-- write a usage record. A revision published with folio_identity_strategy=UNSET already makes posting
-- impossible in the product; this makes it impossible in the database as well, which is the half that still
-- holds if the product is wrong.

\set ON_ERROR_STOP on

GRANT USAGE ON SCHEMA iam_v2 TO svc_pmsd;

-- ---------------------------------------------------------------------------
-- CONNECTOR: interface identity, published revision, secret generation, runtime axes.
-- ---------------------------------------------------------------------------
-- Read-only. pmsd resolves which interfaces it owns and how to dial them; it never authors them.
GRANT SELECT               ON iam_v2.pms_interfaces                    TO svc_pmsd;
GRANT SELECT               ON iam_v2.pms_interface_revisions           TO svc_pmsd;
GRANT SELECT               ON iam_v2.pms_interface_secret_generations  TO svc_pmsd;

-- The per-interface freshness axes (link, feed, complete-sync) are pmsd's own state and its alone. Written
-- as an upsert with compare-and-set, so INSERT and UPDATE are both required: `ON CONFLICT DO UPDATE` needs
-- UPDATE at plan time whether or not any row actually conflicts, which is a privilege check that has bitten
-- this project twice before (svc_netd/network_interfaces, svc_edged/sync_checkpoints).
GRANT SELECT,INSERT,UPDATE ON iam_v2.pms_interface_runtime             TO svc_pmsd;

-- ---------------------------------------------------------------------------
-- STAY INGEST: the durable inbox and the projection it applies into.
-- ---------------------------------------------------------------------------
-- stays and stay_events are the capability-scoped `stay` family: the table privilege below is necessary but
-- NOT sufficient, because p3_stay_controlled_writer refuses any write not made inside a declared
-- iam_v2.begin_controlled_operation('stay') scope. Both halves are granted here on purpose — a role holding
-- the table grant without the opener could not write a single row, and the resulting "permission denied"
-- would point at the wrong thing entirely.
GRANT SELECT,INSERT,UPDATE ON iam_v2.stay_events                       TO svc_pmsd;
GRANT SELECT,INSERT,UPDATE ON iam_v2.stays                             TO svc_pmsd;
-- The Stay's identity rows. Unguarded tables, so the table privilege is the whole boundary.
GRANT SELECT,INSERT,UPDATE ON iam_v2.stay_guests                       TO svc_pmsd;
GRANT SELECT,INSERT,UPDATE ON iam_v2.stay_folios                       TO svc_pmsd;
-- folios: INSERT and SELECT only. A folio is created as an IDENTITY for a Stay, never amended by the
-- connector — there is no read-only PMS event that legitimately rewrites one, so no UPDATE.
GRANT SELECT,INSERT        ON iam_v2.folios                            TO svc_pmsd;

-- ---------------------------------------------------------------------------
-- CHECKOUT CONVERSION (GO events only), gated by STAYCONNECT_PHASE3_CHECKOUT_GRACE.
-- ---------------------------------------------------------------------------
-- WHY A READ-ONLY CONNECTOR HOLDS THESE AT ALL: a GO applies the Stay checkout and its conversion in ONE
-- transaction. stayengine refuses to split them (ErrCheckoutConverterRequired) precisely so that a checked-out
-- Stay and the entitlement boundary that follows from it can never disagree, and there is no Stay-only
-- checkout path to fall back to. So these writes are not a second capability bolted onto ingest — they ARE
-- what ingesting a checkout means.
--
-- With the flag OFF, cmd/pmsd wires no Converter and a GO is refused rather than converted, so this privilege
-- set is inert: it can only be exercised by a build whose Checkout Grace flag is genuinely on. That is the
-- property that makes it safe to hold, and it is a real property of the code rather than a convention —
-- see the gating comment in cmd/pmsd/main.go.
--
-- None of it is financial execution. The conversion ENDS a paid entitlement at the checkout boundary and may
-- create a free grace entitlement in its place; it takes no payment, posts no charge and writes no usage.
-- entitlements: SELECT + INSERT the grace entitlement, and UPDATE for a row LOCK.
--
-- The eligibility scan locks the Stay's pre-checkout entitlements with `SELECT ... FOR UPDATE` so a
-- conversion cannot race another writer, and PostgreSQL requires UPDATE privilege to take that lock. The
-- converter issues no UPDATE against this table — the boundary is moved by
-- iam_v2.terminate_entitlement_at_boundary and the status by apply_entitlement_transition, both granted
-- below — and p3_entitlement_controlled_writer refuses any status change from a non-owner regardless.
--
-- This and site_checkout_grace_config are the two places where UPDATE means "may lock", not "may write".
-- Both were missed by the pre-flight rehearsal because it issued plain SELECTs; both then failed on real
-- checkouts, mid-transaction, leaving the GO PENDING and the ordered stream stalled behind it.
GRANT SELECT,INSERT,UPDATE ON iam_v2.entitlements                      TO svc_pmsd;
GRANT SELECT,INSERT,UPDATE ON iam_v2.entitlement_devices               TO svc_pmsd;
GRANT SELECT,INSERT,UPDATE ON iam_v2.entitlement_device_authorizations TO svc_pmsd; -- capability-scoped: device_auth
GRANT SELECT,INSERT        ON iam_v2.entitlement_boundary_watermarks   TO svc_pmsd; -- capability-scoped: checkout_conversion
GRANT SELECT,INSERT        ON iam_v2.checkout_grace_audit              TO svc_pmsd; -- capability-scoped: checkout_conversion
GRANT SELECT,INSERT        ON iam_v2.purchases                         TO svc_pmsd; -- capability-scoped: commerce_intent
-- On the UPDATE that IS present above: it exists solely so `SELECT ... FOR UPDATE` can take a row lock, and
-- it is not a licence to mutate. The converter issues no UPDATE statement against iam_v2.entitlements; status
-- is operation-owned and moves only through apply_entitlement_transition, and p3_entitlement_controlled_writer
-- refuses a status change from any caller that is not the approved writer's owner — grant or no grant.

-- sessions: SELECT and UPDATE, and the UPDATE is narrower than it looks. p3_session_usage_controlled_writer
-- guards the accounting columns (bytes_up/bytes_down/ip/ingress_interface) and the PENDING_ENFORCEMENT
-- lifecycle; the conversion writes state/ended/end_reason on an already-active session, which the guard
-- classifies as unchanged and permits. A pmsd that tried to move usage totals or promote a session out of
-- PENDING_ENFORCEMENT would still be refused by the trigger, holding this grant or not.
GRANT SELECT,UPDATE        ON iam_v2.sessions                          TO svc_pmsd;

-- Read-only inputs the conversion consults to decide the boundary and what grace, if any, it may grant.
-- site_checkout_grace_config: SELECT **and UPDATE**, and the UPDATE is a row LOCK, not a licence to write.
--
-- The conversion reads this row with `SELECT ... FOR UPDATE`, to serialise a checkout against a concurrent
-- grace-policy publication. PostgreSQL requires UPDATE privilege to take that lock even though the statement
-- modifies nothing, so SELECT alone fails with "permission denied for table site_checkout_grace_config" —
-- mid-transaction, which aborts the conversion and leaves the GO PENDING while the ordered per-interface
-- stream backs up behind it. That is what happened on the live appliance to five real checkouts.
--
-- Granting UPDATE does NOT make svc_pmsd a writer of grace policy. p3_grace_config_controlled_writer refuses
-- any actual column change from a caller that is not the owner of publish_checkout_grace_config, and taking a
-- row lock does not fire it. The privilege buys the lock; the trigger keeps the boundary.
--
-- Worth recording how it was missed: the pre-flight rehearsal ran `SELECT ... FROM site_checkout_grace_config`
-- without `FOR UPDATE`, so it passed. A rehearsal has to issue the statement the code issues, lock clauses
-- included, or it proves something adjacent to the thing you need.
GRANT SELECT,UPDATE ON iam_v2.site_checkout_grace_config TO svc_pmsd;
GRANT SELECT ON iam_v2.entitlement_state_transitions  TO svc_pmsd;
GRANT SELECT ON iam_v2.internet_packages              TO svc_pmsd;
GRANT SELECT ON iam_v2.internet_package_revisions     TO svc_pmsd;
GRANT SELECT ON iam_v2.service_plan_revisions         TO svc_pmsd;
-- service_plans is read INSIDE iam_v2.emergency_grace_health, which is a plain SQL function and therefore
-- runs as the CALLER. Without this the health probe fails with "permission denied for table service_plans"
-- from inside readEmergencyCatalog — and because that call happens in the middle of the checkout transaction,
-- the failure is not a graceful "no grace available": it aborts the conversion, the GO is never applied, and
-- the durable inbox stalls behind an event that will fail again on every retry. That is precisely the wedge
-- the Checkout-Grace preflight exists to prevent, so the privilege that avoids it belongs here.
--
-- Found by rehearsing the converter's exact statement sequence as svc_pmsd inside a rolled-back transaction,
-- before any real guest checkout could hit it.
GRANT SELECT ON iam_v2.service_plans                  TO svc_pmsd;

-- ---------------------------------------------------------------------------
-- Controlled-writer functions. Signatures taken from pg_proc on the live schema, not written from memory:
-- a wrong signature makes GRANT fail with "function does not exist", and under ON_ERROR_STOP that aborts
-- every grant after it — the failure mode this project already hit once on svc_netd.
-- ---------------------------------------------------------------------------
-- The capability-scope opener. ONE grant covers every capability-scoped family, because they all open through
-- this same function. writerguard.Verify checks EXECUTE on it at startup for each family in turn, so without
-- this grant pmsd refuses to start rather than discovering mid-ingest that it cannot write a Stay.
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) TO svc_pmsd;

-- ...and the guard's own question-answerer. p3_controlled_operation_open is what the trigger CALLS, running
-- as whichever role is attempting the write, so the writing role needs EXECUTE on it or the guard fails with
-- "permission denied for function p3_controlled_operation_open" instead of its explanatory refusal — and it
-- fails that way even for a write that has legitimately opened its scope.
--
-- Migration 0010 created it with PUBLIC EXECUTE deliberately, for exactly that reason. That PUBLIC grant has
-- since been revoked on the live cluster and replaced by per-role grants (svc_scd, svc_netd, svc_acctd each
-- hold it individually), so a new writer must be added explicitly. Granting it is not a privilege in any
-- meaningful sense: all a caller learns is whether IT has an open scope, in ITS OWN transaction, for a token
-- IT set.
GRANT EXECUTE ON FUNCTION iam_v2.p3_controlled_operation_open(text) TO svc_pmsd;

-- Entitlement status is operation-owned: the conversion activates the grace entitlement through the approved
-- transition rather than by a raw UPDATE. The UPDATE privilege granted above is for the eligibility scan's
-- row lock only, so this function — not that grant — remains the sole way the status moves.
GRANT EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text) TO svc_pmsd;

-- Ending the pre-checkout entitlement at the boundary, and moving any live session onto the grace
-- entitlement. Both are multi-statement invariants owned by the schema, not by the connector.
GRANT EXECUTE ON FUNCTION iam_v2.terminate_entitlement_at_boundary(uuid, timestamptz, text) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.rebind_session_entitlement(uuid, uuid, timestamptz)        TO svc_pmsd;

-- Grace-policy validation and the emergency-grace health probe: the conversion asks whether the configured
-- grace package still matches policy BEFORE it acts on it, so a misconfigured site degrades deliberately
-- instead of wedging the ingest pipeline behind a checkout it cannot complete.
GRANT EXECUTE ON FUNCTION iam_v2.grace_package_matches_policy(
  uuid, uuid, uuid, integer, integer, integer, bigint, integer, text) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.grace_package_mismatch_reason(
  uuid, uuid, uuid, integer, integer, integer, bigint, integer, text) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.emergency_grace_health(uuid, uuid)   TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO svc_pmsd;

-- ---------------------------------------------------------------------------
-- Sequence USAGE, only for tables svc_pmsd INSERTs into and only where the default is a sequence.
-- ---------------------------------------------------------------------------
DO $$
DECLARE s record;
BEGIN
  FOR s IN
    SELECT DISTINCT quote_ident(sn.nspname)||'.'||quote_ident(sc.relname) AS seq
      FROM pg_depend d
      JOIN pg_class sc ON sc.oid = d.objid AND sc.relkind = 'S'
      JOIN pg_namespace sn ON sn.oid = sc.relnamespace
      JOIN pg_class tc ON tc.oid = d.refobjid
      JOIN pg_namespace tn ON tn.oid = tc.relnamespace
     WHERE d.deptype IN ('a','i')
       AND tn.nspname = 'iam_v2'
       AND tc.relname IN ('stay_events','stays','stay_guests','stay_folios','folios',
                          'pms_interface_runtime','entitlements','entitlement_devices',
                          'entitlement_device_authorizations','entitlement_boundary_watermarks',
                          'purchases','checkout_grace_audit')
  LOOP
    EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %s TO svc_pmsd', s.seq);
  END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- FAIL CLOSED: assert the financial surface was not granted. This runs last so that an over-grant added to
-- this file later — or applied by hand and then "made permanent" by copying it in — stops the deployment
-- instead of shipping. A boundary that is only documented is a boundary that erodes.
-- ---------------------------------------------------------------------------
DO $$
DECLARE bad text;
BEGIN
  SELECT string_agg(DISTINCT table_name||':'||privilege_type, ', ')
    INTO bad
    FROM information_schema.role_table_grants
   WHERE grantee = 'svc_pmsd'
     AND table_schema = 'iam_v2'
     AND (
       table_name IN ('accounting_records','accounting_checkpoints','delayed_accounting_records',
                      'pms_postings','posting_attempts','posting_execution_state','posting_review_state',
                      'posting_review_actions','payments','settlements','offer_quotes')
       OR (table_name IN ('pms_interfaces','pms_interface_revisions')
           AND privilege_type IN ('INSERT','UPDATE','DELETE'))
     );
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'GATE-P BLOCKER: svc_pmsd holds prohibited financial/authoring privilege: %', bad;
  END IF;
END $$;
