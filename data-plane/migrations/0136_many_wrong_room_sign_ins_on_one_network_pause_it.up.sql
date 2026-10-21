-- 0136 -- MANY WRONG ROOM SIGN-INS ON ONE NETWORK PAUSE ROOM SIGN-IN THERE, AND STAFF CAN LET A GUEST THROUGH
-- (GDPR audit 2026-10-19, A4; Product-Owner decision 2026-10-19: "A, but also allow staff to confirm genuine
--  customers ... so that a genuine customer can attempt to log back in once staff confirm it's genuine".)
--
-- The per-device rule of 0068 is defeated by changing the device's MAC address: each new address starts a fresh
-- count, so room + surname could be guessed without limit, and a hit tells the guesser who is in which room. A
-- per-room or per-address limit was ruled out (0068: it locks the real guest out, and an address is shared), so
-- the limit sits on the NETWORK:
--
--   * when wrong room sign-ins on one guest network reach network_max_failures within network_window_seconds,
--     whichever devices they came from, room sign-in on that network is PAUSED for network_pause_seconds. Other
--     sign-in methods and other networks are unaffected. Hotel Admin shows the pause to staff;
--   * during the pause, staff can CONFIRM a genuine guest's device: that device may try again at once. It is a
--     permission to attempt, never access: the guest still signs in with their own details. Confirming also
--     ends the device's own 0068 restriction;
--   * staff can END a pause early (a false alarm). Both actions need a reason and are recorded with who did it;
--   * failures before a pause ended do not count towards the next one, so a pause is not re-triggered by the
--     burst it already answered. Attempts refused by the pause are not wrong credentials and do not count.
--
-- Three settings, defaults 30 failures / 600 s / 900 s, bounded, changed with a reason and the password, and
-- logged in their own append-only history like 0068's (docs/OPERATIONAL_SETTINGS_POLICY.md).
--
-- Personal data: a confirmation holds the device's MAC. Confirmations are removed one day after their pause
-- ends, and ended pauses (which hold no guest data) after 30 days, by guest_signin_network_prune(), which scd
-- runs every minute.

BEGIN;

-- 1. The settings, with their own change history.
CREATE TABLE IF NOT EXISTS iam_v2.site_guest_signin_network_policy (
  tenant_id              uuid NOT NULL,
  site_id                uuid NOT NULL,
  network_max_failures   integer NOT NULL DEFAULT 30,
  network_window_seconds integer NOT NULL DEFAULT 600,
  network_pause_seconds  integer NOT NULL DEFAULT 900,
  config_version         bigint NOT NULL DEFAULT 1,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id),
  CONSTRAINT guest_signin_network_policy_bounds CHECK (
    network_max_failures   BETWEEN 10 AND 1000 AND
    network_window_seconds BETWEEN 60 AND 3600 AND
    network_pause_seconds  BETWEEN 60 AND 3600)
);
COMMENT ON TABLE iam_v2.site_guest_signin_network_policy IS
  'When many wrong room sign-ins on one guest network pause room sign-in there (0136). No row means the '
  'approved defaults (30 / 600 s / 900 s), not "off".';

CREATE TABLE IF NOT EXISTS iam_v2.guest_signin_network_policy_changes (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL,
  site_id       uuid NOT NULL,
  changed_at    timestamptz NOT NULL DEFAULT now(),
  changed_by    text NOT NULL CHECK (length(btrim(changed_by)) > 0),
  change_reason text,
  old_network_max_failures   integer,
  old_network_window_seconds integer,
  old_network_pause_seconds  integer,
  new_network_max_failures   integer NOT NULL,
  new_network_window_seconds integer NOT NULL,
  new_network_pause_seconds  integer NOT NULL,
  new_config_version         bigint NOT NULL
);
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_policy_changes_append_only() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION 'iam_v2.guest_signin_network_policy_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.guest_signin_network_policy_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS guest_signin_network_policy_changes_append_only ON iam_v2.guest_signin_network_policy_changes;
CREATE TRIGGER guest_signin_network_policy_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.guest_signin_network_policy_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.guest_signin_network_policy_changes_append_only();

-- 2. Pauses and the devices staff confirmed during one.
CREATE TABLE IF NOT EXISTS iam_v2.guest_signin_network_pauses (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  guest_network_id   uuid NOT NULL,
  guest_network_name text,
  started_at         timestamptz NOT NULL DEFAULT now(),
  expires_at         timestamptz NOT NULL,
  failure_count      integer NOT NULL,
  ended_at           timestamptz,
  ended_by           text,
  end_reason         text,
  CHECK (expires_at > started_at),
  CHECK ((ended_at IS NULL) = (ended_by IS NULL))
);
CREATE INDEX IF NOT EXISTS guest_signin_network_pauses_lookup
  ON iam_v2.guest_signin_network_pauses (tenant_id, site_id, guest_network_id, expires_at DESC);

