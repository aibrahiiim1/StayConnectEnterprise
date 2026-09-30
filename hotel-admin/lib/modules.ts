// THE MODULE REPORT edged relays from scd (GET /edge/v1/modules), and the words for its codes.
//
// One place for the words so the Modules, Payment methods and Room charge screens never describe the same
// condition three different ways. Unknown codes are shown verbatim by the callers: a newer appliance may
// report a condition this bundle has no sentence for, and hiding it would be worse than showing the code.

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
