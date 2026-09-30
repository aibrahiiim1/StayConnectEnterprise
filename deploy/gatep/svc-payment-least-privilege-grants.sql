-- Gate-P: explicit grants that replace a PUBLIC EXECUTE revoked by migration 0096.

-- LEAST PRIVILEGE FOR THE PAYMENT LOGINS (migration 0096). These nine definer functions were reachable through
-- PUBLIC. PUBLIC is revoked in 0096; the service roles that held them keep them here, explicitly, so a
-- reconcile keeps exactly the pre-0096 access and svc_payment / svc_payment_outcome gain nothing.
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid,uuid,uuid,bigint) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_site_blocked_after_refusals(uuid,uuid) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid,uuid,uuid,bigint) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_site_blocked_after_refusals(uuid,uuid) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid,uuid,uuid,bigint) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_site_blocked_after_refusals(uuid,uuid) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid,uuid,uuid,bigint) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_site_blocked_after_refusals(uuid,uuid) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid,uuid,uuid,bigint) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_site_blocked_after_refusals(uuid,uuid) TO svc_pmsd;
