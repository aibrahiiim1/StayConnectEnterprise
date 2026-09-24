-- Behaviour of migration 0091, run against a FACTORY-CLEAN schema built by scripts/clean-install-reconstruction.sh
-- (real roles, owners and Gate-P grants). Every mutation is attempted AS svc_edged, the role the appliance uses.
-- Each check prints "PASS <name>" or "FAIL <name>: <detail>"; the runner fails on any FAIL or missing PASS.
\set ON_ERROR_STOP off
\set QUIET on
SET client_min_messages = warning;

CREATE TEMP TABLE t_ids (k text PRIMARY KEY, v uuid);
INSERT INTO t_ids VALUES
  ('ten', '11111111-1111-4111-8111-111111111111'), ('site', '22222222-2222-4222-8222-222222222222'),
  ('op',  '33333333-3333-4333-8333-333333333333'),
  ('p1', gen_random_uuid()), ('p1r', gen_random_uuid()), ('k1', gen_random_uuid()), ('k1r', gen_random_uuid()),
  ('p2', gen_random_uuid()), ('p2r', gen_random_uuid()), ('k2', gen_random_uuid()), ('k2r', gen_random_uuid()),
  ('p3', gen_random_uuid()), ('p3r', gen_random_uuid()), ('k3', gen_random_uuid()), ('k3a', gen_random_uuid()), ('k3b', gen_random_uuid()),
  ('p4', gen_random_uuid()), ('p4r', gen_random_uuid()), ('k4', gen_random_uuid()), ('k4a', gen_random_uuid()), ('k4b', gen_random_uuid()),
  ('ks', gen_random_uuid()), ('ksr', gen_random_uuid());
GRANT SELECT ON t_ids TO PUBLIC;
CREATE OR REPLACE FUNCTION pg_temp.id(k text) RETURNS uuid LANGUAGE sql AS $$ SELECT v FROM t_ids WHERE t_ids.k = $1 $$;

