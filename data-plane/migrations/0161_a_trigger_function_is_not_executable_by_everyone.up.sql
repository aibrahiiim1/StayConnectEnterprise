-- 0161 — A TRIGGER FUNCTION IS NOT EXECUTABLE BY EVERYONE.
--
-- 0152 created iam_v2.profile_privacy_notice_stamp() (SECURITY DEFINER, owner iam_v2_owner) without revoking the
-- default EXECUTE from PUBLIC, so every role -- the payment runtime included -- could execute an unreviewed definer
-- (TestIntegrationDefinerAbuse_TheRuntimeDefinerSurfaceIsExactlyWhatWeThinkItIs). A trigger fires without the inserting
-- role holding EXECUTE, so revoking it changes nothing about the trigger on iam_v2.auth_contexts.

BEGIN;
REVOKE ALL ON FUNCTION iam_v2.profile_privacy_notice_stamp() FROM PUBLIC;
COMMIT;
