-- PHASE 6 FOUNDATION — Guest Device Self-Service setting + AGGREGATE_ONLINE_TIME state.
--
-- ADDITIVE ONLY. No existing column changes type, no existing row is rewritten, and no existing behaviour
-- changes when this migration is applied: every object here is either a new table nobody reads yet, or a
-- CHECK that admits a value nothing produces yet. Applying it to a live database is a no-op for every
-- running service.
--
-- WHAT IT ESTABLISHES
--
--   1. iam_v2.appliance_product_settings — the per-appliance, local-first product settings row, FOREIGN
--      KEYED to the enrolled appliance so managed state for a non-existent appliance cannot exist. It is a
--      TYPED table rather than another key in public.tenants.auth_methods or public.appliances.metadata,
--      because those are tenant-scoped and untyped respectively, and a boolean that decides whether a guest
--      can release a device deserves a column a constraint can defend.
--
--   2. iam_v2.appliance_product_setting_changes — append-only audit of every change to that setting, with
--      the actor RESOLVED SERVER-SIDE. A setting that can be flipped without a trace is a setting nobody
--      can investigate; an actor the caller chooses is not an actor at all, so the audit references the
--      authenticated operator record rather than trusting a name in a request body.
--
--   3. iam_v2.session_online_watermarks — the durable per-session "charged through" instant. §6.4 of the
--      FINAL contract makes byte accounting idempotent with a watermark; wall-clock time has no cumulative
--      counter to compare against, so without an equivalent watermark a replayed tick, a delayed tick or a
--      reboot would each double-charge. This is that watermark, deliberately shaped like the byte one.
--
--   4. iam_v2.entitlement_termination_evidence — append-only evidence of WHICH time rule ended an
--      entitlement, WITHOUT touching the contract's terminal_reason set. Aggregate exhaustion terminates
--      with the contract's existing TIME reason; this row is what distinguishes "the week ran out" from
--      "the minutes ran out", and unlike an enum value it carries the budget, the consumption and the
--      crossing instant.
--
-- WHAT IT DELIBERATELY DOES NOT DO
--
--   * It does not add a second window concept. An AGGREGATE_ONLINE_TIME entitlement's outer hard-validity
--     window is the existing entitlements.window_ends_at, stamped once and never moved, exactly as today.
--   * IT DOES NOT WIDEN THE CONTRACT'S terminal_reason SET. An earlier draft added 'AGGREGATE_TIME';
--     that is contract vocabulary, which belongs to the Product Owner, and it has been removed.
--   * It does not touch time_accounting_mode or any package revision. Existing revisions already carry
--     VALIDITY_WINDOW in their immutable snapshots, so they cannot be reinterpreted retroactively — that
--     property comes from immutability that already exists, not from a compatibility branch added here.
--   * It grants nothing to any role. Phase 6 is DARK; privileges are derived from a real audit in M4.
--     Every function it creates IS explicitly revoked from PUBLIC, because a function's ACL starts
--     NULL and NULL means PUBLIC EXECUTE -- revoke first, grant later, to the exact role that
--     needs it and no other.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 0. Scope anchor on the platform's appliance table
-- ---------------------------------------------------------------------------------------------------------
-- A foreign key to public.appliances(id) alone proves the appliance EXISTS. It does not prove the row was
-- filed under the RIGHT tenant and site: a settings row could pair a real appliance with somebody else's
-- scope, and the composite primary key would happily accept it. The anchor makes the whole triple
-- referential, which is the same technique MG-0 uses on public.guest_networks and for the same reason.
--
-- OWNERSHIP IS RECORDED, because `IF NOT EXISTS` creates a hazard on the way back down: if this index (or an
-- equivalent one under another name) already existed on the appliance, an unconditional DROP in the down
-- migration would remove a PLATFORM object that this migration never created. A rollback must reverse what it
-- did and nothing else. The marker is a COMMENT on the index, which needs no new table and is visible to
-- anyone inspecting the catalog.
DO $$
DECLARE existing_name text;
BEGIN
  -- Any UNIQUE index that already provides the (id, tenant_id, site_id) triple is sufficient; the FK does not
  -- care which name it has. Only create one when none exists.
  SELECT c.relname INTO existing_name
    FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_class tb ON tb.oid = i.indrelid
    JOIN pg_namespace n ON n.oid = tb.relnamespace
   WHERE n.nspname = 'public' AND tb.relname = 'appliances' AND i.indisunique
     AND ARRAY(SELECT a.attname::text FROM pg_attribute a
                WHERE a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey::int2[])
                ORDER BY a.attname) = ARRAY['id','site_id','tenant_id']
   LIMIT 1;

  IF existing_name IS NULL THEN
    CREATE UNIQUE INDEX appliances_tsi_anchor ON public.appliances (id, tenant_id, site_id);
    COMMENT ON INDEX public.appliances_tsi_anchor IS 'created by iam_v2 migration 0030_phase6_foundation';
  ELSE
    RAISE NOTICE 'appliance (id,tenant_id,site_id) anchor already exists as %; 0030 will not create or own it',
      existing_name;
  END IF;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Per-appliance product settings (local-first)
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE iam_v2.appliance_product_settings (
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  appliance_id uuid NOT NULL,
  -- Guest Device Self-Service. DEFAULT FALSE is the product decision, expressed where it cannot be
  -- forgotten: a row created by any path that does not mention the column is OFF.
  guest_device_self_service boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id, appliance_id),
  -- THE APPLIANCE MUST EXIST. Without this, a settings row could name any uuid at all, and an operator API
  -- that took an appliance id from a request body would be one typo away from managed state for an appliance
  -- that does not exist -- indistinguishable, afterwards, from the feature being on somewhere real. The
  -- scope is therefore anchored to the enrolled appliance record itself, and the runtime derives the id from
  -- the appliance's own trusted local identity rather than accepting it from a caller.
  CONSTRAINT aps_appliance_must_exist
    FOREIGN KEY (appliance_id, tenant_id, site_id) REFERENCES public.appliances (id, tenant_id, site_id)
    ON DELETE CASCADE
);

