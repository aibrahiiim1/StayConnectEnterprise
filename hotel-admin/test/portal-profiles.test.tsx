import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent, within } from "@testing-library/react";

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a), put: (...a: unknown[]) => put(...a), patch: vi.fn(), del: (...a: unknown[]) => del(...a) },
}));
vi.mock("@/components/ui/toast", () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }) }));

import { ProfileBar } from "@/app/(app)/portal-branding/profiles";
import { brandingBase } from "@/lib/api/portal-design";

const para = (re: RegExp) => (_: string, el: Element | null) => el?.tagName === "P" && re.test(el.textContent ?? "");

const net = (id: string, name: string, state: string, profile: string, extra: Record<string, unknown> = {}) =>
  ({ id, name, service_state: state, network_type: "untagged", parent_interface: "ens192", profile_id: profile, ...extra });

const list = {
  default: { id: "default", name: "Site default", network_ids: ["n1", "n3", "n4"], is_default: true },
  profiles: [{ id: "p1", name: "Pool bar", network_ids: ["n2"], is_default: false }],
  networks: [
    net("n1", "Rooms", "serving", "default"),
    net("n2", "Pool", "serving", "p1", { network_type: "vlan", vlan_id: 60 }),
    net("n3", "Old guest VLAN", "archived", "default", { network_type: "vlan", vlan_id: 90 }),
    net("n4", "Spare", "off", "default"),
  ],
};

beforeEach(() => {
  get.mockReset(); post.mockReset(); put.mockReset(); del.mockReset();
  get.mockImplementation(() => Promise.resolve(list));
});

describe("portal designs", () => {
  it("scopes every design endpoint to the profile being edited", () => {
    expect(brandingBase(null)).toBe("/portal-branding");
    expect(brandingBase("p1")).toBe("/portal-branding/profiles/p1/branding");
  });

  it("lists only serving networks as choices and keeps switched-off and archived ones apart", async () => {
    render(<ProfileBar profile={null} onSelect={vi.fn()} writable dirty={false} />);
    expect(await screen.findByLabelText("Design shown on Rooms")).toBeTruthy();
    expect(screen.getByLabelText("Design shown on Pool")).toBeTruthy();
    expect(screen.queryByLabelText("Design shown on Old guest VLAN")).toBeNull();
    expect(screen.queryByLabelText("Design shown on Spare")).toBeNull();
    expect(screen.queryByText("Old guest VLAN")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Show 2 switched-off or archived networks/ }));
    expect(screen.getByText("Old guest VLAN")).toBeTruthy();
    expect(screen.getByText("Spare")).toBeTruthy();
    expect(screen.getByText("Archived")).toBeTruthy();
    expect(screen.getByText("Switched off")).toBeTruthy();
  });

  it("says which networks share each design and how each network is reached", async () => {
    render(<ProfileBar profile={null} onSelect={vi.fn()} writable dirty={false} />);
    await screen.findByLabelText("Design shown on Rooms");
    expect(screen.getByText("Untagged (native)")).toBeTruthy();
    expect(screen.getByText("VLAN 60")).toBeTruthy();
    expect(screen.getByText(para(/Shown on 1 network: Rooms/))).toBeTruthy();
    expect(screen.getByText(para(/Shown on 1 network: Pool/))).toBeTruthy();
    expect(screen.getByText(/Also assigned to 2 switched-off or archived networks/)).toBeTruthy();
  });

  it("stages an assignment, shows what will change, and applies only after confirmation", async () => {
    put.mockResolvedValue({});
    render(<ProfileBar profile={null} onSelect={vi.fn()} writable dirty={false} />);
    fireEvent.change(await screen.findByLabelText("Design shown on Rooms"), { target: { value: "p1" } });
    expect(put).not.toHaveBeenCalled();
    expect(screen.getByText("Not applied")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Apply…" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Rooms: Site default → Pool bar")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Apply 1 change" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/portal-branding/profiles/assignments/n1", { profile_id: "p1" }));
  });

  it("returns a network to the site default and can discard staged choices", async () => {
    put.mockResolvedValue({});
    render(<ProfileBar profile={null} onSelect={vi.fn()} writable dirty={false} />);
    fireEvent.change(await screen.findByLabelText("Design shown on Pool"), { target: { value: "default" } });
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(screen.queryByText("Not applied")).toBeNull();
    fireEvent.change(screen.getByLabelText("Design shown on Pool"), { target: { value: "default" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply…" }));
    fireEvent.click(await screen.findByRole("button", { name: "Apply 1 change" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/portal-branding/profiles/assignments/n2", { profile_id: null }));
  });

  it("edits a design from its card and refuses to switch away from unsaved work", async () => {
    const onSelect = vi.fn();
    const { rerender } = render(<ProfileBar profile={null} onSelect={onSelect} writable dirty={false} />);
    const poolCard = (await screen.findByText("Pool bar", { selector: "span" })).closest("li") as HTMLElement;
    fireEvent.click(within(poolCard).getByRole("button", { name: /Edit this design/ }));
    expect(onSelect).toHaveBeenCalledWith("p1");
    rerender(<ProfileBar profile="p1" onSelect={onSelect} writable dirty />);
    expect(screen.getByText(/Save or discard your changes/)).toBeTruthy();
    expect((within(screen.getByText("Site default", { selector: "span" }).closest("li") as HTMLElement).getByRole("button", { name: /Edit this design/ }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("creates a design as a copy and selects it; offers no changes to a read-only role", async () => {
    post.mockResolvedValue({ id: "p2" });
    const onSelect = vi.fn();
    const { unmount } = render(<ProfileBar profile={null} onSelect={onSelect} writable dirty={false} />);
    fireEvent.click(await screen.findByText(/New design…/));
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Lobby" } });
    fireEvent.click(screen.getByRole("button", { name: "Create design" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/portal-branding/profiles", { name: "Lobby", copy_from: "default" }));
    await waitFor(() => expect(onSelect).toHaveBeenCalledWith("p2"));
    unmount();
    render(<ProfileBar profile={null} onSelect={vi.fn()} writable={false} dirty={false} />);
    await screen.findByText(para(/Shown on 1 network: Rooms/));
    expect(screen.queryByText(/New design…/)).toBeNull();
    expect((screen.getByLabelText("Design shown on Rooms") as HTMLSelectElement).disabled).toBe(true);
  });

  it("separates how the page looks from sign-in methods, PMS and packages", async () => {
    render(<ProfileBar profile={null} onSelect={vi.fn()} writable dirty={false} />);
    await screen.findByText("This page only changes how the sign-in page looks");
    expect(screen.getByRole("link", { name: "Sign-in methods" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "PMS routing" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Internet packages" })).toBeTruthy();
  });
});
