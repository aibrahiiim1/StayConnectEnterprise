-- Restores the original function (0010): the low 12 bits of the address for every address. WHAT THIS COSTS: on a /17../19 client
-- network every session whose address is in the upper half is refused again (ACCT_SOURCE_MISMATCH). Widen no network before this
-- is undone or those guests cannot be brought online. The SELECT grant on guest_networks is left in place (harmless).
BEGIN;
CREATE OR REPLACE FUNCTION iam_v2.p3_expected_class_minor(p_ip inet) RETURNS int
  LANGUAGE sql IMMUTABLE SET search_path = iam_v2, pg_temp AS $fn$
  SELECT CASE
    WHEN p_ip IS NULL OR family(p_ip) <> 4 THEN NULL
    ELSE 4096 + (((split_part(host(p_ip), '.', 3)::int & 15) << 8) | split_part(host(p_ip), '.', 4)::int)
  END;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_expected_class_minor(inet) FROM PUBLIC;
COMMIT;
