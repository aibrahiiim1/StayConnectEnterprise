// Client for /api/edge/v1/*, which Next rewrites to edged. The site is implicit, so no tenant/site params.

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

export const EDGE_BASE = "/api/edge/v1";

// Marks a request as background: edged does not extend the session idle limit for it, so polls
// cannot keep a session alive forever.
export const BACKGROUND_HEADER = "X-OneGate-Background";

let backgroundSpans = 0;

/** Runs `fn` with every GET it starts (even after an await) marked background. Only GETs: a save is still an action. */
export function inBackground<T>(fn: () => T): T {
  backgroundSpans++;
  let out: T;
  try {
    out = fn();
  } catch (e) {
    backgroundSpans--;
    throw e;
  }
  if (out && typeof (out as any).then === "function") {
    const end = () => { backgroundSpans--; };
    (out as any).then(end, end);
  } else {
    backgroundSpans--;
  }
  return out;
}

async function request<T>(
  method: string,
  path: string,
  body?: any,
  init?: RequestInit
): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method === "GET" && backgroundSpans > 0) headers[BACKGROUND_HEADER] = "1";
  Object.assign(headers, (init?.headers as Record<string, string> | undefined) ?? {});
  const res = await fetch(`${EDGE_BASE}${path}`, {
    method,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    cache: "no-store",
    ...init,
    headers: Object.keys(headers).length ? headers : undefined,
  });
  const contentType = res.headers.get("content-type") ?? "";
  const payload = contentType.includes("application/json") ? await res.json() : await res.text();
  if (!res.ok) throw new ApiError(res.status, payload);
  return payload as T;
}

export const api = {
  // headers: for values that must not travel in the URL (edged logs request lines), e.g. a stay search.
  // signal: lets a superseded read be cancelled (useServerPage) instead of running to the end (perf audit D8-L1).
  get:   <T>(path: string, headers?: Record<string, string>, opts?: { signal?: AbortSignal }) =>
    request<T>("GET", path, undefined,
      headers || opts?.signal ? { ...(headers ? { headers } : {}), ...(opts?.signal ? { signal: opts.signal } : {}) } : undefined),
  post:  <T>(path: string, body?: any)    => request<T>("POST", path, body),
  put:   <T>(path: string, body?: any)    => request<T>("PUT", path, body),
  patch: <T>(path: string, body?: any)    => request<T>("PATCH", path, body),
  del:   <T>(path: string, body?: any)    => request<T>("DELETE", path, body),
  // Content-type is deliberately NOT set: fetch derives it and the multipart boundary from the FormData.
  // Uses EDGE_BASE: a hand-written fetch without the `/api` prefix is not rewritten to edged.
  upload: async <T>(path: string, file: File, field = "file"): Promise<T> => {
    const form = new FormData();
    form.append(field, file);
    const res = await fetch(`${EDGE_BASE}${path}`, { method: "POST", body: form, cache: "no-store" });
    const contentType = res.headers.get("content-type") ?? "";
    const payload = contentType.includes("application/json") ? await res.json() : await res.text();
    if (!res.ok) throw new ApiError(res.status, payload);
    return payload as T;
  },
  // postRaw sends a pre-serialized body without re-encoding it.
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

// A phase-gated surface edged has not mounted answers 404; screens are compiled in at build time while
// mounting is a runtime flag, so the UI says the capability is off rather than showing a raw 404.
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
  // Which domain answered, so an empty list can still tell which contract it is talking to.
  authority?: "iam_v2" | "legacy";
};

export type Whoami = {
  operator_id: string;
  email: string;
  display_name?: string;
  roles: string[];
  site_id: string;
  expires_at: string;
  must_change_password?: boolean;
};

export type GuestAccessPlan = {
  id: string; tenant_id: string; code: string; name: string;
  description?: string | null;
  duration_seconds?: number | null; data_cap_bytes?: number | null;
  down_kbps?: number | null; up_kbps?: number | null;
  max_concurrent_devices: number; is_active: boolean;
  price_cents?: number | null; currency?: string | null;
};

