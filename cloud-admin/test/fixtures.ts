import type { ApplianceDetail, ApplianceRow, Customer, LicenseRow, Overview, Site } from "@/lib/api";

const inDays = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString();

export const OVERVIEW: Overview = {
  customers: 1,
  sites: 2,
  appliances: {
    total: 2, waiting: 1, activating: 0, activated: 1, retiring: 0, retired: 0,
    connected: 1, recently_seen: 0, offline: 0, never: 1,
  },
  licenses: { active: 1, expiring: 0, grace: 0, expired: 0, suspended: 0, revoked: 0, none: 1 },
  attention: [
    {
      kind: "waiting_activation", appliance_id: "a-new", serial: "OG-0002", customer_id: null, customer_name: null,
      site_name: null, detail: "Registered from 203.0.113.9", since: inDays(-0.1),
    },
  ],
};

export const CUSTOMER: Customer = {
  id: "c1", name: "Semantics Hotels", slug: "semantics", status: "active", created_at: "2026-01-01T00:00:00Z",
  sites: 1, appliances: 1, activated: 1, licenses_active: 1, attention: 0,
};

export const SITE: Site = { id: "s1", customer_id: "c1", code: "demo", name: "Demo Resort", timezone: "Africa/Cairo", country: "EG", status: "active", appliances: 1 };

export function appliance(activation: ApplianceRow["activation"], extra: Partial<ApplianceDetail> = {}): ApplianceDetail {
  const assigned = activation !== "waiting";
  return {
    id: "a1",
    serial: "OG-0001",
    hostname: "onegate-demo",
    model: "OG-500",
    version: "2.4.0",
    customer_id: assigned ? "c1" : null,
    customer_name: assigned ? "Semantics Hotels" : null,
    site_id: assigned ? "s1" : null,
    site_name: assigned ? "Demo Resort" : null,
    activation,
    connection: activation === "waiting" ? "connected" : "connected",
    last_seen_at: new Date().toISOString(),
    license: activation === "activated"
      ? { id: "l1", state: "active", valid_until: inDays(200), grace_ends_at: inDays(230), max_concurrent_online_guests: 500, license_version: 3 }
      : { state: "none" },
    registered_at: inDays(-3),
    activated_at: assigned ? inDays(-2) : null,
    open_alerts: 0,
    identity: { wan_mac: "00:11:22:33:44:55", cert_fingerprint: "ab:cd", cert_not_after: inDays(300) },
    assignment: { version: 2, acked_version: 2, signer_key_id: "k1" },
    licenses: activation === "activated"
      ? [{
          id: "l1", appliance_id: "a1", serial: "OG-0001", state: "active", valid_until: inDays(200),
          grace_period_days: 30, max_concurrent_online_guests: 500, license_version: 3, issued_at: inDays(-2),
        }]
      : [],
    events: [{ ts: inDays(-2), action: "appliance.activated", actor_email: "admin@example.test" }],
    replacement: null,
    retirement: null,
    ...extra,
  };
}

export const LICENSES: LicenseRow[] = [
  {
    id: "l1", appliance_id: "a1", serial: "OG-0001", customer_id: "c1", customer_name: "Semantics Hotels", site_id: "s1",
    site_name: "Demo Resort", state: "active", valid_until: inDays(200), grace_period_days: 30,
    max_concurrent_online_guests: 500, license_version: 3, issued_at: inDays(-2),
  },
  {
    id: "l2", appliance_id: "a2", serial: "OG-0003", customer_id: "c1", customer_name: "Semantics Hotels", site_id: "s1",
    site_name: "Demo Resort", state: "expired", valid_until: inDays(-60), grace_ends_at: inDays(-30), grace_period_days: 30,
    max_concurrent_online_guests: 200, license_version: 1, issued_at: inDays(-400),
  },
];