COMMENT ON TABLE iam_v2.appliance_product_settings IS
  'Per-appliance product settings, read from the site database on the appliance itself. Local-first by '
  'construction: no Central Control Plane call is on the read path, so an appliance with no uplink answers '
  'exactly as one with an uplink.';
COMMENT ON COLUMN iam_v2.appliance_product_settings.guest_device_self_service IS
  'OFF by default. When OFF the guest device-management capability is not exposed to the guest at all and '
  'ordinary authentication and device-limit behaviour is unchanged. This is the long-term PRODUCT control; '
  'the Phase-6 deployment gate is a separate and additionally required control.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. Append-only audit of setting changes
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE iam_v2.appliance_product_setting_changes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  appliance_id uuid NOT NULL,
  setting_key text NOT NULL CHECK (setting_key IN ('guest_device_self_service')),
  old_value boolean,
  new_value boolean NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now(),
  -- WHO DID IT, AS THE SERVER KNOWS THEM. changed_by_operator_id references the authenticated operator
  -- record; it is the identity the server resolved from the session, never a name a caller supplied. The
  -- FK is what makes that enforceable rather than conventional: a request body cannot invent an operator
  -- that public.operators does not contain.
  changed_by_operator_id uuid NOT NULL REFERENCES public.operators (id),
  -- A human-readable copy for evidence that survives an operator record being renamed. It is derived from
  -- the resolved operator, not from the request, and the CHECK only catches an empty write.
  changed_by text NOT NULL CHECK (length(btrim(changed_by)) > 0),
  change_reason text,
  -- The audit is scoped to a real appliance for the same reason the settings row is.
  CONSTRAINT apsc_appliance_must_exist
    FOREIGN KEY (appliance_id, tenant_id, site_id) REFERENCES public.appliances (id, tenant_id, site_id)
    ON DELETE CASCADE
);
CREATE INDEX aps_changes_lookup
  ON iam_v2.appliance_product_setting_changes (tenant_id, site_id, appliance_id, changed_at DESC);

