-- 0091 — AN INTERNET PACKAGE OR SERVICE PLAN THAT WAS NEVER USED MAY BE DELETED. NOTHING ELSE MAY.
--
-- Product-Owner authorisation, 2026-09-25, within the Hotel Admin UX and operations mission: real, permanent
-- deletion of an unused package or plan, with every safety and referential check enforced in the database,
-- refusing whenever a purchase, entitlement, voucher, quote, grant, ledger entry or any other legitimate
-- dependency means the record must remain. Disable stays the answer for everything else.
--
-- WHY IT NEEDED A MIGRATION AT ALL. Three things made deletion impossible, and all three were right:
--
--   * revision rows are immutable: `imm_pkg_rev` / `imm_plan_rev` reject every UPDATE and DELETE;
--   * no runtime role holds DELETE on the catalogue -- svc_edged may INSERT and (for packages) UPDATE;
--   * every table that points at a package or plan revision does so with ON DELETE NO ACTION.
--
-- This migration changes the FIRST of those only as far as it must, leaves the second exactly as it is, and
-- relies on the third as the final guard. Concretely:
--
-- 1. ONE SOURCE OF TRUTH FOR "IS IT USED?" -- iam_v2.internet_package_deletion_blockers() and
--    iam_v2.service_plan_deletion_blockers() return a count per reason. The screen that explains why a
--    record can or cannot be deleted and the kernel that deletes it call the SAME function, so the
--    explanation and the enforcement cannot drift apart. They are SECURITY DEFINER because some references
--    live in tables svc_edged may not read (vouchers, settlement mappings, portal offers); they return
--    COUNTS only, never a row, so that is no widening of what edged can see.
--
-- 2. ONE KERNEL PER KIND -- iam_v2.internet_package_delete_unused() and iam_v2.service_plan_delete_unused().
--    Each locks the record, refuses on any blocker, and then removes the record and its OWN configuration
--    history (its revisions, and a package revision's eligibility rules and grant tiers, which cascade
--    because they are part of that revision). Nothing else is removed. There is no cascade from anything a
--    guest, a voucher or a ledger produced.
--
-- 3. THE FOREIGN KEYS HAVE THE LAST WORD. The blocker check is for an operator-readable answer; it is not the
--    guarantee. The deletes are issued without cascade against tables whose every inbound reference is
--    NO ACTION, so if anything at all still points at a revision -- including a guest who takes an offer in
--    the instant between the check and the delete -- PostgreSQL aborts the transaction and nothing is
--    deleted. A reference this migration's author did not know about is refused the same way.
--
-- 4. REVISIONS STAY IMMUTABLE TO EVERYONE ELSE. The shared trg_reject_update_delete() is used by other
--    tables and is not touched. The two revision tables get their own trigger function that still refuses
--    every UPDATE, and refuses every DELETE unless BOTH hold: a transaction-local flag that only the kernels
--    set, AND the statement running as the table's own owner -- which, among the roles that can reach these
--    tables, only a SECURITY DEFINER kernel is. A runtime role cannot satisfy the second condition, and holds
--    no DELETE privilege in any case.
--
-- WHAT COUNTS AS USE, for a package (across EVERY revision it ever had, not only the current one):
--   entitlements, purchases, offer quotes, portal offers (auth_context_offers), vouchers, voucher batches,
--   settlement mappings, the checkout-grace configuration, the checkout-grace publication ledger (a soft
--   reference with no foreign key, found by searching the catalogue for package-shaped columns), guest
--   accounts assigned to it, and being a system or reserved grace package.
-- for a plan (across every revision): any package revision built on it -- including superseded ones, since
--   they are the history of a package that may have been sold -- entitlements, and being the reserved grace
--   plan.
--
-- The audit event is written by edged, exactly as voucher cancellation is (0087): the kernel is the only
-- path, and edged records who asked and why.

-- ---------------------------------------------------------------------------------------------------------
-- 1. The immutability trigger for the two revision tables
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.trg_catalogue_revision_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE v_owner name;
BEGIN
  IF TG_OP = 'DELETE' AND current_setting('iam_v2.catalogue_purge', true) = 'unused' THEN
    SELECT pg_get_userbyid(c.relowner) INTO v_owner FROM pg_class c WHERE c.oid = TG_RELID;
    IF current_user = v_owner THEN
      RETURN OLD;
    END IF;
  END IF;
  -- Word for word what trg_reject_update_delete() says, so nothing that matches on it changes meaning.
  RAISE EXCEPTION '% is immutable (no UPDATE/DELETE) on %', TG_TABLE_NAME, TG_OP;
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.trg_catalogue_revision_immutable() FROM PUBLIC;

