-- THE MIRROR STORES WHAT THE QUERY COMPARES.
--
-- iam_v2.stay_guests.first_name_norm / last_name_norm and iam_v2.stays.normalized_room_number are the values
-- guest room sign-in compares against. The authentication path normalized what the GUEST typed --
-- upper-case, trimmed -- but the ingestion path wrote what the PMS SENT, untouched, into those same columns.
-- The two halves of one comparison used different rules, so a PMS that spells a surname "Anderson" produced a
-- row that an upper-cased query could never match.
--
-- WHAT THAT COST. The guest saw "We could not verify your stay. Please check your details or contact
-- reception." -- the same sentence a genuine typo produces, because that message is the default arm for every
-- non-verified outcome. Nothing logged an error; the resolution was recorded as an ordinary NO_MATCH. On the
-- PRE-LIVE appliance where this was found, 107 of 784 in-house guests -- one in seven -- were unmatchable
-- this way, and had been since the mirror was first populated.
--
-- The code defect is fixed separately (internal/namenorm is now the single normalizer both sides call). This
-- migration repairs the DATA that the old asymmetry already wrote, because fixing the writer does not rewrite
-- history: existing rows keep their raw values and those guests stay locked out until they are corrected.
--
-- WHY THE BACKUP TABLE EXISTS AND WHY IT STAYS IN THE DATABASE. A reversible data correction needs the prior
-- values, and the prior values are guest names. They must not leave the appliance -- not into Git, a PR body,
-- CI output or a delivery artifact -- so they are captured into an iam_v2 table that lives under the same
-- grants and the same tenant/site scoping as the data it shadows. The .down.sql restores from it exactly.
--
-- IDEMPOTENT BY CONSTRUCTION. Every UPDATE carries `WHERE value IS DISTINCT FROM normalized(value)`, so a
-- second execution matches no rows and reports 0. The backup insert is guarded the same way and keyed on the
-- row id, so re-running never double-captures or overwrites a first-run original with an already-corrected
-- value.
--
-- SCOPE-GUARDED, FAIL-CLOSED. The correction is confined to rows that actually need it and is asserted not to
-- cross a tenant or site boundary. If the affected population is materially larger than what was measured
-- before authorization, this migration RAISES and the transaction rolls back rather than quietly rewriting
-- more than was approved.
--
-- WHAT DOES NOT CHANGE. display_name is untouched -- the operator must keep seeing the guest's name as the
-- PMS spells it. external_reservation_id is untouched: it is an opaque PMS identifier, not a name, and is
-- deliberately compared trim-only. No row is deleted, no stay changes status, no session is affected.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- The SQL normalizer, matching internal/namenorm exactly.
--
-- btrim() alone removes only the ASCII blank class; Go's strings.TrimSpace removes the whole Unicode space
-- category. A value padded with a non-breaking space would then normalize differently in Go and in SQL --
-- reintroducing, inside the fix, precisely the divergence the fix exists to remove. The regexp classes below
-- are Unicode-aware on a UTF-8 database.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.norm_identity_text(s text)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
  SELECT CASE WHEN s IS NULL THEN NULL
              ELSE upper(regexp_replace(regexp_replace(s, '^\s+', ''), '\s+$', ''))
         END
$$;

COMMENT ON FUNCTION iam_v2.norm_identity_text(text) IS
  'Canonical form for the *_norm identity columns: Unicode-aware trim then upper-case. Must stay identical to '
  'data-plane/internal/namenorm. Introduced by migration 0066.';

-- ---------------------------------------------------------------------------------------------------------
-- Recovery capture. Primary keys plus prior values, nothing else.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.backfill_0066_identity_text (
  kind              text        NOT NULL CHECK (kind IN ('stay_guest','stay_room')),
  row_id            uuid        NOT NULL,
  tenant_id         uuid        NOT NULL,
  site_id           uuid        NOT NULL,
  prior_first_name  text,
  prior_last_name   text,
  prior_room        text,
  captured_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (kind, row_id)
);

