// NETWORKING (Hotel Admin → Networking): the redesigned screens keep every request they made before, and the
// safety pattern — apply → confirm → automatic rollback — is on screen wherever a network change is live.
//
// What is asserted here is mostly what must NOT regress: no native browser dialog guards a destructive action,
// the pending change shows its countdown with Keep / Roll back, the heaviest action (rotating the certificate)
// still needs a reason, the password and the typed word ROTATE, a WAN change still needs the password, and a
// role that may only read sees no button it cannot use.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

// jsdom has no ResizeObserver and Radix's Switch measures itself on mount. Scoped to this file, as the other
// suites that need it do, so the shared environment is unchanged.
class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver ??= ResizeObserverStub;

vi.mock("@/lib/api", () => {
  class ApiError extends Error {
    status: number;
    body?: { error?: string };
    constructor(status: number, body?: { error?: string }) {
      super(body?.error ?? `HTTP ${status}`);
      this.status = status;
      this.body = body;
    }
  }
  return { ApiError, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useParams: () => ({ id: "net-1" }),
}));

import { api } from "@/lib/api";
import GuestNetworksPage from "@/app/(app)/network/page";
import NewGuestNetworkPage from "@/app/(app)/network/new/page";
import GuestNetworkDetailPage from "@/app/(app)/network/[id]/page";
import DhcpPage from "@/app/(app)/network/dhcp/page";
import RevisionsPage from "@/app/(app)/network/revisions/page";
import WanLanPage from "@/app/(app)/network/system/page";
import CertificatePage from "@/app/(app)/network/certificate/page";

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const del = api.del as unknown as ReturnType<typeof vi.fn>;
const list = <T,>(data: T[]) => ({ data, meta: { has_more: false } });

const IT = { roles: ["hotel_it_manager"] };
const VIEWER = { roles: ["site_viewer"] };

const NET = {
  id: "net-1", name: "Guest WiFi", ssid_label: "Hotel Guest", enabled: true, network_type: "vlan", vlan_id: 20,
  parent_interface: "ens192", bridge_name: "br-g20", gateway_ip: "10.20.0.1", subnet_cidr: "10.20.0.0/22",
  dhcp_mode: "local", dns_mode: "appliance", domain_name: "guest.local",
  lease_default_seconds: 3600, lease_min_seconds: 900, lease_max_seconds: 7200,
  captive_portal_enabled: true, internet_access_enabled: true, nat_enabled: true, client_isolation_enabled: false,
  portal_url: "http://10.20.0.1:8380", pools: [{ start_ip: "10.20.0.100", end_ip: "10.20.3.250" }],
};
const RES = { id: "res-1", guest_network_id: "net-1", mac: "aa:bb:cc:dd:ee:ff", reserved_ip: "10.20.0.50", hostname: "lobby-printer", enabled: true };

function route(map: Record<string, unknown>) {
  get.mockImplementation((path: string) => {
    for (const [k, v] of Object.entries(map)) {
      if (path === k || path.startsWith(k + "?")) return Promise.resolve(v);
    }
    return Promise.reject(new Error(`unexpected GET ${path}`));
  });
}

beforeEach(() => {
  get.mockReset(); post.mockReset(); del.mockReset();
});

