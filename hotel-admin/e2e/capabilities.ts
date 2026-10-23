import type { Page, Route } from "@playwright/test";

// Module pages fail closed without capabilities; install this full-hotel response after each spec's catch-all.

const SURFACES = [
  "account-password-settings", "audit", "auth-methods", "backups", "checkout-grace", "commercial-packages", "diagnostics", "financial-ops",
  "financial-review", "guest-accounts", "guest-device-self-service", "guest-signin-attempts",
  "guest-signin-credentials", "guest-signin-protection", "guest-signin-restrictions", "license", "modules",
  "network", "notification-providers", "operational-alerts", "operators", "payment-providers", "pms-events",
  "pms-financial-onboarding", "pms-interfaces", "pms-reconciliation", "pms-resolutions",
  "pms-roster-reconciliation", "pms-routing", "pms-source-conflicts", "pms-stays", "portal-assets",
  "portal-branding", "portal-https", "post-stay-profiles", "reports", "sessions", "social-providers", "stay-transfers", "usage",
  "voucher-code-settings", "voucher-codes", "vouchers", "walled-garden",
];

const MODULES: Record<string, { label: string; requires: string[]; switchable: boolean }> = {
  hospitality: { label: "Hotel (PMS, Room sign-in, stays)", requires: [], switchable: true },
  paid_access: { label: "Paid access (priced Internet Packages)", requires: [], switchable: true },
  card_payment: { label: "Card payment", requires: ["paid_access"], switchable: true },
  room_charge: { label: "Room charge (PMS posting)", requires: ["hospitality", "paid_access"], switchable: true },
  email_otp: { label: "Email accounts", requires: [], switchable: false },
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

/** Register after catch-all routes so this response takes precedence for capabilities. */
export async function installCapabilities(page: Page, caps = fullHotelCapabilities()) {
  await page.route("**/api/edge/v1/capabilities", (route: Route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(caps) }));
}
