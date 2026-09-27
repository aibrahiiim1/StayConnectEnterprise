import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, within } from "@testing-library/react";
import { mockFetch, PLATFORM_ME, TENANT_ME, renderAs } from "./helpers";
import { nav } from "./navigation";
import { OVERVIEW } from "./fixtures";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import OverviewPage from "@/app/(app)/overview/page";

beforeEach(() => nav.reset("/overview"));

describe("Overview", () => {
  it("shows every count as a link to the filtered list", async () => {
    mockFetch([{ match: "/api/cloud/v1/overview", body: OVERVIEW }]);
    renderAs(PLATFORM_ME, <OverviewPage />);

    expect(screen.getByRole("heading", { level: 1, name: "Overview" })).toBeInTheDocument();
    const waiting = await screen.findByRole("link", { name: /^Waiting for activation\s*1/ });
    expect(waiting).toHaveAttribute("href", "/appliances?activation=waiting");
    expect(screen.getByRole("link", { name: /^Activated\s*1/ })).toHaveAttribute("href", "/appliances?activation=activated");
    expect(screen.getByRole("link", { name: /^Never connected\s*1/ })).toHaveAttribute("href", "/appliances?connection=never");
    expect(screen.getByRole("link", { name: /^Expired\s*0/ })).toHaveAttribute("href", "/licenses?state=expired");
    expect(screen.getByRole("link", { name: /^No license\s*1/ })).toHaveAttribute("href", "/appliances?license=none");
    expect(screen.getByRole("link", { name: /1\s*customer/ })).toHaveAttribute("href", "/customers");
    expect(screen.getByRole("link", { name: /2\s*appliances/ })).toHaveAttribute("href", "/appliances");
  });

  it("lists what needs attention with a direct action", async () => {
    mockFetch([{ match: "/api/cloud/v1/overview", body: OVERVIEW }]);
    renderAs(PLATFORM_ME, <OverviewPage />);
    const list = await screen.findByRole("list", { name: "Items that need attention" });
    const item = within(list).getByText("Waiting for activation").closest("li")!;
    expect(item).toHaveTextContent("OG-0002");
    expect(within(item).getByRole("link", { name: /Activate/ })).toHaveAttribute("href", "/appliances/a-new");
  });

  it("offers a customer's user a View link rather than an action they cannot take", async () => {
    mockFetch([{ match: "/api/cloud/v1/overview", body: OVERVIEW }]);
    renderAs(TENANT_ME, <OverviewPage />);
    const list = await screen.findByRole("list", { name: "Items that need attention" });
    expect(within(list).queryByRole("link", { name: /Activate/ })).not.toBeInTheDocument();
    expect(within(list).getByRole("link", { name: /View/ })).toHaveAttribute("href", "/appliances/a-new");
  });

  it("says so when nothing needs attention", async () => {
    mockFetch([{ match: "/api/cloud/v1/overview", body: { ...OVERVIEW, attention: [] } }]);
    renderAs(PLATFORM_ME, <OverviewPage />);
    expect(await screen.findByText("Nothing needs attention")).toBeInTheDocument();
  });
});
