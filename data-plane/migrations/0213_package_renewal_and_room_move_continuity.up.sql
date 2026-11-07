-- 0213 -- PACKAGE RENEWAL, AND A ROOM MOVE KEEPS THE GUEST'S PACKAGE (Product Owner, 2026-11-06).
--
-- RENEWAL. A stay could take each package revision once (purchase_once_per_stay), so a One Day package that ended could never be
-- bought again. The revision's own renewable flag and max_purchases_per_stay (both in the catalogue since the contract, never read)
-- now decide it, in one function the offer list and the purchase guard both call:
--   * a stay that never bought the revision may buy it (unchanged);
--   * a revision bought before may be bought again only when it is renewable, nothing of it is live or still being settled, and the
--     stay holds fewer than max_purchases_per_stay of it (empty = unlimited).
-- Each guest purchase of a revision carries the next purchase_seq for the stay; the unique index now includes it, so two concurrent
-- purchases of the same renewal still get exactly one winner and no second room charge.
--
-- ROOM MOVE. Protel announces a room move as a check-out from the old room and a check-in to the new one with the same reservation
-- (FIAS 2.20 p. 46; order not fixed). When the check-out came first, the stay was checked out and its package converted into After
-- Check-out Access, and the check-in then reinstated the stay without the package (contract F1: a room move preserves entitlement,
-- devices and quota). restore_room_move_access continues the converted package once the verified evidence shows a move: the
-- check-out event that closed the episode named one room and a check-in of the same stay names another. It never extends the
-- package (the original window is kept), never resets its data (the continuation's allowance is what the original and the After
-- Check-out Access left), and never charges (a zero-price ROOM_MOVE_RESTORE purchase). Every decision is recorded once per episode
-- in room_move_restorations.
--
-- WHAT THIS MIGRATION CHANGES, AND ONLY THAT: a CHECK on max_purchases_per_stay, the purchase guard and its index, the purchase
-- trigger value ROOM_MOVE_RESTORE, the restoration table and functions. No existing row is rewritten. The continuation supersedes the
-- After Check-out Access that superseded the original (or the original when there was none); policy_snapshot.restores names the original.

BEGIN;

ALTER TABLE iam_v2.internet_package_revisions
  ADD CONSTRAINT ipr_max_purchases_per_stay CHECK (max_purchases_per_stay IS NULL OR max_purchases_per_stay BETWEEN 1 AND 1000);

-- ---------------------------------------------------------------------------------------------------------------------------------
-- Renewal
-- ---------------------------------------------------------------------------------------------------------------------------------
CREATE FUNCTION iam_v2.stay_package_purchase_refusal(p_stay uuid, p_revision uuid) RETURNS text
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_prior integer; v_renewable boolean; v_max integer;
BEGIN
  SELECT count(*) INTO v_prior FROM iam_v2.purchases p
   WHERE p.stay_id = p_stay AND p.package_revision_id = p_revision AND p.trigger = 'GUEST_SELECTION'
     AND p.state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED');
  IF v_prior = 0 THEN
    RETURN NULL;
  END IF;
  SELECT r.renewable, r.max_purchases_per_stay INTO v_renewable, v_max
    FROM iam_v2.internet_package_revisions r WHERE r.id = p_revision;
  IF NOT COALESCE(v_renewable, false) THEN
    RETURN 'NOT_RENEWABLE';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.purchases p
              WHERE p.stay_id = p_stay AND p.package_revision_id = p_revision AND p.trigger = 'GUEST_SELECTION'
                AND p.state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW')) THEN
    RETURN 'PURCHASE_IN_PROGRESS';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.entitlements e
              WHERE e.stay_id = p_stay AND e.package_revision_id = p_revision
                AND e.status IN ('PENDING','ACTIVE','SUSPENDED')) THEN
    RETURN 'STILL_ACTIVE';
  END IF;
  IF v_max IS NOT NULL AND v_prior >= v_max THEN
    RETURN 'LIMIT_REACHED';
  END IF;
  RETURN NULL;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.stay_package_purchase_refusal(uuid, uuid) FROM PUBLIC;
COMMENT ON FUNCTION iam_v2.stay_package_purchase_refusal(uuid, uuid) IS
  'Why a stay may not buy this package revision now (NULL: it may): NOT_RENEWABLE, PURCHASE_IN_PROGRESS, STILL_ACTIVE, LIMIT_REACHED. '
  'The guest offer list and the purchase guard call the same function (0213).';