-- Append-only, enforced rather than promised.
CREATE OR REPLACE FUNCTION iam_v2.p6_setting_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.appliance_product_setting_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_setting_changes_append_only() FROM PUBLIC;

CREATE TRIGGER p6_setting_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.appliance_product_setting_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_setting_changes_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- 3. Durable online-time watermark, shaped like the byte watermark in §6.4
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE iam_v2.session_online_watermarks (
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  session_id uuid PRIMARY KEY,
  -- The instant through which this session's online time has ALREADY been charged to its entitlement.
  -- Every accrual charges (now - accounted_through) and then advances this to the same now, in one
  -- transaction, which is what makes a replayed or delayed tick charge exactly zero.
  accounted_through timestamptz NOT NULL,
  -- What this session has contributed so far. Reconciliation evidence: the entitlement's total must equal
  -- the sum of its sessions' contributions, and a disagreement is a defect rather than a rounding artifact.
  accounted_seconds bigint NOT NULL DEFAULT 0 CHECK (accounted_seconds >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, session_id)
    REFERENCES iam_v2.sessions (tenant_id, site_id, id) ON DELETE CASCADE
);

COMMENT ON TABLE iam_v2.session_online_watermarks IS
  'Per-session durable charged-through instant for AGGREGATE_ONLINE_TIME. Bytes are idempotent because they '
  'are cumulative counters compared against a watermark (contract 6.4); wall-clock time has no counter, so '
  'it needs this watermark or a replayed tick, a delayed tick or a reboot would each double-charge.';

-- A watermark may never move backwards: that would re-charge an interval already charged.
CREATE OR REPLACE FUNCTION iam_v2.p6_online_watermark_monotonic() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.accounted_through < OLD.accounted_through THEN
    RAISE EXCEPTION 'online watermark may not move backwards (% -> %): the interval before it is already charged',
      OLD.accounted_through, NEW.accounted_through USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.accounted_seconds < OLD.accounted_seconds THEN
    RAISE EXCEPTION 'accounted_seconds may not decrease (% -> %): corrections go through entitlement_adjustments',
      OLD.accounted_seconds, NEW.accounted_seconds USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_online_watermark_monotonic() FROM PUBLIC;

CREATE TRIGGER p6_online_watermark_monotonic
  BEFORE UPDATE ON iam_v2.session_online_watermarks
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_online_watermark_monotonic();

