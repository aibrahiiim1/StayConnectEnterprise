-- ============================================================================================================
-- PHASE 5 — ONE REVEAL PER GENERATION, AND IT HAPPENS AT MINT.
--
-- 0027 refused a profile created with pin_revealed_at set, on the assumption that revealing was a separate,
-- later act. Implementing the issuance path showed that assumption is wrong, and wrong in a way that matters:
-- the plaintext PIN exists ONLY in the response to the call that minted it. It is never stored and cannot be
-- re-derived from an argon2id hash, so there is no later moment at which anything could be revealed. A
-- separate reveal operation could only exist if the plaintext were kept, which is the thing this design most
-- needs not to do.
--
-- So the invariant is restated as what it actually is: pin_revealed_at is set EXACTLY ONCE PER GENERATION, at
-- the moment that generation is minted. That is strictly stronger than 0027's rule — it now refuses a profile
-- created WITHOUT a reveal (a PIN nobody was ever shown, which would be an unusable credential sitting in the
-- table), as well as a second reveal of a generation, which 0027 already refused and this keeps.
--
-- Rewritten as a whole function rather than patched, so the rule can be read in one place.
-- ============================================================================================================
BEGIN;

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

INSERT INTO public.schema_migrations (version)
  VALUES ('0029_phase5_reveal_is_at_mint') ON CONFLICT DO NOTHING;
COMMIT;
