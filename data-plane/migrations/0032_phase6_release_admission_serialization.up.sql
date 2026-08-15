-- PHASE 6 — reconcile the guest release with the REAL device-admission path.
--
-- THE DEFECT THIS FIXES.
--
-- 0031's release hand-rolled its own deauthorization: two UPDATEs that happened to produce the same rows the
-- approved primitive produces. That was wrong in two ways at once.
--
--   1. It bypassed iam_v2.deauthorize_entitlement_device, which migration 0010 declares to be one of the ONLY
--      two approved ways to open or close an authorization interval. A second implementation of an invariant
--      is a second place for it to drift, and this one had already drifted: it never called
--      begin_controlled_operation('device_auth'), so it wrote a capability-scoped family without declaring
--      the scope, and it did not enforce the "an interval may not close before it opened" rule.
--
--   2. Row-locking the sessions that ALREADY EXIST does not serialize against a session that does not exist
--      yet. FOR UPDATE locks rows; it cannot lock the absence of a row. So a release could observe "no live
--      sessions", release the binding, and commit -- while a concurrent admission inserted a live session
--      against that same binding. The final state, a DISCONNECTED binding carrying an active session, is
--      exactly what the feature must never produce, and 0031's own race test ACCEPTED it.
--
-- THE FIX HAS TWO HALVES, and neither is sufficient alone.
--
--   A. The release now calls the approved primitive, so release and admission share one serialization
--      boundary: both take the L3 entitlement row lock first, in the global lock order, and both declare the
--      device_auth scope. No release-only lock is invented -- that is precisely the mistake that lets two
--      writers believe they are synchronised when they are not.
--
--   B. A STRUCTURAL GUARD makes the forbidden state impossible rather than merely unlikely. A session may not
--      be active or PENDING_ENFORCEMENT while its binding is not AUTHORIZED. This is what turns "the lock
--      ordering should prevent it" into "the database will not hold it", and it is what forces a released
--      device back through iam_v2.authorize_entitlement_device -- which re-checks the device limit and opens
--      a NEW interval -- before it can be online again.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- B. The invariant, enforced
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p6_session_requires_authorized_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE v_status text;
BEGIN
  -- Only live states are constrained. A session may END on a released binding -- that is the ordinary result
  -- of a revocation -- and forbidding it would make every deauthorization path fail.
  IF NEW.state NOT IN ('active', 'PENDING_ENFORCEMENT') THEN
    RETURN NEW;
  END IF;

  SELECT status INTO v_status FROM iam_v2.entitlement_devices
   WHERE entitlement_id = NEW.entitlement_id AND device_id = NEW.device_id;

  IF v_status IS NULL THEN
    RAISE EXCEPTION 'session for a device with no authorization binding on entitlement % (device %)',
      NEW.entitlement_id, NEW.device_id USING ERRCODE = 'restrict_violation';
  END IF;
  IF v_status <> 'AUTHORIZED' THEN
    RAISE EXCEPTION 'a % session may not exist on a % binding: the device must be re-authorized through '
      'iam_v2.authorize_entitlement_device first', NEW.state, v_status USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.p6_session_requires_authorized_binding() FROM PUBLIC;

COMMENT ON FUNCTION iam_v2.p6_session_requires_authorized_binding() IS
  'A live session may not exist on a binding that is not AUTHORIZED. Row locks cannot lock the ABSENCE of a '
  'row, so no lock ordering alone can stop a release and a concurrent admission from producing a DISCONNECTED '
  'binding with an active session. This makes that state unrepresentable, and forces a released device back '
  'through authorize_entitlement_device -- re-checking the device limit and opening a new interval -- before '
  'it can be online again.';

-- BEFORE INSERT OR UPDATE OF state: an admission inserting a live session, and a session being promoted from
-- PENDING_ENFORCEMENT to active, are both moments at which the binding must still be authorized.
CREATE TRIGGER p6_session_requires_authorized_binding
  BEFORE INSERT OR UPDATE OF state, entitlement_id, device_id ON iam_v2.sessions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_session_requires_authorized_binding();

-- ---------------------------------------------------------------------------------------------------------
-- A. The release, through the approved primitive
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p6_guest_release_device(
  p_entitlement uuid, p_device uuid, p_max_releases_per_hour int DEFAULT 20)
RETURNS text LANGUAGE plpgsql AS $$
DECLARE
  e record;
  b record;
  live_sessions int;
  recent int;
  released boolean;
BEGIN
  -- L3 FIRST, in the global lock order, exactly as authorize_entitlement_device and
  -- deauthorize_entitlement_device do. This is the boundary the admission path also takes, which is what
  -- makes the two mutually exclusive rather than merely usually-not-simultaneous.
  SELECT id, tenant_id, site_id, status INTO e
    FROM iam_v2.entitlements WHERE id = p_entitlement FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement' USING ERRCODE = 'foreign_key_violation';
  END IF;

  SELECT count(*) INTO recent FROM iam_v2.guest_device_actions
   WHERE entitlement_id = p_entitlement AND action = 'RELEASE'
     AND acted_at > now() - interval '1 hour';
  IF recent >= p_max_releases_per_hour THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_THROTTLED',
              format('%s release attempts in the last hour', recent));
    RETURN 'REFUSED_THROTTLED';
  END IF;

  SELECT entitlement_id, device_id, status INTO b
    FROM iam_v2.entitlement_devices
   WHERE entitlement_id = p_entitlement AND device_id = p_device;
  IF NOT FOUND THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_NOT_FOUND',
              'the device is not bound to this entitlement');
    RETURN 'REFUSED_NOT_FOUND';
  END IF;

  IF b.status <> 'AUTHORIZED' THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ALREADY_RELEASED',
              format('binding is already %s', b.status));
    RETURN 'REFUSED_ALREADY_RELEASED';
  END IF;

  -- The offline read, still inside the L3 lock. It no longer needs FOR UPDATE: holding the entitlement lock
  -- excludes the admission path entirely, and the structural guard catches anything that could still slip
  -- past a lock. Counting here decides whether to refuse; the guard decides whether the world stays coherent.
  SELECT count(*) INTO live_sessions FROM iam_v2.sessions
   WHERE entitlement_id = p_entitlement AND device_id = p_device
     AND state IN ('active', 'PENDING_ENFORCEMENT');
  IF live_sessions > 0 THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ONLINE',
              format('%s live session(s) on this device', live_sessions));
    RETURN 'REFUSED_ONLINE';
  END IF;

  -- THE APPROVED PRIMITIVE, not a second implementation of it. It closes the interval, flips the binding,
  -- declares the device_auth scope and re-takes L3 (already held, so it is a no-op that keeps the contract).
  released := iam_v2.deauthorize_entitlement_device(p_entitlement, p_device, now(), 'GUEST_SELF_SERVICE');
  IF NOT released THEN
    -- No open interval: the binding said AUTHORIZED but no interval was open, which means something else
    -- closed it between the two reads. Report it as already released rather than inventing a success.
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ALREADY_RELEASED',
              'no open authorization interval');
    RETURN 'REFUSED_ALREADY_RELEASED';
  END IF;

  INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
    VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'OK', 'slot released by guest self-service');
  RETURN 'OK';
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) FROM PUBLIC;

INSERT INTO public.schema_migrations (version) VALUES ('0032_phase6_release_admission_serialization')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
