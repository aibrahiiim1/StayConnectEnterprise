// API client — talks to /api/edge/v1/* which Next.js rewrites to edged, the
// Hotel Admin API on the appliance. The site is implicit (edged is physically
// connected to exactly one site DB) so no tenant_id/site_id params anywhere.
// Cookies flow naturally same-origin; no credentials: 'include' needed.

import type { OutboxFigures } from "@/lib/health-words";

export class ApiError extends Error {
  status: number;
  code: string;          // machine-readable, e.g. "forbidden"
  traceId?: string;      // server request id for support tickets
  body: any;             // full envelope
  constructor(status: number, body: any) {
    const code = (typeof body === "object" && typeof body?.error === "string") ? body.error : "http_error";
    const msg  = (typeof body === "object" && typeof body?.message === "string") ? body.message
               : (typeof body === "object" && typeof body?.error === "string") ? body.error
               : `HTTP ${status}`;
    super(msg);
    this.status = status;
    this.code = code;
    this.traceId = typeof body?.trace_id === "string" ? body.trace_id : undefined;
    this.body = body;
  }
}

// All edged resources live under /edge/v1. The Next proxy strips /api.
export const EDGE_BASE = "/api/edge/v1";

async function request<T>(
  method: string,
  path: string,
  body?: any,
  init?: RequestInit
): Promise<T> {
  const res = await fetch(`${EDGE_BASE}${path}`, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    cache: "no-store",
    ...init,
  });
  const contentType = res.headers.get("content-type") ?? "";
  const payload = contentType.includes("application/json") ? await res.json() : await res.text();
  if (!res.ok) throw new ApiError(res.status, payload);
  return payload as T;
}

