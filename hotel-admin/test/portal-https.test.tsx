// NETWORKING → PORTAL HTTPS, and the callback URLs on Identity providers
// (docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md §4, §5, §8).
//
// Pinned here: the fallback is said in words with its reason and since when; the hotel domain shows the three
// DNS records with copy buttons; every change collects the password and sends exactly what edged expects; a
// refused address shows netd's issues; a role without manage is offered no control. On Identity providers:
// there is no redirect URI field, each portal host name's callback URL is shown with a "registered" tick, and
// the ticks are what is sent.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

// jsdom implements no ResizeObserver; the provider dialog's switch (Radix) measures itself with one.
class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("next/link", () => ({ default: ({ href, children, ...p }: any) => <a href={href} {...p}>{children}</a> }));

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api, ApiError } from "@/lib/api";
import PortalHTTPSPage from "@/app/(app)/portal-https/page";
import SocialProvidersPage from "@/app/(app)/social-providers/page";
import { fallbackWords, keepAvailable, looksLikePortalDomain, offeredWords, portalHeadline } from "@/lib/portal-https";
import { canRead, canWrite } from "@/lib/roles";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;
const patch = api.patch as unknown as ReturnType<typeof vi.fn>;
const del = api.del as unknown as ReturnType<typeof vi.fn>;

const ONEGATE = "abcd1234.gw.onegate.work";

function portalDoc(over: { state?: Record<string, unknown>; settings?: Record<string, unknown>; health?: Record<string, unknown> } = {}) {
  return {
    settings: { portal_ip: "10.255.255.1", custom_domain: null, updated_at: "2026-10-12T10:00:00Z", updated_by: "migration 0116", configured: true, ...over.settings },
    state: {
      central_available: true, onegate_hostname: ONEGATE, dns_state: "SYNCED", custom_domain: "", custom_domain_status: "",
      custom_domain_records: [], custom_domain_error: "", canonical_hostname: "", https_active: false,
      fallback_reason: "NO_CERTIFICATE", fallback_since: "2026-10-12T09:00:00Z", last_sync_at: "2026-10-13T09:00:00Z",
      last_sync_error: "", updated_at: "2026-10-13T09:00:00Z", ...over.state,
    },
    certificates: [],
    callback_urls: [{ hostname: ONEGATE, kind: "ONEGATE", url: `https://${ONEGATE}/auth/social/callback`, canonical: false }],
    history: [{ id: 1, changed_at: "2026-10-12T10:00:00Z", changed_by: "migration 0116", field: "portal_ip", old_value: null, new_value: "10.255.255.1", source_ip: null }],
    health: { name: "portal_https", level: "warn", reason: "NO_CERTIFICATE", message: "m", ...over.health },
  };
}

function routes(doc: unknown, roles: string[]) {
  g.mockImplementation((p: string) => {
    if (p === "/auth/whoami") return Promise.resolve({ roles });
    if (p === "/portal-https") return Promise.resolve(doc);
    return Promise.resolve({});
  });
}

beforeEach(() => {
  g.mockReset(); post.mockReset(); put.mockReset(); patch.mockReset(); del.mockReset();
});

describe("the words", () => {
  it("names every fallback reason and passes an unknown one through", () => {
    for (const r of ["NOT_CONFIGURED", "NO_HOSTNAME", "NO_CERTIFICATE", "CERTIFICATE_EXPIRED", "PORTAL_ADDRESS_NOT_ACTIVE", "CENTRAL_UNREACHABLE"]) {
      expect(fallbackWords(r)).not.toMatch(/_/);
    }
    expect(fallbackWords("SOMETHING_NEW")).toBe("something new");
  });

  it("headlines a fallback after HTTPS had worked as an outage", () => {
    const d = portalDoc({ health: { level: "critical" } });
    expect(portalHeadline(d as never).tone).toBe("danger");
    expect(portalHeadline(portalDoc() as never).tone).toBe("warning");
    expect(portalHeadline(portalDoc({ state: { https_active: true, canonical_hostname: ONEGATE, fallback_reason: "" } }) as never).tone).toBe("success");
  });

  it("checks a hotel domain the way the server does", () => {
    expect(looksLikePortalDomain("wifi.nilehotel.com")).toBeNull();
    expect(looksLikePortalDomain("WiFi.NileHotel.com.")).toBeNull();
    expect(looksLikePortalDomain("nilehotel.com")).toMatch(/subdomain/);
    expect(looksLikePortalDomain("x.gw.onegate.work")).toMatch(/onegate\.work/);
    expect(looksLikePortalDomain("https://wifi.nilehotel.com")).toMatch(/without https/);
  });

  it("explains why a provider is not offered", () => {
    expect(offeredWords("http_fallback", "facebook")).toMatch(/HTTP fallback/);
    expect(offeredWords("needs_hotel_domain", "google")).toMatch(/Google needs your hotel portal domain/);
    expect(offeredWords("callback_not_registered", "apple")).toMatch(/not ticked/);
    expect(keepAvailable(["a", "gone"], [{ hostname: "a" }])).toEqual(["a"]);
  });

  it("mirrors edged's portal-https matrix", () => {
    expect(canWrite("portal-https", ["hotel_it_manager"])).toBe(true);
    expect(canRead("portal-https", ["site_viewer"])).toBe(true);
    expect(canWrite("portal-https", ["site_viewer"])).toBe(false);
    expect(canRead("portal-https", ["front_office_operator"])).toBe(false);
  });
});