CREATE FUNCTION iam_v2.purchase_renewal_guard() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_refusal text;
BEGIN
  IF NEW.trigger <> 'GUEST_SELECTION' OR NEW.stay_id IS NULL
     OR NEW.state NOT IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED') THEN
    RETURN NEW;
  END IF;
  v_refusal := iam_v2.stay_package_purchase_refusal(NEW.stay_id, NEW.package_revision_id);
  IF v_refusal IS NOT NULL THEN
    RAISE EXCEPTION 'purchase_once_per_stay: %', v_refusal USING ERRCODE = 'unique_violation';
  END IF;
  SELECT COALESCE(max(p.purchase_seq), 0) + 1 INTO NEW.purchase_seq FROM iam_v2.purchases p
   WHERE p.stay_id = NEW.stay_id AND p.package_revision_id = NEW.package_revision_id AND p.trigger = 'GUEST_SELECTION'
     AND p.state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED');
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.purchase_renewal_guard() FROM PUBLIC;
CREATE TRIGGER purchase_renewal_guard BEFORE INSERT ON iam_v2.purchases
  FOR EACH ROW EXECUTE FUNCTION iam_v2.purchase_renewal_guard();

DROP INDEX iam_v2.purchase_once_per_stay;
CREATE UNIQUE INDEX purchase_once_per_stay ON iam_v2.purchases (stay_id, package_revision_id, purchase_seq)
  WHERE state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED') AND trigger = 'GUEST_SELECTION';

-- ---------------------------------------------------------------------------------------------------------------------------------
-- Room move
-- ---------------------------------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.purchases DROP CONSTRAINT purchases_trigger_check;
ALTER TABLE iam_v2.purchases ADD CONSTRAINT purchases_trigger_check CHECK (trigger = ANY (ARRAY['GUEST_SELECTION','VOUCHER_REDEMPTION',
  'ACCOUNT_AUTO_GRANT','OTP_SOCIAL_DEFAULT','CHECKOUT_GRACE','EMERGENCY_GRACE','POST_STAY_CONVERSION','CROSS_PMS_TRANSFER','ADMIN_GRANT',
  'RENEWAL','ROOM_MOVE_RESTORE']));

CREATE TABLE iam_v2.room_move_restorations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  stay_id uuid NOT NULL REFERENCES iam_v2.stays(id),
  lifecycle_version integer NOT NULL,
  checkout_event_id uuid NOT NULL,
  checkin_event_id uuid NOT NULL,
  original_entitlement_id uuid,
  grace_entitlement_id uuid,
  superseded_entitlement_id uuid,
  restored_entitlement_id uuid,
  purchase_id uuid,
  outcome text NOT NULL CHECK (outcome IN ('RESTORED','NOTHING_TO_RESTORE','EXPIRED','DATA_USED_UP','LIVE_ACCESS_PRESENT','UNSUPPORTED',
                                           'FUNDING_REVERSED')),
  source text NOT NULL CHECK (source IN ('ROOM_MOVE','RECOVERY')),
  remaining_data_bytes bigint,
  devices integer NOT NULL DEFAULT 0,
  sessions integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (stay_id, lifecycle_version));
CREATE INDEX room_move_restorations_scope ON iam_v2.room_move_restorations (tenant_id, site_id, created_at);
COMMENT ON TABLE iam_v2.room_move_restorations IS
  'One decision per checkout episode that a verified room move closed (0213): what was restored, or why nothing was. Append-only.';

CREATE FUNCTION iam_v2.room_move_restorations_append_only() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION 'iam_v2.room_move_restorations is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $fn$;
CREATE TRIGGER room_move_restorations_append_only BEFORE UPDATE OR DELETE ON iam_v2.room_move_restorations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.room_move_restorations_append_only();

-- The room a stay event names, compared as internal/namenorm.Room does (trim, upper case, NFC).
CREATE FUNCTION iam_v2.stay_event_room(p_payload jsonb) RETURNS text
  LANGUAGE sql IMMUTABLE AS $fn$
  SELECT NULLIF(upper(btrim(normalize(COALESCE(p_payload->>'room', ''), NFC))), '')
$fn$;

