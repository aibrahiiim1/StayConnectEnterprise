-- A REFUSED SIGN-IN LEAVES A RECORD SOMEBODY CAN READ.
--
-- WHAT THAT COST. A guest at the desk says they cannot get online. The operator has, today, nothing to look
-- at. iam_v2.auth_resolutions records an outcome code for the attempts that reached a resolver, and nothing
-- at all for the ones that did not — a room that is absent from the mirror, a submission the server could not
-- read, a device on no mapped network. Even where a row exists it records what the SERVER concluded and not
-- what the GUEST typed, so the single question that settles almost every one of these conversations — "what
-- did you enter, and what would we have accepted?" — has no answer anywhere on the appliance. The standing
-- workaround was to ask the guest to try again while somebody watched the journal.
--
-- WHAT THIS TABLE IS. One row per DELIBERATE Connect submission, carrying the exact structured reason, the
-- evidence the probe saw, the state of the PMS mirror at that instant, and — sealed — what the guest typed
-- beside what the mirror would have accepted.
--
-- WHY THE CREDENTIAL MATERIAL IS ENCRYPTED AND THE ROOM NUMBER IS NOT. The Product Owner's decision is that
-- authorised Hotel Admin and Reception users see the submitted and accepted values IN FULL and unmasked:
-- they are the same people who already hold the guest's room, stay and access credentials, and a masked
-- comparison panel would not answer the question it exists for. "Unmasked to an authorised operator" is not
-- "lying around in the database", so the credential half is sealed with AES-256-GCM under an appliance-local
-- key that lives in a 0600 file outside PostgreSQL — a copy of this database, a backup or a stolen disk
-- yields nothing. The room number stays in clear because it is the column an operator filters and scans by,
-- and because every other screen that shows a stay already shows it to the same audience.
--
-- WHY NO CONTROLLED-WRITER TRIGGER. The capability-scoped guard exists for rows that ARE the answer to "who
-- was allowed what, on whose authority" — iam_v2.auth_resolutions, entitlements, sessions. This table is
-- diagnostics ABOUT those decisions and is never consulted to make one: nothing reads it to authorise, to
-- refuse, to rate-limit or to bill. The existing attempts-adjacent table, public.auth_throttle_buckets,
-- carries no such trigger for the same reason. Adding a capability family here would put a guard on the
-- cheap copy while the authoritative record it describes keeps its own.
--
-- RETENTION IS THIRTY DAYS, AND IT IS ENFORCED IN CODE. scd sweeps this table on a ticker, exactly as it
-- already sweeps the durable throttle's buckets; the index below is what makes that sweep bounded. There is
-- no database scheduler on this appliance and this migration does not invent one.
--
-- WHAT DOES NOT CHANGE. No existing table, function, trigger, grant or row is altered. Nothing here reads or
-- writes a stay, a guest, an entitlement, a session, a financial record or public.audit_log.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- The table.
--
-- Site-scoped in the schema's own idiom: tenant_id + site_id on the row, and the companion UNIQUE key that
-- lets a future composite foreign key carry the site with it. Deliberately NOT a hypertable — iam_v2 uses
-- none, the Phase-3 gate builds on plain PostgreSQL 16 with no TimescaleDB, and a thirty-day table that a
-- ticker already prunes gains nothing from chunking.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.sign_in_attempts (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL,
  site_id       uuid NOT NULL,
  occurred_at   timestamptz NOT NULL DEFAULT now(),

  -- Where the submission came from. Every one of these is nullable because a submission can fail BEFORE it
  -- has one: a device on no mapped guest network has no network and no interface, and recording the attempt
  -- anyway is the entire point of the table.
  guest_network_id   uuid,
  guest_network_name text,
  pms_interface_id   uuid,
  request_id         uuid,

  -- What was submitted, in clear. The room is an operator's primary filter; the kind is a three-valued
  -- label, never a value.
  submitted_room text,
  verifier_kind  text NOT NULL DEFAULT 'UNKNOWN'
    CONSTRAINT sign_in_attempts_verifier_kind_check
    CHECK (verifier_kind IN ('FULL_NAME','RESERVATION_NUMBER_LIKE','UNKNOWN')),

  -- What happened. The CHECK is the contract with internal/signinattempt: a code that reaches here without
  -- being in this list is a code no operator screen knows how to render.
  result text NOT NULL
    CONSTRAINT sign_in_attempts_result_check
    CHECK (result IN ('VERIFIED','CREDENTIAL_MISMATCH','ROOM_NOT_IN_MIRROR','STAY_NOT_ELIGIBLE',
                      'AMBIGUOUS_ROOM_CANDIDATES','MIRROR_STALE_OR_MISSING_CHANGE','RATE_LIMITED',
                      'ROUTING_OR_INTERFACE_FAILURE','SERVICE_UNAVAILABLE','SPENT_REQUEST_ID',
                      'MALFORMED_SUBMISSION','VERIFIED_NO_ELIGIBLE_PACKAGE')),
  matched_field text
    CONSTRAINT sign_in_attempts_matched_field_check
    CHECK (matched_field IS NULL OR matched_field IN ('FIRST_NAME','FAMILY_NAME','RESERVATION_NUMBER')),

  -- The candidate the evidence was compared against, when one existed. No foreign key: these rows outlive
  -- the stay they describe by design — a stay that checks out and is purged from the mirror must not take
  -- the record of a sign-in problem with it, and a cascade would do exactly that.
  matched_stay_id  uuid,
  matched_guest_id uuid,

  -- What the probe SAW. This is what turns a bare refusal into an answer: absent room, present-but-
  -- ineligible, and present-with-candidates-that-did-not-match are three different conversations.
  room_in_mirror           boolean,
  eligible_stay_candidates integer,

  -- The state of the feed at that instant, so "the mirror was four days old" is a recorded fact rather than
  -- something reconstructed afterwards from a runtime row that has since moved on.
  pms_transport_status         text,
  mirror_last_complete_sync_at timestamptz,
  mirror_age_seconds           bigint,

  latency_ms bigint,

  -- What the guest ended up with, when they got in.
  entitlement_id uuid,
  session_id     uuid,

  device_ip  inet,
  device_mac macaddr,

  -- THE SEALED HALF. Columns follow the appliance's established AEAD shape (see
  -- iam_v2.pms_interface_secret_generations): separate ciphertext and nonce, the key id, and a format marker
  -- so a later cipher change can be rolled out without guessing what an existing row is. All four are NULL
  -- together on a row recorded while the key was unavailable — which is why the guard below is written as
  -- all-or-none rather than NOT NULL.
  sensitive_ciphertext bytea,
  sensitive_nonce      bytea,
  encryption_key_id    uuid,
  cipher_version       integer,

  CONSTRAINT sign_in_attempts_sealed_all_or_none CHECK (
    (sensitive_ciphertext IS NULL AND sensitive_nonce IS NULL
       AND encryption_key_id IS NULL AND cipher_version IS NULL)
    OR
    (sensitive_ciphertext IS NOT NULL AND sensitive_nonce IS NOT NULL
       AND encryption_key_id IS NOT NULL AND cipher_version IS NOT NULL)
  ),

  CONSTRAINT sign_in_attempts_tenant_site_id_key UNIQUE (tenant_id, site_id, id)
);

