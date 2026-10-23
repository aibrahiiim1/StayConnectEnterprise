// Shapes edged answers on the Payment methods and Room charge screens, and plain words for its codes.
// The codes are the contract; a code this file does not know is shown VERBATIM so a new server reason still reaches the
// operator.

import { ApiError } from "./api";
import { errMsg } from "./utils";

export type SiteModule = {
  id: string;
  label: string;
  requires?: string[];
  deployed: boolean;
  authorized: boolean;
  licensed: boolean;
  switchable: boolean;
  enabled: boolean;
  ready: boolean;
  effective: boolean;
  manageable: boolean;
  reasons: string[];
  readiness?: string[];
};

export type SiteModules = {
  site_type: string | null;
  license_state?: unknown;
  modules: Record<string, SiteModule>;
  deployment?: unknown;
};

export type CredentialKey = { key: string; label: string; secret: boolean; required: boolean };

export type PaymentProvider = {
  id: "stripe" | "paymob" | string;
  label: string;
  regions?: string[];
  credential_keys: CredentialKey[];
  hosted_domains: string[];
  live_allowed: boolean;
};

export type ProvidersResp = { providers: PaymentProvider[]; engine_unready?: string };

export type PaymentMode = "TEST" | "LIVE";

export type PaymentAccount = {
  id: string;
  provider: string;
  merchant_account_ref: string;
  display_name: string;
  currency: string;
  mode: PaymentMode | string;
  status: string;
  is_default: boolean;
  updated_at?: string;
  has_credentials: boolean;
  provider_converts?: boolean;
  conversion_supported?: boolean;
  credential_keys_set?: string[];
};

export type AccountsResp = {
  accounts: PaymentAccount[];
  ready: boolean;
  readiness: string[];
  extra_domains: string[];
};

export type AccountSaveBody = {
  id?: string;
  provider: string;
  merchant_account_ref: string;
  display_name: string;
  currency: string;
  mode: string;
  status: string;
  is_default: boolean;
  credentials: Record<string, string>;
  reason: string;
  password: string;
};

export type Bound = { min: number; max: number; default: number };

export type CardSettings = {
  settings: {
    checkout_expiry_minutes: number;
    reconcile_grace_minutes: number;
    config_version: number;
    updated_at?: string;
    is_default: boolean;
  };
  bounds: { checkout_expiry_minutes: Bound; reconcile_grace_minutes: Bound };
};

export type PaymentChange = {
  kind: "account" | "domains" | "settings" | string;
  changed_at: string;
  changed_by: string;
  reason: string;
  detail: unknown;
};

export type TestResult = { ok: boolean; error?: string; message?: string };

export type PostingTargetModel = "UNSET" | "RESERVATION";

export type AnswerEffect = "NO_POST_BLOCK" | "DATA_SUSPECT_BLOCK" | "NO_STAY_BLOCK";

export type AnswerMeaning = {
  as_status: string;
  effect: AnswerEffect | string;
  confirmed: boolean;
  evidence?: string | null;
  recorded_at?: string | null;
  recorded_by?: string | null;
};

export type OnboardingInterface = {
  pms_interface_id: string;
  display_label: string;
  connector_kind: string;
  lifecycle_state: string;
  current_revision_id: string;
  posting_target_model: PostingTargetModel | string | null;
  financial_base_currency: string | null;
  financial_base_currency_exponent: number | null;
  ready: boolean;
  reason: string | null;
  approved_at?: string | null;
  approved_by?: string | null;
  attestation?: string | null;
  answer_meanings?: AnswerMeaning[];
  financial_mirror?: { max_age_seconds: number; is_default: boolean; last_complete_sync_at: string | null };
};

export type OnboardingResp = { interfaces: OnboardingInterface[]; posting_target_models: string[] };

export const RESERVATION_TARGET = {
  label: "Reservation (room + reservation number)",
  explanation:
    "The charge goes to the guest's reservation: every posting carries the room number and the PMS reservation " +
    "number together. Protel decides which folio or window of that reservation receives it. A posting with the " +
    "room number alone is never sent.",
};

export function postingTargetLabel(s: string | null | undefined): string {
  if (s === "RESERVATION") return RESERVATION_TARGET.label;
  if (!s || s === "UNSET") return "Not recorded";
  return s;
}

export const ANSWER_NAMES: Record<string, string> = {
  NP: "No-post restriction",
  NG: "Guest not found",
  NR: "Room not found",
  NA: "Night audit in progress",
  RY: "Try again later",
};

export const ANSWER_EFFECT_WORDS: Record<string, string> = {
  NO_POST_BLOCK:
    "The purchase fails and room charge is blocked for the stay until Protel data allows posting again.",
  DATA_SUSPECT_BLOCK:
    "The purchase fails, the stay's data is marked suspect and a resync is requested. Room charge is blocked for " +
    "the stay until fresh Protel data arrives.",
  NO_STAY_BLOCK:
    "The purchase fails. The stay is not blocked, and the charge is never retried automatically.",
};

