import { describe, expect, it } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

// Central's tokens are a GENERATED copy of design-system/tokens.css (tools/sync-design-tokens.mjs). This test
// fails when the copy drifts, so Central and Hotel Admin cannot become two palettes.
const here = dirname(fileURLToPath(import.meta.url));
const consoleRoot = join(here, "..");
const repoRoot = join(consoleRoot, "..");
const HEADER =
  "/* GENERATED from design-system/tokens.css by tools/sync-design-tokens.mjs. Do not edit here. */\n";

const copyPath = join(consoleRoot, "app", "tokens.css");
const sourcePath = process.env.VELONET_DESIGN_TOKENS ?? join(repoRoot, "design-system", "tokens.css");
const toolPath = join(repoRoot, "tools", "sync-design-tokens.mjs");

describe("design tokens", () => {
  it("the copy carries the generated header and the Central accent", () => {
    const copy = readFileSync(copyPath, "utf8");
    expect(copy.startsWith(HEADER)).toBe(true);
    expect(copy).toContain('[data-product="central"]');
    expect(copy).toContain('.dark[data-product="central"]');
  });

  it("the root layout selects the Central accent and the page title", () => {
    const layout = readFileSync(join(consoleRoot, "app", "layout.tsx"), "utf8");
    expect(layout).toContain('data-product="central"');
    expect(layout).toContain('title: "Velonet Central"');
    const globals = readFileSync(join(consoleRoot, "app", "globals.css"), "utf8");
    expect(globals).toContain('@import "@fontsource-variable/inter/index.css";');
    expect(globals).toContain('@import "./tokens.css";');
  });

  it.skipIf(!existsSync(sourcePath))("the copy is byte-identical to design-system/tokens.css", () => {
    const expected = HEADER + readFileSync(sourcePath, "utf8");
    expect(readFileSync(copyPath, "utf8")).toBe(expected);
  });

  it.skipIf(!existsSync(toolPath) || !existsSync(join(repoRoot, "design-system", "tokens.css")))(
    "node tools/sync-design-tokens.mjs --check reports no drift for cloud-admin",
    () => {
      const r = spawnSync(process.execPath, [toolPath, "--check"], { cwd: consoleRoot, encoding: "utf8" });
      // The tool checks every console; this test owns only Central's copy.
      expect(r.stderr).not.toMatch(/cloud-admin\/app\/tokens\.css differs/);
    },
  );
});
