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
  --
  -- NOT VALID, deliberately. effective_checkout_at is introduced BY this migration, so any Stay that departed
  -- before it ran — or before a rollback dropped the column — has no boundary to show. Validating retroactively
  -- would make the migration fail on exactly the databases that have been running longest, and would make the
  -- rollback drill unrepeatable. NOT VALID enforces the rule on every INSERT and UPDATE from now on, which is
  -- what the invariant is actually about; the historical rows are not rewritten to look like they complied.
  ADD CONSTRAINT stays_checkedout_needs_boundary
    CHECK (status <> 'CHECKED_OUT' OR effective_checkout_at IS NOT NULL) NOT VALID,
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
-- (the resolvable-alert VIEW is created further down, once its lifecycle table exists)

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
  -- BITEMPORAL. effective_at is the TRUE business time the state change took effect and is stored EXACTLY as
  -- supplied - it is never clamped, shifted or otherwise rewritten. recorded_at is the SYSTEM time the fact was
  -- learned. They are different domains: a late-discovered change has an OLD effective_at and a NEW recorded_at.
  -- Monotonicity applies to recorded_at (knowledge only ever grows), never to effective_at.
  effective_at timestamptz NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  -- SUPERSESSION is the ONLY way a recorded fact is corrected: nothing is mutated or deleted, a new transition is
  -- appended that supersedes the previous one, and the superseded row is marked (its single permitted mutation).
  supersedes uuid,
  superseded_by uuid,
  reason text CHECK (reason IS NULL OR reason ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (entitlement_id, seq),
  CONSTRAINT est_no_self_supersede CHECK (supersedes IS NULL OR supersedes <> id),
  CONSTRAINT est_not_self_superseded CHECK (superseded_by IS NULL OR superseded_by <> id),
  -- a transition is corrected AT MOST ONCE by exactly one successor (no forked correction chains)
  UNIQUE (supersedes),
  FOREIGN KEY (supersedes) REFERENCES iam_v2.entitlement_state_transitions (id) DEFERRABLE INITIALLY DEFERRED,
  -- DEFERRABLE: a correction MARKS the facts it invalidates before it is itself inserted, so the chain guard
  -- evaluates the new row against the history that REMAINS live. The reference is still proven at COMMIT.
  FOREIGN KEY (superseded_by) REFERENCES iam_v2.entitlement_state_transitions (id) DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id) ON DELETE CASCADE);
-- boundary lookups read the LIVE (non-superseded) history only
CREATE INDEX est_boundary_lookup ON iam_v2.entitlement_state_transitions (entitlement_id, effective_at)
  WHERE superseded_by IS NULL;

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
  -- entitlement_state_transitions: the ONLY permitted mutation is marking a row superseded (NULL -> the id of the
  -- correcting transition), exactly once. Every other column, including effective_at, stays immutable forever.
  IF TG_TABLE_NAME = 'entitlement_state_transitions' THEN
    IF OLD.superseded_by IS NOT NULL THEN
      RAISE EXCEPTION 'entitlement_state_transitions row % is already superseded', OLD.id;
    END IF;
    IF NEW.superseded_by IS NULL THEN
      RAISE EXCEPTION 'entitlement_state_transitions UPDATE must record a supersession';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.entitlement_id IS DISTINCT FROM OLD.entitlement_id
       OR NEW.seq IS DISTINCT FROM OLD.seq OR NEW.from_state IS DISTINCT FROM OLD.from_state
       OR NEW.to_state IS DISTINCT FROM OLD.to_state OR NEW.effective_at IS DISTINCT FROM OLD.effective_at
       OR NEW.recorded_at IS DISTINCT FROM OLD.recorded_at OR NEW.supersedes IS DISTINCT FROM OLD.supersedes
       OR NEW.reason IS DISTINCT FROM OLD.reason THEN
      RAISE EXCEPTION 'entitlement_state_transitions is immutable except for the supersession mark';
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
        max_seq bigint; max_rec timestamptz; tgt_ent uuid; tgt_by uuid;
BEGIN
  -- seq/recorded_at monotonicity is measured over the WHOLE table (knowledge only grows); the STATE CHAIN is
  -- measured over the LIVE (non-superseded) history, because an invalidated fact is no longer part of the chain.
  SELECT COALESCE(max(seq),0), max(recorded_at) INTO max_seq, max_rec
    FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = NEW.entitlement_id;
  IF max_seq > 0 AND NEW.seq <> max_seq + 1 THEN
    RAISE EXCEPTION 'entitlement transition seq must be contiguous (% -> %)', max_seq, NEW.seq;
  END IF;
  IF max_seq = 0 AND NEW.seq <> 1 THEN
    RAISE EXCEPTION 'first entitlement transition must have seq=1 (got %)', NEW.seq;
  END IF;
  -- recorded_at (SYSTEM time) is the axis that can never move backwards. effective_at (BUSINESS time) is stored
  -- verbatim and may legitimately be earlier than an already-recorded fact, but only as an explicit correction
  -- that first INVALIDATES (supersedes) the facts it replaces.
  IF max_rec IS NOT NULL AND NEW.recorded_at < max_rec THEN
    RAISE EXCEPTION 'transition recorded_at cannot move backwards (% < %)', NEW.recorded_at, max_rec;
  END IF;
  IF NEW.supersedes IS NOT NULL THEN
    SELECT entitlement_id, superseded_by INTO tgt_ent, tgt_by
      FROM iam_v2.entitlement_state_transitions WHERE id = NEW.supersedes;
    IF tgt_ent IS NULL OR tgt_ent <> NEW.entitlement_id THEN
      RAISE EXCEPTION 'superseded transition % does not belong to entitlement %', NEW.supersedes, NEW.entitlement_id;
    END IF;
    -- the correction must ALREADY own the fact it claims to correct: the invalidated rows are marked with THIS
    -- row's id before it is inserted, so a caller cannot append a row that merely points at someone else's fact.
    IF tgt_by IS DISTINCT FROM NEW.id THEN
      RAISE EXCEPTION 'superseded transition % is not marked as corrected by this transition', NEW.supersedes;
    END IF;
  END IF;
  -- chain continuity is evaluated against what REMAINS live (post-invalidation)
  SELECT seq, to_state, effective_at INTO prev_seq, prev_to, prev_at
    FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = NEW.entitlement_id AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  IF prev_seq IS NULL THEN
    IF NEW.from_state IS NOT NULL THEN RAISE EXCEPTION 'transition with no live predecessor must have from_state NULL'; END IF;
  ELSE
    IF NEW.from_state IS DISTINCT FROM prev_to THEN RAISE EXCEPTION 'from_state % must equal previous to_state %', NEW.from_state, prev_to; END IF;
    -- an append may not be back-dated behind the live chain: silently accepting an earlier effective_at would
    -- rewrite the state-at-boundary answer with no record. Corrections invalidate what they replace, first.
    IF NEW.effective_at < prev_at THEN
      RAISE EXCEPTION 'transition effective_at % precedes the live head % - record a correction (supersede_entitlement_transition / terminate_entitlement_at_boundary)', NEW.effective_at, prev_at;
    END IF;
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
    WHERE entitlement_id = NEW.id AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
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
  SELECT COALESCE(max(seq),0)+1 INTO v_seq
    FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = p_ent;
  SELECT effective_at INTO v_prev_at FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = p_ent AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  -- TRUE effective time: the requested business time is recorded EXACTLY as given and is NEVER clamped to the
  -- previous transition - clamping would silently rewrite when the change actually took effect. An ordinary
  -- append that precedes the live head is a CORRECTION, and corrections must be explicit (fail closed here and
  -- go through supersede_entitlement_transition, which records the correction instead of hiding it).
  v_at := p_at;
  IF v_prev_at IS NOT NULL AND v_at < v_prev_at THEN
    RAISE EXCEPTION 'requested effective_at % precedes the live head % - use supersede_entitlement_transition to record a correction', v_at, v_prev_at;
  END IF;
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
  INSERT INTO iam_v2.entitlement_state_transitions(tenant_id,site_id,entitlement_id,seq,from_state,to_state,effective_at,recorded_at,reason)
    SELECT tenant_id, site_id, id, v_seq, CASE WHEN v_seq=1 THEN NULL ELSE v_from END, p_to, v_at, now(), p_reason
    FROM iam_v2.entitlements WHERE id = p_ent;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text) FROM PUBLIC;

-- THE controlled CORRECTION operation. A fact that was recorded wrongly (wrong state, or a business time later
-- discovered to be different) is NEVER edited and NEVER deleted: a new transition is APPENDED that supersedes the
-- previous live head, carrying the TRUE effective_at verbatim - which may legitimately precede the superseded
-- row's effective_at, because business time and system time are different domains. The superseded row is then
-- marked (its one permitted mutation), so the correction and the thing it corrected both remain readable forever.
-- entitlements.activated_at / terminated_at are re-derived from the LIVE chain, never left stale.
CREATE OR REPLACE FUNCTION iam_v2.supersede_entitlement_transition(p_target uuid, p_to text, p_at timestamptz, p_reason text) RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_ent uuid; v_seq bigint; v_new uuid; v_term text; v_head uuid; v_from text;
BEGIN
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'transition reason must be a bounded machine code';
  END IF;
  IF p_to NOT IN ('PENDING','ACTIVE','SUSPENDED','TERMINATED') THEN
    RAISE EXCEPTION 'invalid target state %', p_to;
  END IF;
  SELECT entitlement_id INTO v_ent
    FROM iam_v2.entitlement_state_transitions WHERE id = p_target AND superseded_by IS NULL;
  IF v_ent IS NULL THEN RAISE EXCEPTION 'transition % not found or already superseded', p_target; END IF;
  -- L3 Entitlement lock before any write (global lock order)
  PERFORM 1 FROM iam_v2.entitlements WHERE id = v_ent FOR UPDATE;
  SELECT id INTO v_head FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = v_ent AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  IF v_head IS DISTINCT FROM p_target THEN
    RAISE EXCEPTION 'only the live head transition may be superseded (head is %)', v_head;
  END IF;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = v_ent;
  v_new := gen_random_uuid();
  -- INVALIDATE first, then append: the chain guard must judge the correction against the history that remains.
  UPDATE iam_v2.entitlement_state_transitions SET superseded_by = v_new WHERE id = p_target;
  SELECT to_state INTO v_from FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = v_ent AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  v_term := CASE WHEN p_to='TERMINATED' THEN
    (CASE WHEN p_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN','REVOKED','SUPERSEDED','CONVERTED','TRANSFERRED','CANCELLED','OTHER')
          THEN p_reason ELSE 'OTHER' END) ELSE NULL END;
  -- status first, so the INSERT guard's history<->row coherence check sees the corrected state
  UPDATE iam_v2.entitlements SET status = p_to, terminal_reason = v_term WHERE id = v_ent;
  INSERT INTO iam_v2.entitlement_state_transitions
    (id,tenant_id,site_id,entitlement_id,seq,from_state,to_state,effective_at,recorded_at,supersedes,reason)
    SELECT v_new, tenant_id, site_id, id, v_seq, v_from, p_to, p_at, now(), p_target, p_reason
    FROM iam_v2.entitlements WHERE id = v_ent;
  PERFORM iam_v2.p3_rederive_entitlement_times(v_ent);
  RETURN v_new;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.supersede_entitlement_transition(uuid,text,timestamptz,text) FROM PUBLIC;

-- entitlements.activated_at / terminated_at are DERIVED from the live chain; after any correction they are
-- re-derived rather than left carrying a value that the live history no longer supports.
CREATE OR REPLACE FUNCTION iam_v2.p3_rederive_entitlement_times(p_ent uuid) RETURNS void
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  UPDATE iam_v2.entitlements e SET
    activated_at = (SELECT min(t.effective_at) FROM iam_v2.entitlement_state_transitions t
                    WHERE t.entitlement_id = e.id AND t.superseded_by IS NULL AND t.to_state='ACTIVE'),
    terminated_at = (SELECT t.effective_at FROM iam_v2.entitlement_state_transitions t
                     WHERE t.entitlement_id = e.id AND t.superseded_by IS NULL AND t.to_state='TERMINATED'
                     ORDER BY t.seq DESC LIMIT 1)
  WHERE e.id = p_ent;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_rederive_entitlement_times(uuid) FROM PUBLIC;

