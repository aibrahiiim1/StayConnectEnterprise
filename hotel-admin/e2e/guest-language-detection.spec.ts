import { test, expect, devices, webkit, type Browser, type Page } from "@playwright/test";
import http from "node:http";
import type { AddressInfo } from "node:net";
import { portalHTML, shippedWording } from "./portal-page";

// AUTOMATIC GUEST LANGUAGE, IN REAL BROWSERS.
//
// A guest arriving at a captive portal has not chosen a language and is not going to hunt for a selector on a
// phone they are holding one-handed at a reception desk. Their device already knows what they read. The rules
// this exercises:
//
//   1. a language the guest CHOSE outranks anything detected, on this and every later visit;
//   2. otherwise the device's ORDERED preference list decides, first match against what the hotel enabled;
//   3. locale variants resolve to their language -- ar-EG is Arabic, de-AT is German;
//   4. a device asking for something the hotel does not offer falls back to English;
//   5. a stored choice the hotel has since switched off falls through to detection rather than pinning the
//      guest to a language that is no longer on the page.
//
// This serves the REAL landing page -- read out of portald's template -- from a throwaway server, so the
// script under test is the one the appliance ships. The same cases are then run against the live PRE-LIVE
// portal by hand; see the delivery report.

const PORTAL_HTML = portalHTML();
const SHIPPED = shippedWording();

/** Serves the real page plus the two answers it asks the appliance for. */
async function startPortal(enabled: string[] | null) {
  const langs = enabled === null
    ? undefined
    : enabled.map((c) => ({ code: c, label: SHIPPED.languages.find((l) => l.code === c)?.label ?? c }));
  const srv = http.createServer((req, res) => {
    const url = req.url ?? "/";
    const json = (b: unknown) => {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify(b));
    };
    if (url.startsWith("/api/auth-methods")) {
      return json({ pms: { enabled: true, mode: "room_any" }, voucher: { enabled: true } });
    }
    if (url.startsWith("/api/branding")) {
      return json({ design: { hotel_name: "Coral Sea Holiday Resort", ...(langs ? { languages: langs } : {}) } });
    }
    if (url.startsWith("/api/languages")) return json(SHIPPED);
    if (url.startsWith("/access/status")) return json({});
    if (url.startsWith("/assets/")) { res.writeHead(404); return res.end(); }
    res.writeHead(200, { "content-type": "text/html; charset=utf-8" });
    res.end(PORTAL_HTML);
  });
  await new Promise<void>((r) => srv.listen(0, "127.0.0.1", r));
  const port = (srv.address() as AddressInfo).port;
  return { url: `http://127.0.0.1:${port}/`, close: () => srv.close() };
}

/** Opens the portal with a device whose language list is exactly `languages`. */
async function visit(browser: Browser, url: string, languages: string[], extra: Record<string, unknown> = {}) {
  const ctx = await browser.newContext({
    locale: languages[0],
    // locale sets navigator.language; the ORDERED list is what the page is supposed to read, and only an
    // init script can set it. Defined on the prototype so it survives the page's own scripts.
    ...extra,
  });
  await ctx.addInitScript(([list]) => {
    Object.defineProperty(Object.getPrototypeOf(navigator), "languages", {
      get: () => list, configurable: true,
    });
    Object.defineProperty(Object.getPrototypeOf(navigator), "language", {
      get: () => list[0], configurable: true,
    });
  }, [languages]);
  const page = await ctx.newPage();
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(350);
  return { page, ctx };
}

async function shownLanguage(page: Page) {
  return {
    selected: await page.locator("#lang").inputValue(),
    dir: await page.evaluate(() => document.documentElement.dir),
    roomLabel: (await page.locator('[data-i18n="pms.room"]').first().textContent())?.trim(),
  };
}

