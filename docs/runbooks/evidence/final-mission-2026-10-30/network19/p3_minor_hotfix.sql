-- LAB HOTFIX / DRAFT, NOT A MIGRATION. Makes iam_v2.p3_expected_class_minor(inet) agree with netd's class-id scheme for /17../19 client
-- networks (internal/shape/minor.go: host index 0..4095 -> 0x1000+idx, 4096.. -> 0x3000+idx-4096; every other subnet keeps the legacy rule).
-- Found in the lab: with a /19, every session whose address is in the upper half (host index >= 4096) failed
-- "ACCT_SOURCE_MISMATCH: the class origin does not describe session", so it was never activated and the guest was told it could not be brought online.
CREATE OR REPLACE FUNCTION iam_v2.p3_expected_class_minor(p_ip inet) RETURNS integer
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, public
  AS $$
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
    RETURN 4096 + (((split_part(host(p_ip), '.', 3)::int & 15) << 8) | split_part(host(p_ip), '.', 4)::int);  -- the legacy rule, unchanged
  END IF;
  v_idx := p_ip - network(v_net);
  IF v_idx < 4096 THEN
    RETURN 4096 + v_idx::int;
  END IF;
  RETURN 12288 + (v_idx - 4096)::int;
END;
$$;
