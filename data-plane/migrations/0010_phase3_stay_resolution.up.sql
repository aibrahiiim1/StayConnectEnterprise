-- 0010 — Phase 3 (PMS Stay Domain, STRICT resolution, Checkout Grace) additive iam_v2 hardening.
-- ADDITIVE + reversible. DARK; no data. Owner-inherited (iam_v2_owner in prod, as with 0009).
-- Scope is proven by docs/evidence/StayConnect-IAM-Phase3-Schema-Gap-Audit.md against a disposable PG16.
-- The canonical mg1..mg9 schema already provides all 18 Phase-3 base tables; this migration adds ONLY:
--   (1) pms_interface_runtime — durable PMS connector runtime state, FOUR INDEPENDENT axes;
--   (2) one-way stays.status transition + monotonic lifecycle_version/last_applied_event_version guard;
--   (3) NO stays.checkout_episode — the episode IS stays.lifecycle_version (purchases.checkout_episode is
--       populated from the locked Stay's lifecycle_version; existing one_conversion_per_episode key stands);
--   (4) stays.effective_checkout_at + per-Stay occupancy-evidence fields (occupancy freshness axis) +
--       typed grace scalar columns on site_checkout_grace_config (validated bounds);
--   (5) auth_resolutions.resolution_request_id — server-side resolution-request idempotency key;
--   (6) stay_events append-only guard (immutable identity/normalization; one-way terminal processing_status).
-- No public-schema mutation. No SECURITY DEFINER. Zero runtime grants (dark).
BEGIN;

-- ============================================================================
-- (1) pms_interface_runtime — durable connector runtime state, FOUR axes.
--     One row per (tenant,site,pms_interface_id). A derived overall status MAY be
--     exposed but is NOT the stored replacement for the four independent axes.
-- ============================================================================
CREATE TABLE iam_v2.pms_interface_runtime (
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  pms_interface_id uuid NOT NULL,
  pinned_revision_id uuid,                                 -- revision the connector opened with
  runtime_generation bigint NOT NULL DEFAULT 0,            -- ++ each connector (re)start (single-owner)
  updated_at timestamptz NOT NULL DEFAULT now(),

  -- axis 1: transport / heartbeat health
  transport_status text NOT NULL DEFAULT 'UNKNOWN'
    CHECK (transport_status IN ('UNKNOWN','CONNECTING','CONNECTED','DISCONNECTED','ERROR')),
  last_connect_attempt_at timestamptz,
  last_connected_at timestamptz,
  last_heartbeat_at timestamptz,
  disconnected_since timestamptz,
  transport_error_code text,                              -- sanitized code only; never credentials/payload

  -- axis 2: feed-continuity health
  continuity_status text NOT NULL DEFAULT 'UNKNOWN'
    CHECK (continuity_status IN ('UNKNOWN','CONTINUOUS','DISCONTINUOUS','GAP_DETECTED')),
  last_valid_event_at timestamptz,
  last_event_cursor text,                                 -- protocol sequence/cursor where supported
  discontinuity_detected_at timestamptz,
  last_resync_marker_at timestamptz,                      -- observed resync / night-audit marker

  -- axis 3: last complete synchronization / resync health
  sync_status text NOT NULL DEFAULT 'UNKNOWN'
    CHECK (sync_status IN ('UNKNOWN','IN_SYNC','RESYNC_REQUIRED','RESYNC_IN_PROGRESS','SYNC_FAILED')),
  resync_requested_at timestamptz,
  resync_started_at timestamptz,
  last_complete_sync_at timestamptz,
  sync_cursor text,                                       -- durable checkpoint
  last_sync_failure_code text,

  -- derived overall (NON-authoritative convenience for UI/resolver; not a replacement for the four axes)
  derived_freshness text NOT NULL DEFAULT 'UNAVAILABLE'
    CHECK (derived_freshness IN ('HEALTHY','UNAVAILABLE','STALE','DEGRADED_FRESHNESS','RESYNC_REQUIRED','UNSUPPORTED_EVIDENCE')),

  PRIMARY KEY (tenant_id, site_id, pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id)
    REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, pinned_revision_id)
    REFERENCES iam_v2.pms_interface_revisions (tenant_id, site_id, pms_interface_id, id)
);
CREATE INDEX pms_interface_runtime_fresh
  ON iam_v2.pms_interface_runtime (tenant_id, site_id, derived_freshness);

