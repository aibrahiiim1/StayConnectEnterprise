-- THE HOTEL DECIDES HOW MANY WRONG GUESSES ARE TOO MANY.
--
-- WHAT IS MISSING TODAY. Guest room sign-in has no rate control that a property can see or change. There is a
-- durable throttle in the appliance (public.auth_throttle_buckets), and it is the right mechanism for what it
-- was built for and the wrong one for this: its limits are Go literals that need a deployment to change, it
-- is keyed on an irreversible HMAC so nothing can render WHICH device is restricted, it carries no tenant or
-- site column, and its window is a fixed clock boundary rather than a rolling one. An operator cannot answer
-- "who is restricted, why, and can you let them back in" from a table that is unreadable by construction.
--
-- WHAT THIS ADDS.
--
--   1. iam_v2.site_guest_signin_protection  — three numbers per site: how many wrong credentials, over what
--      window, refused for how long. Editable from Hotel Admin, bounded by CHECK, versioned.
--   2. iam_v2.guest_signin_restrictions     — one row per DEVICE per site, readable, releasable, auditable.
--   3. six scoped operations                — the whole policy, so that no service needs UPDATE on either
--      table and both the enforcement and the screen read the same numbers from the same place.
--
-- ABSENCE OF A SETTINGS ROW MEANS THE APPROVED DEFAULTS, NOT "OFF". Five wrong credentials in sixty seconds,
-- refused for sixty. That is deliberate: a policy that had to be switched on would be off on every appliance
-- that nobody remembered to configure, which is every appliance. There is no enable flag for the same reason.
--
-- WHAT THE COUNTER COUNTS, and the Product Owner's rule stated where it is enforced rather than only where it
-- is described: CREDENTIAL_MISMATCH and ROOM_NOT_IN_MIRROR. Nothing else. A technical failure, an unavailable
-- service, a database error or an inability to evaluate the mirror is NEVER a wrong credential -- during an
-- outage every guest in the building would otherwise be restricted within five taps, at the exact moment the
-- desk is least able to cope. The predicate below and internal/signinattempt.CountsAsCredentialFailure name
-- the same two codes, and a test walks every result code so a third cannot be added to one and not the other.
--
-- WHY THE COUNT IS NOT ITS OWN TALLY. It is derived from iam_v2.sign_in_attempts, which already records every
-- submission with its device, its result and its timestamp. A separate counter would be a second number about
-- the same events, and the first time the two disagreed the operator screen and the policy would be arguing
-- about a guest who is standing at the desk. Deriving it also makes the window genuinely ROLLING rather than
-- a fixed boundary a guesser can straddle.
--
-- WHAT DOES NOT CHANGE. No existing table, function, trigger or grant is altered. Existing authorised
-- sessions are untouched: nothing here reads or writes iam_v2.sessions or iam_v2.entitlements.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The settings.
--
-- Site-scoped and typed, in the idiom iam_v2.site_checkout_grace_config already uses: real columns with real
-- CHECK bounds rather than loose JSON, so an impossible policy cannot be stored even by a caller that skipped
-- the validation. config_version increments on every change and is what an operator screen can show.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_guest_signin_protection (
  tenant_id                  uuid NOT NULL,
  site_id                    uuid NOT NULL,
  max_failed_attempts        integer NOT NULL DEFAULT 5,
  observation_window_seconds integer NOT NULL DEFAULT 60,
  restriction_seconds        integer NOT NULL DEFAULT 60,
  config_version             bigint  NOT NULL DEFAULT 1,
  updated_at                 timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id),
  -- The bounds mirror internal/signinattempt exactly. The LOWER ones matter more than the upper: a threshold
  -- below three restricts a guest for ordinary typing (a trailing space, then the correction, is already two),
  -- and a window shorter than the time a guest takes to read a message and retype makes the control invisible
  -- to an attacker while merely confusing the guest. The upper bounds exist so a mistyped number cannot lock
  -- a property's guests out for a day.
  CONSTRAINT guest_signin_protection_bounds CHECK (
    max_failed_attempts        BETWEEN 3  AND 20   AND
    observation_window_seconds BETWEEN 30 AND 3600 AND
    restriction_seconds        BETWEEN 30 AND 3600
  ),
  CONSTRAINT guest_signin_protection_version CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.site_guest_signin_protection IS
  'Per-site guest sign-in protection policy: how many wrong-credential submissions from one device, over what '
  'rolling window, are refused for how long. ABSENCE OF A ROW MEANS THE APPROVED DEFAULTS (5 / 60s / 60s), '
  'not "disabled" -- there is no enable flag, because a policy that must be switched on is off wherever '
  'nobody remembered. Read by the enforcement functions and by the operator API through '
  'iam_v2.guest_signin_protection_get, so enforcement, API and UI cannot disagree about the current values.';
