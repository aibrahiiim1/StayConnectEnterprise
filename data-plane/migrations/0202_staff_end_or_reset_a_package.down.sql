-- 0202 down: removes staff End/Reset Package and restores the 0193 After Check-out device guard. The action rows are dropped with
-- their table; the entitlements and purchases a reset created stay (they are ordinary entitlement history).
BEGIN;

DROP TRIGGER IF EXISTS staff_ended_benefit_guard ON iam_v2.entitlements;
DROP FUNCTION IF EXISTS iam_v2.staff_ended_benefit_guard();
DROP FUNCTION IF EXISTS iam_v2.package_ended_by_staff(uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.admin_reset_package(uuid, uuid, uuid, uuid, text, uuid);
DROP FUNCTION IF EXISTS iam_v2.admin_end_package(uuid, uuid, uuid, uuid, text, uuid);
DROP FUNCTION IF EXISTS iam_v2.package_admin_history(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.package_control_list(uuid, uuid, text, uuid);
DROP FUNCTION IF EXISTS iam_v2.package_control_preview(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.package_reset_window(uuid, timestamptz);
DROP FUNCTION IF EXISTS iam_v2.package_reset_refusal(uuid);
DROP FUNCTION IF EXISTS iam_v2.package_admin_check_actor(uuid, uuid, text);
DELETE FROM iam_v2.scrub_registry WHERE table_name = 'iam_v2.package_admin_actions';
DROP TABLE IF EXISTS iam_v2.package_admin_actions;
DROP FUNCTION IF EXISTS iam_v2.package_admin_actions_append_only();

CREATE OR REPLACE FUNCTION iam_v2.after_checkout_device_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp' AS $$
DECLARE v_mode text;
BEGIN
  SELECT e.end_mode INTO v_mode FROM iam_v2.entitlements e WHERE e.id = NEW.entitlement_id;
  IF v_mode IS DISTINCT FROM 'GRACE_AFTER_CHECKOUT' THEN RETURN NEW; END IF;
  IF TG_OP = 'INSERT' THEN
    IF NEW.grandfathered THEN RETURN NEW; END IF;
    IF EXISTS (SELECT 1 FROM iam_v2.entitlement_devices d
                WHERE d.entitlement_id = NEW.entitlement_id AND d.device_id = NEW.device_id) THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'AFTER_CHECKOUT_NEW_DEVICE_REJECTED: a device that was not authorised on the stay before check-out cannot be added to its After Check-out entitlement'
      USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status IS DISTINCT FROM 'AUTHORIZED' AND NEW.status = 'AUTHORIZED'
     AND COALESCE(OLD.disconnected_reason, '') <> 'ENTITLEMENT_SUSPENDED' THEN
    RAISE EXCEPTION 'AFTER_CHECKOUT_NEW_DEVICE_REJECTED: a device that left an After Check-out entitlement cannot rejoin it'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION iam_v2.after_checkout_device_guard() FROM PUBLIC;

-- The erasure functions as 0147 declared them.
CREATE OR REPLACE FUNCTION iam_v2.anonymise_guest_account(p_account uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.account_email_tokens
     SET last_send_error = NULL, token_hmac = CASE WHEN token_hmac IS NOT NULL THEN public.gen_random_bytes(32) END,
         superseded_at = CASE WHEN used_at IS NULL AND superseded_at IS NULL THEN now() ELSE superseded_at END
   WHERE account_id = p_account;
  UPDATE iam_v2.guest_access_accounts
     SET username = iam_v2.anonymised_token() || '@anonymised.removed-' || left(replace(id::text, '-', ''), 8) || '.invalid',
         display_name = NULL, notes = NULL, created_by_device_mac = NULL, enabled = false,
         -- The link to the reservation, the password hash, the sign-in history and the dates of the account go with the name:
         -- the reservation the account was made for would otherwise name the person a join away.
         stay_id = NULL, password_hash = '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', failed_attempts = 0, locked_until = NULL, last_login_at = NULL,
         login_count = 0, created_at = date_trunc('month', created_at), email_verified_at = NULL, verification_deadline = NULL,
         verification_reminder_sent_at = NULL, grace_until = NULL, valid_from = NULL, valid_until = NULL
   WHERE id = p_account AND username NOT LIKE 'anon-%@anonymised.removed-%';
  -- What staff typed about this account's access.
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  PERFORM set_config('iam_v2.scrub', '', true);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_guest_principal(p_principal uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.guest_principals SET display_name = NULL WHERE id = p_principal AND display_name IS NOT NULL;
  UPDATE iam_v2.guest_principal_identities SET factor_value_norm = iam_v2.anonymised_token(), attrs = '{}'::jsonb
   WHERE guest_principal_id = p_principal AND NOT iam_v2.is_anon_token(factor_value_norm);
  UPDATE iam_v2.guest_principal_identities SET verified_at = date_trunc('month', verified_at)
   WHERE guest_principal_id = p_principal AND verified_at IS NOT NULL AND verified_at <> date_trunc('month', verified_at);
  -- A remembered-device secret is revoked AND its value replaced: a revoked row must not keep a usable shape of it.
  UPDATE iam_v2.principal_device_credentials
     SET revoked_at = COALESCE(revoked_at, now()), secret_hmac = public.gen_random_bytes(32), last_used_at = NULL
   WHERE guest_principal_id = p_principal AND (revoked_at IS NULL OR last_used_at IS NOT NULL);
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  PERFORM set_config('iam_v2.scrub', '', true);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_stay(p_stay uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stays
     SET external_reservation_id = iam_v2.anonymised_token(), external_stay_identity = iam_v2.anonymised_token(),
         normalized_room_number = NULL, vip = NULL, travel_agent = NULL, rate_plan = NULL, room_type = NULL,
         -- The dates stay as months and the times as days: enough for a report, not enough to find the booking in the PMS.
         arrival = date_trunc('month', arrival)::date, departure = date_trunc('month', departure)::date,
         -- (only on a finished stay: an open one is still governed by the occupancy-evidence rules)
         effective_checkout_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', effective_checkout_at) ELSE effective_checkout_at END,
         occupancy_evidence_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', occupancy_evidence_at) ELSE occupancy_evidence_at END,
         occupancy_ingested_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', occupancy_ingested_at) ELSE occupancy_ingested_at END
   WHERE id = p_stay AND NOT iam_v2.is_anon_token(external_reservation_id);
  -- What staff typed about the stay, and the post-stay secret.
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.stay_posting_blocks SET note = CASE WHEN note IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END,
         cleared_reason = CASE WHEN cleared_reason IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE stay_id = p_stay AND ((note IS NOT NULL AND note <> 'Removed after the staff-notes period') OR (cleared_reason IS NOT NULL AND cleared_reason <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.pms_case_resolutions SET note = 'Removed after the staff-notes period', evidence_ref = CASE WHEN evidence_ref IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE stay_id = p_stay AND ((note IS NOT NULL AND note <> 'Removed after the staff-notes period') OR (evidence_ref IS NOT NULL AND evidence_ref <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.entitlement_transfers SET reason = 'Removed after the staff-notes period'
   WHERE (from_stay_id = p_stay OR to_stay_id = p_stay) AND reason IS NOT NULL AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  UPDATE iam_v2.post_stay_profiles
     SET pin_hash = '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', revoke_reason = CASE WHEN revoke_reason IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE origin_stay_id = p_stay AND (pin_hash <> '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' OR (revoke_reason IS NOT NULL AND revoke_reason <> 'Removed after the staff-notes period'));
  PERFORM set_config('iam_v2.scrub', '', true);
  UPDATE iam_v2.stay_guests
     SET external_guest_id = NULL, first_name_norm = NULL, last_name_norm = NULL, display_name = NULL,
         pin_hash = NULL
   WHERE stay_id = p_stay
     AND num_nonnulls(external_guest_id, first_name_norm, last_name_norm, display_name, pin_hash) > 0;
  UPDATE iam_v2.stay_events
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE stay_id = p_stay AND processing_status <> 'PENDING'
     AND payload IS DISTINCT FROM '{"anonymised": true}'::jsonb;
  -- The copy of the names and room migration 0066 kept as rollback evidence goes with the stay
  -- (GDPR audit 2026-10-19, A6). Not every appliance has the table, so it is looked up first.
  IF to_regclass('iam_v2.backfill_0066_identity_text') IS NOT NULL THEN
    EXECUTE $q$
      UPDATE iam_v2.backfill_0066_identity_text x
         SET prior_first_name = NULL, prior_last_name = NULL, prior_room = NULL
       WHERE (x.row_id = $1 OR x.row_id IN (SELECT sg.id FROM iam_v2.stay_guests sg WHERE sg.stay_id = $1))
         AND num_nonnulls(x.prior_first_name, x.prior_last_name, x.prior_room) > 0
    $q$ USING p_stay;
  END IF;
END $$;

COMMIT;
