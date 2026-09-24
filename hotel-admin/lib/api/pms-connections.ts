// PMS CONNECTIONS — the provider catalogue, the request shapes and the operator wording for the PMS
// connections screen.
//
// Everything that decides WHAT is sent to edged lives here rather than in the components, so the one thing the
// tests must be able to pin — that a Protel connection is created with exactly the requests it always was — is
// a pure function that can be read and asserted without rendering anything.
//
// THE CATALOGUE IS THE ONLY SOURCE OF OFFERED PROVIDERS. edged serves GET /pms-providers; a provider that is not
// in it is never offered. Where the catalogue cannot be read at all (an appliance whose edged predates it
// answers 404), exactly one provider is offered — Protel over FIAS, the connector every appliance has always
// carried — and the screen says why the list is short.

import { api, type PmsInterface, type PmsInterfaceHealth } from "@/lib/api";

// ---------------------------------------------------------------- catalogue types (GET /pms-providers)

export type PmsFieldType = "url" | "host_port" | "string" | "int" | "enum" | "bool";

export type PmsProviderField = {
  key: string;
  label: string;
  type: PmsFieldType;
  required?: boolean;
  default?: string | number | boolean | null;
  help?: string;
  placeholder?: string;
  options?: { value: string; label: string }[];
  min?: number;
  max?: number;
  unit?: string;
  /** Client-only: folded behind "Show timing settings". Never sent by edged. */
  advanced?: boolean;
};

export type PmsCredentialField = {
  key: string;
  label: string;
  secret?: boolean;
  required?: boolean;
  help?: string;
};

export type PmsProvider = {
  kind: string;
  label: string;
  vendor?: string;
  integration?: string;
  transport?: "REST_POLL" | "SOCKET" | string;
  verification?: "LIVE_PROVIDER_VERIFIED" | "AUTOMATED_CONTRACT_VERIFIED" | string;
  verification_note?: string;
  docs_url?: string;
  credential?: { mode: "NONE" | "AUTH_KEY" | string; fields?: PmsCredentialField[] };
  fields?: PmsProviderField[];
  capabilities?: {
    full_resync?: boolean;
    live_events?: boolean;
    test_connection?: boolean;
    arrivals?: boolean;
    departures?: boolean;
  };
  setup_steps?: string[];
};

/** A list/detail row. The three provider projections are optional: an older edged does not send them. */
export type PmsConnection = PmsInterface & {
  provider_label?: string;
  transport?: string;
  verification?: string;
};

export type TestConnectionResult = {
  ok: boolean;
  stage?: "CONFIG" | "AUTH" | "READ" | string;
  code?: string;
  message?: string;
  latency_ms?: number;
  details?: { sample_reservations?: number; property?: string };
};

export const PROTEL_KIND = "protel-fias";

// THE PROTEL REVISION FORM, as a schema. These keys are the top-level fields of edged's authorRevisionReq and
// are sent exactly as the form always sent them — Protel never uses provider_config. The defaults are the
// starting points the form has always offered; the server validates every one of them.
export const PROTEL_FIELDS: PmsProviderField[] = [
  {
    key: "endpoint", label: "PMS address and port", type: "host_port", required: true,
    placeholder: "pms.hotel.local:5010", help: "The host and port the appliance connects to.",
  },
  { key: "dial_timeout_ms", label: "Connect timeout", type: "int", required: true, default: 5000, min: 1, max: 86_400_000, unit: "ms", advanced: true },
  { key: "read_timeout_ms", label: "Read timeout", type: "int", required: true, default: 15000, min: 1, max: 86_400_000, unit: "ms", advanced: true },
  { key: "write_timeout_ms", label: "Write timeout", type: "int", required: true, default: 15000, min: 1, max: 86_400_000, unit: "ms", advanced: true },
  { key: "heartbeat_interval_ms", label: "Keep-alive every", type: "int", required: true, default: 30000, min: 1, max: 86_400_000, unit: "ms", advanced: true },
  {
    key: "heartbeat_timeout_ms", label: "Keep-alive timeout", type: "int", required: true, default: 90000, min: 1,
    max: 86_400_000, unit: "ms", advanced: true, help: "Must be longer than the keep-alive interval.",
  },
  { key: "feed_freshness_ms", label: "Guest data stale after", type: "int", required: true, default: 120000, min: 1, max: 86_400_000, unit: "ms", advanced: true },
  { key: "complete_sync_ms", label: "Full refresh at least every", type: "int", required: true, default: 600000, min: 1, max: 86_400_000, unit: "ms", advanced: true },
  {
    key: "financial_base_currency", label: "Currency for charges (optional)", type: "string", placeholder: "USD",
    help: "Three-letter code. Set it together with the decimal places, or leave both empty.",
  },
  {
    key: "financial_base_currency_exponent", label: "Currency decimal places (optional)", type: "int", min: 0, max: 4,
  },
];

