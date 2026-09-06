-- THE ADMIN SERVICE CAN AUTHOR A PACKAGE'S CONDITIONS BUT NOT READ THEM BACK.
--
-- iam_v2.package_eligibility_rules and iam_v2.package_grant_tiers are granted INSERT to svc_edged and SELECT
-- to svc_scd. That split is deliberate and correct: the admin service composes packages, the guest-auth
-- service evaluates them. Nothing was wrong with it.
--
-- What broke is that Hotel Admin's package EDIT needs to load a package's current specification before it can
-- save a new revision of it. Publishing replaces the whole spec, so a form that could not read the existing
-- eligibility rules and grant tiers would republish without them — silently changing who a package is offered
-- to, and leaving a package with no grant tier offered to nobody. The Edit screen therefore refuses to open
-- rather than risk that, which is safe and useless.
--
-- THE FIX IS NOT `GRANT SELECT`. That would let the admin service read every rule and tier of every revision,
-- historical and current, including system packages — permanently widening the role's table surface for one
-- screen's needs. This is the narrow answer instead: a read function that resolves the CURRENT revision of ONE
-- non-system package inside the caller's own tenant and site, and returns nothing else.
--
-- WHY IT TAKES A PACKAGE ID AND NOT A REVISION ID. A revision-id parameter would let the caller name any
-- revision it liked, including a superseded one or one belonging to another package; the scoping would then be
-- the caller's to get right. Resolving current_revision_id inside the function removes that choice entirely:
-- there is no argument that reaches a historical revision.
--
-- WHAT IT CANNOT RETURN: no row ids, no tenant/site columns, no guest, Stay, reservation, folio, payment,
-- financial or PMS identity — none of which exist in either table. The values are package POLICY.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p2_package_current_conditions(
    p_tenant uuid, p_site uuid, p_package uuid)
RETURNS TABLE (kind text, rule_type text, tier_order int, value jsonb)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $fn$
  WITH cur AS (
    -- THE WHOLE AUTHORISATION DECISION, and it is not the caller's to make. The revision is resolved from the
    -- package's own current pointer, and only for a package that is in this tenant and site and is not a
    -- system package. A caller naming another site's package, a system package, or a package with nothing
    -- published gets no rows — the same answer as a package that genuinely has no conditions, which is the
    -- correct amount of information to give something that asked out of scope.
    SELECT p.current_revision_id AS rev
      FROM iam_v2.internet_packages p
     WHERE p.id = p_package
       AND p.tenant_id = p_tenant
       AND p.site_id = p_site
       AND p.is_system = false
       AND p.current_revision_id IS NOT NULL
  )
  SELECT 'RULE'::text, r.rule_type, NULL::int, COALESCE(r.rule_value, '{}'::jsonb)
    FROM iam_v2.package_eligibility_rules r
    JOIN cur ON r.package_revision_id = cur.rev
   WHERE r.tenant_id = p_tenant AND r.site_id = p_site
  UNION ALL
  SELECT 'TIER'::text, NULL::text, t.tier_order, COALESCE(t.grant_value, '{}'::jsonb)
    FROM iam_v2.package_grant_tiers t
    JOIN cur ON t.package_revision_id = cur.rev
   WHERE t.tenant_id = p_tenant AND t.site_id = p_site
   ORDER BY 1, 3, 2
$fn$;

COMMENT ON FUNCTION iam_v2.p2_package_current_conditions(uuid, uuid, uuid) IS
  'The eligibility rules and grant tiers of the CURRENT revision of one non-system Internet package, scoped '
  'to the caller-supplied tenant and site and resolved from the package''s own current-revision pointer. '
  'Exists so the Hotel-Admin service can load a package''s specification before republishing it without '
  'holding SELECT on iam_v2.package_eligibility_rules or iam_v2.package_grant_tiers. Returns package policy '
  'only: no row ids, no tenant/site, and no guest, Stay, reservation, folio, payment or PMS data.';

-- Least privilege: nobody by default, EXECUTE to the admin service only. No table SELECT is granted anywhere
-- by this migration, and no write privilege of any kind.
REVOKE ALL ON FUNCTION iam_v2.p2_package_current_conditions(uuid, uuid, uuid) FROM PUBLIC;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.p2_package_current_conditions(uuid,uuid,uuid) TO svc_edged';
  END IF;
END $grant$;

-- Ownership follows the schema, so the definer runs with the rights that already own these tables rather
-- than with whatever role happened to apply the migration.
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p2_package_current_conditions(uuid,uuid,uuid) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- ---------------------------------------------------------------------------------------------------------
-- The boundary this migration exists to hold, asserted here rather than assumed
-- ---------------------------------------------------------------------------------------------------------
DO $verify$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    RETURN;   -- a scratch database without the service roles has nothing to assert
  END IF;
  -- The admin service must have gained EXECUTE...
  IF NOT has_function_privilege('svc_edged',
        'iam_v2.p2_package_current_conditions(uuid,uuid,uuid)', 'EXECUTE') THEN
    RAISE EXCEPTION '0063: svc_edged did not receive EXECUTE on the scoped reader';
  END IF;
  -- ...and must NOT have gained direct read of what it now reads through the function. This is the entire
  -- point of the migration, so it fails closed if the boundary moved.
  IF has_table_privilege('svc_edged', 'iam_v2.package_eligibility_rules', 'SELECT')
     OR has_table_privilege('svc_edged', 'iam_v2.package_grant_tiers', 'SELECT') THEN
    RAISE EXCEPTION '0063: svc_edged holds direct SELECT on a protected package-conditions table';
  END IF;
  -- ...and no write privilege was widened by this migration.
  IF has_table_privilege('svc_edged', 'iam_v2.package_eligibility_rules', 'UPDATE')
     OR has_table_privilege('svc_edged', 'iam_v2.package_eligibility_rules', 'DELETE')
     OR has_table_privilege('svc_edged', 'iam_v2.package_grant_tiers', 'UPDATE')
     OR has_table_privilege('svc_edged', 'iam_v2.package_grant_tiers', 'DELETE') THEN
    RAISE EXCEPTION '0063: svc_edged holds unexpected write privilege on a package-conditions table';
  END IF;
  IF has_function_privilege('public',
        'iam_v2.p2_package_current_conditions(uuid,uuid,uuid)', 'EXECUTE') THEN
    RAISE EXCEPTION '0063: PUBLIC can execute the scoped reader';
  END IF;
END $verify$;

COMMIT;
