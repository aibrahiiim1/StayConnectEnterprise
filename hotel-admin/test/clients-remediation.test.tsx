// CLIENTS AREA AND ROLE PARITY, audit remediation (2026-10-16). One file for the small console rules the fixes
// added; the screens themselves keep their own suites.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { canRead, canWrite } from "@/lib/roles";
import generated from "@/lib/role-permissions.generated.json";
import { REASON_MAX_CHARS, REASON_MIN_CHARS, reasonLength, reasonOk } from "@/lib/reason";
import { accountEditBody, validityWindow } from "@/lib/account-form";
import { periodQuery } from "@/lib/usage-period";
import { senderReady } from "@/lib/sign-in-readiness";
import { offeredWords } from "@/lib/portal-https";

describe("the console's role table is the server's (L-2)", () => {
  it("is read from the generated file, not written by hand", () => {
    // Every cell the generated table holds is what canRead / canWrite answer.
    for (const [role, row] of Object.entries(generated.roles as Record<string, Record<string, string>>)) {
      for (const [res, p] of Object.entries(row)) {
        expect(canRead(res, [role]), `${role} read ${res}`).toBe(true);
        expect(canWrite(res, [role]), `${role} write ${res}`).toBe(p === "write");
      }
    }
  });

  it("closes the drift the audit measured", () => {
    for (const role of ["front_office_operator", "guest_relations_operator", "payments_operator"]) {
      expect(canRead("usage", [role]), role).toBe(true);
    }
    expect(canRead("usage", ["voucher_operator"])).toBe(false);
    expect(canWrite("commercial-packages", ["hotel_it_manager"])).toBe(true);
    expect(canRead("commercial-packages", ["payments_operator"])).toBe(true);
    expect(canWrite("commercial-packages", ["payments_operator"])).toBe(false);
  });
});

describe("reasons are counted in characters (C-17)", () => {
  it("matches edged's bounds and counts Arabic by character", () => {
    expect([REASON_MIN_CHARS, REASON_MAX_CHARS]).toEqual([4, 500]);
    expect(reasonOk("شكرا")).toBe(true);
    expect(reasonLength("  شكرا  ")).toBe(4);
    expect(reasonOk("ab ")).toBe(false);
    expect(reasonOk("ع".repeat(500))).toBe(true);
    expect(reasonOk("ع".repeat(501))).toBe(false);
  });
});

describe("account edits can clear a field and refuse an inverted window (C-9)", () => {
  const form = (fields: Record<string, string>) => {
    const f = new FormData();
    for (const [k, v] of Object.entries(fields)) f.set(k, v);
    return f;
  };
  it("sends null for an emptied optional field and never undefined", () => {
    const body = accountEditBody(form({ username: " alex ", display_name: "", notes: "  ", valid_from: "", valid_until: "" }), false);
    expect(body).toEqual({ username: "alex", display_name: null, notes: null, valid_from: null, valid_until: null });
  });
  it("never sends a self-service account's username", () => {
    expect(accountEditBody(form({ username: "x@y.z" }), true)).not.toHaveProperty("username");
  });
  it("refuses a window that ends before it starts", () => {
    expect(validityWindow(form({ valid_from: "2026-10-20T10:00", valid_until: "2026-10-19T10:00" })).error).toMatch(/later/);
    expect(validityWindow(form({ valid_from: "2026-10-19T10:00", valid_until: "2026-10-20T10:00" })).error).toBeUndefined();
  });
});

describe("the Usage explorer period (C-8)", () => {
  it("builds the same from/to for the list and the detail", () => {
    const now = new Date("2026-10-16T12:00:00Z");
    expect(periodQuery("7", "", "", now).qs).toBe(
      `from=${encodeURIComponent("2026-10-09T12:00:00.000Z")}&to=${encodeURIComponent(now.toISOString())}`);
    expect(periodQuery("custom", "", "2026-10-01").error).toMatch(/both dates/);
    expect(periodQuery("custom", "2026-10-05", "2026-10-01").error).toMatch(/on or after/);
    expect(periodQuery("custom", "2026-10-01", "2026-10-01").error).toBeUndefined();
  });
});

describe("code-method readiness (C-13) and the Offered words (C-14)", () => {
  it("does not count a test-only sender in a production build", () => {
    const stub = [{ channel: "sms", kind: "stub", enabled: true }];
    expect(senderReady(stub, "sms", true)).toBe(false);
    expect(senderReady(stub, "sms", false)).toBe(true);
    expect(senderReady([{ channel: "sms", kind: "twilio", enabled: true }], "sms", true)).toBe(true);
    expect(senderReady([{ channel: "sms", kind: "twilio", enabled: false }], "sms", true)).toBe(false);
  });
  it("names the Sign-in methods switch when it is why a provider is not offered", () => {
    expect(offeredWords("method_off", "google")).toMatch(/switched off under Sign-in methods/);
  });
});

// ----------------------------------------------------------------------------------- the layout ---------

vi.mock("next/navigation", () => ({
  usePathname: () => currentPath,
  useRouter: () => ({ replace: vi.fn(), refresh: vi.fn(), push: vi.fn() }),
}));
let currentPath = "/usage";
const get = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<any>);
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: vi.fn(async () => ({})) } };
});

function serve(roles: string[]) {
  get.mockImplementation(async (p: string) => {
    if (p === "/auth/whoami") return { operator_id: "o", email: "op@x", roles, site_id: "s", expires_at: "" };
    if (p === "/capabilities") return { surfaces: ["usage", "sessions", "reports", "guest-accounts", "audit"], modules: null };
    return {};
  });
}

describe("a screen the role may not read says so instead of opening empty (L-2)", () => {
  beforeEach(() => { get.mockReset(); vi.resetModules(); });
  afterEach(() => vi.resetModules());

  it("shows Not permitted and does not mount the page", async () => {
    currentPath = "/usage";
    serve(["voucher_operator"]);
    const Layout = (await import("@/app/(app)/layout")).default;
    await act(async () => { render(<Layout><p>the usage page</p></Layout>); });
    await waitFor(() => expect(screen.getByText(/your role does not include this screen/i)).toBeInTheDocument());
    expect(screen.queryByText("the usage page")).toBeNull();
  });

  it("mounts the page for a role that may read it", async () => {
    currentPath = "/usage";
    serve(["front_office_operator"]);
    const Layout = (await import("@/app/(app)/layout")).default;
    await act(async () => { render(<Layout><p>the usage page</p></Layout>); });
    await waitFor(() => expect(screen.getByText("the usage page")).toBeInTheDocument());
  });
});