-- THE controlled BOUNDARY TERMINATION. A Checkout boundary is a real past business time, and TERMINATED is
-- terminal: any lifecycle fact that is effective AFTER the boundary cannot survive it (a suspension or
-- reactivation recorded for a period the guest had already checked out of is void). Rather than silently
-- clamping the termination forward to the last known fact - which would rewrite WHEN access actually ended -
-- this operation records the termination at the TRUE boundary and explicitly INVALIDATES (supersedes) every
-- live post-boundary fact, all of which remain readable. Idempotent for an already-TERMINATED entitlement.
CREATE OR REPLACE FUNCTION iam_v2.terminate_entitlement_at_boundary(p_ent uuid, p_at timestamptz, p_reason text) RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_status text; v_seq bigint; v_new uuid; v_term text; v_from text; v_head uuid; v_marked int;
BEGIN
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'transition reason must be a bounded machine code';
  END IF;
  SELECT status INTO v_status FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;
  IF v_status IS NULL THEN RAISE EXCEPTION 'entitlement % not found', p_ent; END IF;
  IF v_status = 'TERMINATED' THEN RETURN NULL; END IF;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = p_ent;
  v_new := gen_random_uuid();
  SELECT id INTO v_head FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = p_ent AND superseded_by IS NULL AND effective_at > p_at ORDER BY seq DESC LIMIT 1;
  UPDATE iam_v2.entitlement_state_transitions SET superseded_by = v_new
    WHERE entitlement_id = p_ent AND superseded_by IS NULL AND effective_at > p_at;
  GET DIAGNOSTICS v_marked = ROW_COUNT;
  SELECT to_state INTO v_from FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = p_ent AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  v_term := CASE WHEN p_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN','REVOKED','SUPERSEDED','CONVERTED','TRANSFERRED','CANCELLED','OTHER')
                 THEN p_reason ELSE 'OTHER' END;
  UPDATE iam_v2.entitlements SET status = 'TERMINATED', terminal_reason = v_term WHERE id = p_ent;
  INSERT INTO iam_v2.entitlement_state_transitions
    (id,tenant_id,site_id,entitlement_id,seq,from_state,to_state,effective_at,recorded_at,supersedes,reason)
    SELECT v_new, tenant_id, site_id, id, v_seq, v_from, 'TERMINATED', p_at, now(),
           CASE WHEN v_marked > 0 THEN v_head ELSE NULL END, p_reason
    FROM iam_v2.entitlements WHERE id = p_ent;
  PERFORM iam_v2.p3_rederive_entitlement_times(p_ent);
  RETURN v_new;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.terminate_entitlement_at_boundary(uuid,timestamptz,text) FROM PUBLIC;

-- ============================================================================
-- CONTROLLED DEVICE AUTHORIZATION / DEAUTHORIZATION. entitlement_devices is the CURRENT view and
-- entitlement_device_authorizations is the append-only INTERVAL history that a past-boundary question is
-- answered from. Keeping the two in step is not something each caller should re-implement: these two
-- operations are the ONLY approved way to open and close an authorization interval, they take the L3
-- Entitlement lock first (global lock order), they enforce the Entitlement's own device limit atomically
-- against concurrent authorizations, and they are IDEMPOTENT (re-authorizing an already-open device does not
-- open a second interval; deauthorizing an already-closed one is a no-op).
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.authorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamptz) RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_t uuid; v_s uuid; v_status text; v_limit int; v_policy text; v_open int; v_seq bigint; v_id uuid; v_existing uuid;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('device_auth');
  SELECT tenant_id, site_id, status INTO v_t, v_s, v_status FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;
  IF v_t IS NULL THEN RAISE EXCEPTION 'entitlement % not found', p_ent; END IF;
  IF v_status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'device authorization requires an ACTIVE entitlement (status %)', v_status;
  END IF;
  -- the device must exist in the SAME tenant/site scope (never another tenant's device)
  PERFORM 1 FROM iam_v2.devices WHERE id = p_device AND tenant_id = v_t AND site_id = v_s;
  IF NOT FOUND THEN RAISE EXCEPTION 'device % is not in the entitlement scope', p_device; END IF;
  -- IDEMPOTENT: an already-open interval for this device is returned unchanged
  SELECT id INTO v_existing FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND device_id = p_device AND deauthorized_at IS NULL;
  IF v_existing IS NOT NULL THEN
    UPDATE iam_v2.entitlement_devices SET last_authorized = p_at
      WHERE entitlement_id = p_ent AND device_id = p_device;
    RETURN v_existing;
  END IF;
  -- device limit is enforced HERE, under the entitlement row lock, so concurrent authorizations cannot both win
  SELECT spr.max_concurrent_devices, spr.device_limit_policy INTO v_limit, v_policy
    FROM iam_v2.entitlements e JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    WHERE e.id = p_ent;
  SELECT count(*) INTO v_open FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND deauthorized_at IS NULL;
  IF v_limit IS NOT NULL AND v_open >= v_limit THEN
    IF COALESCE(v_policy,'REJECT_NEW_DEVICE') <> 'REJECT_NEW_DEVICE' THEN
      RAISE EXCEPTION 'device limit policy % is not implemented in this phase (fail closed)', v_policy;
    END IF;
    RAISE EXCEPTION 'MAX_DEVICES_REACHED: entitlement % already has % of % devices authorized', p_ent, v_open, v_limit;
  END IF;
  INSERT INTO iam_v2.entitlement_devices(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
    VALUES (v_t,v_s,p_ent,p_device,'AUTHORIZED',p_at,p_at)
    ON CONFLICT (entitlement_id,device_id) DO UPDATE SET status='AUTHORIZED', last_authorized=p_at,
      first_authorized = COALESCE(iam_v2.entitlement_devices.first_authorized, p_at), disconnected_reason=NULL;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND device_id = p_device;
  INSERT INTO iam_v2.entitlement_device_authorizations(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
    VALUES (v_t,v_s,p_ent,p_device,v_seq,p_at) RETURNING id INTO v_id;
  RETURN v_id;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.authorize_entitlement_device(uuid,uuid,timestamptz) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.deauthorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamptz, p_reason text) RETURNS boolean
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_open uuid; v_start timestamptz;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('device_auth');
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'deauthorization reason must be a bounded machine code';
  END IF;
  PERFORM 1 FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;    -- L3 lock first
  SELECT id, authorized_at INTO v_open, v_start FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND device_id = p_device AND deauthorized_at IS NULL;
  IF v_open IS NULL THEN RETURN false; END IF;                        -- idempotent
  -- an interval may not close before it opened (that would invent negative authorized time)
  IF p_at < v_start THEN
    RAISE EXCEPTION 'deauthorization % precedes the interval start %', p_at, v_start;
  END IF;
  UPDATE iam_v2.entitlement_device_authorizations SET deauthorized_at = p_at WHERE id = v_open;
  UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED', disconnected_reason = p_reason
    WHERE entitlement_id = p_ent AND device_id = p_device;
  RETURN true;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.deauthorize_entitlement_device(uuid,uuid,timestamptz,text) FROM PUBLIC;

-- ============================================================================
-- (4m) SESSION -> ENTITLEMENT BINDING INTERVALS + BOUNDARY USAGE WATERMARKS.
--
-- A Checkout REBINDS a grandfathered session from the original Entitlement to the grace Entitlement without a
-- logout. sessions.entitlement_id therefore records only the CURRENT binding, and accounting samples that were
-- taken BEFORE the boundary would silently follow the session to the grace Entitlement — inflating grace usage
-- and erasing the usage the boundary decision was actually made against. These append-only intervals keep the
-- binding history, so any sample is attributed to whichever Entitlement the session was bound to WHEN IT WAS
-- SAMPLED, not to wherever the session points now.
--
-- The WATERMARK freezes the accounting position a boundary decision was made against. Accounting is delayed by
-- nature (a sample taken before checkout can be ingested long after it), and a late sample must never silently
-- rewrite a decision that has already been made and audited: it is recorded as DELAYED, visible to operators,
-- while the frozen watermark keeps the decision reproducible.
-- ============================================================================
CREATE TABLE iam_v2.session_entitlement_bindings (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  session_id uuid NOT NULL, entitlement_id uuid NOT NULL,
  seq bigint NOT NULL CHECK (seq >= 1),
  bound_from timestamptz NOT NULL,
  bound_until timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (session_id, seq),
  CONSTRAINT seb_interval_ordered CHECK (bound_until IS NULL OR bound_until >= bound_from),
  FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id));
CREATE UNIQUE INDEX seb_one_open ON iam_v2.session_entitlement_bindings (session_id) WHERE bound_until IS NULL;
CREATE INDEX seb_attribution ON iam_v2.session_entitlement_bindings (entitlement_id, bound_from);

-- append-only: the ONLY permitted mutation is closing the open interval once (same rule as device intervals).
CREATE OR REPLACE FUNCTION iam_v2.p3_seb_appendonly() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'session_entitlement_bindings: append-only (DELETE rejected)'; END IF;
  IF OLD.bound_until IS NOT NULL THEN RAISE EXCEPTION 'session binding interval is immutable once closed'; END IF;
  IF NEW.bound_until IS NULL THEN RAISE EXCEPTION 'session binding UPDATE must close the interval'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.session_id IS DISTINCT FROM OLD.session_id
     OR NEW.entitlement_id IS DISTINCT FROM OLD.entitlement_id OR NEW.seq IS DISTINCT FROM OLD.seq
     OR NEW.bound_from IS DISTINCT FROM OLD.bound_from THEN
    RAISE EXCEPTION 'session binding identity/interval-start immutable';
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_seb_appendonly BEFORE UPDATE OR DELETE ON iam_v2.session_entitlement_bindings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_seb_appendonly();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_seb_appendonly() FROM PUBLIC;

-- contiguous seq per session; a new interval cannot begin before the previous one closed (no overlap, so a
-- sample can never be attributed to two Entitlements at once).
CREATE OR REPLACE FUNCTION iam_v2.p3_seb_insert_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE prev_seq bigint; prev_until timestamptz; prev_open boolean;
BEGIN
  SELECT seq, bound_until, bound_until IS NULL INTO prev_seq, prev_until, prev_open
    FROM iam_v2.session_entitlement_bindings WHERE session_id = NEW.session_id ORDER BY seq DESC LIMIT 1;
  IF prev_seq IS NULL THEN
    IF NEW.seq <> 1 THEN RAISE EXCEPTION 'first session binding must have seq=1 (got %)', NEW.seq; END IF;
  ELSE
    IF NEW.seq <> prev_seq + 1 THEN RAISE EXCEPTION 'session binding seq must be contiguous (% -> %)', prev_seq, NEW.seq; END IF;
    IF prev_open THEN RAISE EXCEPTION 'session % already has an OPEN binding interval', NEW.session_id; END IF;
    IF NEW.bound_from < prev_until THEN RAISE EXCEPTION 'session binding % cannot begin before the previous closed at %', NEW.bound_from, prev_until; END IF;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_seb_insert BEFORE INSERT ON iam_v2.session_entitlement_bindings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_seb_insert_guard();

-- THE controlled rebinding operation: closes the open interval at p_at and opens the next one, so the history
-- stays gapless and the CURRENT sessions row keeps agreeing with the head of the interval history.
CREATE OR REPLACE FUNCTION iam_v2.rebind_session_entitlement(p_session uuid, p_ent uuid, p_at timestamptz) RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_t uuid; v_s uuid; v_open uuid; v_from timestamptz; v_cur uuid; v_seq bigint; v_id uuid;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('session_binding');
  SELECT tenant_id, site_id, entitlement_id INTO v_t, v_s, v_cur FROM iam_v2.sessions WHERE id = p_session FOR UPDATE;
  IF v_t IS NULL THEN RAISE EXCEPTION 'session % not found', p_session; END IF;
  PERFORM 1 FROM iam_v2.entitlements WHERE id = p_ent AND tenant_id = v_t AND site_id = v_s;
  IF NOT FOUND THEN RAISE EXCEPTION 'entitlement % is not in the session scope', p_ent; END IF;
  SELECT id, bound_from INTO v_open, v_from FROM iam_v2.session_entitlement_bindings
    WHERE session_id = p_session AND bound_until IS NULL;
  IF v_open IS NOT NULL THEN
    IF p_at < v_from THEN RAISE EXCEPTION 'rebinding at % precedes the open interval start %', p_at, v_from; END IF;
    UPDATE iam_v2.session_entitlement_bindings SET bound_until = p_at WHERE id = v_open;
  END IF;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.session_entitlement_bindings WHERE session_id = p_session;
  INSERT INTO iam_v2.session_entitlement_bindings(tenant_id,site_id,session_id,entitlement_id,seq,bound_from)
    VALUES (v_t,v_s,p_session,p_ent,v_seq,p_at) RETURNING id INTO v_id;
  UPDATE iam_v2.sessions SET entitlement_id = p_ent WHERE id = p_session;
  RETURN v_id;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.rebind_session_entitlement(uuid,uuid,timestamptz) FROM PUBLIC;

-- Every session gets its binding history from CREATION, without every session-creating path having to know:
-- the initial interval opens at the session's own start against the Entitlement it was created under. This is
-- what makes attribution total — there is no session whose samples have no owner.
CREATE OR REPLACE FUNCTION iam_v2.p3_session_open_binding() RETURNS trigger
  -- SECURITY DEFINER because this trigger IS the controlled writer for the binding it creates: it fires as
  -- whichever role opened the Session, and the binding table is closed to every role but the operation owner.
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('session_binding');
  INSERT INTO iam_v2.session_entitlement_bindings(tenant_id,site_id,session_id,entitlement_id,seq,bound_from)
    VALUES (NEW.tenant_id,NEW.site_id,NEW.id,NEW.entitlement_id,1,NEW.started);
  RETURN NULL;
END $fn$;
CREATE TRIGGER p3_session_open_binding AFTER INSERT ON iam_v2.sessions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_session_open_binding();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_session_open_binding() FROM PUBLIC;

-- When a session ends, its open binding closes at the SAME instant, so a dead session cannot keep accruing
-- attribution. Never before the interval opened (that would invent negative online time).
CREATE OR REPLACE FUNCTION iam_v2.p3_session_close_binding() RETURNS trigger
  -- SECURITY DEFINER for the same reason as p3_session_open_binding: closing a binding is a write to the
  -- guarded binding table, performed on behalf of whichever role ended the Session.
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('session_binding');
  IF NEW.ended IS NOT NULL AND OLD.ended IS NULL THEN
    UPDATE iam_v2.session_entitlement_bindings b SET bound_until = GREATEST(NEW.ended, b.bound_from)
      WHERE b.session_id = NEW.id AND b.bound_until IS NULL;
  END IF;
  RETURN NULL;
END $fn$;
CREATE TRIGGER p3_session_close_binding AFTER UPDATE ON iam_v2.sessions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_session_close_binding();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_session_close_binding() FROM PUBLIC;

-- Usage AT a point in time, attributed by BINDING INTERVAL rather than by the session's current pointer.
-- Sessions with no binding history at all fall back to their current pointer, so pre-existing/simple sessions
-- are still counted (a missing history must never silently zero real usage).
CREATE OR REPLACE FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamptz)
  RETURNS TABLE (bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamptz)
  LANGUAGE sql STABLE SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(sum(ar.bytes_up),0)::bigint, COALESCE(sum(ar.bytes_down),0)::bigint,
         count(*)::bigint, max(ar.sampled_at)
  FROM iam_v2.accounting_records ar
  JOIN iam_v2.sessions s ON s.id = ar.session_id
  WHERE ar.sampled_at <= p_at
    AND (
      EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
              WHERE b.session_id = ar.session_id AND b.entitlement_id = p_ent
                AND b.bound_from <= ar.sampled_at AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at))
      OR (s.entitlement_id = p_ent
          AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = ar.session_id))
    );
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid,timestamptz) FROM PUBLIC;

