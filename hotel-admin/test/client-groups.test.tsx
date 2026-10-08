import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// CLIENT GROUPS (docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §3, §9).
//
// Pinned here: the list reads the groups and says what each one matches in one line; creating a group posts
// the wire shape edged expects (a domain list, lower-cased, with the subdomain switch, and the optional
// reason); the server's validation message reaches the operator in the dialog they are looking at; and a group
// that a package still names as its audience is NOT deleted, with the server's own words saying which packages.

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
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));

import ClientGroupsPage from "@/app/(app)/client-groups/page";
import { ruleSummary } from "@/app/(app)/client-groups/page";

const list = <T,>(data: T[]) => ({ data, meta: { has_more: false } });

const PARTNERS = {
  id: "g-par", name: "Partners", description: "Agreed partner pricing", priority: 50, enabled: true,
  rules: [
    { id: "r1", type: "EMAIL_DOMAIN", value: { domains: ["company.com", "company.ae"] } },
    { id: "r2", type: "IDP_TENANT", value: { provider: "microsoft", tenant_ids: ["11111111-2222-3333-4444-555555555555"] } },
  ],
  created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-01T10:00:00Z",
};

function routes(groups: unknown[], changes: unknown[] = [], roles = ["site_admin"]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/client-groups") return Promise.resolve(list(groups));
    if (path === "/client-groups/changes") return Promise.resolve(list(changes));
    return Promise.resolve(list([]));
  });
}

beforeEach(() => { get.mockReset(); post.mockReset(); patch.mockReset(); del.mockReset(); });

describe("the list", () => {
  it("renders each group with its priority and what it matches, in one line", async () => {
    routes([PARTNERS]);
    render(<ClientGroupsPage />);
    expect(await screen.findByText("Partners")).toBeInTheDocument();
    expect(screen.getByText("company.com, company.ae · Microsoft tenant")).toBeInTheDocument();
    expect(screen.getByText("50")).toBeInTheDocument();
    expect(screen.getByText("On")).toBeInTheDocument();
  });

  it("explains what groups are for when there are none, with the Employees / Partners / public example", async () => {
    routes([]);
    render(<ClientGroupsPage />);
    expect(await screen.findByText("No client groups yet")).toBeInTheDocument();
    expect(screen.getByText(/Employees/)).toBeInTheDocument();
    expect(screen.getByText(/Partners/)).toBeInTheDocument();
    expect(screen.getByText(/public clients seeing the normal packages/)).toBeInTheDocument();
  });

  it("a read-only role sees the groups and no controls", async () => {
    routes([PARTNERS], [], ["front_office_operator"]);
    render(<ClientGroupsPage />);
    expect(await screen.findByText(/can see the client groups but not change them/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Add group/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Delete$/ })).toBeNull();
  });

  it("summarises every rule kind", () => {
    expect(ruleSummary([{ type: "EMAIL_DOMAIN", value: { domains: ["a.com"], include_subdomains: true } }])).toBe("a.com (and subdomains)");
    expect(ruleSummary([{ type: "IDP_HOSTED_DOMAIN", value: { provider: "google", domains: ["a.com"] } }])).toBe("Google Workspace a.com");
    expect(ruleSummary([])).toMatch(/matches nobody/);
  });
});

