-- 0010 — Phase 3 (PMS Stay Domain, STRICT resolution, Checkout Grace) additive iam_v2 hardening.
-- ADDITIVE + reversible. DARK; no data. Owner-inherited (iam_v2_owner in prod, as with 0009).
-- Scope proven by docs/evidence/StayConnect-IAM-Phase3-Schema-Gap-Audit.md against a disposable PG16.
--
-- The triggers below are STRUCTURAL state-machine guards ONLY. A raw SQL update that sets
-- status='IN_HOUSE' + lifecycle_version+1 is NOT proof of a trusted source; the AUTHORIZATION boundary
-- (trusted normalized PMS event application, or privileged Hotel-Admin Reinstatement with RBAC + password
-- step-up + reason + immutable audit + version check) is implemented in Increment 4 through controlled
-- Stay-domain write operations. Ordinary runtime roles receive NO direct UPDATE grant on Stay lifecycle
-- columns (privilege model: zero runtime grants while DARK; Gate-P least privilege preserved).
--
-- Adds ONLY:
--   (1) pms_interface_runtime — durable PMS connector runtime state, FOUR INDEPENDENT axes (NO stored
--       derived-freshness field: the resolver derives availability from the axes + revision thresholds).
--   (2) one-way stays.status transition guard; lifecycle_version is a STRICT episode counter that changes
--       ONLY on a CHECKED_OUT->IN_HOUSE reinstatement (+1); monotonic last_applied_event_version.
--   (3) NO stays.checkout_episode — the episode IS stays.lifecycle_version.
--   (4) stays.effective_checkout_at + per-Stay occupancy-evidence (composite-FK-pinned, all-or-none) +
--       typed grace scalar columns (bytes) on site_checkout_grace_config with validated bounds.
--   (5) auth_resolutions.resolution_request_id — server-side resolution-request idempotency key.
--   (6) stay_events append-only guard (immutable identity/normalization; one-way terminal processing_status;
--       stay_id may go NULL->same-interface Stay only in the same tx that makes the event terminal).
--   (7) PO Checkout-hardening APPEND-ONLY integrity tables + controlled operations (proving the atomic Checkout
--       conversion AT the effective-checkout boundary from immutable history, not a mutable current row):
--         - checkout_grace_audit (durable one-per-episode conversion evidence + boundary provenance);
--         - checkout_grace_alert_actions + active_operational_alerts view (resolvable OPEN/ACK/RESOLVED alerts);
--         - entitlement_state_transitions + apply_entitlement_transition() (controlled Entitlement state machine);
--         - entitlement_device_authorizations (append-only device-authorization intervals);
--         - stays.last_applied_event_id (exact event lineage pin);
--         - bootstrap_emergency_grace()/emergency_grace_health() (system Emergency catalog, reserved-namespace
--           protected) and publish_checkout_grace_config() (controlled, version-incrementing config publication).
-- No public-schema mutation. Zero runtime grants (dark). SECURITY DEFINER is used ONLY for the narrow
-- controlled-writer functions the PO corrections require (apply_entitlement_transition,
-- publish_checkout_grace_config, bootstrap_emergency_grace) — each with a fixed search_path, no dynamic SQL,
-- EXECUTE revoked from PUBLIC, and NO per-service EXECUTE grant yet: those land at Gate-P/cutover so Phase 3
-- keeps its zero-runtime-privilege invariant. Every other function remains SECURITY INVOKER.
BEGIN;

-- ============================================================================
-- (1) pms_interface_runtime — FOUR independent axes; NO stored derived freshness.
-- ============================================================================
CREATE TABLE iam_v2.pms_interface_runtime (
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  pms_interface_id uuid NOT NULL,
  pinned_revision_id uuid,
  pinned_secret_generation_id uuid,                       -- identity only; never ciphertext/nonce/key
  -- credential mode of the pinned connector: NONE (no-auth transport, e.g. Protel FIAS — NO Secret
  -- Generation is required or pinned) or AUTH_KEY (a Secret Generation MUST be pinned). Denormalized from
  -- the revision at generation allocation so the pin-coherence CHECK can enforce it without a join.
  credential_mode text NOT NULL DEFAULT 'AUTH_KEY'
    CHECK (credential_mode IN ('NONE','AUTH_KEY')),
  runtime_generation bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),

  -- axis 1: transport / heartbeat
  transport_status text NOT NULL DEFAULT 'UNKNOWN'
    CHECK (transport_status IN ('UNKNOWN','CONNECTING','CONNECTED','DISCONNECTED','ERROR')),
  last_connect_attempt_at timestamptz,
  last_connected_at timestamptz,
  last_heartbeat_at timestamptz,
  disconnected_since timestamptz,
  transport_error_code text,

  -- axis 2: feed continuity
  continuity_status text NOT NULL DEFAULT 'UNKNOWN'
    CHECK (continuity_status IN ('UNKNOWN','CONTINUOUS','DISCONTINUOUS','GAP_DETECTED')),
  last_valid_event_at timestamptz,
  last_event_cursor text,
  discontinuity_detected_at timestamptz,
  last_resync_marker_at timestamptz,

  -- axis 3: complete sync / resync
  sync_status text NOT NULL DEFAULT 'UNKNOWN'
    CHECK (sync_status IN ('UNKNOWN','IN_SYNC','RESYNC_REQUIRED','RESYNC_IN_PROGRESS','SYNC_FAILED')),
  resync_requested_at timestamptz,
  resync_started_at timestamptz,
  last_complete_sync_at timestamptz,
  sync_cursor text,
  last_sync_failure_code text,
  -- §G typed resync generation: a monotonic allocator + the ATOMIC generation-level PUBLICATION boundary.
  -- resync_generation_seq is bumped once per DS (a new typed resync generation under the current runtime
  -- generation). published_resync_generation is advanced ONLY by a valid DE, in ONE row update — never by
  -- mass-updating Event rows. RESYNC stay_events rows are immutable; they become consumable only once this
  -- boundary reaches their resync_generation. A partial/failed generation (no DE) leaves it unchanged, so
  -- its staged rows are never published and remain isolated for deterministic cleanup.
  resync_generation_seq bigint NOT NULL DEFAULT 0,
  published_resync_generation bigint NOT NULL DEFAULT 0,

  PRIMARY KEY (tenant_id, site_id, pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id)
    REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, pinned_revision_id)
    REFERENCES iam_v2.pms_interface_revisions (tenant_id, site_id, pms_interface_id, id),
  -- the pinned Secret Generation must belong to the SAME tenant/site/interface (historical rows may
  -- reference a superseded generation; only the identity is stored, never key material).
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, pinned_secret_generation_id)
    REFERENCES iam_v2.pms_interface_secret_generations (tenant_id, site_id, pms_interface_id, id),

  -- structural coherence (no now()-dependent logic; time-threshold decisions live in the domain)
  CONSTRAINT pir_generation_nonneg CHECK (runtime_generation >= 0),
  -- CONNECTED requires a pinned revision + connect time, and a pinned Secret Generation ONLY when the
  -- credential mode is AUTH_KEY (a NONE/no-auth connector legitimately has no Secret Generation).
  CONSTRAINT pir_connected_pins CHECK (
    transport_status <> 'CONNECTED'
    OR (pinned_revision_id IS NOT NULL AND last_connected_at IS NOT NULL
        AND (credential_mode = 'NONE' OR pinned_secret_generation_id IS NOT NULL))),
  CONSTRAINT pir_heartbeat_not_future CHECK (last_heartbeat_at IS NULL OR last_heartbeat_at <= updated_at),
  CONSTRAINT pir_resync_coherent CHECK (
        (resync_started_at IS NULL OR resync_requested_at IS NOT NULL)
    AND (resync_started_at IS NULL OR resync_requested_at IS NULL OR resync_started_at >= resync_requested_at)),
  CONSTRAINT pir_resync_generation_coherent CHECK (
        resync_generation_seq >= 0 AND published_resync_generation >= 0
    AND published_resync_generation <= resync_generation_seq),
  CONSTRAINT pir_bounded_lengths CHECK (
        (transport_error_code   IS NULL OR length(transport_error_code)   <= 200)
    AND (last_sync_failure_code IS NULL OR length(last_sync_failure_code) <= 200)
    AND (last_event_cursor      IS NULL OR length(last_event_cursor)      <= 4096)
    AND (sync_cursor            IS NULL OR length(sync_cursor)            <= 4096))
);

-- ============================================================================
-- (4) stays: effective checkout + per-Stay occupancy evidence (composite-FK pinned, all-or-none).
-- ============================================================================
ALTER TABLE iam_v2.stays
  ADD COLUMN effective_checkout_at timestamptz,
  ADD COLUMN occupancy_evidence_at timestamptz,
  ADD COLUMN occupancy_ingested_at timestamptz,
  ADD COLUMN occupancy_revision_id uuid,
  ADD COLUMN occupancy_normalization_version int,
  ADD COLUMN occupancy_clock_suspect boolean,
  -- MONOTONIC occupancy-evidence snapshot version (distinct from the parser/normalization version); bumped
  -- ONLY when authoritative occupancy evidence for the Stay changes. Pinned by a PMS Auth Context so a later
  -- evidence replacement invalidates the context. Not part of the all-or-none occupancy group (always
  -- present, 0 = no authoritative evidence yet).
  ADD COLUMN occupancy_evidence_version bigint NOT NULL DEFAULT 0
    CHECK (occupancy_evidence_version >= 0);