DROP TRIGGER IF EXISTS imm_pkg_rev ON iam_v2.internet_package_revisions;
CREATE TRIGGER imm_pkg_rev BEFORE DELETE OR UPDATE ON iam_v2.internet_package_revisions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_catalogue_revision_immutable();

DROP TRIGGER IF EXISTS imm_plan_rev ON iam_v2.service_plan_revisions;
CREATE TRIGGER imm_plan_rev BEFORE DELETE OR UPDATE ON iam_v2.service_plan_revisions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_catalogue_revision_immutable();

-- ---------------------------------------------------------------------------------------------------------
-- 2. "Is it used?" -- one answer, shared by the explanation and the kernel
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.internet_package_deletion_blockers(
    p_tenant uuid, p_site uuid, p_package uuid)
  RETURNS TABLE (reason text, n bigint)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE
  v_pkg iam_v2.internet_packages%ROWTYPE;
  v_revs uuid[];
BEGIN
  SELECT * INTO v_pkg FROM iam_v2.internet_packages
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_package;
  IF NOT FOUND THEN
    reason := 'NOT_FOUND'; n := 1; RETURN NEXT; RETURN;
  END IF;
  IF v_pkg.is_system OR v_pkg.code IN ('__sys_emergency_grace_pkg__') THEN
    reason := 'SYSTEM_PACKAGE'; n := 1; RETURN NEXT;
  END IF;

  SELECT coalesce(array_agg(r.id), '{}') INTO v_revs FROM iam_v2.internet_package_revisions r
   WHERE r.tenant_id = p_tenant AND r.site_id = p_site AND r.package_id = p_package;

  RETURN QUERY
  SELECT x.reason, x.n FROM (
    SELECT 'ENTITLEMENTS' AS reason, (SELECT count(*) FROM iam_v2.entitlements e
      WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.package_revision_id = ANY (v_revs)) AS n
    UNION ALL SELECT 'PURCHASES', (SELECT count(*) FROM iam_v2.purchases p
      WHERE p.tenant_id = p_tenant AND p.site_id = p_site AND p.package_revision_id = ANY (v_revs))
    UNION ALL SELECT 'OFFER_QUOTES', (SELECT count(*) FROM iam_v2.offer_quotes q
      WHERE q.tenant_id = p_tenant AND q.site_id = p_site AND q.package_revision_id = ANY (v_revs))
    UNION ALL SELECT 'PORTAL_OFFERS', (SELECT count(*) FROM iam_v2.auth_context_offers o
      WHERE o.tenant_id = p_tenant AND o.site_id = p_site AND o.package_revision_id = ANY (v_revs))
    UNION ALL SELECT 'VOUCHERS', (SELECT count(*) FROM iam_v2.vouchers v
      WHERE v.tenant_id = p_tenant AND v.site_id = p_site AND v.package_revision_id = ANY (v_revs))
    UNION ALL SELECT 'VOUCHER_BATCHES', (SELECT count(*) FROM iam_v2.voucher_batches b
      WHERE b.tenant_id = p_tenant AND b.site_id = p_site AND b.package_revision_id = ANY (v_revs))
    UNION ALL SELECT 'SETTLEMENT_MAPPINGS', (SELECT count(*) FROM iam_v2.package_settlement_mappings m
      WHERE m.tenant_id = p_tenant AND m.site_id = p_site AND m.package_revision_id = ANY (v_revs))
    UNION ALL SELECT 'CHECKOUT_GRACE_CONFIG', (SELECT count(*) FROM iam_v2.site_checkout_grace_config g
      WHERE g.tenant_id = p_tenant AND g.site_id = p_site AND g.grace_package_revision_id = ANY (v_revs))
    UNION ALL SELECT 'CHECKOUT_GRACE_HISTORY', (SELECT count(*) FROM iam_v2.checkout_grace_policy_publications h
      WHERE h.tenant_id = p_tenant AND h.site_id = p_site AND h.grace_package_revision_id = ANY (v_revs))
    UNION ALL SELECT 'GUEST_ACCOUNTS', (SELECT count(*) FROM iam_v2.guest_access_accounts a
      WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND a.assigned_package_id = p_package)
  ) x WHERE x.n > 0;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.service_plan_deletion_blockers(
    p_tenant uuid, p_site uuid, p_plan uuid)
  RETURNS TABLE (reason text, n bigint)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE
  v_plan iam_v2.service_plans%ROWTYPE;
  v_revs uuid[];