-- ---------------------------------------------------------------------------------------------------------
-- 4. Aggregate exhaustion is a TIME termination, evidenced -- NOT a new terminal_reason
-- ---------------------------------------------------------------------------------------------------------
-- THE FINAL CONTRACT'S terminal_reason SET IS LEFT EXACTLY AS IT IS. An earlier draft of this migration added
-- 'AGGREGATE_TIME' to it, which is a change to contract vocabulary, and contract vocabulary is the Product
-- Owner's to define -- not something an implementation may quietly widen because it found the existing words
-- inconvenient.
--
-- The contract already has the right word. AGGREGATE_ONLINE_TIME is a TIME MODE, so exhausting its budget is
-- a TIME termination in the same sense that exhausting a validity window is; §6.1's precedence list is
-- {window end, data cap, hard expiry, checkout, admin}, and aggregate exhaustion belongs INSIDE the first of
-- those rather than beside it.
--
-- Distinguishability is then an EVIDENCE problem, not a vocabulary problem, and evidence is the better
-- answer anyway: an enum value could say "the minutes ran out" but could never say which budget, how much was
-- consumed, or when it crossed. This append-only row says all three.
CREATE TABLE iam_v2.entitlement_termination_evidence (
  entitlement_id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  -- The contract's terminal_reason, copied here so evidence and entitlement can be compared without a join
  -- back through the lifecycle. It is CHECKed against the same set the entitlements table admits, so this
  -- table can never introduce a reason the contract does not define.
  -- CONSTRAINED TO 'TIME'. This table explains a TIME-mode termination and nothing else; a row claiming
  -- DATA, CHECKOUT or any other outcome would be evidence about a termination it does not describe. The
  -- column is kept rather than implied so the evidence can be read without knowing that rule.
  terminal_reason text NOT NULL CHECK (terminal_reason = 'TIME'),
  -- WHICH rule inside that reason ran out. This is implementation detail about the cause, not new contract
  -- vocabulary about the outcome, and the distinction is the whole point of putting it here.
  -- WHICH rule inside TIME ran out. Three, not two: an AGGREGATE_ONLINE_TIME entitlement has TWO terminal
  -- time outcomes and both must be representable -- the budget running out, and the outer calendar window
  -- expiring first while minutes remain. The latter is how an unused package ordinarily ends.
  cause_detail text NOT NULL CHECK (cause_detail IN ('VALIDITY_WINDOW_ELAPSED',
                                                     'AGGREGATE_ONLINE_TIME_EXHAUSTED',
                                                     'AGGREGATE_OUTER_WINDOW_EXPIRED')),
  time_mode text NOT NULL CHECK (time_mode IN ('VALIDITY_WINDOW','AGGREGATE_ONLINE_TIME')),
  -- The numbers that make the claim checkable rather than assertable.
  budget_seconds bigint CHECK (budget_seconds IS NULL OR budget_seconds >= 0),
  consumed_online_seconds bigint CHECK (consumed_online_seconds IS NULL OR consumed_online_seconds >= 0),
  window_ends_at timestamptz,
  terminated_at timestamptz NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, entitlement_id)
    REFERENCES iam_v2.entitlements (tenant_id, site_id, id) ON DELETE CASCADE,
  -- EITHER aggregate outcome must carry the budget and the consumption, because those two numbers are
  -- precisely what distinguishes them: exhaustion has consumed >= budget, outer-window expiry has consumed
  -- < budget and minutes left on the clock. Evidence without the numbers is an assertion.
  CONSTRAINT ete_aggregate_carries_its_budget CHECK (
    cause_detail NOT IN ('AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_OUTER_WINDOW_EXPIRED')
    OR (budget_seconds IS NOT NULL AND consumed_online_seconds IS NOT NULL)),
  -- The cause and the mode must agree in BOTH directions: an aggregate cause cannot describe a
  -- VALIDITY_WINDOW entitlement, and a VALIDITY_WINDOW cause cannot describe an aggregate one.
  CONSTRAINT ete_detail_matches_mode CHECK (
    (cause_detail IN ('AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_OUTER_WINDOW_EXPIRED'))
      = (time_mode = 'AGGREGATE_ONLINE_TIME')),
  -- An outer-window expiry must NAME the window it hit, and must show the budget was NOT exhausted --
  -- otherwise the row cannot be told apart from an exhaustion that happened to be recorded late.
  CONSTRAINT ete_outer_window_is_distinguishable CHECK (
    cause_detail <> 'AGGREGATE_OUTER_WINDOW_EXPIRED'
    OR (window_ends_at IS NOT NULL AND consumed_online_seconds < budget_seconds)),
  -- ...and symmetrically, an exhaustion must show the budget WAS reached.
  CONSTRAINT ete_exhaustion_reached_its_budget CHECK (
    cause_detail <> 'AGGREGATE_ONLINE_TIME_EXHAUSTED'
    OR consumed_online_seconds >= budget_seconds)
);

COMMENT ON TABLE iam_v2.entitlement_termination_evidence IS
  'Why a time-mode entitlement ended, with the numbers. The contract terminal_reason set is unchanged and is '
  'never widened here: an aggregate-budget exhaustion terminates with reason TIME, and THIS row says it was '
  'the online-minute budget rather than the wall-clock window, which budget it was, and what had been '
  'consumed when it crossed.';

