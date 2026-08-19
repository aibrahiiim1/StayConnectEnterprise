-- Least-privilege grants for the Phase-3/4/5 Hotel-Admin READ surfaces served by edged.
--
-- WHY THIS FILE EXISTS
-- --------------------
-- A screen-by-screen operator walkthrough of the DEVELOPMENT appliance found SEVEN admin screens answering
-- HTTP 500 with nothing but "query failed":
--
--   /pms-interfaces  /pms-routing  /pms-source-conflicts  /stays  /online-time
--   /guest-device-self-service  (and the Phase-4/5 read surfaces behind them)
--
-- The handler swallows the driver error, so the 500 reads as an application bug. Running the repository's own
-- query as the real service role gave the answer in one line, exactly as it did for the Phase-2 commerce
-- surface a month earlier:
--
--     ERROR: permission denied for table pms_interfaces
--
-- svc_edged had never been granted anything on the Phase-3/4/5 iam_v2 tables. The boundary was working as
-- designed; the grants for these surfaces were simply never written. That this is the SECOND time the same
-- shape appeared is the argument for deriving grants from the source rather than from the screen that
-- happened to be open.
--
-- SCOPE DISCIPLINE
-- ----------------
-- Every line below is derived from the SQL in
--   data-plane/cmd/edged/resources_phase3.go
--   data-plane/cmd/edged/resources_phase3_interfaces.go
--   data-plane/cmd/edged/resources_phase4_finops.go, resources_phase4_review.go
--   data-plane/cmd/edged/resources_phase5*.go
-- table by table and verb by verb. There is deliberately NO `GRANT ... ON ALL TABLES IN SCHEMA iam_v2` and no
-- DEFAULT PRIVILEGES: a blanket grant would have cleared all seven 500s in one line while silently handing
-- edged read/write over the whole IAM-v2 domain, including guest credentials and the financial ledger.
--
-- Idempotent: re-granting an existing privilege is a no-op in PostgreSQL.

GRANT USAGE ON SCHEMA iam_v2 TO svc_edged;

-- ---- Phase 3: PMS interfaces, routing, stays, conflicts, resolution evidence -------------------------
-- Read-only. The interface list, its revision history, live runtime state and the secret-generation
-- metadata (never the secret itself -- the column is not readable through these screens).
GRANT SELECT ON iam_v2.pms_interfaces                  TO svc_edged;
GRANT SELECT ON iam_v2.pms_interface_revisions         TO svc_edged;
GRANT SELECT ON iam_v2.pms_interface_runtime           TO svc_edged;
GRANT SELECT ON iam_v2.pms_interface_secret_generations TO svc_edged;
GRANT SELECT ON iam_v2.guest_network_pms_map           TO svc_edged;
GRANT SELECT ON iam_v2.pms_source_conflicts            TO svc_edged;
GRANT SELECT ON iam_v2.auth_resolutions                TO svc_edged;
GRANT SELECT ON iam_v2.stays                           TO svc_edged;
GRANT SELECT ON iam_v2.stay_guests                     TO svc_edged;
GRANT SELECT ON iam_v2.stay_events                     TO svc_edged;
GRANT SELECT ON iam_v2.stay_folios                     TO svc_edged;
GRANT SELECT ON iam_v2.folios                          TO svc_edged;

-- Publishing an interface revision moves the interface's current-revision pointer and rotates its secret
-- generation. These are the ONLY two write targets in the Phase-3 admin source, and the controlled-writer
-- trigger still governs both: the grant lets edged attempt the write, it does not let it bypass the
-- operation boundary.
GRANT UPDATE         ON iam_v2.pms_interfaces                  TO svc_edged;
GRANT INSERT, UPDATE ON iam_v2.pms_interface_secret_generations TO svc_edged;

-- ---- Phase 4: financial read surfaces ----------------------------------------------------------------
-- Read-only throughout. Nothing on these screens posts, settles or reverses -- those are capability-gated
-- operations that are not enabled anywhere, and no privilege here would make them reachable.
GRANT SELECT ON iam_v2.pms_postings            TO svc_edged;
GRANT SELECT ON iam_v2.posting_attempts        TO svc_edged;
GRANT SELECT ON iam_v2.posting_review_state    TO svc_edged;
GRANT SELECT ON iam_v2.posting_review_actions  TO svc_edged;
GRANT SELECT ON iam_v2.settlements             TO svc_edged;

-- Views, owned by iam_v2_owner. A view executes its body with its OWNER's privileges, so SELECT on the view
-- is sufficient and the underlying tables stay unreadable to edged -- which is the point of reading through
-- them rather than joining the base tables directly.
GRANT SELECT ON iam_v2.posting_execution_state        TO svc_edged;
GRANT SELECT ON iam_v2.v_financial_payments           TO svc_edged;
GRANT SELECT ON iam_v2.v_financial_settlements        TO svc_edged;
GRANT SELECT ON iam_v2.v_zero_attempt_recovery_queue  TO svc_edged;
GRANT SELECT ON iam_v2.active_operational_alerts      TO svc_edged;

-- ---- Phase 5: post-stay and transfer read surfaces ---------------------------------------------------
GRANT SELECT ON iam_v2.post_stay_profiles              TO svc_edged;
GRANT SELECT ON iam_v2.entitlement_transfers           TO svc_edged;
GRANT SELECT ON iam_v2.entitlement_devices             TO svc_edged;
GRANT SELECT ON iam_v2.entitlement_termination_evidence TO svc_edged;

-- ---- the product-settings row the Device Self-Service screen reads -----------------------------------
-- /guest-device-self-service answered 500 with the driver error intact:
--   permission denied for table appliance_product_settings
-- It lives in iam_v2, not public. SELECT only: the screen reports whether the capability is enabled, and
-- enabling it is a Product-Owner decision that no UI privilege should be able to anticipate.
GRANT SELECT ON iam_v2.appliance_product_settings TO svc_edged;

-- NOT granted, on purpose, and each absence is load-bearing:
--   * DELETE on anything -- no admin read surface deletes;
--   * any privilege on iam_v2 vouchers, guest credentials or session secrets;
--   * INSERT/UPDATE on the financial ledger, postings, attempts or settlements: the financial screens
--     REVIEW, and the operations that act on them are capability-gated elsewhere.