ALTER TABLE iam_v2.stays
  ADD CONSTRAINT stays_effco_only_after_checkout
    CHECK (effective_checkout_at IS NULL OR status IN ('CHECKED_OUT','POST_STAY_ACTIVE')),
  -- (item 10) a CHECKED_OUT Stay MUST carry its boundary (posting_allowed=false is already implied by the base
  -- posting_only_in_house CHECK: CHECKED_OUT<>IN_HOUSE => posting_allowed=false).
  ADD CONSTRAINT stays_checkedout_needs_boundary
    CHECK (status <> 'CHECKED_OUT' OR effective_checkout_at IS NOT NULL),
  -- all-or-none occupancy evidence tuple
  ADD CONSTRAINT stays_occupancy_all_or_none CHECK (
    (occupancy_evidence_at IS NULL AND occupancy_ingested_at IS NULL AND occupancy_revision_id IS NULL
       AND occupancy_normalization_version IS NULL AND occupancy_clock_suspect IS NULL)
    OR
    (occupancy_evidence_at IS NOT NULL AND occupancy_ingested_at IS NOT NULL AND occupancy_revision_id IS NOT NULL
       AND occupancy_normalization_version IS NOT NULL AND occupancy_clock_suspect IS NOT NULL)),
  ADD CONSTRAINT stays_occupancy_norm_pos
    CHECK (occupancy_normalization_version IS NULL OR occupancy_normalization_version > 0),
  -- occupancy-evidence version coherence (STRUCTURAL, INSERT + UPDATE): authoritative evidence present
  -- (evidence_at NOT NULL) REQUIRES a real version > 0; no authoritative evidence (evidence_at NULL) REQUIRES
  -- version = 0. Combined with the monotonic + exactly-once transition rules in p3_stay_lifecycle_guard, this
  -- makes "evidence present with version 0" and "version populated without evidence" structurally impossible.
  ADD CONSTRAINT stays_evidence_version_coherent CHECK (
    (occupancy_evidence_at IS NULL     AND occupancy_evidence_version = 0)
    OR (occupancy_evidence_at IS NOT NULL AND occupancy_evidence_version > 0)),
  -- occupancy evidence Revision must belong to the SAME interface (historical revision, not necessarily current)
  ADD CONSTRAINT stays_occupancy_revision_fk
    FOREIGN KEY (tenant_id, site_id, pms_interface_id, occupancy_revision_id)
    REFERENCES iam_v2.pms_interface_revisions (tenant_id, site_id, pms_interface_id, id);
CREATE INDEX stays_effective_checkout
  ON iam_v2.stays (tenant_id, site_id, pms_interface_id, effective_checkout_at)
  WHERE effective_checkout_at IS NOT NULL;

-- ============================================================================
-- (5) auth_resolutions server-side resolution-request idempotency key.
-- ============================================================================
ALTER TABLE iam_v2.auth_resolutions
  ADD COLUMN resolution_request_id uuid;
CREATE UNIQUE INDEX auth_resolutions_req_idem
  ON iam_v2.auth_resolutions (tenant_id, site_id, resolution_request_id)
  WHERE resolution_request_id IS NOT NULL;

-- ============================================================================
-- (5b) auth_contexts: pin the EXACT occupancy-evidence version the successful STRICT resolution used, so
--      consumption can reject a context whose pinned Stay occupancy evidence has since changed version.
--      Nullable (backward compatible); Phase-3 PMS issuance sets it.
-- ============================================================================
-- Pin the authoritative Stay EPISODE (lifecycle_version) and a MONOTONIC occupancy-evidence snapshot version
-- (NOT the parser/normalization version). Consumption rejects a context whose Stay episode or evidence
-- snapshot has changed, so a Checkout→Reinstatement or an authoritative evidence replacement invalidates an
-- old context even within its TTL and even under the same Revision + normalization version.
ALTER TABLE iam_v2.auth_contexts
  ADD COLUMN pinned_lifecycle_version int
    CHECK (pinned_lifecycle_version IS NULL OR pinned_lifecycle_version > 0),
  ADD COLUMN pinned_occupancy_evidence_version bigint
    CHECK (pinned_occupancy_evidence_version IS NULL OR pinned_occupancy_evidence_version >= 0);

-- ============================================================================
-- (4b) site_checkout_grace_config: typed grace scalars; quota in BYTES; canonical device-limit-policy
--      vocabulary reused (service_plan_revisions.device_limit_policy). The typed columns are the
--      AUTHORITATIVE grace policy; config jsonb must NOT be a second source of truth for these fields.
-- ============================================================================
ALTER TABLE iam_v2.site_checkout_grace_config
  ADD COLUMN eligibility_window_seconds int NOT NULL DEFAULT 86400,   -- 24h default (always present)
  ADD COLUMN grace_duration_seconds int,
  ADD COLUMN grace_down_kbps int,
  ADD COLUMN grace_up_kbps int,
  ADD COLUMN grace_data_quota_bytes bigint,                          -- BYTES are the authoritative unit
  ADD COLUMN grace_device_limit int,
  -- Phase-3 Checkout Grace supports ONLY REJECT_NEW_DEVICE from the canonical vocabulary (mg2): every device
  -- authorized AT effective_checkout_at is grandfathered (kept, even above the configured limit — a lower
  -- limit never disconnects an existing device); EVERY device NOT authorized at the boundary is a new device
  -- and is rejected for the whole Grace lifetime (being below the limit does NOT admit a new post-checkout
  -- device). DISCONNECT_OLDEST / ADMIN_APPROVAL are NOT valid for Grace (the token is reused from the
  -- canonical vocab; no second enum is introduced).
  ADD COLUMN grace_device_limit_policy text
    CHECK (grace_device_limit_policy IS NULL OR grace_device_limit_policy = 'REJECT_NEW_DEVICE');
ALTER TABLE iam_v2.site_checkout_grace_config
  ADD CONSTRAINT grace_bounds CHECK (
        eligibility_window_seconds > 0 AND eligibility_window_seconds <= 604800
    AND (grace_duration_seconds  IS NULL OR (grace_duration_seconds  > 0 AND grace_duration_seconds  <= 604800))
    AND (grace_down_kbps         IS NULL OR (grace_down_kbps         > 0 AND grace_down_kbps         <= 10000000))
    AND (grace_up_kbps           IS NULL OR (grace_up_kbps           > 0 AND grace_up_kbps           <= 10000000))
    AND (grace_data_quota_bytes  IS NULL OR (grace_data_quota_bytes  > 0 AND grace_data_quota_bytes  <= 1099511627776))
    AND (grace_device_limit      IS NULL OR (grace_device_limit      > 0 AND grace_device_limit      <= 1000))),
  -- all-or-none typed policy: either fully UNCONFIGURED (all policy fields NULL, eligibility window keeps
  -- its default) or fully CONFIGURED (every policy field non-NULL + policy = REJECT_NEW_DEVICE).
  ADD CONSTRAINT grace_all_or_none CHECK (
    (grace_duration_seconds IS NULL AND grace_down_kbps IS NULL AND grace_up_kbps IS NULL
       AND grace_data_quota_bytes IS NULL AND grace_device_limit IS NULL AND grace_device_limit_policy IS NULL)
    OR
    (grace_duration_seconds IS NOT NULL AND grace_down_kbps IS NOT NULL AND grace_up_kbps IS NOT NULL
       AND grace_data_quota_bytes IS NOT NULL AND grace_device_limit IS NOT NULL
       AND grace_device_limit_policy = 'REJECT_NEW_DEVICE')),
  -- the typed columns are the SOLE source of truth; config jsonb must NOT carry duplicate authoritative keys.
  ADD CONSTRAINT grace_config_no_dup_policy_keys CHECK (
    NOT (config ?| ARRAY[
      'eligibility_window_seconds','grace_duration_seconds','grace_down_kbps','grace_up_kbps',
      'grace_data_quota_bytes','grace_device_limit','grace_device_limit_policy',
      'device_limit_policy','data_quota_bytes','duration_seconds','down_kbps','up_kbps']));

