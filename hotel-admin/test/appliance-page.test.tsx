import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, within, fireEvent, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// APPLIANCE & LICENCE renders the appliance's section 8 status (docs/CENTRAL_CONTROL_PLANE.md). These tests pin
// the words and actions for every activation, licence and Central state, that offline activation uses only the
// section 8 endpoints, and that protocol vocabulary stays inside Technical details.

const get = vi.fn();
const post = vi.fn();
const postRaw = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: (...a: any[]) => get(...a),
      post: (...a: any[]) => post(...a),
      postRaw: (...a: any[]) => postRaw(...a),
      put: vi.fn(),
      del: vi.fn(),
    },
  };
});
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }));

beforeEach(() => { get.mockReset(); post.mockReset(); postRaw.mockReset(); });
afterEach(() => { cleanup(); vi.resetModules(); });

type Status = Record<string, any>;

function status(over: Status = {}): Status {
  const base: Status = {
    activation: "activated", serial: "SC-7F3A-19", appliance_id: "ap-1", customer_name: "Coral Sea", site_name: "Aqua",
    license: { state: "active", valid_until: "2027-06-01T00:00:00Z", grace_ends_at: "2027-06-15T00:00:00Z", days_left: 247,
      max_concurrent_online_guests: 200, current_online_guests: 40 },
    central: { state: "connected", last_contact_at: new Date().toISOString(), last_error: null },
    details: { identity_key_fingerprint: "a1b2c3d4e5f60718", cert_fingerprint: "ffee", cert_not_after: "2026-12-01T00:00:00Z",
      assignment_version: 4, license_version: 2, wan_mac: "00:11:22:33:44:55", lan_mac: "00:11:22:33:44:66",
      central_endpoint: "https://sc-central.example", assignment_status: "granted", software_version: "1.2.3" },
  };
  return {
    ...base, ...over,
    license: { ...base.license, ...(over.license ?? {}) },
    central: { ...base.central, ...(over.central ?? {}) },
    details: { ...base.details, ...(over.details ?? {}) },
  };
}

function serve(st: Status, roles = ["site_admin"]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/central/status") return Promise.resolve(st);
    if (path === "/central/offline-request") return Promise.resolve({ request_id: "r1" });
    return Promise.resolve({});
  });
}

async function renderPage() {
  const Page = (await import("@/app/(app)/appliance/page")).default;
  render(<Page />);
  await screen.findByRole("region", { name: "Activation" });
}

function region(name: string) {
  return within(screen.getByRole("region", { name }));
}

/** Text of the page OUTSIDE the collapsed Technical details. */
function primaryText(): string {
  const clone = document.body.cloneNode(true) as HTMLElement;
  clone.querySelector('[data-testid="technical-details"]')?.remove();
  return clone.textContent ?? "";
}

