-- PHASE 6 — GUEST DEVICE SELF-SERVICE, the durable half.
--
-- ADDITIVE ONLY, and DARK: one new audit table, one controlled function, nothing granted to any role.
--
-- WHY THE RELEASE LIVES IN THE DATABASE
--
-- Every property this feature has to hold is a property about CONCURRENT state, and every one of them is lost
-- if the decision is made in application code and the write happens afterwards:
--
--   * "only an OFFLINE device may be removed" is worthless if the offline check and the removal are separate
--     statements -- the device can come online in between, and the guest frees a slot whose kernel
--     authorization is still forwarding traffic;
--   * "the slot is freed exactly once" cannot be enforced by a caller that has already decided to free it;
--   * "the caller's own devices only" has to be a WHERE clause on the server's derived entitlement, not a
--     filter the caller could be trusted to have applied.
--
-- So the function below is the whole operation: it takes the entitlement the SERVER resolved and a device id,
-- and either releases exactly that binding or refuses, inside one transaction, under one lock order.
--
-- WHAT IT NEVER DOES
--
--   * it deletes nothing. The device row, its network appearances, every accounting record, every
--     authorization interval and every audit row survive a release untouched. Freeing a slot is a statement
--     about the FUTURE; the evidence of what already happened is not the slot's to destroy.
--   * it takes no tenant, site, MAC, room, stay, PMS interface or profile from anybody. The only identity it
--     accepts is an entitlement id the caller has already been proven to own by the layer above, and every
--     lookup is scoped by it.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Audit: every guest-initiated device action, refused ones included
-- ---------------------------------------------------------------------------------------------------------
-- REFUSALS ARE RECORDED, not just successes. A guest repeatedly trying to release somebody else's device, or
-- hammering a device that keeps coming back online, is exactly the pattern an operator needs to see, and a
-- log that only contains what worked cannot show it.
CREATE TABLE iam_v2.guest_device_actions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  entitlement_id uuid NOT NULL,
  -- The device the action was ABOUT. Nullable because a refusal may name a device that is not in this
  -- entitlement at all, and recording "which device" is exactly what makes that attempt investigable.
  device_id uuid,
  action text NOT NULL CHECK (action IN ('LIST','RELEASE')),
  outcome text NOT NULL CHECK (outcome IN ('OK','REFUSED_ONLINE','REFUSED_NOT_FOUND','REFUSED_ALREADY_RELEASED',
                                           'REFUSED_DISABLED','REFUSED_THROTTLED')),
  -- Why, in the system's words rather than the guest's. Never echoes anything the caller supplied.
  detail text,
  acted_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, entitlement_id)
    REFERENCES iam_v2.entitlements (tenant_id, site_id, id) ON DELETE CASCADE
);
CREATE INDEX gda_lookup ON iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, acted_at DESC);
-- Throttling reads this index: how many RELEASE attempts has this entitlement made recently.
CREATE INDEX gda_release_rate ON iam_v2.guest_device_actions (entitlement_id, action, acted_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.p6_guest_device_actions_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.guest_device_actions is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.p6_guest_device_actions_append_only() FROM PUBLIC;

CREATE TRIGGER p6_guest_device_actions_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.guest_device_actions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_guest_device_actions_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- 2. The controlled release
-- ---------------------------------------------------------------------------------------------------------
-- Returns the outcome as text so the caller can map it to a uniform guest response without having to
-- distinguish exceptions from refusals. A refusal is a normal answer here, not an error: the guest asked a
-- reasonable question and the answer was no.
CREATE OR REPLACE FUNCTION iam_v2.p6_guest_release_device(
  p_entitlement uuid, p_device uuid, p_max_releases_per_hour int DEFAULT 20)
RETURNS text LANGUAGE plpgsql AS $$
DECLARE
  e record;
  b record;
  live_sessions int;
  recent int;
  v_outcome text;
  v_detail text;
BEGIN
  -- The entitlement is the SERVER'S, resolved before this call. Locking it first fixes one order for every
  -- concurrent release on the same entitlement, which is what makes "exactly once" decidable at all.
  SELECT id, tenant_id, site_id, status INTO e
    FROM iam_v2.entitlements WHERE id = p_entitlement FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement' USING ERRCODE = 'foreign_key_violation';
  END IF;

  -- THROTTLE, counted from the durable audit rather than from memory, so it survives a restart and cannot be
  -- reset by reconnecting. Counted per entitlement because that is the subject the server derived; counting
  -- per device would let one guest spend the whole budget on somebody else's device id.
  SELECT count(*) INTO recent FROM iam_v2.guest_device_actions
   WHERE entitlement_id = p_entitlement AND action = 'RELEASE'
     AND acted_at > now() - interval '1 hour';
  IF recent >= p_max_releases_per_hour THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_THROTTLED',
              format('%s release attempts in the last hour', recent));
    RETURN 'REFUSED_THROTTLED';
  END IF;

  -- The binding must be THIS entitlement's. A device id belonging to another guest simply is not here, so it
  -- gets the same answer as a device that never existed -- there is no oracle to probe.
  SELECT entitlement_id, device_id, status INTO b
    FROM iam_v2.entitlement_devices
   WHERE entitlement_id = p_entitlement AND device_id = p_device
   FOR UPDATE;
  IF NOT FOUND THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_NOT_FOUND',
              'the device is not bound to this entitlement');
    RETURN 'REFUSED_NOT_FOUND';
  END IF;

  -- EXACTLY ONCE. A binding already DISCONNECTED has had its slot released; releasing it again would be a
  -- second free of one slot, which is how a device limit quietly becomes no limit at all.
  IF b.status <> 'AUTHORIZED' THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ALREADY_RELEASED',
              format('binding is already %s', b.status));
    RETURN 'REFUSED_ALREADY_RELEASED';
  END IF;

  -- THE OFFLINE RE-CHECK, INSIDE THE LOCK. Whatever the caller observed a moment ago is irrelevant; this is
  -- the read that decides. PENDING_ENFORCEMENT counts as online deliberately -- a grant still converging at
  -- the edge is not safe to release, because its kernel authorization is still landing. (That is the
  -- REMOVAL-SAFETY predicate; the ACCOUNTING one is 'active' only, and they are not interchangeable.)
  -- PERFORM ... FOR UPDATE rather than SELECT count(*) ... FOR UPDATE: PostgreSQL refuses row locking with an
  -- aggregate, and counting without locking would leave exactly the gap this check exists to close. ROW_COUNT
  -- gives the number as a by-product of the locking read, so one statement both counts and holds.
  PERFORM 1 FROM iam_v2.sessions
   WHERE entitlement_id = p_entitlement AND device_id = p_device
     AND state IN ('active', 'PENDING_ENFORCEMENT')
   FOR UPDATE;
  GET DIAGNOSTICS live_sessions = ROW_COUNT;
  IF live_sessions > 0 THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ONLINE',
              format('%s live session(s) on this device', live_sessions));
    RETURN 'REFUSED_ONLINE';
  END IF;

  -- Release: the slot, and nothing else. The device row, its accounting and its history are untouched.
  UPDATE iam_v2.entitlement_devices
     SET status = 'DISCONNECTED', disconnected_reason = 'GUEST_SELF_SERVICE'
   WHERE entitlement_id = p_entitlement AND device_id = p_device AND status = 'AUTHORIZED';

  -- Close the open authorization interval at the same instant, exactly as every other release path does.
  -- Leaving it open would say the device is still authorized under an entitlement that has released it --
  -- the same defect Phase 5 found and fixed on the transfer path (D5-1).
  UPDATE iam_v2.entitlement_device_authorizations a
     SET deauthorized_at = GREATEST(now(), a.authorized_at)
   WHERE a.entitlement_id = p_entitlement AND a.device_id = p_device AND a.deauthorized_at IS NULL;

  INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
    VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'OK', 'slot released by guest self-service');
  RETURN 'OK';
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) FROM PUBLIC;

COMMENT ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) IS
  'The whole guest device release, in one transaction: durable throttle, own-entitlement scoping, '
  'exactly-once slot release, and the offline re-check taken INSIDE the lock so an offline-to-online race '
  'loses safely. Deletes nothing -- device identity, accounting, authorization intervals and audit all '
  'survive. Takes no client-chosen subject identifier: the entitlement is the one the server resolved.';

INSERT INTO public.schema_migrations (version) VALUES ('0031_phase6_guest_device_self_service')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
