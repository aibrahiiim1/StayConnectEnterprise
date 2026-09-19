// OPERATOR SWEEP OF THE LIVE PRE-LIVE HOTEL ADMIN.
//
// Signs in as a site admin and walks every navigation destination on the real appliance, desktop and mobile,
// recording for each page: what the operator sees, every console error, and every operator-API call that did
// not answer 2xx. No writes: this only reads.
import { chromium } from '@playwright/test';
import fs from 'node:fs';

const BASE = 'https://172.21.60.25';
const OUT = process.argv[2] || 'D:/tmp/sweep';
const MOBILE = process.argv[3] === 'mobile';

const ROUTES = [
  '/dashboard',
  '/internet-packages', '/service-plans', '/checkout-grace',
  '/stays', '/guest-accounts', '/sessions', '/guest-device-self-service', '/online-time', '/post-stay',
  '/pms-interfaces', '/pms-routing', '/stay-events', '/pms-resolutions', '/guest-signin-attempts',
  '/pms-source-conflicts', '/stay-transfers',
  '/financial-health', '/financial-review', '/financial-settlements', '/financial-recovery',
  '/sign-in-methods', '/portal-branding', '/walled-garden', '/social-providers', '/notifications',
  '/network', '/network/dhcp', '/network/system', '/network/revisions', '/network/certificate',
  '/health', '/operational-alerts', '/audit',
  '/setup/enrollment', '/license', '/backups', '/operators',
];

fs.mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const ctx = await browser.newContext({
  ignoreHTTPSErrors: true,
  viewport: MOBILE ? { width: 390, height: 844 } : { width: 1600, height: 1100 },
  isMobile: false,
});
const page = await ctx.newPage();

const report = [];
let bucket = { console: [], api: [] };
page.on('console', (m) => {
  if (m.type() === 'error') bucket.console.push(m.text().slice(0, 300));
});
page.on('pageerror', (e) => bucket.console.push('PAGEERROR ' + String(e).slice(0, 300)));
page.on('response', async (r) => {
  const u = r.url();
  if (!u.includes('/api/edge/v1')) return;
  if (r.status() >= 200 && r.status() < 300) return;
  let body = '';
  try { body = (await r.text()).slice(0, 200); } catch {}
  bucket.api.push(`${r.status()} ${r.request().method()} ${u.replace(BASE, '')} :: ${body}`);
});

// ---- sign in -------------------------------------------------------------------------------------------
await page.goto(`${BASE}/login`, { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(1200);
await page.locator('input[type="text"], input[name="email"], input[type="email"]').first().fill('admin');
await page.locator('input[type="password"]').first().fill('admin');
await page.locator('button[type="submit"]').first().click();
await page.waitForTimeout(3000);
console.log('after login ->', page.url());

for (const route of ROUTES) {
  bucket = { console: [], api: [] };
  const name = route.replace(/\//g, '_').replace(/^_/, '');
  let heading = '', text = '', err = '';
  try {
    await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(2600);
    heading = (await page.locator('h1').first().textContent().catch(() => ''))?.trim() ?? '';
    text = (await page.locator('main, body').first().innerText().catch(() => '')) ?? '';
  } catch (e) { err = String(e).slice(0, 200); }
  await page.screenshot({ path: `${OUT}/${name}.png`, fullPage: !MOBILE }).catch(() => {});
  report.push({
    route, heading, err,
    console: bucket.console,
    api: bucket.api,
    // The first 1400 characters an operator reads, which is where contradictions show up.
    text: text.replace(/\s+\n/g, '\n').slice(0, 1400),
  });
  console.log(`${route.padEnd(30)} h1="${heading}" console=${bucket.console.length} apiFail=${bucket.api.length}`);
}

fs.writeFileSync(`${OUT}/report.json`, JSON.stringify(report, null, 1));
await browser.close();
console.log('\nwrote', `${OUT}/report.json`);