-- The FROZEN evidence a boundary decision was made against. One row per (entitlement, boundary); append-only.
CREATE TABLE iam_v2.entitlement_boundary_watermarks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  entitlement_id uuid NOT NULL,
  boundary_at timestamptz NOT NULL,
  bytes_up bigint NOT NULL CHECK (bytes_up >= 0),
  bytes_down bigint NOT NULL CHECK (bytes_down >= 0),
  records_counted bigint NOT NULL CHECK (records_counted >= 0),
  latest_sampled_at timestamptz,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (entitlement_id, boundary_at),
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id) ON DELETE CASCADE);
CREATE TRIGGER p3_ebw_appendonly BEFORE UPDATE OR DELETE ON iam_v2.entitlement_boundary_watermarks
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();

-- DELAYED accounting: a sample that belongs to a period ALREADY frozen by a watermark. It is recorded here for
-- operators and reconciliation and NEVER folded back into the frozen decision.
CREATE TABLE iam_v2.delayed_accounting_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  accounting_record_id uuid NOT NULL UNIQUE,
  session_id uuid NOT NULL,
  entitlement_id uuid NOT NULL,
  watermark_id uuid NOT NULL,
  sampled_at timestamptz NOT NULL,
  bytes_up bigint NOT NULL, bytes_down bigint NOT NULL,
  detected_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (watermark_id) REFERENCES iam_v2.entitlement_boundary_watermarks (id) ON DELETE CASCADE);
CREATE TRIGGER p3_dar_appendonly BEFORE UPDATE OR DELETE ON iam_v2.delayed_accounting_records
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();

-- ============================================================================
-- (4o) CONTROLLED PHASE-3 ACCOUNTING INGESTION.
--
-- A physical counter delta is not, by itself, usable evidence. It has to be tied to WHEN it was measured, to
-- WHICH Entitlement was in force at that moment, and it must survive being delivered twice. Doing that in the
-- daemon would put the rules where nothing can enforce them; doing it here means every writer gets the same
-- answers.
--
-- sampled_at is when the counter was READ; ingested_at is when the row arrived. They are separate columns
-- because a sample taken before a Checkout boundary can legitimately arrive long after it, and collapsing the
-- two would make late usage look like it happened after the boundary.
--
-- Returns a bounded classification: ACCEPTED | DELAYED | DUPLICATE. Failures use bounded prefixes:
--   ACCT_SESSION_OUT_OF_SCOPE / ACCT_NO_BINDING / ACCT_COUNTER_REGRESSION / ACCT_INVALID
-- ============================================================================
ALTER TABLE iam_v2.accounting_records
  ADD COLUMN ingested_at timestamptz NOT NULL DEFAULT now();

-- The sample-sequence ingestion function that used to live here has been REMOVED, not deprecated. It took a
-- caller-supplied sample_seq and a caller-computed delta, which is exactly the contract the durable-checkpoint
-- design replaced: a runtime that restarts cannot produce a trustworthy delta, and a caller-chosen sequence is
-- an idempotency key the caller can get wrong. Leaving it in place as an unused SECURITY DEFINER function would
-- have left a second, weaker way to write accounting rows — one that bypasses the checkpoint entirely.
-- See (4p) below for the operation that replaced it.


-- ============================================================================
-- (4p) DURABLE ACCOUNTING CHECKPOINTS + ABSOLUTE-COUNTER INGESTION.
--
-- The earlier design kept the previous counter reading in the daemon's memory and used a wall-clock second as
-- the idempotency key. Both are wrong in ways that only show up in production:
--
--   * memory does not survive a restart, so a process that came back would adopt the CURRENT counter as a
--     fresh baseline and silently lose every byte the guest used while it was down;
--   * a wall-clock key makes "the same observation" mean "the same second", so a retry that lands in the next
--     second double-counts, and a genuine second observation inside one second disappears.
--
-- The checkpoint moves both facts into the database, next to the rows they protect. The runtime submits what
-- it can actually see — the ABSOLUTE counters — and the database decides what that means.
-- ============================================================================
-- The managed class minor a Session's address MUST occupy, computed HERE rather than trusted from the caller.
-- It mirrors internal/shape.MinorForIP exactly: 0x1000 + (low nibble of the third octet << 8 | fourth octet).
-- Deriving it in the database is the point — acctd computing it correctly is not evidence that the value
-- arriving in the operation is the one acctd computed.
CREATE OR REPLACE FUNCTION iam_v2.p3_expected_class_minor(p_ip inet) RETURNS int
  LANGUAGE sql IMMUTABLE SET search_path = iam_v2, pg_temp AS $fn$
  SELECT CASE
    WHEN p_ip IS NULL OR family(p_ip) <> 4 THEN NULL
    ELSE 4096 + (((split_part(host(p_ip), '.', 3)::int & 15) << 8) | split_part(host(p_ip), '.', 4)::int)
  END;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_expected_class_minor(inet) FROM PUBLIC;

CREATE TABLE iam_v2.accounting_checkpoints (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  session_id uuid NOT NULL,
  -- the counter SOURCE: which device, on which bridge, in which class. A class minor is reused when a class is
  -- destroyed and recreated, so the checkpoint is keyed by the whole tuple and can never be inherited by a
  -- different session that happens to land on the same minor.
  source_device_id uuid NOT NULL,
  bridge text NOT NULL,
  class_minor int NOT NULL,
  -- source_epoch is the TC owner's generation for that managed class. A counter that goes backwards is only
  -- trustworthy as a reset when the owner says the class was replaced.
  source_epoch bigint NOT NULL,
  prev_bytes_up bigint NOT NULL CHECK (prev_bytes_up >= 0),
  prev_bytes_down bigint NOT NULL CHECK (prev_bytes_down >= 0),
  prev_sampled_at timestamptz NOT NULL,
  last_record_id uuid,
  last_classification text NOT NULL CHECK (last_classification IN ('BASELINED','ACCEPTED','DELAYED','DUPLICATE','RESET_BASELINED')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (session_id, source_device_id, bridge, class_minor),
  FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions (tenant_id, site_id, id) ON DELETE CASCADE);

-- THE controlled offer-set writer. It exists so the offer record has exactly one author, and so the row can
-- never be back-filled: an offer inserted after the fact would make an unoffered package look offered.
CREATE OR REPLACE FUNCTION iam_v2.record_auth_context_offer(
    p_tenant uuid, p_site uuid, p_auth_context uuid, p_package_revision uuid,
    p_tier int, p_evidence_version bigint, p_expires_at timestamptz) RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_id uuid; v_consumed timestamptz;
BEGIN
  IF p_evidence_version IS NULL OR p_evidence_version <= 0 THEN
    RAISE EXCEPTION 'OFFER_INVALID: an offer must record the evidence version it was decided under';
  END IF;
  SELECT consumed_at INTO v_consumed FROM iam_v2.auth_contexts
    WHERE id = p_auth_context AND tenant_id = p_tenant AND site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'OFFER_INVALID: auth context % is not in this tenant/site', p_auth_context;
  END IF;
  IF v_consumed IS NOT NULL THEN
    -- Offering something to a context that has already been redeemed would let a second grant find an offer
    -- that was never shown to the guest at the time they proved who they were.
    RAISE EXCEPTION 'OFFER_INVALID: auth context % is already consumed', p_auth_context;
  END IF;
  INSERT INTO iam_v2.auth_context_offers
    (tenant_id, site_id, auth_context_id, package_revision_id, matched_tier_order, evidence_version, expires_at)
    VALUES (p_tenant, p_site, p_auth_context, p_package_revision, p_tier, p_evidence_version, p_expires_at)
    ON CONFLICT (auth_context_id, package_revision_id) DO NOTHING
    RETURNING id INTO v_id;
  RETURN v_id;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.record_auth_context_offer(uuid,uuid,uuid,uuid,int,bigint,timestamptz) FROM PUBLIC;

-- ============================================================================
-- (4s) ONE RESOLUTION, ONE LIVE AUTH CONTEXT.
--
-- The resolution itself is idempotent per request id — submit the same one twice and the stored outcome is
-- replayed rather than re-decided. But issuing a CONTEXT from that replay was not: every retry minted another
-- unconsumed, independently redeemable Context. A guest tapping Connect five times on a bad connection would
-- leave five live credentials for one identity proof, any of which could be redeemed later by whoever held it.
--
-- The rule is one LIVE context per resolution request. A retry returns the still-valid one it already has; it
-- does not create a second. A consumed context is never silently replaced — that would let a redeemed proof
-- be re-redeemed — so a new grant needs a new identity proof.
ALTER TABLE iam_v2.auth_contexts ADD COLUMN resolution_request_id uuid;
CREATE UNIQUE INDEX ac_one_live_per_resolution
  ON iam_v2.auth_contexts (tenant_id, site_id, resolution_request_id)
  WHERE resolution_request_id IS NOT NULL AND consumed_at IS NULL;

-- Issue-or-return. Returns the existing live Context for this resolution when there is one, so a retry is
-- idempotent rather than accumulative, and the caller cannot tell (or need to tell) which happened.
CREATE OR REPLACE FUNCTION iam_v2.issue_or_return_pms_context(
    p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_stay uuid,
    p_device uuid, p_guest_network uuid, p_request uuid, p_ttl_seconds int)
  RETURNS TABLE (context_id uuid, reused boolean)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_existing uuid; v_lifecycle int; v_ev bigint;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('auth_context');
  IF p_request IS NULL THEN
    RAISE EXCEPTION 'CONTEXT_INVALID: a PMS context must name the resolution it came from';
  END IF;
  -- An existing LIVE context for this resolution is returned as-is. Note what is deliberately not matched:
  -- a CONSUMED one. Returning that would hand back a credential that has already bought access.
  SELECT id INTO v_existing FROM iam_v2.auth_contexts
    WHERE tenant_id = p_tenant AND site_id = p_site AND resolution_request_id = p_request
      AND consumed_at IS NULL AND expires_at > now()
    FOR UPDATE;
  IF v_existing IS NOT NULL THEN
    RETURN QUERY SELECT v_existing, true;
    RETURN;
  END IF;

  -- Same authoritative Stay snapshot the plain issue path takes: IN_HOUSE, ACTIVE interface, occupancy
  -- evidence present, versioned, not clock-suspect, produced by the SAME revision, and still fresh.
  SELECT st.lifecycle_version, st.occupancy_evidence_version INTO v_lifecycle, v_ev
    FROM iam_v2.stays st
    JOIN iam_v2.pms_interfaces pi
      ON pi.tenant_id=st.tenant_id AND pi.site_id=st.site_id AND pi.id=st.pms_interface_id
    JOIN iam_v2.pms_interface_revisions pr
      ON pr.tenant_id=st.tenant_id AND pr.site_id=st.site_id
     AND pr.pms_interface_id=st.pms_interface_id AND pr.id=p_revision
   WHERE st.tenant_id=p_tenant AND st.site_id=p_site AND st.pms_interface_id=p_interface AND st.id=p_stay
     AND st.status='IN_HOUSE' AND pi.lifecycle_state='ACTIVE'
     AND st.occupancy_evidence_at IS NOT NULL AND st.occupancy_clock_suspect IS NOT TRUE
     AND st.occupancy_evidence_version > 0 AND st.occupancy_revision_id = p_revision
     AND st.occupancy_evidence_at > now() - make_interval(secs =>
           CASE WHEN (pr.config->>'max_auth_cache_age_seconds') ~ '^[1-9][0-9]{0,5}$'
                THEN CASE WHEN (pr.config->>'max_auth_cache_age_seconds')::int <= 604800
                          THEN (pr.config->>'max_auth_cache_age_seconds')::int ELSE 300 END
                ELSE 300 END)
   FOR UPDATE OF st;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'CONTEXT_INVALID: stay % is not eligible for a PMS context', p_stay;
  END IF;

  RETURN QUERY
    INSERT INTO iam_v2.auth_contexts
      (tenant_id, site_id, method, stay_id, pms_interface_id, authentication_interface_revision_id,
       device_id, guest_network_id, pinned_lifecycle_version, pinned_occupancy_evidence_version,
       resolution_request_id, expires_at)
    VALUES (p_tenant, p_site, 'PMS', p_stay, p_interface, p_revision, p_device, p_guest_network,
            v_lifecycle, v_ev, p_request, now() + make_interval(secs => p_ttl_seconds))
    RETURNING id, false;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.issue_or_return_pms_context(uuid,uuid,uuid,uuid,uuid,uuid,uuid,uuid,int) FROM PUBLIC;

-- ============================================================================
-- (4r) RATE PLAN, AND THE OFFER SET A VERIFIED STAY WAS ACTUALLY SHOWN.
--
-- RATE_PLAN is one of the seven PMS eligibility rule types. A property can publish "corporate rate plans get
-- the business package", so the rule needs an authoritative source — without one it would be permanently
-- unanswerable, which fails closed correctly but means the rule type could never be used for anything.
ALTER TABLE iam_v2.stays ADD COLUMN rate_plan text;

-- THE OFFER SET. A grant must be able to answer "was this package offered to THIS verified Stay?", which is a
-- different question from "is this package generally grantable?". Without this record only the second can be
-- asked, and a guest naming any other free package on the site passes it.
--
-- One row per (Auth Context, package revision), written in the same transaction that issues the Context, and
-- carrying the evidence version the decision was made under so it can be re-justified later.
CREATE TABLE iam_v2.auth_context_offers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  auth_context_id uuid NOT NULL,
  package_revision_id uuid NOT NULL,
  matched_tier_order int,
  evidence_version bigint NOT NULL CHECK (evidence_version > 0),
  offered_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  UNIQUE (auth_context_id, package_revision_id),
  FOREIGN KEY (tenant_id, site_id, auth_context_id)
    REFERENCES iam_v2.auth_contexts (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, package_revision_id)
    REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id),
  CONSTRAINT aco_expiry_after_offer CHECK (expires_at > offered_at));
