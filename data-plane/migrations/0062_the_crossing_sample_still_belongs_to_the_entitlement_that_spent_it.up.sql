-- THE SAMPLE THAT ENDED THE ACCESS BELONGED TO NOBODY.
--
-- 0061 made iam_v2.entitlements.consumed_data_bytes true, and named iam_v2.p3_entitlement_data_usage as the
-- authoritative derivation the counter must equal. On the live data-quota acceptance (T0118) the two
-- disagreed by exactly one sample:
--
--   counter 100,108,666    derived 99,611,446    difference 497,220 = accounting record 412
--
-- Record 412 at 01:40:12.423805Z IS the crossing: it took the guest over 100,000,000 bytes and ended their
-- access. The Entitlement terminated at that instant, so the Session's binding closed at that same instant --
-- and the attribution predicate is HALF-OPEN, `bound_until > sampled_at`. A sample landing exactly on the
-- closing instant therefore falls outside the interval the moment the interval closes. The sample that ended
-- the access stopped being attributed to the access it ended.
--
-- THE HALF-OPEN BOUND IS NOT THE BUG AND IS NOT TOUCHED. It is what makes a Phase-5 rebinding unambiguous:
-- the old binding closes at T and the new one opens at T, so a sample at exactly T must belong to the NEW
-- entitlement and to it alone. Widening the bound to `>=` would attribute that sample to BOTH -- inventing
-- usage out of a transfer, in the one place the system moves a guest between commercial agreements.
--
-- SO THE FIX IS NARROWER THAN THE BOUND. A closing binding may claim a sample at its own closing instant
-- ONLY WHEN NO OTHER BINDING FOR THAT SESSION COVERS THAT INSTANT -- that is, only when the half-open rule
-- has left the sample unattributed. At a Phase-5 boundary the successor binding covers it, so the fallback
-- cannot fire and nothing double-counts. At a terminal boundary there is no successor, so the sample stays
-- with the Entitlement that spent it.
--
-- WHAT THIS DOES NOT CHANGE:
--   * iam_v2.p6_data_crossing -- the enforcement authority for WHEN a quota was crossed -- is untouched.
--     Enforcement reads it while a binding is OPEN, which is the only time it decides anything.
--   * No accounting record, Purchase, Session, Stay or counter value is rewritten. The accepted T0118
--     evidence is read, not edited: the counter already held the right number, and this makes the derivation
--     agree with it rather than the other way round.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p3_entitlement_data_usage(p_entitlement uuid)
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $usage$
  SELECT COALESCE(sum(ar.bytes_up + ar.bytes_down), 0)::bigint
    FROM iam_v2.accounting_records ar
    JOIN iam_v2.session_entitlement_bindings b
      ON b.session_id = ar.session_id
     AND b.entitlement_id = p_entitlement
     AND b.bound_from <= ar.sampled_at
     AND (
           -- the ordinary half-open interval, unchanged
           b.bound_until IS NULL
        OR b.bound_until > ar.sampled_at
           -- ...and the closing instant, ONLY when nothing else owns the sample. At most one binding per
           -- session can close at a given instant, and this arm is unreachable whenever a successor binding
           -- covers it, so a sample is still attributed EXACTLY ONCE.
        OR ( b.bound_until = ar.sampled_at
             AND NOT EXISTS (
                   SELECT 1
                     FROM iam_v2.session_entitlement_bindings b2
                    WHERE b2.session_id = ar.session_id
                      AND b2.bound_from <= ar.sampled_at
                      AND (b2.bound_until IS NULL OR b2.bound_until > ar.sampled_at)) )
         )
$usage$;

COMMENT ON FUNCTION iam_v2.p3_entitlement_data_usage(uuid) IS
  'Authoritative bytes attributed to an Entitlement across every Session and Device bound to it. Attribution '
  'follows the half-open binding interval, plus the closing instant itself when no other binding for that '
  'session covers it - which is the terminal case, where the sample that exhausted the quota would otherwise '
  'be attributed to nothing. A Phase-5 rebinding always has a successor covering the boundary, so that arm '
  'cannot fire there and no sample is counted twice. entitlements.consumed_data_bytes must equal this.';

-- ---------------------------------------------------------------------------------------------------------
-- The invariant 0061 declared, now actually true after a terminal crossing
-- ---------------------------------------------------------------------------------------------------------
DO $verify$
DECLARE bad int; detail text;
BEGIN
  SELECT count(*), string_agg(format('%s: counter=%s derived=%s', e.id, e.consumed_data_bytes,
                                     iam_v2.p3_entitlement_data_usage(e.id)), '; ')
    INTO bad, detail
    FROM iam_v2.entitlements e
   WHERE e.consumed_data_bytes <> iam_v2.p3_entitlement_data_usage(e.id);
  IF bad > 0 THEN
    RAISE EXCEPTION 'migration 0062: % entitlement(s) still disagree with their attributed accounting: %',
      bad, detail;
  END IF;
  RAISE NOTICE 'migration 0062: every entitlement counter equals its attributed accounting';
END $verify$;

-- ---------------------------------------------------------------------------------------------------------
-- ...and no sample is attributed twice, asserted against the real data rather than argued for
-- ---------------------------------------------------------------------------------------------------------
DO $once$
DECLARE dupes int;
BEGIN
  SELECT count(*) INTO dupes FROM (
    SELECT ar.id
      FROM iam_v2.accounting_records ar
      JOIN iam_v2.session_entitlement_bindings b
        ON b.session_id = ar.session_id
       AND b.bound_from <= ar.sampled_at
       AND ( b.bound_until IS NULL
          OR b.bound_until > ar.sampled_at
          OR ( b.bound_until = ar.sampled_at
               AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2
                                WHERE b2.session_id = ar.session_id
                                  AND b2.bound_from <= ar.sampled_at
                                  AND (b2.bound_until IS NULL OR b2.bound_until > ar.sampled_at))) )
     GROUP BY ar.id HAVING count(*) > 1) d;
  IF dupes > 0 THEN
    RAISE EXCEPTION 'migration 0062: % accounting record(s) attribute to more than one binding', dupes;
  END IF;
END $once$;

COMMIT;
