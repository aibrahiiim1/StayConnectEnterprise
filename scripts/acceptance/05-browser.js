// 5 (workstation side). A REAL Chrome whose traffic leaves from a guest-network device: every request goes through
// nsproxy.py running inside a namespace client on the guest bridge (05-browser-setup.sh), reached over
// `ssh -L 18080:<ns-ip>:3128 root@<appliance>`. TLS is verified (ignoreHTTPSErrors false): the portal presents a
// real certificate for its canonical hostname.
//   P-2  the stub OAuth path gives no redirect off the portal
//   sign in with a fixture Personal account, choose the USD card package (provider converts)
//   P-1  the browser really navigates to checkout.stripe.com; pay with 4242 4242 4242 4242 12/34 123 (Stripe TEST)
//   P-6  back on the portal: "You're online" with a real time remaining; no package panel on the success page
//   P-25 "/" does not bounce to /packages once the purchase is acquired
//   P-8  sign-out from the page ends access
//   P-5  wrong vouchers from this one device: the client is told to wait (device-scoped; released afterwards)
// Usage: NODE_PATH=<hotel-admin>/node_modules PORTAL=https://... ACC_USER=... ACC_UPW=... SHOTS=<dir> node 05-browser.js
const { chromium } = require('@playwright/test');
const fs = require('fs');
const path = require('path');
const PORTAL = process.env.PORTAL; const HOSTNAME = new URL(PORTAL).hostname;
const SHOTS = process.env.SHOTS || path.join(__dirname, 'acceptance-shots');
const PKG = process.env.PKG || 'Acc USD card';
fs.mkdirSync(SHOTS, { recursive: true });
const results = [];
const rec = (id, pass, ev) => { results.push({ id, result: pass === null ? 'BLOCKED' : pass ? 'PASS' : 'FAIL', ev }); console.log(`${pass === null ? 'BLOCK' : pass ? 'PASS ' : 'FAIL '} ${id}: ${ev}`); };
const text = async (p) => (await p.evaluate(() => document.body.innerText.replace(/\s+/g, ' '))).trim();
(async () => {
  const browser = await chromium.launch({ channel: 'chrome', proxy: { server: 'http://127.0.0.1:18080' } });
  const ctx = await browser.newContext({ ignoreHTTPSErrors: false, viewport: { width: 430, height: 932 }, locale: 'en-US' });
  const page = await ctx.newPage();
  const shot = async (n) => { try { await page.screenshot({ path: path.join(SHOTS, n + '.png'), fullPage: true }); } catch {} };
  const nav = [];
  page.on('framenavigated', (f) => { if (f === page.mainFrame()) nav.push(f.url().replace(/cs_test_[A-Za-z0-9_]+/, 'cs_test_…').replace(/\?.*/, '?…')); });

  // P-2: stub OAuth
  let r = await page.goto(`${PORTAL}/api/oauth/stub/authorize?provider=google&state=x&auto=1&email=a@b.c&redirect_uri=https://example.org/`, { waitUntil: 'domcontentloaded' });
  const h2 = new URL(page.url()).hostname;
  rec('P-2 stub OAuth gives no redirect', h2 === HOSTNAME, `status ${r && r.status()}, ended on ${h2}${new URL(page.url()).pathname}`);
  await shot('01-stub-oauth');

  // landing + sign in with the fixture Personal account
  await page.goto(PORTAL + '/', { waitUntil: 'networkidle' });
  await shot('02-landing');
  const tab = page.locator('button, [role="tab"]').filter({ hasText: /Personal account|Account Login/i });
  if (await tab.count()) await tab.first().click(); else await page.evaluate(() => { document.getElementById('form-credentials').style.display = 'block'; });
  await page.waitForTimeout(500);
  await page.fill('#ga-username', process.env.ACC_USER);
  await page.fill('#ga-password', process.env.ACC_UPW);
  await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle' }), page.locator('#form-credentials button[type="submit"]').first().click()]);
  const onPk = new URL(page.url()).pathname;
  rec('sign-in with a Personal account', onPk.startsWith('/packages'), `after sign-in: ${onPk}`);
  await shot('03-packages');

  // choose the USD card package
  const form = page.locator('.choice-list form').filter({ has: page.locator('input[name="method"][value="ONLINE_PAYMENT"]') }).filter({ hasText: PKG });
  const n = await form.count();
  rec('USD package offered by card in the browser', n > 0, `${n} card choice(s) named "${PKG}"; card choices on page: ${await page.locator('.choice-list form').filter({ has: page.locator('input[name="method"][value="ONLINE_PAYMENT"]') }).count()}`);
  if (!n) throw new Error('no card package');
  await form.first().locator('button').click();
  let toStripe = false;
  try { await page.waitForURL((u) => u.hostname === 'checkout.stripe.com', { timeout: 45000 }); toStripe = true; } catch {}
  rec('P-1 the browser navigates to checkout.stripe.com', toStripe, `now at ${new URL(page.url()).hostname}${new URL(page.url()).pathname.slice(0, 12)}…`);
  await page.waitForTimeout(4000); await shot('04-stripe-checkout');
  if (!toStripe) throw new Error('not on Stripe: ' + (await text(page)).slice(0, 200));

  // pay with the Stripe TEST card
  const fill = async (sel, v) => { const el = await page.$(sel); if (el && await el.isVisible()) { await el.fill(v); return true; } return false; };
  await page.waitForSelector('input#cardNumber, input#email', { timeout: 60000 }).catch(() => {});
  await fill('input#email', 'acceptance-card@example.com');
  const cardBtn = page.locator('[data-testid="card-accordion-item-button"], button:has-text("Card")'); if (await cardBtn.count()) { try { await cardBtn.first().click({ timeout: 3000 }); } catch {} }
  await fill('input#cardNumber', '4242424242424242');
  await fill('input#cardExpiry', '12 / 34');
  await fill('input#cardCvc', '123');
  await fill('input#billingName', 'Acceptance Tester');
  const country = await page.$('select#billingCountry'); if (country) { try { await country.selectOption('GB'); } catch {} }
  await fill('input#billingPostalCode', 'SW1A 1AA');
  await page.waitForTimeout(800); await shot('05-stripe-filled');
  await page.locator('button[type="submit"], .SubmitButton').first().click();
  let back = false;
  try { await page.waitForURL((u) => u.hostname === HOSTNAME, { timeout: 90000 }); back = true; } catch {}
  rec('Stripe TEST payment returns to the portal', back, `returned to ${new URL(page.url()).hostname}${new URL(page.url()).pathname}`);
  if (!back) { await shot('05b-stripe-stuck'); throw new Error('payment did not return: ' + (await text(page)).slice(0, 300)); }

  // P-6: You're online with a real time remaining
  let online = false;
  for (let i = 0; i < 40 && !online; i++) { online = /You're online/i.test(await text(page)); if (!online) await page.waitForTimeout(1500); }
  await page.waitForTimeout(1000); await shot('06-online');
  const t6 = await text(page);
  const rem = (t6.match(/Time remaining\s*([^A-Z]{0,40}?[0-9][^.]{0,30}?)(?=\s[A-Z]|$)/) || [])[1] || '';
  rec('P-6 "You\'re online" with a real time remaining', online && /\d/.test(rem), `online=${online}; Time remaining "${rem.trim()}"; at ${new URL(page.url()).pathname}`);
  const panel = await page.locator('.choice-list').count();
  rec('success page has no package panel', panel === 0 && !/Choose your package/i.test(t6), `.choice-list elements: ${panel}`);

  // P-25: "/" stays put
  await page.goto(PORTAL + '/', { waitUntil: 'networkidle' });
  const p25 = new URL(page.url()).pathname;
  rec('P-25 "/" does not bounce to /packages', !p25.startsWith('/packages'), `"/" ended on ${p25}`);
  await shot('07-root-after-purchase');

  // internet really works from this device
  // A 204 does not commit a navigation in Chrome, so the probe goes through the context's own request client
  // (same proxy, same guest device), without following redirects.
  const probe = async () => { try { const x = await ctx.request.get('http://connectivitycheck.gstatic.com/generate_204', { maxRedirects: 0, timeout: 10000 }); return x.status(); } catch (e) { return 'err'; } };
  const pOn = await probe();
  rec('the guest device is online (probe through the guest device)', pOn === 204, `generate_204 -> ${pOn}`);

  // P-8: sign-out from the page
  await page.goto(PORTAL + '/success', { waitUntil: 'networkidle' });
  await shot('08-success-before-signout');
  const out = page.locator('form[action="/logout"] button');
  const no = await out.count();
  if (no) { await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle' }).catch(() => {}), out.first().click()]); }
  await page.waitForTimeout(3000); await shot('09-after-signout');
  const after = await probe();
  rec('P-8 sign-out via the page ends access', no > 0 && after !== 204, `sign-out buttons: ${no}; probe after sign-out -> ${after} at ${new URL(page.url()).hostname}`);

  // P-5: wrong vouchers from THIS device only
  let waitMsg = '';
  for (let i = 1; i <= 9 && !waitMsg; i++) {
    await page.goto(PORTAL + '/', { waitUntil: 'networkidle' });
    const vt = page.locator('button, [role="tab"]').filter({ hasText: /voucher|access code/i }); if (await vt.count()) { try { await vt.first().click({ timeout: 2000 }); } catch {} }
    if (!(await page.locator('#voucher').isEnabled().catch(() => false)) || !(await page.locator('#voucher').isVisible().catch(() => false))) { if (i === 1) console.log('note: no voucher tab on the landing page; submitting the page own voucher form'); await page.evaluate(() => { const f = document.getElementById('form-voucher'); f.style.display = 'block'; f.querySelectorAll('input,button').forEach((e) => { e.disabled = false; }); }); }
    await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle' }).catch(() => {}),
      page.evaluate((code) => { const f = document.getElementById('form-voucher'); f.querySelector('#voucher').value = code; f.submit(); }, 'ACCWRONG' + i)]);
    const t = await text(page);
    const m = t.match(/Too many attempts[^.]*\.[^.]*\./i); if (m) waitMsg = `attempt ${i}: "${m[0]}"`;
  }
  await shot('10-voucher-throttle');
  rec('P-5 throttled voucher says wait (not "invalid")', !!waitMsg, waitMsg || 'no wait message after 9 wrong vouchers');

  console.log('NAV ' + JSON.stringify(nav));
  fs.writeFileSync(path.join(SHOTS, 'results.json'), JSON.stringify(results, null, 2));
  await browser.close();
})().catch(async (e) => { console.error('ERROR', String(e).slice(0, 400)); fs.writeFileSync(path.join(SHOTS, 'results.json'), JSON.stringify(results, null, 2)); process.exit(1); });
