-- AD-38 (performance audit D6-L1): every reconnect publishes a roster generation, and each generation stores every in-house
-- guest's record again as RESYNC rows in iam_v2.stay_events. Eight reconnects keep eight copies of every guest's name and
-- dates, long after a newer generation is applied (GDPR Art. 5(1)(c) data minimisation; table growth).
--
-- CONTROLLED historical-data change (PMS, historical evidence). What it does, and what it provably does not:
--
--   * Only RESYNC rows of a generation OLDER than the interface's published generation, whose newest generation is fully applied
--     (no waiting RESYNC row at or below it), and which are already terminal (APPLIED or SKIPPED_DUPLICATE).
--   * Only a MIDDLE COPY: the same record (identical payload) also exists in an EARLIER generation and in a LATER one. The earliest
--     copy (where the PMS first said it, the row that changed the stay) and the newest copy (what the mirror is built from) stay
--     whole. A record that CHANGED between generations is never touched, because no other generation carries its payload.
--   * The row itself stays: id, external_event_identity (the keyed hash of the complete source record, i.e. the evidence hash),
--     received_at, status, stay_id and review code are unchanged. Only the payload text is emptied, through the owner-only
--     retention path stay_events already has, which can only shrink a payload.
--   * Reversible: iam_v2.stay_event_payload_reductions remembers which whole row each emptied payload equals, and the .down puts
--     the payload back from it. If that row was itself anonymised since, the anonymised marker goes back, which is what the
--     data now is.
--   * Idempotent and bounded: a second run finds nothing; each call touches at most p_limit rows per interface.
--
-- The function is also what pmsd's periodic sweep calls, so reconnects stop accumulating copies after this run.

CREATE TABLE IF NOT EXISTS iam_v2.stay_event_payload_reductions (
    event_id        uuid PRIMARY KEY,
    equals_event_id uuid NOT NULL,
    reduced_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sepr_distinct CHECK (event_id <> equals_event_id)
);
COMMENT ON TABLE iam_v2.stay_event_payload_reductions IS
    'Superseded RESYNC copies whose payload was emptied (0171), each with the whole row it equals, so the reduction can be undone. No personal data: ids only.';

CREATE INDEX IF NOT EXISTS se_resync_payload_digest
    ON iam_v2.stay_events (tenant_id, site_id, pms_interface_id, md5(payload::text), resync_generation)
    WHERE admission_kind = 'RESYNC';

CREATE OR REPLACE FUNCTION iam_v2.pms_resync_supersede_payloads(p_tenant uuid, p_site uuid, p_limit integer DEFAULT 2000)
RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE r record; v_n integer; v_total integer := 0; v_limit integer;
BEGIN
    v_limit := LEAST(GREATEST(COALESCE(p_limit, 2000), 1), 20000);
    PERFORM set_config('iam_v2.anonymise', 'retention', true);
    PERFORM iam_v2.begin_controlled_operation('stay');
    FOR r IN SELECT i.pms_interface_id, i.published_resync_generation AS g
               FROM iam_v2.pms_interface_runtime i
              WHERE i.tenant_id = p_tenant AND i.site_id = p_site AND i.published_resync_generation > 1 LOOP
        -- The newest published generation must be completely applied before an older copy is given up.
        IF EXISTS (SELECT 1 FROM iam_v2.stay_events w
                    WHERE w.tenant_id = p_tenant AND w.site_id = p_site AND w.pms_interface_id = r.pms_interface_id
                      AND w.admission_kind = 'RESYNC' AND w.processing_status = 'PENDING' AND w.resync_generation <= r.g) THEN
            CONTINUE;
        END IF;
        WITH cand AS (
            SELECT e.id,
                   (SELECT a.id FROM iam_v2.stay_events a
                     WHERE a.tenant_id = e.tenant_id AND a.site_id = e.site_id AND a.pms_interface_id = e.pms_interface_id
                       AND a.admission_kind = 'RESYNC' AND md5(a.payload::text) = md5(e.payload::text) AND a.payload = e.payload
                       AND a.resync_generation < e.resync_generation
                     ORDER BY a.resync_generation, a.id LIMIT 1) AS first_id
              FROM iam_v2.stay_events e
             WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.pms_interface_id = r.pms_interface_id
               AND e.admission_kind = 'RESYNC' AND e.resync_generation < r.g
               AND e.processing_status IN ('APPLIED', 'SKIPPED_DUPLICATE')
               AND e.payload <> '{}'::jsonb AND e.payload <> '{"anonymised": true}'::jsonb
               AND EXISTS (SELECT 1 FROM iam_v2.stay_events b
                            WHERE b.tenant_id = e.tenant_id AND b.site_id = e.site_id AND b.pms_interface_id = e.pms_interface_id
                              AND b.admission_kind = 'RESYNC' AND md5(b.payload::text) = md5(e.payload::text) AND b.payload = e.payload
                              AND b.resync_generation > e.resync_generation)
             ORDER BY e.resync_generation, e.id
             LIMIT v_limit),
        marked AS (
            INSERT INTO iam_v2.stay_event_payload_reductions (event_id, equals_event_id)
            SELECT id, first_id FROM cand WHERE first_id IS NOT NULL
            ON CONFLICT (event_id) DO NOTHING
            RETURNING event_id),
        upd AS (
            UPDATE iam_v2.stay_events s SET payload = '{}'::jsonb
              FROM marked m WHERE s.id = m.event_id
            RETURNING 1)
        SELECT count(*) INTO v_n FROM upd;
        v_total := v_total + v_n;
    END LOOP;
    PERFORM set_config('iam_v2.anonymise', '', true);
    RETURN v_total;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.pms_resync_supersede_payloads(uuid, uuid, integer) FROM PUBLIC;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.stay_event_payload_reductions OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_resync_supersede_payloads(uuid, uuid, integer) OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.pms_resync_supersede_payloads(uuid, uuid, integer) TO svc_pmsd;
  END IF;
END $g$;

-- The run over history: every site that has more than one published generation, in bounded batches until nothing is left.
-- A site whose newest generation is still being applied is skipped and left to the periodic sweep.
DO $run$
DECLARE s record; v_n integer; v_all bigint := 0; v_pass integer;
BEGIN
    FOR s IN SELECT DISTINCT i.tenant_id, i.site_id FROM iam_v2.pms_interface_runtime i WHERE i.published_resync_generation > 1 LOOP
        v_pass := 0;
        LOOP
            v_n := iam_v2.pms_resync_supersede_payloads(s.tenant_id, s.site_id, 5000);
            v_all := v_all + v_n;
            v_pass := v_pass + 1;
            EXIT WHEN v_n = 0 OR v_pass >= 200;
        END LOOP;
    END LOOP;
    IF v_all > 0 THEN
        RAISE NOTICE '0171: % superseded RESYNC payload(s) reduced to their hash (each still equals a whole earlier and a whole later copy)', v_all;
    END IF;
END $run$;
