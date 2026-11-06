import { describe, it, expect, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return { ...actual, useCapabilities: () => ({ surfaces: ["pms-stays", "pms-events", "modules"], modules: {} }) };
});
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api, ApiError } from "@/lib/api";
import ModulesPage from "@/app/(app)/modules/page";
import { rememberCustomRoles } from "@/lib/roles";

const mod = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
  id, label, requires: [], deployed: true, authorized: false, licensed: false, switchable: false,
  enabled: false, ready: true, effective: false, manageable: false, reasons: ["NOT_LICENSED", "DISABLED_BY_SITE"], ...extra,
});

describe("Modules — identity modules say what Sign-in methods says", () => {
  it("shows No for a licensed identity module that is switched off, Yes when on, and where it is set", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      return Promise.resolve({
        site_type: "HOTEL", license_state: "Active", deployment: {},
        modules: {
          sms_otp: mod("sms_otp", "SMS one-time code", { authorized: true, licensed: true, enabled: false, reasons: ["DISABLED_BY_SITE"] }),
          email_otp: mod("email_otp", "Email one-time code", { authorized: true, licensed: true, enabled: true, effective: true, reasons: [] }),
        },
      });
    });
    render(<ModulesPage />);
    const sms = (await screen.findByText("SMS one-time code")).closest("div.flex.flex-col, [class*='flex-col']") as HTMLElement;
    const smsSwitched = within(sms).getByText("Switched on").nextElementSibling as HTMLElement;
    expect(smsSwitched.textContent).toBe("NoSet on Sign-in methods");
    expect(within(sms).getByRole("link", { name: /Sign-in methods/ })).toHaveAttribute("href", "/sign-in-methods");
    const email = screen.getByText("Email one-time code").closest("div.flex.flex-col, [class*='flex-col']") as HTMLElement;
    expect((within(email).getByText("Switched on").nextElementSibling as HTMLElement).textContent).toBe("YesSet on Sign-in methods");
  });
});

describe("Modules on a café", () => {
  it("shows every module, says what is missing without a licence, no configure link, and the kept hotel records", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      return Promise.resolve({
        site_type: "CAFE", license_state: "Active", deployment: {},
        modules: {
          hospitality: mod("hospitality", "Hotel", { switchable: true, enabled: true, manageable: true }),
          whatsapp_otp: mod("whatsapp_otp", "WhatsApp one-time code"),
          future_mod: mod("future_mod", "Something new"),
        },
      });
    });
    render(<ModulesPage />);
    expect(await screen.findByText("WhatsApp one-time code")).toBeInTheDocument();
    expect(screen.getByText("Something new")).toBeInTheDocument();

    const hotel = screen.getByText("Hotel").closest("div.flex.flex-col, [class*='flex-col']") as HTMLElement;
    expect(within(hotel).getAllByText("Needs a licence", { selector: "dd" })).toHaveLength(2);
    expect(within(hotel).queryByText("—")).toBeNull();
    expect(screen.queryByText("Switched off at this site.")).toBeNull();
    expect(within(hotel).queryByText("PMS connection")).toBeNull();
    const records = screen.getByTestId("records-hospitality");
    expect(within(records).getByRole("link", { name: "Stays" })).toHaveAttribute("href", "/stays");
    expect(within(records).getByRole("link", { name: "PMS activity" })).toBeInTheDocument();
    expect(within(records).queryByRole("link", { name: "Guest sign-in attempts" })).toBeNull();
  });
});

describe("Modules licensed but not installed", () => {
  it("says what switches each one on, instead of only that the appliance does not run it", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      const off = { deployed: false, licensed: true, enabled: true, ready: false, reasons: ["NOT_DEPLOYED"] };
      return Promise.resolve({
        site_type: "HOTEL", license_state: "Active", deployment: {},
        modules: {
          hospitality: mod("hospitality", "Hotel", { switchable: true, ...off }),
          card_payment: mod("card_payment", "Card payment", { switchable: true, ...off }),
          room_charge: mod("room_charge", "Room charge", { switchable: true, ...off }),
        },
      });
    });
    render(<ModulesPage />);
    expect(await screen.findByText(/predates the Hotel module/)).toBeInTheDocument();
    expect(screen.getByText(/predates configurable card payment/)).toBeInTheDocument();
    expect(screen.getByText(/predates configurable room charge/)).toBeInTheDocument();
    expect(screen.queryByText("This appliance does not run this module.")).toBeNull();
  });
});

