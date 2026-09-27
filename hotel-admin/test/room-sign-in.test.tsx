// HOTEL → ROOM SIGN-IN: what a guest types besides the room number.
//
// Pinned here: the screen offers exactly the modes edged accepts (data-plane/cmd/edged/resources_site.go,
// pmsSignInModes) and no invented combination, a choice saves as a merge of the "pms" key without touching the
// on/off switch, a stored legacy "either" is explained rather than silently migrated, and a read-only role is
// offered no control. Sign-in methods keeps only the switch and a link here.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import RoomSignInPage from "@/app/(app)/room-sign-in/page";
import SignInMethodsPage from "@/app/(app)/sign-in-methods/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;
const list = <T,>(data: T[]) => ({ data, meta: { has_more: false } });

const PROTECTION = {
  max_failed_attempts: 5, observation_window_seconds: 60, restriction_seconds: 60, is_default: true,
  limits: {
    min_failed_attempts: 3, max_failed_attempts: 20,
    min_observation_window_seconds: 30, max_observation_window_seconds: 3600,
    min_restriction_seconds: 30, max_restriction_seconds: 3600,
  },
  last_change: null,
};

function routes(roles: string[], pms: Record<string, unknown>) {
  g.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/auth-methods") return Promise.resolve({ voucher: { enabled: true }, pms });
    if (path === "/pms-routing") return Promise.resolve({ routes: [] });
    if (path === "/guest-signin-protection") return Promise.resolve(PROTECTION);
    return Promise.resolve(list([]));
  });
}

const SUPPORTED = [
  "Room number + any one of: first name, surname or reservation number (recommended)",
  "Room number + surname",
  "Room number + first name",
  "Room number + reservation number",
];

beforeEach(() => { vi.clearAllMocks(); });

describe("Hotel → Room sign-in", () => {
  it("offers exactly the supported choices, each saying what the guest types", async () => {
    routes(["site_admin"], { enabled: true, mode: "room_any" });
    render(<RoomSignInPage />);
    expect(await screen.findByRole("heading", { name: "Room sign-in" })).toBeInTheDocument();
    const radios = await screen.findAllByRole("radio");
    expect(radios.map((r) => (r as HTMLInputElement).value)).toEqual(
      ["room_any", "room_lastname", "room_firstname", "room_reservation"]);
    for (const label of SUPPORTED) expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(/the guest types their room number and the surname on the reservation/i)).toBeInTheDocument();
    expect(radios[0]).toBeChecked();
    // "either" is never offered as a new choice.
    expect(radios.some((r) => (r as HTMLInputElement).value === "either")).toBe(false);
  });

  it("shows whether room sign-in is on, and sends the operator to the switch rather than offering one", async () => {
    routes(["site_admin"], { enabled: false, mode: "room_lastname" });
    render(<RoomSignInPage />);
    expect(await screen.findByText(/room sign-in is switched off/i)).toBeInTheDocument();
    expect(screen.queryAllByRole("switch")).toHaveLength(0);
    expect(screen.getByRole("link", { name: /client portal → sign-in methods/i })).toHaveAttribute("href", "/sign-in-methods");
  });

  it("saves a choice as a merge of the pms key, keeping the switch as it was", async () => {
    routes(["site_admin"], { enabled: true, mode: "room_lastname", provider: "fias" });
    put.mockResolvedValue({ pms: { enabled: true, mode: "room_reservation", provider: "fias" } });
    render(<RoomSignInPage />);
    fireEvent.click(await screen.findByText("Room number + reservation number"));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods",
      { pms: { enabled: true, mode: "room_reservation", provider: "fias" } }));
    await waitFor(() => expect(screen.getByDisplayValue("room_reservation")).toBeChecked());
  });

  it("choosing while the method is off does not switch it on", async () => {
    routes(["site_admin"], { enabled: false, mode: "room_lastname" });
    put.mockResolvedValue({ pms: { enabled: false, mode: "room_any" } });
    render(<RoomSignInPage />);
    fireEvent.click(await screen.findByText(SUPPORTED[0]));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", { pms: { enabled: false, mode: "room_any" } }));
  });

  it("explains a stored legacy 'either' instead of silently migrating it", async () => {
    routes(["site_admin"], { enabled: true, mode: "either" });
    render(<RoomSignInPage />);
    expect(await screen.findByText(/older setting that accepts a surname or a reservation number/i)).toBeInTheDocument();
    for (const r of screen.getAllByRole("radio")) expect(r).not.toBeChecked();
    expect(put).not.toHaveBeenCalled();
  });

  it("a read-only role sees the choice and cannot change it", async () => {
    routes(["front_office_operator"], { enabled: true, mode: "room_firstname" });
    render(<RoomSignInPage />);
    expect(await screen.findByText(/can see how room sign-in is set up but not change it/i)).toBeInTheDocument();
    const radios = screen.getAllByRole("radio");
    for (const r of radios) expect(r).toBeDisabled();
    expect(screen.getByDisplayValue("room_firstname")).toBeChecked();
    fireEvent.click(screen.getByText("Room number + surname"));
    expect(put).not.toHaveBeenCalled();
  });
});

describe("Client Portal → Sign-in methods keeps only the Room sign-in switch", () => {
  it("offers the switch and a link to the Hotel screen, and no credential choice", async () => {
    routes(["site_admin"], { enabled: true, mode: "room_any" });
    render(<SignInMethodsPage />);
    expect(await screen.findByRole("switch", { name: "Room sign-in" })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("link", { name: /room sign-in settings — under hotel/i })).toHaveAttribute("href", "/room-sign-in");
    expect(screen.queryByText(/besides the room number/i)).toBeNull();
    expect(screen.queryByRole("radio")).toBeNull();
  });

  it("the switch still saves the pms key only", async () => {
    routes(["site_admin"], { enabled: false });
    put.mockResolvedValue({ pms: { enabled: true, mode: "room_lastname" } });
    render(<SignInMethodsPage />);
    const sw = await screen.findByRole("switch", { name: "Room sign-in" });
    fireEvent.click(sw);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/auth-methods", { pms: { enabled: true, mode: "room_lastname" } }));
  });
});
