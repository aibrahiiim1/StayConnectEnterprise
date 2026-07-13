// API client — talks to /api/* which Next.js rewrites to the Go backend.
// Cookies flow naturally same-origin; no credentials: 'include' needed.

export class ApiError extends Error {
  status: number;
  code: string;          // machine-readable, e.g. "limit_exceeded"
  traceId?: string;      // server request id for support tickets
  body: any;             // full envelope for limit_key/limit/current etc.
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

async function request<T>(
  method: string,
  path: string,
  body?: any,
  init?: RequestInit
): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
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
  del:   <T>(path: string, body?: any)    => request<T>("DELETE", path, body),
};

// ------- Step-up re-authentication -------
//
// Billing/identity-sensitive Platform actions (appliance assign/reassign/revoke,
// certificate issue/revoke, license issue/suspend/revoke) are gated server-side
// by RequireReauth: if the operator has not re-entered their password recently
// the server replies 403 { error: "reauth_required" }. Without handling that, the
// UI buttons simply fail — so every such action is wrapped in withStepUp().

export async function reauth(password: string): Promise<void> {
  await api.post("/v1/auth/reauth", { password });
}

/** Runs fn; on a server step-up demand, prompts for the password, re-authenticates and retries once. */
export async function withStepUp<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (e: any) {
    if (!(e instanceof ApiError) || e.status !== 403 || e.code !== "reauth_required") throw e;
    const pw = typeof window !== "undefined"
      ? window.prompt("This action requires confirming your password.")
      : null;
    if (!pw) throw e;
    await reauth(pw);
    return await fn();
  }
}

// ------- Types -------

export type ListResp<T> = { data: T[]; meta: { has_more: boolean; cursor?: string } };

export type Whoami = {
  operator_id: string;
  email: string;
  display_name?: string;
  is_super_admin: boolean;
  default_tenant_id?: string;
  roles: string[];
  expires_at: string;
};

export type Site = {
  id: string; tenant_id: string; code: string; name: string;
  timezone: string; country?: string; status?: string; created_at: string; updated_at: string;
};

export type Appliance = {
  id: string; tenant_id: string; site_id: string; serial: string; name: string;
  status: string; last_seen_at?: string;
  version?: string;
  identity_verified_at?: string;
};

// EffectiveConfig is what scd at this appliance should currently be
// enforcing — PMS providers after override resolution + walled-garden
// rules unioned across tenant + site scope.
export type EffectiveConfig = {
  appliance_id: string;
  tenant_id: string;
  site_id: string;
  pms_providers: PMSProvider[];
  walled_garden: WalledGardenRule[];
};

export type VoucherBatch = {
  id: string; tenant_id: string; template_id: string; name?: string | null;
  count: number; created_by?: string | null; created_at: string;
};

export type Voucher = {
  id: string; tenant_id: string; template_id: string; batch_id?: string | null;
  code: string; code_display: string; state: string; issued_at: string;
  activated_at?: string; expires_at?: string;
  bytes_used: number; seconds_used: number;
};

export type Session = {
  id: string; tenant_id: string; site_id: string; appliance_id: string;
  guest_id: string; voucher_id?: string | null;
  ip: string; mac: string;
  state: string; end_reason?: string | null;
  started_at: string; last_activity_at: string; ended_at?: string | null;
  bytes_up: number; bytes_down: number;
};

export type UsageSummary = {
  tz: string; period_start: string; period_end: string;
  bytes_up: number; bytes_down: number; total_bytes: number;
  active_sessions: number; sessions_today: number;
  cap_bytes?: number; cap_used_percent?: number;
};

export type TopRow = {
  id: string; name: string;
  bytes_up: number; bytes_down: number; total_bytes: number;
};

export type TopResp = { tz: string; from: string; to: string; rows: TopRow[] };

export type Subscription = {
  id: string; tenant_id: string; plan_id: string;
  plan_code: string; plan_name: string;
  status: string; billing_cycle: string;
  current_period_start: string; current_period_end: string;
  trial_end?: string | null;
};

export type TicketTemplate = {
  id: string; tenant_id: string; code: string; name: string;
  description?: string | null;
  duration_seconds?: number | null; data_cap_bytes?: number | null;
  down_kbps?: number | null; up_kbps?: number | null;
  max_concurrent_devices: number; is_active: boolean;
  price_cents?: number | null; currency?: string | null;
};

export type Plan = {
  id: string; code: string; name: string;
  description?: string | null;
  billing_cycle: "monthly" | "yearly";
  price_cents: number; currency: string;
  trial_days: number; is_public: boolean; is_active: boolean;
  sort_order: number;
  limits?: PlanLimit[];
};

export type PlanLimit = {
  key: string; value_type: "int" | "bool" | "string";
  int_value?: number; bool_value?: boolean; str_value?: string;
  unit?: string;
};

export type EffectiveLimit = PlanLimit & { source: "plan" | "override" };

export type Operator = {
  id: string;
  tenant_id?: string | null;
  email: string;
  display_name?: string;
  status: "active" | "disabled" | "invited";
  roles?: { id: string; role: string; tenant_id?: string | null }[];
  created_at: string;
  updated_at: string;
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
  site_id?: string; // empty/undefined → tenant-wide row
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

export type BootstrapToken = {
  id: string;
  tenant_id: string;
  site_id: string;
  expected_serial?: string;
  token_hint: string;
  created_by?: string;
  expires_at: string;
  consumed_at?: string;
  consumed_by_appliance?: string;
  created_at: string;
};
// On create the server returns the plaintext token exactly once, alongside
// the row. Store the plaintext only for the brief moment the UI shows it.
export type BootstrapTokenCreated = { token: string; row: BootstrapToken };

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

// ------- Cloud domain (/cloud/v1) — licensing + fleet -------

// License mirrors control-plane's licenses row. status is one of
// active/suspended/revoked/superseded (badge-colored in the UI).
export type License = {
  id: string;
  tenant_id: string;
  site_id: string;
  commercial_plan_code: string;
  status: string;
  issued_at: string;
  valid_until: string;
  offline_grace_days: number;
  appliance_ids?: string[] | null;
  features?: unknown;
  limits?: unknown;
  key_id: string;
  revoked_at?: string | null;
  created_at: string;
};

// FleetAppliance is the vendor/group health view of one appliance — no guest
// data, just registry status plus the latest fleet_telemetry health push.
export type FleetAppliance = {
  appliance_id: string;
  tenant_id: string;
  site_id?: string | null;
  name: string;
  serial: string;
  status: string;
  version?: string | null;
  last_seen_at?: string | null;
  license_status?: string | null;
  license_valid_until?: string | null;
  last_health?: unknown;
};

export type TelemetryRow = {
  ts: string;
  kind: string;
  seq: number;
  payload: unknown;
};