describe("Modules with no implementation in this release", () => {
  it("never shows high availability or white label as available, and says why", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      const off = { deployed: false, licensed: true, enabled: true, ready: true, reasons: ["NOT_DEPLOYED"] };
      return Promise.resolve({
        site_type: "HOTEL", license_state: "Active", deployment: {},
        modules: { ha: mod("ha", "High availability", off), white_label: mod("white_label", "White label", off) },
      });
    });
    render(<ModulesPage />);
    expect(await screen.findByText(/OneGate does not provide high availability yet/)).toBeInTheDocument();
    expect(screen.getByText(/Portal branding is available to every site/)).toBeInTheDocument();
    for (const name of ["High availability", "White label"]) {
      const card = screen.getByText(name).closest("div.flex.flex-col, [class*='flex-col']") as HTMLElement;
      expect((within(card).getByText("Available").nextElementSibling as HTMLElement).textContent).toBe("No");
      expect((within(card).getByText("Ready").nextElementSibling as HTMLElement).textContent).toBe("Not available");
    }
  });
});

describe("A configurable module that may not run", () => {
  it("shows the readiness reason and never the bare NOT_READY code", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      return Promise.resolve({
        site_type: "HOTEL", license_state: "Active", deployment: {},
        modules: { card_payment: mod("card_payment", "Card payment", { deployed: true, licensed: true, enabled: true, switchable: true,
          ready: false, reasons: ["NOT_READY"], readiness: ["PROVIDER_CALLS_NOT_AUTHORISED"] }) },
      });
    });
    render(<ModulesPage />);
    expect(await screen.findByText(/not authorised to contact a payment provider/)).toBeInTheDocument();
    expect(screen.queryByText("NOT_READY")).toBeNull();
  });
});

describe("Switching a module", () => {
  const hotel = (extra: Record<string, unknown> = {}) => ({
    site_type: "HOTEL", license_state: "Active", deployment: {},
    modules: { room_charge: mod("room_charge", "Room charge", { licensed: true, switchable: true, enabled: true, reasons: [], ...extra }) },
  });

  it("offers the switch to a custom role allowed to change modules", async () => {
    rememberCustomRoles({ "custom:m1": { name: "Module keeper", permissions: { modules: "write" } } });
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["custom:m1"] });
      if (p === "/modules/changes") return Promise.resolve({ changes: [] });
      return Promise.resolve(hotel());
    });
    render(<ModulesPage />);
    expect(await screen.findByRole("button", { name: "Switch off" })).toBeInTheDocument();
    expect(screen.queryByText(/not switch them on or off/)).toBeNull();
  });

  it("words a refused password and keeps the dialog open", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (p === "/modules/changes") return Promise.resolve({ changes: [] });
      return Promise.resolve(hotel());
    });
    (api.put as ReturnType<typeof vi.fn>).mockRejectedValue(new ApiError(401, { error: "reauth_required", message: "password confirmation required" }));
    render(<ModulesPage />);
    await userEvent.click(await screen.findByRole("button", { name: "Switch off" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Switch off Room charge?")).toBeInTheDocument();
    await userEvent.type(within(dialog).getByLabelText(/Reason/), "Not used at this site");
    await userEvent.type(within(dialog).getByLabelText(/password/i), "secret");
    await userEvent.click(within(dialog).getByRole("button", { name: "Switch off" }));
    expect(await within(dialog).findByText("The password was not accepted. Nothing was changed.")).toBeInTheDocument();
    expect(screen.queryByText("password confirmation required")).toBeNull();
  });

  it("shows a deliberate switch-off as neutral, and who changed it in Recent changes", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (p === "/operators") return Promise.resolve({ data: [{ id: "op-1", display_name: "Mona" }] });
      if (p === "/modules/changes") {
        return Promise.resolve({ changes: [{ module: "room_charge", changed_at: new Date().toISOString(), changed_by: "op-1",
          reason: "Not used at this site", old_enabled: true, new_enabled: false }] });
      }
      return Promise.resolve(hotel({ enabled: false, reasons: ["DISABLED_BY_SITE"] }));
    });
    render(<ModulesPage />);
    const list = await screen.findByRole("list", { name: "Recent module changes" });
    expect(await within(list).findByText(/switched off by Mona/)).toBeInTheDocument();
    expect(within(list).getByText("Reason: Not used at this site")).toBeInTheDocument();
    expect(screen.getAllByText("Switched off").length).toBeGreaterThan(0);
    expect(screen.queryByText("Not in use")).toBeNull();
  });

  it("offers Try again when the modules cannot be loaded", async () => {
    (api.get as ReturnType<typeof vi.fn>).mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      return Promise.reject(new ApiError(502, { error: "upstream", message: "scd unreachable" }));
    });
    render(<ModulesPage />);
    const alert = await screen.findByRole("alert", {}, { timeout: 3000 });
    expect(within(alert).getByText(/could not be loaded/)).toBeInTheDocument();
    expect(within(alert).getByRole("button", { name: /Try again/ })).toBeInTheDocument();
    expect(screen.queryByText(/scd unreachable/)).toBeNull();
  });
});
