import { readFileSync } from "node:fs";
import { resolve } from "node:path";

// Serve portald's rendered fixture; raw Go template actions would break the portal script.

const DIR = resolve(__dirname, "..", "..", "data-plane", "cmd", "portald");

export type ShippedWording = {
  languages: { code: string; label: string; rtl?: boolean }[];
  strings: Record<string, Record<string, string>>;
};

export function shippedWording(): ShippedWording {
  // Merge both language files as portald does at startup.
  const src =
    readFileSync(resolve(DIR, "languages.go"), "utf8") + "\n" + readFileSync(resolve(DIR, "languages_pages.go"), "utf8");
  const languages: ShippedWording["languages"] = [];
  for (const m of src.matchAll(/\{Code: "([a-z]{2})", Label: "([^"]+)"(, RTL: true)?\}/g)) {
    languages.push({ code: m[1], label: m[2], ...(m[3] ? { rtl: true } : {}) });
  }
  const strings: ShippedWording["strings"] = {};
  for (const block of src.matchAll(/\t"([a-z]{2})": \{\n([\s\S]*?)\n\t\},/g)) {
    const d: Record<string, string> = {};
    // gofmt pads map values, so match variable whitespace after colons.
    for (const km of block[2].matchAll(/"([a-z][a-zA-Z0-9.]*)":\s*"((?:[^"\\]|\\.)*)"/g)) {
      d[km[1]] = km[2].replace(/\\"/g, '"').replace(/\\\\/g, "\\");
    }
    strings[block[1]] = { ...(strings[block[1]] ?? {}), ...d };
  }
  if (!languages.length || languages.some((l) => !Object.keys(strings[l.code] ?? {}).length)) {
    // Check each language; a nonempty outer map can still hold empty dictionaries.
    throw new Error("languages.go parsed to nothing usable; this helper is not reading what it thinks it is");
  }
  return { languages, strings };
}

/** Use portald's fixture so browser tests exercise rendered Go templates. */
export function portalHTML(template = "classic"): string {
  const html = readFileSync(resolve(__dirname, "fixtures", "portal-landing.html"), "utf8");
  if (html.includes("{{")) {
    throw new Error("the portal fixture contains unrendered template actions; regenerate it from data-plane");
  }
  return html.replace(/data-template="[^"]*"/, `data-template="${template}"`);
}
