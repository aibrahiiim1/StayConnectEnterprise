-- PHASE 6 FOUNDATION — Guest Device Self-Service setting + AGGREGATE_ONLINE_TIME state.
--
-- ADDITIVE ONLY. No existing column changes type, no existing row is rewritten, and no existing behaviour
-- changes when this migration is applied: every object here is either a new table nobody reads yet, or a
-- CHECK that admits a value nothing produces yet. Applying it to a live database is a no-op for every
-- running service.
--
-- WHAT IT ESTABLISHES
--
--   1. iam_v2.appliance_product_settings — the per-appliance, local-first product settings row. It is a
--      TYPED table rather than another key in public.tenants.auth_methods or public.appliances.metadata,
--      because those are tenant-scoped and untyped respectively, and a boolean that decides whether a guest
--      can release a device deserves a column a constraint can defend.
--
--   2. iam_v2.appliance_product_setting_changes — append-only audit of every change to that setting, with
--      the actor. A setting that can be flipped without a trace is a setting nobody can investigate.
--
--   3. iam_v2.session_online_watermarks — the durable per-session "charged through" instant. §6.4 of the
--      FINAL contract makes byte accounting idempotent with a watermark; wall-clock time has no cumulative
--      counter to compare against, so without an equivalent watermark a replayed tick, a delayed tick or a
--      reboot would each double-charge. This is that watermark, deliberately shaped like the byte one.
--
--   4. The AGGREGATE_ONLINE_TIME terminal reason. entitlements.terminal_reason already admits TIME for a
--      window elapsing; aggregate exhaustion is a DIFFERENT cause and gets its own value, so evidence can
--      distinguish "the week ran out" from "the minutes ran out".
--
-- WHAT IT DELIBERATELY DOES NOT DO
--
--   * It does not add a second window concept. An AGGREGATE_ONLINE_TIME entitlement's outer hard-validity
--     window is the existing entitlements.window_ends_at, stamped once and never moved, exactly as today.
--   * It does not touch time_accounting_mode or any package revision. Existing revisions already carry
--     VALIDITY_WINDOW in their immutable snapshots, so they cannot be reinterpreted retroactively — that
--     property comes from immutability that already exists, not from a compatibility branch added here.
--   * It grants nothing to any role. Phase 6 is DARK; privileges are derived from a real audit in M4.
BEGIN;

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
  PRIMARY KEY (tenant_id, site_id, appliance_id)
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
  -- Who did it. Free-form because the operator identity model lives in the public schema today; it is NOT
  -- NULL because "somebody changed it" is not an audit record.
  changed_by text NOT NULL CHECK (length(btrim(changed_by)) > 0),
  change_reason text
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

CREATE TRIGGER p6_online_watermark_monotonic
  BEFORE UPDATE ON iam_v2.session_online_watermarks
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_online_watermark_monotonic();

-- ---------------------------------------------------------------------------------------------------------
-- 4. Aggregate exhaustion is its OWN terminal cause
-- ---------------------------------------------------------------------------------------------------------
-- TIME already means "the validity window elapsed". Reusing it for "the online-minute budget ran out" would
-- make the two indistinguishable in evidence, and they are different products of different causes.
ALTER TABLE iam_v2.entitlements DROP CONSTRAINT IF EXISTS entitlements_terminal_reason_check;
ALTER TABLE iam_v2.entitlements ADD CONSTRAINT entitlements_terminal_reason_check
  CHECK (terminal_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN','REVOKED','SUPERSEDED',
                             'CONVERTED','TRANSFERRED','CANCELLED','AGGREGATE_TIME','OTHER'));

-- A distinct disconnect reason for guest self-service, so a slot released by the guest is never confused
-- with one released by checkout, expiry, transfer or an operator.
COMMENT ON COLUMN iam_v2.entitlement_devices.disconnected_reason IS
  'Why the device left this entitlement. Phase 6 adds GUEST_SELF_SERVICE, which is deliberately distinct from '
  'ENTITLEMENT_ENDED, CROSS_PMS_TRANSFER and operator-driven reasons: a slot the guest released is a '
  'different fact from one the system took back.';

INSERT INTO public.schema_migrations (version) VALUES ('0030_phase6_foundation')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