-- ============================================================================
-- (4c) checkout_grace_audit — DURABLE, append-only, ONE-per-episode evidence for every atomic Checkout
--      conversion outcome (normal grace, Emergency fallback, or fail-closed no-grace). This is the durable
--      substitute for a transient ConfigInvalidAlert return flag: the critical CHECKOUT_GRACE_CONFIG_INVALID
--      alert, the pinned Emergency policy version, the trusted-vs-clock-suspect boundary, and a bounded machine
--      reason code are committed IN THE SAME transaction as the conversion. The UNIQUE(stay, lifecycle_version)
--      makes a duplicate/concurrent Checkout unable to create a second alert/audit for the same episode. NO
--      room / guest-name / reservation / folio / credential payload is stored (machine codes + ids + times).
-- ============================================================================
CREATE TABLE iam_v2.checkout_grace_audit (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  stay_id uuid NOT NULL, lifecycle_version int NOT NULL CHECK (lifecycle_version > 0),
  trigger text NOT NULL CHECK (trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE','NO_GRACE')),
  is_emergency boolean NOT NULL DEFAULT false,
  policy_version text NOT NULL CHECK (policy_version ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  alert_code text CHECK (alert_code IS NULL OR alert_code = 'CHECKOUT_GRACE_CONFIG_INVALID'),
  reason_code text NOT NULL CHECK (reason_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  grace_entitlement_id uuid,
  -- boundary provenance (item 5/11): the durable Stay Event that established the boundary + its normalization,
  -- and the bounded fallback reason when the server-clock conservative boundary was used. A Phase-3 Checkout
  -- audit MUST cite a boundary Event (NOT NULL); the provenance trigger proves it is the typed GO checkout event
  -- for the exact scope with matching seq/normalization.
  boundary_event_id uuid NOT NULL,
  boundary_event_seq bigint,
  boundary_normalization_version int,
  boundary_reason_code text NOT NULL CHECK (boundary_reason_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  -- the exact site grace-config version pinned at conversion (item 9) — a concurrent Admin publish cannot
  -- retroactively change what this episode was converted against.
  config_version bigint NOT NULL DEFAULT 0,
  boundary_at timestamptz NOT NULL,
  boundary_clock_suspect boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  -- exactly one audit/alert per Stay lifecycle episode (idempotent + concurrent single-winner)
  UNIQUE (tenant_id, site_id, stay_id, lifecycle_version),
  UNIQUE (tenant_id, site_id, id), -- composite target for the alert-action FK

  -- (item 11) full trigger<->emergency<->policy_version<->alert<->grace_entitlement coherence. IS NOT DISTINCT
  -- FROM keeps NULLs from making a branch evaluate to NULL (which a CHECK treats as satisfied).
  CONSTRAINT cga_coherent CHECK (
    (trigger = 'CHECKOUT_GRACE' AND is_emergency = false AND policy_version = 'CHECKOUT_GRACE_V1'
       AND alert_code IS NULL AND grace_entitlement_id IS NOT NULL)
    OR (trigger = 'EMERGENCY_GRACE' AND is_emergency = true AND policy_version = 'EMERGENCY_GRACE_V1'
       AND alert_code IS NOT DISTINCT FROM 'CHECKOUT_GRACE_CONFIG_INVALID' AND grace_entitlement_id IS NOT NULL)
    OR (trigger = 'NO_GRACE' AND is_emergency = false AND policy_version = 'NONE'
       AND alert_code IS NULL AND grace_entitlement_id IS NULL)),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id)
    REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id) ON DELETE CASCADE,
  -- the pinned grace Entitlement (when present) belongs to the SAME tenant/site/stay (traceability)
  FOREIGN KEY (tenant_id, site_id, grace_entitlement_id)
    REFERENCES iam_v2.entitlements (tenant_id, site_id, id));

-- append-only: no UPDATE, no DELETE (immutable sanitized evidence).
CREATE OR REPLACE FUNCTION iam_v2.p3_checkout_grace_audit_appendonly() RETURNS trigger
  LANGUAGE plpgsql
  SET search_path = iam_v2, pg_temp
  AS $fn$
BEGIN
  RAISE EXCEPTION 'iam_v2.checkout_grace_audit is append-only (% rejected)', TG_OP;
END $fn$;
CREATE TRIGGER p3_checkout_grace_audit_guard
  BEFORE UPDATE OR DELETE ON iam_v2.checkout_grace_audit
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_checkout_grace_audit_appendonly();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_checkout_grace_audit_appendonly() FROM PUBLIC;

-- active operational-alert view (item 11): the durable audit rows carrying a critical alert, surfaced as an
-- addressable operational queue Hotel-Admin/monitoring can read and resolve. An alert stored only inside
-- historical evidence is not operational on its own — this view IS the sourced queue.
CREATE VIEW iam_v2.active_operational_alerts AS
  SELECT id AS audit_id, tenant_id, site_id, pms_interface_id, stay_id, lifecycle_version,
         alert_code, trigger, policy_version, reason_code, boundary_at, boundary_clock_suspect, created_at
  FROM iam_v2.checkout_grace_audit
  WHERE alert_code IS NOT NULL;

-- ============================================================================
-- (4d) entitlement_state_transitions — APPEND-ONLY immutable lifecycle history with effective timestamps, so a
--      Checkout can prove the EXACT Entitlement state at a PAST effective_checkout boundary instead of inferring
--      it from the current row. State-at-boundary = the transition with the greatest effective_at <= boundary.
-- ============================================================================
CREATE TABLE iam_v2.entitlement_state_transitions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, entitlement_id uuid NOT NULL,
  seq bigint NOT NULL CHECK (seq >= 1),
  from_state text CHECK (from_state IS NULL OR from_state IN ('PENDING','ACTIVE','SUSPENDED','TERMINATED')),
  to_state text NOT NULL CHECK (to_state IN ('PENDING','ACTIVE','SUSPENDED','TERMINATED')),
  effective_at timestamptz NOT NULL,
  reason text CHECK (reason IS NULL OR reason ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (entitlement_id, seq),
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id) ON DELETE CASCADE);
CREATE INDEX est_boundary_lookup ON iam_v2.entitlement_state_transitions (entitlement_id, effective_at);

-- ============================================================================
-- (4e) entitlement_device_authorizations — APPEND-ONLY authorization INTERVALS, so a Checkout can prove a device
--      was authorized AT the boundary (interval contains effective_checkout_at) rather than trusting the current
--      AUTHORIZED row. deauthorized_at NULL = still authorized.
-- ============================================================================
CREATE TABLE iam_v2.entitlement_device_authorizations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  entitlement_id uuid NOT NULL, device_id uuid NOT NULL,
  seq bigint NOT NULL CHECK (seq >= 1),
  authorized_at timestamptz NOT NULL,
  deauthorized_at timestamptz,
  CHECK (deauthorized_at IS NULL OR deauthorized_at >= authorized_at),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (entitlement_id, device_id, seq),
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices (tenant_id, site_id, id));
CREATE INDEX eda_boundary_lookup ON iam_v2.entitlement_device_authorizations (entitlement_id, device_id, authorized_at);

-- shared append-only guard for the two history tables (immutable: INSERT-only; the ONLY permitted UPDATE is
-- closing an open device-authorization interval by setting deauthorized_at once).
CREATE OR REPLACE FUNCTION iam_v2.p3_history_appendonly() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION '%: append-only history (DELETE rejected)', TG_TABLE_NAME;
  END IF;
  -- UPDATE: only entitlement_device_authorizations.deauthorized_at may go NULL->a value once; nothing else.
  IF TG_TABLE_NAME = 'entitlement_device_authorizations' THEN
    IF OLD.deauthorized_at IS NOT NULL THEN
      RAISE EXCEPTION 'entitlement_device_authorizations interval is immutable once closed';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.entitlement_id IS DISTINCT FROM OLD.entitlement_id
       OR NEW.device_id IS DISTINCT FROM OLD.device_id OR NEW.seq IS DISTINCT FROM OLD.seq
       OR NEW.authorized_at IS DISTINCT FROM OLD.authorized_at THEN
      RAISE EXCEPTION 'entitlement_device_authorizations identity/interval-start immutable';
    END IF;
    IF NEW.deauthorized_at IS NULL THEN
      RAISE EXCEPTION 'entitlement_device_authorizations UPDATE must close the interval (set deauthorized_at)';
    END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION '%: append-only history (UPDATE rejected)', TG_TABLE_NAME;
END $fn$;
CREATE TRIGGER p3_est_appendonly BEFORE UPDATE OR DELETE ON iam_v2.entitlement_state_transitions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();
CREATE TRIGGER p3_eda_appendonly BEFORE UPDATE OR DELETE ON iam_v2.entitlement_device_authorizations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_history_appendonly() FROM PUBLIC;

-- (item 5) INSERT-time STATE-MACHINE guard: exactly one initial (seq=1, from_state=NULL); every later seq is
-- previous+1 with from_state=previous.to_state; effective_at never moves backwards; only legal transition edges;
-- and the new (always-latest) transition's to_state MUST equal the entitlement's current status (so history and
-- the row can never diverge). Combined with p3_entitlement_status_guard, every status change is history-backed.
CREATE OR REPLACE FUNCTION iam_v2.p3_est_insert_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE prev_seq bigint; prev_to text; prev_at timestamptz; cur_status text;
BEGIN
  SELECT seq, to_state, effective_at INTO prev_seq, prev_to, prev_at
    FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = NEW.entitlement_id ORDER BY seq DESC LIMIT 1;
  IF prev_seq IS NULL THEN
    IF NEW.seq <> 1 THEN RAISE EXCEPTION 'first entitlement transition must have seq=1 (got %)', NEW.seq; END IF;
    IF NEW.from_state IS NOT NULL THEN RAISE EXCEPTION 'initial transition from_state must be NULL'; END IF;
  ELSE
    IF NEW.seq <> prev_seq + 1 THEN RAISE EXCEPTION 'entitlement transition seq must be contiguous (% -> %)', prev_seq, NEW.seq; END IF;
    IF NEW.from_state IS DISTINCT FROM prev_to THEN RAISE EXCEPTION 'from_state % must equal previous to_state %', NEW.from_state, prev_to; END IF;
    IF NEW.effective_at < prev_at THEN RAISE EXCEPTION 'transition effective_at cannot move backwards'; END IF;
  END IF;
  IF NEW.from_state IS NOT NULL AND NOT (
       (NEW.from_state='PENDING'   AND NEW.to_state IN ('ACTIVE','SUSPENDED','TERMINATED'))
    OR (NEW.from_state='ACTIVE'    AND NEW.to_state IN ('SUSPENDED','TERMINATED'))
    OR (NEW.from_state='SUSPENDED' AND NEW.to_state IN ('ACTIVE','TERMINATED'))) THEN
    RAISE EXCEPTION 'illegal entitlement transition % -> % (TERMINATED is terminal)', NEW.from_state, NEW.to_state;
  END IF;
  SELECT status INTO cur_status FROM iam_v2.entitlements WHERE id = NEW.entitlement_id;
  IF NEW.to_state IS DISTINCT FROM cur_status THEN
    RAISE EXCEPTION 'transition to_state % must equal entitlement current status % (use apply_entitlement_transition)', NEW.to_state, cur_status;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_est_insert BEFORE INSERT ON iam_v2.entitlement_state_transitions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_est_insert_guard();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_est_insert_guard() FROM PUBLIC;

-- (item 3/5) UNSPOOFABLE status<->history coherence. A caller-settable session GUC is NOT an authorization
-- boundary, so there is no such flag: instead a DEFERRED constraint trigger proves AT COMMIT that every
-- entitlement's current status equals its LATEST recorded transition. A raw `UPDATE entitlements SET status=...`
-- that does not also append the matching transition leaves status != latest-transition and rolls the whole
-- transaction back at commit; likewise an entitlement inserted without its initial transition fails closed
-- (there is no entitlement without history). The privilege model is the second layer: runtime roles receive NO
-- direct UPDATE grant on entitlements (dark), so only the owner-run controlled function mutates status. Because
-- the check is deferred, the controlled function may update the row then append the transition in either order
-- within its transaction.
-- (item 2) SECURITY DEFINER: this check runs at COMMIT as whichever role committed. An EXECUTE-only caller
-- (schema USAGE + EXECUTE on its approved function, and NO direct table privileges) must not be forced to hold
-- table SELECT merely so the deferred consistency checker can read. It is read-only (no mutation), fixed
-- search_path, no dynamic SQL, PUBLIC EXECUTE revoked; its owner needs only SELECT on entitlements +
-- entitlement_state_transitions.
CREATE OR REPLACE FUNCTION iam_v2.p3_entitlement_status_coherent() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE latest text; cur text;
BEGIN
  -- re-read the CURRENT (final, at-commit) row status rather than trusting the deferred trigger's captured NEW:
  -- a row updated several times in one tx queues several deferred events, each carrying a stale intermediate
  -- NEW — only the final committed status must agree with the latest transition.
  SELECT status INTO cur FROM iam_v2.entitlements WHERE id = NEW.id;
  IF cur IS NULL THEN RETURN NULL; END IF; -- row removed within the tx; nothing to check
  SELECT to_state INTO latest FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = NEW.id ORDER BY seq DESC LIMIT 1;
  IF latest IS DISTINCT FROM cur THEN
    RAISE EXCEPTION 'entitlement % status % is not backed by its latest transition % (use apply_entitlement_transition)',
      NEW.id, cur, COALESCE(latest, 'NONE');
  END IF;
  RETURN NULL;
END $fn$;
CREATE CONSTRAINT TRIGGER p3_entitlement_status_coherent
  AFTER INSERT OR UPDATE ON iam_v2.entitlements
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_entitlement_status_coherent();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_entitlement_status_coherent() FROM PUBLIC;