COMMENT ON COLUMN iam_v2.site_guest_signin_protection.observation_window_seconds IS
  'The ROLLING window. Failures older than this stop counting continuously, not at a clock boundary: a guest '
  'who mistypes twice at 10:00:59 and three times at 10:01:01 is one run of five, not two clean slates.';


-- ---------------------------------------------------------------------------------------------------------
-- 1b. The policy's own change log, append-only.
--
-- WHY A SEPARATE TABLE WHEN edged ALREADY WRITES public.audit_log. Because they answer different questions and
-- neither substitutes for the other: the operator audit answers "what did this person do in the admin UI",
-- and this answers "how did this site's protection policy reach the numbers it has". The second question is
-- the one asked after an incident, and it must be answerable even if the operator log has rolled.
--
-- MANDATORY BY PRIVILEGE, NOT BY CONVENTION. The setting write and this insert happen inside ONE definer
-- function, and no runtime role holds UPDATE on the settings table -- so there is no privilege that would
-- move the policy without recording who moved it and what it was before. That is the pattern
-- iam_v2.appliance_product_setting_changes already establishes, and it matters more here: these three numbers
-- are a security control, and the interesting change is always the one that weakens it.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.guest_signin_protection_changes (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now(),
  changed_by text NOT NULL
    CONSTRAINT guest_signin_protection_changes_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason text,

  -- NULL on the first change for a site: there was no stored policy, the defaults were in force, and saying
  -- "it was 5" would invent a row that never existed.
  old_max_failed_attempts        integer,
  old_observation_window_seconds integer,
  old_restriction_seconds        integer,

  new_max_failed_attempts        integer NOT NULL,
  new_observation_window_seconds integer NOT NULL,
  new_restriction_seconds        integer NOT NULL,
  new_config_version             bigint  NOT NULL
);

CREATE INDEX IF NOT EXISTS guest_signin_protection_changes_lookup
  ON iam_v2.guest_signin_protection_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.guest_signin_protection_changes_append_only() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION 'iam_v2.guest_signin_protection_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.guest_signin_protection_changes_append_only() FROM PUBLIC;

DROP TRIGGER IF EXISTS guest_signin_protection_changes_append_only
  ON iam_v2.guest_signin_protection_changes;
CREATE TRIGGER guest_signin_protection_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.guest_signin_protection_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.guest_signin_protection_changes_append_only();

COMMENT ON TABLE iam_v2.guest_signin_protection_changes IS
  'Append-only history of every change to a site''s guest sign-in protection policy, written in the SAME '
  'transaction as the change by iam_v2.guest_signin_protection_set. No runtime role holds UPDATE on the '
  'settings table, so a policy cannot move without this record: the audit is mandatory by privilege rather '
  'than by convention.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. The restrictions.