-- ---- fixture: built as the superuser, exactly as publishing would leave it ----------------------------------
DO $fx$
DECLARE ten uuid := pg_temp.id('ten'); st uuid := pg_temp.id('site');
BEGIN
  -- plan + package pairs
  INSERT INTO iam_v2.service_plans (id, tenant_id, site_id, code, enabled) VALUES
    (pg_temp.id('p1'), ten, st, 't-plan-1', true), (pg_temp.id('p2'), ten, st, 't-plan-2', true),
    (pg_temp.id('p3'), ten, st, 't-plan-3', true), (pg_temp.id('p4'), ten, st, 't-plan-4', true);
  INSERT INTO iam_v2.service_plan_revisions (id, tenant_id, site_id, service_plan_id, revision_no, name, down_kbps, up_kbps,
      max_concurrent_devices, device_limit_policy, time_accounting_mode, speed_allocation) VALUES
    (pg_temp.id('p1r'), ten, st, pg_temp.id('p1'), 1, 'P1', 1000, 500, 2, 'REJECT_NEW_DEVICE', 'VALIDITY_WINDOW', 'PER_DEVICE'),
    (pg_temp.id('p2r'), ten, st, pg_temp.id('p2'), 1, 'P2', 1000, 500, 2, 'REJECT_NEW_DEVICE', 'VALIDITY_WINDOW', 'PER_DEVICE'),
    (pg_temp.id('p3r'), ten, st, pg_temp.id('p3'), 1, 'P3', 1000, 500, 2, 'REJECT_NEW_DEVICE', 'VALIDITY_WINDOW', 'PER_DEVICE'),
    (pg_temp.id('p4r'), ten, st, pg_temp.id('p4'), 1, 'P4', 1000, 500, 2, 'REJECT_NEW_DEVICE', 'VALIDITY_WINDOW', 'PER_DEVICE');
  UPDATE iam_v2.service_plans SET current_revision_id = CASE id
      WHEN pg_temp.id('p1') THEN pg_temp.id('p1r') WHEN pg_temp.id('p2') THEN pg_temp.id('p2r')
      WHEN pg_temp.id('p3') THEN pg_temp.id('p3r') WHEN pg_temp.id('p4') THEN pg_temp.id('p4r') END
   WHERE tenant_id = ten;

  INSERT INTO iam_v2.internet_packages (id, tenant_id, site_id, code, active, is_system) VALUES
    (pg_temp.id('k1'), ten, st, 't-unused', true, false), (pg_temp.id('k2'), ten, st, 't-purchased', true, false),
    (pg_temp.id('k3'), ten, st, 't-old-rev-batched', true, false), (pg_temp.id('k4'), ten, st, 't-old-plan', false, false),
    (pg_temp.id('ks'), ten, st, 't-system', true, true);
  INSERT INTO iam_v2.internet_package_revisions (id, tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type, price_minor, currency, currency_exponent) VALUES
    (pg_temp.id('k1r'), ten, st, pg_temp.id('k1'), 1, pg_temp.id('p1r'), 'GENERAL', 500, 'USD', 2),
    (pg_temp.id('k2r'), ten, st, pg_temp.id('k2'), 1, pg_temp.id('p2r'), 'GENERAL', 500, 'USD', 2),
    (pg_temp.id('k3a'), ten, st, pg_temp.id('k3'), 1, pg_temp.id('p3r'), 'GENERAL', 500, 'USD', 2),
    (pg_temp.id('k3b'), ten, st, pg_temp.id('k3'), 2, pg_temp.id('p3r'), 'GENERAL', 700, 'USD', 2),
    -- k4's FIRST revision was built on plan p4; its current revision moved to plan p3. p4 must stay: it is the
    -- history of k4.
    (pg_temp.id('k4a'), ten, st, pg_temp.id('k4'), 1, pg_temp.id('p4r'), 'GENERAL', 500, 'USD', 2),
    (pg_temp.id('k4b'), ten, st, pg_temp.id('k4'), 2, pg_temp.id('p3r'), 'GENERAL', 500, 'USD', 2),
    (pg_temp.id('ksr'), ten, st, pg_temp.id('ks'), 1, pg_temp.id('p1r'), 'GENERAL', 0, 'USD', 2);
  UPDATE iam_v2.internet_packages SET current_revision_id = CASE id
      WHEN pg_temp.id('k1') THEN pg_temp.id('k1r') WHEN pg_temp.id('k2') THEN pg_temp.id('k2r')
      WHEN pg_temp.id('k3') THEN pg_temp.id('k3b') WHEN pg_temp.id('k4') THEN pg_temp.id('k4b')
      WHEN pg_temp.id('ks') THEN pg_temp.id('ksr') END
   WHERE tenant_id = ten;
  -- k1 carries its own configuration rows, which belong to it and must go WITH it.
  INSERT INTO iam_v2.package_eligibility_rules (tenant_id, site_id, package_revision_id, rule_type, rule_value)
    VALUES (ten, st, pg_temp.id('k1r'), 'MIN_STAY_NIGHTS', '1');
  -- use: a purchase of k2; a voucher batch printed for k3's SUPERSEDED revision. A purchase is a
  -- commerce-intent write, which the schema admits only inside an open controlled operation -- the same
  -- guard every real writer passes.
  PERFORM iam_v2.begin_controlled_operation('commerce_intent');
  INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, trigger, state, amount_minor)
    VALUES (ten, st, pg_temp.id('k2r'), 'ADMIN_GRANT', 'GRANTED', 0);
  INSERT INTO iam_v2.voucher_batches (tenant_id, site_id, package_revision_id, label)
    VALUES (ten, st, pg_temp.id('k3a'), 'old batch');
EXCEPTION WHEN others THEN
  RAISE WARNING 'FIXTURE_FAILED %', SQLERRM;
END $fx$;

