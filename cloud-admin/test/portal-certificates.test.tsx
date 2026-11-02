import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, SUPPORT_ME, renderAs } from "./helpers";
import { nav } from "./navigation";
import type { PortalFleetRow, PortalTLSSettings } from "@/lib/api";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import PortalCertificatesPage from "@/app/(app)/system/portal-certificates/page";

const SETTINGS: PortalTLSSettings = {
  acme_directory: "staging",
  contact_email: "pki@onegate.test",
  zone_name: "onegate.getsemantics.com",
  zone_id: "zone1",
  account_url: "https://acme-staging-v02.api.letsencrypt.org/acme/acct/1",
  account_directory: "https://acme-staging-v02.api.letsencrypt.org/directory",
  weekly_new_limit: 45,
  caa_applied_at: null,
  directory_url: "https://acme-staging-v02.api.letsencrypt.org/directory",
  token_set: false,
  dns_provider: "cloudflare",
  available: false,
  unavailable_reason: "no DNS API token is stored",
  test_overrides: [],
  live_hostnames: 0,
};

const ROW: PortalFleetRow = {
  appliance_id: "a1",
  serial: "OG-0001",
  customer_name: "Nile Hotels",
  site_name: "Nile Cairo",
  hostname: "k3x9q2ma.gw.onegate.getsemantics.com",
  portal_ip: "10.255.255.1",
  dns_state: "SYNCED",
  released: false,
  custom_domain: "wifi.nilehotel.com",
  custom_domain_status: "VERIFIED",
  certificates: [
    {
      hostname: "k3x9q2ma.gw.onegate.getsemantics.com", kind: "ONEGATE", order_id: "o1", status: "ISSUED", serial: "fa12",
      not_after: "2027-01-10T00:00:00Z", renew_after: "2026-12-10T00:00:00Z", revoked: false,
      acme_directory_url: "https://acme-staging-v02.api.letsencrypt.org/directory", acme_directory: "staging",
    },
  ],
};

beforeEach(() => nav.reset("/system/portal-certificates"));