--
-- ONE ROW PER DEVICE PER SITE, and the row outlives the restriction it currently carries: it is also where
-- counter_reset_at lives, which is how "a success clears the counter", "an expiry starts a fresh counter" and
-- "a manual release starts a fresh counter" are all one mechanism instead of three.
--
-- SCOPED TO THE DEVICE, NOT THE ROOM AND NOT THE ADDRESS. Restricting a room number locks out the guest who
-- actually lives there while the attacker -- who chose that number -- moves to the next one; it makes denial
-- of service trivial. Restricting an address restricts a floor, because a guest network NATs. The device is
-- the narrowest thing the appliance can attribute an attempt to and it is the thing doing the guessing.
--
-- HONEST LIMIT, recorded here rather than only in a comment nobody reads: device_mac is the address the
-- APPLIANCE read from its own neighbour table, never a value the client sent, so no cookie, request id or
-- header moves it. It is not unspoofable. Someone on the guest VLAN can change their MAC and get a fresh
-- counter. What this buys is that casual enumeration stops being free, that no guest is restricted by another
-- guest's behaviour, and that every restriction is attributable and releasable by a named human.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.guest_signin_restrictions (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  device_mac macaddr NOT NULL,

  guest_network_id   uuid,
  guest_network_name text,
  -- UNVERIFIED INPUT. The room this device last typed -- what somebody entered, never a statement about who
  -- they are or where they are staying. Every surface that shows it says so; an operator who read it as an
  -- identity would be identifying a person by a string an attacker chose.
  last_submitted_room text,

  -- The current restriction, when there is one. All three are NULL together on a row that exists only to
  -- carry counter_reset_at.
  restricted_at  timestamptz,
  expires_at     timestamptz,
  failure_count  integer,
  reason         text
    CONSTRAINT guest_signin_restrictions_reason_check
    CHECK (reason IS NULL OR reason IN ('FAILED_CREDENTIAL_THRESHOLD')),

  -- Failures at or before this instant do not count. Set when a restriction is created (to its own expiry, so
  -- the fresh counter begins the moment the refusal ends), when a guest succeeds, and when an operator
  -- releases.
  --
  -- THE DEFAULT IS -infinity, NOT now(), AND THAT IS THE WHOLE CORRECTNESS OF THE COUNT. A device is first
  -- seen here at the moment of its first wrong credential, so a default of now() would sit microseconds
  -- AFTER the attempt that created the row and exclude it -- and every threshold would silently be one
  -- higher than the number the operator configured. A new device has nothing to exclude yet, and -infinity
  -- says exactly that.
  counter_reset_at timestamptz NOT NULL DEFAULT '-infinity'::timestamptz,

  released_at     timestamptz,
  released_by     text,
  release_reason  text,

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT guest_signin_restrictions_device_key UNIQUE (tenant_id, site_id, device_mac),
  CONSTRAINT guest_signin_restrictions_all_or_none CHECK (
    (restricted_at IS NULL AND expires_at IS NULL AND failure_count IS NULL AND reason IS NULL)
    OR
    (restricted_at IS NOT NULL AND expires_at IS NOT NULL AND failure_count IS NOT NULL AND reason IS NOT NULL)
  )
);

-- The operator list is "this site, still active, soonest to expire".
CREATE INDEX IF NOT EXISTS guest_signin_restrictions_active
  ON iam_v2.guest_signin_restrictions (tenant_id, site_id, expires_at DESC);

-- THE COUNTING DRIVER. Without it every wrong credential scans the whole attempts table for that site, which
-- is how a cheap policy check becomes the slowest thing on the guest authentication path.
CREATE INDEX IF NOT EXISTS sign_in_attempts_device_recent
  ON iam_v2.sign_in_attempts (tenant_id, site_id, device_mac, occurred_at DESC);

COMMENT ON TABLE iam_v2.guest_signin_restrictions IS
  'One row per guest device per site. Carries the current restriction when there is one, and always carries '
  'counter_reset_at -- the instant before which failures no longer count, which is how expiry, a successful '
  'sign-in and a manual release all start a fresh counter through one mechanism. Scoped to the DEVICE, never '
  'the room (which would lock out the guest who lives there) and never the address (which on a NATed guest '
  'network is a floor).';