describe("no native browser dialog remains on a Networking screen", () => {
  const ROOT = join(__dirname, "..");
  function walk(dir: string, out: string[] = []) {
    for (const e of readdirSync(dir)) {
      const f = join(dir, e);
      if (statSync(f).isDirectory()) walk(f, out);
      else if (/\.tsx?$/.test(e)) out.push(f);
    }
    return out;
  }
  const FILES = [...walk(join(ROOT, "app/(app)/network")), ...walk(join(ROOT, "components/network"))];
  const strip = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");

  it("uses no window.confirm, alert or prompt", () => {
    const offenders = FILES.filter((f) => /(?:^|[^.\w])(?:window\.)?(?:confirm|alert|prompt)\s*\(/.test(strip(readFileSync(f, "utf8"))));
    expect(offenders).toEqual([]);
  });

  it("every page title equals its menu label and none says StayConnect", () => {
    const titles: Record<string, string> = {
      "page.tsx": 'title="Guest networks"',
      "dhcp/page.tsx": 'title="DHCP & leases"',
      "system/page.tsx": 'title="WAN / LAN settings"',
      "revisions/page.tsx": 'title="Config history"',
      "certificate/page.tsx": 'title="TLS certificate"',
    };
    for (const [file, title] of Object.entries(titles)) {
      const src = readFileSync(join(ROOT, "app/(app)/network", file), "utf8");
      expect(src, file).toContain(title);
      expect(src, file).toContain('eyebrow="Networking"');
    }
    for (const f of FILES) expect(readFileSync(f, "utf8"), f).not.toMatch(/stayconnect/i);
  });
});

describe("Guest networks", () => {
  it("shows the pending revision with a live countdown, and Keep confirms that revision", async () => {
    const deadline = new Date(Date.now() + 90_000).toISOString();
    route({
      "/auth/whoami": IT,
      "/network/guest-networks": list([NET]),
      "/network/guest-networks/net-1/status": { id: "net-1", bridge_name: "br-g20", enabled: true, active_clients: 7 },
      "/network/revisions": list([{ id: "rev-9", seq: 9, state: "pending_confirmation", confirm_deadline: deadline }]),
    });
    post.mockResolvedValue({});
    render(<GuestNetworksPage />);

    const banner = await screen.findByText(/Revision #9 — confirm or it rolls back automatically/);
    const box = banner.closest("section")!;
    expect(within(box).getByLabelText(/seconds left/)).toBeTruthy();
    fireEvent.click(within(box).getByRole("button", { name: "Keep this change" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/revisions/rev-9/confirm"));
  });

  it("taking a network offline is a staged change, confirmed in a dialog that says so", async () => {
    route({
      "/auth/whoami": IT,
      "/network/guest-networks": list([NET]),
      "/network/guest-networks/net-1/status": { id: "net-1", bridge_name: "br-g20", enabled: true, active_clients: 0 },
      "/network/revisions": list([]),
    });
    post.mockResolvedValue({});
    render(<GuestNetworksPage />);

    fireEvent.click(await screen.findByRole("button", { name: "Disable Guest WiFi" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Take Guest WiFi offline?")).toBeTruthy();
    expect(within(dialog).getByText(/Nothing happens to guests until you apply/)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Take offline" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/guest-networks/net-1/disable"));
  });

  it("a read-only role sees the list but no write action", async () => {
    route({
      "/auth/whoami": VIEWER,
      "/network/guest-networks": list([NET]),
      "/network/guest-networks/net-1/status": { id: "net-1", bridge_name: "br-g20", enabled: true, active_clients: 0 },
      "/network/revisions": list([{ id: "rev-9", seq: 9, state: "pending_confirmation", confirm_deadline: new Date(Date.now() + 60_000).toISOString() }]),
    });
    render(<GuestNetworksPage />);
    await screen.findByText(/Your role can view guest networks but not change them/);
    expect(screen.queryByText("New guest network")).toBeNull();
    expect(screen.queryByRole("button", { name: /Apply changes/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Disable/ })).toBeNull();
    // The countdown is still shown; the actions are not.
    expect(await screen.findByText(/Revision #9/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Keep this change" })).toBeNull();
  });
});

describe("New guest network", () => {
  it("is a 7-step wizard whose interface picker only offers guest-capable ports", async () => {
    route({
      "/auth/whoami": IT,
      "/network/interfaces": {
        interfaces: [
          { name: "ens192", mac: "00:11:22:33:44:55", link_state: "up", mtu: 1500, ips: [], kind: "physical", role: "guest_trunk" },
          { name: "ens160", mac: "00:11:22:33:44:66", link_state: "up", mtu: 1500, ips: [], kind: "physical", role: "wan" },
        ],
      },
    });
    render(<NewGuestNetworkPage />);
    expect(await screen.findByText("Step 1 of 7 ·")).toBeTruthy();
    for (const s of ["Identity", "Interface / VLAN", "Subnet & gateway", "DHCP & DNS", "Captive portal", "Review", "Apply"]) {
      expect(screen.getAllByText(s).length).toBeGreaterThan(0);
    }
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Pool WiFi" } });
    fireEvent.click(screen.getByRole("button", { name: /Next/ }));

    const trunk = await screen.findByRole("radio", { name: /ens192/ });
    const wan = screen.getByRole("radio", { name: /ens160/ });
    expect((trunk as HTMLInputElement).disabled).toBe(false);
    expect((wan as HTMLInputElement).disabled).toBe(true);
  });
});

describe("DHCP & leases", () => {
  it("removing a reservation asks in a dialog, not a browser confirm, and deletes exactly that reservation", async () => {
    route({
      "/auth/whoami": IT,
      "/network/dhcp/leases": { leases: [] },
      "/network/dhcp/reservations": list([RES]),
      "/network/guest-networks": list([NET]),
    });
    del.mockResolvedValue({});
    const confirmSpy = vi.spyOn(window, "confirm");
    render(<DhcpPage />);

    const tab = await screen.findByRole("tab", { name: /Reservations/ });
    fireEvent.mouseDown(tab, { button: 0 });
    fireEvent.click(tab);
    fireEvent.click(await screen.findByRole("button", { name: "Remove reservation 10.20.0.50" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Remove the reserved address 10.20.0.50?")).toBeTruthy();
    expect(within(dialog).getByText(/any free address next time it connects/)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove reservation" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/network/dhcp/reservations/res-1"));
    expect(confirmSpy).not.toHaveBeenCalled();
  });
});

describe("Guest network detail", () => {
  it("shows the topology read-only and removes a reservation through a dialog", async () => {
    route({
      "/auth/whoami": IT,
      "/network/guest-networks/net-1": NET,
      "/network/guest-networks/net-1/status": { id: "net-1", bridge_name: "br-g20", enabled: true, active_clients: 3 },
      "/network/dhcp/reservations": list([RES]),
    });
    del.mockResolvedValue({});
    render(<GuestNetworkDetailPage />);
    expect(await screen.findByText("Status & topology")).toBeTruthy();
    expect(screen.getByText("br-g20")).toBeTruthy();
    fireEvent.click(await screen.findByRole("button", { name: "Remove reservation 10.20.0.50" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove reservation" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/network/dhcp/reservations/res-1"));
  });
});

describe("Config history", () => {
  it("filters to what needs attention and opens a revision's checks in a side sheet", async () => {
    route({
      "/auth/whoami": IT,
      "/network/revisions": list([
        { id: "r3", seq: 3, state: "rolled_back", summary: "apply from guest networks", failure_reason: "health failed" },
        { id: "r2", seq: 2, state: "active", summary: "create guest network Lobby" },
      ]),
      "/network/revisions/r3": {
        id: "r3", seq: 3, state: "rolled_back",
        validation: { ok: true },
        events: [{ phase: "apply", ok: true }],
        health: [{ check_name: "gateway_reachable", ok: false, detail: "timeout" }],
      },
    });
    render(<RevisionsPage />);
    expect(await screen.findByText("#2")).toBeTruthy();
    fireEvent.click(screen.getByRole("radio", { name: /Needs attention/ }));
    expect(screen.queryByText("#2")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Details for revision 3" }));
    const sheet = await screen.findByRole("dialog");
    expect(await within(sheet).findByText("gateway_reachable")).toBeTruthy();
    expect(within(sheet).getByText("Health checks")).toBeTruthy();
  });
});

describe("WAN / LAN settings", () => {
  const STATE = {
    wan: {
      interface: "ens160", mac: "00:11", link_up: true, mode: "static", ip: "172.21.60.25", prefix_len: 24,
      netmask: "255.255.255.0", gateway: "172.21.60.1", dns: ["1.1.1.1"], management_url: "https://172.21.60.25",
      outbound_interface: "ens160", connectivity: { gateway_reachable: true, internet_ok: true, dns_ok: true },
      persistent_ip: "172.21.60.25", drift: false,
    },
    lan: {
      physical_interface: "ens192", bridge: "br-lan", mac: "00:22", link_up: true, ip: "10.10.0.1", prefix_len: 24,
      netmask: "255.255.255.0", gateway_ip: "10.10.0.1", dhcp_enabled: false, dhcp_start: "", dhcp_end: "",
      dhcp_lease_seconds: 3600, dns: [], members: [],
    },
  };

  it("previews, warns that the admin address changes, and applies only with the password", async () => {
    route({ "/auth/whoami": IT, "/network/system": STATE, "/network/system/history": { history: [] } });
    post.mockImplementation((path: string) => {
      if (path === "/network/system/validate") return Promise.resolve({ validation: { ok: true }, management_url: "https://172.21.60.30", effective: {} });
      if (path === "/network/system/apply") return Promise.resolve({ ok: true, state: "pending_confirmation", validation: { ok: true }, management_url: "https://172.21.60.30", deadline_unix: Math.floor(Date.now() / 1000) + 120 });
      return Promise.resolve({});
    });
    render(<WanLanPage />);
    expect(await screen.findByRole("heading", { name: "WAN / LAN settings" })).toBeTruthy();

    fireEvent.change(screen.getByLabelText("IP address"), { target: { value: "172.21.60.30" } });
    expect(screen.getByText("Changing the WAN IP changes the admin address")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Validate & preview/ }));
    fireEvent.click(await screen.findByRole("button", { name: /Apply change/ }));

    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Apply change" });
    expect((submit as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText(/Confirm your password/), { target: { value: "s3cret" } });
    fireEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/network/system/apply", expect.objectContaining({ password: "s3cret" })));
    const body = post.mock.calls.find((c) => c[0] === "/network/system/apply")![1];
    expect(body.proposal.wan.ip).toBe("172.21.60.30");
    expect(await screen.findByText("Change applied — confirmation required")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Keep this configuration" })).toBeTruthy();
  });
});

describe("TLS certificate", () => {
  it("rotation needs a reason, the password and the typed word ROTATE, and sends exactly that", async () => {
    route({
      "/auth/whoami": IT,
      "/hotel-admin-cert": { available: true, status_threshold: "healthy", days_remaining: 300, subject: "CN=admin", issuer: "Internal CA" },
    });
    post.mockResolvedValue({ ok: true, exit: 0 });
    render(<CertificatePage />);
    expect(await screen.findByText(/Healthy · 300 days left/)).toBeTruthy();
    // The internal certificate authority is not shown.
    expect(screen.queryByText("Internal CA")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /Rotate/ }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Rotate now" });
    fireEvent.change(within(dialog).getByLabelText(/Reason/), { target: { value: "key exposure drill" } });
    fireEvent.change(within(dialog).getByLabelText(/Confirm your password/), { target: { value: "pw" } });
    fireEvent.change(within(dialog).getByLabelText(/to confirm/), { target: { value: "rotate" } });
    expect((submit as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText(/to confirm/), { target: { value: "ROTATE" } });
    expect((submit as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(submit);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/hotel-admin-cert/rotate", {
      reason: "key exposure drill", password: "pw", confirmation: "ROTATE",
    }));
  });

  it("a read-only role can see the certificate but has no Check or Rotate button", async () => {
    route({ "/auth/whoami": VIEWER, "/hotel-admin-cert": { available: true, status_threshold: "warning", days_remaining: 20 } });
    render(<CertificatePage />);
    await screen.findByText(/Your role can view the certificate/);
    expect(screen.queryByRole("button", { name: /Check certificate/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Rotate/ })).toBeNull();
  });
});
