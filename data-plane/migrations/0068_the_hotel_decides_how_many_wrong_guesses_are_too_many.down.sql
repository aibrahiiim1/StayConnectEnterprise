-- Roll back 0068: remove guest sign-in protection.
--
-- WHAT ROLLING BACK COSTS. Guest room sign-in returns to having no rate control that a property can see or
-- change: an attacker may guess room numbers and surnames at whatever rate the network allows, and nobody at
-- the desk can tell that it is happening. Every active restriction is lifted immediately, which is the one
-- part of this that is guest-visible and benign. The policy history goes with the table -- it describes a
-- control that no longer exists, so keeping it would be retaining a record of nothing.
--
-- Existing authorised sessions are untouched, because nothing here ever read or wrote them.
--
-- This exists for completeness of the migration chain, not as an operational step.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.guest_signin_release(uuid,uuid,uuid,text,text);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_note_success(uuid,uuid,macaddr);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_note_failure(uuid,uuid,macaddr,uuid,text,text);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_gate(uuid,uuid,macaddr);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_protection_set(uuid,uuid,integer,integer,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.guest_signin_protection_get(uuid,uuid);

DROP INDEX IF EXISTS iam_v2.sign_in_attempts_device_recent;
DROP INDEX IF EXISTS iam_v2.guest_signin_restrictions_active;
DROP TABLE IF EXISTS iam_v2.guest_signin_restrictions;

DROP TRIGGER IF EXISTS guest_signin_protection_changes_append_only
  ON iam_v2.guest_signin_protection_changes;
DROP INDEX IF EXISTS iam_v2.guest_signin_protection_changes_lookup;
DROP TABLE IF EXISTS iam_v2.guest_signin_protection_changes;
DROP FUNCTION IF EXISTS iam_v2.guest_signin_protection_changes_append_only();
DROP TABLE IF EXISTS iam_v2.site_guest_signin_protection;

COMMIT;