-- ============================================================================
-- (4) stays: effective checkout boundary + per-Stay occupancy-evidence (occupancy axis).
--     A Stay's freshness is never inferred only from the Interface's last event time.
-- ============================================================================
ALTER TABLE iam_v2.stays
  ADD COLUMN effective_checkout_at timestamptz,
  ADD COLUMN occupancy_evidence_at timestamptz,            -- source ts of the occupancy evidence authoritative for this Stay
  ADD COLUMN occupancy_ingested_at timestamptz,            -- when that evidence was ingested
  ADD COLUMN occupancy_revision_id uuid,                   -- pinned interface revision for that evidence
  ADD COLUMN occupancy_normalization_version int,
  ADD COLUMN occupancy_clock_suspect boolean;
-- effective_checkout_at is meaningful only once the Stay has left IN_HOUSE; reinstatement (->IN_HOUSE)
-- must clear it (new episode). Enforced structurally:
ALTER TABLE iam_v2.stays
  ADD CONSTRAINT stays_effco_only_after_checkout
    CHECK (effective_checkout_at IS NULL OR status IN ('CHECKED_OUT','POST_STAY_ACTIVE'));
CREATE INDEX stays_effective_checkout
  ON iam_v2.stays (tenant_id, site_id, pms_interface_id, effective_checkout_at)
  WHERE effective_checkout_at IS NOT NULL;

-- ============================================================================
-- (5) auth_resolutions: server-side resolution-request idempotency key (replay-safe).
--     Nullable for backward compatibility; Phase-3 resolver writes always supply it.
-- ============================================================================
ALTER TABLE iam_v2.auth_resolutions
  ADD COLUMN resolution_request_id uuid;
CREATE UNIQUE INDEX auth_resolutions_req_idem
  ON iam_v2.auth_resolutions (tenant_id, site_id, resolution_request_id)
  WHERE resolution_request_id IS NOT NULL;

-- ============================================================================
-- (4b) site_checkout_grace_config: typed grace scalars with validated bounds.
-- ============================================================================
ALTER TABLE iam_v2.site_checkout_grace_config
  ADD COLUMN eligibility_window_seconds int NOT NULL DEFAULT 86400,   -- 24h default
  ADD COLUMN grace_duration_seconds int,
  ADD COLUMN grace_down_kbps int,
  ADD COLUMN grace_up_kbps int,
  ADD COLUMN grace_data_quota_mb int,
  ADD COLUMN grace_device_limit int,
  ADD COLUMN grace_new_device_policy text
    CHECK (grace_new_device_policy IS NULL OR grace_new_device_policy IN ('REJECT_NEW','ALLOW_UNTIL_LIMIT'));
ALTER TABLE iam_v2.site_checkout_grace_config
  ADD CONSTRAINT grace_bounds CHECK (
        eligibility_window_seconds > 0 AND eligibility_window_seconds <= 604800
    AND (grace_duration_seconds IS NULL OR (grace_duration_seconds > 0 AND grace_duration_seconds <= 604800))
    AND (grace_down_kbps      IS NULL OR (grace_down_kbps      > 0 AND grace_down_kbps      <= 10000000))
    AND (grace_up_kbps        IS NULL OR (grace_up_kbps        > 0 AND grace_up_kbps        <= 10000000))
    AND (grace_data_quota_mb  IS NULL OR (grace_data_quota_mb  > 0 AND grace_data_quota_mb  <= 1048576))
    AND (grace_device_limit   IS NULL OR (grace_device_limit   > 0 AND grace_device_limit   <= 1000)));