/** The provider every appliance carries. Offered alone when the catalogue cannot be read. */
export const PROTEL_FALLBACK: PmsProvider = {
  kind: PROTEL_KIND,
  label: "Protel (FIAS)",
  vendor: "Protel",
  integration: "FIAS interface",
  transport: "SOCKET",
  verification: "LIVE_PROVIDER_VERIFIED",
  verification_note: "Connected to a live Protel system at a property.",
  credential: { mode: "NONE", fields: [] },
  fields: PROTEL_FIELDS,
  capabilities: { full_resync: true, live_events: true, test_connection: false, arrivals: true, departures: true },
  setup_steps: [
    "Ask the property's Protel administrator to enable a FIAS interface for StayConnect.",
    "Note the address and port the FIAS interface listens on.",
    "Make sure the appliance can reach that address on the hotel network.",
  ],
};

export type ProviderCatalogue = { providers: PmsProvider[]; available: boolean };

/**
 * Loads the provider catalogue. Any failure — the route not existing on this appliance, or a malformed answer —
 * produces the Protel-only list with `available: false`, never an error screen: the page must still let an
 * operator manage the Protel connection they already have.
 */
export async function loadProviderCatalogue(): Promise<ProviderCatalogue> {
  try {
    const r = await api.get<{ providers?: PmsProvider[] }>("/pms-providers");
    const list = Array.isArray(r?.providers) ? r.providers.filter((p) => p && typeof p.kind === "string") : null;
    if (!list || list.length === 0) return { providers: [PROTEL_FALLBACK], available: false };
    return { providers: list, available: true };
  } catch {
    return { providers: [PROTEL_FALLBACK], available: false };
  }
}

/** The provider for a connection: from the catalogue when it lists it, Protel's built-in entry for Protel. */
export function providerFor(kind: string, catalogue: PmsProvider[]): PmsProvider | null {
  const hit = catalogue.find((p) => p.kind === kind);
  if (hit) return hit;
  if (kind === PROTEL_KIND) return PROTEL_FALLBACK;
  return null;
}

/** The field schema the form renders. Protel always uses its own top-level fields. */
export function fieldsFor(p: PmsProvider): PmsProviderField[] {
  if (p.kind === PROTEL_KIND) return PROTEL_FIELDS;
  // The time zone is asked once, for every provider, as a revision column — never twice.
  return (p.fields ?? []).filter((f) => f.key !== "source_timezone");
}

export const needsCredential = (p: PmsProvider | null | undefined) =>
  (p?.credential?.mode ?? "NONE") === "AUTH_KEY" && (p?.credential?.fields?.length ?? 0) > 0;

/**
 * Whether "test connection" may be offered. Only when the catalogue says the provider supports it, and never
 * for a socket link: testing one would open a second session to the property's PMS beside the live one.
 */
export const canTestConnection = (p: PmsProvider | null | undefined) =>
  !!p?.capabilities?.test_connection && p?.transport !== "SOCKET";

export const canFullResync = (p: PmsProvider | null | undefined) => p?.capabilities?.full_resync !== false;

// ---------------------------------------------------------------- form values and validation

export type FormValues = Record<string, string | boolean>;

export function initialValues(fields: PmsProviderField[], from?: Record<string, unknown> | null): FormValues {
  const out: FormValues = {};
  for (const f of fields) {
    const v = from?.[f.key];
    if (f.type === "bool") {
      out[f.key] = typeof v === "boolean" ? v : typeof f.default === "boolean" ? f.default : false;
    } else if (v !== undefined && v !== null && v !== "" && typeof v !== "object") {
      out[f.key] = String(v);
    } else if (f.default !== undefined && f.default !== null) {
      out[f.key] = String(f.default);
    } else {
      out[f.key] = "";
    }
  }
  return out;
}

