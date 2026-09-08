-- THE ALLOWANCE A STAY EARNED IS FROZEN WHEN IT IS GRANTED.
--
-- A data allowance has always come from the pinned Service Plan revision, so every guest of a package gets
-- the same number of bytes however long they are staying. PER_STAY_NIGHT lets a package derive the allowance
-- from the length of the stay instead, between a floor and a ceiling.
--
-- That introduces something the schema has never had to hold: a quota that is DERIVED. A derived number that
-- is re-derived on every read is not a grant -- it is a promise that changes when the PMS changes its mind.
-- Extend a stay by a week and yesterday's guest would retroactively acquire more bytes; shorten it and a guest
-- already online would find their allowance had shrunk beneath them, possibly below what they had already
-- spent, terminating them for crossing a line that moved. So the computed value is SNAPSHOTTED onto the
-- entitlement at grant time and never recomputed.
--
-- TWO COLUMNS, AND WHAT THEIR NULLS MEAN:
--
--   internet_package_revisions.data_allocation_policy -- NULL means FIXED. Every revision published before
--     today carries NULL and therefore keeps granting exactly what it granted yesterday.
--
--   entitlements.data_quota_bytes -- NULL means "this entitlement's allowance is the plan revision's", which
--     is what every entitlement in existence means today. Nothing is backfilled, so no historical entitlement
--     changes by one byte.
--
-- Both nulls are chosen so that this migration, applied to a live database, changes NO existing behaviour.
--
-- WHAT THIS DOES NOT CHANGE. Quota aggregation is untouched: p3_entitlement_data_usage still attributes the
-- same accounting records over the same half-open binding intervals, and p6_data_crossing remains the single
-- authority for WHEN a quota was crossed. The only edit to the crossing is WHICH NUMBER it compares the
-- running total against -- the entitlement's own snapshot when it has one, the plan revision otherwise.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. THE PACKAGE REVISION'S RULE.
--
-- It lives on the REVISION, not the package, because it is part of what was offered: a package that switches
-- from a flat 5 GB to 1 GB a night has changed its terms, and the guests who took it under the old terms must
-- go on reading the old revision. It is jsonb rather than three columns because the shape is mode-dependent --
-- a FIXED policy has no per-night rate, and columns that are meaningless for one mode invite rows where they
-- are set anyway.
ALTER TABLE iam_v2.internet_package_revisions
  ADD COLUMN IF NOT EXISTS data_allocation_policy jsonb;

COMMENT ON COLUMN iam_v2.internet_package_revisions.data_allocation_policy IS
  'How this revision turns a stay into a byte allowance. NULL or {"mode":"FIXED"} uses the pinned service '
  'plan revision''s data_quota_bytes unchanged. {"mode":"PER_STAY_NIGHT","gb_per_night":N,"min_gb":N,'
  '"max_gb":N} computes nights x gb_per_night, raises it to min_gb and caps it at max_gb when set. The '
  'computed value is frozen onto the entitlement at grant time and never recomputed.';

-- The shape is guarded HERE as well as in the writer, because a jsonb column with no constraint is a column
-- that will eventually hold something no reader expects. A revision is immutable once published, so a
-- malformed policy that got in would be permanent and would fail at GRANT time -- in front of a guest --
-- rather than at publication in front of the operator who wrote it.
ALTER TABLE iam_v2.internet_package_revisions
  DROP CONSTRAINT IF EXISTS internet_package_revisions_data_allocation_policy_shape;
-- THE WHOLE PREDICATE IS WRAPPED IN COALESCE(..., false), and that is not defensive noise -- without it this
-- constraint does not work at all.
--
-- A CHECK rejects a row only when its expression evaluates to FALSE. UNKNOWN passes. Every operator here
-- yields UNKNOWN on a missing key: `policy->'gb_per_night'` is SQL NULL when the key is absent,
-- jsonb_typeof(NULL) is NULL, and `NULL = 'number'` is UNKNOWN, which propagates through the ANDs and lets
-- the row in. The first real-PostgreSQL run of this migration accepted {"mode":"PER_STAY_NIGHT"} -- a
-- per-night package with no per-night rate -- which would have published an immutable revision that refuses
-- every grant it is offered for. COALESCE turns "cannot tell" into "no".
ALTER TABLE iam_v2.internet_package_revisions
  ADD CONSTRAINT internet_package_revisions_data_allocation_policy_shape CHECK (
    data_allocation_policy IS NULL
    OR COALESCE(
      jsonb_typeof(data_allocation_policy) = 'object'
      AND (
        data_allocation_policy->>'mode' = 'FIXED'
        OR (
          data_allocation_policy->>'mode' = 'PER_STAY_NIGHT'
          AND jsonb_typeof(data_allocation_policy->'gb_per_night') = 'number'
          AND (data_allocation_policy->>'gb_per_night')::numeric > 0
          AND (data_allocation_policy->'min_gb' IS NULL
               OR COALESCE(jsonb_typeof(data_allocation_policy->'min_gb') = 'number'
                           AND (data_allocation_policy->>'min_gb')::numeric >= 0, false))
          AND (data_allocation_policy->'max_gb' IS NULL
               OR COALESCE(jsonb_typeof(data_allocation_policy->'max_gb') = 'number'
                           AND (data_allocation_policy->>'max_gb')::numeric > 0
                           AND (data_allocation_policy->>'max_gb')::numeric
                               >= COALESCE((data_allocation_policy->>'min_gb')::numeric, 0), false))
        )
      ), false)
  );

