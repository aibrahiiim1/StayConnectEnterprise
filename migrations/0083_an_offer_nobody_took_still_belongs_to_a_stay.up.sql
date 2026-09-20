-- AN OFFER NOBODY TOOK STILL BELONGS TO A STAY.
--
-- Guest Activity answers "who was offered what, and did they take it". It could answer the first half only
-- for offers that WERE taken: the purchase carries the stay, so a taken offer resolves to a room. An offer
-- that expired unused carried no purchase, and the screen had to leave the room blank — which is precisely
-- the case an operator opens the screen to investigate.
--
-- iam_v2.auth_contexts is the only link from a quote to a stay before an offer is taken. offer_quotes.
-- auth_context_id points at it, and the context is what the guest's authenticated session resolved to.
--
-- WHY THIS IS FIVE COLUMNS AND NOT A TABLE
-- ----------------------------------------
-- The table is not a lookup table. Its `ac_one_subject` constraint says every row carries exactly ONE
-- subject, and the subject is whatever the guest authenticated as:
--
--     stay_id · guest_account_id · voucher_id · guest_principal_id · post_stay_profile_id
--
-- plus device_id and guest_network_id, which are NOT NULL on every row. A table-level SELECT would hand the
-- operator API the voucher, guest-account and guest-principal linkage for every authentication this
-- appliance has ever performed, and the device and network each one came from. None of that is attribution.
-- It is the authentication record itself, and edged has never held it.
--
-- So this grants the five columns attribution actually needs, and no others:
--
--     id                 the join key: offer_quotes.auth_context_id -> auth_contexts.id
--     tenant_id          so the join can assert tenant isolation rather than assume it
--     site_id            likewise
--     stay_id            the answer — the authoritative stay, NULL unless the guest authenticated as a stay
--     pms_interface_id   the namespace that makes the stay's room number mean anything
--
-- Deliberately NOT granted, and each for its own reason:
--
--     method                                the authentication method. Adjacent to attribution, not part of
--                                           it. Taken offers already show how the guest signed in, from
--                                           sessions.credential_method, which edged may already read.
--     guest_account_id, voucher_id,         the credential a guest presented. Reading these would let an
--       guest_principal_id,                 operator screen correlate a person's voucher or account across
--       post_stay_profile_id                every authentication. Not needed to name a room.
--     device_id, guest_network_id           which handset and which network. The Usage Explorer already
--                                           answers device questions from data scoped to that purpose.
--     expires_at, consumed_at               the quote carries its own timestamps; these would be a second,
--                                           subtly different set of the same facts.
--     pinned_* , resolution_request_id,     internal authentication evidence. It belongs to the engine that
--       authentication_interface_revision_id decides eligibility, not to a screen that reports outcomes.
--
-- The result is strictly narrower than any grant already on this table: iam_v2_owner holds everything,
-- svc_scd holds INSERT/SELECT/UPDATE, and sc_payment_runtime holds table-wide SELECT. This adds the
-- narrowest read on it that exists.
--
-- WHAT IT DOES NOT CHANGE
-- -----------------------
-- No schema change: no table, column, type, index, constraint or trigger is created, altered or dropped.
-- Read only — not INSERT, UPDATE or DELETE, and the p3_auth_context_controlled_writer trigger that guards
-- writes is untouched. No schema-wide grant and no default privileges for future objects. No other role.
-- Nothing is backfilled and no history is rewritten.
--
-- A ROOM IS STILL NOT AN IDENTITY. stay_id is meaningless without pms_interface_id, which is why they are
-- granted together: the foreign key onto iam_v2.stays is (tenant_id, site_id, pms_interface_id, stay_id), so
-- a room resolved this way is PMS-scoped by construction and cannot be read as a global room number.
-- Attribution comes from this recorded link or it does not happen — nothing infers a stay from timing,
-- from the interface alone, or from any other weak evidence.

BEGIN;

DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT SELECT (id, tenant_id, site_id, stay_id, pms_interface_id)
            ON iam_v2.auth_contexts TO svc_edged;
    END IF;
END;
$g$;

COMMIT;