const HOST_PORT = /^(\[[0-9a-fA-F:.]+\]|[^\s:/\[\]]+):(\d{1,5})$/;

/** Client-side shape check. The server is authoritative; this only catches what is certainly wrong. */
export function validateField(f: PmsProviderField, raw: string | boolean | undefined): string | null {
  if (f.type === "bool") return null;
  const v = typeof raw === "string" ? raw.trim() : "";
  if (v === "") return f.required ? "Required." : null;
  switch (f.type) {
    case "url": {
      try {
        const u = new URL(v);
        if (u.protocol !== "https:" && u.protocol !== "http:") return "Enter a web address starting with https://.";
        return null;
      } catch {
        return "Enter a full web address, for example https://api.example.com.";
      }
    }
    case "host_port": {
      const m = HOST_PORT.exec(v);
      if (!m) return "Enter a host and port, for example pms.hotel.local:5010.";
      const port = Number(m[2]);
      if (port < 1 || port > 65535) return "The port must be between 1 and 65535.";
      return null;
    }
    case "int": {
      if (!/^-?\d+$/.test(v)) return "Enter a whole number.";
      const n = Number(v);
      if (f.min !== undefined && n < f.min) return `Must be at least ${f.min.toLocaleString()}.`;
      if (f.max !== undefined && n > f.max) return `Must be at most ${f.max.toLocaleString()}.`;
      return null;
    }
    case "enum":
      return f.options && !f.options.some((o) => o.value === v) ? "Choose one of the options." : null;
    default:
      return null;
  }
}

export function validateAll(fields: PmsProviderField[], values: FormValues): Record<string, string> {
  const errs: Record<string, string> = {};
  for (const f of fields) {
    const e = validateField(f, values[f.key]);
    if (e) errs[f.key] = e;
  }
  // Protel's two cross-field rules, which the server also enforces. Stated here so the operator reads them
  // beside the field rather than as a refusal after pressing Create.
  if (fields === PROTEL_FIELDS) {
    const hi = Number(values.heartbeat_interval_ms);
    const ht = Number(values.heartbeat_timeout_ms);
    if (!errs.heartbeat_timeout_ms && Number.isFinite(hi) && Number.isFinite(ht) && ht <= hi) {
      errs.heartbeat_timeout_ms = "Must be longer than the keep-alive interval.";
    }
    const cur = String(values.financial_base_currency ?? "").trim();
    const exp = String(values.financial_base_currency_exponent ?? "").trim();
    if ((cur === "") !== (exp === "")) {
      errs[cur === "" ? "financial_base_currency" : "financial_base_currency_exponent"] =
        "Set the currency and its decimal places together, or leave both empty.";
    } else if (cur !== "" && !/^[A-Za-z]{3}$/.test(cur)) {
      errs.financial_base_currency = "Use a three-letter currency code, for example USD.";
    }
  }
  return errs;
}

function typed(f: PmsProviderField, raw: string | boolean | undefined): unknown {
  if (f.type === "bool") return raw === true;
  const v = typeof raw === "string" ? raw.trim() : "";
  if (v === "") return undefined;
  if (f.type === "int") return Number(v);
  return v;
}

// ---------------------------------------------------------------- request bodies

/**
 * The Protel revision body — byte-for-byte what the configuration form has always posted: every timing field as
 * a number, the currency pair only when set, read_only fixed true, and nothing else.
 */
export function protelRevisionBody(values: FormValues, sourceTimezone: string): Record<string, unknown> {
  const num = (k: string) => Number(values[k]);
  const cur = String(values.financial_base_currency ?? "").trim().toUpperCase();
  const exp = String(values.financial_base_currency_exponent ?? "").trim();
  return {
    endpoint: String(values.endpoint ?? "").trim(),
    source_timezone: sourceTimezone.trim(),
    dial_timeout_ms: num("dial_timeout_ms"),
    read_timeout_ms: num("read_timeout_ms"),
    write_timeout_ms: num("write_timeout_ms"),
    heartbeat_interval_ms: num("heartbeat_interval_ms"),
    heartbeat_timeout_ms: num("heartbeat_timeout_ms"),
    feed_freshness_ms: num("feed_freshness_ms"),
    complete_sync_ms: num("complete_sync_ms"),
    financial_base_currency: cur || undefined,
    financial_base_currency_exponent: exp === "" ? undefined : Number(exp),
    read_only: true,
  };
}

