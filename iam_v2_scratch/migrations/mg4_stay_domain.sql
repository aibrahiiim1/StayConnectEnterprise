-- MG-4  Stay domain (iam_v2). Single transaction.
BEGIN;
CREATE TABLE iam_v2.stays (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  external_reservation_id text NOT NULL, external_stay_identity text NOT NULL,
  normalized_room_number text,
  status text NOT NULL CHECK (status IN
    ('RESERVED','IN_HOUSE','CHECKED_OUT','POST_STAY_ACTIVE','CANCELLED','NO_SHOW')),
  lifecycle_version int NOT NULL DEFAULT 1,
  posting_allowed boolean NOT NULL DEFAULT false,
  posting_block_reason text, posting_permission_source text, posting_checked_at timestamptz,
  last_applied_event_version bigint NOT NULL DEFAULT 0,
  vip boolean, travel_agent text, room_type text, arrival date, departure date,
  UNIQUE (tenant_id, site_id, pms_interface_id, external_reservation_id, external_stay_identity),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  UNIQUE (tenant_id, site_id, id),
  CONSTRAINT posting_only_in_house CHECK (posting_allowed = false OR status = 'IN_HOUSE'),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id));
CREATE INDEX stays_room_lookup ON iam_v2.stays
  (tenant_id, site_id, pms_interface_id, normalized_room_number) WHERE status='IN_HOUSE';

CREATE TABLE iam_v2.stay_guests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL, stay_id uuid NOT NULL,
  external_guest_id text, first_name_norm text, last_name_norm text, display_name text,
  is_primary boolean NOT NULL DEFAULT false, date_of_birth date, pin_hash text,
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id)
    REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id) ON DELETE CASCADE);
CREATE UNIQUE INDEX one_primary_guest_per_stay ON iam_v2.stay_guests (stay_id) WHERE is_primary;

CREATE TABLE iam_v2.folios (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  external_folio_id text NOT NULL,
  identity_epoch int NOT NULL DEFAULT 1,
  folio_kind text NOT NULL DEFAULT 'GUEST' CHECK (folio_kind IN ('GUEST','COMPANY','GROUP_MASTER','OTHER')),
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','CLOSED')),
  UNIQUE (tenant_id, site_id, pms_interface_id, external_folio_id, identity_epoch),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id));
CREATE UNIQUE INDEX folio_open_identity ON iam_v2.folios (tenant_id, site_id, pms_interface_id, external_folio_id)
  WHERE status='OPEN';

CREATE TABLE iam_v2.stay_folios (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  stay_id uuid NOT NULL, folio_id uuid NOT NULL,
  is_default_posting_target boolean NOT NULL DEFAULT false,
  PRIMARY KEY (stay_id, folio_id),
  UNIQUE (tenant_id, site_id, pms_interface_id, stay_id, folio_id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, folio_id) REFERENCES iam_v2.folios (tenant_id, site_id, pms_interface_id, id));
CREATE UNIQUE INDEX stay_folio_default ON iam_v2.stay_folios (stay_id) WHERE is_default_posting_target;

CREATE TABLE iam_v2.stay_events (               -- append-only normalized event log
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL, stay_id uuid,
  external_event_identity text NOT NULL, event_type text NOT NULL,
  pms_timestamp_raw text, pms_timestamp_utc timestamptz, source_timezone text, received_at timestamptz NOT NULL DEFAULT now(),
  sequence_version bigint NOT NULL DEFAULT 0, normalization_version int NOT NULL DEFAULT 1, clock_suspect boolean NOT NULL DEFAULT false,
  payload jsonb NOT NULL DEFAULT '{}',
  processing_status text NOT NULL DEFAULT 'PENDING'
    CHECK (processing_status IN ('PENDING','APPLIED','SKIPPED_DUPLICATE','MANUAL_REVIEW','FAILED')),
  UNIQUE (tenant_id, site_id, pms_interface_id, external_event_identity),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id));

CREATE TABLE iam_v2.stay_links (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  from_stay uuid NOT NULL, to_stay uuid NOT NULL,
  reason text NOT NULL CHECK (reason IN ('CROSS_PMS_TRANSFER','POST_STAY')),
  UNIQUE (from_stay, to_stay, reason),
  FOREIGN KEY (tenant_id, site_id, from_stay) REFERENCES iam_v2.stays (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, to_stay) REFERENCES iam_v2.stays (tenant_id, site_id, id));

CREATE TABLE iam_v2.post_stay_profiles (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  origin_stay_id uuid NOT NULL, origin_lifecycle_version int NOT NULL,
  UNIQUE (origin_stay_id, origin_lifecycle_version), UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, origin_stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, id));
COMMIT;