-- The check-in the stay engine applied next after a stay event (processing order, which is how the episode actually closed). processed_at is
-- stamped after the per-interface lock since 0213 (clock_timestamp); earlier rows carry their transaction start, which recovery accepts
-- only as recorded.
CREATE FUNCTION iam_v2.stay_next_applied_checkin(p_stay uuid, p_after uuid) RETURNS uuid
  LANGUAGE sql STABLE AS $fn$
  SELECT li.id FROM iam_v2.stay_events li, iam_v2.stay_events lg
   WHERE lg.id = p_after AND li.stay_id = p_stay AND li.event_type = 'GI' AND li.processing_status = 'APPLIED'
     AND li.processed_at IS NOT NULL AND lg.processed_at IS NOT NULL AND (li.processed_at, li.id) > (lg.processed_at, lg.id)
   ORDER BY li.processed_at, li.id LIMIT 1
$fn$;

-- Whether the purchase that funded this access (found by walking back through room-move continuations) was refunded or reversed:
-- a settlement marked REVERSED, card refunds reaching the captured amount, any chargeback, or a reversal posting of the room charge.
CREATE FUNCTION iam_v2.entitlement_funding_reversed(p_ent uuid) RETURNS boolean
  LANGUAGE plpgsql STABLE AS $fn$
DECLARE v_ent uuid := p_ent; v_purchase uuid; v_trigger text; v_restores text; i integer := 0;
BEGIN
  LOOP
    SELECT e.purchase_id, pu.trigger, e.policy_snapshot->>'restores' INTO v_purchase, v_trigger, v_restores
      FROM iam_v2.entitlements e JOIN iam_v2.purchases pu ON pu.id = e.purchase_id WHERE e.id = v_ent;
    EXIT WHEN v_trigger IS DISTINCT FROM 'ROOM_MOVE_RESTORE' OR v_restores IS NULL OR i >= 50;
    v_ent := v_restores::uuid; i := i + 1;
  END LOOP;
  IF v_purchase IS NULL THEN
    RETURN false;
  END IF;
  RETURN EXISTS (SELECT 1 FROM iam_v2.settlements se WHERE se.purchase_id = v_purchase AND se.status = 'REVERSED')
      OR EXISTS (SELECT 1 FROM iam_v2.settlements se
                   JOIN iam_v2.payment_transactions c ON c.settlement_id = se.id AND c.transaction_type = 'CHARGE' AND c.status = 'CAPTURED'
                  WHERE se.purchase_id = v_purchase
                    AND ((SELECT COALESCE(sum(r.amount_minor), 0) FROM iam_v2.payment_transactions r
                           WHERE r.parent_transaction_id = c.id AND r.transaction_type = 'REFUND' AND r.status = 'CAPTURED') >= c.amount_minor
                         OR EXISTS (SELECT 1 FROM iam_v2.payment_transactions b
                                     WHERE b.parent_transaction_id = c.id AND b.transaction_type = 'CHARGEBACK' AND b.status = 'CAPTURED')))
      OR EXISTS (SELECT 1 FROM iam_v2.pms_postings ch JOIN iam_v2.pms_postings rv ON rv.reverses_posting_id = ch.id AND rv.posting_type = 'REVERSAL'
                  WHERE ch.purchase_id = v_purchase AND ch.posting_type = 'CHARGE');
END $fn$;
-- Continues the package a checkout converted, when the verified events show the checkout was half of a room move. p_checkin is the
-- check-in that completed the move (ROOM_MOVE), or NULL for RECOVERY, which then finds the stay's first check-in into another room
-- after that checkout. p_supersede_dominated (RECOVERY only) lets an unlimited, stay-long original replace a live package of the
-- stay that it fully covers; the automatic path never replaces live access. Idempotent per episode.
CREATE FUNCTION iam_v2.restore_room_move_access(p_tenant uuid, p_site uuid, p_stay uuid, p_episode integer, p_checkin uuid,
                                                p_source text, p_supersede_dominated boolean DEFAULT false)
  RETURNS TABLE (outcome text, restored_entitlement uuid, devices integer, sessions integer)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE st record; au record; o record; lv record; v_go_room text; v_in_room text; v_in uuid; v_grace_status text;
        v_quota bigint; v_used numeric; v_remaining bigint; v_now timestamptz; v_purchase uuid; v_new uuid;
        v_devices integer := 0; v_sessions integer := 0; v_sid uuid; v_out text; v_super uuid;
