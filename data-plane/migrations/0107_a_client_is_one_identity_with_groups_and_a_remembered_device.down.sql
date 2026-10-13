-- 0107 down -- removes what 0107 added. Identity rows keep their attrs data only as long as the column exists;
-- dropping it discards verified claims, which is the documented cost of rolling this migration back.
BEGIN;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    REVOKE UPDATE (attrs) ON iam_v2.guest_principal_identities FROM svc_scd;
    REVOKE ALL ON iam_v2.client_groups, iam_v2.client_group_rules FROM svc_scd;
    REVOKE ALL ON iam_v2.principal_device_credentials FROM svc_scd;
    REVOKE ALL ON iam_v2.notification_provider_secret_generations FROM svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    REVOKE ALL ON iam_v2.client_groups, iam_v2.client_group_rules, iam_v2.client_group_changes FROM svc_edged;
  END IF;
END $g$;

ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_client_group_fk;
ALTER TABLE iam_v2.auth_contexts DROP COLUMN IF EXISTS client_group_evidence;
ALTER TABLE iam_v2.auth_contexts DROP COLUMN IF EXISTS client_group_id;

DROP TABLE IF EXISTS iam_v2.notification_provider_secret_generations;
DROP TABLE IF EXISTS iam_v2.principal_device_credentials;
DROP TRIGGER IF EXISTS client_group_changes_append_only ON iam_v2.client_group_changes;
DROP FUNCTION IF EXISTS iam_v2.client_group_changes_append_only();
DROP TABLE IF EXISTS iam_v2.client_group_changes;
DROP TABLE IF EXISTS iam_v2.client_group_rules;
DROP TABLE IF EXISTS iam_v2.client_groups;

ALTER TABLE iam_v2.guest_principal_identities DROP COLUMN IF EXISTS attrs;

-- The smtp kind is withdrawn only where this role owns the table (0099's rule); a live appliance that applied
-- deploy/scripts/extend-notification-kinds-smtp.sql removes it the same way. Any smtp row must be deleted first.
DO $smtp_kind$
DECLARE v_owner oid;
BEGIN
  SELECT relowner INTO v_owner FROM pg_class WHERE oid = 'public.notification_providers'::regclass;
  IF NOT pg_has_role(current_user, v_owner, 'USAGE') THEN
    RAISE NOTICE 'public.notification_providers is not owned by %; the smtp kind stays until the owner removes it', current_user;
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM public.notification_providers WHERE kind = 'smtp') THEN
    RAISE EXCEPTION 'notification_providers still holds an smtp row; delete it before rolling 0107 back';
  END IF;
  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_kind_check';
  EXECUTE $c$ALTER TABLE public.notification_providers ADD CONSTRAINT notification_providers_kind_check
    CHECK (kind = ANY (ARRAY['stub'::text, 'sendgrid'::text, 'ses'::text, 'twilio'::text,
                             'meta_whatsapp'::text, 'twilio_whatsapp'::text]))$c$;
END $smtp_kind$;

COMMIT;
