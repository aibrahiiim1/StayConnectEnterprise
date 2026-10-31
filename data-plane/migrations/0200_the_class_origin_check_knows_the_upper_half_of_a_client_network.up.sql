-- A client network may now be as wide as a /19 (8,190 addresses). netd names the shaping class of an address by its OFFSET in the
-- network (internal/shape/minor.go): offsets 0..4095 -> 0x1000 + offset, offsets 4096.. -> 0x3000 + offset - 4096. The database
-- re-derives the same minor to refuse an accounting observation whose class does not describe the session (ACCT_SOURCE_MISMATCH),
-- but it still used the original rule (the low 12 bits of the address) for every address, so on a /17../19 network every session in
-- the upper half was refused, never activated, and the guest was told it could not be brought online (found in the /19 lab).
--
-- The function now reads the enabled client network that contains the address. A network of /17../19 uses netd's two-band rule;
-- every other address keeps the original rule, so no existing /20-or-narrower network changes a single class id.
-- Reversible: the down migration restores the original function. No data is touched.
BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p3_expected_class_minor(p_ip inet) RETURNS int
  LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $fn$
DECLARE
  v_net inet;
  v_idx bigint;
BEGIN
  IF p_ip IS NULL OR family(p_ip) <> 4 THEN
    RETURN NULL;
  END IF;
  SELECT n.subnet_cidr::inet INTO v_net
    FROM public.guest_networks n
   WHERE n.enabled AND n.subnet_cidr IS NOT NULL
     AND masklen(n.subnet_cidr::inet) BETWEEN 17 AND 19
     AND n.subnet_cidr::inet >>= p_ip
   ORDER BY masklen(n.subnet_cidr::inet) DESC
   LIMIT 1;
  IF v_net IS NULL THEN
    RETURN 4096 + (((split_part(host(p_ip), '.', 3)::int & 15) << 8) | split_part(host(p_ip), '.', 4)::int);
  END IF;
  v_idx := p_ip - network(v_net);
  IF v_idx < 4096 THEN
    RETURN 4096 + v_idx::int;
  END IF;
  RETURN 12288 + (v_idx - 4096)::int;
END;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_expected_class_minor(inet) FROM PUBLIC;

-- The function runs as its caller, the owner of the accounting functions, which must be able to read the networks. Gate-P re-asserts it.
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'GRANT SELECT ON public.guest_networks TO iam_v2_owner';
  END IF;
END $$;

COMMIT;
