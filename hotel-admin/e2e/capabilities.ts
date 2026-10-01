import type { Page, Route } from "@playwright/test";

// WHAT A REAL APPLIANCE SAYS IT CAN DO.
//
// Since the site-modules delivery (D44), the Admin Console asks edged's /capabilities which surfaces are served
// and which optional modules are licensed, and a module-owned page (stays, PMS connections, financial review,
// sign-in protection, post-stay, transfers...) FAILS CLOSED when the answer does not confirm it: it shows "Not
// enabled on this appliance". The browser suites mock edged, and their catch-all answered /capabilities with
// `{}` -- an appliance that serves nothing -- so every module page rendered the not-enabled screen and 58 tests
// timed out looking for content the product correctly refused to show.
//
// This is the answer of a fully licensed hotel appliance with every Phase flag on, in the exact shape edged
// returns (read from PRE-LIVE: { surfaces: string[], modules: { <id>: ModuleFlags } }). It is installed AFTER a
// spec's own catch-all, so it takes precedence for /capabilities only and changes nothing else a spec mocks.

const SURFACES = [
  "audit", "auth-methods", "backups", "checkout-grace", "commercial-packages", "diagnostics", "financial-ops",
  "financial-review", "guest-accounts", "guest-device-self-service", "guest-signin-attempts",
  "guest-signin-credentials", "guest-signin-protection", "guest-signin-restrictions", "license", "modules",
  "network", "notification-providers", "operational-alerts", "operators", "payment-providers", "pms-events",
  "pms-financial-onboarding", "pms-interfaces", "pms-reconciliation", "pms-resolutions",
  "pms-roster-reconciliation", "pms-routing", "pms-source-conflicts", "pms-stays", "portal-assets",
  "portal-branding", "post-stay-profiles", "reports", "sessions", "social-providers", "stay-transfers", "usage",
  "voucher-code-settings", "voucher-codes", "vouchers", "walled-garden",
];

const MODULES: Record<string, { label: string; requires: string[]; switchable: boolean }> = {
  hospitality: { label: "Hotel (PMS, Room sign-in, stays)", requires: [], switchable: true },
  paid_access: { label: "Paid access (priced Internet Packages)", requires: [], switchable: true },
  card_payment: { label: "Card payment", requires: ["paid_access"], switchable: true },
  room_charge: { label: "Room charge (PMS posting)", requires: ["hospitality", "paid_access"], switchable: true },
  email_otp: { label: "Email one-time code", requires: [], switchable: false },
  sms_otp: { label: "SMS one-time code", requires: [], switchable: false },
  whatsapp_otp: { label: "WhatsApp one-time code", requires: [], switchable: false },
  social_login: { label: "Social sign-in", requires: [], switchable: false },
  white_label: { label: "White label", requires: [], switchable: false },
  ha: { label: "High availability", requires: [], switchable: false },
};

export function fullHotelCapabilities() {
  const modules: Record<string, unknown> = {};
  for (const [id, m] of Object.entries(MODULES)) {
    modules[id] = {
      id, label: m.label, requires: m.requires,
      deployed: true, authorized: true, licensed: true, switchable: m.switchable,
      enabled: true, ready: true, effective: true, manageable: true, reasons: [],
    };
  }
  return { surfaces: [...SURFACES], modules };
}

/** Answer /capabilities as a fully licensed hotel appliance. Call AFTER the spec's own catch-all route. */
export async function installCapabilities(page: Page, caps = fullHotelCapabilities()) {
  await page.route("**/api/edge/v1/capabilities", (route: Route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(caps) }));
}