CREATE INDEX aco_by_context ON iam_v2.auth_context_offers (auth_context_id);

-- ============================================================================
-- (4q) THE CLASS-GENERATION ALLOCATOR.
--
-- A managed class's generation is the only trustworthy answer to "did this counter series restart?", so the
-- one thing it must never do is repeat. An earlier attempt seeded it from the wall clock; that is not an
-- allocator. System time moves BACKWARDS on RTC reset, on NTP correction, after offline operation, and after
-- a snapshot or image restore — and a generation that went backwards would let a recreated class present
-- itself as a continuation of the series a checkpoint still remembers, billing one guest's counters as
-- another's delta.
--
-- The allocator is therefore durable, appliance-scoped, and independent of any clock. It is also
-- SELF-RECONCILING: it takes the greatest of its own counter and the highest generation any surviving
-- accounting checkpoint actually pins, so even a lost or rolled-back counter row cannot hand out a value some
-- checkpoint has already seen. That reconciliation is what makes the guarantee survive a restore, not just a
-- restart.
-- ============================================================================
CREATE TABLE iam_v2.appliance_class_generation (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, appliance_id uuid NOT NULL,
  last_generation bigint NOT NULL DEFAULT 0 CHECK (last_generation >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id, appliance_id));

-- Allocates the next generation for this appliance. Strictly increasing, never reissued, no wall clock.
CREATE OR REPLACE FUNCTION iam_v2.allocate_class_generation(
    p_tenant uuid, p_site uuid, p_appliance uuid) RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_next bigint; v_pinned bigint;
BEGIN
  IF p_tenant IS NULL OR p_site IS NULL OR p_appliance IS NULL THEN
    RAISE EXCEPTION 'ACCT_INVALID: a class generation is allocated per tenant/site/appliance';
  END IF;
  INSERT INTO iam_v2.appliance_class_generation (tenant_id, site_id, appliance_id, last_generation)
    VALUES (p_tenant, p_site, p_appliance, 0)
    ON CONFLICT (tenant_id, site_id, appliance_id) DO NOTHING;
  -- Row lock first: two netd instances (or a restart racing itself) must not both read the same value.
  SELECT last_generation INTO v_next FROM iam_v2.appliance_class_generation
    WHERE tenant_id = p_tenant AND site_id = p_site AND appliance_id = p_appliance FOR UPDATE;
  -- RECONCILE against what is actually pinned. If this counter were ever lost, restored from an older
  -- backup, or rolled back, the checkpoints are the surviving evidence of which generations have been used.
  SELECT COALESCE(max(cp.source_epoch), 0) INTO v_pinned FROM iam_v2.accounting_checkpoints cp
    WHERE cp.tenant_id = p_tenant AND cp.site_id = p_site;
  v_next := GREATEST(COALESCE(v_next, 0), v_pinned) + 1;
  UPDATE iam_v2.appliance_class_generation
     SET last_generation = v_next, updated_at = now()
   WHERE tenant_id = p_tenant AND site_id = p_site AND appliance_id = p_appliance;
  RETURN v_next;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.allocate_class_generation(uuid,uuid,uuid) FROM PUBLIC;

-- THE CLASS ORIGIN. Registered by the TC owner at the moment it creates or replaces a managed class, BEFORE
-- the guest can send a packet through it.
--
-- Without this there is a hole between class creation and the first periodic accounting pass: the first
-- observation has nothing to subtract from, so it BASELINES and everything used in that window is discarded.
-- On a tick interval that is seconds of traffic per session, every session, forever — and it looks like
-- nothing at all, because a baseline is a normal, expected outcome.
--
-- The owner states the counters it read IMMEDIATELY after creating the class (zero for a genuinely new class;
-- the exact reading when it adopted an existing one). That reading, not the first tick's, becomes the origin.
-- Nothing here infers zero: a caller that cannot prove the class was newly created passes what it actually
-- read, and the difference from that point on is still exact.
CREATE OR REPLACE FUNCTION iam_v2.register_class_origin(
    p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid,
    p_bridge text, p_class_minor int, p_epoch bigint,
    p_origin_up bigint, p_origin_down bigint, p_created_at timestamptz) RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_started timestamptz; v_device uuid; v_ended timestamptz; v_iface text; v_ip inet; cp record;
BEGIN
  IF p_origin_up IS NULL OR p_origin_down IS NULL OR p_origin_up < 0 OR p_origin_down < 0 THEN
    RAISE EXCEPTION 'ACCT_INVALID: origin counters must be non-negative';
  END IF;
  IF p_epoch IS NULL OR p_epoch < 1 OR p_created_at IS NULL THEN
    RAISE EXCEPTION 'ACCT_INVALID: a class origin needs a source epoch (>= 1) and a creation time';
  END IF;

  -- the SAME source coherence the ingestion operation enforces: an origin is a checkpoint, and a checkpoint
  -- for a source tuple that does not describe this Session would be a way to pre-seed someone else's series
  SELECT started, device_id, ended, ingress_interface, ip
    INTO v_started, v_device, v_ended, v_iface, v_ip
    FROM iam_v2.sessions WHERE id = p_session AND tenant_id = p_tenant AND site_id = p_site;
  IF v_started IS NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % is not in this tenant/site', p_session;
  END IF;
  IF v_ended IS NOT NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % has ended', p_session;
  END IF;
  IF v_device IS DISTINCT FROM p_source_device
     OR v_iface IS DISTINCT FROM p_bridge
     OR iam_v2.p3_expected_class_minor(v_ip) IS DISTINCT FROM p_class_minor THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: the class origin does not describe session %', p_session;
  END IF;

  SELECT * INTO cp FROM iam_v2.accounting_checkpoints
    WHERE session_id = p_session AND source_device_id = p_source_device
      AND bridge = p_bridge AND class_minor = p_class_minor
    FOR UPDATE;

  IF cp.id IS NULL THEN
    INSERT INTO iam_v2.accounting_checkpoints
      (tenant_id, site_id, session_id, source_device_id, bridge, class_minor, source_epoch,
       prev_bytes_up, prev_bytes_down, prev_sampled_at, last_classification)
      VALUES (p_tenant, p_site, p_session, p_source_device, p_bridge, p_class_minor, p_epoch,
              p_origin_up, p_origin_down, p_created_at, 'BASELINED');
    RETURN 'ORIGIN_REGISTERED';
  END IF;

  IF p_epoch < cp.source_epoch THEN
    RAISE EXCEPTION 'ACCT_STALE_EPOCH: origin epoch % is older than the accepted epoch %', p_epoch, cp.source_epoch;
  END IF;
  IF p_epoch = cp.source_epoch THEN
    -- The same class generation is already registered. Re-registering would move the origin forward and
    -- silently forgive whatever was used since — which is exactly the loss this operation exists to close.
    RETURN 'ORIGIN_UNCHANGED';
  END IF;

  -- A NEW generation: the class was replaced, so its counters legitimately restart from the stated origin.
  UPDATE iam_v2.accounting_checkpoints
     SET source_epoch = p_epoch, prev_bytes_up = p_origin_up, prev_bytes_down = p_origin_down,
         prev_sampled_at = p_created_at, last_classification = 'RESET_BASELINED', updated_at = now()
   WHERE id = cp.id;
  RETURN 'ORIGIN_RESET';
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.register_class_origin(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz) FROM PUBLIC;

-- THE controlled absolute-counter ingestion. One transaction decides everything: scope, binding, checkpoint,
-- delta, accounting row, session totals, delayed classification and the new checkpoint.
--
-- Bounded outcomes: BASELINED | ACCEPTED | DELAYED | DUPLICATE | RESET_BASELINED
--                   | REPLAY:ACCEPTED | REPLAY:DELAYED   (an exact replay, reporting what was persisted)
-- Bounded failures: ACCT_SESSION_OUT_OF_SCOPE / ACCT_SOURCE_MISMATCH / ACCT_NO_BINDING /
--                   ACCT_COUNTER_REGRESSION / ACCT_STALE_EPOCH / ACCT_INVALID
CREATE OR REPLACE FUNCTION iam_v2.ingest_absolute_counters(
    p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid,
    p_bridge text, p_class_minor int, p_epoch bigint,
    p_abs_up bigint, p_abs_down bigint, p_sampled_at timestamptz) RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_started timestamptz; v_device uuid; v_ended timestamptz; v_iface text; v_ip inet; v_other int;
  cp record; v_ent uuid; v_up bigint; v_down bigint; v_seq bigint; v_rec uuid; v_class text; v_delayed boolean;
