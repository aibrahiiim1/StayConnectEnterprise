import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
const post = vi.fn();
const patch = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    post: (...a: any[]) => post(...a),
    patch: (...a: any[]) => patch(...a),
    del: (...a: any[]) => del(...a),
  },
  ApiError: class ApiError extends Error {
    status: number; code: string;
    constructor(status: number, body: any) { super(body?.message ?? "err"); this.status = status; this.code = body?.error ?? "http_error"; }
  },
}));

import RolesPage from "@/app/(app)/roles/page";
import { canRead, canWrite, rememberCustomRoles, roleLabel } from "@/lib/roles";

const ROLES = {
  features: [
    { key: "sessions", group: "Clients and access", label: "Active sessions and devices", help: "See who is online." },
    { key: "guest-data-rights", group: "Appliance", label: "Guest data requests", help: "Export or erase.", sensitive: "Personal data" },
    { key: "operators", group: "Appliance", label: "Staff accounts", help: "The list of staff accounts." },
  ],
  max: { sessions: "write", "guest-data-rights": "write", operators: "read" },
  built_in: [{ key: "site_admin", all: true, permissions: {} }, { key: "site_viewer", permissions: { sessions: "read" } }],
  custom: [{ id: "r1", key: "custom:r1", name: "Night desk", description: "Covers the night", permissions: { sessions: "write" }, members: 2, updated_at: "2026-11-04T08:00:00Z" }],
};

function routes(me: string[] = ["site_admin"]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: me });
    if (path === "/roles") return Promise.resolve(ROLES);
    return Promise.resolve({ data: [] });
  });
}

beforeEach(() => { get.mockReset(); post.mockReset(); patch.mockReset(); del.mockReset(); });