CREATE OR REPLACE FUNCTION pg_temp.check(name text, ok boolean, detail text DEFAULT '') RETURNS void
LANGUAGE plpgsql AS $$ BEGIN
  IF ok THEN RAISE WARNING 'PASS %', name; ELSE RAISE WARNING 'FAIL %: %', name, detail; END IF;
END $$;
CREATE OR REPLACE FUNCTION pg_temp.try(sql text) RETURNS text LANGUAGE plpgsql AS $$
BEGIN EXECUTE sql; RETURN 'OK'; EXCEPTION WHEN others THEN RETURN SQLERRM; END $$;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA pg_temp TO PUBLIC;

-- ---- as the role the appliance actually uses --------------------------------------------------------------
SET ROLE svc_edged;

-- 1. The answer function: unused → no blockers; used → the reason and its count.
SELECT pg_temp.check('an unused package has no blockers',
  NOT EXISTS (SELECT 1 FROM iam_v2.internet_package_deletion_blockers(pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('k1'))));
SELECT pg_temp.check('a purchased package reports PURCHASES=1',
  EXISTS (SELECT 1 FROM iam_v2.internet_package_deletion_blockers(pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('k2')) b
          WHERE b.reason = 'PURCHASES' AND b.n = 1));
SELECT pg_temp.check('use of a SUPERSEDED revision counts (voucher batch on revision 1)',
  EXISTS (SELECT 1 FROM iam_v2.internet_package_deletion_blockers(pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('k3')) b
          WHERE b.reason = 'VOUCHER_BATCHES'));
SELECT pg_temp.check('a system package reports SYSTEM_PACKAGE',
  EXISTS (SELECT 1 FROM iam_v2.internet_package_deletion_blockers(pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('ks')) b
          WHERE b.reason = 'SYSTEM_PACKAGE'));
SELECT pg_temp.check('another tenant cannot see the package',
  EXISTS (SELECT 1 FROM iam_v2.internet_package_deletion_blockers(gen_random_uuid(), pg_temp.id('site'), pg_temp.id('k1')) b
          WHERE b.reason = 'NOT_FOUND'));
SELECT pg_temp.check('a plan used by a package reports PACKAGE_REVISIONS',
  EXISTS (SELECT 1 FROM iam_v2.service_plan_deletion_blockers(pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('p2')) b
          WHERE b.reason = 'PACKAGE_REVISIONS'));
SELECT pg_temp.check('a plan used only by a SUPERSEDED package revision is still in use',
  EXISTS (SELECT 1 FROM iam_v2.service_plan_deletion_blockers(pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('p4')) b
          WHERE b.reason = 'PACKAGE_REVISIONS'));

-- 2. No direct path: svc_edged cannot delete the catalogue by itself.
SELECT pg_temp.check('svc_edged cannot DELETE a package directly',
  pg_temp.try(format('DELETE FROM iam_v2.internet_packages WHERE id = %L', pg_temp.id('k1'))) ~* 'permission denied');
SELECT pg_temp.check('svc_edged cannot DELETE a revision directly',
  pg_temp.try(format('DELETE FROM iam_v2.internet_package_revisions WHERE id = %L', pg_temp.id('k1r'))) ~* 'permission denied');
SELECT pg_temp.check('setting the purge flag gives svc_edged nothing',
  pg_temp.try(format($q$SELECT set_config('iam_v2.catalogue_purge','unused',true); DELETE FROM iam_v2.internet_package_revisions WHERE id = %L$q$, pg_temp.id('k1r'))) ~* 'permission denied');

-- 3. The kernel refuses what must stay.
SELECT pg_temp.check('a purchased package is refused, naming the reason',
  pg_temp.try(format($q$SELECT iam_v2.internet_package_delete_unused(%L, %L, %L, %L, 'test delete')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('k2'), pg_temp.id('op'))) ~ 'PACKAGE_IN_USE: PURCHASES=1');