CREATE TABLE IF NOT EXISTS iam_v2.guest_signin_device_passes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  pause_id    uuid NOT NULL REFERENCES iam_v2.guest_signin_network_pauses(id) ON DELETE CASCADE,
  tenant_id   uuid NOT NULL,
  site_id     uuid NOT NULL,
  device_mac  macaddr NOT NULL,
  granted_at  timestamptz NOT NULL DEFAULT now(),
  granted_by  text NOT NULL CHECK (length(btrim(granted_by)) > 0),
  reason      text NOT NULL,
  UNIQUE (pause_id, device_mac)
);
COMMENT ON TABLE iam_v2.guest_signin_device_passes IS
  'A device staff confirmed as a genuine guest during a network pause (0136): it may attempt room sign-in while '
  'the pause lasts. A permission to attempt, never access. Removed one day after the pause ends.';

-- Counting by network needs its own index, or every wrong room sign-in scans the site's attempts.
CREATE INDEX IF NOT EXISTS sign_in_attempts_network_recent
  ON iam_v2.sign_in_attempts (tenant_id, site_id, guest_network_id, occurred_at DESC);

-- 3. The operations. No runtime role holds a table write; everything goes through these.

CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_policy_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (network_max_failures integer, network_window_seconds integer, network_pause_seconds integer,
                 config_version bigint, updated_at timestamptz, is_default boolean)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.network_max_failures, 30), COALESCE(s.network_window_seconds, 600),
         COALESCE(s.network_pause_seconds, 900), COALESCE(s.config_version, 1), s.updated_at, (s.tenant_id IS NULL)
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_guest_signin_network_policy s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;

CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_policy_set(
    p_tenant uuid, p_site uuid, p_max integer, p_window integer, p_pause integer, p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_version bigint; v_old iam_v2.site_guest_signin_network_policy;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 3 THEN
    RAISE EXCEPTION 'a reason is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_max IS NULL OR p_max < 10 OR p_max > 1000 THEN
    RAISE EXCEPTION 'wrong room sign-ins before a pause must be between 10 and 1000 (got %)', p_max;
  END IF;
  IF p_window IS NULL OR p_window < 60 OR p_window > 3600 THEN
    RAISE EXCEPTION 'the counting window must be between 60 and 3600 seconds (got %)', p_window;
  END IF;
  IF p_pause IS NULL OR p_pause < 60 OR p_pause > 3600 THEN
    RAISE EXCEPTION 'the pause must be between 60 and 3600 seconds (got %)', p_pause;
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('guest_signin_network_policy'), hashtext(p_site::text));
  SELECT * INTO v_old FROM iam_v2.site_guest_signin_network_policy s
   WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
  INSERT INTO iam_v2.site_guest_signin_network_policy AS s
    (tenant_id, site_id, network_max_failures, network_window_seconds, network_pause_seconds)
  VALUES (p_tenant, p_site, p_max, p_window, p_pause)
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET network_max_failures = EXCLUDED.network_max_failures,
         network_window_seconds = EXCLUDED.network_window_seconds,
         network_pause_seconds = EXCLUDED.network_pause_seconds,
         config_version = s.config_version + 1, updated_at = now()
  RETURNING s.config_version INTO v_version;
  INSERT INTO iam_v2.guest_signin_network_policy_changes
    (tenant_id, site_id, changed_by, change_reason, old_network_max_failures, old_network_window_seconds,
     old_network_pause_seconds, new_network_max_failures, new_network_window_seconds, new_network_pause_seconds,
     new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old.network_max_failures,
          v_old.network_window_seconds, v_old.network_pause_seconds, p_max, p_window, p_pause, v_version);
  RETURN v_version;
END $fn$;

-- The gate: is room sign-in paused on this network for this device right now?
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_gate(p_tenant uuid, p_site uuid, p_network uuid, p_mac macaddr)
  RETURNS TABLE (paused boolean, pause_id uuid, remaining_seconds integer)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT (p.id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM iam_v2.guest_signin_device_passes d
                                             WHERE d.pause_id = p.id AND d.device_mac = p_mac)),
         p.id,
         CASE WHEN p.id IS NOT NULL THEN GREATEST(1, CEIL(EXTRACT(EPOCH FROM (p.expires_at - now())))::int) END
    FROM (SELECT 1) one
    LEFT JOIN LATERAL (
      SELECT x.id, x.expires_at FROM iam_v2.guest_signin_network_pauses x
       WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.guest_network_id = p_network
         AND x.ended_at IS NULL AND x.expires_at > now()
       ORDER BY x.expires_at DESC LIMIT 1) p ON true;