// Under iam_v2 a batch is not a row: it is the set of vouchers sharing a batch_id, derived rather than fetched.
export type VoucherState = "UNUSED" | "REDEEMED" | "REVOKED" | "REDEMPTION_EXPIRED";

export type VoucherSummary = {
  unused: number; redeemed: number; revoked: number; redemption_expired: number;
};

// config_version 0 means nobody has chosen and these are defaults.
export type VoucherCodeFormat = {
  code_mode: "numbers" | "mixed";
  code_length: 6 | 7 | 8;
  config_version: number;
  updated_at?: string;
};

// Append-only record of who recovered a code in the clear. No field can carry a code: it records THAT one was read.
export type VoucherReveal = {
  revealed_at: string;
  action: "REVEAL" | "EXPORT";
  voucher_id: string | null;
  voucher_count: number;
  operator_label: string;
  reason: string;
  selection: string | null;
};

export type GuestAccount = {
  id: string; username: string; display_name?: string | null; notes?: string | null;
  // Absent under IAM-v2, which has no template/plan on the credential.
  template_id?: string | null; enabled: boolean;
  valid_from?: string | null; valid_until?: string | null;
  last_login_at?: string | null; login_count: number;
  // No locked_until: nothing sets it; repeated-failure protection is per device.
  // Derived (list/get): plan device cap + live distinct active devices.
  max_devices?: number | null; active_devices?: number;
  created_at?: string | null; updated_at?: string;
  // Self-registered accounts: username is the email; `suspended` is absent when site settings could not be read.
  self_registered?: boolean;
  email_verified?: boolean;
  email_verified_at?: string | null;
  verification_deadline?: string | null;
  grace_until?: string | null;
  grace_extensions_used?: number;
  suspended?: boolean;
};

export type GuestAccountDevice = {
  id: string; mac: string; status: "AUTHORIZED" | "DISCONNECTED" | string; online: boolean;
  first_authorized?: string | null; last_authorized?: string | null;
};
export type GuestAccountPackage = {
  id: string; status: string; terminal_reason?: string | null; package_name: string;
  activated_at?: string | null; window_ends_at?: string | null; terminated_at?: string | null;
  consumed_data_bytes: number; data_quota_bytes?: number | null; consumed_online_seconds: number;
  max_devices?: number | null; devices: GuestAccountDevice[];
};
export type GuestAccountManage = {
  account: GuestAccount;
  entitlements: GuestAccountPackage[];
  entitlements_total: number;
  removed: boolean;
  removal: "delete" | "remove";
};
export type DevicePackage = {
  entitlement_id: string; status: string; package_name: string; free: boolean;
  subject_kind: "ACCOUNT" | "VOUCHER" | "ROOM" | "CLIENT" | "OPEN"; subject_label?: string; pms_interface_label?: string; account_id?: string;
  slot: "AUTHORIZED" | "DISCONNECTED" | string; slot_reason?: string;
  first_authorized?: string | null; last_authorized?: string | null; window_ends_at?: string | null;
  consumed_data_bytes: number; data_quota_bytes?: number | null; max_devices?: number | null; places_in_use: number;
};
export type ClientDeviceRow = {
  id: string; mac: string; last_ip?: string; first_seen?: string | null; last_seen?: string | null;
  online: boolean; online_ip?: string; current?: DevicePackage | null;
  // On the Wi-Fi now (active DHCP lease); an empty id means it has never signed in.
  on_network?: boolean; lease_ip?: string; hostname?: string; lease_expires?: string | null;
};
export type ClientDeviceDetail = {
  device: ClientDeviceRow;
  packages: DevicePackage[];
  sessions: { id: string; started?: string | null; ended?: string | null; state: string; end_reason?: string;
    method?: string; ip?: string; package_name?: string }[];
  restriction: { restricted: boolean; until?: string | null; failures?: number | null };
  remembered_signins: number;
  free_packages_used: number;
  actions: { acted_at: string; actor: "CLIENT" | "STAFF"; operator?: string; action: string; outcome: string; reason?: string }[];
};
export type DeviceSettings = { client_device_changes_per_day: number };

