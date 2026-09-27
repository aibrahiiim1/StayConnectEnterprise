import { describe, expect, it } from "vitest";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import config, { RETIRED_ROUTES } from "../next.config.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

type Redirect = { source: string; destination: string; permanent: boolean };
type Rewrite = { source: string; destination: string };

describe("old addresses (docs/CENTRAL_CONTROL_PLANE.md §2)", () => {
  const expected: Record<string, string> = {
    "/dashboard": "/overview",
    "/tenants": "/customers",
    "/sites": "/customers",
    "/onboarding": "/appliances?activation=waiting",
    "/operators": "/system/team",
    "/security": "/system/security-alerts",
    "/certificates": "/system/trust",
    "/assignment-keys": "/system/trust",
    "/backup-health": "/system/backup-health",
    "/audit": "/system/audit",
    "/commercial": "/licenses",
    "/subscription": "/licenses",
  };

  it("every retired route answers a permanent redirect to its replacement", async () => {
    const redirects: Redirect[] = await config.redirects();
    for (const [from, to] of Object.entries(expected)) {
      const r = redirects.find((x) => x.source === from);
      expect(r, from).toBeDefined();
      expect(r!.destination, from).toBe(to);
      expect(r!.permanent, from).toBe(true);
      const deeper = redirects.find((x) => x.source === `${from}/:rest*`);
      expect(deeper?.destination, `${from}/…`).toBe(to);
    }
    expect(Object.fromEntries(RETIRED_ROUTES)).toEqual(expected);
  });

  it("the old page files are gone, so nothing can render at an old address", () => {
    for (const from of Object.keys(expected)) {
      expect(existsSync(join(root, "app", "(app)", from.slice(1))), from).toBe(false);
    }
  });

  it("/ goes to the Overview and /system to its first page", async () => {
    const redirects: Redirect[] = await config.redirects();
    expect(redirects.find((x) => x.source === "/")?.destination).toBe("/overview");
    expect(redirects.find((x) => x.source === "/system")?.destination).toBe("/system/security-alerts");
  });

  it("proxies only the two API families", async () => {
    const rewrites: Rewrite[] = await config.rewrites();
    expect(rewrites.map((r) => r.source)).toEqual(["/api/v1/:path*", "/api/cloud/:path*"]);
    expect(rewrites.every((r) => /\/(v1|cloud)\/:path\*$/.test(r.destination))).toBe(true);
  });

  it("every new route exists", () => {
    for (const p of [
      "overview", "customers", "customers/[id]", "appliances", "appliances/[id]", "licenses",
      "system/security-alerts", "system/trust", "system/audit", "system/team", "system/backup-health",
    ]) {
      expect(existsSync(join(root, "app", "(app)", p, "page.tsx")), p).toBe(true);
    }
  });
});