$fn$;

-- One wrong room sign-in on a network, after its attempt row is written. Starts a pause when the count is
-- reached. Serialised per network, and never starts a second pause while one is running.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_note_network_failure(
    p_tenant uuid, p_site uuid, p_network uuid, p_network_name text)
  RETURNS TABLE (paused_now boolean, expires_at timestamptz, failure_count integer)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_max int; v_window int; v_pause int; v_reset timestamptz; v_count int; v_expires timestamptz;
BEGIN
  IF p_network IS NULL THEN
    RETURN QUERY SELECT false, NULL::timestamptz, 0;
    RETURN;
  END IF;
  SELECT g.network_max_failures, g.network_window_seconds, g.network_pause_seconds
    INTO v_max, v_window, v_pause FROM iam_v2.guest_signin_network_policy_get(p_tenant, p_site) g;
  PERFORM pg_advisory_xact_lock(hashtext('guest_signin_network_pause'), hashtext(p_network::text));
  IF EXISTS (SELECT 1 FROM iam_v2.guest_signin_network_pauses x
              WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.guest_network_id = p_network
                AND x.ended_at IS NULL AND x.expires_at > now()) THEN
    RETURN QUERY SELECT false, NULL::timestamptz, 0;
    RETURN;
  END IF;
  -- Failures up to the end of the last pause were already answered by it.
  SELECT COALESCE(max(LEAST(COALESCE(x.ended_at, x.expires_at), x.expires_at)), '-infinity'::timestamptz)
    INTO v_reset FROM iam_v2.guest_signin_network_pauses x
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.guest_network_id = p_network;
  SELECT count(*) INTO v_count FROM iam_v2.sign_in_attempts a
   WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND a.guest_network_id = p_network
     AND a.result IN ('CREDENTIAL_MISMATCH','ROOM_NOT_IN_MIRROR')
     AND a.occurred_at > now() - make_interval(secs => v_window) AND a.occurred_at > v_reset;
  IF v_count < v_max THEN
    RETURN QUERY SELECT false, NULL::timestamptz, v_count;
    RETURN;
  END IF;
  v_expires := now() + make_interval(secs => v_pause);
  INSERT INTO iam_v2.guest_signin_network_pauses (tenant_id, site_id, guest_network_id, guest_network_name,
                                                   expires_at, failure_count)
  VALUES (p_tenant, p_site, p_network, NULLIF(p_network_name, ''), v_expires, v_count);
  RETURN QUERY SELECT true, v_expires, v_count;
END $fn$;

-- Staff: end a pause early (a false alarm).
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_end_network_pause(
    p_tenant uuid, p_site uuid, p_pause uuid, p_operator text, p_reason text)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_rows integer;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' OR p_reason IS NULL OR length(btrim(p_reason)) < 3 THEN
    RAISE EXCEPTION 'an operator and a reason are required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  UPDATE iam_v2.guest_signin_network_pauses
     SET ended_at = now(), ended_by = btrim(p_operator), end_reason = btrim(p_reason)
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_pause AND ended_at IS NULL AND expires_at > now();
  GET DIAGNOSTICS v_rows = ROW_COUNT;
  RETURN v_rows;
END $fn$;

-- Staff: confirm a genuine guest's device during a pause. It may then attempt; its own 0068 restriction ends.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_confirm_device(
    p_tenant uuid, p_site uuid, p_pause uuid, p_mac macaddr, p_operator text, p_reason text)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' OR p_reason IS NULL OR length(btrim(p_reason)) < 3 THEN
    RAISE EXCEPTION 'an operator and a reason are required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_mac IS NULL OR NOT EXISTS (
       SELECT 1 FROM iam_v2.guest_signin_network_pauses x
        WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.id = p_pause
          AND x.ended_at IS NULL AND x.expires_at > now()) THEN
    RETURN 0;
  END IF;
  INSERT INTO iam_v2.guest_signin_device_passes (pause_id, tenant_id, site_id, device_mac, granted_by, reason)
  VALUES (p_pause, p_tenant, p_site, p_mac, btrim(p_operator), btrim(p_reason))
  ON CONFLICT (pause_id, device_mac) DO NOTHING;
  UPDATE iam_v2.guest_signin_restrictions
     SET released_at = now(), released_by = btrim(p_operator), release_reason = btrim(p_reason),
         counter_reset_at = now(), updated_at = now()
   WHERE tenant_id = p_tenant AND site_id = p_site AND device_mac = p_mac
     AND released_at IS NULL AND expires_at > now();
  RETURN 1;