-- The operator's list is "this site, newest first", optionally narrowed by result. One index serves both.
CREATE INDEX IF NOT EXISTS sign_in_attempts_site_recent
  ON iam_v2.sign_in_attempts (tenant_id, site_id, occurred_at DESC);

-- Looking up the attempts for one room is the second thing anyone does at the desk.
CREATE INDEX IF NOT EXISTS sign_in_attempts_site_room
  ON iam_v2.sign_in_attempts (tenant_id, site_id, submitted_room, occurred_at DESC);

-- THE RETENTION DRIVER. Without it the thirty-day sweep degrades to a sequential scan of the whole table on
-- every tick, which is how a bounded cleanup quietly becomes an unbounded one.
CREATE INDEX IF NOT EXISTS sign_in_attempts_expiry
  ON iam_v2.sign_in_attempts (occurred_at);

-- A correlation id is how an operator ties a guest's report to the row, and how the portal's own logs join to
-- it. Not unique: one request id can legitimately produce a resolution attempt and, after a package choice,
-- nothing further — and a UNIQUE here would turn a duplicate into a lost record.
CREATE INDEX IF NOT EXISTS sign_in_attempts_request
  ON iam_v2.sign_in_attempts (tenant_id, site_id, request_id);

COMMENT ON TABLE iam_v2.sign_in_attempts IS
  'One row per deliberate guest Connect submission: the structured result, what the probe saw, the PMS mirror '
  'state at that instant, and the guest credential material SEALED with AES-256-GCM under an appliance-local '
  'key (internal/signinattempt). RETENTION: 30 days, swept by scd on a ticker; the sweep touches this table '
  'and nothing else. The sealed half NEVER leaves the appliance in clear -- not into a log, a journal, CI '
  'output, telemetry, an export or a governance artifact. Written by svc_scd; read by svc_edged, which does '
  'NOT hold the key and must ask scd to open a sealed row.';

