import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, render, screen, fireEvent } from "@testing-library/react";
import axe from "axe-core";

// axe-core checks (WCAG 2.0-2.2 A/AA) on shared pieces and rendered screens. `color-contrast` is off (jsdom has no
// cascade);
// contrast is pinned by test/muted-text-contrast.test.ts.

const get = vi.fn();
const put = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: (...a: any[]) => get(...a),
      put: (...a: any[]) => put(...a),
      post: (...a: any[]) => post(...a),
      patch: vi.fn(),
      del: vi.fn(),
    },
  };
});
let search = new URLSearchParams("");
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => search,
  usePathname: () => "/dashboard",
}));
vi.mock("next/link", () => ({ default: ({ href, children, ...p }: any) => <a href={href} {...p}>{children}</a> }));
vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return { ...actual, useCapabilities: () => null };
});

import LoginPage from "@/app/login/page";
import { DataRetentionCard } from "@/app/(app)/backups/data-retention-card";
import { ToastProvider } from "@/components/ui/toast";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { Tabs, TabsList, TabsTrigger, TabsContent, Segmented } from "@/components/ui/tabs";
import { FilterChips, ResultsCount, SearchInput } from "@/components/ui/data";
import { Button } from "@/components/ui/button";
import { Explain } from "@/components/ui/tooltip";
import { IdleWarning } from "@/components/idle-warning";
import { Nav } from "@/components/nav";

async function expectNoViolations(root: Element = document.body) {
  const res = await axe.run(root, {
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"] },
    rules: { "color-contrast": { enabled: false } },
  });
  const found = res.violations.map(
    (v) => `${v.id}: ${v.help}\n  ${v.nodes.map((n) => n.target.join(" ")).join("\n  ")}`,
  );
  expect(found).toEqual([]);
}

beforeEach(() => {
  get.mockReset();
  put.mockReset();
  post.mockReset();
  search = new URLSearchParams("");
});

describe("axe: sign-in", () => {
  it("the login page", async () => {
    render(<LoginPage />);
    await screen.findByLabelText("Email or username");
    await expectNoViolations();
  });

  it("the login page after an idle sign-out, with an error shown", async () => {
    search = new URLSearchParams("next=%2Fstays&reason=expired");
    post.mockRejectedValue(new Error("Wrong email or password."));
    render(<LoginPage />);
    expect(await screen.findByText(/Your session ended/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Email or username"), { target: { value: "a" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "b" } });
    fireEvent.submit(screen.getByRole("button", { name: "Sign in" }).closest("form")!);
    await screen.findByRole("alert");
    await expectNoViolations();
  });
});

describe("axe: shared components", () => {
  it("a form of Fields: required, hinted and in error", async () => {
    const { container } = render(
      <form>
        <Field label="Name" required hint="Shown to guests.">
          <Input />
        </Field>
        <Field label="Days" error="Enter 1 to 10.">
          <Input type="number" />
        </Field>
        <Field label="Kind">
          <Select>
            <option>One</option>
          </Select>
        </Field>
        <Field label="Notes">
          <Textarea />
        </Field>
        <Button type="submit">Save</Button>
      </form>,
    );
    await expectNoViolations(container);
  });

  it("a table with an actions column and per-row actions", async () => {
    const { container } = render(
      <Table label="Rules">
        <THead>
          <TR>
            <TH>Name</TH>
            <TH>
              <span className="sr-only">Actions</span>
            </TH>
          </TR>
        </THead>
        <TBody>
          {["Lobby", "Pool"].map((n) => (
            <TR key={n}>
              <TD>{n}</TD>
              <TD>
                <Button size="sm" aria-label={`Edit ${n}`}>Edit</Button>
              </TD>
            </TR>
          ))}
        </TBody>
      </Table>,
    );
    await expectNoViolations(container);
  });

  it("an empty state and a results count", async () => {
    const { container } = render(
      <div>
        <ResultsCount count={0} noun="record" />
        <EmptyState title="No guest found" hint="Try another search." />
      </div>,
    );
    expect(screen.getAllByRole("status").length).toBe(2);
    await expectNoViolations(container);
  });

  it("tabs, a segmented switch, filter chips, a search box and a help button", async () => {
    const { container } = render(
      <TooltipProvider>
        <Tabs defaultValue="a">
          <TabsList aria-label="Sections">
            <TabsTrigger value="a">First</TabsTrigger>
            <TabsTrigger value="b">Second</TabsTrigger>
          </TabsList>
          <TabsContent value="a">First panel</TabsContent>
          <TabsContent value="b">Second panel</TabsContent>
        </Tabs>
        <Segmented label="View" value="a" onChange={() => {}} options={[{ value: "a", label: "Active" }, { value: "b", label: "Recent" }]} />
        <FilterChips label="Status" value="all" onChange={() => {}} options={[{ value: "all", label: "All", count: 3 }, { value: "used", label: "Used" }]} />
        <SearchInput label="Search stays" value="" onChange={() => {}} />
        <p>
          Places in use <Explain>How many devices hold a place now.</Explain>
        </p>
      </TooltipProvider>,
    );
    await expectNoViolations(container);
  });

  it("the navigation", async () => {
    render(
      <TooltipProvider>
        <Nav roles={["site_admin"]} email="a@b.c" onLogout={() => {}} />
      </TooltipProvider>,
    );
    await expectNoViolations();
  });

  it("the idle warning, open", async () => {
    render(
      <IdleWarning
        expiresAt={new Date(Date.now() + 60_000).toISOString()}
        onStay={vi.fn()}
        onCheck={vi.fn()}
        onSignOut={vi.fn()}
        onExpired={vi.fn()}
      />,
    );
    await screen.findByRole("alertdialog");
    await expectNoViolations();
  });
});

describe("axe: a settings card", () => {
  it("the guest data retention card, read and while changing", async () => {
    const bound = { min: 1, max: 3650, default: 30, unit: "days" };
    const keys = ["pms_stay_days", "client_activity_days", "inactive_identity_days", "expired_record_days",
      "audit_log_days", "sign_in_attempt_days", "log_days", "staff_note_days"];
    get.mockResolvedValue({
      settings: Object.fromEntries(keys.map((k) => [k, 30])),
      bounds: Object.fromEntries(keys.map((k) => [k, bound])),
      runs: [],
    });
    const { container } = render(
      <ToastProvider>
        <DataRetentionCard writable />
      </ToastProvider>,
    );
    await screen.findByText("Hotel stay data");
    await expectNoViolations(container);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Change" }));
    });
    expect(document.activeElement).toBe(screen.getByLabelText("Hotel stay data (days)"));
    await expectNoViolations(container);
  });
});
