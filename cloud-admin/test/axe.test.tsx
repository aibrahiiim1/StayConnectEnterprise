import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { mockFetch, PLATFORM_ME, renderAs } from "./helpers";
import { nav } from "./navigation";
import { CUSTOMER, SITE } from "./fixtures";

// AUTOMATED ACCESSIBILITY CHECKS (axe-core) over the main screens and components the other tests already render.
// Every WCAG 2.0/2.1/2.2 A and AA rule axe has runs, and any violation fails the test. Only colour contrast is
// switched off: jsdom does no layout or painting, so axe cannot compute it here (the tokens' contrast is checked
// in design-tokens-sync.test.ts and by hand).

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);
vi.mock("qrcode", () => ({ default: { toDataURL: async () => "data:image/png;base64,AAAA" } }));

import LoginPage from "@/app/login/page";
import CustomersPage from "@/app/(app)/customers/page";
import CustomerPage from "@/app/(app)/customers/[id]/page";
import { SigninSecondStep } from "@/components/signin-second-step";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { TabList, TabPanel } from "@/components/ui/tabs";
import { ApiError } from "@/lib/api";

beforeEach(() => nav.reset());

async function expectNoViolations(root: Element = document.body) {
  const result = await axe.run(root, {
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"] },
    rules: { "color-contrast": { enabled: false } },
  });
  const found = result.violations.map((v) => `${v.id}: ${v.help}\n  ${v.nodes.map((n) => n.target.join(" ")).join("\n  ")}`);
  expect(found).toEqual([]);
}

describe("axe-core finds no WCAG A/AA violations", () => {
  it("on the sign-in page, including its error and expired-sign-in notice", async () => {
    const user = userEvent.setup();
    nav.reset("/login", "reason=expired");
    mockFetch([{ method: "POST", match: "/api/v1/auth/login", status: 401, body: { error: "unauthenticated", message: "invalid credentials" } }]);
    render(<LoginPage />);
    await screen.findByText(/Your sign-in ended/);
    await expectNoViolations();
    await user.type(screen.getByLabelText(/^Email/), "a@example.test");
    await user.type(screen.getByLabelText(/^Password/), "x");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await screen.findByRole("alert");
    await expectNoViolations();
  });

  it("on both second sign-in steps", async () => {
    mockFetch([{ method: "POST", match: "/api/v1/auth/login/enrol", body: { secret: "JBSWY3DPEHPK3PXP", otpauth_uri: "otpauth://totp/x", account: "a" } }]);
    const { unmount } = render(<main><SigninSecondStep step="enrol" onDone={() => {}} onRestart={() => {}} /></main>);
    await screen.findByAltText(/QR code/);
    await expectNoViolations();
    unmount();
    render(<main><SigninSecondStep step="code" onDone={() => {}} onRestart={() => {}} /></main>);
    await expectNoViolations();
  });

  it("on a dialog of fields, with a field error and a disabled submit that says why", async () => {
    render(
      <DialogForm open onOpenChange={() => {}} title="New site" submitLabel="Create site" disabled
        disabledReason="To continue, enter the site's name." onSubmit={() => {}}>
        <Field label="Name" required error="Enter the new site's name."><Input /></Field>
        <Field label="Time zone" required hint="Where the site is."><Select><option>Africa/Cairo</option></Select></Field>
      </DialogForm>,
    );
    await screen.findByRole("dialog");
    await expectNoViolations();
  });

  it("on a confirmation that needs a reason, a typed value and the password", async () => {
    render(
      <ConfirmDialog open onOpenChange={() => {}} title="Delete site" confirmLabel="Delete site" confirmVariant="danger"
        consequences={["It cannot be undone."]} confirmText="demo" requireReason requirePassword onConfirm={() => {}} />,
    );
    await screen.findByRole("dialog");
    await expectNoViolations();
  });

  it("on tabs and their panel", async () => {
    render(
      <main>
        <TabList idBase="t" label="Sections" value="a" onChange={() => {}}
          tabs={[{ value: "a", label: "Summary" }, { value: "b", label: "Sites", count: 2 }]} />
        <TabPanel idBase="t" value="a"><p>Summary content</p></TabPanel>
      </main>,
    );
    await expectNoViolations();
  });

  it("on a list page's table", async () => {
    nav.reset("/customers");
    mockFetch([{ match: "/api/cloud/v1/customers?status=active", body: { items: [CUSTOMER] } }]);
    renderAs(PLATFORM_ME, <main><CustomersPage /></main>);
    await screen.findByRole("table", { name: "Customers" });
    await expectNoViolations();
  });

  it("on a detail page with its summary and the sites table", async () => {
    nav.reset("/customers/c1", "tab=sites");
    mockFetch([
      { match: "/api/cloud/v1/customers/c1", method: "GET", body: CUSTOMER },
      { match: "/api/cloud/v1/customers/c1/sites", method: "GET", body: { items: [SITE] } },
    ]);
    renderAs(PLATFORM_ME, <main><CustomerPage params={{ id: "c1" }} /></main>);
    await screen.findByRole("table", { name: "Sites" });
    await expectNoViolations();
  });

  it("on the error banner with a trace id", async () => {
    render(<main><ErrorBanner err={new ApiError(500, { error: "internal", message: "Something failed.", trace_id: "req-123" })} /></main>);
    await expectNoViolations();
  });
});