COMMENT ON COLUMN iam_v2.sign_in_attempts.submitted_room IS
  'The room number as submitted, in clear: it is the operator''s primary filter and is already visible to the '
  'same audience on every screen that shows a stay.';
COMMENT ON COLUMN iam_v2.sign_in_attempts.sensitive_ciphertext IS
  'AES-256-GCM over a JSON envelope holding the submitted verifier (raw and normalized) and the accepted '
  'first/family/reservation values. AAD binds it to (tenant, site, attempt id), so a ciphertext moved between '
  'rows or sites fails authentication rather than describing the wrong guest.';
COMMENT ON COLUMN iam_v2.sign_in_attempts.result IS
  'The exact operator-visible reason. Distinct from what the guest is told: internal/signinattempt maps this '
  'to one of four coarse guest classes, and several distinct results deliberately share a class so that the '
  'portal does not become a way to enumerate which rooms are occupied.';
COMMENT ON COLUMN iam_v2.sign_in_attempts.matched_stay_id IS
  'The stay the evidence was compared against, when one existed. Deliberately NOT a foreign key: the record '
  'of a sign-in problem must outlive the stay it describes.';

-- ---------------------------------------------------------------------------------------------------------
-- Privileges.
--
-- scd writes (it is the authentication path and it holds the sealing key). edged reads (it serves the
-- operator API) and is deliberately given NO access to the key material, so the plaintext half is reachable
-- only by asking scd -- the same split the voucher DEK already uses.
--
-- NOTE FOR WHOEVER CHANGES THIS NEXT: a grant written ONLY here is removed by the first Gate-P reconcile,
-- because deploy/gatep/gatep-grants.sql REVOKEs ALL on iam_v2 for every svc_* role as its first act. The
-- durable copies live in deploy/gatep/svc-scd-iamv2-guest-auth-grants.sql and
-- deploy/gatep/svc-edged-phase345-admin-grants.sql, and CI proves the grants survive a reconcile.
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON iam_v2.sign_in_attempts FROM PUBLIC;

DO $grant$
BEGIN
  -- svc_scd: the guest authentication path. INSERT to record, SELECT to open a sealed row on edged's behalf,
  -- DELETE for the thirty-day sweep. No UPDATE: an attempt is a statement about one instant and is never
  -- revised.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT, INSERT, DELETE ON iam_v2.sign_in_attempts TO svc_scd;
  END IF;
  -- svc_edged: the operator API. SELECT only, and the sealed columns are useless to it without the key.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.sign_in_attempts TO svc_edged;
  END IF;
  -- Every other runtime role is named explicitly and given nothing, so "was this considered?" has an answer
  -- in the file rather than in whoever remembers.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    REVOKE ALL ON iam_v2.sign_in_attempts FROM svc_pmsd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    REVOKE ALL ON iam_v2.sign_in_attempts FROM svc_acctd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_netd') THEN
    REVOKE ALL ON iam_v2.sign_in_attempts FROM svc_netd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_portald') THEN
    REVOKE ALL ON iam_v2.sign_in_attempts FROM svc_portald;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.sign_in_attempts OWNER TO iam_v2_owner';
  END IF;
END $own$;


-- ---------------------------------------------------------------------------------------------------------
-- COMPLETING AN ATTEMPT, WITHOUT HANDING ANYBODY UPDATE.
--
-- A guest's sign-in is ONE deliberate submission but TWO calls: identity is proved, then access is granted.
-- The record belongs to the submission, so there is one row, written when identity is decided — and the
-- entitlement and session it ends in are only known one call later.
--
-- The obvious fix is to give svc_scd UPDATE on the table. That is exactly what this avoids: a service able to
-- rewrite an attempt is a service able to rewrite the evidence of its own refusals, and the whole value of
-- the record is that it cannot be. So the completion is a single narrow operation instead — it stamps two
-- identifiers and, at most, moves the result from VERIFIED to a terminal grant outcome, and it will only do
-- so ONCE, on a row that is still VERIFIED and still has no session. Anything else is a no-op that reports
-- how many rows it touched, so a caller cannot mistake a refusal for a success.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.complete_sign_in_attempt(
    p_tenant uuid, p_site uuid, p_request uuid,
    p_result text, p_entitlement uuid, p_session uuid)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_rows integer;