COMMENT ON COLUMN iam_v2.guest_signin_restrictions.device_mac IS
  'The hardware address the APPLIANCE read from its own neighbour table for the request source -- never a '
  'client-supplied value, so no cookie, request id or header can move it. NOT unspoofable: a device on the '
  'guest VLAN can change its MAC and obtain a fresh counter.';
COMMENT ON COLUMN iam_v2.guest_signin_restrictions.last_submitted_room IS
  'UNVERIFIED INPUT: the room this device last typed. Never treat it as the identity of a person.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. The operations.
--
-- Every write goes through one of these, so neither scd nor edged holds UPDATE on either table. That is the
-- same reasoning as the sign-in attempt record: a service able to rewrite a restriction is a service able to
-- rewrite the evidence of its own refusals, and a service able to rewrite the policy could quietly disable
-- the control it is subject to.
-- ---------------------------------------------------------------------------------------------------------

-- 3a. The effective policy: the row, or the approved defaults when there is no row.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_protection_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (max_failed_attempts integer, observation_window_seconds integer,
                 restriction_seconds integer, config_version bigint, updated_at timestamptz,
                 is_default boolean)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.max_failed_attempts, 5),
         COALESCE(s.observation_window_seconds, 60),
         COALESCE(s.restriction_seconds, 60),
         COALESCE(s.config_version, 1),
         s.updated_at,
         (s.tenant_id IS NULL)
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_guest_signin_protection s
      ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;

COMMENT ON FUNCTION iam_v2.guest_signin_protection_get(uuid,uuid) IS
  'The site''s EFFECTIVE guest sign-in protection policy: the stored row, or the approved defaults when no '
  'row exists. is_default says which, so a screen can tell an operator whether anyone has ever changed it. '
  'This is the single source the enforcement functions, the operator API and the UI all read.';

-- 3b. Changing the policy. Bounds are re-checked here and again by the table CHECK, because a caller that
-- skipped its own validation must still be unable to store a policy that would hurt guests.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_protection_set(
    p_tenant uuid, p_site uuid, p_max integer, p_window integer, p_restriction integer,
    p_operator text, p_reason text DEFAULT NULL)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_version bigint; v_old_max int; v_old_window int; v_old_block int;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required: "somebody changed it" is not an audit record'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_max IS NULL OR p_max < 3 OR p_max > 20 THEN
    RAISE EXCEPTION 'maximum failed attempts must be between 3 and 20 (got %)', p_max;
  END IF;
  IF p_window IS NULL OR p_window < 30 OR p_window > 3600 THEN
    RAISE EXCEPTION 'the observation window must be between 30 and 3600 seconds (got %)', p_window;
  END IF;
  IF p_restriction IS NULL OR p_restriction < 30 OR p_restriction > 3600 THEN
    RAISE EXCEPTION 'the restriction duration must be between 30 and 3600 seconds (got %)', p_restriction;
  END IF;

  -- Serialise concurrent edits on this site, so two operators saving at once produce two ordered change rows
  -- rather than one silently overwriting the other's audit.
  PERFORM pg_advisory_xact_lock(hashtext('guest_signin_protection'), hashtext(p_site::text));

  SELECT s.max_failed_attempts, s.observation_window_seconds, s.restriction_seconds
    INTO v_old_max, v_old_window, v_old_block
    FROM iam_v2.site_guest_signin_protection s
   WHERE s.tenant_id = p_tenant AND s.site_id = p_site
     FOR UPDATE;

  INSERT INTO iam_v2.site_guest_signin_protection AS s
    (tenant_id, site_id, max_failed_attempts, observation_window_seconds, restriction_seconds)
  VALUES (p_tenant, p_site, p_max, p_window, p_restriction)
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET max_failed_attempts        = EXCLUDED.max_failed_attempts,
         observation_window_seconds = EXCLUDED.observation_window_seconds,
         restriction_seconds        = EXCLUDED.restriction_seconds,
         config_version             = s.config_version + 1,
         updated_at                 = now()
  RETURNING s.config_version INTO v_version;

  INSERT INTO iam_v2.guest_signin_protection_changes
    (tenant_id, site_id, changed_by, change_reason,
     old_max_failed_attempts, old_observation_window_seconds, old_restriction_seconds,
     new_max_failed_attempts, new_observation_window_seconds, new_restriction_seconds, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), NULLIF(btrim(COALESCE(p_reason,'')),''),
          v_old_max, v_old_window, v_old_block,
          p_max, p_window, p_restriction, v_version);

  RETURN v_version;