-- (item 5) THE controlled transition operation: locks the entitlement, updates status + terminal/activation
-- fields, and appends the matching history row ATOMICALLY (exactly one concurrent writer wins via the row lock).
-- Every Phase-2/3 Entitlement path (grant/activate/suspend/reactivate/expire/checkout-terminate/grace) uses it.
-- (item 4) THE CONTROLLED WRITER. SECURITY DEFINER so it executes as the schema OWNER: inside it current_user
-- is the owner, outside it is the caller. That is an UNSPOOFABLE authorization boundary (unlike a session GUC a
-- caller can set) and it lets a future runtime role mutate Entitlement status WITHOUT holding any direct
-- UPDATE/INSERT grant on entitlements or the history table. Fixed search_path, no dynamic SQL, PUBLIC revoked.
-- NOTE (DARK): EXECUTE is deliberately granted to NO runtime role yet — Phase 3 keeps ZERO runtime iam_v2
-- privileges (gate-enforced). The exact per-service EXECUTE grants land with the Gate-P/cutover privilege step.
CREATE OR REPLACE FUNCTION iam_v2.apply_entitlement_transition(p_ent uuid, p_to text, p_at timestamptz, p_reason text) RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_from text; v_seq bigint; v_prev_at timestamptz; v_at timestamptz; v_term text;
BEGIN
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'transition reason must be a bounded machine code';
  END IF;
  SELECT status INTO v_from FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;
  IF v_from IS NULL THEN RAISE EXCEPTION 'entitlement % not found', p_ent; END IF;
  SELECT COALESCE(max(seq),0)+1, max(effective_at) INTO v_seq, v_prev_at
    FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = p_ent;
  -- effective_at never moves backwards: a transition is recorded at the LATER of the requested time and the
  -- previous transition (a state change discovered late is stamped no earlier than the prior known state).
  v_at := GREATEST(p_at, COALESCE(v_prev_at, p_at));
  -- terminal_reason is the entitlements enum; a non-enum transition reason (e.g. a SEED/GRACE code) maps to OTHER.
  v_term := CASE WHEN p_to='TERMINATED' THEN
    (CASE WHEN p_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN','REVOKED','SUPERSEDED','CONVERTED','TRANSFERRED','CANCELLED','OTHER')
          THEN p_reason ELSE 'OTHER' END) ELSE NULL END;
  -- no session flag: status/history coherence is proven by the DEFERRED p3_entitlement_status_coherent trigger.
  UPDATE iam_v2.entitlements SET
    status = p_to,
    activated_at    = CASE WHEN p_to='ACTIVE' AND activated_at IS NULL THEN v_at ELSE activated_at END,
    terminal_reason = v_term,
    terminated_at   = CASE WHEN p_to='TERMINATED' THEN v_at ELSE NULL END
  WHERE id = p_ent;
  INSERT INTO iam_v2.entitlement_state_transitions(tenant_id,site_id,entitlement_id,seq,from_state,to_state,effective_at,reason)
    SELECT tenant_id, site_id, id, v_seq, CASE WHEN v_seq=1 THEN NULL ELSE v_from END, p_to, v_at, p_reason
    FROM iam_v2.entitlements WHERE id = p_ent;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text) FROM PUBLIC;

-- (item 4/7) CONTROLLED-WRITER AUTHORIZATION BOUNDARY. A caller can set any session GUC, but it cannot become
-- the schema owner: inside a SECURITY DEFINER function current_user IS the owner, outside it is the caller.
-- These guards therefore reject a NON-OWNER's raw status UPDATE, forged history INSERT, or direct authoritative
-- grace-policy UPDATE (even one that correctly recomputes config_version+1), while the controlled functions
-- pass. The deferred status/history coherence trigger stays as defense-in-depth behind this boundary.
-- (item 2) PER-FAMILY owner resolution. The permitted writer identity is the EXACT owner of that family's
-- approved controlled function, resolved from the catalog by its unambiguous regprocedure signature (never a
-- bare name — overloads would be ambiguous — and never a caller-supplied GUC/application_name/role string).
-- This lets Gate-P reassign each callable function to its own dedicated minimum-privilege NOLOGIN owner without
-- touching these table guards. Fails CLOSED when the function or its owner cannot be resolved.
CREATE OR REPLACE FUNCTION iam_v2.p3_controlled_writer_owner(p_family text) RETURNS text
  LANGUAGE plpgsql STABLE SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_sig text; v_oid oid; v_owner text;
BEGIN
  v_sig := CASE p_family
    WHEN 'entitlement' THEN 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)'
    WHEN 'grace_config' THEN 'iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int)'
    ELSE NULL END;
  IF v_sig IS NULL THEN
    RAISE EXCEPTION 'no approved controlled-writer family %', p_family;
  END IF;
  v_oid := to_regprocedure(v_sig);            -- NULL (not an error) when unresolvable
  IF v_oid IS NULL THEN
    RAISE EXCEPTION 'controlled-writer function % is not resolvable (fail closed)', v_sig;
  END IF;
  SELECT pg_get_userbyid(proowner) INTO v_owner FROM pg_proc WHERE oid = v_oid;
  IF v_owner IS NULL OR v_owner = '' THEN
    RAISE EXCEPTION 'controlled-writer owner for % is not resolvable (fail closed)', v_sig;
  END IF;
  RETURN v_owner;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_controlled_writer_owner(text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.p3_controlled_writer_only() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE owner_role text; changed boolean := true; v_sig text; v_oid oid;
BEGIN
  -- resolve the family's approved function owner INLINE (catalog-only). Deliberately NOT a call to the
  -- introspection helper: this trigger fires as whichever role is writing, and a cross-function EXECUTE
  -- dependency would break exactly the dedicated-owner separation Gate-P needs.
  v_sig := CASE WHEN TG_TABLE_NAME = 'site_checkout_grace_config'
                THEN 'iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int)'
                ELSE 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)' END;
  v_oid := to_regprocedure(v_sig);
  IF v_oid IS NULL THEN
    RAISE EXCEPTION 'controlled-writer function % is not resolvable (fail closed)', v_sig;
  END IF;
  SELECT pg_get_userbyid(proowner) INTO owner_role FROM pg_proc WHERE oid = v_oid;
  IF owner_role IS NULL OR owner_role = '' THEN
    RAISE EXCEPTION 'controlled-writer owner for % is not resolvable (fail closed)', v_sig;
  END IF;
  -- (item 1) DELETE of the authoritative site grace config is ALWAYS a controlled-writer-only operation. There
  -- is no approved ordinary DELETE for this row; a future reset/disable must be its own audited, PO-approved API
  -- with explicit semantics (this guard deliberately does NOT silently convert DELETE into "disable").
  IF TG_OP = 'DELETE' THEN
    IF current_user <> owner_role THEN
      RAISE EXCEPTION '%: DELETE goes through an approved controlled iam_v2 writer (caller %)',
        TG_TABLE_NAME, current_user;
    END IF;
    RETURN OLD;
  END IF;
  IF TG_TABLE_NAME = 'entitlements' THEN
    changed := (NEW.status IS DISTINCT FROM OLD.status);   -- only status is controlled-writer-only
  ELSIF TG_TABLE_NAME = 'site_checkout_grace_config' AND TG_OP = 'UPDATE' THEN
    changed := (NEW.grace_package_revision_id IS DISTINCT FROM OLD.grace_package_revision_id
      OR NEW.grace_duration_seconds IS DISTINCT FROM OLD.grace_duration_seconds
      OR NEW.grace_down_kbps IS DISTINCT FROM OLD.grace_down_kbps
      OR NEW.grace_up_kbps IS DISTINCT FROM OLD.grace_up_kbps
      OR NEW.grace_data_quota_bytes IS DISTINCT FROM OLD.grace_data_quota_bytes
      OR NEW.grace_device_limit IS DISTINCT FROM OLD.grace_device_limit
      OR NEW.grace_device_limit_policy IS DISTINCT FROM OLD.grace_device_limit_policy
      OR NEW.eligibility_window_seconds IS DISTINCT FROM OLD.eligibility_window_seconds
      OR NEW.config_version IS DISTINCT FROM OLD.config_version);
  END IF;
  IF changed AND current_user <> owner_role THEN
    RAISE EXCEPTION '%: authoritative writes go through the controlled iam_v2 writer functions (caller %)',
      TG_TABLE_NAME, current_user;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_entitlement_controlled_writer BEFORE UPDATE ON iam_v2.entitlements
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_est_controlled_writer BEFORE INSERT ON iam_v2.entitlement_state_transitions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
-- (item 1) INSERT is protected too: seeding the FIRST authoritative grace-config row is itself an authoritative
-- write, so it must come through publish_checkout_grace_config() and never from a raw non-owner INSERT.
CREATE TRIGGER p3_grace_config_controlled_writer BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.site_checkout_grace_config
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_controlled_writer_only() FROM PUBLIC;

-- (item 7) INSERT-time DEVICE-AUTHORIZATION-INTERVAL guard: contiguous seq per (entitlement,device); at most one
-- OPEN interval; a new interval cannot begin before the prior one closes; and it must reference a real
-- entitlement_devices binding. Combined with the append-only guard (which only allows closing the latest open
-- interval once), the interval history is a clean monotonic timeline.
CREATE OR REPLACE FUNCTION iam_v2.p3_eda_insert_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE prev_seq bigint; prev_deauth timestamptz; prev_open int;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM iam_v2.entitlement_devices ed
                 WHERE ed.entitlement_id=NEW.entitlement_id AND ed.device_id=NEW.device_id) THEN
    RAISE EXCEPTION 'device authorization requires an entitlement_devices binding';
  END IF;
  SELECT seq, deauthorized_at INTO prev_seq, prev_deauth
    FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id=NEW.entitlement_id AND device_id=NEW.device_id ORDER BY seq DESC LIMIT 1;
  SELECT count(*) INTO prev_open FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id=NEW.entitlement_id AND device_id=NEW.device_id AND deauthorized_at IS NULL;
  IF prev_seq IS NULL THEN
    IF NEW.seq <> 1 THEN RAISE EXCEPTION 'first device authorization must have seq=1'; END IF;
  ELSE
    IF NEW.seq <> prev_seq + 1 THEN RAISE EXCEPTION 'device authorization seq must be contiguous'; END IF;
    IF prev_open > 0 THEN RAISE EXCEPTION 'a device may not have two open authorization intervals'; END IF;
    IF NEW.authorized_at < prev_deauth THEN RAISE EXCEPTION 'new authorization cannot begin before the prior interval closed'; END IF;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_eda_insert BEFORE INSERT ON iam_v2.entitlement_device_authorizations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_eda_insert_guard();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_eda_insert_guard() FROM PUBLIC;

