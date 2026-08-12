-- 0011 — Phase 4 (Financial Execution Core) additive iam_v2 hardening. DARK. No data.
-- Authorization: D18 / T0029. Target maturity: DARK / NO-FINANCIAL-TRAFFIC. All Phase-4 flags remain OFF.
--
-- SCOPE DISCIPLINE. This migration contains ONLY the gaps that were MEASURED against the authoritative
-- pre-0011 chain in docs/architecture/Phase4-Financial-Schema-Gap-Audit.md. It does not create, rename,
-- replace or weaken any enforcement already proven there: mg9's charge_gate, pa_oneway, ao_postings,
-- ao_review, ao_pa_events and trg_secret_gen_guard, mg7's outbox_one_active and idempotency_key uniqueness,
-- and 0010's stay lifecycle guard and controlled-writer boundary are all left EXACTLY as they are. Every
-- new rule below is an ADDITIONAL trigger or constraint that runs alongside them.
--
-- Trigger firing order matters and is deliberate. PostgreSQL fires BEFORE ROW triggers in name order, so on
-- iam_v2.pms_postings the existing `charge_gate` (folio-strategy + IN_HOUSE) still runs BEFORE the new
-- `p4_posting_currency_gate`. The fail-closed reason an operator sees for an un-onboarded interface is
-- therefore unchanged.
--
-- Adds ONLY:
--   (G1) posting_attempts: RN and G# are mandatory, non-blank and wire-safe on every attempt. Room Number
--        stays EVIDENCE — nothing here makes it an identity (no unique index, no key, no lookup path).
--   (G2) pms_interface_revisions.financial_base_currency + _exponent: the authoritative per-property
--        financial currency, pinned to an IMMUTABLE revision (Tier-2 financial onboarding). NULL means
--        "not financially onboarded" and is FAIL-CLOSED. Nothing is defaulted or invented. A posting is
--        refused unless its own currency, its Purchase's currency and its Package Revision's currency all
--        equal the pinned interface currency EXACTLY, exponent included. No implicit FX anywhere.
--   (G3) iam_v2.posting_execution_state: a DERIVED read model. No mutable duplicated financial state and
--        no second writer — every column is computed from the durable posting, outbox, attempt and review
--        ledgers, and the execution state is taken from the single highest-numbered attempt so that its
--        precedence is unambiguous.
--   (C21) posting_review_state + record_posting_review_action(): DB-backed atomic reviewer concurrency.
--        Two concurrent reviewers cannot both commit incompatible terminal decisions. The append-only
--        review ledger is preserved untouched; the new row carries only the decision pointer and version.
--   (P#)  allocate_p_number(): the durable, transactional, per-interface protocol-attempt allocator over
--        the existing pms_interface_pnumber_seq. No epoch or timestamp seeding.
--   (UNKNOWN) p4_attempt_retry_gate: a second attempt after an UNKNOWN or ACKED attempt is STRUCTURALLY
--        refused unless an audited CONFIRM_NOT_POSTED_RETRY authorized exactly that attempt number. Blind
--        retry is not a convention in one caller; it is impossible through this schema.
--
-- No public-schema mutation. Zero runtime grants (dark): every new SECURITY DEFINER function has a fixed
-- search_path, no dynamic SQL, and EXECUTE revoked from PUBLIC with NO per-service grant — those land at
-- Gate-P, so Phase 4 keeps the Phase-3 zero-runtime-privilege invariant.
BEGIN;

-- ============================================================================
-- (G1) Verified RN + G# on every posting attempt.
-- ============================================================================
-- A financially executable attempt without a verified room number and guest number is not executable at
-- all: the PMS has nothing to target. mg7 left both columns nullable and zero CHECKs mentioned g_number.
--
-- Wire safety is part of the same invariant. FIAS is a pipe-delimited, STX/ETX-framed protocol, so a value
-- containing '|', STX, ETX or any other control character would not be the value the PMS receives. Those
-- are rejected here rather than escaped later, because an escaping bug in one sender is a money defect.
ALTER TABLE iam_v2.posting_attempts
  ADD CONSTRAINT attempt_rn_verified CHECK (rn IS NOT NULL AND btrim(rn) <> '' AND length(rn) <= 32),
  ADD CONSTRAINT attempt_gnumber_verified CHECK (g_number IS NOT NULL AND btrim(g_number) <> '' AND length(g_number) <= 32),
  ADD CONSTRAINT attempt_rn_wire_safe CHECK (rn !~ '[\x00-\x1f\x7f|]'),
  ADD CONSTRAINT attempt_gnumber_wire_safe CHECK (g_number !~ '[\x00-\x1f\x7f|]'),
  -- P# is the protocol-attempt reference. It is transmitted, so it is wire-constrained too; it is
  -- deliberately NOT unique per posting (a posting may legitimately carry more than one attempt) and is
  -- never used as business idempotency — pms_postings.idempotency_key is.
  ADD CONSTRAINT attempt_pnumber_wire_safe CHECK (p_number ~ '^[0-9]{1,18}$');

COMMENT ON CONSTRAINT attempt_rn_verified ON iam_v2.posting_attempts IS
  'G1: RN is mandatory financial TARGETING evidence on every attempt. It is evidence, never the posting identity.';
COMMENT ON CONSTRAINT attempt_gnumber_verified ON iam_v2.posting_attempts IS
  'G1: G# is mandatory financial targeting evidence on every attempt.';

-- ============================================================================
-- (G2) Immutable per-interface financial currency + exact currency equality.
-- ============================================================================
-- Where this lives and why: the currency a property posts in is PROPERTY configuration that is pinned for
-- the life of a revision, not guest input and not global site state. pms_interface_revisions is already
-- fully immutable (mg9 imm_pms_rev rejects UPDATE and DELETE), so recording it here makes it historical
-- evidence by construction: onboarding a currency means publishing a NEW revision, and every posting that
-- pinned the old revision keeps pointing at exactly the currency it was authorized under.
--
-- NULL is the un-onboarded state and it is FAIL-CLOSED. There is no default, no USD, no site fallback and
-- no inference from the package: a property that has not been financially onboarded cannot post.
ALTER TABLE iam_v2.pms_interface_revisions
  ADD COLUMN financial_base_currency char(3),
  ADD COLUMN financial_base_currency_exponent smallint;

ALTER TABLE iam_v2.pms_interface_revisions
  -- both or neither: a currency without an exponent cannot be compared to minor units at all
  ADD CONSTRAINT pmsrev_financial_currency_pair
    CHECK ((financial_base_currency IS NULL) = (financial_base_currency_exponent IS NULL)),
  ADD CONSTRAINT pmsrev_financial_currency_iso
    CHECK (financial_base_currency IS NULL OR financial_base_currency ~ '^[A-Z]{3}$'),
  -- ISO 4217 minor-unit exponents in use are 0..4 (e.g. JPY 0, USD 2, BHD 3, CLF 4)
  ADD CONSTRAINT pmsrev_financial_currency_exponent_range
    CHECK (financial_base_currency_exponent IS NULL OR financial_base_currency_exponent BETWEEN 0 AND 4);

COMMENT ON COLUMN iam_v2.pms_interface_revisions.financial_base_currency IS
  'G2/Tier-2: authoritative property financial currency for this revision. NULL = not financially onboarded '
  '(fail-closed: no posting may execute). Immutable with the revision; onboarding publishes a new revision.';
COMMENT ON COLUMN iam_v2.pms_interface_revisions.financial_base_currency_exponent IS
  'G2: ISO-4217 minor-unit exponent for financial_base_currency. Amounts are integer minor units everywhere.';

-- The currency gate. Additive: it runs AFTER mg9's charge_gate (name order c < p) and adds only the
-- currency dimension, so the existing folio-UNSET and IN_HOUSE refusals keep their exact messages.
--
-- It applies to CHARGE and REVERSAL alike. A reversal in a different currency from the charge it reverses
-- would be an implicit conversion, which is precisely what must never happen.
CREATE OR REPLACE FUNCTION iam_v2.p4_posting_currency_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_if_cur char(3); v_if_exp smallint;
  v_pu_cur char(3); v_pu_exp smallint; v_pkg uuid;
  v_pk_cur char(3); v_pk_exp smallint;
BEGIN
  -- (1) the posting must state its own money explicitly. mg7 left both columns nullable.
  IF NEW.currency IS NULL OR NEW.currency_exponent IS NULL THEN
    RAISE EXCEPTION 'POSTING_CURRENCY_UNSET: posting % states no currency/exponent', NEW.id
      USING ERRCODE = 'check_violation';
  END IF;

  -- (2) the PINNED interface revision must be financially onboarded. Note the lookup is composite-pinned:
  -- a revision belonging to another tenant/site/interface simply does not resolve, and NULL fails closed.
  SELECT financial_base_currency, financial_base_currency_exponent
    INTO v_if_cur, v_if_exp
    FROM iam_v2.pms_interface_revisions
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id
     AND pms_interface_id = NEW.pms_interface_id AND id = NEW.posting_interface_revision_id;
  IF v_if_cur IS NULL THEN
    RAISE EXCEPTION 'INTERFACE_CURRENCY_NOT_ONBOARDED: interface % revision % has no financial base currency',
      NEW.pms_interface_id, NEW.posting_interface_revision_id USING ERRCODE = 'check_violation';
  END IF;

  -- (3) posting currency must EQUAL the pinned interface currency, exponent included. Not convertible-to.
  IF NEW.currency <> v_if_cur OR NEW.currency_exponent IS DISTINCT FROM v_if_exp THEN
    RAISE EXCEPTION 'POSTING_CURRENCY_MISMATCH: posting %/% <> pinned interface %/% (no implicit FX)',
      NEW.currency, NEW.currency_exponent, v_if_cur, v_if_exp USING ERRCODE = 'check_violation';
  END IF;

  -- (4) the pinned Purchase must be in the same currency.
  SELECT currency, currency_exponent, package_revision_id
    INTO v_pu_cur, v_pu_exp, v_pkg
    FROM iam_v2.purchases
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.purchase_id;
  IF v_pu_cur IS NULL OR v_pu_exp IS NULL THEN
    RAISE EXCEPTION 'PURCHASE_CURRENCY_UNSET: purchase % states no currency/exponent', NEW.purchase_id
      USING ERRCODE = 'check_violation';
  END IF;
  IF v_pu_cur <> v_if_cur OR v_pu_exp IS DISTINCT FROM v_if_exp THEN
    RAISE EXCEPTION 'PURCHASE_CURRENCY_MISMATCH: purchase %/% <> pinned interface %/% (no implicit FX)',
      v_pu_cur, v_pu_exp, v_if_cur, v_if_exp USING ERRCODE = 'check_violation';
  END IF;

  -- (5) and so must the pinned Package Revision — this is the contract's "Package currency must equal the
  -- pinned PMS Interface currency" requirement, now that there is an interface currency to compare against.
  SELECT currency, currency_exponent INTO v_pk_cur, v_pk_exp
    FROM iam_v2.internet_package_revisions
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = v_pkg;
  IF v_pk_cur IS NULL OR v_pk_exp IS NULL THEN
    RAISE EXCEPTION 'PACKAGE_CURRENCY_UNSET: package revision % states no currency/exponent', v_pkg
      USING ERRCODE = 'check_violation';
  END IF;
  IF v_pk_cur <> v_if_cur OR v_pk_exp IS DISTINCT FROM v_if_exp THEN
    RAISE EXCEPTION 'PACKAGE_CURRENCY_MISMATCH: package %/% <> pinned interface %/% (no implicit FX)',
      v_pk_cur, v_pk_exp, v_if_cur, v_if_exp USING ERRCODE = 'check_violation';
  END IF;

  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_posting_currency_gate
  BEFORE INSERT ON iam_v2.pms_postings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_posting_currency_gate();

-- ============================================================================
-- (C21) Reviewer concurrency — atomic, DB-backed, append-only ledger preserved.
-- ============================================================================
-- posting_review_actions is and stays fully append-only (mg9 ao_review). It records WHAT was decided; it
-- cannot by itself stop two reviewers deciding incompatible things at the same instant, because two
-- concurrent INSERTs into an append-only table both succeed.
--
-- posting_review_state is the single mutable decision pointer per posting. Every review action goes through
-- record_posting_review_action(), which serializes on this row, so the incompatible second decision sees
-- the first one and is refused. A version column alone would not be evidence of anything; the row lock is.
CREATE TABLE iam_v2.posting_review_state (
  posting_id     uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  review_version int  NOT NULL DEFAULT 0,
  -- the committed TERMINAL decision, if one has been made. ESCALATE is deliberately not terminal.
  terminal_action    text,
  terminal_action_id uuid,
  -- set only by CONFIRM_NOT_POSTED_RETRY: the ONE attempt number that decision authorized.
  retry_authorized_attempt_no int,
  escalation_count int NOT NULL DEFAULT 0,
  decided_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT prs_terminal_catalog CHECK (terminal_action IS NULL OR terminal_action IN
    ('CONFIRM_POSTED','CONFIRM_NOT_POSTED_RETRY','CONFIRM_NOT_POSTED_ABANDON','CREATE_REVERSAL')),
  CONSTRAINT prs_terminal_pair CHECK ((terminal_action IS NULL) = (terminal_action_id IS NULL)),
  CONSTRAINT prs_terminal_decided CHECK ((terminal_action IS NULL) = (decided_at IS NULL)),
  CONSTRAINT prs_retry_only_for_retry CHECK
    (retry_authorized_attempt_no IS NULL OR terminal_action = 'CONFIRM_NOT_POSTED_RETRY'),
  CONSTRAINT prs_version_nonneg CHECK (review_version >= 0),
  FOREIGN KEY (tenant_id, site_id, posting_id) REFERENCES iam_v2.pms_postings (tenant_id, site_id, id));

COMMENT ON TABLE iam_v2.posting_review_state IS
  'C21: one mutable decision pointer per posting. Serializes concurrent financial reviewers. The review '
  'LEDGER (posting_review_actions) remains fully append-only and is the authoritative history.';

-- Lock namespace 41 is reserved for financial review. Namespaces already in use: 0 (stay events),
-- 7 (appliance capacity), 11 (device slot). Domain separation is the whole point of the constant.
CREATE OR REPLACE FUNCTION iam_v2.ns_financial_review(p text) RETURNS bigint
  LANGUAGE sql IMMUTABLE AS $$ SELECT hashtextextended(p, 41) $$;

-- The structural chokepoint. A direct INSERT into the append-only review ledger is refused unless it comes
-- from inside record_posting_review_action() in THIS transaction. This is a structural guarantee, not an
-- authorization boundary: it holds even for the schema owner, which is exactly what makes it testable in a
-- disposable database where everything runs as one role. Role-based authorization (financial-review RBAC +
-- step-up) is a Gate-P/runtime concern and is layered ON TOP of this, never instead of it.
CREATE OR REPLACE FUNCTION iam_v2.p4_review_writer_only() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_tok text;
BEGIN
  v_tok := current_setting('iam_v2.p4_review_writer', true);
  IF v_tok IS NULL OR v_tok <> txid_current()::text THEN
    RAISE EXCEPTION 'REVIEW_WRITER_ONLY: posting_review_actions is written only by '
                    'iam_v2.record_posting_review_action() (concurrency-safe review boundary)'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_review_writer_only
  BEFORE INSERT ON iam_v2.posting_review_actions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_review_writer_only();

-- The one sanctioned review writer.
--
-- Concurrency model, in order:
--   1. pg_advisory_xact_lock on the posting  — serializes reviewers of the SAME posting for the whole
--      transaction, and (unlike SELECT FOR UPDATE alone) also serializes the create-if-absent race on the
--      state row itself. Different postings never contend.
--   2. upsert + SELECT FOR UPDATE            — the row lock, held to commit.
--   3. optimistic version check              — so a reviewer acting on a stale UI read is refused even when
--                                              the two decisions do not overlap in time.
--   4. compatibility check                   — a second, different terminal decision is refused outright.
CREATE OR REPLACE FUNCTION iam_v2.record_posting_review_action(
  p_posting uuid, p_action text, p_actor uuid, p_reason text,
  p_evidence jsonb DEFAULT '{}'::jsonb, p_expected_version int DEFAULT NULL)
RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_t uuid; v_s uuid; st record; v_action_id uuid; v_next_attempt int; v_attempts int;
BEGIN
  IF p_action NOT IN ('CONFIRM_POSTED','CONFIRM_NOT_POSTED_RETRY','CONFIRM_NOT_POSTED_ABANDON',
                      'CREATE_REVERSAL','ESCALATE') THEN
    RAISE EXCEPTION 'REVIEW_ACTION_UNKNOWN: % is not in the approved review catalog', p_action
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_actor IS NULL OR p_reason IS NULL OR btrim(p_reason) = '' THEN
    RAISE EXCEPTION 'REVIEW_ACTOR_REASON_REQUIRED: every financial review decision is attributable'
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT tenant_id, site_id INTO v_t, v_s FROM iam_v2.pms_postings WHERE id = p_posting;
  IF v_t IS NULL THEN
    RAISE EXCEPTION 'REVIEW_POSTING_UNKNOWN: posting % does not exist', p_posting
      USING ERRCODE = 'foreign_key_violation';
  END IF;

  -- (1) serialize every reviewer of this posting
  PERFORM pg_advisory_xact_lock(iam_v2.ns_financial_review(p_posting::text));

  INSERT INTO iam_v2.posting_review_state (posting_id, tenant_id, site_id)
  VALUES (p_posting, v_t, v_s)
  ON CONFLICT (posting_id) DO NOTHING;

  -- (2) hold the row to commit
  SELECT * INTO st FROM iam_v2.posting_review_state WHERE posting_id = p_posting FOR UPDATE;

  -- (3) optimistic concurrency against the version the reviewer actually saw
  IF p_expected_version IS NOT NULL AND p_expected_version <> st.review_version THEN
    RAISE EXCEPTION 'REVIEW_VERSION_STALE: expected version %, current is %',
      p_expected_version, st.review_version USING ERRCODE = 'serialization_failure';
  END IF;

  -- a decision needs something to decide about
  SELECT count(*) INTO v_attempts FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
  IF v_attempts = 0 AND p_action <> 'ESCALATE' THEN
    RAISE EXCEPTION 'REVIEW_NOT_APPLICABLE: posting % has no transmission attempt to decide about', p_posting
      USING ERRCODE = 'check_violation';
  END IF;

  -- (4) incompatible terminal decisions: first one wins, second one is refused
  IF p_action <> 'ESCALATE' AND st.terminal_action IS NOT NULL THEN
    IF st.terminal_action = p_action THEN
      RAISE EXCEPTION 'REVIEW_ALREADY_DECIDED: posting % is already decided as %', p_posting, st.terminal_action
        USING ERRCODE = 'unique_violation';
    END IF;
    RAISE EXCEPTION 'REVIEW_CONFLICT: posting % is already decided as %; % is incompatible',
      p_posting, st.terminal_action, p_action USING ERRCODE = 'unique_violation';
  END IF;

  -- open the structural writer window for exactly this statement, then close it again
  PERFORM set_config('iam_v2.p4_review_writer', txid_current()::text, true);
  INSERT INTO iam_v2.posting_review_actions (tenant_id, site_id, posting_id, action, actor, reason, evidence)
  VALUES (v_t, v_s, p_posting, p_action, p_actor, p_reason, coalesce(p_evidence, '{}'::jsonb))
  RETURNING id INTO v_action_id;
  PERFORM set_config('iam_v2.p4_review_writer', '', true);

  IF p_action = 'ESCALATE' THEN
    UPDATE iam_v2.posting_review_state
       SET escalation_count = escalation_count + 1, review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  ELSE
    -- CONFIRM_NOT_POSTED_RETRY authorizes exactly ONE further attempt, by number. Nothing else can.
    IF p_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
      SELECT coalesce(max(attempt_no), 0) + 1 INTO v_next_attempt
        FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
    ELSE
      v_next_attempt := NULL;
    END IF;
    UPDATE iam_v2.posting_review_state
       SET terminal_action = p_action, terminal_action_id = v_action_id, decided_at = now(),
           retry_authorized_attempt_no = v_next_attempt,
           review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  END IF;

  RETURN v_action_id;
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int) FROM PUBLIC;