BEGIN
  IF p_request IS NULL THEN
    RETURN 0;
  END IF;
  IF p_result NOT IN ('VERIFIED','SERVICE_UNAVAILABLE','STAY_NOT_ELIGIBLE','VERIFIED_NO_ELIGIBLE_PACKAGE') THEN
    RAISE EXCEPTION 'complete_sign_in_attempt: % is not a terminal grant outcome', p_result;
  END IF;
  UPDATE iam_v2.sign_in_attempts
     SET result         = p_result,
         entitlement_id = COALESCE(p_entitlement, entitlement_id),
         session_id     = COALESCE(p_session, session_id)
   WHERE tenant_id = p_tenant AND site_id = p_site AND request_id = p_request
     AND result = 'VERIFIED' AND session_id IS NULL;
  GET DIAGNOSTICS v_rows = ROW_COUNT;
  RETURN v_rows;
END $fn$;

COMMENT ON FUNCTION iam_v2.complete_sign_in_attempt(uuid,uuid,uuid,text,uuid,uuid) IS
  'Stamps the entitlement and session a proved identity ended in, or moves that one row to a terminal grant '
  'outcome. Exists so that svc_scd never holds UPDATE on iam_v2.sign_in_attempts: a service that could '
  'rewrite an attempt could rewrite the evidence of its own refusals. Idempotent and one-way -- it acts only '
  'on a row that is still VERIFIED with no session, and returns the number of rows it touched.';

REVOKE ALL ON FUNCTION iam_v2.complete_sign_in_attempt(uuid,uuid,uuid,text,uuid,uuid) FROM PUBLIC;
DO $grantfn$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.complete_sign_in_attempt(uuid,uuid,uuid,text,uuid,uuid) TO svc_scd;
  END IF;
END
$grantfn$;

DO $ownfn$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.complete_sign_in_attempt(uuid,uuid,uuid,text,uuid,uuid) OWNER TO iam_v2_owner';
  END IF;
END $ownfn$;

-- ---------------------------------------------------------------------------------------------------------
-- Re-assert on the database this migration just changed, rather than assuming.
-- ---------------------------------------------------------------------------------------------------------
DO $verify$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.tables
                  WHERE table_schema='iam_v2' AND table_name='sign_in_attempts') THEN
    RAISE EXCEPTION '0067: iam_v2.sign_in_attempts was not created';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_indexes
                  WHERE schemaname='iam_v2' AND indexname='sign_in_attempts_expiry') THEN
    RAISE EXCEPTION '0067: the retention driver index is missing; the thirty-day sweep would scan the table';
  END IF;

  -- A scratch database without the Gate-P service roles has nothing further to assert.
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    RETURN;
  END IF;

  IF NOT has_table_privilege('svc_scd', 'iam_v2.sign_in_attempts', 'INSERT') THEN
    RAISE EXCEPTION '0067: svc_scd cannot record a sign-in attempt';
  END IF;
  IF NOT has_table_privilege('svc_scd', 'iam_v2.sign_in_attempts', 'DELETE') THEN
    RAISE EXCEPTION '0067: svc_scd cannot purge; the thirty-day retention would never be enforced';
  END IF;
  IF has_table_privilege('svc_scd', 'iam_v2.sign_in_attempts', 'UPDATE') THEN
    RAISE EXCEPTION '0067: svc_scd can rewrite an attempt; the record must describe one instant, once';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    IF NOT has_table_privilege('svc_edged', 'iam_v2.sign_in_attempts', 'SELECT') THEN
      RAISE EXCEPTION '0067: svc_edged cannot read the attempts it is supposed to serve';
    END IF;
    IF has_table_privilege('svc_edged', 'iam_v2.sign_in_attempts', 'INSERT')
       OR has_table_privilege('svc_edged', 'iam_v2.sign_in_attempts', 'DELETE') THEN
      RAISE EXCEPTION '0067: svc_edged can write or delete attempts; the operator API is read-only here';
    END IF;
  END IF;

  IF NOT has_function_privilege('svc_scd',
        'iam_v2.complete_sign_in_attempt(uuid,uuid,uuid,text,uuid,uuid)', 'EXECUTE') THEN
    RAISE EXCEPTION '0067: svc_scd cannot complete an attempt; a granted session would never be recorded';
  END IF;
  IF has_function_privilege('public',
        'iam_v2.complete_sign_in_attempt(uuid,uuid,uuid,text,uuid,uuid)', 'EXECUTE') THEN
    RAISE EXCEPTION '0067: PUBLIC can complete a sign-in attempt';
  END IF;

  IF has_table_privilege('public', 'iam_v2.sign_in_attempts', 'SELECT') THEN
    RAISE EXCEPTION '0067: PUBLIC can read guest sign-in attempts';
  END IF;
END $verify$;

COMMIT;