export type GuestAccountRemoveResp = {
  status: "deleted" | "removed"; history_kept?: boolean; disconnected_sessions?: number; disconnect_failures?: number;
};

export type SelfServiceSettings = {
  verification_required: boolean;
  verification_deadline_hours: number;
  suspension_policy: "SUSPEND" | "WARN_ONLY";
  reminder_hours_before: number;
  resend_cooldown_seconds: number;
  resend_max_per_day: number;
  grace_minutes: number;
  max_grace_extensions: number;
  registrations_per_device_per_day: number;
};
export type SelfServiceBound = { key: keyof SelfServiceSettings; min: number; max: number; unit: string; default: number };
export type SelfServiceChange = { changed_at: string; changed_by?: string; change_reason?: string } & Record<string, unknown>;
export type SelfServiceSettingsResp = {
  settings: SelfServiceSettings;
  defaults: SelfServiceSettings;
  config_version: number;
  updated_at?: string | null;
  updated_by?: string | null;
  limits: { bounds: SelfServiceBound[]; suspension_policies: string[] };
  changes: SelfServiceChange[];
  awaiting_assignment?: boolean;
};

export type GuestAccountsPage = ListResp<GuestAccount> & {
  page: number;
  page_size: number;
  summary: { total: number; enabled: number; disabled: number; devices_online: number };
};

// Password reveal is one-time only.
export type GuestAccountCreateResp = { account: GuestAccount; generated_password?: string };
// disconnected_sessions counts sessions the appliance CONFIRMED ended; disconnect_failures counts devices scd could not
// remove.
export type GuestAccountPasswordResp = {
  status: string; disconnected_sessions?: number; disconnect_failures?: number; generated_password?: string;
};

export type AccountPasswordStyle = {
  key: string;
  label: string;
  alphabet: string;
  min_length: number;
  max_length: number;
  bits_per_char: number;
};
export type AccountPasswordFormat = {
  password_style: string;
  password_length: number;
  config_version: number;
  updated_at?: string | null;
  updated_by?: string | null;
  limits: {
    entropy_floor_bits: number;
    max_length: number;
    default_style: string;
    default_length: number;
    styles: AccountPasswordStyle[];
  };
};
export type AccountPasswordFormatChange = {
  changed_at: string;
  changed_by: string;
  change_reason: string;
  old_password_style: string | null;
  old_password_length: number | null;
  new_password_style: string;
  new_password_length: number;
  new_config_version: number;
};

// No `code` on the list type: a list left open must not reveal one. Only the last four characters are shown.
export type Voucher = {
  id: string;
  code_last4: string;
  state: VoucherState;
  package_revision_id: string;
  batch_id: string | null;
  created_at: string;
  redemption_valid_from: string | null;
  redemption_valid_until: string | null;
  notes: string | null;
  issued_by: string | null;
};

// The four things an Entitlement (hence a session) can belong to. An empty string means unreadable, not subject-less.
export type SubjectKind = "room" | "account" | "voucher" | "guest" | "open" | "";

export type Session = {
  id: string; tenant_id: string; site_id: string; appliance_id: string;
  guest_id: string; voucher_id?: string | null;
  ip: string; mac: string;
  state: string; end_reason?: string | null;
  started_at: string; last_activity_at: string; ended_at?: string | null;
  expires_at?: string | null;
  bytes_up: number; bytes_down: number;

  // A voucher code and a guest principal's contact details are deliberately NOT included.
  credential_method?: string | null;
  /** The PMS connection a room session's stay came from. */
  pms_interface_label?: string | null;
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
  package_acquisitions?: number;
};

// Resolution evidence deliberately carries no guest identity.

