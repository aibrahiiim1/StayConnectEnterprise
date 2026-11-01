// Central supplies appliance and license states; the console displays them without deriving new states.

export class ApiError extends Error {
  status: number;
  code: string;
  traceId?: string;
  body: any;
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

// A 401 on a sign-in or reauthentication route belongs to its form, not the expired-session handler.
type UnauthorizedHandler = (err: ApiError) => void;
let unauthorizedHandler: UnauthorizedHandler | null = null;

const NOT_AN_ENDED_SIGNIN = ["/v1/auth/login", "/v1/auth/reauth", "/v1/auth/logout"];

export function setUnauthorizedHandler(fn: UnauthorizedHandler | null): () => void {
  unauthorizedHandler = fn;
  return () => {
    if (unauthorizedHandler === fn) unauthorizedHandler = null;
  };
}

export function isEndedSignIn(status: number, path: string): boolean {
  if (status !== 401) return false;
  const bare = path.split("?")[0];
  return !NOT_AN_ENDED_SIGNIN.some((p) => bare === p || bare.startsWith(p + "/"));
}

// Timer requests must carry this header so an unattended screen cannot extend an idle session.
export const BACKGROUND_HEADER = "X-OneGate-Background";
export type RequestOptions = { background?: boolean };
export const BACKGROUND: RequestOptions = Object.freeze({ background: true });

async function request<T>(method: string, path: string, body?: any, opts?: RequestOptions): Promise<T> {
  const headers: Record<string, string> = {};
  if (body) headers["Content-Type"] = "application/json";
  if (opts?.background) headers[BACKGROUND_HEADER] = "1";
  const res = await fetch(`/api${path}`, {
    method,
    headers: Object.keys(headers).length ? headers : undefined,
    body: body ? JSON.stringify(body) : undefined,
    cache: "no-store",
  });
  const contentType = res.headers.get("content-type") ?? "";
  const payload = contentType.includes("application/json") ? await res.json() : await res.text();
  if (!res.ok) {
    const err = new ApiError(res.status, payload);
    if (unauthorizedHandler && isEndedSignIn(res.status, path)) unauthorizedHandler(err);
    throw err;
  }
  return payload as T;
}

export const api = {
  get:   <T>(path: string, opts?: RequestOptions) => request<T>("GET", path, undefined, opts),
  post:  <T>(path: string, body?: any) => request<T>("POST", path, body),
  patch: <T>(path: string, body?: any) => request<T>("PATCH", path, body),
  put:   <T>(path: string, body?: any) => request<T>("PUT", path, body),
  del:   <T>(path: string, body?: any) => request<T>("DELETE", path, body),
};

export type Items<T> = { items: T[] };

/** Accept either envelope during staggered API deployments. */
export function itemsOf<T>(r: { items?: T[] | null; data?: T[] | null } | null | undefined): T[] {
  return r?.items ?? r?.data ?? [];
}

export function qs(params: Record<string, string | number | undefined | null>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
  const s = q.toString();
  return s ? `?${s}` : "";
}

// The server requires recent password confirmation for sensitive writes; retry once after reauthentication.

export async function reauth(password: string): Promise<void> {
  await api.post("/v1/auth/reauth", { password });
}

type StepUpPrompter = (message: string) => Promise<string | null>;
let stepUpPrompter: StepUpPrompter | null = null;

export function setStepUpPrompter(fn: StepUpPrompter | null): () => void {
  stepUpPrompter = fn;
  return () => {
    if (stepUpPrompter === fn) stepUpPrompter = null;
  };
}

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

export type Whoami = {
  operator_id: string;
  email: string;
  display_name?: string | null;
  roles: string[];
  is_super_admin: boolean;
  customer_id?: string | null;
  customer_name?: string | null;
  permissions?: string[];
  /** Optional for older Central versions. */
  idle_expires_at?: string | null;
  absolute_expires_at?: string | null;
  /** Corrects browser clock drift when displaying session deadlines. */
  server_time?: string | null;
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
  /** Site type is descriptive and grants no capabilities. */
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
  /** Days; zero means no grace. */
  grace_period_days?: number | null;
  license_version?: number | null;
  /** Empty means core only; null means no license. */
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
  /** Activation is restricted to the customer whose data remains on the appliance. */
  holds_customer_id?: string | null;
  holds_customer_name?: string | null;
  offline_request_pending?: boolean;
  activating_on?: "certificate_reissue" | "certificate" | "license" | string;
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
  /** Empty means core only. */
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

/** Exactly one of valid_days and valid_until is supplied. */
export type LicenseTerms = {
  max_concurrent_online_guests: number;
  valid_days?: number;
  valid_until?: string;
  grace_period_days: number;
  /** Empty means core only. */
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

export type PortalTLSSettings = {
  acme_directory: "staging" | "production";
  contact_email: string;
  zone_name: string;
  zone_id: string;
  account_url: string;
  account_directory: string;
  weekly_new_limit: number;
  caa_applied_at?: string | null;
  updated_at?: string;
  updated_by?: string;
  directory_url: string;
  /** The token itself is never returned. */
  token_set: boolean;
  dns_provider: string;
  available: boolean;
  unavailable_reason?: string;
  test_overrides: string[];
  /** Appliance hostnames under the name base; while any exist the base is fixed. */
  live_hostnames: number;
};

export type PortalTLSTestResult = {
  zone_id: string;
  zone_name: string;
  nameservers: string[];
  account_url: string;
  directory: string;
};

export type PortalCert = {
  hostname: string;
  kind: "ONEGATE" | "CUSTOM";
  order_id: string;
  status: "QUEUED" | "PROCESSING" | "ISSUED" | "FAILED" | "REVOKED" | "SUPERSEDED";
  serial?: string | null;
  not_after?: string | null;
  renew_after?: string | null;
  revoked: boolean;
  acme_directory_url?: string | null;
  acme_directory?: string;
  error?: string | null;
};

export type PortalFleetRow = {
  appliance_id: string;
  serial: string;
  customer_name?: string | null;
  site_name?: string | null;
  hostname: string;
  portal_ip?: string | null;
  dns_state: "PENDING" | "SYNCED" | "ERROR" | "RELEASED";
  dns_error?: string | null;
  released: boolean;
  custom_domain?: string | null;
  custom_domain_status?: "PENDING" | "VERIFIED" | "FAILED" | null;
  custom_domain_error?: string | null;
  certificates: PortalCert[];
  last_error?: string | null;
};

export type PortalTLSEvent = {
  id: number;
  at: string;
  appliance_id?: string | null;
  hostname?: string | null;
  kind: string;
  actor?: string | null;
  detail?: Record<string, unknown> | null;
};

export type CentralUser = {
  id: string;
  email: string;
  display_name?: string | null;
  status: "active" | "disabled" | "invited" | string;
  roles: string[];
  created_at?: string;
  last_login_at?: string | null;
  /** Whether the account turned two-step sign-in on; without it the password alone signs in (D68). */
  two_step_enrolled?: boolean;
};
