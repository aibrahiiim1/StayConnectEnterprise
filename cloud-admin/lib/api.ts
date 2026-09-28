// API client — talks to /api/* which Next.js rewrites to ctrlapi (/api/v1/* and /api/cloud/* only).
// Cookies flow naturally same-origin; no credentials: 'include' needed.
//
// The shapes below are the contract in docs/CENTRAL_CONTROL_PLANE.md §6. The console never derives an appliance
// or license state itself: ctrlapi returns `activation`, `connection` and `license.state` on every row (§3).

export class ApiError extends Error {
  status: number;
  code: string;          // machine-readable, e.g. "reauth_required"
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

async function request<T>(method: string, path: string, body?: any): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
    cache: "no-store",
  });
  const contentType = res.headers.get("content-type") ?? "";
  const payload = contentType.includes("application/json") ? await res.json() : await res.text();
  if (!res.ok) throw new ApiError(res.status, payload);
  return payload as T;
}

export const api = {
  get:   <T>(path: string)             => request<T>("GET", path),
  post:  <T>(path: string, body?: any) => request<T>("POST", path, body),
  patch: <T>(path: string, body?: any) => request<T>("PATCH", path, body),
  del:   <T>(path: string, body?: any) => request<T>("DELETE", path, body),
};

/** A list endpoint answers `{items:[…]}` (§6). */
export type Items<T> = { items: T[] };

/** The rows of a list answer. Tolerates the pre-redesign `{data:[…]}` envelope so a half-deployed API still reads. */
export function itemsOf<T>(r: { items?: T[] | null; data?: T[] | null } | null | undefined): T[] {
  return r?.items ?? r?.data ?? [];
}

/** Builds `?a=1&b=2` from the non-empty entries. */
export function qs(params: Record<string, string | number | undefined | null>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
  const s = q.toString();
  return s ? `?${s}` : "";
}

// ------- Step-up re-authentication -------
//
// License and activation writes (every §6 route marked SU) need a recent password re-entry: without one the
// server replies 403 { error: "reauth_required" }. withStepUp() asks for the password in a designed dialog
// (components/step-up.tsx), re-authenticates and retries once.

export async function reauth(password: string): Promise<void> {
  await api.post("/v1/auth/reauth", { password });
}

type StepUpPrompter = (message: string) => Promise<string | null>;
let stepUpPrompter: StepUpPrompter | null = null;

/** Registers the password-confirmation dialog. Returns an unregister function. */
export function setStepUpPrompter(fn: StepUpPrompter | null): () => void {
  stepUpPrompter = fn;
  return () => {
    if (stepUpPrompter === fn) stepUpPrompter = null;
  };
}

/** Runs fn; on a server step-up demand, asks for the password, re-authenticates and retries once. */
export async function withStepUp<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (e: any) {
    if (!(e instanceof ApiError) || e.status !== 403 || e.code !== "reauth_required") throw e;
    const pw = stepUpPrompter
      ? await stepUpPrompter("This action requires confirming your password.")
      : null;
    if (!pw) throw e;
    await reauth(pw);
    return await fn();
  }
}

// ------- Types (§6) -------

export type Whoami = {
  operator_id: string;
  email: string;
  display_name?: string | null;
  roles: string[];
  is_super_admin: boolean;
  customer_id?: string | null;
  customer_name?: string | null;
  permissions?: string[];
};

export type Activation = "waiting" | "activating" | "activated" | "retiring" | "retired";
export type Connection = "connected" | "recently_seen" | "offline" | "never";
export type LicenseState = "none" | "active" | "expiring" | "grace" | "expired" | "suspended" | "revoked";

export type AttentionKind =
  | "waiting_activation" | "license_expiring" | "license_grace" | "license_expired" | "license_suspended"
  | "appliance_offline" | "security_alert" | "retirement_unconfirmed";

export type AttentionItem = {
  kind: AttentionKind;
  appliance_id?: string | null;
  serial?: string | null;
  customer_id?: string | null;
  customer_name?: string | null;
  site_name?: string | null;
  detail?: string | null;
  since?: string | null;
};

export type Overview = {
  customers: number;
  sites: number;
  appliances: Record<"total" | Activation | Connection, number>;
  licenses: Record<LicenseState, number>;
  attention: AttentionItem[];
};