-- ============================================================================
-- (P#) Durable, atomic, per-interface protocol-attempt allocator.
-- ============================================================================
-- P# is a PROTOCOL-ATTEMPT reference, never business idempotency (that is pms_postings.idempotency_key).
-- Allocation is a single UPDATE ... RETURNING against the existing pms_interface_pnumber_seq row, so it is
-- transactional, durable and row-locked: concurrent allocators for the SAME interface serialize on that
-- row and every one of them gets a distinct number, while a different interface touches a different row
-- and is never blocked by it. No epoch, no timestamp, no clock — a restarted process continues from the
-- durable counter, and a rolled-back transaction correctly gives the number back.
CREATE OR REPLACE FUNCTION iam_v2.allocate_p_number(p_tenant uuid, p_site uuid, p_interface uuid)
RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_p bigint;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_interfaces
                  WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_interface) THEN
    RAISE EXCEPTION 'PNUMBER_INTERFACE_UNKNOWN: interface % is not in tenant %/site %',
      p_interface, p_tenant, p_site USING ERRCODE = 'foreign_key_violation';
  END IF;
  INSERT INTO iam_v2.pms_interface_pnumber_seq (tenant_id, site_id, pms_interface_id, next_p_number)
  VALUES (p_tenant, p_site, p_interface, 1)
  ON CONFLICT (pms_interface_id) DO NOTHING;

  UPDATE iam_v2.pms_interface_pnumber_seq
     SET next_p_number = next_p_number + 1
   WHERE pms_interface_id = p_interface
  RETURNING next_p_number - 1 INTO v_p;

  IF v_p IS NULL THEN
    RAISE EXCEPTION 'PNUMBER_ALLOCATION_FAILED: no sequence row for interface %', p_interface
      USING ERRCODE = 'no_data_found';
  END IF;
  RETURN v_p;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.allocate_p_number(uuid,uuid,uuid) FROM PUBLIC;