export const POSTING_BLOCK_WORDS: Record<string, string> = {
  NOT_IN_HOUSE: "The guest is not in house",
  NO_RESERVATION: "The stay has no reservation number from the PMS",
  PMS_NO_POST: "Protel refused a charge: the reservation has a no-post restriction",
  PMS_DATA_SUSPECT: "Protel did not recognise the guest or room, so the stay's data is being refreshed",
  POSTING_UNRESOLVED: "An earlier room charge has an unknown outcome and is waiting for manual review",
  ADMIN_BLOCK: "Blocked by an administrator",
};

export const POSTING_BLOCK_CLEARED_BY: Record<string, string> = {
  NOT_IN_HOUSE: "Clears when the PMS reports the guest in house.",
  NO_RESERVATION: "Clears when the PMS sends the reservation number for this stay.",
  PMS_NO_POST: "Clears only when fresh Protel data allows posting again. It cannot be removed here.",
  PMS_DATA_SUSPECT:
    "Clears only when fresh Protel data confirms the same reservation in house with a known room. It cannot be removed here.",
  POSTING_UNRESOLVED:
    "Clears only when the charge is decided in Manual review. It cannot be removed here.",
  ADMIN_BLOCK: "A site administrator can remove it.",
};

export const POSTING_SOURCE_WORDS: Record<string, string> = {
  PMS_FEED: "PMS guest data",
  PMS_ANSWER: "Protel's answer to a charge",
  POSTING_LEDGER: "A room charge with an unknown outcome",
  OPERATOR: "An administrator",
};

export function postingBlockWords(code: string | null | undefined): string {
  if (!code) return "Not allowed";
  return POSTING_BLOCK_WORDS[code] ?? code.replace(/_/g, " ").toLowerCase();
}

export function postingSourceWords(code: string | null | undefined): string {
  if (!code) return "—";
  return POSTING_SOURCE_WORDS[code] ?? code;
}

const CODE_WORDS: Record<string, string> = {
  CARD_NOT_DEPLOYED: "Card payment is not installed on this appliance",
  PAYMENT_KEY_MISSING: "This appliance has no payment key",
  PAYMENT_CREDENTIALS_MISSING: "The appliance's payment database login is not configured",
  PAYMENT_DATABASE_UNAVAILABLE: "The payment database is not available",
  CARD_NOT_CONFIGURED: "Card payment has not been configured yet",
  PROVIDER_CREDENTIALS_MISSING: "The provider account has no credentials",
  NO_ACTIVE_PAYMENT_ACCOUNT: "No active provider account",
  LIVE_MODE_NOT_AUTHORISED: "LIVE mode is not authorised on this appliance",
  PROVIDER_NOT_SUPPORTED: "The provider of the active account is not supported",
  PROVIDER_CREDENTIALS_INCOMPLETE: "Some required provider credentials are not set",
  PROVIDER_UNREACHABLE: "The payment provider cannot be reached",
  PMS_POSTING_NOT_AUTHORISED: "Sending charges to the PMS is switched off on this appliance",
  NO_DATABASE: "The appliance database is not available",
  READINESS_UNREADABLE: "Readiness could not be read",
  NO_ONBOARDED_INTERFACE: "No PMS interface is approved for room charge",
  NOT_DEPLOYED: "Not installed on this appliance",
  NOT_LICENSED: "Not included in the licence",
  LICENCE_NOT_ACTIVE: "The licence is not active",
  DEPENDENCY_NOT_EFFECTIVE: "A module it depends on is not in effect",
  DISABLED_BY_SITE: "Switched off for this site",
  LOCAL_STATE_UNAVAILABLE: "The appliance could not read its module settings",
  NOT_READY: "Not ready",
  INTERFACE_UNKNOWN: "The PMS interface is not known",
  CONNECTOR_NOT_FINANCIAL: "Room charge is supported on FIAS interfaces only",
  INTERFACE_NOT_ACTIVE: "The PMS interface is not active",
  NOT_ONBOARDED: "Not approved for room charge yet",
  ONBOARDING_NOT_APPROVED: "The room charge approval is incomplete",
};

export function codeWords(code: string): string | null {
  return CODE_WORDS[code] ?? null;
}

/**
 * The message for a failed step-up save: the server's own `message` wins when present, else known codes get a sentence.
 */
export function saveErrorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    const code = typeof e.body?.error === "string" ? (e.body.error as string) : "";
    const message = typeof e.body?.message === "string" ? (e.body.message as string) : "";
    if (code === "reauth_required") return "Your password was not accepted. Nothing was saved.";
    if (message) return message;
    switch (code) {
      case "live_not_authorised": return "LIVE mode is not authorised on this appliance.";
      case "payment_key_missing": return "This appliance has no payment key, so provider credentials cannot be stored.";
      case "provider_unsupported": return "That payment provider is not supported.";
      case "mode_invalid": return "Choose TEST or LIVE.";
      case "region_unsupported": return "That region is not supported by the provider.";
      case "out_of_range": return "A value is outside the allowed range.";
      case "revision_conflict": return "This interface changed since the page was loaded. It has been reloaded — review it and try again.";
      case "forbidden": return "Your role cannot make this change.";
      case "not_saved": case "invalid": case "not_approved": case "not_recorded": case "not_changed": return "The change was refused. Nothing was saved.";
    }
  }
  return errMsg(e);
}
