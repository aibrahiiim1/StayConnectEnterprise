-- Reverse of 0070: restore the empty re-offer log and its append-only trigger.
--
-- The FUNCTION is deliberately not restored. It never worked on any schema carrying the
-- p3_stay_event_appendonly trigger, which is every schema this product builds, so recreating it would
-- reintroduce a call that can only fail. A rollback should return the schema, not the defect.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.stay_event_reoffers (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL,
    site_id        uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    stay_event_id  uuid NOT NULL,
    requested_at   timestamptz NOT NULL DEFAULT now(),
    requested_by   text NOT NULL CHECK (length(btrim(requested_by)) > 0),
    reason         text NOT NULL CHECK (length(btrim(reason)) >= 3 AND length(reason) <= 500),
    prior_processing_status text NOT NULL,
    prior_review_code       text,
    prior_processed_at      timestamptz,
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT ser_evidence_is_object CHECK (jsonb_typeof(evidence) = 'object')
);

CREATE OR REPLACE FUNCTION iam_v2.stay_event_reoffers_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.stay_event_reoffers is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS stay_event_reoffers_no_update ON iam_v2.stay_event_reoffers;
CREATE TRIGGER stay_event_reoffers_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.stay_event_reoffers
    FOR EACH ROW EXECUTE FUNCTION iam_v2.stay_event_reoffers_append_only();

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER TABLE iam_v2.stay_event_reoffers OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.stay_event_reoffers_append_only() OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

COMMIT;
