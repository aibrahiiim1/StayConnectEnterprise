#!/usr/bin/env node
// Copies the canonical Velonet design tokens into each admin console.
//
//   node tools/sync-design-tokens.mjs          write the copies
//   node tools/sync-design-tokens.mjs --check  exit 1 if any copy differs (used by the consoles' unit tests)
//
// Each console is its own Next.js project and cannot import a file outside its directory at build time, so
// the tokens are COPIED rather than referenced. The copy is generated, carries a header saying so, and is
// compared byte-for-byte by tests, so the two consoles cannot drift into two palettes.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const source = readFileSync(join(root, "design-system", "tokens.css"), "utf8");
const HEADER =
  "/* GENERATED from design-system/tokens.css by tools/sync-design-tokens.mjs. Do not edit here. */\n";
const expected = HEADER + source;
const targets = ["hotel-admin/app/tokens.css", "cloud-admin/app/tokens.css"];

const check = process.argv.includes("--check");
let drift = 0;
for (const rel of targets) {
  const path = join(root, rel);
  let current = "";
  try {
    current = readFileSync(path, "utf8");
  } catch {}
  if (current === expected) continue;
  if (check) {
    console.error(`design tokens: ${rel} differs from design-system/tokens.css`);
    drift++;
  } else {
    writeFileSync(path, expected);
    console.log(`design tokens: wrote ${rel}`);
  }
}
process.exit(drift ? 1 : 0);