-- ============================================================================
-- (UNKNOWN) No blind retry. Structurally.
-- ============================================================================
-- The financial safety rule is that a transmitted PS with no conclusively matched PA leaves the attempt
-- UNKNOWN, and that state may only be left through audited manual review. mg9 already makes the outcome
-- one-way and the attempt identity immutable, but nothing stopped a caller from inserting attempt_no+1 and
-- burning a second P# on its own initiative.
--
-- Precedence of the rules below is deliberate:
--   SENDING  -> never a second concurrent attempt at all.
--   UNKNOWN  -> a further attempt requires CONFIRM_NOT_POSTED_RETRY naming exactly this attempt number.
--   ACKED    -> the PMS answered. Retrying is a review decision, not an automatic one.
--   FAILED   -> the PS was NOT transmitted. Retrying is safe and stays automatic; that is the whole
--               difference between FAILED and UNKNOWN, and collapsing them would make UNKNOWN meaningless.
CREATE OR REPLACE FUNCTION iam_v2.p4_attempt_retry_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_last record; v_auth int; v_term text;
BEGIN
  SELECT attempt_no, outcome INTO v_last
    FROM iam_v2.posting_attempts
   WHERE internal_posting_id = NEW.internal_posting_id
   ORDER BY attempt_no DESC LIMIT 1;

  IF v_last IS NULL THEN
    IF NEW.attempt_no <> 1 THEN
      RAISE EXCEPTION 'ATTEMPT_SEQUENCE: first attempt for posting % must be attempt_no 1, got %',
        NEW.internal_posting_id, NEW.attempt_no USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.attempt_no <> v_last.attempt_no + 1 THEN
    RAISE EXCEPTION 'ATTEMPT_SEQUENCE: next attempt for posting % must be %, got %',
      NEW.internal_posting_id, v_last.attempt_no + 1, NEW.attempt_no USING ERRCODE = 'check_violation';
  END IF;

  IF v_last.outcome = 'SENDING' THEN
    RAISE EXCEPTION 'ATTEMPT_IN_FLIGHT: attempt % for posting % is still SENDING; a second concurrent '
                    'attempt would risk a duplicate charge', v_last.attempt_no, NEW.internal_posting_id
      USING ERRCODE = 'check_violation';
  END IF;

  IF v_last.outcome IN ('UNKNOWN','ACKED') THEN
    SELECT terminal_action, retry_authorized_attempt_no INTO v_term, v_auth
      FROM iam_v2.posting_review_state WHERE posting_id = NEW.internal_posting_id;
    IF v_term IS DISTINCT FROM 'CONFIRM_NOT_POSTED_RETRY' OR v_auth IS DISTINCT FROM NEW.attempt_no THEN
      RAISE EXCEPTION 'RETRY_REQUIRES_REVIEW: posting % last attempt is %; attempt % is not authorized by '
                      'an audited CONFIRM_NOT_POSTED_RETRY', NEW.internal_posting_id, v_last.outcome, NEW.attempt_no
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;

  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_attempt_retry_gate
  BEFORE INSERT ON iam_v2.posting_attempts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_attempt_retry_gate();