/** A catalogue provider's revision body: the time zone as a column, everything provider-specific in provider_config. */
export function providerRevisionBody(
  p: PmsProvider, values: FormValues, sourceTimezone: string,
): Record<string, unknown> {
  if (p.kind === PROTEL_KIND) return protelRevisionBody(values, sourceTimezone);
  const provider_config: Record<string, unknown> = {};
  for (const f of fieldsFor(p)) {
    const v = typed(f, values[f.key]);
    if (v !== undefined) provider_config[f.key] = v;
  }
  return { source_timezone: sourceTimezone.trim(), read_only: true, provider_config };
}

/** The credential object for POST /secret: keyed by the provider's credential field keys, blanks omitted. */
export function credentialSecret(p: PmsProvider, values: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of p.credential?.fields ?? []) {
    const v = (values[f.key] ?? "").trim();
    if (v !== "") out[f.key] = v;
  }
  return out;
}

/** The values a revision already carries, to start an edit from what is live rather than from defaults. */
export function valuesFromRevision(
  p: PmsProvider, rev: { config?: Record<string, unknown>; source_timezone?: string } | null | undefined,
): { values: FormValues; timezone: string } {
  const fields = fieldsFor(p);
  const cfg = (rev?.config ?? {}) as Record<string, unknown>;
  const nested = (cfg.provider_config && typeof cfg.provider_config === "object"
    ? cfg.provider_config : {}) as Record<string, unknown>;
  const src: Record<string, unknown> = { ...cfg, ...nested };
  // A redacted value is not a value: starting an edit from "[redacted]" would save the placeholder.
  for (const k of Object.keys(src)) if (src[k] === "[redacted]") delete src[k];
  return { values: initialValues(fields, src), timezone: rev?.source_timezone || "Africa/Cairo" };
}

// ---------------------------------------------------------------- operator wording

export const LIFECYCLE_WORDS: Record<string, string> = {
  ACTIVE: "Active",
  AUTH_DISABLED: "Room sign-in paused",
  DRAINING: "Winding down",
  DECOMMISSIONED: "Retired",
};
export const TRANSPORT_WORDS: Record<string, string> = {
  CONNECTED: "Connected",
  DISCONNECTED: "Not connected",
  UNKNOWN: "Never connected",
};
export const CONTINUITY_WORDS: Record<string, string> = {
  CONTINUOUS: "Receiving updates",
  GAP_DETECTED: "Updates were missed",
  UNKNOWN: "No updates yet",
};
export const SYNC_WORDS: Record<string, string> = {
  IN_SYNC: "Up to date",
  RESYNC_REQUIRED: "Needs a full refresh",
  RESYNC_IN_PROGRESS: "Refreshing now",
  RESYNCING: "Refreshing now",
  OUT_OF_SYNC: "Out of date",
  UNKNOWN: "Not yet loaded",
};
export const TRANSPORT_KIND_WORDS: Record<string, string> = {
  REST_POLL: "Web API, checked regularly",
  SOCKET: "Direct link to the PMS",
};

/** The bounded room-sign-in reasons, as what an operator does about them. */
export const ROOM_AUTH_WORDS: Record<string, string> = {
  INTERFACE_NOT_ACTIVE: "This connection is not active — activate it to serve room sign-in.",
  NO_PUBLISHED_REVISION: "No configuration has been put live for this connection.",
  CONTINUITY_GAP: "Updates from the PMS were missed, so the guest list cannot be trusted until a full refresh.",
  CONTINUITY_NOT_ESTABLISHED: "No updates have been received yet, so there is nothing to verify guests against.",
  NOT_IN_SYNC: "The guest list is not up to date. A full refresh will fix it.",
  FEED_SILENT: "The PMS has gone quiet for longer than this connection allows.",
  REVISION_NOT_PINNED: "The live configuration changed and the connection has not picked it up yet.",
  MIRROR_NEVER_SYNCHRONIZED: "No complete guest list has ever been loaded, so there is nothing to fall back on.",
  RESYNC_IN_FLIGHT: "A full refresh is partway through. Room sign-in resumes when it finishes.",
  MATERIALIZATION_BEHIND: "The guest list has arrived and is being applied — usually a few seconds.",
};