END $fn$;

-- Staff: the devices that tried room sign-in on the paused network, to pick the guest standing at the desk.
-- The room is what was TYPED, unverified; nothing else of what was typed is returned.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_pause_devices(p_tenant uuid, p_site uuid, p_pause uuid)
  RETURNS TABLE (device_mac text, last_submitted_room text, last_attempt_at timestamptz, attempts integer,
                 confirmed boolean)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  WITH p AS (
    SELECT x.id, x.guest_network_id, x.started_at FROM iam_v2.guest_signin_network_pauses x
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.id = p_pause
       AND x.ended_at IS NULL AND x.expires_at > now())
  SELECT a.device_mac::text,
         (array_agg(a.submitted_room ORDER BY a.occurred_at DESC))[1],
         max(a.occurred_at), count(*)::int,
         EXISTS (SELECT 1 FROM iam_v2.guest_signin_device_passes d WHERE d.pause_id = p.id AND d.device_mac = a.device_mac)
    FROM p JOIN iam_v2.sign_in_attempts a
      ON a.tenant_id = p_tenant AND a.site_id = p_site AND a.guest_network_id = p.guest_network_id
     AND a.occurred_at > p.started_at - interval '30 minutes' AND a.device_mac IS NOT NULL
   GROUP BY a.device_mac, p.id
   ORDER BY max(a.occurred_at) DESC
   LIMIT 200;
$fn$;

-- The minute sweep: confirmations a day after their pause, pauses 30 days after.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_prune() RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE n1 integer; n2 integer;
BEGIN
  DELETE FROM iam_v2.guest_signin_device_passes d USING iam_v2.guest_signin_network_pauses x
   WHERE d.pause_id = x.id AND LEAST(COALESCE(x.ended_at, x.expires_at), x.expires_at) < now() - interval '1 day';
  GET DIAGNOSTICS n1 = ROW_COUNT;
  DELETE FROM iam_v2.guest_signin_network_pauses x
   WHERE LEAST(COALESCE(x.ended_at, x.expires_at), x.expires_at) < now() - interval '30 days';
  GET DIAGNOSTICS n2 = ROW_COUNT;
  RETURN n1 + n2;
END $fn$;

-- 4. Privileges (mirrored in deploy/gatep/svc-signin-network-pause-grants.sql).
REVOKE ALL ON iam_v2.site_guest_signin_network_policy, iam_v2.guest_signin_network_policy_changes,
              iam_v2.guest_signin_network_pauses, iam_v2.guest_signin_device_passes FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_signin_network_policy_get(uuid,uuid),
                       iam_v2.guest_signin_network_policy_set(uuid,uuid,integer,integer,integer,text,text),
                       iam_v2.guest_signin_network_gate(uuid,uuid,uuid,macaddr),
                       iam_v2.guest_signin_note_network_failure(uuid,uuid,uuid,text),
                       iam_v2.guest_signin_end_network_pause(uuid,uuid,uuid,text,text),
                       iam_v2.guest_signin_confirm_device(uuid,uuid,uuid,macaddr,text,text),
                       iam_v2.guest_signin_network_pause_devices(uuid,uuid,uuid),
                       iam_v2.guest_signin_network_prune() FROM PUBLIC;
DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    -- Enforcement: ask the gate, report a wrong room sign-in, sweep old confirmations. Nothing else.
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_gate(uuid,uuid,uuid,macaddr) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_note_network_failure(uuid,uuid,uuid,text) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_prune() TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- Staff: read and change the settings, see the pauses, end one, confirm a guest's device.
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_policy_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_policy_set(uuid,uuid,integer,integer,integer,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_end_network_pause(uuid,uuid,uuid,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_confirm_device(uuid,uuid,uuid,macaddr,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_pause_devices(uuid,uuid,uuid) TO svc_edged;
    GRANT SELECT ON iam_v2.guest_signin_network_pauses, iam_v2.guest_signin_network_policy_changes TO svc_edged;
  END IF;
END $grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.site_guest_signin_network_policy OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.guest_signin_network_policy_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.guest_signin_network_pauses OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.guest_signin_device_passes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_network_policy_changes_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_network_policy_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_network_policy_set(uuid,uuid,integer,integer,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_network_gate(uuid,uuid,uuid,macaddr) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_note_network_failure(uuid,uuid,uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_end_network_pause(uuid,uuid,uuid,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_confirm_device(uuid,uuid,uuid,macaddr,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_network_pause_devices(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_network_prune() OWNER TO iam_v2_owner';
  END IF;
END $own$;

COMMIT;