-- ============================================================================
-- (4f) site_checkout_grace_config.config_version — monotonic version bumped on every Admin publish, so a
--      Checkout can pin the EXACT config it converted against (item 9) and a concurrent publish only affects
--      later Checkouts.
-- ============================================================================
ALTER TABLE iam_v2.site_checkout_grace_config
  ADD COLUMN config_version bigint NOT NULL DEFAULT 1 CHECK (config_version >= 1);

-- ============================================================================
-- (4g) reserved Emergency-Grace namespace protection (item 7): the reserved codes may ONLY name a system-owned
--      object; such an object's code/is_system identity is immutable and it cannot be deleted through ordinary
--      DML (Hotel Admin). A pre-existing non-system row with the reserved code is REJECTED (never adopted).
-- ============================================================================
-- Table-aware: internet_packages carries is_system (a reserved-code package MUST be system-owned + immutable
-- identity); service_plans has no is_system column (its reserved-code plan is protected by code immutability +
-- no-delete only). The distinguishing invariant on both tables is: the reserved code cannot be deleted, and its
-- code cannot be re-pointed once created.
CREATE OR REPLACE FUNCTION iam_v2.p3_reserved_grace_codes() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE reserved text[] := ARRAY['__sys_emergency_grace_plan__','__sys_emergency_grace_pkg__'];
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.code = ANY(reserved) THEN
      RAISE EXCEPTION 'reserved system grace object % cannot be deleted', OLD.code;
    END IF;
    RETURN OLD;
  END IF;
  -- protect BOTH the old and the new code: a reserved object cannot be renamed AWAY (OLD reserved -> NEW not),
  -- nor can a non-reserved row be renamed INTO the reserved namespace as a non-system object.
  IF TG_OP = 'UPDATE' AND OLD.code = ANY(reserved) AND NEW.code IS DISTINCT FROM OLD.code THEN
    RAISE EXCEPTION 'reserved system grace object code is immutable (cannot rename away from %)', OLD.code;
  END IF;
  IF NEW.code = ANY(reserved) THEN
    IF TG_TABLE_NAME = 'internet_packages' THEN
      IF NEW.is_system IS NOT TRUE THEN
        RAISE EXCEPTION 'reserved grace code % requires a system-owned package', NEW.code;
      END IF;
      IF NEW.active IS NOT TRUE THEN
        RAISE EXCEPTION 'reserved system grace package cannot be disabled';
      END IF;
      IF TG_OP = 'UPDATE' THEN
        IF NEW.is_system IS DISTINCT FROM OLD.is_system THEN
          RAISE EXCEPTION 'reserved system grace package is_system is immutable';
        END IF;
        -- current_revision_id may be SET once by bootstrap, never re-pointed afterwards.
        IF OLD.current_revision_id IS NOT NULL AND NEW.current_revision_id IS DISTINCT FROM OLD.current_revision_id THEN
          RAISE EXCEPTION 'reserved system grace package current revision cannot be re-pointed';
        END IF;
      END IF;
    ELSIF TG_TABLE_NAME = 'service_plans' THEN
      IF NEW.enabled IS NOT TRUE THEN
        RAISE EXCEPTION 'reserved system grace plan cannot be disabled';
      END IF;
      IF TG_OP = 'UPDATE' AND OLD.current_revision_id IS NOT NULL
         AND NEW.current_revision_id IS DISTINCT FROM OLD.current_revision_id THEN
        RAISE EXCEPTION 'reserved system grace plan current revision cannot be re-pointed';
      END IF;
    END IF;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_reserved_grace_plan BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.service_plans
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_reserved_grace_codes();
CREATE TRIGGER p3_reserved_grace_pkg BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.internet_packages
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_reserved_grace_codes();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_reserved_grace_codes() FROM PUBLIC;

-- Controlled, idempotent Site bootstrap of the canonical Emergency-Grace catalog (item 6). This is the ONLY
-- place the Emergency Package/Revision + Service-Plan/Revision are created — NOT the Checkout hot path. A
-- pre-existing reserved-code row with mismatching identity/values FAILS CLOSED (raises); it is never adopted.
CREATE OR REPLACE FUNCTION iam_v2.bootstrap_emergency_grace(p_tenant uuid, p_site uuid) RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_plan uuid; v_spr uuid; v_pkg uuid; v_ipr uuid;
BEGIN
  -- (item 9) serialize per tenant/site so >=24 concurrent bootstraps are safe (exactly one provisions; the rest
  -- verify). The tx-level advisory lock is released at commit; the caller supplies tenant/site as the key.
  PERFORM pg_advisory_xact_lock(hashtextextended(p_tenant::text || ':' || p_site::text || ':emergency-grace', 0));
  -- service plan (verify-before-mutate: a pre-existing row must already be system-shaped/enabled or we fail
  -- closed rather than adopt/repair it).
  SELECT id INTO v_plan FROM iam_v2.service_plans
    WHERE tenant_id=p_tenant AND site_id=p_site AND code='__sys_emergency_grace_plan__';
  IF v_plan IS NOT NULL THEN
    IF NOT EXISTS (SELECT 1 FROM iam_v2.service_plans WHERE id=v_plan AND enabled=true) THEN
      RAISE EXCEPTION 'reserved emergency service plan exists but is not enabled/system-shaped (fail closed)';
    END IF;
  END IF;
  IF v_plan IS NULL THEN
    INSERT INTO iam_v2.service_plans(tenant_id,site_id,code,enabled)
      VALUES (p_tenant,p_site,'__sys_emergency_grace_plan__',true) RETURNING id INTO v_plan;
  END IF;
  SELECT id INTO v_spr FROM iam_v2.service_plan_revisions WHERE service_plan_id=v_plan AND revision_no=1;
  IF v_spr IS NULL THEN
    INSERT INTO iam_v2.service_plan_revisions
      (tenant_id,site_id,service_plan_id,revision_no,name,down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
      VALUES (p_tenant,p_site,v_plan,1,'emergency-grace',5000,2000,1,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',524288000)
      RETURNING id INTO v_spr;
  ELSE
    -- (item 8) verify an EXISTING revision has the EXACT approved attributes BEFORE re-pointing anything; a
    -- poisoned revision RAISES and (via rollback) leaves every current-revision pointer unchanged.
    IF NOT EXISTS (SELECT 1 FROM iam_v2.service_plan_revisions WHERE id=v_spr
        AND down_kbps=5000 AND up_kbps=2000 AND max_concurrent_devices=1
        AND device_limit_policy='REJECT_NEW_DEVICE' AND time_accounting_mode='VALIDITY_WINDOW' AND data_quota_bytes=524288000) THEN
      RAISE EXCEPTION 'reserved emergency service-plan revision 1 has mismatching attributes (poisoned; fail closed)';
    END IF;
  END IF;
  UPDATE iam_v2.service_plans SET current_revision_id=v_spr WHERE id=v_plan AND current_revision_id IS DISTINCT FROM v_spr;
  -- package
  SELECT id INTO v_pkg FROM iam_v2.internet_packages WHERE tenant_id=p_tenant AND site_id=p_site AND code='__sys_emergency_grace_pkg__';
  IF v_pkg IS NOT NULL AND NOT EXISTS (SELECT 1 FROM iam_v2.internet_packages WHERE id=v_pkg AND is_system AND active) THEN
    RAISE EXCEPTION 'reserved emergency package exists but is not system/active (poisoned; fail closed)';
  END IF;
  IF v_pkg IS NULL THEN
    INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,is_system)
      VALUES (p_tenant,p_site,'__sys_emergency_grace_pkg__',true) RETURNING id INTO v_pkg;
  END IF;
  SELECT id INTO v_ipr FROM iam_v2.internet_package_revisions WHERE package_id=v_pkg AND revision_no=1;
  IF v_ipr IS NULL THEN
    INSERT INTO iam_v2.internet_package_revisions
      (tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
      VALUES (p_tenant,p_site,v_pkg,1,v_spr,'CHECKOUT_GRACE',0,ARRAY['NOT_REQUIRED']::text[],
              '{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":"EMERGENCY_GRACE_V1"}'::jsonb)
      RETURNING id INTO v_ipr;
  ELSE
    -- (item 8) verify the EXISTING package revision exactly (type/price/settlement/duration/end/version + its
    -- Plan-Revision relationship). Any mismatch is poisoned → RAISE (pointers unchanged).
    IF NOT EXISTS (SELECT 1 FROM iam_v2.internet_package_revisions WHERE id=v_ipr
        AND package_type='CHECKOUT_GRACE' AND price_minor=0 AND settlement_methods=ARRAY['NOT_REQUIRED']::text[]
        AND service_plan_revision_id=v_spr
        AND (duration_policy->>'grace_duration_seconds')='3600' AND (duration_policy->>'end_mode')='GRACE_AFTER_CHECKOUT'
        AND (duration_policy->>'policy_version')='EMERGENCY_GRACE_V1') THEN
      RAISE EXCEPTION 'reserved emergency package revision 1 has mismatching attributes (poisoned; fail closed)';
    END IF;
  END IF;
  UPDATE iam_v2.internet_packages SET current_revision_id=v_ipr WHERE id=v_pkg AND current_revision_id IS DISTINCT FROM v_ipr;
  -- final coherence assertion (the whole graph must be exactly OK after bootstrap).
  IF iam_v2.emergency_grace_health(p_tenant,p_site) <> 'OK' THEN
    RAISE EXCEPTION 'emergency-grace catalog not OK after bootstrap (fail closed)';
  END IF;
END $fn$;

-- Preflight/health check (item 6): returns 'OK' when the canonical Emergency catalog is present with the EXACT
-- approved attributes, else a bounded machine defect code the operator must resolve.
CREATE OR REPLACE FUNCTION iam_v2.emergency_grace_health(p_tenant uuid, p_site uuid) RETURNS text
  LANGUAGE sql STABLE SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE((
    SELECT CASE WHEN
        ip.is_system AND ip.current_revision_id = ipr.id
        AND ipr.package_type='CHECKOUT_GRACE' AND ipr.price_minor=0
        AND ipr.settlement_methods = ARRAY['NOT_REQUIRED']::text[]
        AND (ipr.duration_policy->>'grace_duration_seconds')='3600'
        AND (ipr.duration_policy->>'policy_version')='EMERGENCY_GRACE_V1'
        AND sp.enabled AND sp.current_revision_id = spr.id
        AND spr.down_kbps=5000 AND spr.up_kbps=2000 AND spr.data_quota_bytes=524288000
        AND spr.max_concurrent_devices=1 AND spr.device_limit_policy='REJECT_NEW_DEVICE'
        AND spr.time_accounting_mode='VALIDITY_WINDOW'
      THEN 'OK' ELSE 'EMERGENCY_GRACE_CATALOG_INVALID' END
    FROM iam_v2.internet_packages ip
    JOIN iam_v2.internet_package_revisions ipr ON ipr.tenant_id=ip.tenant_id AND ipr.site_id=ip.site_id AND ipr.package_id=ip.id AND ipr.revision_no=1
    JOIN iam_v2.service_plan_revisions spr ON spr.tenant_id=ipr.tenant_id AND spr.site_id=ipr.site_id AND spr.id=ipr.service_plan_revision_id
    JOIN iam_v2.service_plans sp ON sp.tenant_id=spr.tenant_id AND sp.site_id=spr.site_id AND sp.id=spr.service_plan_id
    WHERE ip.tenant_id=p_tenant AND ip.site_id=p_site AND ip.code='__sys_emergency_grace_pkg__'
  ), 'EMERGENCY_GRACE_CATALOG_ABSENT');
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.bootstrap_emergency_grace(uuid,uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.emergency_grace_health(uuid,uuid) FROM PUBLIC;

-- ============================================================================
-- (4h) stays.last_applied_event_id (item 3): the EXACT durable Stay Event whose application last advanced the
--      Stay, so the Checkout boundary verifier can prove exact event lineage (not a "seq >= counter" heuristic).
--      The Stay engine pins it on every applied event; it FKs the durable event.
-- ============================================================================
-- STRUCTURAL lineage scope: a plain uuid FK could still point at another Tenant/Site/Interface's event, so the
-- reference is COMPOSITE-SCOPED. stay_events gets a matching unique key first.
ALTER TABLE iam_v2.stay_events
  ADD CONSTRAINT stay_events_scoped_identity UNIQUE (tenant_id, site_id, pms_interface_id, id);
ALTER TABLE iam_v2.stays ADD COLUMN last_applied_event_id uuid;
ALTER TABLE iam_v2.stays
  ADD CONSTRAINT stays_last_applied_event_scoped
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, last_applied_event_id)
  REFERENCES iam_v2.stay_events (tenant_id, site_id, pms_interface_id, id);

-- ============================================================================
-- (4i) publish_checkout_grace_config (item 10): the SOLE controlled Hotel-Admin publication of the typed grace
--      policy. It locks the site config (approved lock order), increments config_version by EXACTLY 1 (rejecting
--      any decrease/jump/no-op), and applies only to later Checkouts. The no-config-row case serializes via a
--      site-scoped advisory lock so a concurrent first publication cannot double-create.
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.publish_checkout_grace_config(
    p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration int, p_down int, p_up int, p_quota bigint,
    p_dev_limit int, p_dev_policy text, p_eligibility int) RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_ver bigint;
BEGIN
  -- (item 2) eligibility_window_seconds is an AUTHORITATIVE grace-policy field: validated, versioned, compared
  -- for idempotency and included in material-change detection exactly like the shaping/quota/device fields.
  IF p_eligibility IS NULL OR p_eligibility <= 0 OR p_eligibility > 604800 THEN
    RAISE EXCEPTION 'eligibility_window_seconds must be within 1..604800 (got %)', p_eligibility;
  END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(p_tenant::text || ':' || p_site::text || ':grace-config', 0));
  SELECT config_version INTO v_ver FROM iam_v2.site_checkout_grace_config
    WHERE tenant_id=p_tenant AND site_id=p_site FOR UPDATE;
  IF v_ver IS NULL THEN
    INSERT INTO iam_v2.site_checkout_grace_config
      (tenant_id,site_id,grace_package_revision_id,grace_duration_seconds,grace_down_kbps,grace_up_kbps,
       grace_data_quota_bytes,grace_device_limit,grace_device_limit_policy,eligibility_window_seconds,config_version)
      VALUES (p_tenant,p_site,p_pkg_rev,p_duration,p_down,p_up,p_quota,p_dev_limit,p_dev_policy,p_eligibility,1);
    RETURN 1;
  END IF;
  -- idempotent re-publication of the IDENTICAL typed policy does NOT bump the version (a material change does).
  IF EXISTS (SELECT 1 FROM iam_v2.site_checkout_grace_config
             WHERE tenant_id=p_tenant AND site_id=p_site
               AND grace_package_revision_id IS NOT DISTINCT FROM p_pkg_rev
               AND grace_duration_seconds IS NOT DISTINCT FROM p_duration
               AND grace_down_kbps IS NOT DISTINCT FROM p_down AND grace_up_kbps IS NOT DISTINCT FROM p_up
               AND grace_data_quota_bytes IS NOT DISTINCT FROM p_quota
               AND grace_device_limit IS NOT DISTINCT FROM p_dev_limit
               AND grace_device_limit_policy IS NOT DISTINCT FROM p_dev_policy
               AND eligibility_window_seconds IS NOT DISTINCT FROM p_eligibility) THEN
    RETURN v_ver;
  END IF;
  UPDATE iam_v2.site_checkout_grace_config SET
    grace_package_revision_id=p_pkg_rev, grace_duration_seconds=p_duration, grace_down_kbps=p_down,
    grace_up_kbps=p_up, grace_data_quota_bytes=p_quota, grace_device_limit=p_dev_limit,
    grace_device_limit_policy=p_dev_policy, eligibility_window_seconds=p_eligibility, config_version=v_ver+1
    WHERE tenant_id=p_tenant AND site_id=p_site;
  RETURN v_ver+1;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int) FROM PUBLIC;