export type Customer = {
  id: string;
  name: string;
  slug?: string;
  status: "active" | "archived" | string;
  created_at?: string;
  sites: number;
  appliances: number;
  activated: number;
  licenses_active: number;
  attention: number;
};

export type Site = {
  id: string;
  customer_id: string;
  code?: string;
  name: string;
  timezone: string;
  country?: string | null;
  /** Descriptive only (lib/site-types.ts); "UNSPECIFIED" when nobody classified the site. Grants nothing. */
  site_type?: string;
  status: "active" | "archived" | string;
  appliances: number;
};

export type ApplianceLicense = {
  id?: string | null;
  state: LicenseState;
  valid_until?: string | null;
  grace_ends_at?: string | null;
  max_concurrent_online_guests?: number | null;
  license_version?: number | null;
  /** Module ids the current license authorises; [] = core only; null with no license. */
  modules?: string[] | null;
};

export type ApplianceRow = {
  id: string;
  serial: string;
  hostname?: string | null;
  model?: string | null;
  version?: string | null;
  customer_id?: string | null;
  customer_name?: string | null;
  site_id?: string | null;
  site_name?: string | null;
  site_type?: string | null;
  activation: Activation;
  connection: Connection;
  last_seen_at?: string | null;
  last_public_ip?: string | null;
  license: ApplianceLicense;
  registered_at?: string | null;
  activated_at?: string | null;
  open_alerts?: number;
  /** The customer whose data the appliance reported still holding when it registered; it can be activated only
   *  for that customer. The name is null when that customer is no longer in Central. */
  holds_customer_id?: string | null;
  holds_customer_name?: string | null;
};

export type LicenseRow = {
  id: string;
  appliance_id: string;
  serial: string;
  customer_id?: string | null;
  customer_name?: string | null;
  site_id?: string | null;
  site_name?: string | null;
  state: LicenseState | "superseded" | string;
  status?: string;
  valid_from?: string | null;
  valid_until?: string | null;
  grace_period_days?: number | null;
  grace_ends_at?: string | null;
  max_concurrent_online_guests?: number | null;
  license_version?: number | null;
  issued_at?: string | null;
  /** Module ids the license authorises; [] = core only. */
  modules?: string[];
};

export type ActivityEvent = {
  ts?: string;
  at?: string;
  action: string;
  actor?: string | null;
  actor_email?: string | null;
  detail?: string | null;
  reason?: string | null;
  payload?: Record<string, unknown> | null;
};

export type ApplianceDetail = ApplianceRow & {
  identity?: {
    wan_mac?: string | null;
    lan_mac?: string | null;
    hardware_fingerprint?: string | null;
    identity_key_fingerprint?: string | null;
    cert_fingerprint?: string | null;
    cert_not_after?: string | null;
  } | null;
  assignment?: {
    version?: number | null;
    state?: string | null;
    signer_key_id?: string | null;
    issued_at?: string | null;
    acked_version?: number | null;
  } | null;
  licenses?: LicenseRow[];
  events?: ActivityEvent[];
  replacement?: { pending: boolean; deadline?: string | null; replaces?: string | null; replaced_by?: string | null } | null;
  retirement?: { state: string; deadline?: string | null } | null;
};

/** The license terms an operator chooses (Activate and Set license). Exactly one of valid_days / valid_until. */
export type LicenseTerms = {
  max_concurrent_online_guests: number;
  valid_days?: number;
  valid_until?: string;
  grace_period_days: number;
  /** Module ids (GET /cloud/v1/modules). [] = core only. */
  modules: string[];
};

export type AuditEntry = {
  id?: string;
  ts: string;
  customer_id?: string | null;
  customer_name?: string | null;
  appliance_id?: string | null;
  serial?: string | null;
  actor_type?: string;
  actor_id?: string | null;
  actor_email?: string | null;
  action: string;
  target_type?: string | null;
  target_id?: string | null;
  ip?: string | null;
  payload?: Record<string, unknown> | null;
};

export type AuditPage = { items?: AuditEntry[]; data?: AuditEntry[]; next_cursor?: string | null };

/** A Central sign-in: a platform operator (Team) or a customer's user. */
export type CentralUser = {
  id: string;
  email: string;
  display_name?: string | null;
  status: "active" | "disabled" | "invited" | string;
  roles: string[];
  created_at?: string;
  last_login_at?: string | null;
};