SELECT pg_temp.check('a package whose OLD revision was printed on vouchers is refused',
  pg_temp.try(format($q$SELECT iam_v2.internet_package_delete_unused(%L, %L, %L, %L, 'test delete')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('k3'), pg_temp.id('op'))) ~ 'PACKAGE_IN_USE: .*VOUCHER_BATCHES');
SELECT pg_temp.check('a system package is refused',
  pg_temp.try(format($q$SELECT iam_v2.internet_package_delete_unused(%L, %L, %L, %L, 'test delete')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('ks'), pg_temp.id('op'))) ~ 'PACKAGE_IN_USE: .*SYSTEM_PACKAGE');
SELECT pg_temp.check('a reason is required',
  pg_temp.try(format($q$SELECT iam_v2.internet_package_delete_unused(%L, %L, %L, %L, 'x')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('k1'), pg_temp.id('op'))) ~ 'CATALOGUE_DELETE_NEEDS_A_REASON');
SELECT pg_temp.check('an operator is required',
  pg_temp.try(format($q$SELECT iam_v2.internet_package_delete_unused(%L, %L, %L, NULL, 'test delete')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('k1'))) ~ 'CATALOGUE_DELETE_NEEDS_AN_OPERATOR');
SELECT pg_temp.check('another tenant''s package cannot be deleted',
  pg_temp.try(format($q$SELECT iam_v2.internet_package_delete_unused(%L, %L, %L, %L, 'test delete')$q$,
     gen_random_uuid(), pg_temp.id('site'), pg_temp.id('k1'), pg_temp.id('op'))) ~ 'PACKAGE_NOT_FOUND');
SELECT pg_temp.check('a plan used by a package is refused',
  pg_temp.try(format($q$SELECT iam_v2.service_plan_delete_unused(%L, %L, %L, %L, 'test delete')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('p2'), pg_temp.id('op'))) ~ 'PLAN_IN_USE: PACKAGE_REVISIONS');
SELECT pg_temp.check('a plan used only by a superseded revision is refused',
  pg_temp.try(format($q$SELECT iam_v2.service_plan_delete_unused(%L, %L, %L, %L, 'test delete')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('p4'), pg_temp.id('op'))) ~ 'PLAN_IN_USE');

-- 4. The kernel deletes what was never used -- the record AND its own configuration, nothing else.
SELECT pg_temp.check('an unused package is deleted',
  pg_temp.try(format($q$SELECT iam_v2.internet_package_delete_unused(%L, %L, %L, %L, 'never used')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), pg_temp.id('k1'), pg_temp.id('op'))) = 'OK');
RESET ROLE;
SELECT pg_temp.check('the package row is gone',
  NOT EXISTS (SELECT 1 FROM iam_v2.internet_packages WHERE id = pg_temp.id('k1')));
SELECT pg_temp.check('its revision is gone',
  NOT EXISTS (SELECT 1 FROM iam_v2.internet_package_revisions WHERE id = pg_temp.id('k1r')));
SELECT pg_temp.check('its eligibility rule went with it',
  NOT EXISTS (SELECT 1 FROM iam_v2.package_eligibility_rules WHERE package_revision_id = pg_temp.id('k1r')));
SELECT pg_temp.check('its plan was NOT removed (plans are deleted on their own)',
  EXISTS (SELECT 1 FROM iam_v2.service_plans WHERE id = pg_temp.id('p1')));
SELECT pg_temp.check('the refused packages are all still there, untouched',
  (SELECT count(*) FROM iam_v2.internet_packages WHERE id IN (pg_temp.id('k2'), pg_temp.id('k3'), pg_temp.id('k4'), pg_temp.id('ks'))) = 4
  AND (SELECT count(*) FROM iam_v2.internet_package_revisions WHERE package_id IN (pg_temp.id('k2'), pg_temp.id('k3'), pg_temp.id('k4'), pg_temp.id('ks'))) = 6
  AND (SELECT count(*) FROM iam_v2.purchases WHERE package_revision_id = pg_temp.id('k2r')) = 1
  AND (SELECT count(*) FROM iam_v2.voucher_batches WHERE package_revision_id = pg_temp.id('k3a')) = 1);