BEGIN
  IF p_source NOT IN ('ROOM_MOVE','RECOVERY') OR (p_source = 'ROOM_MOVE' AND (p_checkin IS NULL OR p_supersede_dominated)) THEN
    RAISE EXCEPTION 'ROOM_MOVE_INVALID_REQUEST' USING ERRCODE = 'P0001';
  END IF;
  SELECT * INTO st FROM iam_v2.stays WHERE id = p_stay AND tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF NOT FOUND OR st.status <> 'IN_HOUSE' OR p_episode >= st.lifecycle_version THEN
    RAISE EXCEPTION 'ROOM_MOVE_STAY_NOT_REINSTATED' USING ERRCODE = 'P0001';
  END IF;
  -- An older episode is continued only when every later checkout of the stay was itself a proven room move: a genuine departure in
  -- between would have ended the package anyway (contract 7.2).
  IF EXISTS (SELECT 1 FROM iam_v2.checkout_grace_audit la
              WHERE la.stay_id = p_stay AND la.lifecycle_version > p_episode AND la.lifecycle_version < st.lifecycle_version
                AND NOT EXISTS (SELECT 1 FROM iam_v2.stay_events lg, iam_v2.stay_events li
                                 WHERE lg.id = la.boundary_event_id AND lg.event_type = 'GO'
                                   AND li.id = iam_v2.stay_next_applied_checkin(p_stay, lg.id)
                                   AND iam_v2.stay_event_room(lg.payload) IS NOT NULL AND iam_v2.stay_event_room(li.payload) IS NOT NULL
                                   AND iam_v2.stay_event_room(li.payload) <> iam_v2.stay_event_room(lg.payload))) THEN
    RAISE EXCEPTION 'ROOM_MOVE_SUPERSEDED: a later checkout of this stay was not a room move' USING ERRCODE = 'P0001';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.room_move_restorations x WHERE x.stay_id = p_stay AND x.lifecycle_version = p_episode) THEN
    RETURN QUERY SELECT x.outcome, x.restored_entitlement_id, x.devices, x.sessions
                   FROM iam_v2.room_move_restorations x WHERE x.stay_id = p_stay AND x.lifecycle_version = p_episode;
    RETURN;
  END IF;

  -- The evidence: the check-out that closed the episode, and a check-in of the same stay into another room after it.
  SELECT * INTO au FROM iam_v2.checkout_grace_audit a
   WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND a.stay_id = p_stay AND a.lifecycle_version = p_episode;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'ROOM_MOVE_NOT_PROVEN: the episode has no checkout record' USING ERRCODE = 'P0001';
  END IF;
  SELECT iam_v2.stay_event_room(e.payload) INTO v_go_room FROM iam_v2.stay_events e
   WHERE e.id = au.boundary_event_id AND e.stay_id = p_stay AND e.event_type = 'GO';
  IF v_go_room IS NULL THEN
    RAISE EXCEPTION 'ROOM_MOVE_NOT_PROVEN: the checkout was not a check-out event naming a room' USING ERRCODE = 'P0001';
  END IF;
  IF p_source = 'ROOM_MOVE' THEN
    -- The stay engine calls this from the transaction in which p_checkin reinstated the stay, so it is the check-in that closed the
    -- episode by construction.
    SELECT e.id, iam_v2.stay_event_room(e.payload) INTO v_in, v_in_room FROM iam_v2.stay_events e
     WHERE e.id = p_checkin AND e.stay_id = p_stay AND e.event_type = 'GI';
  ELSE
    SELECT e.id, iam_v2.stay_event_room(e.payload) INTO v_in, v_in_room FROM iam_v2.stay_events e
     WHERE e.id = iam_v2.stay_next_applied_checkin(p_stay, au.boundary_event_id);
  END IF;
  IF v_in IS NULL OR v_in_room IS NULL OR v_in_room = v_go_room THEN
    RAISE EXCEPTION 'ROOM_MOVE_NOT_PROVEN: no check-in of this stay into another room' USING ERRCODE = 'P0001';
  END IF;

  -- Waits for usage being recorded on the stay's live sessions (an ingest locks the session's checkpoint first).
  PERFORM 1 FROM iam_v2.accounting_checkpoints cp JOIN iam_v2.sessions s ON s.id = cp.session_id
    JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
   WHERE e.stay_id = p_stay AND s.ended IS NULL ORDER BY cp.id FOR UPDATE OF cp;

  SELECT * INTO o FROM iam_v2.entitlements e
   WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.stay_id = p_stay AND e.status = 'TERMINATED'
     AND e.terminal_reason = 'CONVERTED' AND e.terminated_at = au.boundary_at AND e.end_mode <> 'GRACE_AFTER_CHECKOUT'
   ORDER BY e.id LIMIT 1 FOR UPDATE;
  v_out := CASE WHEN NOT FOUND THEN 'NOTHING_TO_RESTORE'
                WHEN o.time_accounting_mode <> 'VALIDITY_WINDOW' THEN 'UNSUPPORTED'
                WHEN o.window_ends_at IS NOT NULL AND o.window_ends_at <= now() THEN 'EXPIRED'
                -- A refund or reversal of what paid for the package ends it for good (0115); a move never brings it back.
                WHEN iam_v2.entitlement_funding_reversed(o.id) THEN 'FUNDING_REVERSED' END;

  IF au.grace_entitlement_id IS NOT NULL THEN
    SELECT e.status INTO v_grace_status FROM iam_v2.entitlements e WHERE e.id = au.grace_entitlement_id FOR UPDATE;
  END IF;

  IF v_out IS NULL THEN
    SELECT COALESCE(o.data_quota_bytes, spr.data_quota_bytes) INTO v_quota
      FROM iam_v2.service_plan_revisions spr WHERE spr.id = o.service_plan_revision_id;
    IF v_quota IS NOT NULL THEN
      SELECT COALESCE(sum(u.bytes_up + u.bytes_down), 0) INTO v_used
        FROM (SELECT o.id AS id UNION ALL SELECT au.grace_entitlement_id WHERE au.grace_entitlement_id IS NOT NULL) x,
             LATERAL iam_v2.entitlement_usage_bytes(x.id, clock_timestamp()) u;
      v_remaining := v_quota - v_used::bigint;
      IF v_remaining <= 0 THEN
        v_out := 'DATA_USED_UP';
      END IF;
    END IF;
  END IF;

  IF v_out IS NULL THEN
    -- Live access other than the episode's After Check-out Access is never replaced, except (RECOVERY, asked for) by an
    -- original that is unlimited, lasts the whole stay and is at least as fast.
    SELECT e.* INTO lv FROM iam_v2.entitlements e
     WHERE e.stay_id = p_stay AND e.status IN ('PENDING','ACTIVE','SUSPENDED')
       AND (au.grace_entitlement_id IS NULL OR e.id <> au.grace_entitlement_id)
     FOR UPDATE;
    IF FOUND THEN
      IF p_supersede_dominated AND o.window_ends_at IS NULL AND v_quota IS NULL AND o.end_mode = 'MANUAL_END'
         AND EXISTS (SELECT 1 FROM iam_v2.service_plan_revisions a, iam_v2.service_plan_revisions b
                      WHERE a.id = o.service_plan_revision_id AND b.id = lv.service_plan_revision_id
                        AND a.down_kbps >= b.down_kbps AND a.up_kbps >= b.up_kbps
                        AND a.max_concurrent_devices >= b.max_concurrent_devices) THEN
        v_super := lv.id;
      ELSE
        v_out := 'LIVE_ACCESS_PRESENT';
      END IF;
    END IF;
  END IF;

  IF v_out IS NOT NULL THEN
    INSERT INTO iam_v2.room_move_restorations (tenant_id, site_id, stay_id, lifecycle_version, checkout_event_id, checkin_event_id,
                                               original_entitlement_id, grace_entitlement_id, outcome, source, remaining_data_bytes)
    VALUES (p_tenant, p_site, p_stay, p_episode, au.boundary_event_id, v_in, o.id, au.grace_entitlement_id, v_out, p_source, v_remaining);
    RETURN QUERY SELECT v_out, NULL::uuid, 0, 0;
    RETURN;
  END IF;

  v_now := clock_timestamp();
  IF v_grace_status IN ('PENDING','ACTIVE','SUSPENDED') THEN
    PERFORM iam_v2.apply_entitlement_transition(au.grace_entitlement_id, 'TERMINATED', v_now, 'SUPERSEDED');
  END IF;
  IF v_super IS NOT NULL THEN
    PERFORM iam_v2.apply_entitlement_transition(v_super, 'TERMINATED', v_now, 'SUPERSEDED');
  END IF;

  PERFORM iam_v2.begin_controlled_operation('commerce_intent');
  INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, auth_context_id, trigger,
                                amount_minor, state)
  VALUES (p_tenant, p_site, o.package_revision_id, o.pms_interface_id, o.stay_id,
          (SELECT pu.auth_context_id FROM iam_v2.purchases pu WHERE pu.id = o.purchase_id), 'ROOM_MOVE_RESTORE', 0, 'GRANTED')
  RETURNING id INTO v_purchase;

  INSERT INTO iam_v2.entitlements (tenant_id, site_id, stay_id, guest_account_id, voucher_id, guest_principal_id,
                                   anonymous_subject_id, pms_interface_id, purchase_id, policy_snapshot, snapshot_version,
                                   service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode,
                                   window_ends_at, status, renewal_number, supersedes_entitlement_id, is_emergency_grace,
                                   data_quota_bytes)
  VALUES (p_tenant, p_site, o.stay_id, o.guest_account_id, o.voucher_id, o.guest_principal_id, o.anonymous_subject_id,
          o.pms_interface_id, v_purchase,
          o.policy_snapshot || jsonb_build_object('granted_from', 'ROOM_MOVE_RESTORE', 'restores', o.id::text),
          o.snapshot_version, o.service_plan_revision_id, o.package_revision_id, o.time_accounting_mode, o.end_mode,
          o.window_ends_at, 'PENDING', o.renewal_number, COALESCE(au.grace_entitlement_id, o.id), o.is_emergency_grace,
          CASE WHEN v_quota IS NULL THEN o.data_quota_bytes ELSE v_remaining END)
  RETURNING id INTO v_new;
  PERFORM iam_v2.apply_entitlement_transition(v_new, 'ACTIVE', v_now, 'ROOM_MOVE_RESTORED');

  -- The devices the original held at the checkout, and any the After Check-out Access or a replaced package holds now.
  PERFORM iam_v2.begin_controlled_operation('device_auth');
  INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id, status, grandfathered,
                                          first_authorized, last_authorized)
  SELECT p_tenant, p_site, v_new, d.device_id, 'AUTHORIZED', false, min(d.first_authorized), v_now
    FROM (SELECT ed.device_id, ed.first_authorized FROM iam_v2.entitlement_devices ed
           WHERE ed.entitlement_id = o.id
             AND EXISTS (SELECT 1 FROM iam_v2.entitlement_device_authorizations a
                          WHERE a.entitlement_id = o.id AND a.device_id = ed.device_id
                            AND a.authorized_at <= au.boundary_at AND (a.deauthorized_at IS NULL OR a.deauthorized_at >= au.boundary_at))
          UNION ALL
          SELECT ed.device_id, ed.first_authorized FROM iam_v2.entitlement_devices ed
           WHERE ed.entitlement_id IN (au.grace_entitlement_id, v_super) AND ed.status = 'AUTHORIZED') d
   GROUP BY d.device_id;
  GET DIAGNOSTICS v_devices = ROW_COUNT;
  INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
  SELECT p_tenant, p_site, v_new, ed.device_id, 1, v_now FROM iam_v2.entitlement_devices ed WHERE ed.entitlement_id = v_new;
  WITH closed AS (
    UPDATE iam_v2.entitlement_device_authorizations da SET deauthorized_at = GREATEST(v_now, da.authorized_at)
     WHERE da.entitlement_id IN (au.grace_entitlement_id, v_super) AND da.deauthorized_at IS NULL
     RETURNING da.entitlement_id, da.device_id)
  UPDATE iam_v2.entitlement_devices ed SET status = 'DISCONNECTED', disconnected_reason = 'ROOM_MOVE_RESTORED'
    FROM closed WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;

  FOR v_sid IN SELECT s.id FROM iam_v2.sessions s
                WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.entitlement_id IN (au.grace_entitlement_id, v_super)
                  AND s.ended IS NULL AND s.state IN ('active','PENDING_ENFORCEMENT')
                ORDER BY s.id
  LOOP
    -- Switches just after the last recorded sample (an idle one no earlier than 10 minutes ago): later samples are the continuation's.
    PERFORM iam_v2.rebind_session_entitlement(v_sid, v_new, GREATEST(
      (SELECT b.bound_from FROM iam_v2.session_entitlement_bindings b WHERE b.session_id = v_sid AND b.bound_until IS NULL),
      (SELECT max(cp.prev_sampled_at) + interval '1 microsecond' FROM iam_v2.accounting_checkpoints cp WHERE cp.session_id = v_sid),
      v_now - interval '10 minutes'));
    v_sessions := v_sessions + 1;
  END LOOP;

  INSERT INTO iam_v2.room_move_restorations (tenant_id, site_id, stay_id, lifecycle_version, checkout_event_id, checkin_event_id,
                                             original_entitlement_id, grace_entitlement_id, superseded_entitlement_id,
                                             restored_entitlement_id, purchase_id, outcome, source, remaining_data_bytes, devices, sessions)
  VALUES (p_tenant, p_site, p_stay, p_episode, au.boundary_event_id, v_in, o.id, au.grace_entitlement_id, v_super, v_new, v_purchase,
          'RESTORED', p_source, v_remaining, v_devices, v_sessions);
  RETURN QUERY SELECT 'RESTORED'::text, v_new, v_devices, v_sessions;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.restore_room_move_access(uuid, uuid, uuid, integer, uuid, text, boolean) FROM PUBLIC;
