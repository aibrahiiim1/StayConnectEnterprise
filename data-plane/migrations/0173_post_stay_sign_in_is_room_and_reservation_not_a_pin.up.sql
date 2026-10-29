-- 0173 -- POST-STAY SIGN-IN IS THE ROOM AND THE RESERVATION, NOT A PIN (audit decision AD-36, replaced by the
-- Product Owner: the Post-Stay PIN contract is replaced by room number + reservation number, bound to the original stay).
--
-- WHAT CHANGES. A guest who has checked out comes back with the room number and the reservation number of the stay they
-- left. They are checked against THAT stay's own record (never searched for among other guests' stays from the typed
-- values alone): the stays this device was authorised on are the only candidates, and a device with no such stay
-- (a new device) must also give the last name on the stay. No PIN is issued, shown, reset or verified any more.
--
-- WHAT THIS MIGRATION DOES, AND DELIBERATELY DOES NOT:
--   * iam_v2.post_stay_profiles stays the per-stay-episode identity the auth context and the conversion are bound to
--     (UNIQUE(origin_stay_id, origin_lifecycle_version) is still what makes it unusable by the next occupant). A profile
--     is now created on the first successful room + reservation check, not handed out while the guest was in house.
--   * pin_hash becomes NULLABLE: a profile created by the new route has no PIN. NOTHING IS DROPPED OR REWRITTEN.
--     Every existing profile keeps its pin_hash, pin_generation, pin_revealed_at, issued_via, issued_by_operator and
--     revoke columns exactly as they are: they are the record of who was allowed post-stay access and on whose
--     authority (the table's own comment says rows are never deleted). No code verifies a PIN any more.
--   * issued_via gains 'ROOM_RESERVATION' (no operator). The two historical values stay valid.
--   * A profile whose PIN was issued but never used remains usable through the new route while it is within validity,
--     because it is bound to the same origin stay.
--
-- Additive: one constraint is widened, one NOT NULL is lifted. No existing row is changed.

BEGIN;

ALTER TABLE iam_v2.post_stay_profiles ALTER COLUMN pin_hash DROP NOT NULL;

ALTER TABLE iam_v2.post_stay_profiles DROP CONSTRAINT IF EXISTS post_stay_profiles_issued_via_check;
ALTER TABLE iam_v2.post_stay_profiles ADD CONSTRAINT post_stay_profiles_issued_via_check
  CHECK (issued_via IN ('GUEST_AUTHENTICATED_SESSION','OPERATOR_RESET','ROOM_RESERVATION'));

ALTER TABLE iam_v2.post_stay_profiles DROP CONSTRAINT IF EXISTS psp_issuer_coherent;
ALTER TABLE iam_v2.post_stay_profiles ADD CONSTRAINT psp_issuer_coherent CHECK (
      (issued_via = 'OPERATOR_RESET'              AND issued_by_operator IS NOT NULL)
   OR (issued_via = 'GUEST_AUTHENTICATED_SESSION' AND issued_by_operator IS NULL)
   OR (issued_via = 'ROOM_RESERVATION'            AND issued_by_operator IS NULL AND pin_hash IS NULL));

COMMENT ON COLUMN iam_v2.post_stay_profiles.pin_hash IS
  'HISTORICAL (0173). A PIN issued before the post-stay sign-in became room + reservation. Never verified and never '
  'written again; kept as the record of that issuance. NULL on a profile created by the room + reservation route.';

COMMENT ON TABLE iam_v2.post_stay_profiles IS
  'The post-stay identity of one stay episode. Created on the first successful room + reservation check for the stay '
  '(0173); older rows were created with a PIN (see pin_hash). Rows are never deleted: revoke instead.';

-- The profile guard: a PIN-less profile needs no reveal, and a PIN can never be set, changed or re-generated again.
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
    IF NEW.pin_hash IS NULL THEN
      -- 0173: the room + reservation route. There is no PIN, so there is nothing to reveal.
      IF NEW.pin_revealed_at IS NOT NULL THEN
        RAISE EXCEPTION 'a post-stay profile without a PIN has nothing to reveal';
      END IF;
    ELSIF NEW.pin_revealed_at IS NULL THEN
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

  IF NEW.pin_hash IS DISTINCT FROM OLD.pin_hash OR NEW.pin_generation IS DISTINCT FROM OLD.pin_generation THEN
    -- 0173: post-stay sign-in is room + reservation. A PIN is not issued or reset any more, and an existing one is
    -- historical evidence that nothing may rewrite.
    RAISE EXCEPTION 'post-stay PINs are no longer issued or reset; the PIN columns are historical';
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