-- EVIDENCE MUST DESCRIBE A TERMINATION THAT ACTUALLY HAPPENED, and must agree with it. Without this the
-- table is a place to write claims: a row could exist for a live entitlement, name a reason the entitlement
-- did not terminate with, a time mode it does not have, or an instant it did not end at. The trigger reads
-- the entitlement inside the same transaction, so the evidence and the transition either agree or neither
-- exists.
CREATE OR REPLACE FUNCTION iam_v2.p6_termination_evidence_matches_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE e record; real_budget bigint;
BEGIN
  SELECT status, terminal_reason, terminated_at, time_accounting_mode, window_ends_at,
         consumed_online_seconds, service_plan_revision_id
    INTO e FROM iam_v2.entitlements
   WHERE id = NEW.entitlement_id AND tenant_id = NEW.tenant_id AND site_id = NEW.site_id
   FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'termination evidence for an entitlement that does not exist in this scope (%)',
      NEW.entitlement_id USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF e.status <> 'TERMINATED' THEN
    RAISE EXCEPTION 'termination evidence for entitlement % which is % , not TERMINATED: evidence may not describe a transition that did not occur',
      NEW.entitlement_id, e.status USING ERRCODE = 'restrict_violation';
  END IF;
  IF e.terminal_reason IS DISTINCT FROM NEW.terminal_reason THEN
    RAISE EXCEPTION 'termination evidence claims reason % but the entitlement terminated with %',
      NEW.terminal_reason, e.terminal_reason USING ERRCODE = 'restrict_violation';
  END IF;
  IF e.terminated_at IS DISTINCT FROM NEW.terminated_at THEN
    RAISE EXCEPTION 'termination evidence claims % but the entitlement terminated at %',
      NEW.terminated_at, e.terminated_at USING ERRCODE = 'restrict_violation';
  END IF;
  IF e.time_accounting_mode IS DISTINCT FROM NEW.time_mode THEN
    RAISE EXCEPTION 'termination evidence claims time mode % but the entitlement is %',
      NEW.time_mode, e.time_accounting_mode USING ERRCODE = 'restrict_violation';
  END IF;

  -- THE NUMBERS MUST BE THE REAL ONES, not merely numbers that agree with each other. Everything above this
  -- point makes a row internally consistent, and an internally consistent fabrication is still a fabrication:
  -- a caller could invent a budget and a consumption that satisfy every CHECK and describe a termination that
  -- never had those values. So each is compared against the state the termination actually used -- the
  -- entitlement's own counter, and the budget on the IMMUTABLE plan revision pinned into it at grant time.
  IF NEW.consumed_online_seconds IS NOT NULL
     AND NEW.consumed_online_seconds IS DISTINCT FROM e.consumed_online_seconds THEN
    RAISE EXCEPTION 'termination evidence claims % consumed seconds but the entitlement recorded %',
      NEW.consumed_online_seconds, e.consumed_online_seconds USING ERRCODE = 'restrict_violation';
  END IF;

  SELECT spr.time_quota_seconds INTO real_budget
    FROM iam_v2.service_plan_revisions spr
   WHERE spr.id = e.service_plan_revision_id;
  IF NEW.budget_seconds IS NOT NULL AND NEW.budget_seconds IS DISTINCT FROM real_budget THEN
    RAISE EXCEPTION 'termination evidence claims a % second budget but the pinned plan revision states %',
      NEW.budget_seconds, coalesce(real_budget::text, 'none') USING ERRCODE = 'restrict_violation';
  END IF;

  -- An outer-window expiry must name the entitlement's OWN immutable window. window_ends_at is stamped once
  -- and never moves, so evidence naming a different instant is describing a boundary that does not exist.
  IF NEW.window_ends_at IS NOT NULL AND NEW.window_ends_at IS DISTINCT FROM e.window_ends_at THEN
    RAISE EXCEPTION 'termination evidence names window % but the entitlement''s immutable window is %',
      NEW.window_ends_at, coalesce(e.window_ends_at::text, 'none') USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.cause_detail = 'AGGREGATE_OUTER_WINDOW_EXPIRED' AND e.window_ends_at IS NULL THEN
    RAISE EXCEPTION 'outer-window expiry evidence for an entitlement that has no window at all'
      USING ERRCODE = 'restrict_violation';
  END IF;

  RETURN NEW;
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_termination_evidence_matches_transition() FROM PUBLIC;

