-- 0010 DOWN — reverse of 0010_phase3_stay_resolution.up.sql. Additive rollback. Drops in reverse
-- dependency order: ledger row + triggers/functions first, then indexes/constraints, then added
-- columns, then the runtime table last. DARK; no Production data.
BEGIN;

DELETE FROM public.schema_migrations WHERE version = '0010_phase3_stay_resolution';

-- (2)+(6) triggers + functions first
DROP TRIGGER IF EXISTS p3_stay_lifecycle_guard ON iam_v2.stays;
DROP FUNCTION IF EXISTS iam_v2.p3_stay_lifecycle_guard();
DROP TRIGGER IF EXISTS p3_stay_event_guard ON iam_v2.stay_events;
DROP FUNCTION IF EXISTS iam_v2.p3_stay_event_appendonly();

-- (6b) §G durable resync inbox: partial indexes + admission columns, then restore the baseline unique.
DROP INDEX IF EXISTS iam_v2.se_resync_identity;
DROP INDEX IF EXISTS iam_v2.se_live_identity;
ALTER TABLE iam_v2.stay_events DROP CONSTRAINT IF EXISTS se_admission_coherent;
ALTER TABLE iam_v2.stay_events
  DROP COLUMN IF EXISTS fingerprint_key_version,
  DROP COLUMN IF EXISTS resync_generation,
  DROP COLUMN IF EXISTS admission_runtime_generation,
  DROP COLUMN IF EXISTS admission_kind;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint
                 WHERE conrelid = 'iam_v2.stay_events'::regclass AND contype = 'u'
                   AND pg_get_constraintdef(oid) LIKE '%external_event_identity%') THEN
    -- restore the EXACT baseline auto-generated constraint name so the rolled-back catalog matches pre-0010
    ALTER TABLE iam_v2.stay_events
      ADD CONSTRAINT stay_events_tenant_id_site_id_pms_interface_id_external_eve_key
      UNIQUE (tenant_id, site_id, pms_interface_id, external_event_identity);
  END IF;
END$$;

-- (6) stay_events application-result columns
ALTER TABLE iam_v2.stay_events
  DROP COLUMN IF EXISTS review_code,
  DROP COLUMN IF EXISTS processed_at;

-- (4b) grace scalars: constraints then columns
ALTER TABLE iam_v2.site_checkout_grace_config DROP CONSTRAINT IF EXISTS grace_config_no_dup_policy_keys;
ALTER TABLE iam_v2.site_checkout_grace_config DROP CONSTRAINT IF EXISTS grace_all_or_none;
ALTER TABLE iam_v2.site_checkout_grace_config DROP CONSTRAINT IF EXISTS grace_bounds;
ALTER TABLE iam_v2.site_checkout_grace_config
  DROP COLUMN IF EXISTS grace_device_limit_policy,
  DROP COLUMN IF EXISTS grace_device_limit,
  DROP COLUMN IF EXISTS grace_data_quota_bytes,
  DROP COLUMN IF EXISTS grace_up_kbps,
  DROP COLUMN IF EXISTS grace_down_kbps,
  DROP COLUMN IF EXISTS grace_duration_seconds,
  DROP COLUMN IF EXISTS eligibility_window_seconds;

-- (5b) auth_contexts occupancy-evidence + episode pins
ALTER TABLE iam_v2.auth_contexts
  DROP COLUMN IF EXISTS pinned_occupancy_evidence_version,
  DROP COLUMN IF EXISTS pinned_lifecycle_version;

-- (5) auth_resolutions idempotency key: index then column
DROP INDEX IF EXISTS iam_v2.auth_resolutions_req_idem;
ALTER TABLE iam_v2.auth_resolutions DROP COLUMN IF EXISTS resolution_request_id;

-- (4) stays effective-checkout + occupancy evidence: index + constraints then columns
DROP INDEX IF EXISTS iam_v2.stays_effective_checkout;
ALTER TABLE iam_v2.stays
  DROP CONSTRAINT IF EXISTS stays_occupancy_revision_fk,
  DROP CONSTRAINT IF EXISTS stays_occupancy_norm_pos,
  DROP CONSTRAINT IF EXISTS stays_occupancy_all_or_none,
  DROP CONSTRAINT IF EXISTS stays_effco_only_after_checkout;
ALTER TABLE iam_v2.stays
  DROP COLUMN IF EXISTS occupancy_evidence_version,
  DROP COLUMN IF EXISTS occupancy_clock_suspect,
  DROP COLUMN IF EXISTS occupancy_normalization_version,
  DROP COLUMN IF EXISTS occupancy_revision_id,
  DROP COLUMN IF EXISTS occupancy_ingested_at,
  DROP COLUMN IF EXISTS occupancy_evidence_at,
  DROP COLUMN IF EXISTS effective_checkout_at;

-- (1) runtime table last (its index + constraints drop with it)
DROP TABLE IF EXISTS iam_v2.pms_interface_runtime;

COMMIT;