/** A value's words, or a neutral phrase — an unrecognised code is never printed raw. */
export const wordsFor = (map: Record<string, string>, v?: string | null, unknown = "Not reported") =>
  (v ? map[v] ?? unknown : "—");

export function verificationWords(v?: string | null): { label: string; tone: "ok" | "warn" | "default" } {
  if (v === "LIVE_PROVIDER_VERIFIED") return { label: "Live-verified", tone: "ok" };
  if (v === "AUTOMATED_CONTRACT_VERIFIED") {
    return { label: "Automated contract tests only — not yet live-verified", tone: "warn" };
  }
  return { label: "Verification not stated", tone: "default" };
}

export const PUBLISH_REASONS = [
  { value: "INITIAL_COMMISSIONING", label: "First configuration of this connection" },
  { value: "CONFIG_CORRECTION", label: "Correcting the settings" },
  { value: "ENDPOINT_CHANGE", label: "The PMS address changed" },
  { value: "TIMEOUT_TUNING", label: "Adjusting timeouts" },
  { value: "CONFIG_ROLLBACK", label: "Returning to an earlier configuration" },
];

export const LIFECYCLE_REASONS = [
  { value: "COMMISSIONING", label: "Commissioning the connection" },
  { value: "PMS_MAINTENANCE", label: "PMS maintenance" },
  { value: "TROUBLESHOOTING", label: "Troubleshooting" },
  { value: "PREPARING_REPLACEMENT", label: "Preparing to replace this connection" },
  { value: "OPERATOR_REQUEST", label: "Requested by hotel management" },
];

export const SECRET_REASONS = [
  { value: "INITIAL_COMMISSIONING", label: "First credential for this connection" },
  { value: "SCHEDULED_ROTATION", label: "Routine credential change" },
  { value: "PROVIDER_REISSUED", label: "The PMS provider issued a new credential" },
  { value: "SUSPECTED_EXPOSURE", label: "The credential may have been exposed" },
];

/** Where a saved version came from, for the reason codes the publish path records. */
export const REASON_WORDS: Record<string, string> = {
  INITIAL_COMMISSIONING: "First set up when the connection was commissioned",
  DERIVED_SOURCE_FINGERPRINT: "Saved automatically once the PMS message format had been observed",
  CONFIG_CORRECTION: "A correction to the saved settings",
  ENDPOINT_CHANGE: "The PMS address was changed",
  TIMEOUT_TUNING: "Timeouts were adjusted",
  CONFIG_ROLLBACK: "Returned to an earlier configuration",
};

// ---------------------------------------------------------------- health helpers (display only)

/** The most recent moment the PMS was heard from, whichever signal carried it. */
export function lastCommunication(h?: PmsInterfaceHealth | null): string | null {
  if (!h) return null;
  const cands = [h.last_heartbeat_at, h.last_valid_event_at, h.last_stay_event_at, h.last_connected_at]
    .filter((x): x is string => typeof x === "string" && x !== "");
  if (cands.length === 0) return null;
  return cands.reduce((a, b) => (new Date(b).getTime() > new Date(a).getTime() ? b : a));
}

const ACTIVE_STAGES = new Set(["REQUESTING_FULL_SYNC", "WAITING_FOR_PMS", "RECEIVING", "PUBLISHING", "APPLYING"]);
export function isSyncing(h?: PmsInterfaceHealth | null): boolean {
  if (!h) return false;
  const stage = h.sync_stage === "COMPLETE" && h.materialization_ready === false ? "APPLYING" : h.sync_stage ?? "";
  return ACTIVE_STAGES.has(stage);
}

/**
 * Where a connection points, with nothing that could be a credential: the host (and port) only. A URL's user
 * info, path and query are dropped — an API key pasted into a query string must never reach a card.
 */
