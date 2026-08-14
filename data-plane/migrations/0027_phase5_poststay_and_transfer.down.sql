-- ============================================================================================================
-- PHASE 5 — MILESTONE 1 ROLLBACK.
--
-- This reverses everything 0027 added and RESTORES p3_stay_lifecycle_guard to its Phase-3 body, including the
-- refusal message that names Phase 5. Rolling back Phase 5 must leave the Phase-3 guard exactly as Phase 3
-- accepted it — a rollback that left the post-stay arm in place would be a rollback in name only.
--
-- Note on fingerprints: a DOWN → UP cycle does not free the dropped columns' attribute slots, so the CATALOG
-- fingerprint (which includes ordinal_position) legitimately changes while the schema is identical. Verify a
-- rollback with iam_v2_scratch/schema_structure_fingerprint.sql, which answers the question actually being
-- asked here.
-- ============================================================================================================
BEGIN;

DROP TRIGGER IF EXISTS p5_stay_link_guard              ON iam_v2.stay_links;
DROP TRIGGER IF EXISTS p5_stay_link_controlled_writer  ON iam_v2.stay_links;
DROP TRIGGER IF EXISTS p5_entitlement_transfer_guard             ON iam_v2.entitlement_transfers;
DROP TRIGGER IF EXISTS p5_entitlement_transfer_controlled_writer ON iam_v2.entitlement_transfers;
DROP TRIGGER IF EXISTS p5_post_stay_profile_guard             ON iam_v2.post_stay_profiles;
DROP TRIGGER IF EXISTS p5_post_stay_profile_controlled_writer ON iam_v2.post_stay_profiles;

DROP FUNCTION IF EXISTS iam_v2.p5_stay_link_guard();
DROP FUNCTION IF EXISTS iam_v2.p5_entitlement_transfer_guard();
DROP FUNCTION IF EXISTS iam_v2.p5_post_stay_authenticable(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p5_post_stay_profile_guard();
DROP FUNCTION IF EXISTS iam_v2.p5_controlled_writer_only();
DROP FUNCTION IF EXISTS iam_v2.p5_controlled_operation_open(text);
DROP FUNCTION IF EXISTS iam_v2.p5_begin_controlled_operation(text);

ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_post_stay_pins;

DROP INDEX IF EXISTS iam_v2.post_stay_profiles_origin;
ALTER TABLE iam_v2.post_stay_profiles
  DROP CONSTRAINT IF EXISTS psp_issuer_coherent,
  DROP CONSTRAINT IF EXISTS psp_validity_window,
  DROP CONSTRAINT IF EXISTS psp_revoked_coherent,
  DROP COLUMN IF EXISTS issued_at,
  DROP COLUMN IF EXISTS issued_by_operator,
  DROP COLUMN IF EXISTS issued_via,
  DROP COLUMN IF EXISTS revoke_reason,
  DROP COLUMN IF EXISTS revoked_by,
  DROP COLUMN IF EXISTS revoked_at,
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS valid_until,
  DROP COLUMN IF EXISTS pin_revealed_at,
  DROP COLUMN IF EXISTS pin_set_at,
  DROP COLUMN IF EXISTS pin_generation,
  DROP COLUMN IF EXISTS pin_hash,
  DROP COLUMN IF EXISTS created_at;

-- Restore the Phase-3 lifecycle guard EXACTLY as Phase 3 accepted it (0010, section 2).
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
REVOKE EXECUTE ON FUNCTION iam_v2.p3_stay_lifecycle_guard() FROM PUBLIC;

DELETE FROM public.schema_migrations WHERE version = '0027_phase5_poststay_and_transfer';
COMMIT;