-- ============================================================================
-- (6) stay_events append-only guard: immutable identity/normalization; one-way terminal status.
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p3_stay_event_appendonly() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'stay_events is append-only (DELETE rejected)';
  END IF;
  IF   NEW.tenant_id             IS DISTINCT FROM OLD.tenant_id
    OR NEW.site_id               IS DISTINCT FROM OLD.site_id
    OR NEW.pms_interface_id      IS DISTINCT FROM OLD.pms_interface_id
    OR NEW.external_event_identity IS DISTINCT FROM OLD.external_event_identity
    OR NEW.event_type            IS DISTINCT FROM OLD.event_type
    OR NEW.pms_timestamp_raw     IS DISTINCT FROM OLD.pms_timestamp_raw
    OR NEW.pms_timestamp_utc     IS DISTINCT FROM OLD.pms_timestamp_utc
    OR NEW.source_timezone       IS DISTINCT FROM OLD.source_timezone
    OR NEW.sequence_version      IS DISTINCT FROM OLD.sequence_version
    OR NEW.normalization_version IS DISTINCT FROM OLD.normalization_version
    OR NEW.clock_suspect         IS DISTINCT FROM OLD.clock_suspect
    OR NEW.payload               IS DISTINCT FROM OLD.payload
  THEN
    RAISE EXCEPTION 'stay_events identity/normalization columns are immutable (append-only)';
  END IF;
  -- processing_status: one-way, terminal, only from PENDING
  IF OLD.processing_status <> NEW.processing_status THEN
    IF OLD.processing_status <> 'PENDING' THEN
      RAISE EXCEPTION 'stay_events.processing_status is terminal (% cannot change)', OLD.processing_status;
    END IF;
    IF NEW.processing_status NOT IN ('APPLIED','SKIPPED_DUPLICATE','MANUAL_REVIEW','FAILED') THEN
      RAISE EXCEPTION 'invalid stay_events.processing_status transition to %', NEW.processing_status;
    END IF;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_stay_event_guard
  BEFORE UPDATE OR DELETE ON iam_v2.stay_events
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_stay_event_appendonly();

-- ============================================================================
-- (2) stays one-way status transition + monotonic version guard.
--     Reinstatement (CHECKED_OUT->IN_HOUSE) requires lifecycle_version to increment
--     exactly once in the SAME update (proof of a trusted reinstatement, not a bare version bump).
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p3_stay_lifecycle_guard() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
DECLARE allowed boolean;
BEGIN
  IF NEW.last_applied_event_version < OLD.last_applied_event_version THEN
    RAISE EXCEPTION 'stays.last_applied_event_version cannot decrease (% -> %)',
      OLD.last_applied_event_version, NEW.last_applied_event_version;
  END IF;
  IF NEW.lifecycle_version < OLD.lifecycle_version THEN
    RAISE EXCEPTION 'stays.lifecycle_version cannot decrease (% -> %)', OLD.lifecycle_version, NEW.lifecycle_version;
  END IF;
  IF NEW.lifecycle_version > OLD.lifecycle_version + 1 THEN
    RAISE EXCEPTION 'stays.lifecycle_version may increase by at most 1 per update (% -> %)',
      OLD.lifecycle_version, NEW.lifecycle_version;
  END IF;
  IF NEW.status <> OLD.status THEN
    allowed := CASE
      WHEN OLD.status='RESERVED'         AND NEW.status IN ('IN_HOUSE','CANCELLED','NO_SHOW') THEN true
      WHEN OLD.status='IN_HOUSE'         AND NEW.status IN ('CHECKED_OUT')                    THEN true
      WHEN OLD.status='CHECKED_OUT'      AND NEW.status IN ('IN_HOUSE','POST_STAY_ACTIVE')    THEN true
      WHEN OLD.status='POST_STAY_ACTIVE' AND NEW.status IN ('CHECKED_OUT')                    THEN true
      ELSE false END;
    IF NOT allowed THEN
      RAISE EXCEPTION 'illegal stays.status transition % -> %', OLD.status, NEW.status;
    END IF;
    IF OLD.status='CHECKED_OUT' AND NEW.status='IN_HOUSE'
       AND NEW.lifecycle_version <> OLD.lifecycle_version + 1 THEN
      RAISE EXCEPTION 'reinstatement (CHECKED_OUT->IN_HOUSE) must increment lifecycle_version exactly once';
    END IF;
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p3_stay_lifecycle_guard
  BEFORE UPDATE ON iam_v2.stays
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_stay_lifecycle_guard();

COMMIT;