END $fn$;

COMMENT ON FUNCTION iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text) IS
  'Stores a validated guest sign-in protection policy and returns its new config_version. Takes effect on the '
  'NEXT decision -- the enforcement functions read this table on every evaluation, so no restart, rebuild or '
  'deployment is involved. Restrictions already in force keep the expiry they were recorded with; shortening '
  'the duration does not retroactively free anyone, and lengthening it does not retroactively extend them.';

-- 3c. The gate: is this device refused right now? A pure read, called before any evidence is evaluated.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_gate(p_tenant uuid, p_site uuid, p_mac macaddr)
  RETURNS TABLE (restricted boolean, expires_at timestamptz, remaining_seconds integer)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(r.expires_at > now() AND r.released_at IS NULL, false),
         CASE WHEN r.expires_at > now() AND r.released_at IS NULL THEN r.expires_at END,
         CASE WHEN r.expires_at > now() AND r.released_at IS NULL
              THEN GREATEST(1, CEIL(EXTRACT(EPOCH FROM (r.expires_at - now())))::int) END
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.guest_signin_restrictions r
      ON r.tenant_id = p_tenant AND r.site_id = p_site AND r.device_mac = p_mac;
$fn$;

COMMENT ON FUNCTION iam_v2.guest_signin_gate(uuid,uuid,macaddr) IS
  'Whether this device is currently refused, and for how much longer. remaining_seconds is the SERVER''s '
  'answer and is what the guest is shown counting down -- a browser that ignores it gains nothing, because '
  'this same gate refuses the next submission.';

-- 3d. Recording a wrong credential, and creating the restriction when the threshold is reached.
--
-- ATOMIC BY CONSTRUCTION. Concurrent fifth failures both count the same window and both attempt the insert;
-- the unique key means one wins, and the DO UPDATE is guarded so it will not extend a restriction that is
-- already running. That guard is the Product Owner's "requests during a restriction do not extend it",
-- enforced in the one place a race could otherwise defeat it.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_note_failure(
    p_tenant uuid, p_site uuid, p_mac macaddr,
    p_network uuid, p_network_name text, p_room text)
  RETURNS TABLE (restricted boolean, expires_at timestamptz, failure_count integer)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_max int; v_window int; v_block int;
  v_reset timestamptz; v_count int; v_expires timestamptz; v_active boolean;
