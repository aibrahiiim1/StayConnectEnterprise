import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { mockFetch, PLATFORM_ME, renderAs } from "./helpers";
import { nav } from "./navigation";
import { CUSTOMER, SITE } from "./fixtures";

// The console's accessibility behaviour (WCAG 2.2 AA audit): page titles, an ended sign-in, the sign-in page's
// reason and safe return, focus when the sign-in step changes, labelled controls, field-level errors and
// descriptive names for buttons that show only a number.

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);
vi.mock("qrcode", () => ({ default: { toDataURL: async () => "data:image/png;base64,AAAA" } }));

import AppLayout from "@/app/(app)/layout";
import LoginPage from "@/app/login/page";
import CustomersPage from "@/app/(app)/customers/page";
import CustomerPage from "@/app/(app)/customers/[id]/page";
import { SigninSecondStep } from "@/components/signin-second-step";
import { TimezoneSelect } from "@/components/placement-fields";
import { Field } from "@/components/ui/input";
import { MonoId } from "@/components/ui/misc";
import { api, setUnauthorizedHandler } from "@/lib/api";

beforeEach(() => nav.reset());
afterEach(() => setUnauthorizedHandler(null));

const WHOAMI = {
  ...PLATFORM_ME,
  server_time: "2026-10-19T10:00:00Z",
  idle_expires_at: "2026-10-19T10:30:00Z",
  absolute_expires_at: "2026-10-19T22:00:00Z",
};

describe("the app shell", () => {
  it("names each route in the page title", async () => {
    nav.reset("/system/audit");
    mockFetch([{ match: "/api/v1/auth/whoami", body: WHOAMI }]);
    render(<AppLayout><p>content</p></AppLayout>);
    await screen.findByText("content");
    await waitFor(() => expect(document.title).toBe("Audit log · OneGate Central"));
  });

  it("sends an ended sign-in back to the sign-in page, saying why and where it was", async () => {
    nav.reset("/customers");
    const { calls } = mockFetch([
      { match: "/api/v1/auth/whoami", body: WHOAMI },
      { match: "/api/cloud/v1/customers", status: 401, body: { error: "unauthenticated", message: "session ended: signed out after inactivity" } },
      { method: "POST", match: "/api/v1/auth/logout", body: { status: "logged_out" } },
    ]);
    function Page() {
      useEffect(() => { api.get("/cloud/v1/customers").catch(() => undefined); }, []);
      return <p>customers</p>;
    }
    render(<AppLayout><Page /></AppLayout>);
    await waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/login?next=%2Fcustomers&reason=expired"));
    // The stale cookie is cleared first, or the sign-in page would bounce straight back.
    expect(calls.some((c) => c.method === "POST" && c.url === "/api/v1/auth/logout")).toBe(true);
  });
});