COMMENT ON FUNCTION iam_v2.restore_room_move_access(uuid, uuid, uuid, integer, uuid, text, boolean) IS
  'Continues the package a checkout converted when the verified stay events show a room move (0213): same window, the data the '
  'original and the After Check-out Access left, the devices it held, no charge. Records one decision per episode.';

-- A full refund ends the access its purchase funded (0115); after a room move that access lives on in the continuation.
-- CREATE OR REPLACE keeps the function's owner and grants.
CREATE OR REPLACE FUNCTION iam_v2.p4_apply_refund_policy(p_tenant uuid, p_site uuid, p_charge uuid) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  ch record; se record; v_captured bigint; v_refunded bigint; v_charged_back bigint; v_ent uuid; v_status text; v_tr uuid;
  v_cont uuid; v_cont_status text;
BEGIN
  SELECT * INTO ch FROM iam_v2.payment_transactions
   WHERE id = p_charge AND tenant_id = p_tenant AND site_id = p_site AND transaction_type = 'CHARGE';
  IF NOT FOUND OR ch.status <> 'CAPTURED' THEN
    RETURN 'NOT_A_CAPTURED_CHARGE';
  END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = ch.settlement_id FOR UPDATE;
  v_captured := ch.amount_minor;
  SELECT COALESCE(sum(amount_minor) FILTER (WHERE transaction_type = 'REFUND'), 0),
         COALESCE(sum(amount_minor) FILTER (WHERE transaction_type = 'CHARGEBACK'), 0)
    INTO v_refunded, v_charged_back
    FROM iam_v2.payment_transactions WHERE parent_transaction_id = p_charge AND status = 'CAPTURED';

  IF v_charged_back > 0 THEN
    INSERT INTO iam_v2.card_reversal_reviews (tenant_id, site_id, settlement_id, charge_transaction_id, kind, provider_ref)
    VALUES (p_tenant, p_site, se.id, p_charge, 'CHARGEBACK', 'ledger')
    ON CONFLICT (charge_transaction_id, kind, provider_ref) DO NOTHING;
    RETURN 'CHARGEBACK_REVIEW';
  END IF;
  IF v_refunded <= 0 THEN
    RETURN 'NO_REFUND';
  END IF;
  IF v_refunded < v_captured THEN
    RETURN 'PARTIAL_NO_ACTION';
  END IF;

  SELECT id, status INTO v_ent, v_status FROM iam_v2.entitlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND purchase_id = se.purchase_id;
  IF v_ent IS NULL THEN
    RETURN 'NO_ENTITLEMENT';
  END IF;
  -- A room move continued this access under its own zero-price purchase (0213): the refund ends the live continuation it funded.
  WITH RECURSIVE chain AS (
    SELECT e.id, e.status, 0 AS depth FROM iam_v2.entitlements e WHERE e.id = v_ent
    UNION ALL
    SELECT n.id, n.status, chain.depth + 1 FROM iam_v2.entitlements n
      JOIN chain ON n.policy_snapshot->>'restores' = chain.id::text
     WHERE chain.depth < 50)
  SELECT c.id, c.status INTO v_cont, v_cont_status FROM chain c
   WHERE c.depth > 0 AND c.status IN ('PENDING','ACTIVE','SUSPENDED') LIMIT 1;
  IF v_cont IS NOT NULL THEN
    v_ent := v_cont; v_status := v_cont_status;
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.refund_access_terminations WHERE entitlement_id = v_ent) THEN
    RETURN 'ALREADY_ENDED';
  END IF;
  IF v_status = 'TERMINATED' THEN
    RETURN 'ALREADY_TERMINATED'; -- ended for another reason before the refund; nothing to record
  END IF;
  v_tr := iam_v2.p4_end_access_funded_by(v_ent, 'REVOKED');
  INSERT INTO iam_v2.refund_access_terminations
    (entitlement_id, tenant_id, site_id, settlement_id, charge_transaction_id, captured_minor, refunded_minor, transition_id)
  VALUES (v_ent, p_tenant, p_site, se.id, p_charge, v_captured, v_refunded, v_tr);
  RETURN 'ACCESS_ENDED';
