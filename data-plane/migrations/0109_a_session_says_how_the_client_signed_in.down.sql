-- 0109 down -- drops the two credential labels. Sessions already opened keep the credential_method they were
-- given (a text column that never had a CHECK); nothing else references the labels.
BEGIN;
ALTER TABLE iam_v2.principal_device_credentials DROP CONSTRAINT IF EXISTS principal_device_credentials_label;
ALTER TABLE iam_v2.principal_device_credentials DROP COLUMN IF EXISTS credential;
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_credential_label;
ALTER TABLE iam_v2.auth_contexts DROP COLUMN IF EXISTS credential;
COMMIT;