CREATE TRIGGER p6_termination_evidence_matches_transition
  BEFORE INSERT ON iam_v2.entitlement_termination_evidence
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_termination_evidence_matches_transition();

CREATE OR REPLACE FUNCTION iam_v2.p6_termination_evidence_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.entitlement_termination_evidence is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_termination_evidence_append_only() FROM PUBLIC;

CREATE TRIGGER p6_termination_evidence_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.entitlement_termination_evidence
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_termination_evidence_append_only();

-- A distinct disconnect reason for guest self-service, so a slot released by the guest is never confused
-- with one released by checkout, expiry, transfer or an operator.
COMMENT ON COLUMN iam_v2.entitlement_devices.disconnected_reason IS
  'Why the device left this entitlement. Phase 6 adds GUEST_SELF_SERVICE, which is deliberately distinct from '
  'ENTITLEMENT_ENDED, CROSS_PMS_TRANSFER and operator-driven reasons: a slot the guest released is a '
  'different fact from one the system took back.';

-- ---------------------------------------------------------------------------------------------------------
-- 5. The controlled writer: callers state the CAUSE, the database derives the FACTS
-- ---------------------------------------------------------------------------------------------------------
-- The trigger above makes a fabricated row impossible. This makes it unnecessary to try: the sanctioned path
-- takes only the entitlement and which time rule ran out, and reads every number from the entitlement and its
-- pinned immutable plan revision. A caller that cannot supply a number cannot supply a wrong one.
CREATE OR REPLACE FUNCTION iam_v2.p6_record_time_termination(p_entitlement uuid, p_cause text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE e record; v_budget bigint;
BEGIN
  IF p_cause NOT IN ('VALIDITY_WINDOW_ELAPSED','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_OUTER_WINDOW_EXPIRED') THEN
    RAISE EXCEPTION 'unknown time-termination cause %', p_cause USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT id, tenant_id, site_id, status, terminal_reason, terminated_at, time_accounting_mode,
         window_ends_at, consumed_online_seconds, service_plan_revision_id
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement %', p_entitlement USING ERRCODE = 'foreign_key_violation';
  END IF;
  SELECT spr.time_quota_seconds INTO v_budget FROM iam_v2.service_plan_revisions spr
   WHERE spr.id = e.service_plan_revision_id;

  INSERT INTO iam_v2.entitlement_termination_evidence
    (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode,
     budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
  VALUES (e.id, e.tenant_id, e.site_id, e.terminal_reason, p_cause, e.time_accounting_mode,
          v_budget, e.consumed_online_seconds, e.window_ends_at, e.terminated_at);
END $$;

-- THE CONTROLLED WRITER IS THE ONE THAT MATTERS MOST, and it is the one PostgreSQL hands to everybody by
-- default: a function's ACL starts NULL, which means PUBLIC EXECUTE. It was measured that way -- proacl NULL
-- on all five Phase-6 functions -- rather than assumed, and the measurement is why this block exists. Phase 3
-- and Phase 5 already revoke every controlled writer from PUBLIC; 0030 simply had not, and a mutation-capable
-- function reachable by PUBLIC is a privilege defect whether or not anything currently calls it.
--
-- NOTHING IS GRANTED HERE. Phase 6 is DARK and no service role needs this yet; the grant belongs to the
-- vertical slice that actually wires a caller, given to that role and no other. Revoking now and granting
-- later is the safe order -- the reverse leaves a window in which the privilege exists for no reason.
REVOKE EXECUTE ON FUNCTION iam_v2.p6_record_time_termination(uuid, text) FROM PUBLIC;

COMMENT ON FUNCTION iam_v2.p6_record_time_termination(uuid, text) IS
  'The sanctioned way to record why a time-mode entitlement ended. It accepts the entitlement and the cause '
  'and derives every number from the entitlement and its pinned immutable plan revision, so no caller is in '
  'a position to state a budget, a consumption or a window at all.';

INSERT INTO public.schema_migrations (version) VALUES ('0030_phase6_foundation')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