-- (item 9) config_version integrity: a material policy change REQUIRES config_version = OLD+1; the version can
-- never decrease or jump; and the version may not be bumped without a change. This makes a raw UPDATE that
-- changes policy fields while leaving/decreasing/jumping config_version fail closed (the controlled publish
-- function is the only writer that satisfies it).
CREATE OR REPLACE FUNCTION iam_v2.p3_grace_config_version_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE policy_changed boolean;
BEGIN
  policy_changed := (NEW.grace_package_revision_id IS DISTINCT FROM OLD.grace_package_revision_id
    OR NEW.grace_duration_seconds IS DISTINCT FROM OLD.grace_duration_seconds
    OR NEW.grace_down_kbps IS DISTINCT FROM OLD.grace_down_kbps OR NEW.grace_up_kbps IS DISTINCT FROM OLD.grace_up_kbps
    OR NEW.grace_data_quota_bytes IS DISTINCT FROM OLD.grace_data_quota_bytes
    OR NEW.grace_device_limit IS DISTINCT FROM OLD.grace_device_limit
    OR NEW.grace_device_limit_policy IS DISTINCT FROM OLD.grace_device_limit_policy
    OR NEW.eligibility_window_seconds IS DISTINCT FROM OLD.eligibility_window_seconds);
  IF NEW.config_version < OLD.config_version THEN
    RAISE EXCEPTION 'site grace config_version cannot decrease (% -> %)', OLD.config_version, NEW.config_version;
  END IF;
  IF policy_changed AND NEW.config_version <> OLD.config_version + 1 THEN
    RAISE EXCEPTION 'a grace policy change must increment config_version by exactly 1 (use publish_checkout_grace_config)';
  END IF;
  IF NOT policy_changed AND NEW.config_version <> OLD.config_version THEN
    RAISE EXCEPTION 'config_version may not change without a policy change';
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_grace_config_version_guard BEFORE UPDATE ON iam_v2.site_checkout_grace_config
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_grace_config_version_guard();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_grace_config_version_guard() FROM PUBLIC;

-- ============================================================================
-- (4j) resolvable operational-alert model (item 12): the immutable audit rows are the EVIDENCE; an alert's
--      OPEN/ACKNOWLEDGED/RESOLVED lifecycle is a SEPARATE append-only action log (never an update that erases
--      evidence). The active view returns only alerts whose latest action is not RESOLVED.
-- ============================================================================
CREATE TABLE iam_v2.checkout_grace_alert_actions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  audit_id uuid NOT NULL,
  seq bigint NOT NULL CHECK (seq >= 1),
  action text NOT NULL CHECK (action IN ('OPEN','ACKNOWLEDGED','RESOLVED')),
  actor uuid,
  reason_code text CHECK (reason_code IS NULL OR reason_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (audit_id, seq),
  FOREIGN KEY (tenant_id, site_id, audit_id) REFERENCES iam_v2.checkout_grace_audit (tenant_id, site_id, id) ON DELETE CASCADE);
-- append-only lifecycle guard: seq contiguous; first is OPEN; RESOLVED is terminal; RESOLVED/ACK need an actor.
CREATE OR REPLACE FUNCTION iam_v2.p3_alert_action_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE prev_seq bigint; prev_action text;
BEGIN
  IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'checkout_grace_alert_actions is append-only (% rejected)', TG_OP; END IF;
  SELECT seq, action INTO prev_seq, prev_action FROM iam_v2.checkout_grace_alert_actions
    WHERE audit_id=NEW.audit_id ORDER BY seq DESC LIMIT 1;
  IF prev_seq IS NULL THEN
    IF NEW.seq <> 1 OR NEW.action <> 'OPEN' THEN RAISE EXCEPTION 'first alert action must be seq=1 OPEN'; END IF;
  ELSE
    IF NEW.seq <> prev_seq + 1 THEN RAISE EXCEPTION 'alert action seq must be contiguous'; END IF;
    -- (item 10) legal edges only: OPEN->ACKNOWLEDGED|RESOLVED, ACKNOWLEDGED->RESOLVED, RESOLVED terminal.
    -- Rejects OPEN->OPEN, ACKNOWLEDGED->OPEN, repeated ACKNOWLEDGED, and any action after RESOLVED.
    IF NOT ( (prev_action='OPEN'         AND NEW.action IN ('ACKNOWLEDGED','RESOLVED'))
          OR (prev_action='ACKNOWLEDGED' AND NEW.action='RESOLVED') ) THEN
      RAISE EXCEPTION 'illegal alert action edge % -> %', prev_action, NEW.action;
    END IF;
  END IF;
  IF NEW.action IN ('ACKNOWLEDGED','RESOLVED') AND NEW.actor IS NULL THEN
    RAISE EXCEPTION 'ACKNOWLEDGED/RESOLVED alert action requires an actor';
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_alert_action_insert BEFORE INSERT ON iam_v2.checkout_grace_alert_actions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_alert_action_guard();
CREATE TRIGGER p3_alert_action_appendonly BEFORE UPDATE OR DELETE ON iam_v2.checkout_grace_alert_actions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_alert_action_guard();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_alert_action_guard() FROM PUBLIC;

