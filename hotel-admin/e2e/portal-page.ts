import { readFileSync } from "node:fs";
import { resolve } from "node:path";

// THE REAL SIGN-IN PAGE, FOR TESTS THAT NEED A BROWSER TO RUN IT.
//
// Both the settings-preview spec and the language-detection spec need the page portald actually serves. They
// read it out of the Go source rather than copying it, so a change to the portal is a change to what these
// tests exercise.
//
// THE TEMPLATE IS NOT HTML UNTIL IT IS RENDERED. It carries Go template actions — the two client-address
// conditionals and, since the shipped wording moved into languages.go, {{.Languages}} and {{.Strings}}. A
// harness that served the raw template left `var LANGS = {{.Languages}};` in the page, which is a syntax
// error that kills the entire script block: no tabs, no branding, no language selection. Six tests failed
// with six different symptoms and one cause. Rendering happens here, once.

const DIR = resolve(__dirname, "..", "..", "data-plane", "cmd", "portald");

export type ShippedWording = {
  languages: { code: string; label: string; rtl?: boolean }[];
  strings: Record<string, Record<string, string>>;
};

/** The shipped wording, parsed out of languages.go — the same map the appliance compiles in. */
export function shippedWording(): ShippedWording {
  // languages.go holds the sign-in page's words and languages_pages.go every page after it and the server's
  // messages; portald merges the two into one dictionary at start-up, and so does this.
  const src =
    readFileSync(resolve(DIR, "languages.go"), "utf8") + "\n" + readFileSync(resolve(DIR, "languages_pages.go"), "utf8");
  const languages: ShippedWording["languages"] = [];
  for (const m of src.matchAll(/\{Code: "([a-z]{2})", Label: "([^"]+)"(, RTL: true)?\}/g)) {
    languages.push({ code: m[1], label: m[2], ...(m[3] ? { rtl: true } : {}) });
  }
  const strings: ShippedWording["strings"] = {};
  for (const block of src.matchAll(/\t"([a-z]{2})": \{\n([\s\S]*?)\n\t\},/g)) {
    const d: Record<string, string> = {};
    // `":\s*"` and not `": "`: gofmt pads a map literal's values into a column, so the separator is one
    // space in some entries and nine in others. A parser that assumed one silently produced six EMPTY
    // dictionaries, which rendered as a portal that had lost every translation.
    for (const km of block[2].matchAll(/"([a-z][a-zA-Z0-9.]*)":\s*"((?:[^"\\]|\\.)*)"/g)) {
      d[km[1]] = km[2].replace(/\\"/g, '"').replace(/\\\\/g, "\\");
    }
    strings[block[1]] = { ...(strings[block[1]] ?? {}), ...d };
  }
  if (!languages.length || languages.some((l) => !Object.keys(strings[l.code] ?? {}).length)) {
    // Every offered language must have arrived with words. Checking only that the MAP is non-empty is what
    // let the empty-dictionary bug through.
    throw new Error("languages.go parsed to nothing usable; this helper is not reading what it thinks it is");
  }
  return { languages, strings };
}

/** The landing page as portald renders it for an arriving device it has no ARP entry for, with no published
 *  design.
 *
 *  RENDERED BY PORTALD ITSELF, not imitated here. The page is assembled from shared Go pieces and carries
 *  template actions a regular expression cannot render faithfully, so data-plane/cmd/portald renders it with
 *  the real handler into e2e/fixtures/portal-landing.html, and a Go test (TestE2EPortalFixtures) fails when that
 *  fixture is stale. `template` is what portald stamps onto <html> from the PUBLISHED design; the page's own
 *  script then re-applies whatever /api/branding answers. The nonce is the fixed placeholder "e2e-nonce": these
 *  specs serve the page through page.route with no Content-Security-Policy header, so it is inert here. */
export function portalHTML(template = "classic"): string {
  const html = readFileSync(resolve(__dirname, "fixtures", "portal-landing.html"), "utf8");
  if (html.includes("{{")) {
    throw new Error("the portal fixture contains unrendered template actions; regenerate it from data-plane");
  }
  // Only the document element's attribute: the first occurrence is <html>.
  return html.replace(/data-template="[^"]*"/, `data-template="${template}"`);
}