describe("Portal certificates", () => {
  it("stores the DNS token write-only: sent once, cleared, never shown", async () => {
    const user = userEvent.setup();
    let tokenSet = false;
    const { calls } = mockFetch([
      { match: "/api/cloud/v1/portal-tls/settings", method: "GET", body: () => ({ body: { ...SETTINGS, token_set: tokenSet } }) },
      { match: "/api/cloud/v1/portal-tls/fleet", body: { items: [ROW] } },
      { match: "/api/cloud/v1/portal-tls/dns-token", method: "PUT", body: () => { tokenSet = true; return { body: { set: true } }; } },
    ]);
    renderAs(PLATFORM_ME, <PortalCertificatesPage />);

    expect(await screen.findByText("Not set")).toBeInTheDocument();
    expect(screen.getByText("Not issuing yet")).toBeInTheDocument();
    const field = screen.getByLabelText("Cloudflare API token");
    expect(field).toHaveAttribute("type", "password");
    await user.type(field, "cf-secret-123");
    await user.click(screen.getByRole("button", { name: "Store token" }));

    await waitFor(() => expect(screen.getByText("Set")).toBeInTheDocument());
    const put = calls.find((c) => c.method === "PUT" && c.url === "/api/cloud/v1/portal-tls/dns-token");
    expect(put?.body).toEqual({ token: "cf-secret-123" });
    expect((screen.getByLabelText("Replace token") as HTMLInputElement).value).toBe("");
    expect(document.body.textContent).not.toContain("cf-secret-123");
  });

  it("shows the Client Portal address first and the technical name second", async () => {
    mockFetch([
      { match: "/api/cloud/v1/portal-tls/settings", method: "GET", body: SETTINGS },
      { match: "/api/cloud/v1/portal-tls/fleet", body: { items: [{ ...ROW, client_portal: "portal.onegate.getsemantics.com" }] } },
    ]);
    renderAs(PLATFORM_ME, <PortalCertificatesPage />);
    const table = await screen.findByRole("table", { name: "Portal certificates" });
    expect(within(table).getByText("portal.onegate.getsemantics.com")).toBeInTheDocument();
    expect(within(table).getByText(/technical name /)).toBeInTheDocument();
  });

  it("lists the fleet and revokes with a reason", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { match: "/api/cloud/v1/portal-tls/settings", method: "GET", body: { ...SETTINGS, token_set: true, available: true } },
      { match: "/api/cloud/v1/portal-tls/fleet", body: { items: [ROW] } },
      { match: "/api/cloud/v1/portal-tls/appliances/a1/revoke", method: "POST", body: { revocations_requested: 1 } },
      { match: /\/api\/cloud\/v1\/portal-tls\/events/, body: { items: [] } },
    ]);
    renderAs(PLATFORM_ME, <PortalCertificatesPage />);

    const table = await screen.findByRole("table", { name: "Portal certificates" });
    const row = within(table).getAllByRole("row")[1];
    expect(within(row).getByRole("link", { name: "OG-0001" })).toHaveAttribute("href", "/appliances/a1");
    expect(within(row).getByText("k3x9q2ma.gw.onegate.getsemantics.com")).toBeInTheDocument();
    expect(within(row).getByText("Issued")).toBeInTheDocument();
    expect(within(row).getByText("Staging")).toBeInTheDocument();
    expect(within(row).getByText("Published")).toBeInTheDocument();

    await user.click(row);
    const drawer = await screen.findByRole("dialog", { name: "OG-0001" });
    expect(within(drawer).getByText("Certificate orders")).toBeInTheDocument();
    await user.click(within(drawer).getByRole("button", { name: "Revoke" }));
    const dialog = await screen.findByRole("dialog", { name: "Revoke portal certificates" });
    const confirm = within(dialog).getByRole("button", { name: "Revoke" });
    expect(confirm).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/Reason/), "key exposure suspected");
    await user.click(confirm);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.url === "/api/cloud/v1/portal-tls/appliances/a1/revoke" &&
        (c.body as { reason: string }).reason === "key exposure suspected")).toBe(true),
    );
  });

  it("confirms before switching to the production CA", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { match: "/api/cloud/v1/portal-tls/settings", method: "GET", body: SETTINGS },
      { match: "/api/cloud/v1/portal-tls/settings", method: "PUT", body: { ...SETTINGS, acme_directory: "production" } },
      { match: "/api/cloud/v1/portal-tls/fleet", body: { items: [] } },
    ]);
    renderAs(PLATFORM_ME, <PortalCertificatesPage />);
    await user.selectOptions(await screen.findByLabelText("ACME directory"), "production");
    await user.click(screen.getByRole("button", { name: "Save settings" }));
    const dialog = await screen.findByRole("dialog");
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "Switch to production" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" &&
      (c.body as { acme_directory: string }).acme_directory === "production")).toBe(true));
  });

  it("calls the setting the name base, and fixes it while appliance hostnames live under it", async () => {
    mockFetch([
      { match: "/api/cloud/v1/portal-tls/settings", method: "GET", body: { ...SETTINGS, live_hostnames: 1 } },
      { match: "/api/cloud/v1/portal-tls/fleet", body: { items: [] } },
    ]);
    renderAs(PLATFORM_ME, <PortalCertificatesPage />);
    const base = await screen.findByLabelText("Name base");
    expect(base).toHaveValue("onegate.getsemantics.com");
    expect(base).toBeDisabled();
    expect(screen.getByText(/Fixed while 1 appliance hostname live under it/)).toBeInTheDocument();
    expect(screen.queryByLabelText("DNS zone")).not.toBeInTheDocument();
  });

  it("lets the name base be set while no appliance hostname exists", async () => {
    mockFetch([
      { match: "/api/cloud/v1/portal-tls/settings", method: "GET", body: SETTINGS },
      { match: "/api/cloud/v1/portal-tls/fleet", body: { items: [] } },
    ]);
    renderAs(PLATFORM_ME, <PortalCertificatesPage />);
    expect(await screen.findByLabelText("Name base")).toBeEnabled();
  });

  it("shows support staff the page without any control that writes", async () => {
    mockFetch([
      { match: "/api/cloud/v1/portal-tls/settings", method: "GET", body: { ...SETTINGS, token_set: true, available: true,
        test_overrides: ["CTRLAPI_PORTAL_DNS_PROVIDER=challtestsrv"] } },
      { match: "/api/cloud/v1/portal-tls/fleet", body: { items: [ROW] } },
      { match: /\/api\/cloud\/v1\/portal-tls\/events/, body: { items: [{ id: 1, at: "2026-10-01T08:00:00Z", kind: "ISSUED", hostname: ROW.hostname }] } },
    ]);
    const user = userEvent.setup();
    renderAs(SUPPORT_ME, <PortalCertificatesPage />);
    const table = await screen.findByRole("table", { name: "Portal certificates" });
    expect(screen.getByText("Test overrides are active on this Central")).toBeInTheDocument();
    for (const name of ["Revoke", "Store token", "Test DNS access", "Apply CAA", "Save settings"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    await user.click(within(table).getAllByRole("row")[1]);
    const drawer = await screen.findByRole("dialog", { name: "OG-0001" });
    expect(await within(drawer).findByRole("list", { name: "Portal certificate events" })).toHaveTextContent("Issued");
    expect(within(drawer).queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
  });
});
