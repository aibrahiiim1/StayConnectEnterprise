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
  const src = readFileSync(resolve(DIR, "languages.go"), "utf8");
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
    strings[block[1]] = d;
  }
  if (!languages.length || languages.some((l) => !Object.keys(strings[l.code] ?? {}).length)) {
    // Every offered language must have arrived with words. Checking only that the MAP is non-empty is what
    // let the empty-dictionary bug through.
    throw new Error("languages.go parsed to nothing usable; this helper is not reading what it thinks it is");
  }
  return { languages, strings };
}

/** The landing page as portald renders it for a device it has no ARP entry for. */
export function portalHTML(): string {
  const src = readFileSync(resolve(DIR, "templates.go"), "utf8");
  const tmpl = src.match(/const landingHTML = `([\s\S]*?)`\n/);
  if (!tmpl) throw new Error("the landing template could not be found in templates.go");
  const shipped = shippedWording();
  const html = tmpl[1]
    .replace(/\{\{if \.ClientIP\}\}\{\{\.ClientIP\}\}\{\{else\}\}([^{]*)\{\{end\}\}/g, "$1")
    .replace(/\{\{if \.ClientMAC\}\}\{\{\.ClientMAC\}\}\{\{else\}\}([^{]*)\{\{end\}\}/g, "$1")
    // The refusal banner, which landing() fills only when it has something to tell the guest. These specs
    // exercise the page a guest meets on ARRIVAL, so the block is dropped exactly as html/template drops it
    // when .Error is empty — rendering an empty banner here would put markup on the page that a real first
    // visit never carries. The Go tests cover the branch where there IS a message.
    .replace(/\{\{if \.Error\}\}[\s\S]*?\{\{end\}\}/g, "")
    .replace(/\{\{\.Languages\}\}/g, JSON.stringify(shipped.languages))
    .replace(/\{\{\.Strings\}\}/g, JSON.stringify(shipped.strings));
  if (html.includes("{{")) {
    // A template action nobody rendered is a syntax error waiting to happen inside a <script>. Fail here,
    // where the message names the cause, rather than in six tests that each report a different symptom.
    const left = html.match(/\{\{[^}]*\}\}/g);
    throw new Error(`the landing template still contains unrendered actions: ${left?.join(", ")}`);
  }
  return html;
}