-- the retry gate and the read model both look up "the latest attempt for this posting"
CREATE INDEX posting_attempts_by_posting
  ON iam_v2.posting_attempts (internal_posting_id, attempt_no DESC);

-- ============================================================================
-- (G3) Derived Posting read model. No stored state, no second writer.
-- ============================================================================
-- Every column below is computed. There is exactly one authoritative financial history — the posting,
-- outbox, attempt and review ledgers — and this view reads it.
--
-- execution_state is taken from the SINGLE highest-numbered attempt, which is what makes its precedence
-- unambiguous: there is never a tie to break and never a rule about which of two states "wins". Anything
-- that cannot be derived that way is exposed as its OWN column (outbox_state, terminal_review_action,
-- has_unknown_history) rather than folded into an invented composite state. In particular there is no
-- generic REVIEWED state: "reviewed" would have to mean at least four different financial situations, and
-- an operator cannot act on a label that does not say which.
CREATE VIEW iam_v2.posting_execution_state AS
SELECT
  p.id                       AS posting_id,
  p.tenant_id,
  p.site_id,
  p.pms_interface_id,
  p.posting_type,
  p.amount_minor,
  p.currency,
  p.currency_exponent,
  p.idempotency_key,
  p.created_at,
  CASE
    WHEN la.attempt_no IS NULL THEN 'NOT_ATTEMPTED'
    WHEN la.outcome = 'SENDING' THEN 'IN_FLIGHT'
    WHEN la.outcome = 'UNKNOWN' THEN 'UNKNOWN'
    WHEN la.outcome = 'FAILED'  THEN 'NOT_SENT'
    WHEN la.outcome = 'ACKED' AND la.pa_as_status = 'OK' THEN 'POSTED'
    WHEN la.outcome = 'ACKED'   THEN 'REJECTED'
  END                        AS execution_state,
  la.attempt_no              AS latest_attempt_no,
  la.p_number                AS latest_p_number,
  la.outcome                 AS latest_attempt_outcome,
  la.pa_as_status            AS latest_pa_as_status,
  ac.attempt_count,
  ac.unknown_attempt_count,
  (ac.unknown_attempt_count > 0)               AS has_unknown_history,
  ob.state                   AS outbox_state,
  rs.terminal_action         AS terminal_review_action,
  rs.review_version,
  rs.escalation_count,
  rs.retry_authorized_attempt_no,
  -- an UNKNOWN attempt that no terminal decision has resolved is the one thing an operator must act on
  (la.outcome = 'UNKNOWN' AND rs.terminal_action IS NULL) AS awaiting_manual_review