BEGIN
  SELECT * INTO v_plan FROM iam_v2.service_plans
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_plan;
  IF NOT FOUND THEN
    reason := 'NOT_FOUND'; n := 1; RETURN NEXT; RETURN;
  END IF;
  IF v_plan.code IN ('__sys_emergency_grace_plan__') THEN
    reason := 'SYSTEM_PLAN'; n := 1; RETURN NEXT;
  END IF;

  SELECT coalesce(array_agg(r.id), '{}') INTO v_revs FROM iam_v2.service_plan_revisions r
   WHERE r.tenant_id = p_tenant AND r.site_id = p_site AND r.service_plan_id = p_plan;

  RETURN QUERY
  SELECT x.reason, x.n FROM (
    -- EVERY package revision, current or superseded: a superseded revision is the history of a package that
    -- may have been sold, and it cannot be allowed to point at a plan that no longer exists.
    SELECT 'PACKAGE_REVISIONS' AS reason, (SELECT count(*) FROM iam_v2.internet_package_revisions pr
      WHERE pr.tenant_id = p_tenant AND pr.site_id = p_site AND pr.service_plan_revision_id = ANY (v_revs)) AS n
    UNION ALL SELECT 'ENTITLEMENTS', (SELECT count(*) FROM iam_v2.entitlements e
      WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.service_plan_revision_id = ANY (v_revs))
  ) x WHERE x.n > 0;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 3. The kernels
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.internet_package_delete_unused(
    p_tenant uuid, p_site uuid, p_package uuid, p_operator uuid, p_reason text)
  RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE
  v_blockers text;
  v_n integer;
BEGIN
  IF p_operator IS NULL THEN
    RAISE EXCEPTION 'CATALOGUE_DELETE_NEEDS_AN_OPERATOR' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason, ''))) < 4 THEN
    RAISE EXCEPTION 'CATALOGUE_DELETE_NEEDS_A_REASON' USING ERRCODE = 'check_violation';
  END IF;

  -- Lock first, then decide: a concurrent publish of a new revision waits for us or we wait for it.
  PERFORM 1 FROM iam_v2.internet_packages
    WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_package FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'PACKAGE_NOT_FOUND: no package % for tenant %/site %', p_package, p_tenant, p_site
      USING ERRCODE = 'no_data_found';
  END IF;

  SELECT string_agg(b.reason || '=' || b.n, ',' ORDER BY b.reason) INTO v_blockers
    FROM iam_v2.internet_package_deletion_blockers(p_tenant, p_site, p_package) b;
  IF v_blockers IS NOT NULL THEN
    RAISE EXCEPTION 'PACKAGE_IN_USE: %', v_blockers USING ERRCODE = 'restrict_violation';
  END IF;

  PERFORM set_config('iam_v2.catalogue_purge', 'unused', true);
  -- The package points at its current revision and the revisions point at the package; unhook the first so
  -- the revisions can go, then the package. No cascade: eligibility rules and grant tiers cascade from THEIR
  -- revision by the schema's own definition, and everything else is NO ACTION and will abort this if present.
  UPDATE iam_v2.internet_packages SET current_revision_id = NULL
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_package;
  DELETE FROM iam_v2.internet_package_revisions
   WHERE tenant_id = p_tenant AND site_id = p_site AND package_id = p_package;
  DELETE FROM iam_v2.internet_packages
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_package;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  PERFORM set_config('iam_v2.catalogue_purge', '', true);
  IF v_n <> 1 THEN
    RAISE EXCEPTION 'PACKAGE_NOT_DELETED: package % was not removed', p_package USING ERRCODE = 'check_violation';
  END IF;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.service_plan_delete_unused(
    p_tenant uuid, p_site uuid, p_plan uuid, p_operator uuid, p_reason text)
  RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE
  v_blockers text;
  v_n integer;