BEGIN
  IF p_abs_up IS NULL OR p_abs_down IS NULL OR p_abs_up < 0 OR p_abs_down < 0 THEN
    RAISE EXCEPTION 'ACCT_INVALID: absolute counters must be non-negative (up=%, down=%)', p_abs_up, p_abs_down;
  END IF;
  IF p_sampled_at IS NULL OR p_bridge IS NULL OR p_bridge = '' OR p_class_minor IS NULL OR p_epoch IS NULL THEN
    RAISE EXCEPTION 'ACCT_INVALID: sampled_at, bridge, class minor and source epoch are all required';
  END IF;

  -- SCOPE AND SOURCE COHERENCE. Every field describing WHERE these counters came from is re-derived here from
  -- the Session's own row and compared. The daemon computing them correctly is not evidence: the operation
  -- has to be able to refuse a caller that computed them wrongly, was fed the wrong session, or is replaying
  -- one session's counters under another's identity.
  SELECT started, device_id, ended, ingress_interface, ip
    INTO v_started, v_device, v_ended, v_iface, v_ip
    FROM iam_v2.sessions
   WHERE id = p_session AND tenant_id = p_tenant AND site_id = p_site;
  IF v_started IS NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % is not in this tenant/site', p_session;
  END IF;
  -- DEVICE. Without this a class minor reused by another guest would quietly bill its traffic to whoever
  -- held the minor before.
  IF v_device IS DISTINCT FROM p_source_device THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % is not bound to device %', p_session, p_source_device;
  END IF;
  -- BRIDGE. The Session records the interface it is actually on. A Session with none cannot be measured at
  -- all: there is no server-pinned answer to compare against, and accepting the caller's bridge would let it
  -- open a second counter series for the same guest.
  IF v_iface IS NULL OR v_iface = '' THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % records no ingress interface', p_session;
  END IF;
  IF v_iface IS DISTINCT FROM p_bridge THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % is on %, not %', p_session, v_iface, p_bridge;
  END IF;
  -- ADDRESS + CLASS. The minor is a pure function of the Session's own address, so a caller cannot name a
  -- different guest's class, and a Session whose address changed cannot keep accruing against the old one.
  IF v_ip IS NULL THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % has no address to measure', p_session;
  END IF;
  IF iam_v2.p3_expected_class_minor(v_ip) IS DISTINCT FROM p_class_minor THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % belongs to class %, not %',
      p_session, iam_v2.p3_expected_class_minor(v_ip), p_class_minor;
  END IF;
  IF p_sampled_at < v_started THEN
    RAISE EXCEPTION 'ACCT_INVALID: sample time % precedes the session start %', p_sampled_at, v_started;
  END IF;
  -- ONE AUTHORITATIVE SERIES. Every component of the checkpoint key is now pinned to the Session's own row,
  -- so a second source tuple for the same Session cannot be invented. This assertion states that explicitly
  -- rather than leaving it as a property somebody has to re-derive: if a stale checkpoint from a previous
  -- address or bridge still exists, it is not the one this observation may advance.
  SELECT count(*) INTO v_other FROM iam_v2.accounting_checkpoints cp2
   WHERE cp2.session_id = p_session
     AND (cp2.bridge, cp2.class_minor, cp2.source_device_id) IS DISTINCT FROM (p_bridge, p_class_minor, p_source_device);
  -- An ENDED session owns no further traffic. Refusing here (rather than only on the delta path) means an
  -- ended session cannot even establish a baseline, so nothing can later be billed against it.
  IF v_ended IS NOT NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % ended at %', p_session, v_ended;
  END IF;

  -- CHECKPOINT: locked (or created) before anything is decided, so two runtimes observing the same counter
  -- cannot both compute a delta from the same previous value.
  SELECT * INTO cp FROM iam_v2.accounting_checkpoints
    WHERE session_id = p_session AND source_device_id = p_source_device
      AND bridge = p_bridge AND class_minor = p_class_minor
    FOR UPDATE;

  IF cp.id IS NULL THEN
    -- FIRST OBSERVATION. There is nothing to subtract from, so nothing is billed: the absolute counter becomes
    -- the baseline. Storing it as usage would charge the guest for everything since the class was created.
    INSERT INTO iam_v2.accounting_checkpoints
      (tenant_id, site_id, session_id, source_device_id, bridge, class_minor, source_epoch,
       prev_bytes_up, prev_bytes_down, prev_sampled_at, last_classification)
      VALUES (p_tenant, p_site, p_session, p_source_device, p_bridge, p_class_minor, p_epoch,
              p_abs_up, p_abs_down, p_sampled_at, 'BASELINED');
    RETURN 'BASELINED';
  END IF;

  IF p_epoch < cp.source_epoch THEN
    -- An older generation than the one already accepted is a stale or misrouted reading, never new usage.
    RAISE EXCEPTION 'ACCT_STALE_EPOCH: epoch % is older than the accepted epoch %', p_epoch, cp.source_epoch;
  END IF;

  IF p_epoch > cp.source_epoch THEN
    -- TRUSTED RESET. The TC owner says this managed class was replaced, so its counters legitimately restart.
    -- The new absolutes become the baseline; the bytes since the replacement are counted from here on.
    UPDATE iam_v2.accounting_checkpoints
       SET source_epoch = p_epoch, prev_bytes_up = p_abs_up, prev_bytes_down = p_abs_down,
           prev_sampled_at = p_sampled_at, last_classification = 'RESET_BASELINED', updated_at = now()
     WHERE id = cp.id;
    RETURN 'RESET_BASELINED';
  END IF;

  -- SAME EPOCH from here on.
  --
  -- TEMPORAL ORDER. Within one counter series, time only moves forward. An observation dated before the last
  -- accepted one is a delayed or misrouted delivery, and treating it as new usage would date a CURRENT delta
  -- into a HISTORICAL window — potentially one already frozen by a boundary watermark, where it would be
  -- recorded as delayed usage that never happened then.
  IF p_sampled_at < cp.prev_sampled_at THEN
    RAISE EXCEPTION 'ACCT_STALE_SAMPLE: sample at % precedes the last accepted sample at %',
      p_sampled_at, cp.prev_sampled_at;
  END IF;
  -- EQUAL sample times are explicitly ALLOWED when the counters advanced. The absolute counters are the
  -- authoritative evidence of how much was used; the timestamp only says when it was read. Two readings
  -- sharing an instant means the caller's clock is coarser than its sampling rate — a real and ordinary
  -- condition — and refusing the pair would throw away measured bytes to defend a precision assumption.
  -- A DECREASE at the same instant is still a regression and is refused below, and an identical pair is the
  -- replay case; neither can slip through here.

  IF p_abs_up = cp.prev_bytes_up AND p_abs_down = cp.prev_bytes_down THEN
    -- EXACT REPLAY. The counters have not moved since the last accepted observation, so whatever the caller
    -- believes about its own delivery, there is nothing new to store. This is what makes an uncertain commit
    -- safe: the retry sees the persisted state and is told so.
    --
    -- The persisted classification is reported so a caller retrying after an uncertain commit learns what
    -- actually happened to its observation — in particular whether it was ACCEPTED or landed in a frozen
    -- window as DELAYED. It is prefixed REPLAY: because the caller must be able to tell "your observation was
    -- accepted, just now" from "your observation was accepted, earlier": counting the second as fresh usage
    -- would make every retry look like new traffic in the daemon's own tallies and health.
    RETURN CASE WHEN cp.last_classification IN ('ACCEPTED','DELAYED')
                THEN 'REPLAY:' || cp.last_classification ELSE 'DUPLICATE' END;
  END IF;

  IF p_abs_up < cp.prev_bytes_up OR p_abs_down < cp.prev_bytes_down THEN
    -- A DECREASE WITHOUT A NEW EPOCH is ambiguous: it could be a silently recreated class, a misread, or a
    -- reused minor. Guessing "count from zero" would invent usage; guessing "ignore" would lose it. Fail closed
    -- and keep the checkpoint, so the next trustworthy observation can be judged against a known-good value.
    RAISE EXCEPTION 'ACCT_COUNTER_REGRESSION: counters went backwards without a new source epoch (up %->%, down %->%)',
      cp.prev_bytes_up, p_abs_up, cp.prev_bytes_down, p_abs_down;
  END IF;

  v_up := p_abs_up - cp.prev_bytes_up;
  v_down := p_abs_down - cp.prev_bytes_down;

  -- ATTRIBUTION at SAMPLE time, through the ONE shared resolver (iam_v2.p3_entitlement_at). There is no
  -- fallback to the session's current entitlement anywhere in this chain.
  v_ent := iam_v2.p3_entitlement_at(p_session, p_sampled_at);
  IF v_ent IS NULL THEN
    RAISE EXCEPTION 'ACCT_NO_BINDING: no entitlement was bound to session % at %', p_session, p_sampled_at;
  END IF;

  -- the per-session record sequence is allocated under the checkpoint lock, so it cannot collide
  SELECT COALESCE(max(sample_seq),0)+1 INTO v_seq FROM iam_v2.accounting_records WHERE session_id = p_session;
  INSERT INTO iam_v2.accounting_records
    (tenant_id, site_id, session_id, sample_seq, bytes_up, bytes_down, sampled_at, ingested_at)
    VALUES (p_tenant, p_site, p_session, v_seq, v_up, v_down, p_sampled_at, now())
    RETURNING id INTO v_rec;

  UPDATE iam_v2.sessions SET bytes_up = bytes_up + v_up, bytes_down = bytes_down + v_down
   WHERE id = p_session;

  SELECT EXISTS (SELECT 1 FROM iam_v2.delayed_accounting_records WHERE accounting_record_id = v_rec)
    INTO v_delayed;
  v_class := CASE WHEN v_delayed THEN 'DELAYED' ELSE 'ACCEPTED' END;

  UPDATE iam_v2.accounting_checkpoints
     SET prev_bytes_up = p_abs_up, prev_bytes_down = p_abs_down, prev_sampled_at = p_sampled_at,
         last_record_id = v_rec, last_classification = v_class, updated_at = now()
   WHERE id = cp.id;
  RETURN v_class;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz) FROM PUBLIC;

-- Detection happens at INGEST: acctd does not need to know a boundary exists. A sample whose sampled_at is at
-- or before a frozen boundary of the Entitlement it was bound to is recorded as delayed. The sample itself is
-- still stored (it is real usage), and the watermark is left exactly as it was.
-- THE binding answer. One function, used by the controlled ingestion operation AND by every trigger beneath
-- it, so "which Entitlement owned this Session at this instant" cannot have two answers.
--
-- There is deliberately NO fallback to iam_v2.sessions.entitlement_id. That pointer says who owns the session
-- NOW; a sample says who owned it THEN. Using the first to answer the second is exactly how a departing
-- guest's pre-boundary traffic gets charged to the grace allowance that replaced it — and it silently
-- "rescues" rows that the exact-interval rule would have refused, which is worse than refusing them.
CREATE OR REPLACE FUNCTION iam_v2.p3_entitlement_at(p_session uuid, p_at timestamptz) RETURNS uuid
  LANGUAGE sql STABLE SET search_path = iam_v2, pg_temp AS $fn$
  SELECT b.entitlement_id FROM iam_v2.session_entitlement_bindings b
   WHERE b.session_id = p_session AND b.bound_from <= p_at
     AND (b.bound_until IS NULL OR b.bound_until > p_at)
   ORDER BY b.seq DESC LIMIT 1;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_entitlement_at(uuid,timestamptz) FROM PUBLIC;

-- Every accounting row must be attributable AT ITS OWN SAMPLE TIME, enforced at the table rather than only
-- inside the operation: a forged row that somehow reached the table would otherwise be attributed by whatever
-- read it later. With no covering interval the insert fails, so there is no row, no delayed row, no session
-- total change and no checkpoint advance.
-- SECURITY DEFINER because a trigger function runs as whoever is WRITING, and the resolver's EXECUTE is
-- revoked from PUBLIC. Without this, a forged insert by a non-owner fails with "permission denied for
-- function p3_entitlement_at" — still refused, but the reason names a privilege instead of the missing
-- binding, which sends whoever reads the log after an incident down the wrong path entirely.
CREATE OR REPLACE FUNCTION iam_v2.p3_accounting_needs_binding() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF iam_v2.p3_entitlement_at(NEW.session_id, NEW.sampled_at) IS NULL THEN
    RAISE EXCEPTION 'ACCT_NO_BINDING: no entitlement was bound to session % at %', NEW.session_id, NEW.sampled_at;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_accounting_needs_binding BEFORE INSERT ON iam_v2.accounting_records
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_accounting_needs_binding();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_accounting_needs_binding() FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.p3_detect_delayed_accounting() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_ent uuid; v_t uuid; v_s uuid; v_wm uuid;
BEGIN
  -- The SAME binding answer the ingestion operation used — no second opinion, and no fallback to the
  -- session's current pointer. The BEFORE INSERT guard has already refused any row without one, so a NULL
  -- here would mean the guard was bypassed; fail closed rather than attribute it to whatever is current.
  v_ent := iam_v2.p3_entitlement_at(NEW.session_id, NEW.sampled_at);
  IF v_ent IS NULL THEN
    RAISE EXCEPTION 'ACCT_NO_BINDING: no entitlement was bound to session % at %', NEW.session_id, NEW.sampled_at;
  END IF;
  SELECT id INTO v_wm FROM iam_v2.entitlement_boundary_watermarks
    WHERE entitlement_id = v_ent AND boundary_at >= NEW.sampled_at ORDER BY boundary_at ASC LIMIT 1;
  IF v_wm IS NULL THEN RETURN NEW; END IF;   -- nothing frozen for this period
  SELECT tenant_id, site_id INTO v_t, v_s FROM iam_v2.sessions WHERE id = NEW.session_id;
  INSERT INTO iam_v2.delayed_accounting_records
    (tenant_id,site_id,accounting_record_id,session_id,entitlement_id,watermark_id,sampled_at,bytes_up,bytes_down)
    VALUES (v_t,v_s,NEW.id,NEW.session_id,v_ent,v_wm,NEW.sampled_at,NEW.bytes_up,NEW.bytes_down)
    ON CONFLICT (accounting_record_id) DO NOTHING;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_detect_delayed_accounting AFTER INSERT ON iam_v2.accounting_records
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_detect_delayed_accounting();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_detect_delayed_accounting() FROM PUBLIC;