-- redefine the active view: an alert-bearing audit row whose latest action is not RESOLVED (an audit row with
-- no action yet is implicitly OPEN and therefore active).
CREATE OR REPLACE VIEW iam_v2.active_operational_alerts AS
  SELECT a.id AS audit_id, a.tenant_id, a.site_id, a.pms_interface_id, a.stay_id, a.lifecycle_version,
         a.alert_code, a.trigger, a.policy_version, a.reason_code, a.boundary_at, a.boundary_clock_suspect, a.created_at,
         COALESCE((SELECT act.action FROM iam_v2.checkout_grace_alert_actions act
                   WHERE act.audit_id=a.id ORDER BY act.seq DESC LIMIT 1), 'OPEN') AS state
  FROM iam_v2.checkout_grace_audit a
  WHERE a.alert_code IS NOT NULL
    AND COALESCE((SELECT act.action FROM iam_v2.checkout_grace_alert_actions act
                  WHERE act.audit_id=a.id ORDER BY act.seq DESC LIMIT 1), 'OPEN') <> 'RESOLVED';

-- ============================================================================
-- (4k) checkout audit provenance guard (item 11): the DB proves boundary-event + grace-entitlement lineage, not
--      just that the Go writer supplied matching UUIDs.
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p3_checkout_audit_provenance() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE ev RECORD; ent RECORD; pur_episode int;
BEGIN
  IF NEW.boundary_event_id IS NOT NULL THEN
    SELECT tenant_id, site_id, pms_interface_id, stay_id, event_type, processing_status, sequence_version, normalization_version
      INTO ev FROM iam_v2.stay_events WHERE id = NEW.boundary_event_id;
    IF ev.tenant_id IS NULL THEN RAISE EXCEPTION 'boundary_event_id does not reference a stay_event'; END IF;
    IF ev.tenant_id <> NEW.tenant_id OR ev.site_id <> NEW.site_id OR ev.pms_interface_id <> NEW.pms_interface_id
       OR ev.stay_id IS DISTINCT FROM NEW.stay_id THEN
      RAISE EXCEPTION 'boundary event scope must match the audit (tenant/site/interface/stay)';
    END IF;
    IF ev.event_type <> 'GO' THEN RAISE EXCEPTION 'boundary event must be the typed checkout (GO) event'; END IF;
    IF ev.processing_status <> 'APPLIED' THEN RAISE EXCEPTION 'boundary event must be APPLIED'; END IF;
    IF NEW.boundary_event_seq IS DISTINCT FROM ev.sequence_version
       OR NEW.boundary_normalization_version IS DISTINCT FROM ev.normalization_version THEN
      RAISE EXCEPTION 'audit boundary seq/normalization must match the source event';
    END IF;
  END IF;
  IF NEW.grace_entitlement_id IS NOT NULL THEN
    SELECT e.tenant_id, e.site_id, e.pms_interface_id, e.stay_id, e.purchase_id
      INTO ent FROM iam_v2.entitlements e WHERE e.id = NEW.grace_entitlement_id;
    IF ent.tenant_id IS NULL THEN RAISE EXCEPTION 'grace_entitlement_id does not reference an entitlement'; END IF;
    IF ent.tenant_id <> NEW.tenant_id OR ent.site_id <> NEW.site_id OR ent.pms_interface_id IS DISTINCT FROM NEW.pms_interface_id
       OR ent.stay_id IS DISTINCT FROM NEW.stay_id THEN
      RAISE EXCEPTION 'grace entitlement scope must match the audit (tenant/site/interface/stay)';
    END IF;
    SELECT checkout_episode INTO pur_episode FROM iam_v2.purchases WHERE id = ent.purchase_id;
    IF pur_episode IS DISTINCT FROM NEW.lifecycle_version THEN
      RAISE EXCEPTION 'grace purchase checkout_episode % must equal audit lifecycle_version %', pur_episode, NEW.lifecycle_version;
    END IF;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_checkout_audit_provenance BEFORE INSERT ON iam_v2.checkout_grace_audit
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_checkout_audit_provenance();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_checkout_audit_provenance() FROM PUBLIC;

-- ============================================================================
-- (6) stay_events: application-result columns + append-only lineage guard.
-- ============================================================================
ALTER TABLE iam_v2.stay_events
  ADD COLUMN processed_at timestamptz,
  ADD COLUMN review_code text
    CHECK (review_code IS NULL OR length(review_code) <= 200),
  -- §G durable append-first inbox: admission classification + isolated resync staging. LIVE rows are
  -- immediately consumable; RESYNC rows are staged under a typed resync generation and become consumable
  -- ONLY when a valid DE atomically activates the complete generation. Defaults keep every existing/base
  -- insert a consumable LIVE row (backward compatible).
  ADD COLUMN admission_kind text NOT NULL DEFAULT 'LIVE'
    CHECK (admission_kind IN ('LIVE','RESYNC')),
  ADD COLUMN admission_runtime_generation bigint NOT NULL DEFAULT 0
    CHECK (admission_runtime_generation >= 0),
  ADD COLUMN resync_generation bigint NOT NULL DEFAULT 0
    CHECK (resync_generation >= 0),
  ADD COLUMN fingerprint_key_version int NOT NULL DEFAULT 0
    CHECK (fingerprint_key_version >= 0);

-- admission coherence: a LIVE row has no resync generation (immediately consumable); a RESYNC row carries a
-- positive resync generation and is consumable only once the interface's published_resync_generation reaches
-- it. RESYNC Event rows are IMMUTABLE append-first evidence — publication is the single runtime-row boundary,
-- never a mass row update.
ALTER TABLE iam_v2.stay_events
  ADD CONSTRAINT se_admission_coherent CHECK (
       (admission_kind = 'LIVE'   AND resync_generation = 0)
    OR (admission_kind = 'RESYNC' AND resync_generation > 0));

-- Admission-aware idempotency. The baseline UNIQUE(tenant,site,pms_interface_id,external_event_identity)
-- deduplicates the whole table; that is wrong for resync (a fresh full roster must be able to restage a
-- record whose content matches an existing LIVE row). Replace it with two PARTIAL unique indexes: LIVE rows
-- dedup within the interface; RESYNC rows dedup within their resync generation.
DO $$
DECLARE cn text;
BEGIN
  SELECT conname INTO cn FROM pg_constraint
   WHERE conrelid = 'iam_v2.stay_events'::regclass AND contype = 'u'
     AND pg_get_constraintdef(oid) LIKE '%external_event_identity%'
     AND pg_get_constraintdef(oid) NOT LIKE '%resync_generation%';
  IF cn IS NOT NULL THEN
    EXECUTE format('ALTER TABLE iam_v2.stay_events DROP CONSTRAINT %I', cn);
  END IF;
END$$;
CREATE UNIQUE INDEX IF NOT EXISTS se_live_identity
  ON iam_v2.stay_events (tenant_id, site_id, pms_interface_id, external_event_identity)
  WHERE admission_kind = 'LIVE';
CREATE UNIQUE INDEX IF NOT EXISTS se_resync_identity
  ON iam_v2.stay_events (tenant_id, site_id, pms_interface_id, resync_generation, external_event_identity)
  WHERE admission_kind = 'RESYNC';

-- Append-first lifecycle: INSERT must be PENDING with no result/lineage; the ONLY mutation is a single
-- PENDING->terminal update that sets processed_at (+ stay_id/review_code per result rules); once terminal
-- the row is immutable. Cross-interface stay_id is already structurally rejected by the base composite
-- FK (tenant,site,pms_interface_id,stay_id)->stays; the trigger adds a clear error + the lifecycle rules.
CREATE OR REPLACE FUNCTION iam_v2.p3_stay_event_appendonly() RETURNS trigger
  LANGUAGE plpgsql
  SET search_path = iam_v2, pg_temp
  AS $fn$