BEGIN
  IF p_mac IS NULL THEN
    -- Nothing to attribute the failure to, so nothing to restrict. A submission that failed before the
    -- appliance could identify the device is not a credential guess anyway.
    RETURN QUERY SELECT false, NULL::timestamptz, 0;
    RETURN;
  END IF;

  SELECT g.max_failed_attempts, g.observation_window_seconds, g.restriction_seconds
    INTO v_max, v_window, v_block
    FROM iam_v2.guest_signin_protection_get(p_tenant, p_site) g;

  -- Make sure the device has a row, so counter_reset_at exists to count from and the update below has
  -- something to lock.
  INSERT INTO iam_v2.guest_signin_restrictions (tenant_id, site_id, device_mac, guest_network_id,
                                                guest_network_name, last_submitted_room)
  VALUES (p_tenant, p_site, p_mac, p_network, NULLIF(p_network_name,''), NULLIF(p_room,''))
  ON CONFLICT (tenant_id, site_id, device_mac) DO UPDATE
     SET guest_network_id    = COALESCE(EXCLUDED.guest_network_id, iam_v2.guest_signin_restrictions.guest_network_id),
         guest_network_name  = COALESCE(EXCLUDED.guest_network_name, iam_v2.guest_signin_restrictions.guest_network_name),
         last_submitted_room = COALESCE(EXCLUDED.last_submitted_room, iam_v2.guest_signin_restrictions.last_submitted_room),
         updated_at          = now();

  -- Take the row lock BEFORE counting, so two concurrent fifth failures serialise here rather than both
  -- deciding to create a restriction from the same count.
  SELECT r.counter_reset_at, (r.expires_at > now() AND r.released_at IS NULL)
    INTO v_reset, v_active
    FROM iam_v2.guest_signin_restrictions r
   WHERE r.tenant_id = p_tenant AND r.site_id = p_site AND r.device_mac = p_mac
     FOR UPDATE;

  -- THE ROLLING COUNT, derived from the attempts themselves. Only the two codes that mean "the value you
  -- submitted did not identify a stay" are counted; see the header.
  SELECT count(*) INTO v_count
    FROM iam_v2.sign_in_attempts a
   WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND a.device_mac = p_mac
     AND a.result IN ('CREDENTIAL_MISMATCH','ROOM_NOT_IN_MIRROR')
     AND a.occurred_at > now() - make_interval(secs => v_window)
     AND a.occurred_at > v_reset;

  IF v_active OR v_count < v_max THEN
    -- Already refused (do not extend), or not there yet.
    RETURN QUERY SELECT COALESCE(v_active,false),
                        (SELECT r.expires_at FROM iam_v2.guest_signin_restrictions r
                          WHERE r.tenant_id=p_tenant AND r.site_id=p_site AND r.device_mac=p_mac
                            AND r.released_at IS NULL AND r.expires_at > now()),
                        v_count;
    RETURN;
  END IF;

  v_expires := now() + make_interval(secs => v_block);
  UPDATE iam_v2.guest_signin_restrictions
     SET restricted_at = now(), expires_at = v_expires, failure_count = v_count,
         reason = 'FAILED_CREDENTIAL_THRESHOLD',
         -- The fresh counter begins the moment the refusal ends, so the device is not instantly restricted
         -- again by the same five failures it has already served the time for.
         counter_reset_at = v_expires,
         released_at = NULL, released_by = NULL, release_reason = NULL,
         updated_at = now()
   WHERE tenant_id = p_tenant AND site_id = p_site AND device_mac = p_mac;

  RETURN QUERY SELECT true, v_expires, v_count;
END $fn$;

COMMENT ON FUNCTION iam_v2.guest_signin_note_failure(uuid,uuid,macaddr,uuid,text,text) IS
  'Counts this device''s wrong credentials over the site''s rolling window and creates a restriction when the '
  'threshold is reached. Counts ONLY CREDENTIAL_MISMATCH and ROOM_NOT_IN_MIRROR. Takes the device row lock '
  'before counting, so concurrent failures serialise, and refuses to extend a restriction that is already '
  'running.';

-- 3e. A success clears that device's counter. Stated as its own operation because "clear the counter" and
-- "release a restriction" are different acts with different authority: a guest proves who they are, an
-- operator makes a decision.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_note_success(p_tenant uuid, p_site uuid, p_mac macaddr)
  RETURNS void
  LANGUAGE sql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  UPDATE iam_v2.guest_signin_restrictions
     SET counter_reset_at = now(), updated_at = now()
   WHERE tenant_id = p_tenant AND site_id = p_site AND device_mac = p_mac;
$fn$;

-- 3f. Manual release. It ends the refusal and starts a fresh counter, and it does NOTHING ELSE -- in
-- particular it grants no access. The guest must authenticate, correctly, like anybody else.
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_release(
    p_tenant uuid, p_site uuid, p_id uuid, p_operator text, p_reason text)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_rows integer;