describe("the sign-in page", () => {
  async function signIn(user: ReturnType<typeof userEvent.setup>) {
    await user.type(await screen.findByLabelText(/^Email/), "admin@example.test");
    await user.type(screen.getByLabelText(/^Password/), "a-good-password");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await user.type(await screen.findByLabelText(/^Code/), "123456");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
  }
  const ROUTES = [
    { method: "POST", match: "/api/v1/auth/login", body: { second_step: "code" } },
    { method: "POST", match: "/api/v1/auth/login/code", body: PLATFORM_ME },
  ];

  it("has its own title, says politely why the person is there, and returns them to a safe next", async () => {
    const user = userEvent.setup();
    nav.reset("/login", "next=%2Fcustomers%2Fc1%3Ftab%3Dsites&reason=expired");
    mockFetch(ROUTES);
    render(<LoginPage />);
    await waitFor(() => expect(document.title).toBe("Sign in · OneGate Central"));
    const notice = await screen.findByText(/Your sign-in ended/);
    expect(notice.closest("[role=status]")).toHaveAttribute("aria-live", "polite");
    // The username field is for the browser's password manager: autocomplete says so.
    expect(screen.getByLabelText(/^Email/)).toHaveAttribute("autocomplete", "username");
    await signIn(user);
    await waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/customers/c1?tab=sites"));
  });

  it.each(["//evil.example/x", "/\\evil.example", "https://evil.example/"])("never follows next=%j", async (next) => {
    const user = userEvent.setup();
    nav.reset("/login", new URLSearchParams({ next }).toString());
    mockFetch(ROUTES);
    render(<LoginPage />);
    await signIn(user);
    await waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/overview"));
  });

  it("announces an error once: the banner is not inside another live region", async () => {
    const user = userEvent.setup();
    nav.reset("/login");
    mockFetch([{ method: "POST", match: "/api/v1/auth/login", status: 401, body: { error: "unauthenticated", message: "invalid credentials" } }]);
    render(<LoginPage />);
    await user.type(await screen.findByLabelText(/^Email/), "a@example.test");
    await user.type(screen.getByLabelText(/^Password/), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    const alert = await screen.findByRole("alert");
    expect(alert.parentElement?.closest("[aria-live]")).toBeNull();
  });
});

describe("the second sign-in step", () => {
  it("moves focus to the recovery codes' heading when they replace the form", async () => {
    const user = userEvent.setup();
    mockFetch([{ method: "POST", match: "/api/v1/auth/login/code", body: { recovery_codes: ["AAAA-BBBB", "CCCC-DDDD"] } }]);
    render(<SigninSecondStep step="code" onDone={vi.fn()} onRestart={vi.fn()} />);
    // Disabled until a code is typed, and it says why.
    const submit = screen.getByRole("button", { name: "Sign in" });
    expect(submit).toBeDisabled();
    expect(submit).toHaveAccessibleDescription("Enter your code to continue.");
    await user.type(screen.getByLabelText(/^Code/), "123456");
    await user.click(submit);
    const heading = await screen.findByRole("heading", { name: "Save your recovery codes" });
    expect(heading).toHaveFocus();
  });

  it("moves focus to the heading of the codes-running-low notice", async () => {
    const user = userEvent.setup();
    mockFetch([{ method: "POST", match: "/api/v1/auth/login/code", body: { recovery_codes_left: 2 } }]);
    render(<SigninSecondStep step="code" onDone={vi.fn()} onRestart={vi.fn()} />);
    await user.type(screen.getByLabelText(/^Code/), "ABCD-EFGH");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("heading", { name: "Recovery codes running low" })).toHaveFocus();
  });
});

describe("labelled controls and field errors", () => {
  it("binds the time zone control to its label", () => {
    render(
      <Field label="Time zone" required>
        <TimezoneSelect value="Africa/Cairo" onChange={() => {}} />
      </Field>,
    );
    const control = screen.getByLabelText(/^Time zone/);
    expect(control).toHaveValue("Africa/Cairo");
    expect(control).toHaveAttribute("aria-required", "true");
  });

  it("shows a refused customer name under the Name field and focuses it", async () => {
    const user = userEvent.setup();
    nav.reset("/customers");
    mockFetch([
      { match: "/api/cloud/v1/customers?status=active", body: { items: [CUSTOMER] } },
      { method: "POST", match: "/api/cloud/v1/customers", status: 409, body: { error: "conflict", message: "A customer with that name already exists." } },
    ]);
    renderAs(PLATFORM_ME, <CustomersPage />);
    await screen.findByRole("table", { name: "Customers" });
    await user.click(screen.getAllByRole("button", { name: /New customer/ })[0]);
    const dialog = await screen.findByRole("dialog", { name: "New customer" });
    const submit = within(dialog).getByRole("button", { name: "Create customer" });
    expect(submit).toHaveAccessibleDescription("To continue, enter the customer's name.");
    const name = within(dialog).getByLabelText(/^Name/);
    await user.type(name, "Semantics Hotels");
    await user.click(submit);
    await waitFor(() => expect(name).toHaveFocus());
    expect(name).toHaveAttribute("aria-invalid", "true");
    expect(name).toHaveAccessibleDescription("A customer with that name already exists.");
  });

  it("names the summary's count buttons by what they count and do", async () => {
    nav.reset("/customers/c1");
    mockFetch([
      { match: "/api/cloud/v1/customers/c1", method: "GET", body: { ...CUSTOMER, sites: 3 } },
      { match: "/api/cloud/v1/customers/c1/sites", method: "GET", body: { items: [SITE] } },
    ]);
    renderAs(PLATFORM_ME, <CustomerPage params={{ id: "c1" }} />);
    expect(await screen.findByRole("button", { name: "3 sites, open the Sites tab" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "1 appliance, open the Appliances tab" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "1 active license, open the Licenses tab" })).toBeInTheDocument();
  });

  it("names the copy button and announces the copy", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    render(<MonoId value="0123456789abcdef0123" head={8} title="Fingerprint" />);
    const button = screen.getByRole("button", { name: "Copy fingerprint 01234567…" });
    await user.click(button);
    expect(writeText).toHaveBeenCalledWith("0123456789abcdef0123");
    expect(await screen.findByRole("status")).toHaveTextContent("Copied");
  });
});