describe("creating a group", () => {
  it("posts the name, priority, rules and reason the way edged expects", async () => {
    routes([]);
    post.mockResolvedValue({ ...PARTNERS, id: "g-emp", name: "Employees" });
    render(<ClientGroupsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Add group/ }));
    const dialog = await screen.findByRole("dialog");

    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "Employees" } });
    fireEvent.change(within(dialog).getByLabelText(/^Priority/), { target: { value: "10" } });
    // The first rule is a verified-email-domain rule, with its assurance level said beside it.
    expect((screen.getByTestId("group-rule-type-0") as HTMLSelectElement).value).toBe("EMAIL_DOMAIN");
    expect(within(dialog).getByText(/Anyone who can receive mail at the domain qualifies/)).toBeInTheDocument();
    fireEvent.change(screen.getByTestId("group-rule-list-0"), { target: { value: "Company.com\ncompany.ae, company.com" } });
    fireEvent.click(within(dialog).getByRole("switch", { name: "Rule 1: include subdomains" }));
    fireEvent.change(within(dialog).getByLabelText(/Reason for this change/), { target: { value: "Staff benefit" } });

    fireEvent.click(within(dialog).getByRole("button", { name: /Create group/ }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0][0]).toBe("/client-groups");
    expect(post.mock.calls[0][1]).toEqual({
      name: "Employees", description: undefined, priority: 10, enabled: true,
      rules: [{ type: "EMAIL_DOMAIN", value: { domains: ["company.com", "company.ae"], include_subdomains: true } }],
      reason: "Staff benefit",
    });
  });

  it("offers the two organisation-account kinds with their stronger assurance", async () => {
    routes([]);
    render(<ClientGroupsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Add group/ }));
    const kind = screen.getByTestId("group-rule-type-0") as HTMLSelectElement;
    expect(Array.from(kind.options).map((o) => o.textContent)).toEqual([
      "Verified email address domain", "Microsoft organisation (Entra tenant)", "Google Workspace organisation",
    ]);
    fireEvent.change(kind, { target: { value: "IDP_TENANT" } });
    expect(screen.getByText(/signed in with a Microsoft account in your organisation's directory/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Rule 1: tenant ID/)).toBeInTheDocument();
  });

  it("shows the server's validation message, naming the rule, in the dialog", async () => {
    routes([]);
    const { ApiError } = await import("@/lib/api");
    post.mockRejectedValue(new (ApiError as any)(400, { error: "validation", message: "rule 1: \"not a domain\" is not a domain name" }));
    render(<ClientGroupsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Add group/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "X" } });
    fireEvent.change(screen.getByTestId("group-rule-list-0"), { target: { value: "not a domain" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Create group/ }));
    expect(await within(dialog).findByText(/rule 1: "not a domain" is not a domain name/)).toBeInTheDocument();
    // The dialog stays open with the operator's input, so it can be corrected rather than retyped.
    expect((within(dialog).getByLabelText(/^Name/) as HTMLInputElement).value).toBe("X");
  });

  it("refuses, before the server, a rule with nothing in it", async () => {
    routes([]);
    render(<ClientGroupsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Add group/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "X" } });
    // The textarea is required, so the browser refuses first; the form's own check is what the message says.
    (screen.getByTestId("group-rule-list-0") as HTMLTextAreaElement).removeAttribute("required");
    fireEvent.click(within(dialog).getByRole("button", { name: /Create group/ }));
    expect(await within(dialog).findByText(/Rule 1: enter at least one domain/)).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
  });
});

describe("deleting a group", () => {
  it("is refused while a package names it as its audience, and says which packages", async () => {
    routes([PARTNERS]);
    const { ApiError } = await import("@/lib/api");
    del.mockRejectedValue(new (ApiError as any)(409, {
      error: "in_use", message: "this group is the audience of: PARTNERWIFI, PARTNERPLUS. Change those packages first.",
    }));
    render(<ClientGroupsPage />);
    await screen.findByText("Partners");
    fireEvent.click(screen.getByRole("button", { name: /^Delete$/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Reason/), { target: { value: "ended" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /^Delete$/ }));
    expect(await within(dialog).findByText(/this group is the audience of: PARTNERWIFI, PARTNERPLUS\. Change those packages first\./)).toBeInTheDocument();
    expect(del).toHaveBeenCalledWith("/client-groups/g-par", { reason: "ended" });
    // Still listed: nothing was deleted.
    expect(screen.getByText("Partners")).toBeInTheDocument();
  });

  it("deletes an unused group and re-reads the list", async () => {
    routes([PARTNERS]);
    del.mockImplementation(() => { routes([]); return Promise.resolve(undefined); });
    render(<ClientGroupsPage />);
    await screen.findByText("Partners");
    fireEvent.click(screen.getByRole("button", { name: /^Delete$/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: /^Delete$/ }));
    await waitFor(() => expect(del).toHaveBeenCalledWith("/client-groups/g-par", undefined));
    expect(await screen.findByText("No client groups yet")).toBeInTheDocument();
  });
});

describe("history", () => {
  it("reads the change log and shows who, what and why", async () => {
    routes([PARTNERS], [
      { id: "c1", group_id: "g-par", action: "UPDATED", changed_by: "gm@site", reason: "Added company.ae", before: { name: "Partners" }, after: { name: "Partners" }, changed_at: "2026-10-02T10:00:00Z" },
      { id: "c2", group_id: "g-old", action: "DELETED", changed_by: "it@site", reason: "", before: { name: "Old partners" }, after: null, changed_at: "2026-10-01T10:00:00Z" },
    ]);
    render(<ClientGroupsPage />);
    await screen.findByText("Partners");
    // A Radix tab activates on mouse-down, not click.
    fireEvent.mouseDown(screen.getByRole("tab", { name: "History" }), { button: 0 });
    expect(await screen.findByText("Added company.ae")).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith("/client-groups/changes");
    expect(screen.getByText("Changed")).toBeInTheDocument();
    expect(screen.getByText("Deleted")).toBeInTheDocument();
    expect(screen.getByText("Old partners")).toBeInTheDocument();
    expect(screen.getByText("gm@site")).toBeInTheDocument();
  });
});