-- ---------------------------------------------------------------------------
-- 2. THE SNAPSHOT.
--
-- Positive, not merely non-negative: a zero-byte allowance is exhausted the instant it is created, which
-- presents to a guest as a package that never worked and to an operator as an enforcement fault. The writer
-- refuses to compute one; this refuses to store one.
ALTER TABLE iam_v2.entitlements
  ADD COLUMN IF NOT EXISTS data_quota_bytes bigint;

ALTER TABLE iam_v2.entitlements
  DROP CONSTRAINT IF EXISTS entitlements_data_quota_bytes_positive;
ALTER TABLE iam_v2.entitlements
  ADD CONSTRAINT entitlements_data_quota_bytes_positive
    CHECK (data_quota_bytes IS NULL OR data_quota_bytes > 0);

COMMENT ON COLUMN iam_v2.entitlements.data_quota_bytes IS
  'The byte allowance THIS entitlement was granted, frozen at grant time. NULL means the allowance is the '
  'pinned service plan revision''s data_quota_bytes -- which is what every entitlement granted before '
  'PER_STAY_NIGHT existed means, and why nothing was backfilled. A PMS update that lengthens or shortens the '
  'stay does not change a value that is already here.';

-- ---------------------------------------------------------------------------
-- 3. THE CROSSING READS THE SNAPSHOT FIRST.
--
-- This is the ONE behavioural edit, and it is deliberately the smallest possible one: the same attribution,
-- the same half-open binding intervals, the same running sum, the same >= comparison -- against
-- COALESCE(entitlement snapshot, plan revision) instead of the plan revision alone.
--
-- The join to service_plan_revisions becomes a LEFT JOIN. It has to: a PER_STAY_NIGHT package may pin a plan
-- whose own data_quota_bytes is NULL (the plan sets the speed; the package sets the allowance), and under the
-- old INNER JOIN semantics that row would still have joined -- but under any future plan-revision gap the
-- entitlement would silently stop terminating. An entitlement that carries its own quota must terminate on
-- that quota whatever the plan revision does or does not say.
CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid)
RETURNS timestamptz
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
  SELECT min(x.sampled_at)
    FROM iam_v2.entitlements e
    LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    CROSS JOIN LATERAL (
      SELECT ar.sampled_at,
             sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
        FROM iam_v2.accounting_records ar
        JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
         AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
         AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
    ) x
   WHERE e.id = p_entitlement
     AND COALESCE(e.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
     AND x.running >= COALESCE(e.data_quota_bytes, spr.data_quota_bytes);
$$;

COMMENT ON FUNCTION iam_v2.p6_data_crossing(uuid) IS
  'The instant attributed usage first reached this entitlement''s quota, or NULL. The quota is the '
  'entitlement''s own frozen data_quota_bytes when it has one (a PER_STAY_NIGHT grant) and the pinned plan '
  'revision''s otherwise. The single implementation: both the expiry sweep''s candidate query and the '
  'sanctioned expiry writer call it, so they cannot disagree about when -- or whether -- a guest ran out.';

-- The grant is re-stated because CREATE OR REPLACE preserves the ACL but a rebuilt environment may not have
-- reached 0041 with the same role set. Stating it is idempotent; assuming it is how a sweep goes quiet.
REVOKE ALL ON FUNCTION iam_v2.p6_data_crossing(uuid) FROM PUBLIC;
DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p6_data_crossing(uuid) TO svc_acctd;
  END IF;
END
$grant$;

-- ---------------------------------------------------------------------------
-- 4. PROVE THE PROPERTIES THIS MIGRATION CLAIMS, on the database it just changed.
DO $verify$
DECLARE
  n_backfilled bigint;
  n_policied   bigint;
BEGIN
  -- Nothing was backfilled: every pre-existing entitlement still defers to its plan revision.
  SELECT count(*) INTO n_backfilled FROM iam_v2.entitlements WHERE data_quota_bytes IS NOT NULL;
  IF n_backfilled <> 0 THEN
    RAISE EXCEPTION 'migration 0064 set a quota on % existing entitlements; it must set none', n_backfilled;
  END IF;
  SELECT count(*) INTO n_policied
    FROM iam_v2.internet_package_revisions WHERE data_allocation_policy IS NOT NULL;
  IF n_policied <> 0 THEN
    RAISE EXCEPTION 'migration 0064 gave % existing package revisions a policy; it must give none', n_policied;
  END IF;

  -- The crossing function still exists, is still SECURITY DEFINER, and still reads the plan revision for an
  -- entitlement that carries no snapshot of its own.
  IF to_regprocedure('iam_v2.p6_data_crossing(uuid)') IS NULL THEN
    RAISE EXCEPTION 'migration 0064 lost iam_v2.p6_data_crossing';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'iam_v2' AND p.proname = 'p6_data_crossing' AND p.prosecdef
  ) THEN
    RAISE EXCEPTION 'iam_v2.p6_data_crossing is no longer SECURITY DEFINER';
  END IF;

  -- THE SHAPE CONSTRAINT MUST REFUSE, NOT MERELY EXIST.
  --
  -- The first version of this block "tested" the constraint by inserting into the real table and swallowing
  -- every error, so a NOT NULL violation on an unrelated column looked exactly like the constraint doing its
  -- job. It passed while the constraint accepted {"mode":"PER_STAY_NIGHT"} -- a per-night package with no
  -- per-night rate. That is what a check written to pass looks like.
  --
  -- So the predicate is lifted OUT of the catalog with pg_get_constraintdef and applied to a temp table. It
  -- cannot drift from the real constraint, because it IS the real constraint, and a temp table has no other
  -- columns whose failures could be mistaken for a refusal.
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'internet_package_revisions_data_allocation_policy_shape'
  ) THEN
    RAISE EXCEPTION 'the data_allocation_policy shape constraint is missing';
  END IF;
  DECLARE
    probe   text;
    bad     text;
    refused boolean;
  BEGIN
    SELECT pg_get_constraintdef(oid) INTO probe FROM pg_constraint
     WHERE conname = 'internet_package_revisions_data_allocation_policy_shape';
    EXECUTE format('CREATE TEMP TABLE _alloc_shape_probe (data_allocation_policy jsonb, %s) ON COMMIT DROP',
                   probe);
    FOREACH bad IN ARRAY ARRAY[
      '{"mode":"PER_STAY_NIGHT"}',                                        -- no rate at all
      '{"mode":"PER_STAY_NIGHT","gb_per_night":0}',                       -- a rate of nothing
      '{"mode":"PER_STAY_NIGHT","gb_per_night":"lots"}',                  -- not a number
      '{"mode":"PER_STAY_NIGHT","gb_per_night":1,"max_gb":0}',            -- a ceiling of nothing
      '{"mode":"PER_STAY_NIGHT","gb_per_night":1,"min_gb":10,"max_gb":5}',-- ceiling under floor
      '{"mode":"PER_GUEST_MOOD"}',                                        -- unknown mode
      '{}',                                                               -- an object stating nothing
      '"not an object"'
    ] LOOP
      refused := false;
      BEGIN
        EXECUTE 'INSERT INTO _alloc_shape_probe VALUES ($1::jsonb)' USING bad;
      EXCEPTION WHEN check_violation THEN
        refused := true;
      END;
      IF NOT refused THEN
        RAISE EXCEPTION 'the shape constraint accepted a malformed policy: %', bad;
      END IF;
    END LOOP;
    -- ...and it accepts the approved one, so the constraint is not merely refusing everything.
    EXECUTE 'INSERT INTO _alloc_shape_probe VALUES ($1::jsonb)'
      USING '{"mode":"PER_STAY_NIGHT","gb_per_night":1,"min_gb":5,"max_gb":20}';
    EXECUTE 'INSERT INTO _alloc_shape_probe VALUES ($1::jsonb)' USING '{"mode":"FIXED"}';
    EXECUTE 'INSERT INTO _alloc_shape_probe VALUES (NULL)';
    DROP TABLE _alloc_shape_probe;
  END;
END
$verify$;

COMMIT;