export type Stay = {
  id: string; pms_interface_id: string; pms_interface_label?: string; external_reservation_id: string;
  room?: string | null; status: string; lifecycle_version: number;
  arrival?: string | null; departure?: string | null;
  effective_checkout_at?: string | null; posting_allowed: boolean; occupants: number;
  primary_guest?: string | null;
  posting_block_reason?: string | null; posting_permission_source?: string | null;
  occupancy_evidence_at?: string | null;
  // VIP / travel agent come from Protel FIAS mapped fields (A1, A0); room type and rate plan only where a connector
  // sends them.
  vip?: boolean | null; room_type?: string | null; rate_plan?: string | null; travel_agent?: string | null;

  // Absent when the stay has no live access (a common state, not a missing value).
  access_status?: string | null;
  access_package_code?: string | null;
  access_package_name?: string | null;
  access_plan_code?: string | null;
  access_down_kbps?: number | null;
  access_up_kbps?: number | null;
  access_max_devices?: number | null;
  access_active_devices?: number;
  // Devices holding a place on the package (what the device limit is enforced against); a device keeps its place after
  // sign-out.
  access_authorized_devices?: number;
  identity_hidden?: boolean;
};

export type StaysPage = {
  data: Stay[];
  meta: { has_more: boolean };
  page: number;
  page_size: number;
  summary: { total: number; with_internet: number; devices_online: number; arriving: number; vip: number };
  /** What each PMS connection would hold under the current filters (ignoring the connection filter itself). */
  by_interface?: { pms_interface_id: string; label: string; lifecycle_state: string; total: number; in_house: number }[];
};

export type TravelAgent = { name: string; in_house: number; stays: number };

export type StayDetail = Stay & {
  occupant_list: { display_name?: string | null; is_primary: boolean }[];
  // Room charges target the reservation (room + reservation number); there are no folios.
  posting_blocks?: StayPostingBlock[];
  room_charge_open?: boolean;
};

export type StayPostingBlock = {
  reason: "PMS_NO_POST" | "PMS_DATA_SUSPECT" | "POSTING_UNRESOLVED" | "ADMIN_BLOCK" | string;
  source: "PMS_ANSWER" | "POSTING_LEDGER" | "OPERATOR" | string;
  pa_as_status?: string | null;
  note?: string | null;
  created_at: string;
  created_by?: string | null; // operator e-mail (administrative blocks)
  cleared_at?: string | null;
  cleared_by_source?: string | null;
  cleared_by?: string | null; // operator e-mail (administrative blocks)
  cleared_reason?: string | null;
};

export type StayEvent = {
  id: string; pms_interface_id: string; external_event_identity: string; event_type: string;
  processing_status: string; review_code?: string | null; stay_id?: string | null;
  pms_timestamp_utc?: string | null; received_at: string;
  // Absent when the appliance could not match the event to a stay.
  pms_interface_label?: string;
  room?: string | null;
  external_reservation_id?: string | null;
  primary_guest?: string | null;
  stay_status?: string | null;
  decided?: boolean;
  decision?: string | null;
  message_reservation?: string | null;
  message_room?: string | null;
  identity_hidden?: boolean;
};

export type PmsResolution = {
  id: string; guest_network_id: string; outcome_code: string; resolved: boolean; resolved_at: string;
  guest_network_name?: string | null;
};

// Credential values are never in SignInAttempt/Detail; SignInAttemptCredentials has its own endpoint and permission.
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

// Policy and restrictions are separate types behind separate permissions.