BEGIN
  IF p_reason IS NULL OR length(btrim(p_reason)) < 3 THEN
    RAISE EXCEPTION 'a release reason is required';
  END IF;
  UPDATE iam_v2.guest_signin_restrictions
     SET released_at = now(), released_by = NULLIF(btrim(p_operator),''),
         release_reason = btrim(p_reason),
         counter_reset_at = now(),
         updated_at = now()
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_id
     AND released_at IS NULL AND expires_at > now();
  GET DIAGNOSTICS v_rows = ROW_COUNT;
  RETURN v_rows;
END $fn$;

COMMENT ON FUNCTION iam_v2.guest_signin_release(uuid,uuid,uuid,text,text) IS
  'Ends one active restriction and starts a fresh counter for that device. Grants NO access: the guest must '
  'still authenticate correctly. Requires a reason, is scoped to the caller''s own site, and returns the '
  'number of rows it changed so a caller cannot mistake "already expired" for "released".';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Privileges.
--
-- scd enforces: it reads the gate and reports outcomes. edged serves operators: it reads, changes the policy
-- and releases. NEITHER holds UPDATE on either table.
--
-- NOTE FOR WHOEVER CHANGES THIS NEXT: a grant written only here is removed by the first Gate-P reconcile. The
-- durable copies live in deploy/gatep/svc-scd-iamv2-guest-auth-grants.sql and
-- deploy/gatep/svc-edged-phase345-admin-grants.sql, and CI proves the grants survive a reconcile.
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON iam_v2.site_guest_signin_protection FROM PUBLIC;
REVOKE ALL ON iam_v2.guest_signin_restrictions   FROM PUBLIC;
REVOKE ALL ON iam_v2.guest_signin_protection_changes FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_signin_protection_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_signin_gate(uuid,uuid,macaddr) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_signin_note_failure(uuid,uuid,macaddr,uuid,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_signin_note_success(uuid,uuid,macaddr) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_signin_release(uuid,uuid,uuid,text,text) FROM PUBLIC;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    -- The enforcement side: ask the gate, report an outcome. No table privilege at all -- scd cannot read the
    -- restriction list, cannot change the policy it is subject to, and cannot release anybody.
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_gate(uuid,uuid,macaddr)                        TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_note_failure(uuid,uuid,macaddr,uuid,text,text) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_note_success(uuid,uuid,macaddr)                TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- The operator side: read the list and the policy, change the policy, release a restriction.
    GRANT SELECT ON iam_v2.guest_signin_restrictions      TO svc_edged;
    -- The history a screen shows, read-only. The INSERT happens inside the definer function.
    GRANT SELECT ON iam_v2.guest_signin_protection_changes TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_protection_get(uuid,uuid)                           TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text)   TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_release(uuid,uuid,uuid,text,text)                   TO svc_edged;
  END IF;
  -- Every other runtime role is named and given nothing, so "was this considered?" has an answer in the file.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd')    THEN REVOKE ALL ON iam_v2.guest_signin_restrictions, iam_v2.site_guest_signin_protection FROM svc_pmsd;    END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd')   THEN REVOKE ALL ON iam_v2.guest_signin_restrictions, iam_v2.site_guest_signin_protection FROM svc_acctd;   END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_netd')    THEN REVOKE ALL ON iam_v2.guest_signin_restrictions, iam_v2.site_guest_signin_protection FROM svc_netd;    END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_portald') THEN REVOKE ALL ON iam_v2.guest_signin_restrictions, iam_v2.site_guest_signin_protection FROM svc_portald; END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.site_guest_signin_protection OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.guest_signin_restrictions OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.guest_signin_protection_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_protection_changes_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_protection_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_gate(uuid,uuid,macaddr) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_note_failure(uuid,uuid,macaddr,uuid,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_note_success(uuid,uuid,macaddr) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_signin_release(uuid,uuid,uuid,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- ---------------------------------------------------------------------------------------------------------
-- 5. Re-assert on the database this migration just changed.
-- ---------------------------------------------------------------------------------------------------------
DO $verify$
DECLARE v_max int; v_window int; v_block int; v_default boolean;
BEGIN
  -- THE APPROVED POLICY IS ACTIVE WITH NO ROW PRESENT. This is the assertion that "activated on PRE-LIVE"
  -- rests on: a factory-clean site answers 5 / 60 / 60 without anyone configuring anything.
  SELECT g.max_failed_attempts, g.observation_window_seconds, g.restriction_seconds, g.is_default
    INTO v_max, v_window, v_block, v_default
    FROM iam_v2.guest_signin_protection_get(gen_random_uuid(), gen_random_uuid()) g;
  IF v_max <> 5 OR v_window <> 60 OR v_block <> 60 OR NOT v_default THEN
    RAISE EXCEPTION '0068: a site with no stored policy answers %/%/% (default=%), want 5/60/60 default=true',
      v_max, v_window, v_block, v_default;
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_indexes
                  WHERE schemaname='iam_v2' AND indexname='sign_in_attempts_device_recent') THEN
    RAISE EXCEPTION '0068: the counting driver index is missing; every wrong credential would scan the table';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    RETURN;  -- a scratch database without the Gate-P roles has nothing further to assert
  END IF;

  IF NOT has_function_privilege('svc_scd','iam_v2.guest_signin_gate(uuid,uuid,macaddr)','EXECUTE') THEN
    RAISE EXCEPTION '0068: svc_scd cannot ask the gate; the policy would never refuse anybody';
  END IF;
  -- scd must NOT be able to read the restriction list, change the policy it is subject to, or release.
  IF has_table_privilege('svc_scd','iam_v2.guest_signin_restrictions','SELECT') THEN
    RAISE EXCEPTION '0068: svc_scd can read the restriction list directly; it asks the gate instead';
  END IF;
  IF has_function_privilege('svc_scd',
        'iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text)','EXECUTE') THEN
    RAISE EXCEPTION '0068: svc_scd can change the policy it is subject to';
  END IF;
  IF has_function_privilege('svc_scd','iam_v2.guest_signin_release(uuid,uuid,uuid,text,text)','EXECUTE') THEN
    RAISE EXCEPTION '0068: svc_scd can release its own restrictions';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    IF NOT has_function_privilege('svc_edged',
          'iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text)','EXECUTE') THEN
      RAISE EXCEPTION '0068: svc_edged cannot change the policy it is supposed to serve';
    END IF;
    IF NOT has_function_privilege('svc_edged','iam_v2.guest_signin_release(uuid,uuid,uuid,text,text)','EXECUTE') THEN
      RAISE EXCEPTION '0068: svc_edged cannot release a restriction';
    END IF;
    IF has_table_privilege('svc_edged','iam_v2.guest_signin_restrictions','UPDATE')
       OR has_table_privilege('svc_edged','iam_v2.site_guest_signin_protection','UPDATE') THEN
      RAISE EXCEPTION '0068: svc_edged holds UPDATE directly; every write goes through the scoped operations';
    END IF;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    IF has_table_privilege('svc_edged','iam_v2.guest_signin_protection_changes','INSERT')
       OR has_table_privilege('svc_edged','iam_v2.guest_signin_protection_changes','UPDATE')
       OR has_table_privilege('svc_edged','iam_v2.guest_signin_protection_changes','DELETE') THEN
      RAISE EXCEPTION '0068: svc_edged can write the policy change log directly; the record is written by the '
                      'same operation that makes the change, which is what makes it mandatory';
    END IF;
  END IF;

  IF has_table_privilege('public','iam_v2.guest_signin_restrictions','SELECT')
     OR has_table_privilege('public','iam_v2.site_guest_signin_protection','SELECT') THEN
    RAISE EXCEPTION '0068: PUBLIC can read guest sign-in protection state';
  END IF;
END $verify$;

COMMIT;