-- ============================================================================
-- (4t) THE SECOND TIER OF THE WRITER BOUNDARY: CAPABILITY-SCOPED OPERATIONS.
--
-- The boundary below has one shape: the authoritative write is performed BY a SECURITY DEFINER operation, so
-- the owner is the only role that ever appears as the writer. That is the strongest form and it is the right
-- one wherever the write is a single well-specified statement.
--
-- It is the wrong tool for the families whose authoritative operation is genuinely multi-statement SERVICE
-- logic — the Stay lifecycle (create, room move, status change, reinstatement, each with its own invariants
-- and its own event lineage), the checkout conversion, the resolution record, the quote/purchase pair.
-- Reimplementing those in PL/pgSQL purely to move the writer identity would replace tested, reviewed service
-- code with a second implementation of the same rules, and two implementations of one rule is how the rules
-- start to differ.
--
-- So those families get a capability instead. An approved operation opens a scope for its family; writes to
-- that family are refused unless such a scope is open in the SAME transaction. The scope cannot be forged: a
-- caller can set any GUC it likes, but the token in the GUC must match a row that only the SECURITY DEFINER
-- opener can write, keyed to the current transaction id.
--
-- BE PRECISE ABOUT WHAT THIS PROVES, because the two tiers are not equivalent:
--
--   it DOES prove   the write came from a role holding EXECUTE on that family's opener, inside a declared
--                   operation for that family. An ad-hoc psql session, a restored-dump repair script, a
--                   different service, or a stray statement elsewhere in the SAME service outside any
--                   declared operation are all refused.
--   it does NOT prove the write is the exact statement the operation intended. A service that legitimately
--                   writes Stays can, within its own declared Stay operation, write a Stay row that is
--                   wrong. Tier 1 excludes that; tier 2 does not.
--
-- writerguard.Phase3Requirements() records which tier each family is in, so a family cannot be quietly
-- downgraded from the first to the second without that showing up as a change to the recorded contract.
CREATE UNLOGGED TABLE iam_v2.controlled_operation_scope (
  txid      bigint      NOT NULL,
  family    text        NOT NULL,
  token     uuid        NOT NULL,
  opened_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (txid, family)
);
-- UNLOGGED deliberately: every row is meaningful only for the duration of one transaction, so surviving a
-- crash would mean nothing. The rows a crash leaves behind are cleaned by the opener's janitor below.
COMMENT ON TABLE iam_v2.controlled_operation_scope IS
  'Transaction-scoped capability tokens for controlled-writer families whose operation is service logic. '
  'Written only by iam_v2.begin_controlled_operation().';

CREATE OR REPLACE FUNCTION iam_v2.begin_controlled_operation(p_family text) RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_token uuid;
BEGIN
  -- The allowlist is the whole authorization decision, and it is also what keeps the GUC name below safe:
  -- the family is never caller-shaped text by the time it is concatenated.
  IF p_family NOT IN ('stay','auth_resolution','commerce_intent','checkout_conversion','source_conflict',
                      'auth_context','device_auth','session_binding','grace_publication','alert') THEN
    RAISE EXCEPTION 'no approved capability-scoped controlled-writer family %', p_family;
  END IF;
  -- Bounded janitor. A transaction that rolled back or a backend that died leaves its row behind; nothing
  -- reads a stale row (the txid will not recur inside the retention window) but the table should not grow
  -- without limit on an appliance that runs for years.
  DELETE FROM iam_v2.controlled_operation_scope WHERE opened_at < now() - interval '1 hour';
  v_token := gen_random_uuid();
  INSERT INTO iam_v2.controlled_operation_scope (txid, family, token)
  VALUES (txid_current(), p_family, v_token)
  ON CONFLICT (txid, family) DO UPDATE SET token = EXCLUDED.token, opened_at = now();
  -- is_local = true: the setting dies with the transaction, so a scope cannot outlive the operation that
  -- opened it and be reused by the next statement on a pooled connection.
  PERFORM set_config('iam_v2.op_' || p_family, v_token::text, true);
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) FROM PUBLIC;

-- p3_controlled_operation_open answers the guard's question: "is a scope for this family open in MY
-- transaction?". It is called once per guarded row write, so it must stay cheap.
--
-- SECURITY DEFINER, and deliberately WITHOUT the usual REVOKE FROM PUBLIC. Both are needed for the guard to
-- work at all, and neither weakens it:
--
--   DEFINER — it reads the scope table, which is closed to every role but the opener's owner. As INVOKER it
--             could only be evaluated by roles that can already read the tokens, which is nobody.
--   PUBLIC  — the guard trigger runs as whichever role is attempting the write, so that role must be able to
--             call this. Without PUBLIC EXECUTE the guard still fails closed, but with "permission denied
--             for function p3_controlled_operation_open" instead of the refusal that explains what to do —
--             and an error nobody can act on is an error somebody eventually works around.
--
-- What a caller learns by invoking it directly is whether IT has an open scope, in ITS OWN transaction, for a
-- token IT set. There is nothing there that the caller did not already know.
CREATE OR REPLACE FUNCTION iam_v2.p3_controlled_operation_open(p_family text) RETURNS boolean
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_tok text;
BEGIN
  v_tok := current_setting('iam_v2.op_' || p_family, true);   -- missing_ok: NULL when never set
  IF v_tok IS NULL OR v_tok = '' THEN
    RETURN false;
  END IF;
  RETURN EXISTS (
    SELECT 1 FROM iam_v2.controlled_operation_scope s
     WHERE s.txid = txid_current() AND s.family = p_family AND s.token::text = v_tok);
END $fn$;
-- NO "REVOKE ... FROM PUBLIC" here; see the reasoning above. This is the one Phase-3 SECURITY DEFINER
-- function that intentionally keeps it, and iam_v2_scratch/phase3_0010_lifecycle.sh names it individually.

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
    WHEN 'accounting' THEN 'iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)'
    WHEN 'accounting_origin' THEN 'iam_v2.register_class_origin(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)'
    WHEN 'class_generation' THEN 'iam_v2.allocate_class_generation(uuid,uuid,uuid)'
    WHEN 'auth_offers' THEN 'iam_v2.record_auth_context_offer(uuid,uuid,uuid,uuid,int,bigint,timestamptz)'
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
DECLARE owner_role text; changed boolean := true; v_sig text; v_oid oid; v_cap text;
BEGIN
  -- CAPABILITY-SCOPED FAMILIES (see 4t). The write is allowed when a scope for the family is open in this
  -- transaction, or when the caller is already the opener's owner — the latter so that the operations
  -- themselves, and a database whose roles have not yet been separated, behave identically.
  v_cap := CASE
    WHEN TG_TABLE_NAME IN ('stays','stay_events')                                   THEN 'stay'
    WHEN TG_TABLE_NAME = 'auth_resolutions'                                          THEN 'auth_resolution'
    WHEN TG_TABLE_NAME IN ('offer_quotes','purchases')                               THEN 'commerce_intent'
    WHEN TG_TABLE_NAME IN ('checkout_grace_audit','entitlement_boundary_watermarks') THEN 'checkout_conversion'
    WHEN TG_TABLE_NAME = 'pms_source_conflicts'                                      THEN 'source_conflict'
    WHEN TG_TABLE_NAME = 'auth_contexts'                                             THEN 'auth_context'
    WHEN TG_TABLE_NAME = 'entitlement_device_authorizations'                          THEN 'device_auth'
    WHEN TG_TABLE_NAME = 'session_entitlement_bindings'                               THEN 'session_binding'
    WHEN TG_TABLE_NAME = 'checkout_grace_policy_publications'                         THEN 'grace_publication'
    WHEN TG_TABLE_NAME = 'checkout_grace_alert_actions'                               THEN 'alert'
    ELSE NULL END;
  -- resolve the family's approved function owner INLINE (catalog-only). Deliberately NOT a call to the
  -- introspection helper: this trigger fires as whichever role is writing, and a cross-function EXECUTE
  -- dependency would break exactly the dedicated-owner separation Gate-P needs.
  v_sig := CASE
    WHEN TG_TABLE_NAME = 'site_checkout_grace_config'
      THEN 'iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int)'
    WHEN TG_TABLE_NAME = 'appliance_class_generation'
      THEN 'iam_v2.allocate_class_generation(uuid,uuid,uuid)'
    WHEN TG_TABLE_NAME = 'auth_context_offers'
      THEN 'iam_v2.record_auth_context_offer(uuid,uuid,uuid,uuid,int,bigint,timestamptz)'
    WHEN TG_TABLE_NAME IN ('accounting_records','accounting_checkpoints','delayed_accounting_records','sessions')
      THEN 'iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)'
    -- Capability-scoped families resolve their owner through the operation-scope opener (see 4t), as does the
    -- scope table itself: a token nobody but the opener can write is what makes the scope unforgeable.
    WHEN TG_TABLE_NAME IN ('stays','stay_events','auth_resolutions','offer_quotes','purchases',
                           'checkout_grace_audit','entitlement_boundary_watermarks','pms_source_conflicts',
                           'auth_contexts','entitlement_device_authorizations','session_entitlement_bindings',
                           'checkout_grace_policy_publications','checkout_grace_alert_actions',
                           'controlled_operation_scope')
      THEN 'iam_v2.begin_controlled_operation(text)'
    ELSE 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)' END;
  v_oid := to_regprocedure(v_sig);
  IF v_oid IS NULL THEN
    RAISE EXCEPTION 'controlled-writer function % is not resolvable (fail closed)', v_sig;
  END IF;
  SELECT pg_get_userbyid(proowner) INTO owner_role FROM pg_proc WHERE oid = v_oid;
  IF owner_role IS NULL OR owner_role = '' THEN
    RAISE EXCEPTION 'controlled-writer owner for % is not resolvable (fail closed)', v_sig;
  END IF;

  IF v_cap IS NOT NULL THEN
    -- EVERY write to a capability-scoped family is checked, including DELETE: an authoritative record that
    -- can be removed outside a declared operation is a record that can be made to have never happened.
    IF current_user <> owner_role AND NOT iam_v2.p3_controlled_operation_open(v_cap) THEN
      -- RAISE takes only '%' substitutions; '%L' is format()'s syntax and would print a literal L.
      RAISE EXCEPTION
        '%: writes to the % family require an open controlled operation (caller %) — call iam_v2.begin_controlled_operation(''%'') in the transaction that performs them',
        TG_TABLE_NAME, v_cap, current_user, v_cap;
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
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
  IF TG_TABLE_NAME IN ('accounting_records','accounting_checkpoints','delayed_accounting_records',
                       'appliance_class_generation','auth_context_offers') THEN
    -- EVERY write is controlled. A physical measurement has exactly one legitimate author: the operation that
    -- computed it from a locked checkpoint. A raw INSERT here is invented usage; a raw UPDATE is rewritten
    -- history; and a raw checkpoint write is worse than either, because it silently changes what every FUTURE
    -- observation will be measured against.
    changed := true;
  ELSIF TG_TABLE_NAME = 'sessions' AND TG_OP = 'UPDATE' THEN
    -- A Session row is written by several legitimate paths (it is created, bound, rebound and ended), but two
    -- groups of columns are accounting state:
    --   * the USAGE TOTALS, advanced only by the ingestion operation in the same transaction as the record
    --     that justifies them — anything else moves a total with no row behind it;
    --   * the ACCOUNTING IDENTITY (address and ingress interface), which the operation re-derives the
    --     counter source from. Rewriting either retroactively changes which physical counters a Session is
    --     measured against, which is a silent way to make one guest's traffic land on another's checkpoint.
    changed := (NEW.bytes_up IS DISTINCT FROM OLD.bytes_up OR NEW.bytes_down IS DISTINCT FROM OLD.bytes_down
      OR NEW.ip IS DISTINCT FROM OLD.ip OR NEW.ingress_interface IS DISTINCT FROM OLD.ingress_interface);
  ELSIF TG_TABLE_NAME = 'entitlements' THEN
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
-- INSERT *and* UPDATE: appending a transition and marking one superseded are both authoritative history writes.
CREATE TRIGGER p3_est_controlled_writer BEFORE INSERT OR UPDATE ON iam_v2.entitlement_state_transitions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
-- (item 1) INSERT is protected too: seeding the FIRST authoritative grace-config row is itself an authoritative
-- write, so it must come through publish_checkout_grace_config() and never from a raw non-owner INSERT.
CREATE TRIGGER p3_grace_config_controlled_writer BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.site_checkout_grace_config
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
-- (C7) THE ACCOUNTING WRITER BOUNDARY. Physical usage is the one kind of Phase-3 state that can be corrupted
-- without anything looking wrong afterwards: there is no second copy to reconcile against, and a plausible row
-- is indistinguishable from a real one. So every table in the measurement chain is closed to raw DML, and the
-- checkpoint — the value every future delta is computed FROM — is closed hardest.
CREATE TRIGGER p3_accounting_records_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.accounting_records
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_accounting_checkpoints_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.accounting_checkpoints
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_delayed_accounting_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.delayed_accounting_records
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
-- Session usage totals only (see the guard body): creating, binding and ending a Session stay ordinary writes.
-- The offer set is authoritative state: a row inserted afterwards would retroactively make an unoffered
-- package look offered, which is precisely the check it exists to support. Attached HERE, with the other
-- guards, because the guard function is defined in this section -- a trigger created earlier in the file
-- would reference a function that does not exist yet and the whole migration would fail to apply.
CREATE TRIGGER p3_auth_context_offers_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.auth_context_offers
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_class_generation_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.appliance_class_generation
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_session_usage_controlled_writer
  BEFORE UPDATE ON iam_v2.sessions
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