FROM iam_v2.pms_postings p
LEFT JOIN LATERAL (
  SELECT a.attempt_no, a.outcome, a.p_number, a.pa_as_status
    FROM iam_v2.posting_attempts a
   WHERE a.internal_posting_id = p.id
   ORDER BY a.attempt_no DESC LIMIT 1) la ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS attempt_count,
         count(*) FILTER (WHERE a.outcome = 'UNKNOWN')::bigint AS unknown_attempt_count
    FROM iam_v2.posting_attempts a
   WHERE a.internal_posting_id = p.id) ac ON true
LEFT JOIN LATERAL (
  SELECT o.state FROM iam_v2.posting_outbox o
   WHERE o.posting_id = p.id AND o.state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY') LIMIT 1) ob ON true
LEFT JOIN iam_v2.posting_review_state rs ON rs.posting_id = p.id;

COMMENT ON VIEW iam_v2.posting_execution_state IS
  'G3: DERIVED posting read model. No stored posting status exists and none is created here — every column '
  'is computed from the posting, outbox, attempt and review ledgers. execution_state comes from the single '
  'highest-numbered attempt so its precedence is unambiguous.';

-- migration ledger (prod parity with 0009/0010; scripts/edge-migrate.sh gates on this)
INSERT INTO public.schema_migrations (version) VALUES ('0011_phase4_financial_execution') ON CONFLICT DO NOTHING;

COMMIT;
