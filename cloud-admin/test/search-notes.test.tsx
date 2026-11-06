import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, SUPPORT_ME, renderAs } from "./helpers";
import { nav } from "./navigation";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import { GlobalSearch } from "@/components/global-search";
import { NotesCard } from "@/components/notes";
import { TagsBar } from "@/components/tags";

beforeEach(() => nav.reset("/overview"));

const APP = "11111111-2222-3333-4444-555555555555";
const empty = { customers: [], sites: [], appliances: [], licenses: [], notes: [] };

describe("Global search", () => {
  it("opens with Ctrl K, groups results and opens the chosen one with Enter", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([{
      match: /\/api\/cloud\/v1\/search\?q=/, method: "GET",
      body: {
        ...empty, query: "sn12",
        appliances: [{ id: APP, title: "SN1234", subtitle: "Hotel Group . Cairo", href: `/appliances/${APP}` }],
        notes: [{ id: "n1", title: "Note on appliance SN1234", subtitle: "WAN cable replaced", href: `/appliances/${APP}` }],
      },
    }]);
    renderAs(PLATFORM_ME, <GlobalSearch />);
    await user.keyboard("{Control>}k{/Control}");
    const box = await screen.findByRole("combobox", { name: "Search" });
    await user.type(box, "sn12");
    expect(await screen.findByText("SN1234")).toBeInTheDocument();
    const list = screen.getByRole("listbox", { name: "Results" });
    expect(within(list).getByRole("group", { name: "Appliances" })).toBeInTheDocument();
    expect(within(list).getByRole("group", { name: "Notes" })).toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(nav.router.push).toHaveBeenCalledWith(`/appliances/${APP}`);
    expect(calls.at(-1)?.url).toContain("q=sn12");
  });

  it("does not search for one character and says when nothing matches", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([{ match: /\/api\/cloud\/v1\/search\?q=/, method: "GET", body: { ...empty, query: "zz" } }]);
    renderAs(PLATFORM_ME, <GlobalSearch />);
    await user.click(screen.getByRole("button", { name: "Search Central" }));
    const box = await screen.findByRole("combobox", { name: "Search" });
    await user.type(box, "z");
    expect(screen.getByText(/Type at least 2 characters/)).toBeInTheDocument();
    expect(calls.length).toBe(0);
    await user.type(box, "z");
    expect(await screen.findByText(/Nothing found for/)).toBeInTheDocument();
  });
});

const note = (over: Record<string, unknown> = {}) => ({
  id: "n1", body: "WAN cable replaced on Monday", pinned: false, author_id: "op-1", author_name: "Admin",
  author_email: "admin@example.test", created_at: "2026-11-05T10:00:00Z", updated_at: "2026-11-05T10:00:00Z",
  edited: false, can_edit: true, can_delete: true, ...over,
});

describe("Notes", () => {
  it("lists notes and adds a new one for the appliance", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { match: `/api/cloud/v1/notes?appliance_id=${APP}`, method: "GET", body: { items: [note({ pinned: true })], can_write: true } },
      { match: "/api/cloud/v1/notes", method: "POST", status: 201, body: note({ id: "n2", body: "Second" }) },
    ]);
    renderAs(PLATFORM_ME, <NotesCard subject={{ appliance_id: APP }} what="appliance" />);
    expect(await screen.findByText("WAN cable replaced on Monday")).toBeInTheDocument();
    expect(screen.getByText("Pinned")).toBeInTheDocument();
    expect(screen.getByText("Never write guest names or details.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("New note on this appliance"), "Second");
    await user.click(screen.getByRole("button", { name: "Add note" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ appliance_id: APP, body: "Second", pinned: false });
  });

  it("support staff read notes but cannot write, edit or delete them", async () => {
    mockFetch([{
      match: `/api/cloud/v1/notes?appliance_id=${APP}`, method: "GET",
      body: { items: [note({ can_edit: false, can_delete: false, author_id: null, author_name: null, author_email: null })], can_write: false },
    }]);
    renderAs(SUPPORT_ME, <NotesCard subject={{ appliance_id: APP }} what="appliance" />);
    expect(await screen.findByText("WAN cable replaced on Monday")).toBeInTheDocument();
    expect(screen.getByText("Removed member")).toBeInTheDocument();
    expect(screen.queryByLabelText("New note on this appliance")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  });

  it("asks before deleting a note", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { match: `/api/cloud/v1/notes?appliance_id=${APP}`, method: "GET", body: { items: [note()], can_write: true } },
      { match: "/api/cloud/v1/notes/n1", method: "DELETE", status: 204, body: null },
    ]);
    renderAs(PLATFORM_ME, <NotesCard subject={{ appliance_id: APP }} what="appliance" />);
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(await screen.findByText("Delete this note?")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Delete note" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
  });
});

describe("Tags", () => {
  it("shows the record's tags and lets an admin add an existing one", async () => {
    const user = userEvent.setup();
    const vip = { id: "t1", name: "VIP", color: "accent", customers: 1, appliances: 0 };
    const pilot = { id: "t2", name: "Pilot", color: "info", customers: 0, appliances: 0 };
    const { calls } = mockFetch([
      { match: `/api/cloud/v1/tags/links?appliance_id=${APP}`, method: "GET", body: { items: [vip], can_write: true } },
      { match: "/api/cloud/v1/tags", method: "GET", body: { items: [pilot, vip] } },
      { match: "/api/cloud/v1/tags/links", method: "PUT", body: { items: [pilot, vip] } },
    ]);
    renderAs(PLATFORM_ME, <TagsBar subject={{ appliance_id: APP }} what="appliance" />);
    expect(await screen.findByText("VIP")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Edit tags/ }));
    const dialog = await screen.findByRole("dialog");
    await user.click(await within(dialog).findByLabelText("Pilot"));
    await user.click(within(dialog).getByRole("button", { name: "Save tags" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!.body as { appliance_id: string; tag_ids: string[] };
    expect(put.appliance_id).toBe(APP);
    expect([...put.tag_ids].sort()).toEqual(["t1", "t2"]);
  });

  it("shows nothing to support staff when a record has no tags", async () => {
    mockFetch([{ match: `/api/cloud/v1/tags/links?appliance_id=${APP}`, method: "GET", body: { items: [], can_write: false } }]);
    const { container } = renderAs(SUPPORT_ME, <TagsBar subject={{ appliance_id: APP }} what="appliance" />);
    await waitFor(() => expect(container.querySelector('[aria-label="Tags"]')).toBeNull());
    expect(screen.queryByRole("button", { name: /tags/i })).not.toBeInTheDocument();
  });
});
