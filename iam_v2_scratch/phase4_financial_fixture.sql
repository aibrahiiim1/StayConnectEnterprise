-- Phase-4 financial fixture. Runs AFTER migration 0011 on a disposable scratch database ONLY.
--
-- Financial onboarding is a REVISION EVENT, not an edit. pms_interface_revisions is immutable (mg9
-- imm_pms_rev), so giving a property its authoritative financial currency means publishing a NEW revision
-- and repointing the interface at it. Every posting that pinned an older revision keeps pointing at the
-- currency it was actually authorized under — which is the entire reason G2 lives on the revision.
--
-- What this builds, all deterministic all-hex UUIDs:
--   IF1 revision 3  — financially onboarded, USD/2, GLOBALLY_UNIQUE          (the executable interface)
--   IF1 revision 4  — folio strategy set but NO currency                     (Tier-2 not onboarded)
--   IF2 + rev 1/2   — a SECOND independent interface, EUR/2                  (lane + namespace isolation)
--   PKG2 rev        — a EUR package on IF1                                   (currency-mismatch fixture)
--   PKG3 rev        — a USD package with exponent 3                          (exponent-mismatch fixture)
-- plus the stay/folio/purchase/settlement chain each of them needs so that a refusal below is always the
-- refusal under test and never a missing row.
BEGIN;

-- ---------------------------------------------------------------- IF1: onboarded revision (USD/2)
INSERT INTO iam_v2.pms_interface_revisions
  (id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config,
   financial_base_currency,financial_base_currency_exponent) VALUES
 ('aaaa0000-0000-0000-0000-0000000000d3','11111111-1111-1111-1111-111111111111',
  '22222222-2222-2222-2222-222222222222','aaaa0000-0000-0000-0000-000000000001',3,'UTC','GLOBALLY_UNIQUE','{"heartbeat_timeout_ms":60000,"feed_freshness_ms":300000,"complete_sync_ms":3600000}',
  'USD',2),
-- revision 4: folio strategy is fine, but the property was never financially onboarded. This is the row
-- that proves INTERFACE_CURRENCY_NOT_ONBOARDED is a currency refusal and not a folio refusal.
 ('aaaa0000-0000-0000-0000-0000000000d4','11111111-1111-1111-1111-111111111111',
  '22222222-2222-2222-2222-222222222222','aaaa0000-0000-0000-0000-000000000001',4,'UTC','GLOBALLY_UNIQUE','{"heartbeat_timeout_ms":60000,"feed_freshness_ms":300000,"complete_sync_ms":3600000}',
  NULL,NULL);
UPDATE iam_v2.pms_interfaces SET current_revision_id='aaaa0000-0000-0000-0000-0000000000d3'
 WHERE id='aaaa0000-0000-0000-0000-000000000001';

-- ---------------------------------------------------------------- IF2: a second, independent interface (EUR/2)
INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind) VALUES
 ('aaaa0000-0000-0000-0000-000000000002','11111111-1111-1111-1111-111111111111',
  '22222222-2222-2222-2222-222222222222','protel-fias');
INSERT INTO iam_v2.pms_interface_revisions
  (id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config,
   financial_base_currency,financial_base_currency_exponent) VALUES
 ('aaaa0000-0000-0000-0000-0000000002d1','11111111-1111-1111-1111-111111111111',
  '22222222-2222-2222-2222-222222222222','aaaa0000-0000-0000-0000-000000000002',1,'UTC','GLOBALLY_UNIQUE','{"heartbeat_timeout_ms":60000,"feed_freshness_ms":300000,"complete_sync_ms":3600000}',
  'EUR',2);
UPDATE iam_v2.pms_interfaces SET current_revision_id='aaaa0000-0000-0000-0000-0000000002d1'
 WHERE id='aaaa0000-0000-0000-0000-000000000002';

-- ---------------------------------------------------------------- packages for the mismatch fixtures
INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code) VALUES
 ('cccc0000-0000-0000-0000-000000000002','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','PKG_EUR'),
 ('cccc0000-0000-0000-0000-000000000003','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','PKG_EXP3');
