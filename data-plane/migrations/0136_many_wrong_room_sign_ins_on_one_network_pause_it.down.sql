-- 0136 down: no network pause. The pauses, confirmations and settings are dropped with their tables.

BEGIN;
DROP FUNCTION IF EXISTS iam_v2.guest_signin_network_prune();
DROP FUNCTION IF EXISTS iam_v2.guest_signin_network_pause_devices(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_confirm_device(uuid,uuid,uuid,macaddr,text,text);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_end_network_pause(uuid,uuid,uuid,text,text);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_note_network_failure(uuid,uuid,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_network_gate(uuid,uuid,uuid,macaddr);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_network_policy_set(uuid,uuid,integer,integer,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_network_policy_get(uuid,uuid);
DROP INDEX IF EXISTS iam_v2.sign_in_attempts_network_recent;
DROP TABLE IF EXISTS iam_v2.guest_signin_device_passes;
DROP TABLE IF EXISTS iam_v2.guest_signin_network_pauses;
DROP TABLE IF EXISTS iam_v2.guest_signin_network_policy_changes;
DROP FUNCTION IF EXISTS iam_v2.guest_signin_network_policy_changes_append_only();
DROP TABLE IF EXISTS iam_v2.site_guest_signin_network_policy;
COMMIT;
