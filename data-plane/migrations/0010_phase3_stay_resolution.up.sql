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
-- No public-schema mutation. No SECURITY DEFINER. Zero runtime grants (dark).
BEGIN;

-- ============================================================================
-- (1) pms_interface_runtime — FOUR independent axes; NO stored derived freshness.
-- ============================================================================
CREATE TABLE iam_v2.pms_interface_runtime (
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  pms_interface_id uuid NOT NULL,
  pinned_revision_id uuid,
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

  PRIMARY KEY (tenant_id, site_id, pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id)
    REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, pinned_revision_id)
    REFERENCES iam_v2.pms_interface_revisions (tenant_id, site_id, pms_interface_id, id),

  -- structural coherence (no now()-dependent logic; time-threshold decisions live in the domain)
  CONSTRAINT pir_generation_nonneg CHECK (runtime_generation >= 0),
  CONSTRAINT pir_connected_pins CHECK (
    transport_status <> 'CONNECTED' OR (pinned_revision_id IS NOT NULL AND last_connected_at IS NOT NULL)),
  CONSTRAINT pir_heartbeat_not_future CHECK (last_heartbeat_at IS NULL OR last_heartbeat_at <= updated_at),
  CONSTRAINT pir_resync_coherent CHECK (
        (resync_started_at IS NULL OR resync_requested_at IS NOT NULL)
    AND (resync_started_at IS NULL OR resync_requested_at IS NULL OR resync_started_at >= resync_requested_at)),
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
  ADD COLUMN occupancy_clock_suspect boolean;
ALTER TABLE iam_v2.stays
  ADD CONSTRAINT stays_effco_only_after_checkout
    CHECK (effective_checkout_at IS NULL OR status IN ('CHECKED_OUT','POST_STAY_ACTIVE')),
  -- all-or-none occupancy evidence tuple
  ADD CONSTRAINT stays_occupancy_all_or_none CHECK (
    (occupancy_evidence_at IS NULL AND occupancy_ingested_at IS NULL AND occupancy_revision_id IS NULL
       AND occupancy_normalization_version IS NULL AND occupancy_clock_suspect IS NULL)
    OR
    (occupancy_evidence_at IS NOT NULL AND occupancy_ingested_at IS NOT NULL AND occupancy_revision_id IS NOT NULL
       AND occupancy_normalization_version IS NOT NULL AND occupancy_clock_suspect IS NOT NULL)),
  ADD CONSTRAINT stays_occupancy_norm_pos
    CHECK (occupancy_normalization_version IS NULL OR occupancy_normalization_version > 0),
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
  -- Phase-3 Checkout Grace supports ONLY REJECT_NEW_DEVICE from the canonical vocabulary (mg2):
  -- existing authorized devices persist; devices above the limit are grandfathered; new devices are
  -- rejected until the active count falls below the limit. DISCONNECT_OLDEST / ADMIN_APPROVAL are NOT
  -- valid for Grace (the token is reused from the canonical vocab; no second enum is introduced).
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
-- (6) stay_events: application-result columns + append-only lineage guard.
-- ============================================================================
ALTER TABLE iam_v2.stay_events
  ADD COLUMN processed_at timestamptz,
  ADD COLUMN review_code text
    CHECK (review_code IS NULL OR length(review_code) <= 200);

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

  -- UPDATE: immutable identity / normalization columns
  IF   NEW.tenant_id             IS DISTINCT FROM OLD.tenant_id
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
  THEN
    RAISE EXCEPTION 'stay_events identity/normalization columns are immutable (append-only)';
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

  -- PENDING -> terminal (one move)
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
DECLARE allowed boolean; is_reinstate boolean;
BEGIN
  IF NEW.last_applied_event_version < OLD.last_applied_event_version THEN
    RAISE EXCEPTION 'stays.last_applied_event_version cannot decrease (% -> %)',
      OLD.last_applied_event_version, NEW.last_applied_event_version;
  END IF;

  is_reinstate := (OLD.status = 'CHECKED_OUT' AND NEW.status = 'IN_HOUSE');

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