test.describe("automatic guest language", () => {
  const ALL = ["en", "ar", "de", "fr", "it", "ru"];

  test("picks the device's language, including locale variants", async ({ browser }) => {
    const portal = await startPortal(ALL);
    try {
      for (const c of [
        { device: ["ar-EG"], want: "ar", label: SHIPPED.strings.ar["pms.room"], dir: "rtl" },
        { device: ["it-IT"], want: "it", label: SHIPPED.strings.it["pms.room"], dir: "ltr" },
        { device: ["fr-FR"], want: "fr", label: SHIPPED.strings.fr["pms.room"], dir: "ltr" },
        { device: ["ru-RU"], want: "ru", label: SHIPPED.strings.ru["pms.room"], dir: "ltr" },
        { device: ["de-AT"], want: "de", label: SHIPPED.strings.de["pms.room"], dir: "ltr" },
        { device: ["en-GB"], want: "en", label: SHIPPED.strings.en["pms.room"], dir: "ltr" },
      ]) {
        const { page, ctx } = await visit(browser, portal.url, c.device);
        const got = await shownLanguage(page);
        expect(got.selected, `${c.device[0]} should select ${c.want}`).toBe(c.want);
        expect(got.dir).toBe(c.dir);
        expect(got.roomLabel).toBe(c.label);
        await ctx.close();
      }
    } finally { portal.close(); }
  });

  test("reads the whole ordered list, not just the first entry", async ({ browser }) => {
    // A phone set to Japanese first and Italian second, at a hotel offering neither Japanese nor French,
    // should land on Italian rather than on English.
    const portal = await startPortal(["en", "it", "de"]);
    try {
      const { page, ctx } = await visit(browser, portal.url, ["ja-JP", "it-IT", "de-DE"]);
      expect((await shownLanguage(page)).selected).toBe("it");
      await ctx.close();

      // And the hotel's list decides, not the device's first choice: the same phone at a hotel offering only
      // German gets German.
      const portal2 = await startPortal(["en", "de"]);
      const v2 = await visit(browser, portal2.url, ["ja-JP", "it-IT", "de-DE"]);
      expect((await shownLanguage(v2.page)).selected).toBe("de");
      await v2.ctx.close();
      portal2.close();
    } finally { portal.close(); }
  });

  test("falls back to English when the hotel offers nothing the device asked for", async ({ browser }) => {
    const portal = await startPortal(["en", "fr"]);
    try {
      const { page, ctx } = await visit(browser, portal.url, ["ja-JP", "ko-KR"]);
      const got = await shownLanguage(page);
      expect(got.selected).toBe("en");
      expect(got.roomLabel).toBe(SHIPPED.strings.en["pms.room"]);
      await ctx.close();

      // A language the portal ships but the hotel switched OFF is not offered either.
      const v2 = await visit(browser, portal.url, ["de-DE"]);
      expect((await shownLanguage(v2.page)).selected).toBe("en");
      await v2.ctx.close();
    } finally { portal.close(); }
  });

  test("a guest's own choice outranks detection, and survives a reload", async ({ browser }) => {
    const portal = await startPortal(ALL);
    try {
      const { page, ctx } = await visit(browser, portal.url, ["de-DE"]);
      expect((await shownLanguage(page)).selected).toBe("de");

      await page.selectOption("#lang", "ar");
      await page.waitForTimeout(200);
      expect((await shownLanguage(page)).dir).toBe("rtl");

      // The same device, arriving again — which on a captive portal is the common case, not the rare one.
      await page.goto(portal.url, { waitUntil: "networkidle" });
      await page.waitForTimeout(350);
      const again = await shownLanguage(page);
      expect(again.selected, "a chosen language must outlive the visit").toBe("ar");
      expect(again.roomLabel).toBe(SHIPPED.strings.ar["pms.room"]);
      await ctx.close();
    } finally { portal.close(); }
  });

  test("a choice the hotel has since switched off falls through to detection", async ({ browser }) => {
    const portal = await startPortal(ALL);
    const ctx = await browser.newContext({ locale: "it-IT" });
    await ctx.addInitScript(() => {
      Object.defineProperty(Object.getPrototypeOf(navigator), "languages", {
        get: () => ["it-IT"], configurable: true,
      });
    });
    const page = await ctx.newPage();
    try {
      await page.goto(portal.url, { waitUntil: "networkidle" });
      await page.selectOption("#lang", "ru");
      await page.waitForTimeout(200);
      portal.close();

      const narrowed = await startPortal(["en", "it"]);
      await page.goto(narrowed.url, { waitUntil: "networkidle" });
      await page.waitForTimeout(350);
      // Russian is no longer on the page. Rather than pinning the guest to a missing option, or dropping
      // them to English, detection runs again and their device says Italian.
      expect((await shownLanguage(page)).selected).toBe("it");
      narrowed.close();
    } finally { await ctx.close(); }
  });

  test("detection leaves no trace, so it never masquerades as a choice", async ({ browser }) => {
    const portal = await startPortal(ALL);
    try {
      const { page, ctx } = await visit(browser, portal.url, ["fr-FR"]);
      expect((await shownLanguage(page)).selected).toBe("fr");
      const stored = await page.evaluate(() => ({
        ls: (() => { try { return localStorage.getItem("sc-lang"); } catch { return null; } })(),
        cookie: document.cookie.includes("sc-lang"),
      }));
      expect(stored.ls, "detection must not record a preference the guest never expressed").toBeNull();
      expect(stored.cookie).toBe(false);
      await ctx.close();
    } finally { portal.close(); }
  });
});