DECLARE ok_stay int;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'stay_events is append-only (DELETE rejected)';
  END IF;

  IF TG_OP = 'INSERT' THEN
    IF NEW.processing_status <> 'PENDING' THEN
      RAISE EXCEPTION 'stay_events must be inserted as PENDING (no terminal event inserted directly)';
    END IF;
    IF NEW.stay_id IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events cannot be inserted with a pre-resolved stay_id';
    END IF;
    IF NEW.processed_at IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.processed_at must be NULL on insert';
    END IF;
    IF NEW.review_code IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.review_code must be NULL on insert';
    END IF;
    RETURN NEW;
  END IF;

  -- UPDATE: immutable identity / normalization / admission columns
  IF   NEW.id                    IS DISTINCT FROM OLD.id
    OR NEW.tenant_id             IS DISTINCT FROM OLD.tenant_id
    OR NEW.site_id               IS DISTINCT FROM OLD.site_id
    OR NEW.pms_interface_id      IS DISTINCT FROM OLD.pms_interface_id
    OR NEW.external_event_identity IS DISTINCT FROM OLD.external_event_identity
    OR NEW.event_type            IS DISTINCT FROM OLD.event_type
    OR NEW.pms_timestamp_raw     IS DISTINCT FROM OLD.pms_timestamp_raw
    OR NEW.pms_timestamp_utc     IS DISTINCT FROM OLD.pms_timestamp_utc
    OR NEW.source_timezone       IS DISTINCT FROM OLD.source_timezone
    OR NEW.received_at           IS DISTINCT FROM OLD.received_at
    OR NEW.sequence_version      IS DISTINCT FROM OLD.sequence_version
    OR NEW.normalization_version IS DISTINCT FROM OLD.normalization_version
    OR NEW.clock_suspect         IS DISTINCT FROM OLD.clock_suspect
    OR NEW.payload               IS DISTINCT FROM OLD.payload
    OR NEW.admission_kind              IS DISTINCT FROM OLD.admission_kind
    OR NEW.admission_runtime_generation IS DISTINCT FROM OLD.admission_runtime_generation
    OR NEW.resync_generation           IS DISTINCT FROM OLD.resync_generation
    OR NEW.fingerprint_key_version     IS DISTINCT FROM OLD.fingerprint_key_version
  THEN
    RAISE EXCEPTION 'stay_events identity/normalization/admission columns are immutable (append-only)';
  END IF;

  -- Once terminal, the only permitted update is a no-op (no result/lineage field may change).
  IF OLD.processing_status <> 'PENDING' THEN
    IF NEW.processing_status IS DISTINCT FROM OLD.processing_status
       OR NEW.stay_id      IS DISTINCT FROM OLD.stay_id
       OR NEW.processed_at IS DISTINCT FROM OLD.processed_at
       OR NEW.review_code  IS DISTINCT FROM OLD.review_code THEN
      RAISE EXCEPTION 'terminal stay_events row is immutable (status/stay_id/processed_at/review_code frozen)';
    END IF;
    RETURN NEW;
  END IF;

  -- OLD is PENDING and staying PENDING: no result/lineage field may be set yet.
  IF NEW.processing_status = 'PENDING' THEN
    IF NEW.stay_id IS NOT NULL OR NEW.processed_at IS NOT NULL OR NEW.review_code IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events result fields (stay_id/processed_at/review_code) may only be set on PENDING->terminal';
    END IF;
    RETURN NEW;
  END IF;

  -- PENDING -> terminal (one move). A RESYNC row's PUBLICATION is enforced by the consumer against the
  -- interface's published_resync_generation boundary; the row itself stays immutable append-first evidence.
  IF NEW.processing_status NOT IN ('APPLIED','SKIPPED_DUPLICATE','MANUAL_REVIEW','FAILED') THEN
    RAISE EXCEPTION 'invalid stay_events terminal processing_status %', NEW.processing_status;
  END IF;
  IF NEW.processed_at IS NULL THEN
    RAISE EXCEPTION 'stay_events.processed_at is required on PENDING->%', NEW.processing_status;
  END IF;
  -- stay_id lineage: NULL -> a same-interface Stay only
  IF NEW.stay_id IS NOT NULL THEN
    IF OLD.stay_id IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.stay_id may only go from NULL to a resolved Stay';
    END IF;
    SELECT count(*) INTO ok_stay FROM iam_v2.stays s
      WHERE s.id = NEW.stay_id AND s.tenant_id = NEW.tenant_id
        AND s.site_id = NEW.site_id AND s.pms_interface_id = NEW.pms_interface_id;
    IF ok_stay <> 1 THEN
      RAISE EXCEPTION 'stay_events.stay_id must reference a Stay in the same tenant/site/pms_interface';
    END IF;
  END IF;
  -- review_code vocabulary: bounded machine code only (no PII / payload / stack traces)
  IF NEW.review_code IS NOT NULL AND NEW.review_code !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'stay_events.review_code must match ^[A-Z][A-Z0-9_]{0,63}$ (bounded machine code)';
  END IF;
  -- result-specific invariants
  IF NEW.processing_status = 'APPLIED' THEN
    IF NEW.stay_id IS NULL THEN RAISE EXCEPTION 'APPLIED requires a resolved same-interface stay_id'; END IF;
    IF NEW.review_code IS NOT NULL THEN RAISE EXCEPTION 'APPLIED must not carry a review_code'; END IF;
  ELSIF NEW.processing_status = 'MANUAL_REVIEW' THEN
    IF NEW.review_code IS NULL THEN RAISE EXCEPTION 'MANUAL_REVIEW requires a bounded review_code'; END IF;
  ELSIF NEW.processing_status = 'FAILED' THEN
    IF NEW.review_code IS NULL THEN RAISE EXCEPTION 'FAILED requires a bounded review_code'; END IF;
  END IF;
  -- SKIPPED_DUPLICATE: processed_at required (checked); stay_id/review_code optional (validated above).
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_stay_event_guard
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.stay_events
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_stay_event_appendonly();

-- ============================================================================
-- (2) stays one-way status guard; lifecycle_version = STRICT episode counter.
--     lifecycle_version changes ONLY on a CHECKED_OUT->IN_HOUSE reinstatement (+1). Every other
--     lifecycle_version change (same-status ++, checkout ++, room-move ++, decrease, jump) is rejected.
--     POST_STAY_ACTIVE has NO executable transition (Phase 5); the enum is retained for forward compat.
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p3_stay_lifecycle_guard() RETURNS trigger
  LANGUAGE plpgsql
  SET search_path = iam_v2, pg_temp
  AS $fn$
DECLARE allowed boolean; is_reinstate boolean; evidence_changed boolean;
BEGIN
  IF NEW.last_applied_event_version < OLD.last_applied_event_version THEN
    RAISE EXCEPTION 'stays.last_applied_event_version cannot decrease (% -> %)',
      OLD.last_applied_event_version, NEW.last_applied_event_version;
  END IF;

  -- occupancy-evidence version transition guard (MONOTONIC + exactly-once). The "material" evidence is the
  -- observed content: evidence_at / revision_id / normalization_version / clock_suspect. occupancy_ingested_at
  -- is processing metadata and is DELIBERATELY excluded, so a duplicate reapplication of identical evidence may
  -- refresh ingested_at WITHOUT bumping the version (no uncontrolled increment). Deterministic semantics:
  --   * version never decreases;
  --   * a material evidence change (incl. a Revision change or occupancy-state change) increments by EXACTLY 1;
  --   * an unchanged material tuple must leave the version UNCHANGED (an arbitrary jump or a bump-without-change
  --     is rejected);
  --   * coherence (present<=>version>0, absent<=>version=0) is the structural stays_evidence_version_coherent
  --     CHECK, so evidence once observed can never silently revert to "never observed" (that would require a
  --     decrease and is rejected here).
  -- No caller can mutate the evidence fields without applying the required version transition.
  IF NEW.occupancy_evidence_version < OLD.occupancy_evidence_version THEN
    RAISE EXCEPTION 'stays.occupancy_evidence_version cannot decrease (% -> %)',
      OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
  END IF;
  evidence_changed := (
       NEW.occupancy_evidence_at          IS DISTINCT FROM OLD.occupancy_evidence_at
    OR NEW.occupancy_revision_id          IS DISTINCT FROM OLD.occupancy_revision_id
    OR NEW.occupancy_normalization_version IS DISTINCT FROM OLD.occupancy_normalization_version
    OR NEW.occupancy_clock_suspect        IS DISTINCT FROM OLD.occupancy_clock_suspect);
  IF evidence_changed THEN
    IF NEW.occupancy_evidence_version <> OLD.occupancy_evidence_version + 1 THEN
      RAISE EXCEPTION 'a material occupancy-evidence change must increment occupancy_evidence_version by exactly 1 (% -> %)',
        OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
    END IF;
  ELSE
    IF NEW.occupancy_evidence_version <> OLD.occupancy_evidence_version THEN
      RAISE EXCEPTION 'stays.occupancy_evidence_version may not change without a material occupancy-evidence change (% -> %)',
        OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
    END IF;
  END IF;

  is_reinstate := (OLD.status = 'CHECKED_OUT' AND NEW.status = 'IN_HOUSE');

  -- (item 10) effective_checkout_at is the episode's IMMUTABLE boundary: it may be SET only on the
  -- IN_HOUSE->CHECKED_OUT checkout, and CLEARED only on the controlled CHECKED_OUT->IN_HOUSE reinstatement
  -- (which also increments lifecycle_version, starting a fresh episode). It can never move within an episode.
  IF NEW.effective_checkout_at IS DISTINCT FROM OLD.effective_checkout_at THEN
    IF is_reinstate THEN
      IF NEW.effective_checkout_at IS NOT NULL THEN
        RAISE EXCEPTION 'reinstatement must CLEAR effective_checkout_at (starts a new episode)';
      END IF;
    ELSIF OLD.status = 'IN_HOUSE' AND NEW.status = 'CHECKED_OUT' THEN
      IF NEW.effective_checkout_at IS NULL THEN
        RAISE EXCEPTION 'checkout must SET effective_checkout_at';
      END IF;
    ELSE
      RAISE EXCEPTION 'effective_checkout_at is immutable within an episode (% -> %, status % -> %)',
        OLD.effective_checkout_at, NEW.effective_checkout_at, OLD.status, NEW.status;
    END IF;
  END IF;

  IF NEW.lifecycle_version <> OLD.lifecycle_version THEN
    IF NOT (NEW.lifecycle_version = OLD.lifecycle_version + 1 AND is_reinstate) THEN
      RAISE EXCEPTION 'stays.lifecycle_version may increment by exactly 1 ONLY during a CHECKED_OUT->IN_HOUSE reinstatement (% -> %, % -> %)',
        OLD.lifecycle_version, NEW.lifecycle_version, OLD.status, NEW.status;
    END IF;
  END IF;

  IF NEW.status <> OLD.status THEN
    allowed := CASE
      WHEN OLD.status='RESERVED'    AND NEW.status IN ('IN_HOUSE','CANCELLED','NO_SHOW') THEN true
      WHEN OLD.status='IN_HOUSE'    AND NEW.status IN ('CHECKED_OUT')                    THEN true
      WHEN OLD.status='CHECKED_OUT' AND NEW.status IN ('IN_HOUSE')                       THEN true  -- reinstatement
      ELSE false END;
    IF NOT allowed THEN
      RAISE EXCEPTION 'illegal stays.status transition % -> % (POST_STAY_ACTIVE transitions are Phase 5)', OLD.status, NEW.status;
    END IF;
    IF is_reinstate AND NEW.lifecycle_version <> OLD.lifecycle_version + 1 THEN
      RAISE EXCEPTION 'reinstatement (CHECKED_OUT->IN_HOUSE) must increment lifecycle_version exactly once';
    END IF;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_stay_lifecycle_guard
  BEFORE UPDATE ON iam_v2.stays
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_stay_lifecycle_guard();

-- Privilege hardening: the trigger functions are SECURITY INVOKER (run as the caller, not the owner);
-- revoke the implicit PUBLIC EXECUTE so they cannot be invoked directly outside their trigger context.
-- Runtime service roles receive NO privileges on the new objects (dark; Gate-P least privilege preserved).
REVOKE EXECUTE ON FUNCTION iam_v2.p3_stay_event_appendonly() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_stay_lifecycle_guard() FROM PUBLIC;

-- migration ledger (prod parity with 0009; the authoritative runner scripts/edge-migrate.sh gates on this)
INSERT INTO public.schema_migrations (version) VALUES ('0010_phase3_stay_resolution') ON CONFLICT DO NOTHING;

COMMIT;