-- An alert-bearing audit row OPENS its own lifecycle in the SAME transaction. Leaving the OPEN row to the
-- application would allow an alert that exists but has no lifecycle — invisible to the queue, impossible to
-- acknowledge — so the database creates it structurally instead.
CREATE OR REPLACE FUNCTION iam_v2.p3_alert_open_on_audit() RETURNS trigger
  -- SECURITY DEFINER: opening the alert is a write to the guarded alert-action family, and it happens as a
  -- consequence of an audit row written by whichever role performed the conversion.
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('alert');
  IF NEW.alert_code IS NOT NULL THEN
    INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id, site_id, audit_id, seq, action, reason_code)
      VALUES (NEW.tenant_id, NEW.site_id, NEW.id, 1, 'OPEN', NEW.reason_code);
  END IF;
  RETURN NULL;
END $fn$;
CREATE TRIGGER p3_alert_open_on_audit AFTER INSERT ON iam_v2.checkout_grace_audit
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_alert_open_on_audit();
REVOKE EXECUTE ON FUNCTION iam_v2.p3_alert_open_on_audit() FROM PUBLIC;

-- THE controlled alert-lifecycle operation. Callers never insert actions directly: they name the audit, the
-- action they intend, who they are, why, and WHAT STATE THEY BELIEVE THE ALERT IS IN. That last part is what
-- makes two operators clicking Acknowledge at the same moment resolve to exactly one winner and one clear
-- conflict, instead of one silently overwriting the other's view of the world.
--
-- Returns the new sequence number. Raises with a bounded, machine-greppable prefix so an API layer can map
-- the failure to the right HTTP status without parsing prose:
--   ALERT_NOT_FOUND / ALERT_STATE_CONFLICT / ALERT_ACTOR_INVALID / ALERT_ACTION_INVALID
CREATE OR REPLACE FUNCTION iam_v2.record_alert_action(
    p_tenant uuid, p_site uuid, p_audit uuid, p_action text,
    p_actor uuid, p_reason text, p_expected_state text) RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_seq bigint; v_head text; v_head_seq bigint; v_audit uuid;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('alert');
  IF p_action NOT IN ('ACKNOWLEDGED','RESOLVED') THEN
    -- OPEN belongs to the audit that raised the alert; an operator can only move it forward.
    RAISE EXCEPTION 'ALERT_ACTION_INVALID: % is not an operator action', p_action;
  END IF;
  -- Mandatory, and enforced HERE rather than only at the HTTP layer: an operator action with no reason is an
  -- unexplained state change in an audit trail whose whole purpose is explaining state changes.
  IF p_reason IS NULL OR p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'ALERT_ACTION_INVALID: a bounded machine reason code is required';
  END IF;
  -- NULL must never mean "act against whatever state you find": that is precisely the race the expected-state
  -- check exists to prevent.
  IF p_expected_state IS NULL OR p_expected_state NOT IN ('OPEN','ACKNOWLEDGED') THEN
    RAISE EXCEPTION 'ALERT_STATE_CONFLICT: an expected state of OPEN or ACKNOWLEDGED is required';
  END IF;
  -- scope: the alert must belong to THIS tenant+site, and the row lock serializes the whole lifecycle
  SELECT id INTO v_audit FROM iam_v2.checkout_grace_audit
    WHERE id = p_audit AND tenant_id = p_tenant AND site_id = p_site AND alert_code IS NOT NULL
    FOR UPDATE;
  IF v_audit IS NULL THEN
    RAISE EXCEPTION 'ALERT_NOT_FOUND: no alert % in this scope', p_audit;
  END IF;
  -- actor: an existing, active operator of the SAME tenant. An action nobody can be held to is not an audit.
  IF p_actor IS NULL THEN
    RAISE EXCEPTION 'ALERT_ACTOR_INVALID: an actor is required';
  END IF;
  PERFORM 1 FROM public.operators o
    WHERE o.id = p_actor AND o.status = 'active'
      AND (o.tenant_id IS NULL OR o.tenant_id = p_tenant);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'ALERT_ACTOR_INVALID: actor % is not an active operator of this tenant', p_actor;
  END IF;
  SELECT action, seq INTO v_head, v_head_seq FROM iam_v2.checkout_grace_alert_actions
    WHERE audit_id = p_audit ORDER BY seq DESC LIMIT 1;
  IF v_head IS NULL THEN
    RAISE EXCEPTION 'ALERT_NOT_FOUND: alert % has no lifecycle', p_audit;
  END IF;
  -- optimistic state match: the caller acted on what it last saw, and nothing has moved since.
  IF p_expected_state <> v_head THEN
    RAISE EXCEPTION 'ALERT_STATE_CONFLICT: alert is % (caller expected %)', v_head, p_expected_state;
  END IF;
  IF v_head = 'RESOLVED' THEN
    RAISE EXCEPTION 'ALERT_STATE_CONFLICT: alert is already RESOLVED';
  END IF;
  IF v_head = 'ACKNOWLEDGED' AND p_action = 'ACKNOWLEDGED' THEN
    RAISE EXCEPTION 'ALERT_STATE_CONFLICT: alert is already ACKNOWLEDGED';
  END IF;
  v_seq := v_head_seq + 1;
  INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id, site_id, audit_id, seq, action, actor, reason_code)
    VALUES (p_tenant, p_site, p_audit, v_seq, p_action, p_actor, p_reason);
  RETURN v_seq;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.record_alert_action(uuid,uuid,uuid,text,uuid,text,text) FROM PUBLIC;

-- ============================================================================
-- (4m2) THE ONE authoritative Grace-package validator.
--
-- Two places need to know whether a Package Revision can actually serve the published Checkout-Grace policy:
-- the Hotel-Admin PUBLICATION (refuse it up front) and the CHECKOUT CONVERSION (fall back to Emergency if it
-- ever stops being true). Implementing that twice guarantees they eventually disagree — and the failure mode
-- is the worst kind: publication says "saved", every subsequent checkout silently falls back to Emergency
-- Grace, and the alert queue fills up with something an operator already thought they had configured.
--
-- So it lives HERE, once, and both callers use it.
--
-- grace_package_mismatch_reason returns NULL when the revision exactly serves the policy, or the FIRST bounded
-- reason it does not. grace_package_matches_policy is the boolean form for the conversion path.
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.grace_package_mismatch_reason(
    p_tenant uuid, p_site uuid, p_pkg_rev uuid,
    p_duration int, p_down int, p_up int, p_quota bigint, p_dev_limit int, p_dev_policy text) RETURNS text
  LANGUAGE plpgsql STABLE SET search_path = iam_v2, pg_temp AS $fn$
DECLARE r record;
BEGIN
  IF p_pkg_rev IS NULL THEN
    -- A policy with no Package cannot grant anything. The conversion treats it as invalid configuration and
    -- falls back to Emergency, so it must never be publishable as an ordinary policy.
    RETURN 'PACKAGE_REQUIRED';
  END IF;
  SELECT ipr.id, ipr.package_type, ipr.price_minor, ipr.settlement_methods, ipr.duration_policy,
         ipr.service_plan_revision_id, ip.is_system, ip.active AS pkg_active, ip.current_revision_id, ip.code AS pkg_code,
         spr.id AS spr_id, spr.down_kbps, spr.up_kbps, spr.data_quota_bytes,
         spr.max_concurrent_devices, spr.device_limit_policy, spr.time_accounting_mode, sp.enabled AS plan_enabled,
         sp.code AS plan_code
    INTO r
    FROM iam_v2.internet_package_revisions ipr
    JOIN iam_v2.internet_packages ip
      ON ip.tenant_id = ipr.tenant_id AND ip.site_id = ipr.site_id AND ip.id = ipr.package_id
    LEFT JOIN iam_v2.service_plan_revisions spr
      ON spr.tenant_id = ipr.tenant_id AND spr.site_id = ipr.site_id AND spr.id = ipr.service_plan_revision_id
    LEFT JOIN iam_v2.service_plans sp
      ON sp.tenant_id = spr.tenant_id AND sp.site_id = spr.site_id AND sp.id = spr.service_plan_id
   WHERE ipr.id = p_pkg_rev AND ipr.tenant_id = p_tenant AND ipr.site_id = p_site;

  IF r.id IS NULL THEN RETURN 'PACKAGE_NOT_IN_SITE'; END IF;
  IF r.current_revision_id IS DISTINCT FROM r.id THEN RETURN 'NOT_CURRENT_REVISION'; END IF;
  IF r.pkg_active IS NOT TRUE THEN RETURN 'PACKAGE_INACTIVE'; END IF;
  IF r.package_type <> 'CHECKOUT_GRACE' THEN RETURN 'PACKAGE_TYPE'; END IF;
  IF r.is_system IS NOT TRUE THEN RETURN 'PACKAGE_NOT_SYSTEM_OWNED'; END IF;
  -- The RESERVED Emergency catalog is the fallback of last resort, not a policy an operator may adopt as the
  -- ordinary one. Allowing it would make "configured" and "emergency" indistinguishable in the audit trail and
  -- would silence the very alert that tells an operator their real policy is broken.
  --
  -- Checking the PACKAGE code alone is not enough: an ordinary-looking package can pin the reserved Emergency
  -- SERVICE PLAN, which repurposes the same reserved infrastructure through a different door.
  IF r.pkg_code IN ('__sys_emergency_grace_pkg__','__sys_emergency_grace_plan__') THEN
    RETURN 'PACKAGE_IS_EMERGENCY_CATALOG';
  END IF;
  IF r.plan_code IN ('__sys_emergency_grace_plan__','__sys_emergency_grace_pkg__') THEN
    RETURN 'PLAN_IS_EMERGENCY_CATALOG';
  END IF;
  IF r.price_minor <> 0 THEN RETURN 'PACKAGE_NOT_FREE'; END IF;
  IF array_length(r.settlement_methods,1) <> 1 OR r.settlement_methods[1] <> 'NOT_REQUIRED' THEN
    RETURN 'PACKAGE_SETTLEMENT';
  END IF;
  IF r.spr_id IS NULL THEN RETURN 'PLAN_REVISION_MISSING'; END IF;
  IF r.plan_enabled IS NOT TRUE THEN RETURN 'PLAN_DISABLED'; END IF;
  -- duration policy: the package must END as grace, for exactly the published duration, under the approved
  -- policy version when it declares one.
  IF COALESCE(r.duration_policy->>'end_mode','') <> 'GRACE_AFTER_CHECKOUT' THEN RETURN 'DURATION_END_MODE'; END IF;
  IF jsonb_typeof(r.duration_policy->'grace_duration_seconds') IS DISTINCT FROM 'number'
     OR (r.duration_policy->>'grace_duration_seconds') !~ '^[0-9]{1,9}$'
     OR (r.duration_policy->>'grace_duration_seconds')::int <> p_duration THEN
    RETURN 'DURATION_SECONDS';
  END IF;
  -- The approved policy version is REQUIRED, not optional-if-present: a package that simply omits the key
  -- would otherwise pass, and "no declared version" is not the same as "the approved version". It must also be
  -- a scalar string — a number, array or object is a malformed declaration, not a version.
  IF jsonb_typeof(r.duration_policy->'policy_version') IS DISTINCT FROM 'string' THEN
    RETURN 'DURATION_POLICY_VERSION';
  END IF;
  IF r.duration_policy->>'policy_version' <> 'CHECKOUT_GRACE_V1' THEN
    RETURN 'DURATION_POLICY_VERSION';
  END IF;
  -- the pinned plan revision must carry EXACTLY the published scalars: a policy the plan cannot deliver is a
  -- promise to the guest that the enforcement path would quietly break.
  IF r.down_kbps <> p_down THEN RETURN 'PLAN_DOWN_KBPS'; END IF;
  IF r.up_kbps <> p_up THEN RETURN 'PLAN_UP_KBPS'; END IF;
  IF r.data_quota_bytes IS DISTINCT FROM p_quota THEN RETURN 'PLAN_DATA_QUOTA'; END IF;
  IF r.max_concurrent_devices <> p_dev_limit THEN RETURN 'PLAN_DEVICE_LIMIT'; END IF;
  IF r.device_limit_policy <> p_dev_policy THEN RETURN 'PLAN_DEVICE_POLICY'; END IF;
  IF r.time_accounting_mode <> 'VALIDITY_WINDOW' THEN RETURN 'PLAN_TIME_ACCOUNTING'; END IF;
  RETURN NULL;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.grace_package_mismatch_reason(uuid,uuid,uuid,int,int,int,bigint,int,text) FROM PUBLIC;