describe("Portal HTTPS page", () => {
  it("says the portal is on the HTTP fallback, why, and what still works", async () => {
    routes(portalDoc(), ["site_admin"]);
    render(<PortalHTTPSPage />);
    expect(await screen.findByText("The Client Portal is on the HTTP fallback")).toBeInTheDocument();
    expect(screen.getByText(/No valid certificate is installed/)).toBeInTheDocument();
    expect(screen.getByText(/emails carry the code only/)).toBeInTheDocument();
    expect(screen.getAllByText(ONEGATE).length).toBeGreaterThan(0);
    expect(screen.getByText("No certificate yet")).toBeInTheDocument();
  });

  it("changes the portal address with the password and sends the operator to apply it", async () => {
    routes(portalDoc(), ["site_admin"]);
    put.mockResolvedValueOnce({ changed: true, apply_required: true, apply_href: "/network" });
    render(<PortalHTTPSPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Change…" }));
    const dlg = await screen.findByRole("dialog");
    fireEvent.change(within(dlg).getByLabelText(/^Portal address/), { target: { value: "10.254.254.1" } });
    fireEvent.change(within(dlg).getByLabelText(/Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(within(dlg).getByRole("button", { name: "Save address" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/portal-https/address", { portal_ip: "10.254.254.1", password: "pw" }));
    expect(await screen.findByText("Apply the network configuration")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Client\s+networks/ })).toHaveAttribute("href", "/network");
  });

  it("shows netd's reasons when the address is refused", async () => {
    routes(portalDoc(), ["site_admin"]);
    put.mockRejectedValueOnce(new ApiError(422, {
      error: "validation", message: "the portal address cannot be used on this appliance",
      issues: [{ code: "overlaps_guest_network", message: "10.254.254.0/24 is the Lobby network" }],
    }));
    render(<PortalHTTPSPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Change…" }));
    const dlg = await screen.findByRole("dialog");
    fireEvent.change(within(dlg).getByLabelText(/^Portal address/), { target: { value: "10.254.254.1" } });
    fireEvent.change(within(dlg).getByLabelText(/Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(within(dlg).getByRole("button", { name: "Save address" }));
    expect(await within(dlg).findByText("10.254.254.0/24 is the Lobby network")).toBeInTheDocument();
  });

  it("registers a hotel domain with the password", async () => {
    routes(portalDoc(), ["site_admin"]);
    put.mockResolvedValueOnce({ changed: true, sync_requested: true, portal: portalDoc({ settings: { custom_domain: "wifi.nilehotel.com" } }) });
    render(<PortalHTTPSPage />);
    fireEvent.change(await screen.findByLabelText("Domain"), { target: { value: "wifi.nilehotel.com" } });
    fireEvent.click(screen.getByRole("button", { name: /Use this domain/ }));
    const dlg = await screen.findByRole("dialog");
    fireEvent.change(within(dlg).getByLabelText(/Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(within(dlg).getByRole("button", { name: "Use this domain" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/portal-https/custom-domain", { fqdn: "wifi.nilehotel.com", password: "pw" }));
  });

  it("shows the three DNS records to publish, each copyable, then Check now and Remove", async () => {
    const records = [
      { type: "TXT", name: "_onegate-verify.wifi.nilehotel.com", value: "tok123" },
      { type: "CNAME", name: "wifi.nilehotel.com", value: `${ONEGATE}.` },
      { type: "CNAME", name: "_acme-challenge.wifi.nilehotel.com", value: "q1w2e3r4t5y6u7i8.acme.onegate.work." },
    ];
    const doc = portalDoc({
      settings: { custom_domain: "wifi.nilehotel.com" },
      state: { custom_domain: "wifi.nilehotel.com", custom_domain_status: "PENDING", custom_domain_records: records },
    });
    routes(doc, ["site_admin"]);
    post.mockResolvedValueOnce({ refreshed: true, portal: doc });
    del.mockResolvedValueOnce({ changed: true, portal: portalDoc() });
    render(<PortalHTTPSPage />);
    for (const r of records) {
      expect(await screen.findByText(r.value)).toBeInTheDocument();
    }
    expect(screen.getAllByText("Waiting for DNS").length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: "Copy" }).length).toBe(6);

    fireEvent.click(screen.getAllByRole("button", { name: /Check now/ })[0]);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/portal-https/check", {}));

    fireEvent.click(screen.getByRole("button", { name: "Remove…" }));
    const dlg = await screen.findByRole("dialog");
    fireEvent.change(within(dlg).getByLabelText(/Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(within(dlg).getByRole("button", { name: "Remove domain" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/portal-https/custom-domain", { password: "pw" }));
  });

  it("renews with the password", async () => {
    routes(portalDoc({ state: { https_active: true, canonical_hostname: ONEGATE, fallback_reason: "" }, health: { level: "ok" } }), ["hotel_it_manager"]);
    post.mockResolvedValueOnce({ status: "renewal_requested" });
    render(<PortalHTTPSPage />);
    expect(await screen.findByText(/HTTPS is active/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Renew now/ }));
    const dlg = await screen.findByRole("dialog");
    fireEvent.change(within(dlg).getByLabelText(/Confirm your password/), { target: { value: "pw" } });
    fireEvent.click(within(dlg).getByRole("button", { name: "Renew now" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/portal-https/renew", { password: "pw" }));
  });

  it("offers a read-only role no control", async () => {
    routes(portalDoc(), ["site_viewer"]);
    render(<PortalHTTPSPage />);
    expect(await screen.findByText(/can view the portal/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Check now/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Renew now/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Change…" })).toBeNull();
    expect(screen.queryByLabelText("Domain")).toBeNull();
  });
});

describe("Identity providers: derived callback URLs", () => {
  const list = (rows: unknown[]) => ({
    data: rows, meta: { has_more: false }, https_active: true, canonical_hostname: ONEGATE, onegate_hostname: ONEGATE,
    callback_urls: [
      { hostname: ONEGATE, kind: "ONEGATE", url: `https://${ONEGATE}/auth/social/callback`, canonical: true },
      { hostname: "wifi.nilehotel.com", kind: "CUSTOM", url: "https://wifi.nilehotel.com/auth/social/callback", canonical: false },
    ],
  });
  const fb = {
    id: "p1", provider: "facebook", enabled: true, client_id: "app-1", callback_hosts: ["wifi.nilehotel.com"],
    callback_urls: [], secret_set: true, offered: false, offered_reason: "callback_not_registered",
    created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z",
  };

  function social(rows: unknown[], roles = ["site_admin"]) {
    g.mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles });
      if (p === "/social-providers") return Promise.resolve(list(rows));
      return Promise.resolve({});
    });
  }

  it("shows why a provider is not offered", async () => {
    social([fb]);
    render(<SocialProvidersPage />);
    expect(await screen.findByText(/not ticked as registered/)).toBeInTheDocument();
  });

  it("has no redirect URI field; the ticks are what is sent", async () => {
    social([]);
    post.mockResolvedValueOnce({});
    render(<SocialProvidersPage />);
    fireEvent.click((await screen.findAllByRole("button", { name: /Add (a )?provider/ }))[0]);
    const dlg = await screen.findByRole("dialog");
    expect(within(dlg).queryByText(/Redirect URI/)).toBeNull();
    expect(within(dlg).getByText(`https://${ONEGATE}/auth/social/callback`)).toBeInTheDocument();
    expect(within(dlg).getByText(/Search Console/)).toBeInTheDocument(); // Google is the default provider
    fireEvent.change(within(dlg).getByLabelText(/^Client ID/), { target: { value: "cid" } });
    fireEvent.change(within(dlg).getByLabelText(/^Client secret/), { target: { value: "sec" } });
    const ticks = within(dlg).getAllByRole("checkbox");
    expect(ticks).toHaveLength(2);
    fireEvent.click(ticks[1]); // the hotel domain
    fireEvent.click(within(dlg).getByRole("button", { name: "Add provider" }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/social-providers");
    expect(body.callback_hosts).toEqual(["wifi.nilehotel.com"]);
    expect(body).not.toHaveProperty("redirect_uri");
  });

  it("edits with the secret write-only and the stored ticks restored", async () => {
    social([fb]);
    patch.mockResolvedValueOnce({});
    render(<SocialProvidersPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const dlg = await screen.findByRole("dialog");
    expect(within(dlg).getByPlaceholderText("Unchanged")).toHaveValue("");
    const ticks = within(dlg).getAllByRole("checkbox") as HTMLInputElement[];
    expect(ticks.map((t) => t.checked)).toEqual([false, true]);
    fireEvent.click(ticks[0]);
    fireEvent.click(within(dlg).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(patch).toHaveBeenCalled());
    const [, body] = patch.mock.calls[0];
    expect([...body.callback_hosts].sort()).toEqual([ONEGATE, "wifi.nilehotel.com"].sort());
    expect(body).not.toHaveProperty("client_secret");
  });
});
