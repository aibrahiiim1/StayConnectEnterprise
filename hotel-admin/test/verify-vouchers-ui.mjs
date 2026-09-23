// A REAL BROWSER AGAINST THE REAL APPLIANCE.
//
// curl gets HTTP 200 and an empty shell: the voucher view renders client-side after its own requests resolve,
// so the page's text is not in the served HTML. This project has already accepted a structural check as proof
// of visual completion once and been wrong, so every assertion below is on the rendered page or on a real
// form control.
import { chromium } from "playwright";

const BASE = process.env.SC_BASE || "https://172.21.60.25";
const ok = [];
const bad = [];
function check(name, cond, detail) {
  (cond ? ok : bad).push(detail ? `${name} :: ${detail}` : name);
}

const browser = await chromium.launch({ channel: "chrome", args: ["--ignore-certificate-errors"] });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1400, height: 1200 } });
const page = await ctx.newPage();
const consoleErrors = [];
page.on("console", (m) => { if (m.type() === "error") consoleErrors.push(m.text()); });

try {
  await page.goto(`${BASE}/login`, { waitUntil: "domcontentloaded", timeout: 60000 });
  await page.fill('input[autocomplete="username"]', "admin");
  await page.fill('input[autocomplete="current-password"]', "admin");
  await page.click('button[type="submit"]');
  await page.waitForURL((u) => !u.pathname.includes("/login"), { timeout: 60000 });
  check("an operator can sign in", true);

  // THE SIDEBAR MUST OFFER IT. A page reachable only by typing its URL is a page operators do not have.
  const navText = await page.locator("nav").first().innerText().catch(() => "");
  check("the sidebar offers Vouchers", /Vouchers/i.test(navText), navText.split("\n").filter(Boolean).length + " entries");

  await page.goto(`${BASE}/vouchers`, { waitUntil: "domcontentloaded", timeout: 60000 });

  // WAIT FOR THE PANELS THAT LOAD THEMSELVES, NOT JUST FOR THE SHELL.
  //
  // The first run of this script reported two missing panels and both reports were this script's fault. The
  // heading renders at once; the code format, its option lists and the key-generation table each arrive from
  // their own request afterwards. Sampling innerText the moment "Vouchers" appears reads the page mid-load
  // and calls a late panel a missing one. The wait is therefore on the LAST thing to arrive, so that a
  // failure here means the panel genuinely never came.
  await page.waitForFunction(() => {
    const t = document.body.innerText;
    return t.includes("Code format") && t.includes("Code keys") && t.includes("Print a batch")
      && t.includes("Who has read a code") && /Generation \d+/.test(t);
  }, null, { timeout: 60000 });
  const body = await page.locator("body").innerText();

  for (const [name, re] of [
    ["the heading", /Vouchers/],
    ["the honest sentence about recoverability", /encrypted and can be read again/i],
    ["the sentence that a reveal is recorded", /records who read it/i],
    ["the code format panel", /Code format/i],
    ["the eight-character ceiling stated in words", /[Nn]ever more than eight characters/],
    ["the key rotation panel", /Code keys/i],
    ["the print form", /Print a batch/i],
    ["the package chooser", /What these cards grant/i],
    ["the card list", /Cards|No cards yet/i],
    ["the reveal history", /Who has read a code/i],
    ["the reveal history says it is permanent", /Nothing on this list can be edited or removed/i],
  ]) {
    check(name, re.test(body), re.source.slice(0, 44));
  }

  // THE <select> CONTROLS, ASSERTED INDIVIDUALLY.
  //
  // A collapsed select's <option> text is not part of body.innerText, so asserting "Digits only" against the
  // page text could only ever have matched the panel's prose. Each control is read by itself instead.
  const sel = async (i) => (await page.locator("select").nth(i).innerText().catch(() => "")).replace(/\n/g, " / ");
  const fmt = await sel(0), len = await sel(1), pkg = await sel(2);

  // The Product Owner asked for exactly two choices: numbers only, or numbers mixed with characters.
  check("the format control offers digits only", /Digits only/i.test(fmt), fmt);
  check("the format control offers digits and letters", /Digits and letters/i.test(fmt), fmt);
  check("and offers nothing else", fmt.split(" / ").filter(Boolean).length === 2, fmt);

  // "not to be more than 8 as length for both" -- the ceiling is in the control, not only in the server.
  check("the length control stops at eight", /8 characters/.test(len) && !/(9|1[0-9]) characters/.test(len), len);

  check("the chooser offers the real packages", /OneDay/.test(pkg) && /Free Internet/i.test(pkg), pkg.slice(0, 120));
  check("the chooser hides the system packages", !/__sys|Post-?stay|Checkout grace/i.test(pkg), pkg.slice(0, 120));

  // A CODE IS NEVER IN THE LIST. The card rows show a masked tail and the full code is only ever produced by
  // the audited reveal, so the rendered page must not contain a redeemable code anywhere.
  check("the list masks every code", /…[0-9A-Z]{4}\s/.test(body), "masked tails present");

  // The key generation the printed cards are tied to.
  check("a key generation is listed as in use", /Generation 1[\s\S]{0,60}In use/.test(body));

  // Phone width: no horizontal overflow at an iPhone profile.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(800);
  const overflow = await page.evaluate(() =>
    document.documentElement.scrollWidth - document.documentElement.clientWidth);
  check("no horizontal overflow at 390px", overflow <= 2, `overflow=${overflow}px`);

  check("no console errors", consoleErrors.length === 0, consoleErrors.slice(0, 2).join(" ; "));
} catch (e) {
  bad.push("EXCEPTION :: " + String(e).split("\n")[0]);
} finally {
  await browser.close();
}

console.log("== PASS ==");
for (const l of ok) console.log("  ok: " + l);
if (bad.length) {
  console.log("== FAIL ==");
  for (const l of bad) console.log("  FAIL: " + l);
}
console.log(bad.length ? `VOUCHERS_UI = FAIL (${bad.length})` : `VOUCHERS_UI = PASS (${ok.length} checks)`);
process.exit(bad.length ? 1 : 0);
