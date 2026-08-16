-- Reverse of 0031. Removes only what 0031 created.
--
-- The bindings 0031 released are NOT re-authorized. A rollback undoes the mechanism, not the history: a slot
-- a guest released really was released, the device really did stop being authorized at that instant, and
-- rewriting those rows would fabricate an authorization that never existed. That is the same rule the
-- Phase-4 and Phase-5 down migrations follow.
BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p6_guest_release_device(uuid, uuid, int);

DROP TRIGGER IF EXISTS p6_guest_device_actions_append_only ON iam_v2.guest_device_actions;
DROP FUNCTION IF EXISTS iam_v2.p6_guest_device_actions_append_only();
DROP TABLE IF EXISTS iam_v2.guest_device_actions;

DELETE FROM public.schema_migrations WHERE version = '0031_phase6_guest_device_self_service';

COMMIT;
