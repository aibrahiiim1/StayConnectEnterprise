// 8. READ-ONLY Admin Console sweep (workstation): one operator, every route. Opens pages, clicks nothing.
// For each route: is it OFFERED (in this role's navigation)? An offered page must show no 403 from the API and no
// "Your role does not include this screen"; an unoffered page must show that sentence (audit L-2) rather than a
// shell of 403 banners. Also: the Room charge page carries the "posting is live" notice (D-E) where offered.
// Usage: NODE_PATH=<hotel-admin>/node_modules BASE=https://<test-appliance> ADMIN_USER=.. ADMIN_PASS=.. ROLE=.. OUT=<dir> node 08-console-sweep.js
const { chromium } = require('@playwright/test');
const fs = require('fs');
const path = require('path');
const BASE = process.env.BASE;
if (!BASE) { console.error('BASE is required (e.g. https://<test-appliance>)'); process.exit(2); }
const ROLE = process.env.ROLE || 'site_admin';
const OUT = process.env.OUT;
const ROUTES = `/dashboard,/appliance,/audit,/backups,/checkout-grace,/client-groups,/commercial-packages,/devices,/financial-health,/financial-recovery,/financial-review,/financial-settlements,/guest-accounts,/guest-device-self-service,/guest-signin-attempts,/health,/internet-packages,/license,/modules,/network,/notifications,/online-time,/operational-alerts,/operators,/payment-methods,/pms-interfaces,/pms-reconciliation,/pms-resolutions,/pms-routing,/pms-source-conflicts,/portal-branding,/portal-https,/post-stay,/room-charge,/room-sign-in,/roster-reconciliation,/service-plans,/sessions,/setup,/sign-in-methods,/social-providers,/stay-events,/stay-transfers,/stays,/usage,/vouchers,/walled-garden,/account`.split(',');
const NOT_PERMITTED = /Your role does not include this screen/;
fs.mkdirSync(OUT, { recursive: true });
(async () => {
  // The console's certificate is the appliance's internal one (management address), so verification is off here
  // -- unlike the guest portal, which presents a public certificate.
  const browser = await chromium.launch({ channel: 'chrome', args: ['--ignore-certificate-errors'] });
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  let api = [];
  page.on('response', (r) => { if (r.status() >= 400 && r.url().includes('/api/')) api.push(`${r.status()} ${r.request().method()} ${r.url().replace(BASE, '').replace(/\?.*/, '')}`); });
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await page.fill('input[autocomplete="username"]', process.env.ADMIN_USER);
  await page.fill('input[autocomplete="current-password"]', process.env.ADMIN_PASS);
  await page.keyboard.press('Enter');
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 20000 });
  await page.waitForTimeout(1500);
  const nav = [...new Set(await page.$$eval('aside a[href], nav a[href]', (as) => as.map((a) => a.getAttribute('href')).filter((h) => h && h.startsWith('/'))))];
  const offered = new Set(nav.map((h) => '/' + h.split('/')[1].split('?')[0]));
  const rows = []; let pass = 0, fail = 0;
  for (const route of ROUTES) {
    api = [];
    let status = '';
    try { const resp = await page.goto(BASE + route, { waitUntil: 'networkidle', timeout: 45000 }); status = resp ? String(resp.status()) : 'none'; } catch (e) { status = 'nav-error'; }
    await page.waitForTimeout(700);
    const body = (await page.evaluate(() => document.body.innerText.replace(/\s+/g, ' '))).trim();
    const notPermitted = NOT_PERMITTED.test(body);
    const a403 = api.filter((x) => x.startsWith('403')); const aOther = api.filter((x) => !x.startsWith('403'));
    const isOffered = offered.has(route);
    let verdict;
    if (isOffered) verdict = !notPermitted && a403.length === 0 ? 'PASS' : 'FAIL';
    else verdict = notPermitted ? 'PASS' : (a403.length ? 'FAIL' : 'INFO');
    if (verdict === 'PASS') pass++; if (verdict === 'FAIL') fail++;
    const extra = {};
    if (route === '/room-charge') extra.postingLiveNotice = /Room-charge posting is live on this appliance/.test(body);
    try { await page.screenshot({ path: path.join(OUT, route.slice(1) + '.png'), fullPage: route === '/room-charge' }); } catch {}
    rows.push({ route, offered: isOffered, status, notPermitted, api403: a403, apiOther: aOther, verdict, ...extra });
    console.log(`${verdict.padEnd(5)} ${route.padEnd(28)} offered=${isOffered ? 'y' : 'n'} notPermitted=${notPermitted ? 'y' : 'n'} 403=${a403.length} other4xx5xx=${aOther.length}${aOther.length ? ' ' + aOther.slice(0, 2).join(' | ') : ''}${'postingLiveNotice' in extra ? ' postingLiveNotice=' + extra.postingLiveNotice : ''}`);
  }
  fs.writeFileSync(path.join(OUT, 'results.json'), JSON.stringify({ role: ROLE, nav, rows }, null, 2));
  console.log(`SUMMARY role=${ROLE} offered=${offered.size} PASS=${pass} FAIL=${fail} INFO=${ROUTES.length - pass - fail}`);
  await browser.close();
})().catch((e) => { console.error('ERROR', String(e).slice(0, 300)); process.exit(1); });
