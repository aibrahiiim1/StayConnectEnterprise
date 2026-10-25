import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

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

const list = {
  default: { id: "default", name: "Site default", network_ids: ["n1"], is_default: true },
  profiles: [{ id: "p1", name: "Pool bar", network_ids: ["n2"], is_default: false }],
};
const networks = { data: [{ id: "n1", name: "Rooms" }, { id: "n2", name: "Pool" }] };

beforeEach(() => {
  get.mockReset(); post.mockReset(); put.mockReset(); del.mockReset();
  get.mockImplementation((path: string) => Promise.resolve(path === "/portal-branding/profiles" ? list : networks));
});

describe("portal profiles", () => {
  it("scopes every design endpoint to the profile being edited", () => {
    expect(brandingBase(null)).toBe("/portal-branding");
    expect(brandingBase("p1")).toBe("/portal-branding/profiles/p1/branding");
  });

  it("says where each design is shown and refuses to switch away from unsaved work", async () => {
    const onSelect = vi.fn();
    const { rerender } = render(<ProfileBar profile={null} onSelect={onSelect} writable dirty={false} />);
    await screen.findByText(/Shown on: Rooms/);
    fireEvent.change(screen.getByLabelText("Portal design"), { target: { value: "p1" } });
    expect(onSelect).toHaveBeenCalledWith("p1");
    rerender(<ProfileBar profile="p1" onSelect={onSelect} writable dirty />);
    expect((screen.getByLabelText("Portal design") as HTMLSelectElement).disabled).toBe(true);
    expect(screen.getByText(/Save or discard your changes/)).toBeTruthy();
  });

  it("assigns a network to a profile and back to the site default", async () => {
    put.mockResolvedValue({});
    render(<ProfileBar profile={null} onSelect={vi.fn()} writable dirty={false} />);
    fireEvent.click(await screen.findByText("Networks…"));
    fireEvent.change(await screen.findByLabelText("Portal design for Rooms"), { target: { value: "p1" } });
    await waitFor(() => expect(put).toHaveBeenCalledWith("/portal-branding/profiles/assignments/n1", { profile_id: "p1" }));
    fireEvent.change(screen.getByLabelText("Portal design for Pool"), { target: { value: "default" } });
    await waitFor(() => expect(put).toHaveBeenCalledWith("/portal-branding/profiles/assignments/n2", { profile_id: null }));
  });

  it("creates a profile as a copy and selects it; offers no changes to a read-only role", async () => {
    post.mockResolvedValue({ id: "p2" });
    const onSelect = vi.fn();
    const { unmount } = render(<ProfileBar profile={null} onSelect={onSelect} writable dirty={false} />);
    fireEvent.click(await screen.findByText(/New profile…/));
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Lobby" } });
    fireEvent.click(screen.getByRole("button", { name: "Create profile" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/portal-branding/profiles", { name: "Lobby", copy_from: "default" }));
    await waitFor(() => expect(onSelect).toHaveBeenCalledWith("p2"));
    unmount();
    render(<ProfileBar profile={null} onSelect={vi.fn()} writable={false} dirty={false} />);
    await screen.findByText(/Shown on/);
    expect(screen.queryByText(/New profile…/)).toBeNull();
  });
});
