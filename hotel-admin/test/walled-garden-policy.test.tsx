// N-7: the Allowed sites screen shows the site's widest-range limit, marks a stored rule that is wider than it,
// edits a rule in place, and changes the limit only with a reason and the password.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

vi.mock("@/lib/api", () => {
  class ApiError extends Error { status = 0; }
  return { ApiError, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
vi.mock("@/lib/capabilities", () => ({ useCapabilities: () => null, moduleLicensed: () => false }));

import { api } from "@/lib/api";
import WalledGardenPage from "@/app/(app)/walled-garden/page";

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;

const RULES = [
  { id: "r1", kind: "cidr", value: "0.0.0.0/0", created_at: new Date().toISOString(), wider_than_policy: true },
  { id: "r2", kind: "domain", value: "checkout.stripe.com", ports: [443], description: "Stripe", created_at: new Date().toISOString() },
];
const POLICY = { widest_prefix_len: 24, min: 16, max: 32, default: 24, config_version: 0 };

beforeEach(() => {
  get.mockReset(); put.mockReset();
  get.mockImplementation((p: string) => {
    if (p === "/walled-garden") return Promise.resolve({ data: RULES, meta: { has_more: false } });
    if (p === "/walled-garden/policy") return Promise.resolve(POLICY);
    if (p === "/auth/whoami") return Promise.resolve({ roles: ["hotel_it_manager"] });
    return Promise.reject(new Error("unexpected " + p));
  });
});

describe("Allowed sites", () => {
  it("shows the limit and marks a rule wider than it", async () => {
    render(<WalledGardenPage />);
    expect(await screen.findByText("/24")).toBeTruthy();
    expect(await screen.findByText(/Wider than this site allows/)).toBeTruthy();
  });

  it("edits a rule with PUT, keeping its ports", async () => {
    put.mockResolvedValue({});
    render(<WalledGardenPage />);
    const edits = await screen.findAllByRole("button", { name: /^Edit\b/ });
    fireEvent.click(edits[1]);
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save rule" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/walled-garden/r2",
      { kind: "domain", value: "checkout.stripe.com", ports: [443], description: "Stripe" }));
  });

  it("changes the limit only with a reason and the password", async () => {
    put.mockResolvedValue({ ...POLICY, widest_prefix_len: 20, config_version: 1 });
    render(<WalledGardenPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Change limit" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByRole("spinbutton"), { target: { value: "20" } });
    const save = within(dialog).getByRole("button", { name: "Save limit" });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText(/Reason/), { target: { value: "CDN range for payments" } });
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw-123" } });
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/walled-garden/policy",
      { widest_prefix_len: 20, reason: "CDN range for payments", password: "pw-123" }));
  });
});
