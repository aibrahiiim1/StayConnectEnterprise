import { describe, expect, it, vi, beforeEach } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { screen } from "@testing-library/react";
import { PLATFORM_ME, SUPPORT_ME, TENANT_ME, renderAs } from "./helpers";
import { nav } from "./navigation";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import { Nav, NAV_ITEMS, SYSTEM_PAGES, activeNavHref, crumbs } from "@/components/nav";

beforeEach(() => nav.reset("/licenses"));

describe("Central navigation", () => {
  it("has exactly five destinations, in order", () => {
    expect(NAV_ITEMS.map((i) => [i.label, i.href])).toEqual([
      ["Overview", "/overview"],
      ["Customers", "/customers"],
      ["Appliances", "/appliances"],
      ["Licenses", "/licenses"],
      ["System", "/system"],
    ]);
  });

  it("keeps the rarely used pages under System", () => {
    expect(SYSTEM_PAGES.map((p) => p.label)).toEqual(["Security alerts", "Trust & keys", "Audit log", "Team", "Backup health"]);
    expect(SYSTEM_PAGES.every((p) => p.href.startsWith("/system/"))).toBe(true);
  });

  it("marks the current destination, including on a detail page", () => {
    expect(activeNavHref("/licenses")).toBe("/licenses");
    expect(activeNavHref("/appliances/a1")).toBe("/appliances");
    expect(activeNavHref("/system/trust")).toBe("/system");
    expect(activeNavHref("/dashboard")).toBeNull();
  });

  it("names the parent in the top bar", () => {
    expect(crumbs("/overview")).toEqual({ label: "Overview" });
    expect(crumbs("/appliances/a1")).toEqual({ parent: { href: "/appliances", label: "Appliances" }, label: "Appliance" });
    expect(crumbs("/system/team")).toEqual({ parent: { href: "/system", label: "System" }, label: "Team" });
  });

  it("shows a platform admin all five, with the current page marked", () => {
    renderAs(PLATFORM_ME, <Nav email={PLATFORM_ME.email} onLogout={() => {}} />);
    const links = screen.getAllByRole("link").filter((l) => NAV_ITEMS.some((i) => i.href === l.getAttribute("href")));
    expect(links.map((l) => l.textContent)).toEqual(["Overview", "Customers", "Appliances", "Licenses", "System"]);
    expect(screen.getByRole("link", { name: "Licenses" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByText("Central")).toBeInTheDocument();
    expect(screen.getByText("OneGate by Semantics")).toBeInTheDocument();
  });

  it("shows support staff System too", () => {
    renderAs(SUPPORT_ME, <Nav email={SUPPORT_ME.email} onLogout={() => {}} />);
    expect(screen.getByRole("link", { name: "System" })).toBeInTheDocument();
  });

  it("shows a customer's user their own customer and no System", () => {
    renderAs(TENANT_ME, <Nav email={TENANT_ME.email} onLogout={() => {}} />);
    expect(screen.queryByRole("link", { name: "System" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Customer" })).toHaveAttribute("href", "/customers");
    expect(screen.getByText("Semantics")).toBeInTheDocument();
  });

  it("has no customer selector anywhere", () => {
    const { container } = renderAs(PLATFORM_ME, <Nav email={PLATFORM_ME.email} onLogout={() => {}} />);
    expect(container.querySelector("#customer-context")).toBeNull();
    expect(screen.queryByLabelText(/customer context/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });
});

describe("no global customer context in the source", () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), "..");
  const walk = (dir: string, out: string[] = []): string[] => {
    for (const name of readdirSync(dir)) {
      const p = join(dir, name);
      if (statSync(p).isDirectory()) walk(p, out);
      else if (/\.(tsx?|mjs)$/.test(name)) out.push(p);
    }
    return out;
  };
  const files = [...walk(join(root, "app")), ...walk(join(root, "components")), ...walk(join(root, "lib"))];

  it("nothing reads or writes sc.customerContext, and the provider and selector are gone", () => {
    for (const f of files) {
      const code = readFileSync(f, "utf8");
      expect(code, f).not.toContain("sc.customerContext");
      expect(code, f).not.toMatch(/customer-context|CustomerProvider|useCustomer\b|CustomerSelector|customer-selector/);
    }
  });
});
