import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { allowListProblem, cidrProblem, sameList, splitCidrs, valueWords } from "@/lib/enforcement-policy";

const get = vi.fn();
const patch = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), patch: (...a: any[]) => patch(...a), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), usePathname: () => "/network/enforcement" }));

beforeEach(() => { get.mockReset(); patch.mockReset(); });
afterEach(() => { cleanup(); vi.resetModules(); });

function doc(over: Record<string, any> = {}) {
  return {
    policy: {
      private_deny_enabled: true, private_allow_cidrs: [], allow_upstream_infrastructure: true, mac_binding_enabled: false,
      cutoff_seconds: 5, config_version: 1, updated_at: "2026-10-29T08:00:00Z", updated_by: "migration 0166", ...(over.policy ?? {}),
    },
    settings: [
      { field: "private_deny_enabled", label: "Deny guests the hotel's private networks", default: true, explanation: "Explains denial." },
      { field: "private_allow_cidrs", label: "Private networks a guest may still reach", default: [], unit: "IPv4 networks", explanation: "Explains allow-list." },
      { field: "allow_upstream_infrastructure", label: "Keep the upstream gateway and DNS reachable", default: true, explanation: "Explains upstream." },
      { field: "mac_binding_enabled", label: "Bind a guest's access to its device address (MAC)", default: false, explanation: "The AD-23 control." },
      { field: "cutoff_seconds", label: "Cut-off delay when an allowance is used up", default: 5, unit: "seconds", explanation: "Explains cut-off.", min: 1, max: 60 },
    ],
    limits: { max_cidrs: 64, min_prefix: 8, max_prefix: 32, cutoff_min: 1, cutoff_max: 60 },
    history: over.history ?? [
      { changed_at: "2026-10-29T08:00:00Z", changed_by: "migration 0166", reason: "defaults created", field: "all", old_value: null, new_value: "defaults", config_version: 1 },
      { changed_at: "2026-10-30T08:00:00Z", changed_by: "ops@hotel.example", reason: "printer network", field: "private_allow_cidrs", old_value: "", new_value: "192.168.50.0/24", config_version: 2 },
    ],
  };
}

function serve(d: any, roles: string[]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/network/enforcement-policy") return Promise.resolve(d);
    return Promise.resolve({});
  });
}

async function renderPage() {
  const Page = (await import("@/app/(app)/network/enforcement/page")).default;
  render(<Page />);
  await screen.findByLabelText(/Cut-off delay/);
}

describe("allow-list rules", () => {
  it("accepts IPv4 /8../32 network addresses and refuses the rest with a readable message", () => {
    expect(cidrProblem("10.0.0.0/8")).toBeNull();
    expect(cidrProblem("192.168.50.7/32")).toBeNull();
    expect(cidrProblem("10.0.0.0/7")).toMatch(/between \/8 and \/32/);
    expect(cidrProblem("192.168.1.7/24")).toMatch(/192\.168\.1\.0\/24/);
    expect(cidrProblem("fd00::/8")).toMatch(/IPv4/);
    expect(cidrProblem("300.1.1.0/24")).toMatch(/IPv4/);
    expect(cidrProblem("10.0.0.0")).toMatch(/CIDR/);
  });
  it("caps the list at 64 distinct networks", () => {
    const many = Array.from({ length: 65 }, (_, i) => `10.${i}.0.0/16`);
    expect(allowListProblem(many)).toMatch(/At most 64/);
    expect(allowListProblem(many.slice(0, 64))).toBeNull();
  });
  it("splits on lines and commas and compares as a set", () => {
    expect(splitCidrs("10.1.0.0/16\n192.168.1.0/24, 10.1.0.0/16")).toEqual(["10.1.0.0/16", "192.168.1.0/24", "10.1.0.0/16"]);
    expect(sameList(["a", "b"], ["b", "a", "a"])).toBe(true);
    expect(valueWords("mac_binding_enabled", "true")).toBe("on");
    expect(valueWords("cutoff_seconds", "7")).toBe("7 s");
  });
});

