// The module report edged relays from scd (GET /edge/v1/modules) and the words for its codes; unknown codes are shown
// verbatim by callers.

export type ModuleState = {
  id: string;
  label: string;
  requires: string[];
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

export type ModuleReport = {
  site_type: string | null;
  license_state: string;
  modules: Record<string, ModuleState>;
  deployment: Record<string, boolean>;
};

export const MODULE_REASON_WORDS: Record<string, string> = {
  NOT_DEPLOYED: "This appliance does not run this module.",
  NOT_LICENSED: "The licence for this site does not include it.",
  LICENCE_NOT_ACTIVE: "The licence is not active.",
  DEPENDENCY_NOT_EFFECTIVE: "A module it needs is not in use.",
  DISABLED_BY_SITE: "Switched off at this site.",
  LOCAL_STATE_UNAVAILABLE: "The site's switch could not be read.",
};

// A licence alone does not install a module: these say what does, per module, when the appliance does not run it.
const NOT_DEPLOYED_BY_MODULE: Record<string, string> = {
  hospitality: "Licensed, but this appliance's installation predates the Hotel module. Upgrade it with the current release (installation manual E.5) and it becomes available.",
  card_payment: "Licensed, but this appliance's installation predates configurable card payment. Upgrade it with the current release (installation manual E.5) and it becomes available.",
  room_charge: "Licensed, but this appliance's installation predates configurable room charge. Upgrade it with the current release (installation manual E.5) and it becomes available.",
  ha: "Not implemented in this release: a licence can include it, but OneGate does not provide high availability yet.",
  white_label: "Not implemented in this release: a licence can include it, but it adds nothing yet. Portal branding is available to every site.",
};

/** The words for one reason on one module; a module-specific explanation wins over the generic one. */
export function moduleReasonWords(moduleId: string, reason: string): string | undefined {
  if (reason === "NOT_DEPLOYED" && NOT_DEPLOYED_BY_MODULE[moduleId]) return NOT_DEPLOYED_BY_MODULE[moduleId];
  return MODULE_REASON_WORDS[reason];
}

export const READINESS_WORDS: Record<string, string> = {
  CARD_NOT_DEPLOYED: "Card payment does not run on this appliance.",
  PAYMENT_KEY_MISSING: "This appliance has no payment key, so provider credentials cannot be stored.",
  PAYMENT_CREDENTIALS_MISSING: "The appliance's payment database login is not configured.",
  PAYMENT_DATABASE_UNAVAILABLE: "The payment database cannot be reached.",
  CARD_NOT_CONFIGURED: "Card payment is not configured.",
  PROVIDER_CREDENTIALS_MISSING: "The provider account has no credentials.",
  NO_ACTIVE_PAYMENT_ACCOUNT: "No active provider account.",
  LIVE_MODE_NOT_AUTHORISED: "The active account is in LIVE mode, which is not authorised on this appliance.",
  PROVIDER_NOT_SUPPORTED: "The account's provider or region is not supported.",
  PROVIDER_CREDENTIALS_INCOMPLETE: "The provider account is missing a required credential.",
  PROVIDER_UNREACHABLE: "The payment provider cannot be reached.",
  PROVIDER_CALLS_NOT_AUTHORISED: "Configurable only: this appliance is not authorised to contact a payment provider, so clients are not offered card payment and a provider connection cannot be tested.",
  PMS_POSTING_NOT_AUTHORISED: "Sending charges to the PMS is switched off on this appliance (a safety switch set at installation). Room charge can be configured, but clients are not offered it and nothing is posted to the PMS.",
  NO_DATABASE: "The database is not available.",
  READINESS_UNREADABLE: "Readiness could not be read.",
  NO_ONBOARDED_INTERFACE: "No PMS interface is approved for room charge.",
};

export const SITE_TYPE_LABELS: Record<string, string> = {
  UNSPECIFIED: "Not set",
  HOTEL: "Hotel",
  CAFE: "Café",
  OFFICE: "Office",
  CLINIC: "Clinic",
  CAMPUS: "Campus",
  VENUE: "Venue",
  COMPOUND: "Compound",
  BEACH_CLUB: "Beach club",
  OTHER: "Other",
};
