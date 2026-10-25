import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {
    code: string;
    status: number;
    constructor(code: string, message: string, status = 409) { super(message); this.code = code; this.status = status; }
  },
  api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a), put: (...a: unknown[]) => put(...a), patch: vi.fn(), del: (...a: unknown[]) => del(...a) },
}));
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));

import { ApiError } from "@/lib/api";
import { RoutingEditor, type ChoosableInterface } from "@/components/pms/routing-editor";

const iface = (id: string, label: string, lifecycle = "ACTIVE", published = true): ChoosableInterface => ({ id, label, lifecycle, published });
const state = (mode: string, labels: string[] = []) => ({
  policies: [{ network_id: "n1", name: "Rooms", enabled: true, archived: false, is_default: false, mode, fingerprint: "fp1",
    interfaces: labels.map((l, i) => ({ pms_interface_id: `i${i + 1}`, label: l, lifecycle_state: "ACTIVE", published: true })) }],
});
const plan = {
  network: { network_id: "n1", name: "Rooms", mode: "", fingerprint: "fp1", interfaces: [] },
  proposed: { mode: "DEDICATED", interfaces: [{ pms_interface_id: "i1", label: "Protel A", lifecycle_state: "ACTIVE", published: true, in_house_stays: 3, room_auth_ready: true, health_known: true, also_used_by: [], live_sessions_on_this_network: 0 }] },
  added: [], removed: [], blockers: [], warnings: [],
  impact: { active_sessions: 0, live_room_entitlements: 0, room_numbers_in_more_than_one_selected_pms: 0, sign_in_methods_switched_off: [], network_serves_clients: true },
  guarantees: ["No PMS traffic is started or stopped, and nothing is posted to any PMS."],
};

function open(interfaces: ChoosableInterface[], onDone = vi.fn()) {
  render(<RoutingEditor open network={{ id: "n1", name: "Rooms" }} interfaces={interfaces} onClose={() => {}} onDone={onDone} />);
  return onDone;
}

beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset(); del.mockReset(); get.mockResolvedValue(state("")); });

describe("the room sign-in editor guides the choice", () => {
  it("says which options are not available yet and why, instead of letting a dead end be chosen", async () => {
    open([iface("i1", "Protel A")]);
    const shared = await screen.findByRole("radio", { name: "A shared strict set" });
    expect((shared as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText(/Needs two PMS connections in service\. Only one is\./)).toBeTruthy();
    expect((screen.getByRole("radio", { name: "One dedicated PMS" }) as HTMLInputElement).disabled).toBe(false);
    expect(screen.getByText("Recommended")).toBeTruthy();
  });

  it("with no PMS in service, offers only none and points to where to add one", async () => {
    open([iface("i1", "Protel A", "AUTH_DISABLED")]);
    expect((await screen.findByRole("radio", { name: "One dedicated PMS" }) as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText(/No PMS connection is in service yet/)).toBeTruthy();
    expect((screen.getByRole("radio", { name: "No room sign-in" }) as HTMLInputElement).disabled).toBe(false);
  });

  it("tells the operator in a sentence what their choice means, and counts the shared selection down", async () => {
    open([iface("i1", "Protel A"), iface("i2", "Protel B")]);
    await userEvent.click(await screen.findByRole("radio", { name: "A shared strict set" }));
    expect(screen.getByText("Choose at least 2 more.")).toBeTruthy();
    await userEvent.click(screen.getByRole("checkbox", { name: /Protel A/ }));
    expect(screen.getByText("Choose at least 1 more.")).toBeTruthy();
    await userEvent.click(screen.getByRole("checkbox", { name: /Protel B/ }));
    expect(screen.getByTestId("routing-summary").textContent).toMatch(/checked against Protel A and Protel B together.*never asked which PMS/);
    expect(screen.getByText(/Step 1 of 2/)).toBeTruthy();
  });

  it("a preview taken from a state that has since changed returns to step 1 with a clear message and the new state", async () => {
    get.mockResolvedValueOnce(state("")).mockResolvedValue(state("DEDICATED", ["Protel A"]));
    post.mockResolvedValue(plan);
    put.mockRejectedValue(new (ApiError as unknown as new (c: string, m: string) => Error)("routing_changed", "stale"));
    open([iface("i1", "Protel A"), iface("i2", "Protel B")]);
    await userEvent.click(await screen.findByRole("radio", { name: "One dedicated PMS" }));
    await userEvent.click(screen.getByRole("radio", { name: /Protel A/ }));
    await userEvent.click(screen.getByRole("button", { name: "Preview the change" }));
    await screen.findByTestId("routing-preview");
    expect(screen.getByText(/Step 2 of 2/)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Confirm and apply" }));
    await waitFor(() => expect(screen.getByText(/changed after your preview/)).toBeTruthy());
    expect(screen.getByText(/Step 1 of 2/)).toBeTruthy();
    await waitFor(() => expect(screen.getByText(/Now: One dedicated PMS — Protel A/)).toBeTruthy());
  });
});

describe("returning a network to not decided is protected like every other routing change", () => {
  it("sends the fingerprint of the routing the operator is looking at", async () => {
    const policies = { policies: [{ network_id: "n1", name: "Rooms", enabled: true, archived: false, is_default: false, mode: "SHARED_STRICT", fingerprint: "fp-shared",
      interfaces: [{ pms_interface_id: "i1", label: "Protel A", lifecycle_state: "ACTIVE", published: true }, { pms_interface_id: "i2", label: "Protel B", lifecycle_state: "ACTIVE", published: true }] }], routes: [], unmapped_guest_networks: [] };
    get.mockImplementation((p: string) => Promise.resolve(p === "/auth/whoami" ? { roles: ["site_admin"] } : p === "/pms-interfaces" ? { interfaces: [] }
      : p === "/pms-routing/readiness" ? { overall: "READY", networks: [], interfaces: [], issues: [] } : policies));
    del.mockResolvedValue({});
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);
    await userEvent.click(await screen.findByRole("button", { name: /more actions|actions/i }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Back to not decided" }));
    const dialog = await screen.findByRole("alertdialog").catch(() => screen.findByRole("dialog"));
    await userEvent.click(within(dialog).getByRole("button", { name: "Return to not decided" }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/pms-routing/n1?based_on=fp-shared"));
  });
});