describe("enforcement policy page", () => {
  it("shows every setting with its default and explanation, and the change history", async () => {
    serve(doc(), ["hotel_it_manager"]);
    await renderPage();
    expect(screen.getByText(/The AD-23 control/)).toBeTruthy();
    expect(screen.getByText(/Explains allow-list/)).toBeTruthy();
    expect(screen.getAllByText(/Default:/).length).toBe(5);
    const hist = screen.getByRole("list", { name: "Change history" });
    expect(hist.textContent).toContain("printer network");
    expect(hist.textContent).toContain("none → 192.168.50.0/24");
    expect((screen.getByLabelText(/Cut-off delay/) as HTMLInputElement).value).toBe("5");
  });

  it("is read-only for a role that cannot write the network", async () => {
    serve(doc(), ["site_viewer"]);
    await renderPage();
    expect(screen.getByText(/can see these settings but not change them/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save settings" })).toBeNull();
    expect((screen.getByLabelText(/Cut-off delay/) as HTMLInputElement).disabled).toBe(true);
  });

  it("will not save without a reason, then sends only the changed fields with the reason", async () => {
    serve(doc(), ["hotel_it_manager"]);
    patch.mockResolvedValue(doc({ policy: { mac_binding_enabled: true, config_version: 3 } }));
    const user = userEvent.setup();
    await renderPage();
    const save = screen.getByRole("button", { name: "Save settings" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    await user.click(screen.getByRole("switch", { name: /Bind a guest's access/ }));
    expect(save.disabled).toBe(true); // changed but no reason or password yet
    await user.type(screen.getByLabelText(/Reason for the change/), "Prove AD-23 on the test appliance");
    await user.type(screen.getByLabelText(/Your password/), "pw-1234567890");
    expect(save.disabled).toBe(false);
    await user.click(save);
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
    expect(patch).toHaveBeenCalledWith("/network/enforcement-policy", { reason: "Prove AD-23 on the test appliance", password: "pw-1234567890", mac_binding_enabled: true });
  });

  it("refuses a bad allow-list entry and an out-of-range cut-off before sending anything", async () => {
    serve(doc(), ["site_admin"]);
    const user = userEvent.setup();
    await renderPage();
    await user.type(screen.getByLabelText(/Private networks a guest may still reach/), "192.168.1.7/24");
    expect(await screen.findByText(/has host bits set; write the network address, 192\.168\.1\.0\/24/)).toBeTruthy();
    const cutoff = screen.getByLabelText(/Cut-off delay/);
    await user.clear(cutoff);
    await user.type(cutoff, "61");
    expect(screen.getByText(/between 1 and 60/, { selector: "p" })).toBeTruthy();
    await user.type(screen.getByLabelText(/Reason for the change/), "A valid reason");
    await user.type(screen.getByLabelText(/Your password/), "pw-1234567890");
    expect((screen.getByRole("button", { name: "Save settings" }) as HTMLButtonElement).disabled).toBe(true);
    expect(patch).not.toHaveBeenCalled();
  });

  it("turning private denial off needs the confirmation text", async () => {
    serve(doc(), ["hotel_it_manager"]);
    patch.mockResolvedValue(doc({ policy: { private_deny_enabled: false, config_version: 2 } }));
    const user = userEvent.setup();
    await renderPage();
    await user.click(screen.getByRole("switch", { name: /Deny guests the hotel's private networks/ }));
    await user.type(screen.getByLabelText(/Reason for the change/), "Legacy kiosk needs the old subnet");
    await user.type(screen.getByLabelText(/Your password/), "pw-1234567890");
    await user.click(screen.getByRole("button", { name: "Save settings" }));
    // Nothing is sent until the dialog is confirmed.
    expect(patch).not.toHaveBeenCalled();
    const confirm = await screen.findByRole("button", { name: "Turn off denial and save" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    await user.type(screen.getByLabelText(/Type/), "turn off");
    expect((confirm as HTMLButtonElement).disabled).toBe(true); // case-sensitive
    await user.clear(screen.getByLabelText(/Type/));
    await user.type(screen.getByLabelText(/Type/), "TURN OFF");
    expect((confirm as HTMLButtonElement).disabled).toBe(false);
    await user.click(confirm);
    await waitFor(() => expect(patch).toHaveBeenCalledWith("/network/enforcement-policy", { reason: "Legacy kiosk needs the old subnet", password: "pw-1234567890", private_deny_enabled: false }));
  });

  it("shows the server's validation message when the save is refused", async () => {
    serve(doc(), ["hotel_it_manager"]);
    const { ApiError } = await import("@/lib/api");
    patch.mockRejectedValue(new ApiError(400, { error: "invalid_policy", message: "at most 64 networks may be allowed" }));
    const user = userEvent.setup();
    await renderPage();
    await user.click(screen.getByRole("switch", { name: /Bind a guest's access/ }));
    await user.type(screen.getByLabelText(/Reason for the change/), "Prove AD-23");
    await user.type(screen.getByLabelText(/Your password/), "pw-1234567890");
    await user.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText(/at most 64 networks may be allowed/)).toBeTruthy();
  });
});