// iOS CAPTIVE PORTAL.
//
// iOS opens the portal in the Captive Network Assistant, a WebKit WebView that is NOT the Safari profile: its
// storage is short-lived and on a managed device localStorage can throw on the first write. That is why the
// chosen language is written to a cookie as well. This runs the real page under WebKit with the iPhone device
// profile, and again with localStorage made to throw, which is the condition that used to lose the choice.
test.describe("iOS captive-portal sheet", () => {
  test("detects and remembers under WebKit, even when localStorage is unavailable", async () => {
    // WebKit is launched explicitly rather than through a project override: the iPhone descriptor carries
    // defaultBrowserType, which Playwright refuses inside a describe. This is the same engine Safari and the
    // Captive Network Assistant run, with the iPhone viewport, user agent and touch profile.
    // channel is cleared: the config pins Chrome for the Chromium projects, and WebKit has no such
    // channel. Without this the launch is refused with 'Unsupported webkit channel "chrome"'.
    const wk = await webkit.launch({ channel: undefined });
    const portal = await startPortal(["en", "ar", "fr"]);
    const ctx = await wk.newContext({ ...devices["iPhone 13"], locale: "ar-EG" });
    await ctx.addInitScript(() => {
      Object.defineProperty(Object.getPrototypeOf(navigator), "languages", {
        get: () => ["ar-EG", "en-US"], configurable: true,
      });
      // The CNA condition: storage present but refusing.
      const dead = {
        getItem() { throw new Error("denied"); },
        setItem() { throw new Error("denied"); },
        removeItem() { throw new Error("denied"); },
      };
      Object.defineProperty(window, "localStorage", { get: () => dead, configurable: true });
    });
    const page = await ctx.newPage();
    try {
      await page.goto(portal.url, { waitUntil: "networkidle" });
      await page.waitForTimeout(400);
      const got = await shownLanguage(page);
      expect(got.selected, "an Egyptian iPhone should land on Arabic").toBe("ar");
      expect(got.dir).toBe("rtl");

      // The guest prefers French. With localStorage throwing, the cookie is what carries it.
      await page.selectOption("#lang", "fr");
      await page.waitForTimeout(200);
      expect(await page.evaluate(() => document.cookie.includes("sc-lang"))).toBe(true);

      await page.goto(portal.url, { waitUntil: "networkidle" });
      await page.waitForTimeout(400);
      expect((await shownLanguage(page)).selected,
        "the choice must survive the sheet reopening, which is what iOS does").toBe("fr");
    } finally { await ctx.close(); await wk.close(); portal.close(); }
  });
});
