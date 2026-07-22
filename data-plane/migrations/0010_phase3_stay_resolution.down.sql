-- 0010 DOWN — reverse of 0010_phase3_stay_resolution.up.sql. Additive rollback. Drops in reverse
-- dependency order: ledger row + triggers/functions first, then indexes/constraints, then added
-- columns, then the runtime table last. DARK; no Production data.
BEGIN;

DELETE FROM public.schema_migrations WHERE version = '0010_phase3_stay_resolution';

-- (2)+(6) triggers + functions first
DROP TRIGGER IF EXISTS p3_stay_lifecycle_guard ON iam_v2.stays;
DROP FUNCTION IF EXISTS iam_v2.p3_stay_lifecycle_guard();
DROP TRIGGER IF EXISTS p3_stay_event_guard ON iam_v2.stay_events;
DROP FUNCTION IF EXISTS iam_v2.p3_stay_event_appendonly();

-- (4g) reserved-namespace + emergency bootstrap/health
DROP TRIGGER IF EXISTS p3_reserved_grace_plan ON iam_v2.service_plans;
DROP TRIGGER IF EXISTS p3_reserved_grace_pkg ON iam_v2.internet_packages;
DROP FUNCTION IF EXISTS iam_v2.p3_reserved_grace_codes();
DROP FUNCTION IF EXISTS iam_v2.bootstrap_emergency_grace(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.emergency_grace_health(uuid,uuid);

-- (4i/4j/4k) grace-config publish + alert-action model + audit provenance
DROP TRIGGER IF EXISTS p3_grace_config_version_guard ON iam_v2.site_checkout_grace_config;
DROP FUNCTION IF EXISTS iam_v2.p3_grace_config_version_guard();
DROP FUNCTION IF EXISTS iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int);
DROP FUNCTION IF EXISTS iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text);
DROP TRIGGER IF EXISTS p3_checkout_audit_provenance ON iam_v2.checkout_grace_audit;
DROP FUNCTION IF EXISTS iam_v2.p3_checkout_audit_provenance();
DROP VIEW IF EXISTS iam_v2.active_operational_alerts; -- depends on checkout_grace_alert_actions
DROP TRIGGER IF EXISTS p3_alert_action_insert ON iam_v2.checkout_grace_alert_actions;
DROP TRIGGER IF EXISTS p3_alert_action_appendonly ON iam_v2.checkout_grace_alert_actions;
DROP FUNCTION IF EXISTS iam_v2.p3_alert_action_guard();
DROP TRIGGER IF EXISTS p3_alert_open_on_audit ON iam_v2.checkout_grace_audit;
DROP FUNCTION IF EXISTS iam_v2.p3_alert_open_on_audit();
DROP FUNCTION IF EXISTS iam_v2.record_alert_action(uuid,uuid,uuid,text,uuid,text,text);
DROP FUNCTION IF EXISTS iam_v2.publish_checkout_grace_policy(uuid,uuid,uuid,int,int,int,bigint,int,text,int,int,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.grace_package_matches_policy(uuid,uuid,uuid,int,int,int,bigint,int,text);
DROP FUNCTION IF EXISTS iam_v2.selectable_grace_packages(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.grace_package_mismatch_reason(uuid,uuid,uuid,int,int,int,bigint,int,text);
DROP TABLE IF EXISTS iam_v2.checkout_grace_policy_publications;
DROP TABLE IF EXISTS iam_v2.checkout_grace_alert_actions;

-- (4d/4e) history tables + guards (append-only + insert state machines) + controlled transition
DROP TRIGGER IF EXISTS p3_entitlement_controlled_writer ON iam_v2.entitlements;
DROP TRIGGER IF EXISTS p3_est_controlled_writer ON iam_v2.entitlement_state_transitions;
DROP TRIGGER IF EXISTS p3_grace_config_controlled_writer ON iam_v2.site_checkout_grace_config;
-- Every controlled-writer guard 0010 attached. Three of these tables PRE-DATE 0010 and survive the rollback,
-- so their triggers must be dropped by name or the guard function cannot be dropped and the whole rollback
-- fails. The fourth table is dropped by this migration, but only later in the script — naming its trigger too
-- makes the rollback independent of statement order rather than quietly dependent on it.
DROP TRIGGER IF EXISTS p3_accounting_records_controlled_writer ON iam_v2.accounting_records;
-- accounting_checkpoints IS dropped by this migration, but not until later in the script — and the function
-- drop comes first. Dropping its trigger explicitly makes the rollback independent of statement order.
DROP TRIGGER IF EXISTS p3_accounting_checkpoints_controlled_writer ON iam_v2.accounting_checkpoints;
DROP TRIGGER IF EXISTS p3_delayed_accounting_controlled_writer ON iam_v2.delayed_accounting_records;
DROP TRIGGER IF EXISTS p3_class_generation_controlled_writer ON iam_v2.appliance_class_generation;
DROP TRIGGER IF EXISTS p3_auth_context_offers_controlled_writer ON iam_v2.auth_context_offers;
DROP TRIGGER IF EXISTS p3_session_usage_controlled_writer ON iam_v2.sessions;
DROP FUNCTION IF EXISTS iam_v2.p3_controlled_writer_only();
DROP FUNCTION IF EXISTS iam_v2.p3_controlled_writer_owner(text);
DROP TRIGGER IF EXISTS p3_entitlement_status_coherent ON iam_v2.entitlements;
DROP FUNCTION IF EXISTS iam_v2.p3_entitlement_status_coherent();
DROP FUNCTION IF EXISTS iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text);
DROP FUNCTION IF EXISTS iam_v2.supersede_entitlement_transition(uuid,text,timestamptz,text);
DROP FUNCTION IF EXISTS iam_v2.terminate_entitlement_at_boundary(uuid,timestamptz,text);
DROP FUNCTION IF EXISTS iam_v2.authorize_entitlement_device(uuid,uuid,timestamptz);
DROP FUNCTION IF EXISTS iam_v2.deauthorize_entitlement_device(uuid,uuid,timestamptz,text);
-- (4m) accounting attribution: delayed-record detection, watermarks, session binding intervals
DROP TRIGGER IF EXISTS p3_detect_delayed_accounting ON iam_v2.accounting_records;
DROP TRIGGER IF EXISTS p3_accounting_needs_binding ON iam_v2.accounting_records;
DROP FUNCTION IF EXISTS iam_v2.p3_accounting_needs_binding();
DROP FUNCTION IF EXISTS iam_v2.p3_detect_delayed_accounting();
DROP FUNCTION IF EXISTS iam_v2.p3_entitlement_at(uuid,timestamptz);
DROP FUNCTION IF EXISTS iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz);
DROP FUNCTION IF EXISTS iam_v2.register_class_origin(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz);
DROP FUNCTION IF EXISTS iam_v2.allocate_class_generation(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.record_auth_context_offer(uuid,uuid,uuid,uuid,int,bigint,timestamptz);
DROP FUNCTION IF EXISTS iam_v2.issue_or_return_pms_context(uuid,uuid,uuid,uuid,uuid,uuid,uuid,uuid,int);
DROP INDEX IF EXISTS iam_v2.ac_one_live_per_resolution;
ALTER TABLE iam_v2.auth_contexts DROP COLUMN IF EXISTS resolution_request_id;
DROP TABLE IF EXISTS iam_v2.accounting_checkpoints;
DROP TABLE IF EXISTS iam_v2.auth_context_offers;
ALTER TABLE iam_v2.stays DROP COLUMN IF EXISTS rate_plan;
DROP TABLE IF EXISTS iam_v2.appliance_class_generation;
DROP FUNCTION IF EXISTS iam_v2.p3_expected_class_minor(inet);
ALTER TABLE iam_v2.accounting_records DROP COLUMN IF EXISTS ingested_at;
DROP TABLE IF EXISTS iam_v2.delayed_accounting_records;
DROP TABLE IF EXISTS iam_v2.entitlement_boundary_watermarks;
DROP FUNCTION IF EXISTS iam_v2.entitlement_usage_bytes(uuid,timestamptz);
DROP FUNCTION IF EXISTS iam_v2.rebind_session_entitlement(uuid,uuid,timestamptz);
DROP TRIGGER IF EXISTS p3_session_open_binding ON iam_v2.sessions;
DROP FUNCTION IF EXISTS iam_v2.p3_session_open_binding();
DROP TRIGGER IF EXISTS p3_session_close_binding ON iam_v2.sessions;
DROP FUNCTION IF EXISTS iam_v2.p3_session_close_binding();
DROP TRIGGER IF EXISTS p3_seb_insert ON iam_v2.session_entitlement_bindings;
DROP FUNCTION IF EXISTS iam_v2.p3_seb_insert_guard();
DROP TRIGGER IF EXISTS p3_seb_appendonly ON iam_v2.session_entitlement_bindings;
DROP FUNCTION IF EXISTS iam_v2.p3_seb_appendonly();
DROP TABLE IF EXISTS iam_v2.session_entitlement_bindings;
DROP FUNCTION IF EXISTS iam_v2.p3_rederive_entitlement_times(uuid);
DROP TRIGGER IF EXISTS p3_est_appendonly ON iam_v2.entitlement_state_transitions;
DROP TRIGGER IF EXISTS p3_est_insert ON iam_v2.entitlement_state_transitions;
DROP FUNCTION IF EXISTS iam_v2.p3_est_insert_guard();
DROP TRIGGER IF EXISTS p3_eda_appendonly ON iam_v2.entitlement_device_authorizations;
DROP TRIGGER IF EXISTS p3_eda_insert ON iam_v2.entitlement_device_authorizations;
DROP FUNCTION IF EXISTS iam_v2.p3_eda_insert_guard();
DROP FUNCTION IF EXISTS iam_v2.p3_history_appendonly();
DROP TABLE IF EXISTS iam_v2.entitlement_device_authorizations;
DROP TABLE IF EXISTS iam_v2.entitlement_state_transitions;

-- (4h) stays lineage pin
ALTER TABLE iam_v2.stays DROP CONSTRAINT IF EXISTS stays_last_applied_event_scoped;
ALTER TABLE iam_v2.stays DROP COLUMN IF EXISTS last_applied_event_id;
ALTER TABLE iam_v2.stay_events DROP CONSTRAINT IF EXISTS stay_events_scoped_identity;

-- (4c) checkout_grace_audit alert view + append-only guard + table
DROP VIEW IF EXISTS iam_v2.active_operational_alerts;
DROP TRIGGER IF EXISTS p3_checkout_grace_audit_guard ON iam_v2.checkout_grace_audit;
DROP FUNCTION IF EXISTS iam_v2.p3_checkout_grace_audit_appendonly();
DROP TABLE IF EXISTS iam_v2.checkout_grace_audit;

-- (6b) §G durable resync inbox: partial indexes + admission columns, then restore the baseline unique.
DROP INDEX IF EXISTS iam_v2.se_resync_identity;
DROP INDEX IF EXISTS iam_v2.se_live_identity;
ALTER TABLE iam_v2.stay_events DROP CONSTRAINT IF EXISTS se_admission_coherent;
ALTER TABLE iam_v2.stay_events
  DROP COLUMN IF EXISTS fingerprint_key_version,
  DROP COLUMN IF EXISTS resync_generation,
  DROP COLUMN IF EXISTS admission_runtime_generation,
  DROP COLUMN IF EXISTS admission_kind;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint
                 WHERE conrelid = 'iam_v2.stay_events'::regclass AND contype = 'u'
                   AND pg_get_constraintdef(oid) LIKE '%external_event_identity%') THEN
    -- restore the EXACT baseline auto-generated constraint name so the rolled-back catalog matches pre-0010
    ALTER TABLE iam_v2.stay_events
      ADD CONSTRAINT stay_events_tenant_id_site_id_pms_interface_id_external_eve_key
      UNIQUE (tenant_id, site_id, pms_interface_id, external_event_identity);
  END IF;