END $$;

CREATE OR REPLACE FUNCTION iam_v2.p4_resolve_reversal_review(p_tenant uuid, p_site uuid, p_review uuid, p_resolution text, p_actor text, p_reason text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE rv record; v_ent uuid; v_cont uuid;
BEGIN
  IF p_resolution NOT IN ('KEEP_ACCESS','END_ACCESS') THEN
    RAISE EXCEPTION 'resolution must be KEEP_ACCESS or END_ACCESS' USING ERRCODE = 'check_violation';
  END IF;
  IF COALESCE(btrim(p_actor), '') = '' OR COALESCE(btrim(p_reason), '') = '' THEN
    RAISE EXCEPTION 'a resolution needs an operator and a reason' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO rv FROM iam_v2.card_reversal_reviews
   WHERE id = p_review AND tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF NOT FOUND THEN
    RETURN 'NOT_FOUND';
  END IF;
  IF rv.resolution IS NOT NULL THEN
    RETURN 'ALREADY_RESOLVED';
  END IF;
  IF p_resolution = 'END_ACCESS' THEN
    SELECT e.id INTO v_ent FROM iam_v2.settlements se JOIN iam_v2.entitlements e ON e.purchase_id = se.purchase_id
     WHERE se.id = rv.settlement_id;
    -- As for a refund: a room move's continuation (0213) is the access this payment still funds.
    WITH RECURSIVE chain AS (
      SELECT e.id, e.status, 0 AS depth FROM iam_v2.entitlements e WHERE e.id = v_ent
      UNION ALL
      SELECT n.id, n.status, chain.depth + 1 FROM iam_v2.entitlements n
        JOIN chain ON n.policy_snapshot->>'restores' = chain.id::text
       WHERE chain.depth < 50)
    SELECT c.id INTO v_cont FROM chain c WHERE c.depth > 0 AND c.status IN ('PENDING','ACTIVE','SUSPENDED') LIMIT 1;
    v_ent := COALESCE(v_cont, v_ent);
    IF v_ent IS NOT NULL THEN
      PERFORM iam_v2.p4_end_access_funded_by(v_ent, 'REVOKED');
    END IF;
  END IF;
  UPDATE iam_v2.card_reversal_reviews
     SET resolution = p_resolution, resolved_at = now(), resolved_by = btrim(p_actor), resolution_reason = btrim(p_reason)
   WHERE id = p_review;
  RETURN p_resolution;
END $$;
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.stay_package_purchase_refusal(uuid, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.purchase_renewal_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.room_move_restorations OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.room_move_restorations_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.stay_event_room(jsonb) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.stay_next_applied_checkin(uuid, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.entitlement_funding_reversed(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.restore_room_move_access(uuid, uuid, uuid, integer, uuid, text, boolean) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Repeated by deploy/gatep/svc-renewal-room-move-grants.sql (a Gate-P reconcile revokes and re-grants).
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.stay_package_purchase_refusal(uuid, uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.restore_room_move_access(uuid, uuid, uuid, integer, uuid, text, boolean) TO svc_pmsd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.room_move_restorations TO svc_edged;
  END IF;
END $g$;

COMMIT;