describe("activation states", () => {
  it("not registered: says it keeps trying and offers the offline request", async () => {
    serve(status({ activation: "not_registered", appliance_id: null, customer_name: null, site_name: null,
      license: { state: "none", valid_until: null, days_left: null, max_concurrent_online_guests: null },
      central: { state: "unreachable", last_contact_at: null, last_error: "Could not reach OneGate Central." } }));
    await renderPage();
    expect(region("Activation").getByText("Not registered yet", { selector: "span" })).toBeTruthy();
    expect(region("Activation").getByText(/keeps trying by itself/)).toBeTruthy();
    expect(screen.getByRole("button", { name: /Download activation request/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Upload activation package/ })).toBeTruthy();
    expect(region("OneGate Central").getByText(/Guests are not affected/)).toBeTruthy();
    expect(region("OneGate Central").getByText("Could not reach OneGate Central.")).toBeTruthy();
  });

  it("waiting: tells the admin to give the vendor the serial, with a copy button", async () => {
    serve(status({ activation: "waiting", customer_name: null, site_name: null,
      license: { state: "none", valid_until: null, days_left: null, max_concurrent_online_guests: null } }));
    await renderPage();
    const a = region("Activation");
    expect(a.getByText("Waiting for activation", { selector: "span" })).toBeTruthy();
    expect(a.getByText(/Give your OneGate vendor this serial number/)).toBeTruthy();
    expect(a.getByText("SC-7F3A-19")).toBeTruthy();
    expect(a.getByRole("button", { name: /Copy serial number/ })).toBeTruthy();
    // Registered appliances do not emit an offline request (the appliance refuses it); only the package upload.
    expect(screen.queryByRole("button", { name: /Download activation request/ })).toBeNull();
    expect(region("Licence").getByText(/Guests cannot sign in until/)).toBeTruthy();
  });

  it("activating: says it is finishing", async () => {
    serve(status({ activation: "activating", license: { state: "none", valid_until: null, max_concurrent_online_guests: null } }));
    await renderPage();
    expect(region("Activation").getByText("Finishing activation…", { selector: "span" })).toBeTruthy();
    expect(region("Activation").getByText(/collecting its licence now/)).toBeTruthy();
  });

  it("activated: names the customer and site, and hides offline activation while Central answers", async () => {
    serve(status());
    await renderPage();
    expect(region("Activation").getByText("Coral Sea")).toBeTruthy();
    expect(region("Activation").getByText(/Aqua/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Upload activation package/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Upload licence file/ })).toBeTruthy();
  });

  it("retired: says it no longer signs guests in", async () => {
    serve(status({ activation: "retired" }));
    await renderPage();
    expect(region("Activation").getByText(/no longer signs guests in/)).toBeTruthy();
  });

  it("an activation the appliance cannot verify is surfaced, not reported as a quiet wait", async () => {
    serve(status({ activation: "waiting", details: { assignment_status: "unverifiable" } }));
    await renderPage();
    expect(screen.getByText(/holds an activation it cannot verify/)).toBeTruthy();
  });
});

describe("licence states", () => {
  const cases: [string, Status, RegExp, RegExp | null][] = [
    ["active", {}, /Valid until .* · 247 days left/, null],
    ["expiring", { state: "expiring", days_left: 12 }, /12 days left/, null],
    ["grace", { state: "grace", days_left: 5 }, /Guests keep signing in until/, /grace period/],
    ["expired", { state: "expired", days_left: 0 }, /Ended/, /The licence is expired/],
    ["suspended", { state: "suspended" }, /Suspended by your OneGate vendor/, /The licence is suspended/],
    ["revoked", { state: "revoked" }, /Revoked by your OneGate vendor/, /The licence is revoked/],
    ["wrong hardware", { state: "wrong_hardware" }, /issued for a different appliance/, /belongs to a different appliance/],
  ];
  for (const [name, lic, line, banner] of cases) {
    it(`${name}: says it in words${banner ? " with a banner" : ""}`, async () => {
      serve(status({ license: lic }));
      await renderPage();
      expect(region("Licence").getByText(line)).toBeTruthy();
      if (banner) expect(screen.getByText(banner)).toBeTruthy();
    });
  }

  it("problem banners say existing guests are not disconnected", async () => {
    serve(status({ license: { state: "expired" } }));
    await renderPage();
    expect(screen.getByText(/guests already online are not disconnected/)).toBeTruthy();
  });

  it("shows guest capacity and warns when it is reached", async () => {
    serve(status({ license: { max_concurrent_online_guests: 50, current_online_guests: 50 } }));
    await renderPage();
    expect(screen.getByText("Licensed capacity reached")).toBeTruthy();
    expect(region("Licence").getByText(/100% of the licensed capacity · full/)).toBeTruthy();
  });

  it("a changed WAN adapter is a warning that keeps the licence in force", async () => {
    serve(status({ license: { hardware_notice: "WAN MAC mismatch" } }));
    await renderPage();
    expect(screen.getByText(/The licence stays in force/)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/time-limited grace/i);
  });
});