END$$;

-- (6) stay_events application-result columns
ALTER TABLE iam_v2.stay_events
  DROP COLUMN IF EXISTS review_code,
  DROP COLUMN IF EXISTS processed_at;

-- (4f) grace config version
ALTER TABLE iam_v2.site_checkout_grace_config DROP COLUMN IF EXISTS config_version;

-- (4b) grace scalars: constraints then columns
ALTER TABLE iam_v2.site_checkout_grace_config DROP CONSTRAINT IF EXISTS grace_config_no_dup_policy_keys;
ALTER TABLE iam_v2.site_checkout_grace_config DROP CONSTRAINT IF EXISTS grace_all_or_none;
ALTER TABLE iam_v2.site_checkout_grace_config DROP CONSTRAINT IF EXISTS grace_bounds;
ALTER TABLE iam_v2.site_checkout_grace_config
  DROP COLUMN IF EXISTS grace_device_limit_policy,
  DROP COLUMN IF EXISTS grace_device_limit,
  DROP COLUMN IF EXISTS grace_data_quota_bytes,
  DROP COLUMN IF EXISTS grace_up_kbps,
  DROP COLUMN IF EXISTS grace_down_kbps,
  DROP COLUMN IF EXISTS grace_duration_seconds,
  DROP COLUMN IF EXISTS eligibility_window_seconds;