export function safeHost(endpoint?: string | null): string | null {
  const v = (endpoint ?? "").trim();
  if (!v) return null;
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) {
    try { return new URL(v).host || null; } catch { return null; }
  }
  const m = HOST_PORT.exec(v);
  if (m) return v;
  return /^[\w.-]+$/.test(v) ? v : null;
}

/** "Never configured": switched off and never given a live configuration. Kept apart from real connections. */
export const isNeverConfigured = (i: PmsInterface) => i.lifecycle_state === "AUTH_DISABLED" && !i.published;

// ---------------------------------------------------------------- errors

type ErrLike = { status?: number; code?: string; message?: string; traceId?: string; body?: any };

/**
 * An edged refusal in words an operator can act on. The server's error code decides the sentence; where the
 * server's own message is already written for people (a resync refusal) it is kept.
 */
export function pmsErrorText(e: unknown): string | null {
  if (e == null || e === "") return null;
  if (typeof e === "string") return e;
  const x = e as ErrLike;
  const code = x.code ?? x.body?.error ?? "";
  const msg = String(x.message ?? x.body?.message ?? "");
  const ref = x.traceId ? ` (reference ${x.traceId})` : "";
  const say = (s: string) => s + ref;

  switch (code) {
    case "reauth_required":
      return say("Your password was not accepted. Enter your own admin password to confirm this change.");
    case "reason_required":
      return say("Choose a reason — it is recorded with the change.");
    case "not_found":
      return say("This connection no longer exists. Refresh the page.");
    case "forbidden":
      return say("Your role can view PMS connections but not change them.");
    case "revision_conflict":
      return say(
        "Another operator published a different revision while this form was open. Reload to see what is live " +
          "now, then decide again.",
      );
    case "revision_invalid":
      return say("That configuration version does not belong to this connection. Reload and try again.");
    case "secret_required":
      return say("Enter the credential.");
    case "encryption_unavailable":
      return say(
        "Credentials cannot be stored on this appliance because credential encryption is not set up. Nothing was saved.",
      );
    case "resync_not_possible":
      return say(`A full refresh cannot be requested right now: ${msg || "the connection is not ready"}.`);
    case "settings_rejected":
      return say(`The recovery settings were not accepted: ${msg}`);
    case "conflict":
      if (/decommissioned/i.test(msg)) return say("This connection is retired and can no longer be changed.");
      if (/cannot move/i.test(msg)) return say("That change is not possible from the connection's current state.");
      if (/another revision/i.test(msg)) {
        return say("Someone else saved a configuration for this connection at the same moment. Reload and try again.");
      }
      return say(msg || "The change conflicts with the connection's current state. Reload and try again.");
    case "validation":
      if (/publish a revision before activating/i.test(msg)) {
        return say("Publish a configuration before activating — without one there is nothing to connect to.");
      }
      if (/endpoint must be host:port/i.test(msg)) return say("The PMS address must be a host and port, for example pms.hotel.local:5010.");
      if (/port must be/i.test(msg)) return say("The PMS port must be a number between 1 and 65535.");
      if (/source_timezone.*not a known/i.test(msg)) return say("The PMS time zone is not a recognised time zone name, for example Europe/Berlin.");
      if (/source_timezone is required/i.test(msg)) return say("Enter the PMS time zone.");
      if (/heartbeat_timeout_ms must be greater/i.test(msg)) return say("The keep-alive timeout must be longer than the keep-alive interval.");
      if (/connector_kind/i.test(msg)) return say("This appliance does not support that property management system.");
      if (/display_label/i.test(msg)) return say("Enter a name of up to 120 characters.");
      if (/financial_base_currency/i.test(msg)) return say("Set the currency and its decimal places together, as a three-letter code and 0 to 4.");
      return say(`The settings were not accepted: ${msg}`);
    case "bad_request":
      return say("The request could not be read by the appliance. Reload the page and try again.");
    case "internal":
      return say("The appliance could not complete this just now. Nothing was changed — try again in a moment.");
  }
  if (x.status === 404) return say("This action is not available on this appliance.");
  return msg ? say(msg) : "Something went wrong.";
}