describe("Central connection", () => {
  it("connected / unreachable / not configured", async () => {
    serve(status());
    await renderPage();
    expect(region("OneGate Central").getByText("Connected", { selector: "span" })).toBeTruthy();
    cleanup(); vi.resetModules();

    serve(status({ central: { state: "unreachable", last_error: "OneGate Central did not answer in time." } }));
    await renderPage();
    expect(region("OneGate Central").getByText("Temporarily unreachable", { selector: "span" })).toBeTruthy();
    expect(region("OneGate Central").getByText(/Guests are not affected/)).toBeTruthy();
    expect(region("OneGate Central").getByText("OneGate Central did not answer in time.")).toBeTruthy();
    // Unreachable: offline files are offered even to an activated appliance.
    expect(screen.getByRole("button", { name: /Upload activation package/ })).toBeTruthy();
    cleanup(); vi.resetModules();

    serve(status({ central: { state: "not_configured", last_contact_at: null } }));
    await renderPage();
    expect(region("OneGate Central").getByText("Not configured", { selector: "span" })).toBeTruthy();
  });

  it("Check now posts to /central/refresh and shows what it found", async () => {
    serve(status());
    post.mockResolvedValue({ ...status(), diagnostics: { dns_ok: true, central_https: false } });
    await renderPage();
    await userEvent.click(screen.getByRole("button", { name: /Check now/ }));
    expect(post).toHaveBeenCalledWith("/central/refresh");
    expect(await screen.findByText(/no connection could be opened/)).toBeTruthy();
  });
});

describe("offline activation and licence files", () => {
  it("downloads the request from /central/offline-request and uploads the package to /central/offline-package", async () => {
    serve(status({ activation: "not_registered", license: { state: "none" } }));
    const createObjectURL = vi.fn(() => "blob:x");
    (URL as any).createObjectURL = createObjectURL;
    (URL as any).revokeObjectURL = vi.fn();
    postRaw.mockResolvedValue({ status: "activated" });
    await renderPage();

    await userEvent.click(screen.getByRole("button", { name: /Download activation request/ }));
    await waitFor(() => expect(get).toHaveBeenCalledWith("/central/offline-request"));
    expect(createObjectURL).toHaveBeenCalled();

    const file = new File(['{"request_id":"r1","assignment":{}}'], "package.json", { type: "application/json" });
    fireEvent.change(screen.getByLabelText("Activation package"), { target: { files: [file] } });
    await waitFor(() => expect(postRaw).toHaveBeenCalledWith("/central/offline-package", '{"request_id":"r1","assignment":{}}'));
    expect(await screen.findByText(/Activated\. The appliance is restarting/)).toBeTruthy();
  });

  it("installs a licence file through POST /license", async () => {
    serve(status());
    postRaw.mockResolvedValue({ status: "installed" });
    await renderPage();
    const file = new File(['{"payload":"x"}'], "lic.license");
    fireEvent.change(screen.getByLabelText("Licence file"), { target: { files: [file] } });
    await waitFor(() => expect(postRaw).toHaveBeenCalledWith("/license", '{"payload":"x"}'));
  });

  it("a read-only role sees the status but no file actions", async () => {
    serve(status({ central: { state: "unreachable" } }), ["site_viewer"]);
    await renderPage();
    await screen.findByText(/cannot install files|not install files/);
    expect(screen.queryByRole("button", { name: /Upload/ })).toBeNull();
  });
});

describe("vocabulary", () => {
  it("keeps protocol words out of the primary surface", async () => {
    for (const st of [
      status(),
      status({ activation: "waiting", license: { state: "none" } }),
      status({ activation: "not_registered", central: { state: "unreachable", last_error: "Could not reach OneGate Central." } }),
    ]) {
      serve(st);
      await renderPage();
      const text = primaryText();
      for (const word of ["CSR", "mTLS", "assignment", "registry", "real-time", "Real-time", "fingerprint", "certificate"]) {
        expect(text, `"${word}" appears outside Technical details`).not.toContain(word);
      }
      // ...and they are available for support.
      const details = screen.getByTestId("technical-details");
      expect(within(details).getByText("Identity key fingerprint")).toBeTruthy();
      cleanup(); vi.resetModules();
    }
  });
});