-- p1 is still used by the SYSTEM package ks, so it is refused; p... none unused yet. Make one: a fresh plan.
INSERT INTO iam_v2.service_plans (id, tenant_id, site_id, code, enabled)
  VALUES ('44444444-4444-4444-8444-444444444444', pg_temp.id('ten'), pg_temp.id('site'), 't-plan-unused', true);
INSERT INTO iam_v2.service_plan_revisions (id, tenant_id, site_id, service_plan_id, revision_no, name, down_kbps, up_kbps,
    max_concurrent_devices, device_limit_policy, time_accounting_mode, speed_allocation)
  VALUES ('55555555-5555-4555-8555-555555555555', pg_temp.id('ten'), pg_temp.id('site'), '44444444-4444-4444-8444-444444444444', 1,
          'Unused', 1000, 500, 1, 'REJECT_NEW_DEVICE', 'VALIDITY_WINDOW', 'PER_DEVICE');
UPDATE iam_v2.service_plans SET current_revision_id = '55555555-5555-4555-8555-555555555555'
 WHERE id = '44444444-4444-4444-8444-444444444444';
SET ROLE svc_edged;
SELECT pg_temp.check('an unused plan is deleted',
  pg_temp.try(format($q$SELECT iam_v2.service_plan_delete_unused(%L, %L, %L, %L, 'never used')$q$,
     pg_temp.id('ten'), pg_temp.id('site'), '44444444-4444-4444-8444-444444444444', pg_temp.id('op'))) = 'OK');
RESET ROLE;
SELECT pg_temp.check('the plan and its revision are gone',
  NOT EXISTS (SELECT 1 FROM iam_v2.service_plans WHERE id = '44444444-4444-4444-8444-444444444444')
  AND NOT EXISTS (SELECT 1 FROM iam_v2.service_plan_revisions WHERE id = '55555555-5555-4555-8555-555555555555'));

-- 5. Revisions stay immutable to everyone else -- even the owner, and even with the flag, may not UPDATE.
SELECT pg_temp.check('the superuser without the flag cannot delete a revision',
  pg_temp.try(format('DELETE FROM iam_v2.internet_package_revisions WHERE id = %L', pg_temp.id('k2r'))) ~ 'immutable');
SELECT pg_temp.check('the superuser WITH the flag cannot delete a revision (not the owner)',
  pg_temp.try(format($q$SELECT set_config('iam_v2.catalogue_purge','unused',true); DELETE FROM iam_v2.internet_package_revisions WHERE id = %L$q$, pg_temp.id('k4a'))) ~ 'immutable');
SET ROLE iam_v2_owner;
SELECT pg_temp.check('the owner WITH the flag still cannot UPDATE a revision',
  pg_temp.try(format($q$SELECT set_config('iam_v2.catalogue_purge','unused',true); UPDATE iam_v2.internet_package_revisions SET price_minor = 1 WHERE id = %L$q$, pg_temp.id('k2r'))) ~ 'immutable');
SELECT pg_temp.check('the owner WITH the flag still cannot delete a USED revision (the foreign key refuses)',
  pg_temp.try(format($q$SELECT set_config('iam_v2.catalogue_purge','unused',true); DELETE FROM iam_v2.internet_package_revisions WHERE id = %L$q$, pg_temp.id('k2r'))) ~* 'foreign key|violates');
RESET ROLE;
SELECT pg_temp.check('nothing used was lost', (SELECT count(*) FROM iam_v2.internet_package_revisions WHERE id IN (pg_temp.id('k2r'), pg_temp.id('k4a'))) = 2);
\echo DONE