-- selectable_grace_packages is the ONE answer to "what may an operator choose?". It derives each candidate's
-- typed values from its OWN pinned immutable revision and judges them with the SAME validator publication
-- uses, so the list can never contain a package that publication will then refuse. It returns the mismatch
-- reason too: an excluded candidate is a bounded, operator-visible diagnostic rather than a mystery or a 500.
--
-- Derivation is defensive on purpose. duration_policy is operator-authored JSON, so a malformed value must
-- exclude THAT candidate, never break the whole list.
CREATE OR REPLACE FUNCTION iam_v2.selectable_grace_packages(p_tenant uuid, p_site uuid)
  RETURNS TABLE (
    package_revision_id uuid, package_code text, revision_no int,
    service_plan_revision_id uuid, service_plan_code text, service_plan_revision_no int,
    down_kbps int, up_kbps int, data_quota_bytes bigint,
    device_limit int, device_limit_policy text, time_accounting_mode text,
    grace_duration_seconds int, end_mode text, policy_version text,
    settlement_mode text, is_current boolean, is_active boolean, mismatch_reason text)
  LANGUAGE sql STABLE SET search_path = iam_v2, pg_temp AS $fn$
  WITH candidate AS (
    SELECT ipr.id AS package_revision_id, ip.code AS package_code, ipr.revision_no,
           spr.id AS service_plan_revision_id, sp.code AS service_plan_code, spr.revision_no AS service_plan_revision_no,
           spr.down_kbps, spr.up_kbps, spr.data_quota_bytes,
           spr.max_concurrent_devices AS device_limit, spr.device_limit_policy, spr.time_accounting_mode,
           -- a non-numeric or oversized duration yields NULL, which the validator then rejects for THIS row
           CASE WHEN jsonb_typeof(ipr.duration_policy->'grace_duration_seconds') = 'number'
                     AND (ipr.duration_policy->>'grace_duration_seconds') ~ '^[0-9]{1,9}$'
                THEN (ipr.duration_policy->>'grace_duration_seconds')::int END AS grace_duration_seconds,
           CASE WHEN jsonb_typeof(ipr.duration_policy->'end_mode') = 'string'
                THEN ipr.duration_policy->>'end_mode' END AS end_mode,
           CASE WHEN jsonb_typeof(ipr.duration_policy->'policy_version') = 'string'
                THEN ipr.duration_policy->>'policy_version' END AS policy_version,
           array_to_string(ipr.settlement_methods, ',') AS settlement_mode,
           (ip.current_revision_id = ipr.id) AS is_current, ip.active AS is_active
      FROM iam_v2.internet_package_revisions ipr
      JOIN iam_v2.internet_packages ip
        ON ip.tenant_id = ipr.tenant_id AND ip.site_id = ipr.site_id AND ip.id = ipr.package_id
      JOIN iam_v2.service_plan_revisions spr
        ON spr.tenant_id = ipr.tenant_id AND spr.site_id = ipr.site_id AND spr.id = ipr.service_plan_revision_id
      JOIN iam_v2.service_plans sp
        ON sp.tenant_id = spr.tenant_id AND sp.site_id = spr.site_id AND sp.id = spr.service_plan_id
     WHERE ipr.tenant_id = p_tenant AND ipr.site_id = p_site
       AND ipr.package_type = 'CHECKOUT_GRACE')
  SELECT c.package_revision_id, c.package_code, c.revision_no,
         c.service_plan_revision_id, c.service_plan_code, c.service_plan_revision_no,
         c.down_kbps, c.up_kbps, c.data_quota_bytes,
         c.device_limit, c.device_limit_policy, c.time_accounting_mode,
         c.grace_duration_seconds, c.end_mode, c.policy_version,
         c.settlement_mode, c.is_current, c.is_active,
         -- judged by the SAME function publication uses, against the candidate's OWN values
         iam_v2.grace_package_mismatch_reason(p_tenant, p_site, c.package_revision_id,
             COALESCE(c.grace_duration_seconds, -1), c.down_kbps, c.up_kbps, c.data_quota_bytes,
             c.device_limit, c.device_limit_policy) AS mismatch_reason
    FROM candidate c
   ORDER BY c.package_code, c.revision_no DESC;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.selectable_grace_packages(uuid,uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.grace_package_matches_policy(
    p_tenant uuid, p_site uuid, p_pkg_rev uuid,
    p_duration int, p_down int, p_up int, p_quota bigint, p_dev_limit int, p_dev_policy text) RETURNS boolean
  LANGUAGE sql STABLE SET search_path = iam_v2, pg_temp AS $fn$
  SELECT iam_v2.grace_package_mismatch_reason($1,$2,$3,$4,$5,$6,$7,$8,$9) IS NULL;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.grace_package_matches_policy(uuid,uuid,uuid,int,int,int,bigint,int,text) FROM PUBLIC;

-- ============================================================================
-- (4n) CONTROLLED CHECKOUT-GRACE POLICY PUBLICATION.
--
-- publish_checkout_grace_config() is the low-level writer: it locks the row, validates the typed policy and
-- bumps config_version. What it does NOT know is who published, whether they were looking at the current
-- version, or whether the Package they pointed at is actually a usable grace Package. Exposing it directly to
-- an API would let two operators overwrite each other silently and let a policy reference a Package that can
-- never be granted.
--
-- This operation adds the binding preconditions and an IMMUTABLE publication audit. Failures use bounded,
-- machine-greppable prefixes so an API can map them to the right status without parsing prose:
--   GRACE_VERSION_CONFLICT / GRACE_ACTOR_INVALID / GRACE_PACKAGE_INVALID / GRACE_POLICY_UNSUPPORTED
-- ============================================================================
CREATE TABLE iam_v2.checkout_grace_policy_publications (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  config_version int NOT NULL CHECK (config_version >= 1),
  actor uuid NOT NULL,
  reason_code text CHECK (reason_code IS NULL OR reason_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
  grace_package_revision_id uuid,
  policy_snapshot jsonb NOT NULL,
  published_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, site_id, config_version));
CREATE TRIGGER p3_grace_publication_appendonly BEFORE UPDATE OR DELETE ON iam_v2.checkout_grace_policy_publications
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();

CREATE OR REPLACE FUNCTION iam_v2.publish_checkout_grace_policy(
    p_tenant uuid, p_site uuid, p_pkg_rev uuid,
    p_duration int, p_down int, p_up int, p_quota bigint, p_dev_limit int, p_dev_policy text,
    p_eligibility int, p_expected_version int, p_actor uuid, p_reason text) RETURNS int
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_current int; v_new int; v_mismatch text;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('grace_publication');
  -- ACTOR: an existing, active operator of this tenant. A policy nobody can be held to is not governed.
  IF p_actor IS NULL THEN RAISE EXCEPTION 'GRACE_ACTOR_INVALID: an actor is required'; END IF;
  PERFORM 1 FROM public.operators o
    WHERE o.id = p_actor AND o.status = 'active' AND (o.tenant_id IS NULL OR o.tenant_id = p_tenant);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'GRACE_ACTOR_INVALID: actor % is not an active operator of this tenant', p_actor;
  END IF;
  -- A publication with no recorded reason is an unattributable change to what every departing guest receives.
  IF p_reason IS NULL OR p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'GRACE_ACTOR_INVALID: a bounded machine reason code is required';
  END IF;

  -- POLICY: only the capability that actually exists. DISCONNECT_OLDEST and ADMIN_APPROVAL are refused rather
  -- than accepted-and-approximated, because a policy the enforcement path cannot honour is worse than none.
  IF p_dev_policy <> 'REJECT_NEW_DEVICE' THEN
    RAISE EXCEPTION 'GRACE_POLICY_UNSUPPORTED: device limit policy % is not implemented', p_dev_policy;
  END IF;

  -- PACKAGE GRAPH: validated by THE SAME function the Checkout conversion uses, so a policy that would later
  -- be judged invalid (and silently fall back to Emergency Grace on every departure) is refused NOW, while an
  -- operator is looking at it. A NULL package is refused for exactly that reason: it is not a policy, it is a
  -- guaranteed Emergency fallback wearing a success message.
  v_mismatch := iam_v2.grace_package_mismatch_reason(p_tenant, p_site, p_pkg_rev,
                                                     p_duration, p_down, p_up, p_quota, p_dev_limit, p_dev_policy);
  IF v_mismatch IS NOT NULL THEN
    RAISE EXCEPTION 'GRACE_PACKAGE_INVALID: %', v_mismatch;
  END IF;

  -- OPTIMISTIC VERSION: the caller edited what it last read. Two operators publishing at once produce one
  -- winner and one explicit conflict, never a silent overwrite. 0 means "I believe nothing is published yet".
  -- It is MANDATORY here, not just at the HTTP layer: a NULL that meant "skip concurrency control" would make
  -- the database's own guarantee depend on a caller remembering to ask for it.
  IF p_expected_version IS NULL OR p_expected_version < 0 THEN
    RAISE EXCEPTION 'GRACE_VERSION_CONFLICT: an expected config_version (>= 0) is required';
  END IF;
  SELECT config_version INTO v_current FROM iam_v2.site_checkout_grace_config
    WHERE tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF COALESCE(v_current,0) <> p_expected_version THEN
    RAISE EXCEPTION 'GRACE_VERSION_CONFLICT: current version is % (caller expected %)', COALESCE(v_current,0), p_expected_version;
  END IF;

  v_new := iam_v2.publish_checkout_grace_config(p_tenant, p_site, p_pkg_rev, p_duration, p_down, p_up,
                                                p_quota, p_dev_limit, p_dev_policy, p_eligibility);

  -- IMMUTABLE publication audit. An identical re-publish is idempotent in the writer (the version does not
  -- move), so the audit is only appended when a NEW version was actually created — the record then means
  -- exactly what it says: this actor put this policy in force.
  INSERT INTO iam_v2.checkout_grace_policy_publications
    (tenant_id, site_id, config_version, actor, reason_code, grace_package_revision_id, policy_snapshot)
    SELECT p_tenant, p_site, v_new, p_actor, p_reason, p_pkg_rev,
           jsonb_build_object('grace_duration_seconds', p_duration, 'grace_down_kbps', p_down,
                              'grace_up_kbps', p_up, 'grace_data_quota_bytes', p_quota,
                              'grace_device_limit', p_dev_limit, 'grace_device_limit_policy', p_dev_policy,
                              'eligibility_window_seconds', p_eligibility)
    WHERE NOT EXISTS (SELECT 1 FROM iam_v2.checkout_grace_policy_publications
                      WHERE tenant_id = p_tenant AND site_id = p_site AND config_version = v_new);
  RETURN v_new;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.publish_checkout_grace_policy(uuid,uuid,uuid,int,int,int,bigint,int,text,int,int,uuid,text) FROM PUBLIC;

-- An operator's queue is what still needs attention, so the view exposes only alerts whose lifecycle head is
-- NOT RESOLVED. It carries that head state AND its sequence, because the next action must be able to say what
-- state it believed the alert was in — that is what makes two operators clicking at once produce exactly one
-- winner and one clear conflict instead of one silently overwriting the other.
CREATE OR REPLACE VIEW iam_v2.active_operational_alerts AS
  SELECT a.id AS audit_id, a.tenant_id, a.site_id, a.pms_interface_id, a.stay_id, a.lifecycle_version,
         a.alert_code, a.trigger, a.policy_version, a.reason_code, a.boundary_at, a.boundary_clock_suspect, a.created_at,
         COALESCE(h.action, 'OPEN') AS state,
         COALESCE(h.action, 'OPEN') AS alert_state,
         COALESCE(h.seq, 1)         AS alert_seq,
         h.created_at               AS state_changed_at
  FROM iam_v2.checkout_grace_audit a
  LEFT JOIN LATERAL (SELECT act.action, act.seq, act.created_at FROM iam_v2.checkout_grace_alert_actions act
                     WHERE act.audit_id = a.id ORDER BY act.seq DESC LIMIT 1) h ON true
  WHERE a.alert_code IS NOT NULL AND COALESCE(h.action, 'OPEN') <> 'RESOLVED';

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

-- (C8) THE REMAINING AUTHORITATIVE FAMILIES.
--
-- Everything above closes the families whose corruption is silent. These close the families whose corruption
-- is ATTRIBUTABLE — the rows that answer "who was allowed what, when, and on whose authority". They matter
-- for a different reason: not because a bad row is invisible, but because a bad row is the ANSWER. An
-- Auth Context consumed by a raw UPDATE, a device authorization interval closed by hand, an alert action
-- appended without going through its state machine — each of those is a durable false statement about what
-- happened, and every audit afterwards reads it as fact.
--
-- The intervals are guarded rather than the point events: an authorization that was never closed and an
-- authorization that was closed retroactively are the same row shape, and only the controlled operation
-- knows the difference.
CREATE TRIGGER p3_auth_context_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.auth_contexts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_device_auth_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.entitlement_device_authorizations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_session_binding_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.session_entitlement_bindings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_grace_publication_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.checkout_grace_policy_publications
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_alert_action_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.checkout_grace_alert_actions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_source_conflict_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.pms_source_conflicts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_stay_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.stays
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_stay_event_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.stay_events
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_auth_resolution_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.auth_resolutions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_quote_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.offer_quotes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_purchase_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.purchases
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_checkout_conversion_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.checkout_grace_audit
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
CREATE TRIGGER p3_boundary_watermark_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.entitlement_boundary_watermarks
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
-- The scope table is guarded by the FIRST tier (owner-only), not by a capability — a capability that could
-- authorise writing its own token would authorise nothing at all.
CREATE TRIGGER p3_operation_scope_controlled_writer
  BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.controlled_operation_scope
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();

-- migration ledger (prod parity with 0009; the authoritative runner scripts/edge-migrate.sh gates on this)
INSERT INTO public.schema_migrations (version) VALUES ('0010_phase3_stay_resolution') ON CONFLICT DO NOTHING;

COMMIT;
