// THE FIVE JOURNEYS, IN A REAL BROWSER, AGAINST THE REAL APPLIANCE.
//
// Not a mock. Structural tests were accepted as proof of visual completion once in this project and were
// wrong, so this drives the deployed Hotel Admin at 172.21.60.25 at a desktop and a phone viewport, reads
// what is actually on the screen, and writes screenshots.
import { chromium, devices } from "playwright";
import fs from "node:fs";

const BASE = "https://172.21.60.25";
const OUT = process.argv[2] || "/d/tmp/journey-shots";
fs.mkdirSync(OUT, { recursive: true });

const PAGES = [
  { slug: "appliance", path: "/appliance", journey: "1 Appliance & License",
    expect: [/Appliance/i], forbid: [] },
  { slug: "audit", path: "/audit", journey: "2 Activity",
    expect: [/Activity|Audit/i], forbid: [] },
  { slug: "backups", path: "/backups", journey: "3 Backups & Restore",
    expect: [/Backup/i], forbid: [],
    then: [{ slug: "storage", click: "Storage, retention and the nightly sweep",
             expect: [/Disk used/i, /Nightly sweep/i, /retained/i] }] },
  { slug: "internet-packages", path: "/internet-packages", journey: "4 Guest Activity",
    expect: [/Package/i], forbid: [],
    then: [{ slug: "guest-activity", click: "Guest activity", expect: [/Room|room/] }] },
  { slug: "usage", path: "/usage", journey: "5 Usage Explorer",
    expect: [/Usage|room|device/i], forbid: [],
    then: [{ slug: "by-device", click: "By device", expect: [/device|MAC/i] }] },
  // The old routes must still land somewhere sensible for a bookmarked operator.
  { slug: "redirect-license", path: "/license", journey: "1 bookmark compatibility",
    expect: [/Appliance/i], forbid: [], expectURL: /\/appliance/ },
  { slug: "redirect-enrollment", path: "/setup/enrollment", journey: "1 bookmark compatibility",
    expect: [/Appliance/i], forbid: [], expectURL: /\/appliance/ },
];

const results = [];

async function run(label, contextOpts, tag) {
  const browser = await chromium.launch({ channel: "chrome", headless: true,
    args: ["--ignore-certificate-errors"] });
  const ctx = await browser.newContext({ ...contextOpts, ignoreHTTPSErrors: true });
  const page = await ctx.newPage();

  const consoleErrors = [];
  page.on("console", (m) => { if (m.type() === "error") consoleErrors.push(m.text()); });
  const badResponses = [];
  page.on("response", (r) => {
    const u = r.url();
    if (u.includes("/api/edge/") && r.status() >= 400) badResponses.push(`${r.status()} ${u.replace(BASE, "")}`);
  });

  // sign in
  await page.goto(`${BASE}/login`, { waitUntil: "domcontentloaded", timeout: 60000 });
  await page.fill('input[autocomplete="username"]', "admin");
  await page.fill('input[autocomplete="current-password"]', "admin");
  await page.click('button[type="submit"]');
  await page.waitForURL((u) => !u.pathname.endsWith("/login"), { timeout: 60000 });

  for (const p of PAGES) {
    const r = { viewport: label, journey: p.journey, path: p.path, ok: true, notes: [] };
    consoleErrors.length = 0; badResponses.length = 0;
    try {
      await page.goto(BASE + p.path, { waitUntil: "domcontentloaded", timeout: 60000 });
      await page.waitForTimeout(3500);
      const body = await page.innerText("body");
      r.chars = body.length;

      for (const re of p.expect) if (!re.test(body)) { r.ok = false; r.notes.push(`missing ${re}`); }
      if (p.expectURL && !p.expectURL.test(page.url())) {
        r.ok = false; r.notes.push(`url is ${page.url()}, expected ${p.expectURL}`);
      }
      // A page that rendered nothing but a shell is a failure however green its assertions.
      if (body.length < 200) { r.ok = false; r.notes.push("page is essentially empty"); }
      // Raw UUIDs on an operator screen are the thing journeys 4 and 5 exist to remove.
      const uuids = body.match(/\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi) || [];
      if (uuids.length) r.notes.push(`${uuids.length} uuid(s) visible`);
      if (badResponses.length) { r.ok = false; r.notes.push(`api: ${badResponses.slice(0, 4).join("; ")}`); }
      if (consoleErrors.length) r.notes.push(`console: ${consoleErrors.slice(0, 2).join(" | ").slice(0, 160)}`);

      // HORIZONTAL OVERFLOW. On a phone this is the difference between usable and not.
      const overflow = await page.evaluate(() =>
        document.documentElement.scrollWidth - document.documentElement.clientWidth);
      if (overflow > 4) { r.ok = false; r.notes.push(`body scrolls ${overflow}px horizontally`); }

      await page.screenshot({ path: `${OUT}/${tag}-${p.slug}.png`, fullPage: true });

      // THE INTERACTIVE HALF. A tab nobody clicked and a disclosure nobody opened are not verified, and both
      // of these are where the journey actually lives.
      for (const step of p.then ?? []) {
        const el = page.getByText(step.click, { exact: false }).first();
        await el.click({ timeout: 15000 });
        await page.waitForTimeout(2500);
        const after = await page.innerText("body");
        for (const re of step.expect) if (!re.test(after)) { r.ok = false; r.notes.push(`after "${step.click}": missing ${re}`); }
        if (badResponses.length) { r.ok = false; r.notes.push(`api: ${badResponses.slice(0, 3).join("; ")}`); }
        const ov = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        if (ov > 4) { r.ok = false; r.notes.push(`after "${step.click}": scrolls ${ov}px horizontally`); }
        await page.screenshot({ path: `${OUT}/${tag}-${p.slug}-${step.slug}.png`, fullPage: true });
      }
    } catch (e) {
      r.ok = false; r.notes.push(String(e).split("\n")[0].slice(0, 160));
      try { await page.screenshot({ path: `${OUT}/${tag}-${p.slug}-FAIL.png` }); } catch {}
    }
    results.push(r);
  }
  await browser.close();
}

await run("desktop 1440x900", { viewport: { width: 1440, height: 900 } }, "desktop");
await run("iPhone 13", devices["iPhone 13"], "mobile");

let bad = 0;
for (const r of results) {
  if (!r.ok) bad++;
  console.log(`${r.ok ? "PASS" : "FAIL"}  ${r.viewport.padEnd(16)} ${r.journey.padEnd(28)} ${r.path.padEnd(22)} ${r.chars ?? "-"} chars  ${r.notes.join(" · ")}`);
}
console.log(`\n${results.length - bad}/${results.length} passed; screenshots in ${OUT}`);
process.exit(bad ? 1 : 0);