export type GuestSignInProtection = {
  max_failed_attempts: number;
  observation_window_seconds: number;
  restriction_seconds: number;
  // Never saved: running on approved defaults (not "protection off").
  is_default: boolean;
  // The server's bounds, so the form validates against the same numbers.
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
  // UNVERIFIED INPUT: the room this device last typed.
  last_submitted_room?: string;
  failure_count: number;
  reason: string;
  restricted_at: string;
  expires_at: string;
  // Server-computed at read time so an open screen does not count down against a drifting client clock.
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

// One published version of the hotel's policy, from the append-only publication ledger.
export type GraceHistoryEntry = {
  config_version: number;
  published_at: string;
  actor: string;
  reason_code: string;
  policy: {
    grace_duration_seconds?: number;
    grace_down_kbps?: number;
    grace_up_kbps?: number;
    grace_data_quota_bytes?: number;
    grace_device_limit?: number;
    grace_device_limit_policy?: string;
    eligibility_window_seconds?: number;
  } | null;
};

// Retained for diagnostics only: the system derives the package that expresses the policy.
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
  // Sent back with every action so a concurrent change is a 409, not a silent overwrite.
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

export type EdgeOperator = {
  id: string;
  email: string;
  display_name: string;
  status: "active" | "disabled" | string;
  roles: string[];
  created_at: string;
  must_change_password?: boolean;
  two_step_enrolled?: boolean;
};

export type SessionPolicy = {
  idle_timeout_minutes: number;
  max_session_hours: number;
  config_version: number;
  updated_at?: string | null;
  updated_by?: string | null;
};
export type SessionPolicyBound = {
  key: keyof Pick<SessionPolicy, "idle_timeout_minutes" | "max_session_hours">;
  label: string; unit: string; min: number; max: number; default: number; explains: string;
};

export type AccountRetention = {
  disabled_account_days: number;
  config_version: number;
  updated_at?: string | null;
  updated_by?: string | null;
};
export type AccountRetentionBound = {
  key: "disabled_account_days";
  label: string; unit: string; min: number; max: number; default: number; explains: string;
};

export type SigninProtection = {
  account_max_failures: number;
  account_window_seconds: number;
  ip_max_failures: number;
  ip_window_seconds: number;
  lockout_seconds: number;
  config_version: number;
  updated_at?: string | null;
  updated_by?: string | null;
};
export type SigninProtectionBound = {
  key: keyof Pick<SigninProtection, "account_max_failures" | "account_window_seconds" | "ip_max_failures" | "ip_window_seconds" | "lockout_seconds">;
  label: string; unit: string; min: number; max: number; default: number; explains: string;
};

export type WalledGardenRule = {
  id: string; tenant_id: string; site_id?: string | null;
  kind: "domain" | "cidr" | "ip";
  value: string;
  ports?: number[] | null;
  description?: string | null;
  created_at: string;
  wider_than_policy?: boolean;
};

export type WalledGardenPolicy = {
  widest_prefix_len: number; min: number; max: number; default: number;
  config_version: number; updated_at?: string | null; updated_by?: string | null;
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
  scopes?: string;
  callback_hosts: string[];
  callback_urls: { hostname: string; kind: string; url: string; canonical: boolean }[];
  secret_set: boolean;
  offered: boolean;
  offered_reason?: string;
  tenant?: string;   // Microsoft: directory (tenant) ID, or common / organizations / consumers
  team_id?: string;  // Apple: Team ID
  key_id?: string;   // Apple: Key ID of the .p8 key
  last_success_at?: string;
  last_error?: string;
  last_error_at?: string;
  created_at: string;
  updated_at: string;
};

export type NotificationProvider = {
  id: string;
  tenant_id: string;
  channel: "email" | "sms" | "whatsapp";
  // "ses" is not offered for a new sender; a stored row of that kind is still listed so it can be removed.
  kind: "stub" | "sendgrid" | "smtp" | "ses" | "twilio" | "meta_whatsapp" | "twilio_whatsapp";
  enabled: boolean;
  display_name?: string;
  api_user?: string;       // Twilio account SID / Meta phone number ID — not a secret
  // The secret travels as `api_key`, write-only, never in this shape.
  extra?: {
    template_name?: string; language?: string; content_sid?: string;
    host?: string; port?: string; security?: "starttls" | "tls" | "none"; username?: string; timeout_seconds?: string | number;
  } | null;
  from_address?: string;
  from_name?: string;
  region?: string;
  last_success_at?: string;
  last_error?: string;
  last_error_at?: string;
  created_at: string;
  updated_at: string;
};

export type NotificationTestResult = { ok: boolean; kind?: string; error?: string; message?: string };

export type ClientGroupRuleType = "EMAIL_DOMAIN" | "IDP_TENANT" | "IDP_HOSTED_DOMAIN";
export type ClientGroupRule = {
  id?: string;
  type: ClientGroupRuleType;
  value: {
    domains?: string[];
    include_subdomains?: boolean;
    provider?: string;
    tenant_ids?: string[];
  };
};
export type ClientGroup = {
  id: string;
  name: string;
  description?: string | null;
  priority: number;
  enabled: boolean;
  rules: ClientGroupRule[];
  created_at: string;
  updated_at: string;
};
export type ClientGroupChange = {
  id: string;
  group_id: string;
  action: "CREATED" | "UPDATED" | "DELETED";
  changed_by?: string | null;
  reason?: string | null;
  before?: Partial<ClientGroup> | null;
  after?: Partial<ClientGroup> | null;
  changed_at: string;
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

export type ActivationState = "not_registered" | "waiting" | "activating" | "activated" | "retired";
export type LicenseState =
  | "none" | "active" | "expiring" | "grace" | "expired" | "suspended" | "revoked" | "wrong_hardware";
export type CentralLinkState = "connected" | "unreachable" | "credential_refused" | "not_configured";

export type CentralStatus = {
  activation: ActivationState;
  serial: string;
  appliance_id: string | null;
  customer_name: string | null;
  site_name: string | null;
  license: {
    state: LicenseState;
    valid_until: string | null;
    grace_ends_at: string | null;
    days_left: number | null;
    max_concurrent_online_guests: number | null;
    current_online_guests: number | null;
    hardware_notice?: string;
  };
  central: { state: CentralLinkState; last_contact_at: string | null; last_error: string | null };
  details: {
    identity_key_fingerprint: string;
    cert_fingerprint: string;
    cert_not_after: string | null;
    assignment_version: number | null;
    license_version: number | null;
    wan_mac: string;
    lan_mac: string;
    central_endpoint: string;
    assignment_status?: "absent" | "unverifiable" | "not_granting" | "granted" | string;
    license_id?: string;
    software_version?: string;
    build_profile?: string;
    permissive_blocked?: string;
    /** "removed_from_central" blocks new guests and requires factory-clean installation. */
    reason?: string;
  };
};

export type CentralRefresh = CentralStatus & {
  diagnostics?: { dns_ok?: boolean; central_https?: boolean; central_mtls?: boolean; clock?: string };
};

export type EdgeHealth = {
  service: string;
  version: string;
  site_id: string;
  status: "ok" | "degraded";
  db: boolean;
  scd: boolean;
  license_state?: LicenseState | null;
  license_installed?: boolean;
  activation?: ActivationState | null;
};

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

// Each section can fail independently; `available: false` means the appliance lacks that surface, which is not zero
// activity.

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
  historical_exceptions?: number;
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

export type PortalBranding = Record<string, unknown>;

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

export type GuestNetwork = {
  id: string;
  name: string;
  /** Switched off by a confirmed reset and hidden from the main list. */
  archived?: boolean;
  /** The prepared network that is serving as the site's Default Network. */
  is_default?: boolean;
  /** serving | off | archived: the one reading of whether this network is a place clients can be served. */
  service_state?: "serving" | "off" | "archived";
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

export type ValidateResult = {
  revision_id?: string;
  seq?: number;
  state?: string;
  validation: ValidationResult;
};

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
  // The pending change's id, once netd reports it: confirm sends it back so a different change is not kept.
  revision_id?: string;
};

export type SysNetState = {
  wan: SysWAN;
  lan: SysLAN;
  pending?: SysNetPending;
};

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
  revision_id?: string;
};

export type BackupHealth = {
  retention?: Record<string, any>;
  retention_readable: boolean;
  retention_error?: string;
  timer_active: boolean;
  timer_next_run?: string;
  timer_last_run?: string;
  timer_readable: boolean;
  database_backups: number;
  newest_database_backup?: string;
};

export type BackupArtifact = {
  name: string;
  kind: string;
  size_bytes: number;
  modified_at: string;
  downloadable: boolean;
};

export type BackupVerifyResult = {
  ok: boolean;
  tables: number;
  detail?: string;
  duration: string;
};

export type RetentionSetting = {
  key: string; label: string; unit: string;
  default: number; min: number; max: number; value: number; explains: string;
};

export type BackupSettings = {
  retention: RetentionSetting[];
  schedule?: string;
  schedule_known: boolean;
  config_present: boolean;
};

export type PortalAsset = {
  name: string; url: string; size_bytes: number; uploaded_at: string; in_use: boolean;
  /** "ready" once stored; "optimising" while the appliance scales and compresses it (no url yet); "failed" with a reason. */
  status?: "ready" | "optimising" | "failed"; reason?: string;
};

/** The hard upload limit for portal images, enforced by the appliance as well. */
export const PORTAL_IMAGE_MAX_BYTES = 1024 * 1024;

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

// The credential never appears in these shapes: no endpoint returns it.

export type PmsInterface = {
  id: string; connector_kind: string; display_label: string; lifecycle_state: string;
  current_revision_id?: string; current_revision_no?: number | null;
  revision_count: number; published: boolean;
  secret_generation?: number | null; secret_rotated_at?: string | null;
  endpoint?: string | null; source_timezone?: string | null; financial_base_currency?: string | null;
};

export type PmsRevision = {
  id: string; revision_no: number; source_timezone: string; posting_target_model: string;
  normalization_version: number; source_fingerprint?: string;
  financial_base_currency?: string | null;
  financial_base_currency_exponent?: number | null;
  config: Record<string, unknown>;
  published: boolean;
  authored_at?: string | null;
  published_at?: string | null;
  actor_id?: string;
  actor_label?: string;
  reason_code?: string;
};

export type PmsInterfaceHealth = {
  pms_interface_id: string;
  // Whether this interface can serve Room authentication now, decided server-side (including heartbeat_timeout_ms);
  // excludes per-guest occupancy evidence. room_auth_reason is a closed-set code, empty when ready.
  room_auth_ready?: boolean;
  room_auth_reason?: string;
  // While ready: the live feed, or the last complete guest list (the PMS is unreachable or a refresh is running).
  room_auth_source?: "LIVE_FEED" | "LAST_GOOD_ROSTER";
  transport_status: string; last_connected_at?: string | null; last_heartbeat_at?: string | null;
  disconnected_since?: string | null; transport_error_code?: string;
  continuity_status: string; last_valid_event_at?: string | null; discontinuity_detected_at?: string | null;
  sync_status: string; resync_requested_at?: string | null; resync_started_at?: string | null;
  last_complete_sync_at?: string | null; last_sync_failure_code?: string;
  in_house_stays: number; last_stay_event_at?: string | null;
  pending_events: number; review_events: number; oldest_pending_at?: string | null;
  // No total/percentage/remaining field: FIAS gives no total before sync ends. sync_records_received is a running
  // count.
  sync_stage?: string; sync_stage_at?: string | null;
  sync_records_received?: number; sync_records_skipped?: number;
  sync_failure_code?: string;
  // sync_stage=COMPLETE means published; materialization_ready means the applier finished writing Stay tables.
  materialization_ready?: boolean;
  resync_command_reason?: string; resync_command_requested_at?: string | null;
};

export type PmsGuestNetworkRoute = {
  guest_network_id: string; guest_network_name?: string;
  pms_interface_id: string; pms_interface_label?: string;
  is_default: boolean; routing_mode: string;
};

export type PmsSourceConflict = {
  detected?: boolean;
  id: string; interface_a: string; interface_a_label?: string;
  interface_b: string; interface_b_label?: string;
  severity?: string; resolution?: string;
};

// No provider reference, idempotency key or guest field exists in these shapes; the API never sends them.

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
  package_name?: string | null;
  at?: string | null;
  source?: "ROOM" | "VOUCHER" | "ACCOUNT" | "OPEN" | "" | null;
  pms_interface_label?: string | null;
  room?: string | null;
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
  settlement_report?: {
    settlement_currency: string; settlement_amount_minor: number; exchange_rate?: string | null;
    fee_minor?: number | null; net_minor?: number | null; provider_balance_ref?: string | null; converted: boolean;
  };
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

export const RECOVERY_RESOLUTIONS = [
  "CONFIRMED_COMPLETED",
  "CONFIRMED_NOT_COMPLETED",
  "ABANDONED",
  "ESCALATED",
] as const;
export type RecoveryResolution = (typeof RECOVERY_RESOLUTIONS)[number];

// Postings held by recovery that were never transmitted; not in Manual Review. eligible_for_retry_authorization is
// served, not computed.
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

// The action catalog and evidence contract are SERVED; a frontend copy would drift.

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
  not_sent_reason?: string | null;
};