BEGIN
  IF p_operator IS NULL THEN
    RAISE EXCEPTION 'CATALOGUE_DELETE_NEEDS_AN_OPERATOR' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason, ''))) < 4 THEN
    RAISE EXCEPTION 'CATALOGUE_DELETE_NEEDS_A_REASON' USING ERRCODE = 'check_violation';
  END IF;

  PERFORM 1 FROM iam_v2.service_plans
    WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_plan FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'PLAN_NOT_FOUND: no plan % for tenant %/site %', p_plan, p_tenant, p_site
      USING ERRCODE = 'no_data_found';
  END IF;

  SELECT string_agg(b.reason || '=' || b.n, ',' ORDER BY b.reason) INTO v_blockers
    FROM iam_v2.service_plan_deletion_blockers(p_tenant, p_site, p_plan) b;
  IF v_blockers IS NOT NULL THEN
    RAISE EXCEPTION 'PLAN_IN_USE: %', v_blockers USING ERRCODE = 'restrict_violation';
  END IF;

  PERFORM set_config('iam_v2.catalogue_purge', 'unused', true);
  UPDATE iam_v2.service_plans SET current_revision_id = NULL
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_plan;
  DELETE FROM iam_v2.service_plan_revisions
   WHERE tenant_id = p_tenant AND site_id = p_site AND service_plan_id = p_plan;
  DELETE FROM iam_v2.service_plans
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_plan;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  PERFORM set_config('iam_v2.catalogue_purge', '', true);
  IF v_n <> 1 THEN
    RAISE EXCEPTION 'PLAN_NOT_DELETED: plan % was not removed', p_plan USING ERRCODE = 'check_violation';
  END IF;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. Ownership (guarded, exactly as 0087 learned to) and privileges
-- ---------------------------------------------------------------------------------------------------------
-- SECURITY DEFINER runs as the owner, and the immutability trigger admits a DELETE only from the TABLE'S
-- owner, so on the appliance these must belong to iam_v2_owner. A bare ALTER aborts the whole migration on a
-- database with no such role (every disposable fixture), so it is guarded.
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.trg_catalogue_revision_immutable() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.internet_package_deletion_blockers(uuid, uuid, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.service_plan_deletion_blockers(uuid, uuid, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.internet_package_delete_unused(uuid, uuid, uuid, uuid, text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.service_plan_delete_unused(uuid, uuid, uuid, uuid, text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

REVOKE EXECUTE ON FUNCTION iam_v2.internet_package_deletion_blockers(uuid, uuid, uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.service_plan_deletion_blockers(uuid, uuid, uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.internet_package_delete_unused(uuid, uuid, uuid, uuid, text) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.service_plan_delete_unused(uuid, uuid, uuid, uuid, text) FROM PUBLIC;

-- Mirrored in deploy/gatep/svc-edged-phase2-commerce-grants.sql: gatep-grants.sql revokes everything from the
-- service roles and runs AFTER the migrations, so a grant made only here would not survive a factory-clean
-- install (tools/validate-migration-grant-durability.py refuses this file without the mirror).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.internet_package_deletion_blockers(uuid, uuid, uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.service_plan_deletion_blockers(uuid, uuid, uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.internet_package_delete_unused(uuid, uuid, uuid, uuid, text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.service_plan_delete_unused(uuid, uuid, uuid, uuid, text) TO svc_edged;
  END IF;
END $$;

-- The boundary, asserted: deletion is the kernel, so no runtime role may DELETE the catalogue directly.
DO $$
DECLARE r text; t text;
BEGIN
  FOREACH r IN ARRAY ARRAY['svc_edged','svc_scd','svc_pmsd','svc_acctd','svc_netd'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      FOREACH t IN ARRAY ARRAY['iam_v2.internet_packages','iam_v2.internet_package_revisions',
                               'iam_v2.service_plans','iam_v2.service_plan_revisions'] LOOP
        IF has_table_privilege(r, t, 'DELETE') THEN
          RAISE EXCEPTION '% must not hold DELETE on %: deletion of an unused package or plan is '
                          'iam_v2.internet_package_delete_unused / iam_v2.service_plan_delete_unused', r, t;
        END IF;
      END LOOP;
    END IF;
  END LOOP;
END $$;
