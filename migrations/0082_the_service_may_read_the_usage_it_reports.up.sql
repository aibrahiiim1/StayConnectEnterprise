-- THE SERVICE MAY READ THE USAGE IT REPORTS.
--
-- The Usage Explorer answers the two questions a hotel gets during a guest dispute: how much internet did
-- room 4202 use during this stay, and how much did this device use between these dates. Both are answerable
-- from data the appliance already records durably — and the answer must be traceable, because a number an
-- operator cannot defend is worse than no number at all.
--
-- WHAT THE ANSWER IS BUILT FROM, and why each part is needed:
--
--   iam_v2.sessions              already readable. Carries the per-session byte totals, start and end times,
--                                end reason, and the device the session belonged to.
--   iam_v2.entitlements          already readable. Links a session's entitlement to a stay, carries the data
--                                quota and the terminal reason.
--   iam_v2.stays                 already readable. The room number, reservation and PMS interface that scope
--                                it — a room number is only meaningful inside one interface's namespace.
--   iam_v2.accounting_records    GRANTED HERE. The durable per-session samples behind the totals. Without it
--                                the screen can show a total but cannot show WHERE it came from, which is
--                                exactly what a disputed figure needs.
--   iam_v2.devices               GRANTED HERE. Maps a session's device_id to the MAC an operator can read off
--                                a handset. Without it the device investigation cannot name the device at all.
--
-- The totals already agree: on the PRE-LIVE appliance iam_v2.sessions and iam_v2.accounting_records both sum
-- to 647,371,044 bytes across 16 sessions and 14,160 samples. This grant does not change that number, it
-- lets the operator see how it is composed.
--
-- WHAT THIS GRANT IS, EXACTLY
-- ---------------------------
-- SELECT on two tables, to one role. Nothing else.
--
--   * No schema change. No table, column, type, index or constraint is created, altered or dropped. No
--     product semantics change: quota enforcement, accounting durability and entitlement rules are untouched,
--     and nothing here writes.
--   * Not INSERT, UPDATE or DELETE. accounting_records is the durable evidence behind every quota decision
--     the appliance has ever made; a service that could write it could rewrite the basis of a dispute it is
--     party to. edged serves the screen and must only ever read.
--   * Not a schema-wide or future-objects grant, and not to any other service role. acctd writes the records,
--     scd enforces on them, and neither needs a new privilege for an operator screen.
--
-- WHAT IT DOES NOT MAKE VISIBLE. A room number is not an identity and a MAC is not a person, and this grant
-- does not change that: rooms stay scoped by PMS interface and stay, the device tables carry no guest name,
-- and the screen built on top presents a device as a device.

BEGIN;

DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT SELECT ON iam_v2.accounting_records TO svc_edged;
        GRANT SELECT ON iam_v2.devices TO svc_edged;
    END IF;
END;
$g$;

COMMIT;