export type ReviewPostingDetail = {
  posting: ReviewQueueRow;
  pinned_evidence: {
    settlement_id: string;
    purchase_id: string;
    stay_id: string | null;
    g_number: string | null;
    posting_interface_revision_id?: string;
    connector_kind: string;
    posting_target_model: string;
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

/** The server owns resolution_state; incomplete rosters and shared rooms cannot justify automatic reconciliation. */
export type ReconciliationState =
  | "RESOLVABLE"
  | "SUPERSEDED_ROOM_EMPTY"
  | "ROOM_SHARED"
  | "LATER_OCCUPANT"
  | "ROSTER_CONTRADICTS"
  | "NEEDS_PMS_EVIDENCE";

export type ReconciliationCase = {
  /** A case key is unique only within one PMS connection. */
  pms_interface_id: string;
  pms_interface_label?: string;
  case_key: string;
  reservation?: string;
  room?: string;
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

export type ReconcileOutcome =
  | "COMPLETED"
  | "REFUSED_ROSTER_INCOMPLETE"
  | "REFUSED_GENERATION_NOT_LATEST"
  | "REFUSED_GENERATION_UNPUBLISHED"
  | "REFUSED_LINK_NOT_HEALTHY"
  | "REFUSED_SCOPE_MISMATCH"
  | "REFUSED_ROSTER_TOO_SMALL"
  | "REFUSED_CAP_EXCEEDED";

export type ReconcileRun = {
  outcome: ReconcileOutcome;
  roster_size: number;
  mirror_in_house: number;
  absent_from_roster: number;
  stays_closed: number;
  rooms_enumerated: number;
  rooms_expected: number;
  protected_by_newer_events: number;
  run_id?: string;
};

export type IntegrationBlocker = {
  blocker: "LINK_DOWN" | "RECONCILIATION_BLOCKED" | "DEPARTURE_FOR_UNKNOWN_STAY";
  since?: string | null;
  detail: string;
  guests_affected: boolean;
};

export type PmsConnectionSettings = {
  backoff_min_ms: number;
  backoff_max_ms: number;
  stable_reset_seconds: number;
  link_down_alert_seconds: number;
  blocked_after_refusals: number;
  config_version: number;
  is_default: boolean;
};

export type RosterReconciliationState = {
  blockers: IntegrationBlocker[];
  /** Recovery bounds are per interface (GET /pms-interfaces/{id}/connection-settings), never a site-wide copy here. */
  settings: {
    roster_trust_min: number;
    inventory_tolerance: number;
    inventory_lookback: number;
    max_close_per_run: number;
    config_version: number;
    is_default: boolean;
  };
  generation: number;
  /** The last recorded run; opening the page runs nothing. Null when none has run. */
  last_run?: ReconcileRun | null;
  last_run_mode?: "DRY_RUN" | "APPLY" | "";
  last_run_at?: string | null;
  preview?: ReconcileRun;
  undisposed_cases: number;
  pms_interface_id: string;
  interfaces?: { id: string; label: string; connector_kind: string; lifecycle_state: string;
    published_generation: number; last_complete_sync_at?: string | null }[];
};

export type ReconcileRunRecord = ReconcileRun & {
  pms_interface_id?: string;
  pms_interface_label?: string;
  mode: "DRY_RUN" | "APPLY";
  resync_generation: number;
  run_at: string;
  run_by: string;
  reason: string;
};

export type MultiOccupancyRoom = {
  pms_interface_id: string;
  pms_interface_label?: string;
  room: string;
  stays_in_room: number;
  earliest_arrival?: string | null;
  latest_planned_departure?: string | null;
};

export type StayPastDeparture = {
  stay_id: string;
  pms_interface_id: string;
  pms_interface_label?: string;
  room?: string;
  reservation?: string;
  arrival?: string | null;
  departure?: string | null;
  days_past_departure: number;
  roster_present: boolean;
  room_now_holds_another_stay: boolean;
};