INSERT INTO iam_v2.internet_package_revisions
  (id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent) VALUES
 ('cccc0000-0000-0000-0000-0000000002d1','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'cccc0000-0000-0000-0000-000000000002',1,'bbbb0000-0000-0000-0000-0000000000d1','GENERAL',100,'EUR',2),
 ('cccc0000-0000-0000-0000-0000000003d1','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'cccc0000-0000-0000-0000-000000000003',1,'bbbb0000-0000-0000-0000-0000000000d1','GENERAL',100,'USD',3);
UPDATE iam_v2.internet_packages SET current_revision_id='cccc0000-0000-0000-0000-0000000002d1'
 WHERE id='cccc0000-0000-0000-0000-000000000002';
UPDATE iam_v2.internet_packages SET current_revision_id='cccc0000-0000-0000-0000-0000000003d1'
 WHERE id='cccc0000-0000-0000-0000-000000000003';

INSERT INTO iam_v2.package_settlement_mappings
  (id,tenant_id,site_id,package_revision_id,pms_interface_id,mapping_revision,posting_code) VALUES
 ('dddd0000-0000-0000-0000-000000000002','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'cccc0000-0000-0000-0000-0000000002d1','aaaa0000-0000-0000-0000-000000000001',1,'WIFI'),
 ('dddd0000-0000-0000-0000-000000000003','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'cccc0000-0000-0000-0000-0000000003d1','aaaa0000-0000-0000-0000-000000000001',1,'WIFI'),
 -- the IF1 USD package also mapped onto IF2, so the IF2 chain is complete
 ('dddd0000-0000-0000-0000-000000000004','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'cccc0000-0000-0000-0000-0000000002d1','aaaa0000-0000-0000-0000-000000000002',1,'WIFI');

-- ---------------------------------------------------------------- IF2 stay/folio chain
INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,posting_allowed) VALUES
 ('eeee0000-0000-0000-0000-000000000002','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'aaaa0000-0000-0000-0000-000000000002','RES2','S2','IN_HOUSE',true);
INSERT INTO iam_v2.folios(id,tenant_id,site_id,pms_interface_id,external_folio_id) VALUES
 ('eeee0000-0000-0000-0000-0000000000f2','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'aaaa0000-0000-0000-0000-000000000002','F2');

-- ---------------------------------------------------------------- purchases + settlements
-- P_EUR_IF1  : EUR purchase/package against the USD interface  -> PACKAGE/PURCHASE currency mismatch
-- P_EXP3_IF1 : USD purchase with exponent 3 against USD/2      -> exponent mismatch
-- P_IF2      : the complete EUR chain on the second interface  -> lane isolation
INSERT INTO iam_v2.purchases
  (id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,
   amount_minor,currency,currency_exponent,state) VALUES
 ('99990000-0000-0000-0000-000000000002','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'cccc0000-0000-0000-0000-0000000002d1','aaaa0000-0000-0000-0000-000000000001','eeee0000-0000-0000-0000-000000000001',
  'dddd0000-0000-0000-0000-000000000002','VOUCHER_REDEMPTION',100,'EUR',2,'GRANTED'),
 ('99990000-0000-0000-0000-000000000003','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'cccc0000-0000-0000-0000-0000000003d1','aaaa0000-0000-0000-0000-000000000001','eeee0000-0000-0000-0000-000000000001',
  'dddd0000-0000-0000-0000-000000000003','VOUCHER_REDEMPTION',100,'USD',3,'GRANTED'),
 ('99990000-0000-0000-0000-000000000004','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'cccc0000-0000-0000-0000-0000000002d1','aaaa0000-0000-0000-0000-000000000002','eeee0000-0000-0000-0000-000000000002',
  'dddd0000-0000-0000-0000-000000000004','VOUCHER_REDEMPTION',100,'EUR',2,'GRANTED');
INSERT INTO iam_v2.settlements(id,tenant_id,site_id,purchase_id,method,status) VALUES
 ('99990000-0000-0000-0000-0000000000d2','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  '99990000-0000-0000-0000-000000000002','PMS_POSTING','REQUIRED'),
 ('99990000-0000-0000-0000-0000000000d3','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  '99990000-0000-0000-0000-000000000003','PMS_POSTING','REQUIRED'),
 ('99990000-0000-0000-0000-0000000000d4','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  '99990000-0000-0000-0000-000000000004','PMS_POSTING','REQUIRED');

-- ---------------------------------------------------------------- PMS runtime state: all four axes green
-- Migration 0012 makes the financial boundary consult the SAME Phase-3 runtime axes the resolver uses. A
-- fixture without a runtime row would fail closed with RUNTIME_UNKNOWN, which is correct behaviour but
-- would make every other check untestable, so the fixture records a healthy interface explicitly.
INSERT INTO iam_v2.pms_interface_runtime
  (tenant_id,site_id,pms_interface_id,pinned_revision_id,credential_mode,runtime_generation,
   transport_status,last_connected_at,last_heartbeat_at,
   continuity_status,last_valid_event_at,
   sync_status,last_complete_sync_at,
   resync_generation_seq,published_resync_generation) VALUES
 ('11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'aaaa0000-0000-0000-0000-000000000001','aaaa0000-0000-0000-0000-0000000000d3','NONE',1,
  'CONNECTED',now(),now(),'CONTINUOUS',now(),'IN_SYNC',now(),0,0),
 ('11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
  'aaaa0000-0000-0000-0000-000000000002','aaaa0000-0000-0000-0000-0000000002d1','NONE',1,
  'CONNECTED',now(),now(),'CONTINUOUS',now(),'IN_SYNC',now(),0,0);

COMMIT;