export const api = {
  get:   <T>(path: string)                => request<T>("GET", path),
  post:  <T>(path: string, body?: any)    => request<T>("POST", path, body),
  put:   <T>(path: string, body?: any)    => request<T>("PUT", path, body),
  patch: <T>(path: string, body?: any)    => request<T>("PATCH", path, body),
  del:   <T>(path: string)                => request<T>("DELETE", path),
  // postRaw sends a pre-serialized body (e.g. a pasted license envelope)
  // without re-encoding it.
  postRaw: async <T>(path: string, raw: string): Promise<T> => {
    const res = await fetch(`${EDGE_BASE}${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: raw,
      cache: "no-store",
    });
    const contentType = res.headers.get("content-type") ?? "";
    const payload = contentType.includes("application/json") ? await res.json() : await res.text();
    if (!res.ok) throw new ApiError(res.status, payload);
    return payload as T;
  },
};

// ------- Types (edged /edge/v1 response shapes) -------

// A phase-gated surface that edged has NOT enabled answers 404 "page not found" for every path under it,
// because the routes are never mounted. Rendered raw, an operator sees "HTTP 404" on a screen the menu just
// offered them, which reads as a broken product rather than a capability that is deliberately off.
//
// This is not something the UI can pre-empt: the screens are compiled in at BUILD time (NEXT_PUBLIC_PHASE*)
// while mounting is decided at RUNTIME by edged's own flags, so the two can disagree on a given appliance --
// and on this one they do. Saying so plainly is the honest surface. It is NOT a way to turn the capability
// on: enabling it is a deliberate deployment decision, made in edged's configuration, not here.
export function surfaceUnavailableMessage(e: unknown, surface: string): string {
  if (e instanceof ApiError && e.status === 404) {
    return `${surface} is not enabled on this appliance. The screens are present in this build, but the ` +
      `backend does not serve them here, so there is nothing to show. Enabling it is a deployment decision.`;
  }
  return (e as { message?: string })?.message ?? `Could not load ${surface.toLowerCase()}`;
}

export type ListResp<T> = {
  data: T[];
  meta: { has_more: boolean };
  // Which domain answered. Present on surfaces where legacy and IAM-v2 have genuinely different contracts,
  // so a screen with ZERO rows can still tell which one it is talking to. Per-row authority cannot: an empty
  // list has no rows to ask.
  authority?: "iam_v2" | "legacy";
};

export type Whoami = {
  operator_id: string;
  email: string;
  display_name?: string;
  roles: string[];
  site_id: string;
  expires_at: string;
};

// GuestAccessPlan — the local `ticket_templates` rows, renamed per the edge
// domain language (duration/caps/bandwidth/price for a guest access tier).
export type GuestAccessPlan = {
  id: string; tenant_id: string; code: string; name: string;
  description?: string | null;
  duration_seconds?: number | null; data_cap_bytes?: number | null;
  down_kbps?: number | null; up_kbps?: number | null;
  max_concurrent_devices: number; is_active: boolean;
  price_cents?: number | null; currency?: string | null;
};

export type VoucherTotals = {
  unused: number; active: number; exhausted: number; expired: number; revoked: number;
};
export type VoucherBatch = {
  id: string; tenant_id: string; template_id: string; name?: string | null;
  count: number; created_by?: string | null; created_at: string;
  code_length?: number | null; char_mode?: string | null;
  code_prefix?: string | null; exclude_ambiguous?: boolean | null;
  totals?: VoucherTotals | null;
};

// Guest Username/Password account (password is never returned).
export type GuestAccount = {
  id: string; username: string; display_name?: string | null; notes?: string | null;
  // ABSENT under IAM-v2 authority, which has no template/plan on the credential at all -- see the
  // guest-accounts screen. Optional so the type cannot promise a field the backend does not send.
  template_id?: string | null; enabled: boolean;
  valid_from?: string | null; valid_until?: string | null;
  last_login_at?: string | null; login_count: number;
  locked_until?: string | null;
  // Derived (list/get): plan device cap + live distinct active devices.
  max_devices?: number | null; active_devices?: number;
  created_at: string; updated_at: string;
};

// Response of create / set-password: password reveal is one-time only.
export type GuestAccountCreateResp = { account: GuestAccount; generated_password?: string };
export type GuestAccountPasswordResp = { status: string; disconnected_sessions?: number; generated_password?: string };

export type Voucher = {
  id: string; tenant_id: string; template_id: string; batch_id?: string | null;
  code: string; code_display: string; state: string; issued_at: string;
  activated_at?: string; expires_at?: string;
  bytes_used: number; seconds_used: number;
  // Enriched on the detail view (GET /vouchers/{id}).
  plan_name?: string | null; plan_code?: string | null;
  duration_seconds?: number | null; data_cap_bytes?: number | null;
  down_kbps?: number | null; up_kbps?: number | null;
  max_devices?: number | null; active_devices?: number | null;
  // Derived usage under the validity-window model.
  first_activated_at?: string | null; valid_until?: string | null;
  time_remaining_seconds?: number | null;
  data_used_bytes?: number | null; data_remaining_bytes?: number | null;
  effective_state?: string; exhaustion_reason?: string;
};

// SubjectKind — the four things an Entitlement can belong to, which is therefore the four things a session can
// belong to. The database guarantees exactly one (iam_v2 ent_one_subject), so this is a closed set rather than a
// convention. An empty string means the session's entitlement could not be read, not that it has no subject.
export type SubjectKind = "room" | "account" | "voucher" | "guest" | "";

export type Session = {
  id: string; tenant_id: string; site_id: string; appliance_id: string;
  guest_id: string; voucher_id?: string | null;
  ip: string; mac: string;
  state: string; end_reason?: string | null;
  started_at: string; last_activity_at: string; ended_at?: string | null;
  expires_at?: string | null;
  bytes_up: number; bytes_down: number;

  // WHO, WHERE AND ON WHAT — see edged's resources_sessions.go for why each of these is safe to send and why a
  // voucher code and a guest principal's contact details are deliberately NOT among them.
  credential_method?: string | null;
  ingress_interface?: string | null;
  guest_network_name?: string | null;

  entitlement_id?: string;
  entitlement_status?: string | null;
  subject_kind?: SubjectKind;
  subject_label?: string | null;
  subject_name?: string | null;

  stay_id?: string | null;
  room?: string | null;
  external_reservation_id?: string | null;

  package_code?: string | null;
  package_name?: string | null;
  service_plan_code?: string | null;
  down_kbps?: number | null;
  up_kbps?: number | null;
  max_devices?: number | null;

  data_quota_bytes?: number | null;
  data_used_bytes?: number | null;
  time_quota_seconds?: number | null;
  time_used_seconds?: number | null;

  active_devices?: number;
};

// ---- Phase 3 (DARK) PMS stay resolution + checkout grace -------------------
// These mirror edged's resources_phase3.go exactly. Resolution evidence deliberately carries NO guest
// identity: an operator learns that a resolution succeeded, never who it was.

export type Stay = {
  id: string; pms_interface_id: string; pms_interface_label?: string; external_reservation_id: string;
  room?: string | null; status: string; lifecycle_version: number;
  arrival?: string | null; departure?: string | null;
  effective_checkout_at?: string | null; posting_allowed: boolean; occupants: number;
  // The primary occupant, so the list can be searched the way an operator actually thinks about a stay.
  primary_guest?: string | null;
  // Why posting is closed, and which side decided it.
  posting_block_reason?: string | null; posting_permission_source?: string | null;
  // When the PMS last confirmed this occupancy — the evidence guest sign-in is judged against.
  occupancy_evidence_at?: string | null;
  // Hotel attributes the schema carries. Present only when the connector sends them; the Protel FIAS feed
  // does not, so these are absent on that interface rather than blank.
  vip?: boolean | null; room_type?: string | null; rate_plan?: string | null; travel_agent?: string | null;

  // WHICH INTERNET THIS ROOM HAS. The live entitlement's package and the service plan behind it — absent when
  // the stay has no live access, which is a real and common state rather than a missing value.
  access_status?: string | null;
  access_package_code?: string | null;
  access_package_name?: string | null;
  access_plan_code?: string | null;
  access_down_kbps?: number | null;
  access_up_kbps?: number | null;
  access_max_devices?: number | null;
  access_active_devices?: number;
};

export type StayDetail = Stay & {
  occupant_list: { display_name?: string | null; is_primary: boolean }[];
  folios: { external_folio_id: string; folio_kind: string; status: string; is_default_posting_target: boolean }[];
};

export type StayEvent = {
  id: string; pms_interface_id: string; external_event_identity: string; event_type: string;
  processing_status: string; review_code?: string | null; stay_id?: string | null;
  pms_timestamp_utc?: string | null; received_at: string;
  // What the event is ABOUT. Absent on an event the appliance could not match to a stay — which is usually
  // precisely why it is still waiting.
  pms_interface_label?: string;
  room?: string | null;
  external_reservation_id?: string | null;
  primary_guest?: string | null;
  stay_status?: string | null;
};

export type PmsResolution = {
  id: string; guest_network_id: string; outcome_code: string; resolved: boolean; resolved_at: string;
  // The network's own name. No guest, room or reservation appears on this surface by design.
  guest_network_name?: string | null;
};

// GUEST SIGN-IN ATTEMPTS.
//
// Two shapes, because there are two permissions. SignInAttempt and SignInAttemptDetail carry NO credential
// value of any kind — not masked, absent — and SignInAttemptCredentials is fetched from its own endpoint
// behind its own permission. A single type with optional secret fields is how a surface that "hides" them in
// one view eventually ships them in another.
export type SignInAttempt = {
  id: string;
  occurred_at: string;
  room: string;
  guest_network: string;
  result: string;
  result_label: string;
  succeeded: boolean;
  verifier_kind: string;
  mirror_age_seconds?: number | null;
  pms_transport_status?: string | null;
  device_ip?: string | null;
  device_mac?: string | null;
  request_id?: string | null;
  latency_ms?: number | null;
};

export type SignInAttemptDetail = SignInAttempt & {
  matched_field?: string | null;
  matched_stay_id?: string | null;
  room_in_mirror?: boolean | null;
  eligible_stay_candidates?: number | null;
  mirror_last_complete_sync_at?: string | null;
  entitlement_id?: string | null;
  session_id?: string | null;
  // Whether a sealed half exists at all, so the panel can say "the key was unavailable when this was
  // recorded" instead of showing an empty box that reads as a permission problem.
  credentials_available: boolean;
};

export type SignInAttemptCredentials = {
  available: boolean;
  submitted_verifier?: string;
  normalized_verifier?: string;
  accepted_first_name?: string;
  accepted_family_name?: string;
  accepted_reservation_number?: string;
  additional_accepted_guests?: number;
};

// ---- guest sign-in protection ------------------------------------------------------
//
// The policy and the restrictions are separate types behind separate permissions, for the same reason the
// attempts and the credentials are: a single shape with optional fields is how a boundary that holds in one
// view stops holding in the next.

export type GuestSignInProtection = {
  max_failed_attempts: number;
  observation_window_seconds: number;
  restriction_seconds: number;
  // The site has never saved a policy and is running on the approved defaults. NOT "protection is off" —
  // there is no off.
  is_default: boolean;
  // The server's own bounds, sent so the form validates against the same numbers the server enforces
  // instead of a copy that can drift.
  limits: {
    min_failed_attempts: number;
    max_failed_attempts: number;
    min_observation_window_seconds: number;
    max_observation_window_seconds: number;
    min_restriction_seconds: number;
    max_restriction_seconds: number;
  };
  last_change?: GuestSignInProtectionChange | null;
};

export type GuestSignInProtectionChange = {
  changed_at: string;
  changed_by: string;
  reason?: string;
  // Absent on a site's first saved policy: there was no stored row to report.
  old_max_failed_attempts?: number | null;
  old_observation_window_seconds?: number | null;
  old_restriction_seconds?: number | null;
  new_max_failed_attempts: number;
  new_observation_window_seconds: number;
  new_restriction_seconds: number;
};

export type GuestSignInRestriction = {
  id: string;
  device_mac: string;
  guest_network?: string;
  // UNVERIFIED INPUT — the room this device last TYPED. The field name says so, and every label that
  // renders it says so too.
  last_submitted_room?: string;
  failure_count: number;
  reason: string;
  restricted_at: string;
  expires_at: string;
  // Computed by the server at the moment of the read, so a screen left open overnight cannot count down
  // against a clock that drifted from the appliance's.
  remaining_seconds: number;
};

export type CheckoutGraceConfig = {
  grace_package_revision_id?: string | null;
  grace_duration_seconds: number;
  grace_down_kbps: number;
  grace_up_kbps: number;
  grace_data_quota_bytes: number;
  grace_device_limit: number;
  grace_device_limit_policy: string;
  eligibility_window_seconds: number;
  config_version: number;
};

// GracePackageOption is one selectable Checkout-Grace package revision, described by its own IMMUTABLE
// attributes. The operator picks one; the numbers are never typed, so the published policy and the package
// agree by construction.
export type GracePackageOption = {
  package_revision_id: string;
  package_code: string;
  revision_no: number;
  service_plan_revision_id: string;
  service_plan_code: string;
  service_plan_revision_no: number;
  down_kbps: number;
  up_kbps: number;
  data_quota_bytes: number;
  device_limit: number;
  device_limit_policy: string;
  time_accounting_mode: string;
  grace_duration_seconds: number;
  end_mode: string;
  policy_version: string;
  settlement_mode: string;
  is_current: boolean;
  is_active: boolean;
  selected: boolean;
};

export type OperationalAlert = {
  audit_id: string; stay_id: string; lifecycle_version: number;
  alert_code: string; trigger: string; reason_code?: string | null;
  boundary_at: string; boundary_clock_suspect: boolean; created_at: string;
  // the lifecycle head the operator is looking at. Every action sends it back, so a concurrent change is a
  // clean 409 rather than a silent overwrite.
  state: "OPEN" | "ACKNOWLEDGED";
  seq: number;
  state_changed_at?: string | null;
};

export type Operator = {
  id: string;
  email: string;
  display_name?: string;
  status: "active" | "disabled" | "invited";
  roles?: { id: string; role: string }[];
  created_at: string;
  updated_at: string;
};

// EdgeOperator is the shape edged's /operators returns: roles is a flat
// string[] and there is no updated_at (unlike the control-plane Operator).
export type EdgeOperator = {
  id: string;
  email: string;
  display_name: string;
  status: "active" | "disabled" | string;
  roles: string[];
  created_at: string;
};

export type WalledGardenRule = {
  id: string; tenant_id: string; site_id?: string | null;
  kind: "domain" | "cidr" | "ip";
  value: string;
  ports?: number[] | null;
  description?: string | null;
  created_at: string;
};

export type PMSProvider = {
  id: string;
  tenant_id: string;
  site_id?: string;
  name: string;
  kind: "stub" | "protel-fias" | "opera-fias" | "fidelio-fias" | "mews" | "apaleo";
  enabled: boolean;
  display_name?: string;
  host?: string;
  port?: number;
  use_tls: boolean;
  base_url?: string;
  property_id?: string;
  extra?: Record<string, unknown>;
  field_map?: Record<string, string>;
  normalization?: { room_format?: string; name_strip_titles?: boolean; reservation_case?: string };
  stay_window?: { early_checkin_minutes?: number; late_checkout_minutes?: number; min_remaining_seconds?: number };
  status: "idle" | "connecting" | "connected" | "degraded" | "down";
  last_record_at?: string;
  last_error?: string;
  last_error_at?: string;
  created_at: string;
  updated_at: string;
};

export type PMSTestResult = { ok: boolean; latency_ms: number; error?: string };

export type PMSCacheRow = {
  room_number: string;
  first_name: string;
  last_name: string;
  guest_display_name?: string;
  reservation_number: string;
  check_in?: string;
  check_out?: string;
  email?: string;
};
export type PMSCacheResult = { provider: string; kind: string; count: number; rows: PMSCacheRow[] };

export type PMSHealthSnapshot = {
  status: string;
  connected_since?: string;
  last_record_at?: string;
  last_error?: string;
  last_error_at?: string;
  cache_size: number;
};
export type PMSHealthResult = { provider: string; kind: string; health: PMSHealthSnapshot };

export type StripeAccount = {
  id: string;
  tenant_id: string;
  enabled: boolean;
  display_name?: string;
  publishable_key: string;        // pk_live / pk_test — not a secret
  success_url: string;
  cancel_url: string;
  last_success_at?: string;
  last_error?: string;
  last_error_at?: string;
  created_at: string;
  updated_at: string;
};

export type Payment = {
  id: string;
  tenant_id: string;
  site_id?: string;
  template_id: string;
  stripe_session_id: string;
  status: "pending" | "paid" | "failed" | "expired" | "cancelled";
  amount_cents: number;
  currency: string;
  voucher_id?: string;
  created_at: string;
  completed_at?: string;
};

export type SocialOAuthProvider = {
  id: string;
  tenant_id: string;
  provider: "google" | "apple" | "facebook" | "microsoft";
  enabled: boolean;
  display_name?: string;
  client_id: string;
  redirect_uri: string;
  scopes?: string;
  last_success_at?: string;
  last_error?: string;
  last_error_at?: string;
  created_at: string;
  updated_at: string;
};

export type NotificationProvider = {
  id: string;
  tenant_id: string;
  channel: "email" | "sms";
  kind: "stub" | "sendgrid" | "ses" | "twilio";
  enabled: boolean;
  display_name?: string;
  api_user?: string;       // Twilio account SID — not a secret
  from_address?: string;
  from_name?: string;
  region?: string;
  last_success_at?: string;
  last_error?: string;
  last_error_at?: string;
  created_at: string;
  updated_at: string;
};

export type AuditEntry = {
  ts: string;
  tenant_id?: string | null;
  actor_type: string;
  actor_id?: string | null;
  action: string;
  target_type?: string | null;
  target_id?: string | null;
  ip?: string | null;
  user_agent?: string | null;
  payload?: Record<string, unknown> | null;
};

// ------- License (GET /edge/v1/license — scd's locally evaluated state) -------

export type LicenseState =
  | "Active" | "GracePeriod" | "Suspended" | "Expired" | "Revoked" | "Unlicensed"
  // "Restricted" is a legacy (pre-v3) intermediate state kept only for old license docs.
  | "Restricted"
  | string; // tolerate future states

export type LicenseFeatures = {
  pms: boolean;
  paid_wifi: boolean;
  sms_otp: boolean;
  email_otp: boolean;
  social_login: boolean;
  ha: boolean;
  white_label: boolean;
};

export type LicenseLimits = {
  max_appliances_for_site: number;
  max_concurrent_guest_sessions: number;
  max_local_operators: number;
  max_guest_access_plans: number;
  accounting_retention_days: number;
  audit_retention_days: number;
};

export type LicenseStatus = {
  state: LicenseState;
  installed?: boolean;
  mode?: string;                    // "unlicensed-dev" on dev boxes
  license_id?: string;
  commercial_plan_code?: string;
  issued_at?: string;
  valid_until?: string;
  offline_grace_days?: number;
  grace_until?: string | null;
  restricted_until?: string | null;
  features?: LicenseFeatures;
  limits?: LicenseLimits;
  cloud_stale?: boolean;
  clock_rollback?: boolean;
  last_cloud_validation?: string | null;
};

// ------- Health / reports / backups -------

export type EdgeHealth = {
  service: string;
  version: string;
  site_id: string;
  status: "ok" | "degraded";
  db: boolean;
  scd: boolean;
  license_state?: LicenseState | null;
  // True only for a REAL signed license. The permissive unlicensed-dev licstate
  // reports license_state="Active" with no license — this flag disambiguates so
  // the dashboard shows "Pending activation" instead of a false "Active".
  license_installed?: boolean;
  sync_outbox?: OutboxFigures;
};

// ReportsSummary mirrors the aggregates edged computes from local data —
// the same numbers scd pushes to the cloud as `usage` telemetry.
export type ReportsSummary = {
  active_sessions: number;
  sessions_today: number;
  bytes_up_today: number;
  bytes_down_today: number;
  total_bytes_today?: number;
  revenue_cents_today?: number;
  currency?: string;
  tz?: string;
};

// ------- Dashboard snapshot (GET /reports/dashboard) -------
//
// One read, many independently-fallible sections. `available: false` on a section means this appliance does not
// have that surface (no PMS, charges not deployed) — the UI says so instead of rendering zeros, because a zero
// and an absence are different facts and only one of them means "nothing happened tonight".

export type DashSection = { available: boolean; reason?: string };

export type DashHourBucket = { hour: number; sign_ins: number; sessions_started: number };

export type DashGuests = {
  devices_online: number;
  guests_online: number;
  sign_ins_today: number;
  devices_today: number;
  sign_ins_7d: number;
  busiest_hour_today?: number | null;
  busiest_hour_count?: number;
};

export type DashData = {
  bytes_down_today: number;
  bytes_up_today: number;
  total_bytes_today: number;
  bytes_down_7d: number;
  bytes_up_7d: number;
};

export type DashOccupancy = DashSection & {
  in_house: number;
  with_internet: number;
  arrivals_today: number;
  departures_today: number;
  posting_allowed: number;
};

export type DashSignInChecks = DashSection & {
  window: string;
  total: number;
  verified: number;
  outcomes: { outcome_code: string; count: number }[];
};

export type DashPmsInterface = {
  pms_interface_id: string;
  display_label: string;
  lifecycle_state: string;
  published: boolean;
  transport_status: string;
  continuity_status: string;
  sync_status: string;
  sync_stage?: string;
  room_auth_ready: boolean;
  room_auth_reason?: string;
  in_house_stays: number;
  pending_events: number;
  review_events: number;
  last_stay_event_at?: string | null;
  last_heartbeat_at?: string | null;
  last_complete_sync_at?: string | null;
  sync_records_received?: number;
  materialization_ready: boolean;
};

export type DashPms = DashSection & {
  interfaces: DashPmsInterface[];
  events_today: number;
  events_applied_today: number;
  events_needing_review: number;
};

export type DashPostings = DashSection & {
  posted_today: number;
  failed_today: number;
  pending: number;
  review_open: number;
  unknown_open: number;
};

export type DashNetwork = {
  name: string;
  bridge_name: string;
  enabled: boolean;
  vlan_id?: number | null;
  subnet_cidr: string;
  dhcp_mode: string;
  pool_addresses: number;
  devices_online: number;
  captive_portal_enabled: boolean;
  internet_access_enabled: boolean;
};

export type DashPackage = {
  package_id: string;
  code: string;
  name?: string;
  active: boolean;
  online_now: number;
  grants_7d: number;
};

export type DashboardSnapshot = {
  generated_at: string;
  /** The appliance's local midnight that every "today" figure is measured from. */
  day_start: string;
  guests: DashGuests;
  data: DashData;
  hourly: DashHourBucket[];
  occupancy: DashOccupancy;
  sign_in_checks: DashSignInChecks;
  pms: DashPms;
  postings: DashPostings;
  networks: DashNetwork[];
  packages: DashPackage[];
};

export type BackupRecord = {
  id: string;
  started_at: string;
  finished_at?: string | null;
  status: "running" | "ok" | "failed";
  kind: "scheduled" | "manual" | "pre_migration";
  path?: string | null;
  size_bytes?: number | null;
  error?: string | null;
};

// Portal branding is a free-form jsonb document (logo, T&C, languages…).
export type PortalBranding = Record<string, unknown>;

// ------- Networking (Phase 19 — /edge/v1/network, single "network" key) -------

// Interface — a NIC as discovered by netd (proxied through edged).
export type Interface = {
  name: string;
  mac: string;
  link_state: string;         // up | down | unknown
  mtu: number;
  ips: string[];
  kind: string;               // physical | bond | vlan | bridge …
  parent?: string;
  role?: string;              // guest_access | guest_trunk | ha_sync | unused | management | wan
};

export type Pool = { start_ip: string; end_ip: string };

// GuestNetwork mirrors edged's guestNetworkRow (JSON shape).
export type GuestNetwork = {
  id: string;
  name: string;
  description?: string;
  ssid_label?: string;
  enabled: boolean;
  network_type: "untagged" | "vlan";
  parent_interface: string;
  vlan_id?: number;
  bridge_name: string;
  gateway_ip: string;
  subnet_cidr: string;
  dhcp_mode: "local" | "external" | "relay" | "disabled";
  dns_mode: string;           // appliance | custom
  domain_name: string;
  lease_default_seconds: number;
  lease_min_seconds: number;
  lease_max_seconds: number;
  captive_portal_enabled: boolean;
  internet_access_enabled: boolean;
  nat_enabled: boolean;
  client_isolation_enabled: boolean;
  portal_url?: string;
  pools: Pool[];
  reservations?: Reservation[];
  dns_servers?: string[];
};

// Input for create/update (subset of the row plus dns_servers).
export type GuestNetworkInput = {
  name: string;
  description?: string;
  ssid_label?: string;
  network_type?: "untagged" | "vlan";
  parent_interface?: string;
  vlan_id?: number;
  gateway_ip: string;
  subnet_cidr: string;
  dhcp_mode: string;
  dns_mode: string;
  dns_servers?: string[];
  domain_name: string;
  lease_default_seconds: number;
  lease_min_seconds: number;
  lease_max_seconds: number;
  captive_portal_enabled: boolean;
  internet_access_enabled: boolean;
  nat_enabled: boolean;
  client_isolation_enabled: boolean;
  pools: Pool[];
};

export type GuestNetworkStatus = {
  id: string;
  bridge_name: string;
  enabled: boolean;
  active_clients: number;
};

// DhcpLease — Kea lease shape (hyphenated keys preserved verbatim).
export type DhcpLease = {
  "ip-address": string;
  "hw-address": string;
  hostname?: string;
  "subnet-id"?: number;
  state?: number | string;
  cltt?: number;              // client-last-transaction-time (epoch seconds)
  "valid-lft"?: number;       // valid lifetime (seconds)
};

export type Reservation = {
  id: string;
  guest_network_id: string;
  mac: string;
  reserved_ip: string;
  hostname?: string;
  enabled: boolean;
};

export type ValidationIssue = { field: string; code: string; message: string };
export type ValidationResult = { ok: boolean; issues?: ValidationIssue[] };

export type HealthCheck = { name: string; ok: boolean; detail?: string };

// ValidateResult — POST /network/validate return.
export type ValidateResult = {
  revision_id?: string;
  seq?: number;
  state?: string;
  validation: ValidationResult;
};

// ApplyResult — POST /network/apply return.
export type ApplyResult = {
  revision_id: string;
  seq: number;
  state: "pending_confirmation" | "rolled_back" | "failed" | string;
  validation?: ValidationResult;
  health?: HealthCheck[];
  message?: string;
};

export type NetRevision = {
  id: string;
  seq: number;
  state: string;              // active | pending_confirmation | rolled_back | failed | superseded
  summary?: string | null;
  created_at?: string | null;
  applied_at?: string | null;
  confirmed_at?: string | null;
  confirm_deadline?: string | null;
  failure_reason?: string | null;
};

// RevisionEvent / RevisionHealth — from GET /network/revisions/{id}.
export type RevisionEvent = { phase: string; ok: boolean; detail?: unknown; at?: string };
export type RevisionHealth = { check_name: string; ok: boolean; detail?: string; at?: string };

export type NetRevisionDetail = {
  id: string;
  seq: number;
  state: string;
  validation?: ValidationResult;
  intent?: unknown;
  events?: RevisionEvent[];
  health?: RevisionHealth[];
};

// ------- System (WAN/LAN) networking — the appliance's own base network -------
// GET /edge/v1/network/system  (proxied to netd). WAN = uplink + management.

export type SysConnState = {
  gateway_reachable: boolean;
  internet_ok: boolean;
  dns_ok: boolean;
};

export type SysWAN = {
  interface: string;
  mac: string;
  link_up: boolean;
  mode: "static" | "dhcp" | string;
  ip: string;
  prefix_len: number;
  netmask: string;
  gateway: string;
  dns: string[];
  management_url: string;
  outbound_interface: string;
  connectivity: SysConnState;
  persistent_ip: string;
  drift: boolean;
};

export type SysLAN = {
  physical_interface: string;
  bridge: string;
  mac: string;
  link_up: boolean;
  ip: string;
  prefix_len: number;
  netmask: string;
  gateway_ip: string;
  dhcp_enabled: boolean;
  dhcp_start: string;
  dhcp_end: string;
  dhcp_lease_seconds: number;
  dns: string[];
  members: string[];
};

export type SysNetPending = {
  deadline_unix: number;
  management_url: string;
  backup_path: string;
};

export type SysNetState = {
  wan: SysWAN;
  lan: SysLAN;
  pending?: SysNetPending;
};

// Proposal to change WAN and/or LAN. Only the sections present are changed.
export type SysWANProposal = {
  mode: "static" | "dhcp";
  ip?: string;
  prefix_len?: number;
  gateway?: string;
  dns?: string[];
};
export type SysLANProposal = {
  ip: string;
  prefix_len: number;
  dhcp_enabled: boolean;
  dhcp_start?: string;
  dhcp_end?: string;
  dhcp_lease_seconds?: number;
  dns?: string[];
};
export type SysNetProposal = { wan?: SysWANProposal; lan?: SysLANProposal };

export type SysNetValidateResp = {
  validation: ValidationResult;
  management_url: string;
  effective: { wan: SysWANProposal; lan: SysLANProposal };
};

export type SysNetApplyResp = {
  ok: boolean;
  state: "pending_confirmation" | "failed" | "rolled_back" | string;
  validation: ValidationResult;
  management_url: string;
  backup_path?: string;
  deadline_unix?: number;
  message?: string;
  verify?: Record<string, boolean>;
};

// ------- Cloud Connection (carryover F) — appliance <-> Central status -------
export type CloudStatus = {
  cloud: {
    cloud_api_url?: string;
    nats_url?: string;
    tenant_id?: string;
    site_id?: string;
    appliance_id?: string;
    serial?: string;
    enrolled?: boolean;
    api_mtls?: { mtls_ready?: boolean; cert_fingerprint?: string; not_after?: string };
    nats_mtls?: { connected?: boolean; mtls?: boolean; url?: string };
  };
  license: {
    state?: string;
    installed?: boolean;
    commercial_plan_code?: string;
    valid_until?: string;
    grace_until?: string | null;
    offline_grace_days?: number;
    last_cloud_validation?: string | null;
    cloud_stale?: boolean;
  };
  outbox: OutboxFigures;
  connection: { state?: string; reachable?: boolean; cert_valid?: boolean; http_code?: number; error?: string };
};

// ------- Appliance setup / enrollment (GET /setup/status, POST /setup/enroll) -------
// The local enrollment wizard's live state, straight from edged. No secrets are
// ever included (the bootstrap/enrollment token is write-only, never returned).
export type SetupStatus = {
  serial?: string;
  hardware?: {
    serial?: string;
    wan_interface?: string;
    wan_mac?: string;
    lan_interface?: string;
    lan_mac?: string;
    hostname?: string;
    model?: string;
  };
  activation_status?: "unlicensed" | "pending_activation" | "licensed" | "activated" | "mismatch" | string;
  hardware_mismatch?: string;
  // Non-empty when a production appliance REJECTED an attempt to enable
  // permissive/dev licensing (critical security event).
  permissive_blocked?: string;
  build_profile?: string;
  appliance_id?: string;
  identity_key_fingerprint?: string;
  version?: string;
  enrolled?: boolean;
  locked?: boolean;
  tenant_id?: string;
  site_id?: string;
  api_mtls?: { mtls_ready?: boolean; cert_fingerprint?: string; not_after?: string };
  nats_mtls?: { connected?: boolean; mtls?: boolean };
  license?: {
    state?: string; license_id?: string; plan?: string;
    valid_from?: string; valid_until?: string; offline_grace_days?: number;
    // Simple license model: usage against the licensed cap.
    license_version?: number;
    grace_period_days?: number;
    grace_ends_at?: string;
    max_concurrent_online_guests?: number; // -1 unlimited
    current_online_guests?: number;
    remaining_capacity?: number;
    usage_percent?: number;
  };
  assignment?: {
    status?: string;
    assigned?: boolean;
    lifecycle_state?: string;
    version?: number;
    tenant_name?: string;
    site_name?: string;
    adopted_at?: string;
    last_refresh_success?: string;
  };
  outbox?: { pending?: number; dead?: number };
  network?: {
    dns_ok?: boolean;
    central_https_443?: boolean;
    mtls_9443?: boolean;
    nats_4223?: boolean;
    clock?: boolean;
  };
};

// POST /setup/enroll success (202) envelope.
export type EnrollResult = { status: string; appliance_id?: string; note?: string };

export type SysNetAudit = {
  at: string;
  actor: string;
  source_ip: string;
  action: string;
  target: string;
  apply_result: string;
  confirm_result: string;
  rollback_result: string;
  failure_reason: string;
  backup_path: string;
};

// ---- Phase 3 (DARK): the PMS interface itself -------------------------------
// The credential never appears in any of these shapes. There is no field for it because there is no endpoint
// that returns it — see the write-only rule in edged's resources_phase3_interfaces.go.

export type PmsInterface = {
  id: string; connector_kind: string; display_label: string; lifecycle_state: string;
  current_revision_id?: string; current_revision_no?: number | null;
  revision_count: number; published: boolean;
  secret_generation?: number | null; secret_rotated_at?: string | null;
  // Projections of the published Revision, so the list can say what the interface points at without the
  // operator opening its revision history.
  endpoint?: string | null; source_timezone?: string | null; financial_base_currency?: string | null;
};

export type PmsRevision = {
  id: string; revision_no: number; source_timezone: string; folio_identity_strategy: string;
  normalization_version: number; source_fingerprint?: string;
  // already redacted by edged; the client never un-redacts anything
  config: Record<string, unknown>;
  published: boolean;
};

export type PmsInterfaceHealth = {
  pms_interface_id: string;
  // Whether this interface can currently serve Room authentication, decided by the server from the same
  // runtime and active Revision that Phase 3 reads — including the Revision's own heartbeat_timeout_ms,
  // which a client cannot know. Excludes Stay-specific occupancy evidence: that is per guest, not per feed.
  // room_auth_reason is a bounded code from a closed set, empty when ready.
  room_auth_ready?: boolean;
  room_auth_reason?: string;
  transport_status: string; last_connected_at?: string | null; last_heartbeat_at?: string | null;
  disconnected_since?: string | null; transport_error_code?: string;
  continuity_status: string; last_valid_event_at?: string | null; discontinuity_detected_at?: string | null;
  sync_status: string; resync_requested_at?: string | null; resync_started_at?: string | null;
  last_complete_sync_at?: string | null; last_sync_failure_code?: string;
  in_house_stays: number; last_stay_event_at?: string | null;
  pending_events: number; review_events: number; oldest_pending_at?: string | null;
  // SYNCHRONIZATION. Note what is absent and stays absent: there is no total, no percentage and no
  // remaining-record field, because FIAS provides no total before the end of a sync and any such number
  // would be invented by the client. sync_records_received is a real running count of records staged so far.
  sync_stage?: string; sync_stage_at?: string | null;
  sync_records_received?: number; sync_records_skipped?: number;
  sync_failure_code?: string;
  // Two DIFFERENT facts. sync_stage=COMPLETE means a generation was published; materialization_ready means
  // the applier has finished writing it into the Stay tables. The UI renders the effective state from both.
  // last_sync_in_house_count is gone: it was stamped at the publish barrier and described the roster the sync
  // replaced, contradicting the live figure on the same screen.
  materialization_ready?: boolean;
  resync_command_reason?: string; resync_command_requested_at?: string | null;
};

export type PmsGuestNetworkRoute = {
  guest_network_id: string; guest_network_name?: string;
  pms_interface_id: string; pms_interface_label?: string;
  is_default: boolean; routing_mode: string;
};

export type PmsSourceConflict = {
  id: string; interface_a: string; interface_a_label?: string;
  interface_b: string; interface_b_label?: string;
  severity?: string; resolution?: string;
};

// ---------------------------------------------------------------- Phase 4 (DARK): financial operations
//
// These mirror the edged financial-ops surface exactly. Note what the types CANNOT express: there is no
// provider reference, no idempotency key and no guest field anywhere. The API does not send them, and the
// client cannot invent a place to put them.

export type FinancialHealth = {
  outbox_queued: number;
  outbox_in_flight: number;
  outbox_held_recovery: number;
  outbox_oldest_age_seconds: number;
  postings_unknown: number;
  review_queue_open: number;
  review_oldest_age_seconds: number;
  payments_created: number;
  payments_pending: number;
  payments_unknown: number;
  payments_oldest_age_seconds: number;
  settlements_required: number;
  settlements_in_progress: number;
  settlements_manual_review: number;
  settlements_failed: number;
  recovery_active: boolean;
  recovery_epoch: number;
  recovery_holds_open: number;
  payment_account_configured: boolean;
  provider_egress_enabled: boolean;
  status: "OK" | "DEGRADED" | "ATTENTION_REQUIRED" | "HELD";
  reasons: string[];
};

export type FinancialSettlement = {
  settlement_id: string;
  purchase_id: string;
  method: string;
  status: string;
  purchase_state: string;
  amount_minor: number;
  currency: string;
  currency_exponent: number;
};

export type FinancialPayment = {
  payment_id: string;
  transaction_type: "CHARGE" | "REFUND" | "CHARGEBACK";
  status: string;
  provider: string;
  amount_minor: number;
  currency: string;
  currency_exponent: number;
  parent_transaction_id: string | null;
};

export type RecoveryStatus = {
  Epoch: number;
  Reason: string;
  Active: boolean;
  HeldTotal: number;
  HeldOpen: number;
  EnteredAt: string;
  ReleasedAt: string;
};

export type RecoveryHold = {
  hold_id: string;
  work_kind: "POSTING_OUTBOX" | "PAYMENT_TRANSACTION" | "SETTLEMENT";
  work_id: string;
  held_status: string;
  amount_minor: number | null;
  currency: string;
  held_at: string;
};

// The four conclusions an operator may reach about a held item. Held deliberately in one place so the UI
// and the database cannot drift into offering different words for the same decision.
export const RECOVERY_RESOLUTIONS = [
  "CONFIRMED_COMPLETED",
  "CONFIRMED_NOT_COMPLETED",
  "ABANDONED",
  "ESCALATED",
] as const;
export type RecoveryResolution = (typeof RECOVERY_RESOLUTIONS)[number];

// Postings held by recovery that were NEVER transmitted. They cannot appear in the Manual Review queue,
// which keys on attempts, so before this they were reachable from no operator surface at all -- the one
// state a restore can produce that nobody could decide.
//
// eligible_for_retry_authorization is SERVED, not computed here. The screen must not be able to disagree
// with the database about what is allowed.
export type ZeroAttemptRow = {
  posting_id: string;
  outbox_id: string;
  pms_interface_id: string;
  amount_minor: number;
  currency: string;
  currency_exponent: number;
  hold_id: string | null;
  hold_resolution: string | null;
  retry_authorized_attempt_no: number | null;
  eligible_for_retry_authorization: boolean;
};

export type ZeroAttemptQueue = {
  queue: ZeroAttemptRow[];
  limit: number;
  evidence_contract?: { source_types: string[] };
  note: string;
  eligibility: string;
};

// ---------------------------------------------------------------- Phase 4 (DARK): Manual Review
//
// The action catalog and the evidence contract are SERVED, not declared here. §15's rules live in the
// database; a second copy in the frontend would drift, and the first symptom would be an operator offered
// an action the backend refuses.

export type ReviewActionDoc = {
  action: string;
  terminal: boolean;
  needs_evidence: boolean;
  accepts_amount: boolean;
  summary: string;
};

export type ReviewQueueRow = {
  posting_id: string;
  pms_interface_id: string;
  execution_state: string;
  amount_minor: number;
  currency: string;
  currency_exponent: number;
  latest_attempt_no: number | null;
  latest_p_number: string | null;
  latest_pa_as_status: string | null;
  outbox_state: string | null;
  review_version: number | null;
  terminal_review_action: string | null;
  awaiting_manual_review: boolean;
  created_at: string;
};

export type ReviewAttempt = {
  attempt_no: number;
  p_number: string;
  rn: string;
  g_number: string;
  outcome: string;
  pa_as_status: string | null;
  sent_at: string;
  response_at: string | null;
};

export type ReviewPostingDetail = {
  posting: ReviewQueueRow;
  pinned_evidence: {
    settlement_id: string;
    purchase_id: string;
    stay_id: string | null;
    folio_id: string | null;
    connector_kind: string;
    folio_identity_strategy: string;
    interface_lifecycle_state: string;
    settlement_status: string;
    purchase_state: string;
  };
  attempts: ReviewAttempt[];
  review: {
    history: { action: string; actor: string; reason: string; created_at: string }[];
    version: number | null;
    terminal_action: string | null;
    escalation_count: number;
    retry_authorized_attempt_no: number | null;
    retry_authorization_consumed: boolean;
  };
  diagnostics: {
    attempt_count: number;
    unknown_attempt_count: number;
    has_unknown_history: boolean;
    interface_freshness_block: string | null;
  };
  available_actions: string[];
  evidence_contract?: { source_types: string[] };
  limitations: string[];
};

// ---------------------------------------------------------------------------------------------------------
// CLOUD SYNC — how long delivered records are kept, and rescuing the ones the appliance gave up on.
// ---------------------------------------------------------------------------------------------------------

export type CloudSyncSettings = {
  delivered_retention_days: number;
  is_default: boolean;
  /** The server's own bounds, sent so the form cannot validate against a copy that has drifted. */
  limits: { min_days: number; max_days: number };
  last_change?: {
    changed_at: string;
    changed_by: string;
    reason?: string;
    old_delivered_retention_days?: number | null;
    new_delivered_retention_days: number;
    new_config_version: number;
  } | null;
};

export type CloudSyncRecovery = {
  requested_at: string;
  requested_by: string;
  reason: string;
  recovered: number;
  seq_from?: number;
  seq_to?: number;
  oldest_created_at?: string | null;
  exhausted_remaining: number;
};

// ---------------------------------------------------------------------------------------------------------
// PMS RECONCILIATION — unresolved departures as cases, not as rows.
// ---------------------------------------------------------------------------------------------------------

/**
 * resolution_state is computed by the server from current evidence, and the UI never re-derives it.
 *
 *  RESOLVABLE             one candidate stay, it began before the departure, and the PMS's own latest
 *                         complete roster does not contain it — so a departure sent from the PMS for that
 *                         room will apply cleanly. NOT an action this system can take on its own.
 *  SUPERSEDED_ROOM_EMPTY  the room holds nobody now. Nothing outstanding, and nothing to close.
 *  ROOM_SHARED            more than one stay in the room — undecidable from a room number, by construction.
 *  LATER_OCCUPANT         the one stay in that room arrived AFTER the departure was raised.
 *  ROSTER_CONTRADICTS     the PMS's current roster still lists this stay as in house.
 *  NEEDS_PMS_EVIDENCE     everything else, including anything seen with no complete roster to compare to.
 */
export type ReconciliationState =
  | "RESOLVABLE"
  | "SUPERSEDED_ROOM_EMPTY"
  | "ROOM_SHARED"
  | "LATER_OCCUPANT"
  | "ROSTER_CONTRADICTS"
  | "NEEDS_PMS_EVIDENCE";

export type ReconciliationCase = {
  case_key: string;
  reservation?: string;
  room?: string;
  /** How many recorded copies of this one departure exist. Shown, not hidden — it describes the feed. */
  repeat_count: number;
  generations: number;
  first_seen_at: string;
  last_seen_at: string;
  event_at?: string;
  latest_event_id: string;
  review_code?: string;
  candidate_stays: number;
  candidate_stay_id?: string;
  roster_present: boolean;
  resolution_state: ReconciliationState;
  /** True for every case: the PMS resolves them, never this screen. The state says which evidence is due. */
  needs_pms_evidence: boolean;
};

export type ReconciliationSummary = {
  cases: number;
  recorded_rows: number;
  by_state: { state: ReconciliationState; cases: number; recorded_rows: number }[];
  rooms_multi_occupancy: number;
  stays_past_departure: number;
  stays_past_departure_absent_from_roster: number;
};

export type MultiOccupancyRoom = {
  room: string;
  stays_in_room: number;
  earliest_arrival?: string | null;
  latest_planned_departure?: string | null;
};

export type StayPastDeparture = {
  stay_id: string;
  room?: string;
  reservation?: string;
  arrival?: string | null;
  departure?: string | null;
  days_past_departure: number;
  roster_present: boolean;
  /** Stronger than "not listed": the PMS has that room under a DIFFERENT reservation, so it has been re-let. */
  room_now_holds_another_stay: boolean;
};
