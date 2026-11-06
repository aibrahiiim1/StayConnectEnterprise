BEGIN;

CREATE TABLE IF NOT EXISTS central_notes (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  customer_id  uuid REFERENCES tenants(id) ON DELETE CASCADE,
  site_id      uuid REFERENCES sites(id) ON DELETE CASCADE,
  appliance_id uuid REFERENCES appliances(id) ON DELETE CASCADE,
  body         text NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 4000),
  pinned       boolean NOT NULL DEFAULT false,
  author_id    uuid REFERENCES operators(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  CHECK (num_nonnulls(customer_id, site_id, appliance_id) = 1)
);
CREATE INDEX IF NOT EXISTS central_notes_customer ON central_notes (customer_id, created_at DESC) WHERE customer_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS central_notes_site ON central_notes (site_id, created_at DESC) WHERE site_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS central_notes_appliance ON central_notes (appliance_id, created_at DESC) WHERE appliance_id IS NOT NULL;
COMMENT ON TABLE central_notes IS
  'Central team notes on a customer, site or appliance (0066). Removed with the record they belong to. Never guest data.';

CREATE TABLE IF NOT EXISTS central_tags (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 40 AND name = btrim(name)),
  color      text NOT NULL DEFAULT 'neutral' CHECK (color IN ('neutral','info','ok','warn','err','accent')),
  created_by uuid REFERENCES operators(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS central_tags_name ON central_tags (lower(name));
COMMENT ON TABLE central_tags IS 'Labels the Central team puts on customers and appliances (0066).';

CREATE TABLE IF NOT EXISTS central_tag_links (
  tag_id       uuid NOT NULL REFERENCES central_tags(id) ON DELETE CASCADE,
  customer_id  uuid REFERENCES tenants(id) ON DELETE CASCADE,
  appliance_id uuid REFERENCES appliances(id) ON DELETE CASCADE,
  created_by   uuid REFERENCES operators(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CHECK (num_nonnulls(customer_id, appliance_id) = 1)
);
CREATE UNIQUE INDEX IF NOT EXISTS central_tag_links_customer ON central_tag_links (tag_id, customer_id) WHERE customer_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS central_tag_links_appliance ON central_tag_links (tag_id, appliance_id) WHERE appliance_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS central_tag_links_by_customer ON central_tag_links (customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS central_tag_links_by_appliance ON central_tag_links (appliance_id) WHERE appliance_id IS NOT NULL;
COMMENT ON TABLE central_tag_links IS 'Which tag is on which customer or appliance (0066). Removed with either side.';

SELECT central_apply_runtime_grants();

COMMIT;