-- (5b) auth_contexts occupancy-evidence + episode pins
ALTER TABLE iam_v2.auth_contexts
  DROP COLUMN IF EXISTS pinned_occupancy_evidence_version,
  DROP COLUMN IF EXISTS pinned_lifecycle_version;

-- (5) auth_resolutions idempotency key: index then column
DROP INDEX IF EXISTS iam_v2.auth_resolutions_req_idem;
ALTER TABLE iam_v2.auth_resolutions DROP COLUMN IF EXISTS resolution_request_id;

-- (4) stays effective-checkout + occupancy evidence: index + constraints then columns
DROP INDEX IF EXISTS iam_v2.stays_effective_checkout;
ALTER TABLE iam_v2.stays
  DROP CONSTRAINT IF EXISTS stays_occupancy_revision_fk,
  DROP CONSTRAINT IF EXISTS stays_evidence_version_coherent,
  DROP CONSTRAINT IF EXISTS stays_occupancy_norm_pos,
  DROP CONSTRAINT IF EXISTS stays_occupancy_all_or_none,
  DROP CONSTRAINT IF EXISTS stays_checkedout_needs_boundary,
  DROP CONSTRAINT IF EXISTS stays_effco_only_after_checkout;
ALTER TABLE iam_v2.stays
  DROP COLUMN IF EXISTS occupancy_evidence_version,
  DROP COLUMN IF EXISTS occupancy_clock_suspect,
  DROP COLUMN IF EXISTS occupancy_normalization_version,
  DROP COLUMN IF EXISTS occupancy_revision_id,
  DROP COLUMN IF EXISTS occupancy_ingested_at,
  DROP COLUMN IF EXISTS occupancy_evidence_at,
  DROP COLUMN IF EXISTS effective_checkout_at;

-- (1) runtime table last (its index + constraints drop with it)
DROP TABLE IF EXISTS iam_v2.pms_interface_runtime;

COMMIT;
