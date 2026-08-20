-- MG-8  Resolution/audit aux (iam_v2). Single transaction.
BEGIN;
CREATE TABLE iam_v2.auth_resolutions (         -- STRICT resolution outcomes; codes only, never guest data
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  guest_network_id uuid NOT NULL, resolved_stay_id uuid,
  outcome_code text NOT NULL, resolved_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES public.guest_networks (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, resolved_stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, id));
COMMIT;
