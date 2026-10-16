// 5b. P-5 through the portal's per-device flood damper (30 sign-in posts / minute / address), from the guest
// device behind nsproxy.py: 30 quick wrong-voucher posts from the page, then one real form submission, which must
// say "wait", not "invalid". Only this device's address is affected; it expires within a minute.
const { chromium } = require('@playwright/test');
const path = require('path');
const PORTAL = process.env.PORTAL; const SHOTS = process.env.SHOTS;
(async () => {
  const browser = await chromium.launch({ channel: 'chrome', proxy: { server: 'http://127.0.0.1:18080' } });
  const page = await (await browser.newContext({ viewport: { width: 430, height: 932 }, locale: 'en-US' })).newPage();
  await page.goto(PORTAL + '/', { waitUntil: 'networkidle' });
  const codes = await page.evaluate(async () => { const out = []; for (let i = 0; i < 30; i++) { const r = await fetch('/auth/voucher', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: 'code=ACCDAMP' + i, redirect: 'manual' }); out.push(r.status); } return out; });
  await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle' }).catch(() => {}),
    page.evaluate(() => { const f = document.getElementById('form-voucher'); f.querySelector('#voucher').value = 'ACCDAMPLAST'; f.submit(); })]);
  const t = (await page.evaluate(() => document.body.innerText.replace(/\s+/g, ' ')));
  const m = (t.match(/Too many attempts[^.]*\.[^.]*\./i) || [])[0];
  await page.screenshot({ path: path.join(SHOTS, '11-voucher-wait.png'), fullPage: true });
  console.log(`${m ? 'PASS ' : 'FAIL '} P-5 throttled voucher says wait: ${m ? '"' + m + '"' : t.slice(0, 160)} (30 quick posts answered ${[...new Set(codes)].join('/')})`);
  await browser.close();
})().catch((e) => { console.error('ERROR', String(e).slice(0, 300)); process.exit(1); });