describe("the Roles page", () => {
  it("lists custom roles with their staff count and the built-in roles read-only", async () => {
    routes();
    render(<RolesPage />);
    expect(await screen.findByText("Night desk")).toBeInTheDocument();
    expect(screen.getByText("Covers the night")).toBeInTheDocument();
    expect(screen.getByText("Site viewer")).toBeInTheDocument();
    expect(screen.getByText("Everything")).toBeInTheDocument();
  });

  it("offers No access, View and Manage per feature, and never Manage where only Site admin may", async () => {
    routes();
    render(<RolesPage />);
    fireEvent.click((await screen.findAllByRole("button", { name: /New role/ }))[0]);
    const sheet = await screen.findByRole("dialog");
    const staff = within(sheet).getByRole("radiogroup", { name: "Staff accounts" });
    expect(within(staff).queryByRole("radio", { name: "Manage" })).toBeNull();
    expect(within(staff).getByRole("radio", { name: "View" })).toBeInTheDocument();
    const sessions = within(sheet).getByRole("radiogroup", { name: "Active sessions and devices" });
    expect(within(sessions).getByRole("radio", { name: "Manage" })).toBeInTheDocument();
    expect(within(sheet).getByText("Personal data")).toBeInTheDocument();
  });

  it("creates a role with the levels chosen, confirmed with the password", async () => {
    routes();
    post.mockResolvedValue({ id: "r2", key: "custom:r2" });
    render(<RolesPage />);
    fireEvent.click((await screen.findAllByRole("button", { name: /New role/ }))[0]);
    const sheet = await screen.findByRole("dialog");
    const create = within(sheet).getByRole("button", { name: /^Create role$/ }) as HTMLButtonElement;
    expect(create.disabled).toBe(true);
    fireEvent.change(within(sheet).getByLabelText(/^Name/), { target: { value: "Night desk 2" } });
    fireEvent.click(within(within(sheet).getByRole("radiogroup", { name: "Active sessions and devices" })).getByRole("radio", { name: "Manage" }));
    fireEvent.click(within(within(sheet).getByRole("radiogroup", { name: "Staff accounts" })).getByRole("radio", { name: "View" }));
    expect(create.disabled).toBe(false);
    fireEvent.click(create);
    const confirm = await screen.findByRole("dialog", { name: /Create this role\?/ });
    fireEvent.change(within(confirm).getByLabelText(/password/i), { target: { value: "pw" } });
    fireEvent.click(within(confirm).getByRole("button", { name: /^Create role$/ }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0]).toEqual(["/roles", { name: "Night desk 2", description: "", permissions: { sessions: "write", operators: "read" }, password: "pw" }]);
  });

  it("offers Try again when the roles cannot be read, and recovers", async () => {
    let fail = true;
    get.mockImplementation((path: string) => {
      if (path === "/roles") return fail ? Promise.reject(Object.assign(new Error("x"), { status: 500 })) : Promise.resolve(ROLES);
      return Promise.resolve({ roles: ["site_admin"] });
    });
    render(<RolesPage />);
    expect(await screen.findByText("The roles could not be loaded")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: /Try again/ }));
    expect(await screen.findByText("Night desk")).toBeInTheDocument();
  });

  it("counts the features a role manages", async () => {
    routes();
    render(<RolesPage />);
    expect(await screen.findByText("1 (1 manage)")).toBeInTheDocument();
  });

  it("does not ask for a password to remove a role staff still hold, and says why", async () => {
    routes();
    render(<RolesPage />);
    await screen.findByText("Night desk");
    fireEvent.keyDown(screen.getByRole("button", { name: /^Actions for Night desk/ }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Remove/ }));
    const dialog = await screen.findByRole("dialog", { name: /Night desk is still in use/ });
    expect(within(dialog).getByText(/2 staff accounts still hold it/)).toBeInTheDocument();
    expect(within(dialog).queryByLabelText(/password/i)).toBeNull();
    expect(del).not.toHaveBeenCalled();
  });

  it("says why Create role is disabled, and asks before discarding changes", async () => {
    routes();
    render(<RolesPage />);
    fireEvent.click((await screen.findAllByRole("button", { name: /New role/ }))[0]);
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("Give the role a name of at least 2 characters.")).toBeInTheDocument();
    fireEvent.change(within(sheet).getByLabelText(/^Name/), { target: { value: "Pool desk" } });
    expect(within(sheet).getByText("Choose at least one feature.")).toBeInTheDocument();
    fireEvent.click(within(sheet).getByRole("button", { name: "Cancel" }));
    expect(await screen.findByRole("dialog", { name: /Discard your changes\?/ })).toBeInTheDocument();
  });

  it("puts a refused password in plain words", async () => {
    routes();
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new ApiError(401, { error: "reauth_required", message: "password confirmation required" }));
    render(<RolesPage />);
    fireEvent.click((await screen.findAllByRole("button", { name: /New role/ }))[0]);
    const sheet = await screen.findByRole("dialog");
    fireEvent.change(within(sheet).getByLabelText(/^Name/), { target: { value: "Pool desk" } });
    fireEvent.click(within(within(sheet).getByRole("radiogroup", { name: "Active sessions and devices" })).getByRole("radio", { name: "View" }));
    fireEvent.click(within(sheet).getByRole("button", { name: /^Create role$/ }));
    const confirm = await screen.findByRole("dialog", { name: /Create this role\?/ });
    fireEvent.change(within(confirm).getByLabelText(/password/i), { target: { value: "wrong" } });
    fireEvent.click(within(confirm).getByRole("button", { name: /^Create role$/ }));
    expect(await within(confirm).findByText("Your password was not accepted. Nothing was changed.")).toBeInTheDocument();
  });

  it("is read-only for anyone but a Site admin", async () => {
    routes(["hotel_it_manager"]);
    render(<RolesPage />);
    expect(await screen.findByText(/Only a Site admin manages roles/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /New role/ })).toBeNull();
  });
});

describe("custom roles in the console's checks", () => {
  it("allows what whoami says the custom role allows, and nothing more", () => {
    rememberCustomRoles({ "custom:r9": { name: "Pool desk", permissions: { sessions: "write", usage: "read" } } });
    expect(canWrite("sessions", ["custom:r9"])).toBe(true);
    expect(canRead("usage", ["custom:r9"])).toBe(true);
    expect(canWrite("usage", ["custom:r9"])).toBe(false);
    expect(canRead("vouchers", ["custom:r9"])).toBe(false);
    expect(canRead("roles", ["custom:r9"])).toBe(false);
    expect(roleLabel("custom:r9")).toBe("Pool desk");
    expect(roleLabel("site_viewer")).toBe("Site viewer");
    expect(roleLabel("custom:unknown")).toBe("Custom role");
  });
});
