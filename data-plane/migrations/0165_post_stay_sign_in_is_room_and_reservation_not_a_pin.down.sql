-- Down for 0165. Profiles created by the room + reservation route have no PIN and cannot satisfy the earlier NOT NULL
-- and issuer constraints, so they are refused here rather than rewritten: the rollback is taken only when none exist
-- (their record is the audit of post-stay access; it is not dropped to make a rollback pass).
BEGIN;
DO $d$
BEGIN
  IF EXISTS (SELECT 1 FROM iam_v2.post_stay_profiles WHERE issued_via = 'ROOM_RESERVATION' OR pin_hash IS NULL) THEN
    RAISE EXCEPTION '0165 down refused: post-stay profiles created by the room + reservation route exist; revoke or retain them and roll back deliberately';
  END IF;
END $d$;
ALTER TABLE iam_v2.post_stay_profiles DROP CONSTRAINT IF EXISTS psp_issuer_coherent;
ALTER TABLE iam_v2.post_stay_profiles ADD CONSTRAINT psp_issuer_coherent CHECK (
      (issued_via = 'OPERATOR_RESET'              AND issued_by_operator IS NOT NULL)
   OR (issued_via = 'GUEST_AUTHENTICATED_SESSION' AND issued_by_operator IS NULL));
ALTER TABLE iam_v2.post_stay_profiles DROP CONSTRAINT IF EXISTS post_stay_profiles_issued_via_check;
ALTER TABLE iam_v2.post_stay_profiles ADD CONSTRAINT post_stay_profiles_issued_via_check
  CHECK (issued_via IN ('GUEST_AUTHENTICATED_SESSION','OPERATOR_RESET'));
ALTER TABLE iam_v2.post_stay_profiles ALTER COLUMN pin_hash SET NOT NULL;
COMMENT ON COLUMN iam_v2.post_stay_profiles.pin_hash IS NULL;

CREATE OR REPLACE FUNCTION iam_v2.p5_post_stay_profile_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp
  AS $fn$
DECLARE v_status text; v_lifecycle int;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'post_stay_profiles rows are never deleted; revoke the profile instead';
  END IF;

  IF TG_OP = 'INSERT' THEN
    SELECT s.status, s.lifecycle_version INTO v_status, v_lifecycle
      FROM iam_v2.stays s
     WHERE s.tenant_id = NEW.tenant_id AND s.site_id = NEW.site_id AND s.id = NEW.origin_stay_id
     FOR UPDATE;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'post-stay profile origin Stay % is not in scope (fail closed)', NEW.origin_stay_id;
    END IF;
    IF v_lifecycle <> NEW.origin_lifecycle_version THEN
      RAISE EXCEPTION 'post-stay profile must name the CURRENT Stay episode (stay is at lifecycle_version %, profile names %)',
        v_lifecycle, NEW.origin_lifecycle_version;
    END IF;
    IF v_status NOT IN ('IN_HOUSE','CHECKED_OUT') THEN
      RAISE EXCEPTION 'post-stay profile may only be issued from an IN_HOUSE or CHECKED_OUT Stay (stay is %)', v_status;
    END IF;
    -- The minting call returned the plaintext, so the reveal happened. A profile inserted WITHOUT one would
    -- be a credential nobody has ever seen and nobody can ever be given: unusable, and indistinguishable
    -- afterwards from one whose reveal was lost.
    IF NEW.pin_revealed_at IS NULL THEN
      RAISE EXCEPTION 'a post-stay profile records its one-time reveal at mint; the plaintext exists only in that response';
    END IF;
    RETURN NEW;
  END IF;

  -- UPDATE. Identity and lineage are immutable.
  IF NEW.id                          IS DISTINCT FROM OLD.id
     OR NEW.tenant_id                IS DISTINCT FROM OLD.tenant_id
     OR NEW.site_id                  IS DISTINCT FROM OLD.site_id
     OR NEW.origin_stay_id           IS DISTINCT FROM OLD.origin_stay_id
     OR NEW.origin_lifecycle_version IS DISTINCT FROM OLD.origin_lifecycle_version
     OR NEW.created_at               IS DISTINCT FROM OLD.created_at
     OR NEW.issued_at                IS DISTINCT FROM OLD.issued_at THEN
    RAISE EXCEPTION 'post-stay profile identity and origin lineage are read-only';
  END IF;
  IF OLD.status = 'REVOKED' AND NEW.status <> 'REVOKED' THEN
    RAISE EXCEPTION 'a revoked post-stay profile is never reactivated (issue a new one)';
  END IF;

  IF NEW.pin_hash IS DISTINCT FROM OLD.pin_hash THEN
    -- A NEW GENERATION. It is a different secret, minted and returned by this same statement, so it carries
    -- its OWN reveal. Inheriting the previous generation's timestamp would silently consume the new secret's
    -- one-time reveal and leave no record that it was shown.
    IF NEW.pin_generation <> OLD.pin_generation + 1 THEN
      RAISE EXCEPTION 'a new post-stay PIN must increment pin_generation by exactly 1 (% -> %)',
        OLD.pin_generation, NEW.pin_generation;
    END IF;
    IF NEW.pin_revealed_at IS NULL OR NEW.pin_revealed_at IS NOT DISTINCT FROM OLD.pin_revealed_at THEN
      RAISE EXCEPTION 'a newly minted post-stay PIN records its own reveal';
    END IF;
  ELSE
    IF NEW.pin_generation IS DISTINCT FROM OLD.pin_generation THEN
      RAISE EXCEPTION 'pin_generation may not change without a new PIN';
    END IF;
    -- SAME generation: the reveal already happened and cannot happen again. This is the rule that makes
    -- "show me that PIN again" impossible rather than merely unimplemented.
    IF NEW.pin_revealed_at IS DISTINCT FROM OLD.pin_revealed_at THEN
      RAISE EXCEPTION 'a post-stay PIN is revealed exactly once per generation';
    END IF;
  END IF;
  RETURN NEW;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p5_post_stay_profile_guard() FROM PUBLIC;
COMMIT;