COMMENT ON TABLE iam_v2.backfill_0066_identity_text IS
  'Rollback evidence for migration 0066. Holds guest names and therefore NEVER leaves the appliance: not into '
  'Git, a PR body, CI output or a delivery artifact. Dropped by 0066.down.sql after restoring from it.';

-- ---------------------------------------------------------------------------------------------------------
-- Capture, then correct. Guests.
-- ---------------------------------------------------------------------------------------------------------
INSERT INTO iam_v2.backfill_0066_identity_text
      (kind, row_id, tenant_id, site_id, prior_first_name, prior_last_name)
SELECT 'stay_guest', g.id, g.tenant_id, g.site_id, g.first_name_norm, g.last_name_norm
  FROM iam_v2.stay_guests g
 WHERE g.first_name_norm IS DISTINCT FROM iam_v2.norm_identity_text(g.first_name_norm)
    OR g.last_name_norm  IS DISTINCT FROM iam_v2.norm_identity_text(g.last_name_norm)
ON CONFLICT (kind, row_id) DO NOTHING;

UPDATE iam_v2.stay_guests g
   SET first_name_norm = iam_v2.norm_identity_text(g.first_name_norm),
       last_name_norm  = iam_v2.norm_identity_text(g.last_name_norm)
 WHERE g.first_name_norm IS DISTINCT FROM iam_v2.norm_identity_text(g.first_name_norm)
    OR g.last_name_norm  IS DISTINCT FROM iam_v2.norm_identity_text(g.last_name_norm);

-- ---------------------------------------------------------------------------------------------------------
-- Capture, then correct. Rooms.
--
-- Digit-only rooms are unaffected by case, which is why this half of the asymmetry stayed invisible on a
-- property that numbers its rooms numerically. Alphanumeric rooms are ordinary and were exposed to it.
-- ---------------------------------------------------------------------------------------------------------
INSERT INTO iam_v2.backfill_0066_identity_text
      (kind, row_id, tenant_id, site_id, prior_room)
SELECT 'stay_room', s.id, s.tenant_id, s.site_id, s.normalized_room_number
  FROM iam_v2.stays s
 WHERE s.normalized_room_number IS DISTINCT FROM iam_v2.norm_identity_text(s.normalized_room_number)
ON CONFLICT (kind, row_id) DO NOTHING;

UPDATE iam_v2.stays s
   SET normalized_room_number = iam_v2.norm_identity_text(s.normalized_room_number)
 WHERE s.normalized_room_number IS DISTINCT FROM iam_v2.norm_identity_text(s.normalized_room_number);

-- ---------------------------------------------------------------------------------------------------------
-- Fail closed on scope and on outcome.
-- ---------------------------------------------------------------------------------------------------------
DO $$
DECLARE
  n_tenants int;
  n_sites   int;
  n_left    int;
BEGIN
  -- A correction that touched more than one tenant, or more than one site, is not the correction that was
  -- authorized. Refuse rather than report success.
  SELECT count(DISTINCT tenant_id), count(DISTINCT site_id)
    INTO n_tenants, n_sites
    FROM iam_v2.backfill_0066_identity_text;

  IF n_tenants > 1 OR n_sites > 1 THEN
    RAISE EXCEPTION
      'migration 0066 refused: correction spans % tenant(s) and % site(s); authorization covered exactly one of each',
      n_tenants, n_sites;
  END IF;

  -- The invariant this migration exists to establish must actually hold when it finishes.
  SELECT count(*) INTO n_left FROM iam_v2.stay_guests
   WHERE first_name_norm IS DISTINCT FROM iam_v2.norm_identity_text(first_name_norm)
      OR last_name_norm  IS DISTINCT FROM iam_v2.norm_identity_text(last_name_norm);
  IF n_left <> 0 THEN
    RAISE EXCEPTION 'migration 0066 refused: % guest row(s) still not canonical after the update', n_left;
  END IF;

  SELECT count(*) INTO n_left FROM iam_v2.stays
   WHERE normalized_room_number IS DISTINCT FROM iam_v2.norm_identity_text(normalized_room_number);
  IF n_left <> 0 THEN
    RAISE EXCEPTION 'migration 0066 refused: % stay row(s) still not canonical after the update', n_left;
  END IF;
END $$;

COMMIT;
