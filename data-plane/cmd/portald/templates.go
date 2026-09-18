package main

const landingHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Wi-Fi Access</title>
<style>
  /* THE GUEST PORTAL, to the Product Owner's reference design.
     Full-bleed hotel photograph, one centred white card, branded header, two tab groups, and a quiet
     information affordance in the corner. Everything below is driven by CSS custom properties so the
     Branding screen can restyle it without touching this file. */
  :root {
    --sc-brand:      #0f6b63;   /* the teal of the reference buttons and active tab */
    --sc-brand-dark: #0b544e;
    --sc-ink:        #1c2b2a;
    --sc-muted:      #6b7b7a;
    --sc-card:       #ffffff;
    --sc-line:       #e3e8e8;
    --sc-radius:     18px;
    --sc-bg: url("/assets/portal-background.jpg");
    font-family: "Inter", -apple-system, system-ui, "Segoe UI", sans-serif;
  }
  * { box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    margin: 0; color: var(--sc-ink);
    /* The photograph fills the viewport and stays put while the card scrolls on a short screen. */
    background: var(--sc-bg) center/cover no-repeat fixed, linear-gradient(160deg,#cfe3e6,#eef3f2);
    min-height: 100%;
    display: flex; align-items: flex-start; justify-content: center;
    padding: clamp(12px, 4vh, 64px) clamp(12px, 4vw, 48px) 48px;
  }

  /* Language selector, upper right of the PAGE rather than the card, as in the reference. */
  .langbar { position: fixed; top: 12px; right: 12px; z-index: 5; }
  .lang {
    display: inline-flex; align-items: center; gap: 8px;
    background: #fff; border: 1px solid var(--sc-line); border-radius: 999px;
    padding: 8px 14px; font-size: .95rem; color: var(--sc-ink); cursor: pointer;
    box-shadow: 0 2px 10px rgb(0 0 0 / .08);
  }
  .lang select { border: 0; background: none; font: inherit; color: inherit; cursor: pointer; outline: none; }

  .card {
    position: relative;
    width: min(1100px, 100%);
    background: var(--sc-card);
    border-radius: var(--sc-radius);
    box-shadow: 0 24px 60px rgb(0 0 0 / .18);
    padding: clamp(20px, 3.5vw, 44px);
    margin-top: clamp(8px, 6vh, 72px);
  }
  .brand { display: flex; align-items: center; gap: 14px; min-height: 56px; }
  .brand img { max-height: 56px; max-width: 260px; object-fit: contain; }
  .brand .name { font-size: 1.05rem; letter-spacing: .04em; color: var(--sc-muted); }
  .rule { border: 0; border-top: 1px solid var(--sc-line); margin: clamp(16px, 2.5vw, 28px) 0 0; }

  /* TWO GROUPS, NOT SIX. The reference presents a Guest door and an Account keypad; every enabled
     method lands in one of them, so a guest chooses between "I am staying here" and "I have a code". */
  .tabs { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); }
  .tab {
    appearance: none; background: none; border: 0; border-bottom: 2px solid transparent;
    padding: clamp(14px, 2vw, 22px) 8px; font: inherit; font-size: clamp(1rem, 1.4vw, 1.25rem);
    color: var(--sc-muted); cursor: pointer; display: inline-flex; align-items: center;
    justify-content: center; gap: 10px;
  }
  .tab[aria-selected="true"] { color: var(--sc-brand); border-bottom-color: var(--sc-brand); font-weight: 600; }
  .tab svg { width: 1.1em; height: 1.1em; flex: 0 0 auto; }

  .panels { border-top: 1px solid var(--sc-line); padding-top: clamp(18px, 3vw, 34px); }
  .panel { display: none; }
  .panel.active { display: block; }
  .field { margin-bottom: clamp(14px, 2vw, 22px); max-width: 560px; }
  .panel--wide .field { max-width: none; }
  label { display: block; font-size: clamp(.95rem, 1.1vw, 1.05rem); margin-bottom: 8px; }
  input[type=text], input[type=password], input[type=email], input[type=tel] {
    width: 100%; padding: 14px 16px; font-size: 1rem; color: var(--sc-ink);
    border: 1px solid var(--sc-line); border-radius: 12px; background: #fff;
  }
  input:focus-visible, .tab:focus-visible, button:focus-visible, .lang:focus-within {
    outline: 2px solid var(--sc-brand); outline-offset: 2px;
  }
  .hint { color: var(--sc-muted); font-size: .92rem; margin: 8px 0 0; }
  button.primary {
    background: linear-gradient(180deg, var(--sc-brand), var(--sc-brand-dark));
    color: #fff; border: 0; border-radius: 12px; padding: 14px 34px;
    font: inherit; font-weight: 600; cursor: pointer;
  }
  button.primary:disabled { opacity: .55; cursor: wait; }

  /* "Use Personal Account" — a pill-shaped checkbox, as in the reference. */
  .pill {
    display: inline-flex; align-items: center; gap: 10px;
    border: 1px solid var(--sc-line); border-radius: 999px; padding: 12px 20px;
    color: var(--sc-muted); cursor: pointer; user-select: none; margin-bottom: clamp(16px, 2.5vw, 26px);
  }
  .pill input { width: 18px; height: 18px; accent-color: var(--sc-brand); }
  .pill:has(input:checked) { color: var(--sc-brand); border-color: var(--sc-brand); }

  /* Information affordance, lower right of the card. */
  .info-btn {
    position: absolute; right: clamp(14px, 2vw, 26px); bottom: clamp(14px, 2vw, 26px);
    width: 30px; height: 30px; border-radius: 999px; border: 1px solid var(--sc-line);
    background: #f4f7f7; color: var(--sc-muted); cursor: pointer; font-weight: 700; line-height: 1;
  }
  .info-panel {
    margin-top: 20px; border: 1px solid var(--sc-line); border-radius: 12px;
    padding: 14px 16px; font-size: .92rem; color: var(--sc-muted); background: #f8fafa;
  }
  .info-panel dl { display: grid; grid-template-columns: auto 1fr; gap: 4px 16px; margin: 8px 0 0; }
  .info-panel dd { margin: 0; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--sc-ink); }

  .err { color: #b00020; margin-top: 12px; min-height: 1.2em; font-size: .92rem; }
  .notice { display:none; margin:0 0 16px; padding:12px 16px; border-radius:10px; font-size:.92rem;
            background:#fff8e1; border:1px solid #f0d38a; color:#6b4e00; }
  .notice.show { display:block; }
  .small { font-size:.85rem; color: var(--sc-muted); }
  .alt { margin-top: 22px; padding-top: 18px; border-top: 1px dashed var(--sc-line); }
  .alt h3 { font-size: .9rem; font-weight: 600; color: var(--sc-muted); margin: 0 0 12px; }
  #pms-choices button.choice { display:block; width:100%; text-align:left; margin:8px 0; padding:14px 16px;
    border:1px solid var(--sc-line); border-radius:12px; background:#fff; color:var(--sc-ink); cursor:pointer;
    font-size:1rem; }
  #pms-choices button.choice[disabled] { opacity:.5; cursor:default; }
  button.link { background:none; border:0; color:var(--sc-brand); font:inherit; padding:6px; cursor:pointer; }

  /* PHONE. The card becomes the page: full width, flat corners, tighter rhythm. Same design language,
     not a second design. */
  @media (max-width: 640px) {
    body { padding: 0; align-items: stretch; background-attachment: scroll; }
    .card { margin-top: 0; border-radius: 0; min-height: 100vh; box-shadow: none; }
    .tabs { grid-template-columns: 1fr 1fr; }
    .langbar { position: static; display: flex; justify-content: flex-end; padding: 10px 12px 0; }
  }
  @media (prefers-reduced-motion: no-preference) {
    .panel.active { animation: fade .18s ease-out; }
    @keyframes fade { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: none; } }
  }
</style>
</head><body>
  <div class="langbar">
    <label class="lang" for="lang">
      <span id="lang-flag" aria-hidden="true">🌐</span>
      <select id="lang" aria-label="Language"><option value="en" selected>English</option></select>
    </label>
  </div>

  <main class="card">
    <!-- BRANDING. Filled from /api/branding when the hotel has published a design; the defaults below are
         what an unbranded appliance shows, which must still look deliberate rather than broken. -->
    <div class="brand">
      <img id="brand-logo" alt="" style="display:none">
      <span class="name" id="brand-name"></span>
    </div>
    <hr class="rule">

    <!-- WHY THE INTERNET STOPPED. Shown only when this device's most recent access ended because it ran out
         of data or time; the sign-in below is unchanged and the guest carries straight on into it. -->
    <div class="notice" id="access-ended" role="status" aria-live="polite"></div>
    <div class="notice" id="site-notice" role="status" aria-live="polite"></div>

    <div class="tabs" id="tabs" role="tablist"></div>
    <div class="panels">

  <!-- Voucher panel -->
  <div class="panel" id="panel-voucher">
    <form method="POST" action="/auth/voucher">
      <label for="voucher"><span data-i18n="voucher.label" data-i18n-en="Voucher code">Voucher code</span></label>
      <input id="voucher" name="code" type="text" autocomplete="off" required maxlength="32" placeholder="XXXX-XXXX-XXXX">
      <button type="submit"><span data-i18n="btn.connect" data-i18n-en="Connect">Connect</span></button>
      <div class="err">{{.Error}}</div>
    </form>
  </div>

  <!-- Guest account (username + password) panel -->
  <div class="panel" id="panel-account">
    <form method="POST" action="/auth/credentials" autocomplete="off">
      <label for="ga-username"><span data-i18n="account.user" data-i18n-en="Username">Username</span></label>
      <input id="ga-username" name="username" type="text" autocomplete="username" required maxlength="64" placeholder="username">
      <label for="ga-password" style="margin-top:10px"><span data-i18n="account.pass" data-i18n-en="Password">Password</span></label>
      <input id="ga-password" name="password" type="password" autocomplete="current-password" required maxlength="128" placeholder="password">
      <button type="submit" style="margin-top:10px">Connect</button>
      <div class="err">{{.Error}}</div>
    </form>
  </div>

  <!-- Email panel -->
  <div class="panel" id="panel-email">
    <form data-otp="email" data-stage="dest" autocomplete="off">
      <label for="email"><span data-i18n="email.dest" data-i18n-en="Email address">Email address</span></label>
      <input id="email" name="dest" type="email" required placeholder="you@example.com" autocomplete="email">
      <button type="submit"><span data-i18n="btn.sendcode" data-i18n-en="Send code">Send code</span></button>
      <div class="err"></div>
    </form>
    <form data-otp="email" data-stage="code" autocomplete="off" style="display:none">
      <p class="small">We sent a 6-digit code to <span class="dest"></span>.</p>
      <label><span data-i18n="otp.code" data-i18n-en="Verification code">Verification code</span></label>
      <input name="code" type="text" inputmode="numeric" pattern="[0-9]*" required maxlength="6" placeholder="------">
      <button type="submit"><span data-i18n="btn.verify" data-i18n-en="Verify">Verify</span></button>
      <button type="button" class="link" data-resend>Try a different email</button>
      <div class="err"></div>
    </form>
  </div>

  <!-- PMS / Room panel — guest enters room number plus one verification field -->
  <div class="panel" id="panel-pms">
    <form id="form-pms" autocomplete="off">
      <label for="pms-room"><span data-i18n="pms.room" data-i18n-en="Room number">Room number</span></label>
      <input id="pms-room" name="room" type="text" inputmode="numeric" required placeholder="e.g. 101">
      <p class="small" id="pms-prompt" style="margin-top:10px"></p>
      <input id="pms-secondary" name="secondary" type="text" required placeholder="Last name or reservation number">
      <button type="submit">Connect</button>
    </form>
    <!-- the error lives OUTSIDE the form: during package selection the form is hidden, and a failure message
         inside it would be invisible exactly when the guest most needs to see it. -->
    <div class="err" id="pms-err" role="alert" aria-live="polite"></div>
    <div id="pms-choices" role="group" aria-label="Internet packages" style="display:none"></div>
  </div>

  <!-- Post-stay panel (Phase 5, DARK) — ONE field.
       A departing guest is proving an identity for the SECOND time, and the first proof left a durable
       record on this device. So there is no room, no name and no reservation number here: the appliance
       already knows which stay this device belonged to, and asking again would only create a field an
       attacker could put someone else's answer in. -->
  <div class="panel" id="panel-poststay">
    <form id="form-poststay" autocomplete="off">
      <label for="ps-pin">Post-stay PIN</label>
      <input id="ps-pin" name="pin" type="text" inputmode="text" autocapitalize="characters"
             required placeholder="The PIN you were given at checkout">
      <button type="submit">Reconnect</button>
    </form>
    <div class="err" id="ps-err" role="alert" aria-live="polite"></div>
  </div>

  <!-- Social panel -->
  <div class="panel" id="panel-social">
    <div id="social-providers"></div>
    <p class="small" style="margin-top:12px">You'll be redirected to the provider, then back here.</p>
  </div>

  <!-- SMS panel -->
  <div class="panel" id="panel-sms">
    <form data-otp="sms" data-stage="dest" autocomplete="off">
      <label for="phone"><span data-i18n="sms.dest" data-i18n-en="Phone number">Phone number</span></label>
      <input id="phone" name="dest" type="tel" required placeholder="+1 555 123 4567" autocomplete="tel">
      <p class="small">Include country code, e.g. <span class="small">+44 20 7946 0958</span></p>
      <button type="submit">Send code</button>
      <div class="err"></div>
    </form>
    <form data-otp="sms" data-stage="code" autocomplete="off" style="display:none">
      <p class="small">We texted a 6-digit code to <span class="dest"></span>.</p>
      <label>Verification code</label>
      <input name="code" type="text" inputmode="numeric" pattern="[0-9]*" required maxlength="6" placeholder="------">
      <button type="submit">Verify</button>
      <button type="button" class="link" data-resend>Use a different number</button>
      <div class="err"></div>
    </form>
  </div>

      <div class="alt" id="alt-methods" style="display:none"></div>
    </div>

    <button class="info-btn" id="info-btn" type="button" aria-expanded="false" aria-controls="info-panel"
            aria-label="Device information" title="Device information">i</button>
    <div class="info-panel" id="info-panel" hidden>
      <strong data-i18n="info.device" data-i18n-en="Your device">Your device</strong>
      <dl>
        <dt><span data-i18n="info.ip" data-i18n-en="IP address">IP address</span></dt><dd>{{if .ClientIP}}{{.ClientIP}}{{else}}not detected{{end}}</dd>
        <dt><span data-i18n="info.mac" data-i18n-en="MAC address">MAC address</span></dt><dd>{{if .ClientMAC}}{{.ClientMAC}}{{else}}not detected{{end}}</dd>
      </dl>
      <p class="small" style="margin-top:10px" data-i18n="info.help" data-i18n-en="Reception may ask for these if you need help connecting.">Reception may ask for these if you need help connecting.</p>
    </div>
  </main>

  <script>
    // THE REFERENCE DESIGN PRESENTS TWO DOORS, NOT SIX.
    //
    // With PMS, voucher, accounts, email, SMS, social and post-stay all enabled the old row carried seven
    // tabs; on a 360px phone the labels were unreadable, which is the device almost every guest uses. The
    // question a guest can actually answer is not "which authentication method" -- it is "am I staying here,
    // or do I have a code?". So every enabled method lands in one of two groups, and the group is only shown
    // when it has something in it. No method is removed: this is presentation, and each panel below is the
    // same form it always was.
    const ICON_DOOR = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M3 21h18M6 21V4a1 1 0 0 1 1-1h7a1 1 0 0 1 1 1v17"/><circle cx="12" cy="12" r="1" fill="currentColor" stroke="none"/></svg>';
    const ICON_KEYS = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M7 14h10"/></svg>';

    const Groups = {
      guest:   { id:'guest',   label:'Guest Login',   icon: ICON_DOOR, members:['pms','poststay'] },
      account: { id:'account', label:'Account Login', icon: ICON_KEYS, members:['voucher','account','email','sms','social'] },
    };
    const Tabs = {
      voucher: { id:'voucher', label:'Voucher', panel:'panel-voucher' },
      account: { id:'account', label:'Username', panel:'panel-account' },
      email:   { id:'email',   label:'Email',   panel:'panel-email' },
      sms:     { id:'sms',     label:'Phone',   panel:'panel-sms' },
      pms:     { id:'pms',     label:'Room',    panel:'panel-pms' },
      social:  { id:'social',  label:'Social',  panel:'panel-social' },
      poststay:{ id:'poststay',label:'Post-stay',panel:'panel-poststay' },
    };
    const ProviderLabels = { google: 'Continue with Google', apple: 'Continue with Apple', facebook: 'Continue with Facebook' };
    const PMSPrompts = {
      room_lastname:    "Last name on the reservation",
      room_firstname:   "First name on the reservation",
      room_reservation: "Reservation / confirmation number",
      // room_any accepts any of the three. The guest is told what they MAY type and is never asked to
      // classify it — the server compares one value against all three fields.
      room_any:         "First name, last name, or reservation number",
      either:           "Last name OR reservation number",
    };
    const challenges = {}; // channel -> challenge_id

    // Which METHOD is showing, within whichever group is selected.
    function setTab(id) {
      document.querySelectorAll('.panel').forEach(el => el.classList.remove('active'));
      const t = Tabs[id]; if (t) document.getElementById(t.panel).classList.add('active');
    }
    // Which GROUP is selected. Shows that group's first available method, and any others as alternatives
    // beneath it, so a hotel running both vouchers and accounts still offers both.
    function setGroup(gid, groupMembers) {
      document.querySelectorAll('.tab').forEach(el => {
        const on = el.dataset.group === gid;
        el.setAttribute('aria-selected', on ? 'true' : 'false');
      });
      const members = groupMembers[gid] || [];
      setTab(members[0]);
      const alt = document.getElementById('alt-methods');
      alt.innerHTML = '';
      if (members.length > 1) {
        const h = document.createElement('h3');
        h.textContent = 'Or sign in with';
        alt.appendChild(h);
        members.slice(1).forEach(m => {
          const b = document.createElement('button');
          b.type = 'button'; b.className = 'link'; b.textContent = Tabs[m].label;
          // The alternatives ARE method selectors, so they carry the method they select. Anything looking for
          // a particular sign-in method finds it here now that there is no longer a tab per method.
          b.dataset.tab = m;
          b.onclick = () => setTab(m);
          alt.appendChild(b);
        });
        alt.style.display = '';
      } else {
        alt.style.display = 'none';
      }
    }

    // TRANSLATIONS. The language selector used to change nothing, which is worse than not offering one: it
    // tells a guest their language is supported and then does not support it.
    //
    // Every guest-facing string carries a data-i18n key. The hotel publishes translations as part of its
    // design, so a property can say "Room number" in the words its guests actually use without anyone
    // shipping a build. English is the fallback for any key a translation omits -- a half-translated portal
    // still reads, where a portal showing raw keys does not.
    var I18N = {};        // code -> { key: text }
    var LANG = 'en';

    function applyLanguage(code) {
      LANG = code;
      var dict = I18N[code] || {};
      document.documentElement.lang = code;
      document.querySelectorAll('[data-i18n]').forEach(function (el) {
        var k = el.dataset.i18n;
        if (dict[k]) el.textContent = dict[k];
        else if (el.dataset.i18nEn) el.textContent = el.dataset.i18nEn;
      });
      document.querySelectorAll('[data-i18n-ph]').forEach(function (el) {
        var k = el.dataset.i18nPh;
        if (dict[k]) el.placeholder = dict[k];
        else if (el.dataset.i18nPhEn) el.placeholder = el.dataset.i18nPhEn;
      });
      // Tab labels are generated, so they are re-rendered rather than translated in place.
      document.querySelectorAll('.tab[data-group]').forEach(function (el) {
        var g = el.dataset.group;
        var span = el.querySelector('span');
        if (span) span.textContent = dict['tab.' + g] || Groups[g].label;
      });
      try { localStorage.setItem('sc-lang', code); } catch (e) {}
    }

    // Remember the guest's choice for this device: a guest who picked their language once should not have to
    // do it again on the next captive-portal redirect.
    document.getElementById('lang').addEventListener('change', function (e) {
      applyLanguage(e.target.value);
    });

    // BRANDING. Applied before anything else paints so the guest never sees the default teal flash to the
    // hotel's colour. Every value is optional and every default is a deliberate, finished-looking fallback.
    fetch('/api/branding').then(r => r.ok ? r.json() : {}).then(b => {
      const d = (b && b.design) || b || {};
      const root = document.documentElement.style;
      if (d.brand_color)      root.setProperty('--sc-brand', d.brand_color);
      if (d.brand_color_dark) root.setProperty('--sc-brand-dark', d.brand_color_dark);
      if (d.text_color)       root.setProperty('--sc-ink', d.text_color);
      if (d.font_family)      root.setProperty('font-family', d.font_family);
      if (d.corner_radius)    root.setProperty('--sc-radius', d.corner_radius);
      if (d.background_url)   root.setProperty('--sc-bg', 'url("' + encodeURI(d.background_url) + '")');
      if (d.hotel_name) {
        document.getElementById('brand-name').textContent = d.hotel_name;
        document.title = d.hotel_name + ' — Wi-Fi';
      }
      if (d.logo_url) {
        const img = document.getElementById('brand-logo');
        img.src = d.logo_url; img.alt = d.hotel_name || 'Hotel'; img.style.display = '';
      }
      I18N = (d.translations && typeof d.translations === 'object') ? d.translations : {};
      const sel = document.getElementById('lang');
      // The offered languages are the ones the hotel actually published words for, plus English. Offering a
      // language with no translation behind it is the defect this replaces.
      const codes = Array.isArray(d.languages) && d.languages.length
        ? d.languages
        : ['en'].concat(Object.keys(I18N).filter(c => c !== 'en')).map(c => ({ code: c }));
      sel.innerHTML = '';
      codes.forEach(l => {
        const code = l.code || l;
        const o = document.createElement('option');
        o.value = code;
        o.textContent = l.label || (I18N[code] && I18N[code]['lang.name']) || code.toUpperCase();
        sel.appendChild(o);
      });
      let want = 'en';
      try { want = localStorage.getItem('sc-lang') || 'en'; } catch (e) {}
      // The browser's own preference is a better first guess than English for a guest who has never chosen.
      if (want === 'en' && navigator.language) {
        const short = navigator.language.slice(0, 2).toLowerCase();
        if (I18N[short]) want = short;
      }
      if (!Array.prototype.some.call(sel.options, o => o.value === want)) want = sel.options[0] ? sel.options[0].value : 'en';
      sel.value = want;
      applyLanguage(want);
    }).catch(() => {});

    // The information affordance. Purely local: the addresses are already rendered into the panel.
    (function () {
      const btn = document.getElementById('info-btn');
      const panel = document.getElementById('info-panel');
      btn.addEventListener('click', () => {
        const open = panel.hasAttribute('hidden');
        if (open) { panel.removeAttribute('hidden'); } else { panel.setAttribute('hidden', ''); }
        btn.setAttribute('aria-expanded', open ? 'true' : 'false');
      });
    })();

    fetch('/api/auth-methods').then(r => r.json()).then(cfg => {
      const tabsEl = document.getElementById('tabs');
      const enabled = [];
      if (cfg.voucher && cfg.voucher.enabled) enabled.push('voucher');
      if (cfg.guest_account && cfg.guest_account.enabled) enabled.push('account');
      if (cfg.email   && cfg.email.enabled)   enabled.push('email');
      if (cfg.sms     && cfg.sms.enabled)     enabled.push('sms');
      PHASE3_PMS = !!cfg.phase3_pms;
      if (cfg.pms     && cfg.pms.enabled) {
        enabled.push('pms');
        document.getElementById('pms-prompt').textContent = PMSPrompts[cfg.pms.mode] || PMSPrompts.either;
        // Pre-set the secondary field's autocomplete hint based on mode.
        const sec = document.getElementById('pms-secondary');
        sec.placeholder = PMSPrompts[cfg.pms.mode] || sec.placeholder;
        sec.dataset.mode = cfg.pms.mode || 'either';
      }
      // POST-STAY HAS ITS OWN GATE, and it is not the PMS one.
      //
      // This used to be pushed inside the PMS branch, so turning on room sign-in also put a Post-Stay tab in
      // front of every guest. They are different capabilities — PMS proves an in-house guest by room and
      // name, post-stay lets a DEPARTED guest back in with a PIN — and on an appliance with Phase 5 off the
      // routes behind that tab are not mounted at all, so every PIN typed into it reached nothing.
      //
      // It is still pushed AFTER pms so the tab ORDER is unchanged: setTab(enabled[0]) opens the first tab,
      // and a guest arriving to authenticate for the first time must land on Room, not on a PIN they do not
      // have yet.
      if (cfg.phase5_poststay) {
        enabled.push('poststay');
      }
      // Render social provider buttons.
      if (cfg.social) {
        const providers = Object.keys(cfg.social).filter(k => cfg.social[k] && cfg.social[k].enabled);
        if (providers.length > 0) {
          enabled.push('social');
          const host = document.getElementById('social-providers');
          providers.forEach(p => {
            const a = document.createElement('a');
            a.href = '/auth/social/start?provider=' + encodeURIComponent(p);
            a.style.cssText = 'display:block;text-align:center;padding:12px;margin-top:10px;border:1px solid #ccc;border-radius:8px;color:inherit;text-decoration:none;font-weight:600';
            a.textContent = ProviderLabels[p] || ('Continue with ' + p);
            host.appendChild(a);
          });
        }
      }
      // NO INTERNET PACKAGE EXISTS AT THIS SITE — a site availability notice, not an authentication result.
      //
      // Every property that makes this safe is structural rather than a matter of care:
      //
      //   * it is read from /api/auth-methods on page render, BEFORE any identity is submitted;
      //   * the value is site-wide configuration — the same answer for every guest on this network;
      //   * it is rendered above the sign-in tabs, as general availability, never in an error slot;
      //   * nothing after submission reads it, so it cannot vary with what a guest typed.
      //
      // That is what keeps it outside the uniform-envelope contract. The contract governs what an
      // AUTHENTICATION ATTEMPT may reveal; this is a statement about the site made before anyone attempts
      // anything, and it carries no information about any room, name or stay.
      // THE EXPIRY NOTICE. Asked once, on load, and only ever renders a message the SERVER chose from its
      // two-sentence vocabulary — the browser never composes this text and never learns anything else about
      // the access that ended. Any failure is silent: the ordinary sign-in page is the correct fallback.
      fetch('/access/status', {method:'POST', headers:{'Content-Type':'application/json'}, body:'{}'})
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(res){
          if (!res || !res.message) return;
          const n = document.getElementById('access-ended');
          n.textContent = res.message;
          n.classList.add('show');
        })
        .catch(function(){ /* no notice; the sign-in form below is unaffected */ });

      if (cfg.internet_packages_available === false) {
        const n = document.getElementById('site-notice');
        n.textContent = 'Internet access is not available here at the moment. You can still sign in, but there is nothing to connect you to yet — please let reception know.';
        n.classList.add('show');
      }
      if (enabled.length === 0) {
        tabsEl.innerHTML = '<div class="small">There is no way to sign in on this network yet. Please contact reception.</div>';
        return;
      }
      // Sort the enabled methods into the two groups, preserving the order that enabled[] already established --
      // that order is deliberate elsewhere in this file and must not be re-litigated here.
      const groupMembers = {};
      Object.values(Groups).forEach(g => {
        const members = enabled.filter(id => g.members.includes(id));
        if (members.length) groupMembers[g.id] = members;
      });
      const shown = Object.keys(groupMembers);

      // A single group is not a choice, so it is not rendered as one: the guest sees the form, not a lone
      // tab asking them to pick the only option.
      if (shown.length > 1) {
        shown.forEach(gid => {
          const el = document.createElement('button');
          el.type = 'button'; el.className = 'tab'; el.dataset.group = gid;
          el.setAttribute('role', 'tab');
          el.innerHTML = Groups[gid].icon + '<span>' + ((I18N[LANG] || {})['tab.' + gid] || Groups[gid].label) + '</span>';
          el.addEventListener('click', () => setGroup(gid, groupMembers));
          tabsEl.appendChild(el);
        });
      } else {
        tabsEl.style.display = 'none';
      }
      setGroup(shown[0], groupMembers);
    }).catch(() => { setTab('voucher'); });

    function panel(channel) { return document.getElementById('panel-' + channel); }
    function form(channel, stage) { return panel(channel).querySelector('form[data-stage="' + stage + '"]'); }

    function attach(channel) {
      const destForm = form(channel, 'dest');
      const codeForm = form(channel, 'code');
      destForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const btn = destForm.querySelector('button[type=submit]');
        const errEl = destForm.querySelector('.err');
        errEl.textContent = ''; btn.disabled = true;
        const dest = destForm.querySelector('input[name=dest]').value.trim();
        try {
          const r = await fetch('/auth/otp/request', {
            method:'POST', headers:{'Content-Type':'application/json'},
            body: JSON.stringify({ channel, destination: dest })
          });
          const j = await r.json().catch(() => ({}));
          if (!r.ok) { errEl.textContent = j.error || 'Request failed'; return; }
          challenges[channel] = j.challenge_id;
          codeForm.querySelector('.dest').textContent = dest;
          destForm.style.display = 'none';
          codeForm.style.display = 'block';
          codeForm.querySelector('input[name=code]').focus();
        } finally { btn.disabled = false; }
      });
      codeForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        const btn = codeForm.querySelector('button[type=submit]');
        const errEl = codeForm.querySelector('.err');
        errEl.textContent = ''; btn.disabled = true;
        const code = codeForm.querySelector('input[name=code]').value.trim();
        try {
          const r = await fetch('/auth/otp/verify', {
            method:'POST', headers:{'Content-Type':'application/json'},
            body: JSON.stringify({ challenge_id: challenges[channel], code })
          });
          const j = await r.json().catch(() => ({}));
          if (!r.ok) { errEl.textContent = j.error || 'Verification failed'; return; }
          window.location = '/success?s=' + encodeURIComponent(j.session_id || '') +
                            '&t=' + encodeURIComponent(j.duration_seconds || 0);
        } finally { btn.disabled = false; }
      });
      codeForm.querySelector('[data-resend]').addEventListener('click', () => {
        codeForm.style.display = 'none';
        destForm.style.display = 'block';
        destForm.querySelector('input[name=dest]').focus();
        delete challenges[channel];
      });
    }
    attach('email');
    attach('sms');

    // ---- Phase 3 (Stay resolution) ----------------------------------------
    // The guest sees exactly two possible outcomes: they are in, or the one message below. There is
    // deliberately no branch here that renders a server reason — a page that could say "that room exists but
    // the name is wrong" is an occupancy oracle for anyone sitting in the lobby.
    let PHASE3_PMS = false;
    let PMS_AUTH_CONTEXT = '';
    // THE ONE MESSAGE EVERY AUTHENTICATION NON-SUCCESS RENDERS.
    //
    // Wrong room, wrong name, no such stay, checked out, stale occupancy, PMS unreachable, network not
    // mapped, verified with nothing to grant, the server's response-time budget expiring, a dropped
    // connection: all of them produce exactly this text. That uniformity is the Phase-0 FINAL contract and a
    // security property, not a UX shortcut — any answer that varies with the outcome lets someone submit
    // room/surname pairs and learn which ones are real.
    //
    // An earlier revision added a second message for the verified-but-no-package case, on the reasoning that
    // a correct guest should not be told to re-check correct details. The reasoning was right about the
    // guest and wrong about the contract: differentiating AFTER submission is exactly the oracle the uniform
    // envelope exists to close. The site-level notice above the sign-in tabs carries that information
    // instead — it is read from configuration before any identity is submitted, is identical for every guest
    // on the site, and never varies with what was typed.
    const PHASE3_FAIL = 'We could not verify your stay. Please check your details or contact reception.';

    // THE SERVER'S SENTENCE IS THE ONE THE GUEST READS.
    //
    // This page used to assign PHASE3_FAIL for every non-success and never look at what the server sent,
    // which was correct while there was exactly one sentence. There is no longer one: the property now
    // distinguishes "check what you typed" from "we cannot check right now" from "you are being asked to
    // wait", and that distinction is decided ON THE SERVER, in internal/signinattempt.GuestClass, where the
    // reasoning about what each answer discloses lives. Rendering the server's message keeps exactly one
    // place that decides what a guest is told; re-deriving it here would be a second place to keep correct.
    //
    // PHASE3_FAIL remains the fallback for a transport failure or an empty body — cases where no server
    // sentence exists and the page must still say something that discloses nothing.
    function phase3Message(j) {
      return (j && typeof j.message === 'string' && j.message) ? j.message : PHASE3_FAIL;
    }

    // PHASE3_WAIT_UNTIL is the moment the SERVER said it would consider another submission. It is a local
    // convenience for the countdown and for keeping the button quiet — it is never the thing that decides
    // whether a submission is accepted. A guest who edits it, reloads the page or opens a new tab simply
    // meets the same refusal from the appliance, which holds the restriction in its own database.
    let PHASE3_WAIT_UNTIL = 0;
    let PHASE3_WAIT_TIMER = 0;

    // phase3Countdown renders the wait shrinking, second by second.
    //
    // THE WORDING COMES FROM THE SERVER, THE NUMBER TICKS LOCALLY. The template is built by replacing the
    // number in the server's own sentence with a placeholder, so this page never carries a second copy of
    // the text. A message with no number in it — which is what the server sends when it has no remaining
    // time to quote — is shown once and not counted down, because counting down a number nobody gave us is
    // the one thing worse than not counting down at all.
    function phase3Countdown(errEl, message, seconds) {
      if (PHASE3_WAIT_TIMER) { clearInterval(PHASE3_WAIT_TIMER); PHASE3_WAIT_TIMER = 0; }
      const form = document.getElementById('form-pms');
      const btn = form ? form.querySelector('button[type=submit]') : null;
      if (!(seconds > 0) || !/[0-9]+/.test(message)) {
        errEl.textContent = message;
        PHASE3_WAIT_UNTIL = 0;
        return;
      }
      const tmpl = message.replace(/[0-9]+/, '%WAIT%');
      PHASE3_WAIT_UNTIL = Date.now() + seconds * 1000;
      const render = function () {
        const left = Math.ceil((PHASE3_WAIT_UNTIL - Date.now()) / 1000);
        if (left <= 0) {
          clearInterval(PHASE3_WAIT_TIMER);
          PHASE3_WAIT_TIMER = 0;
          PHASE3_WAIT_UNTIL = 0;
          // The wait is over as far as this page knows. It does NOT announce that the guest is now allowed
          // in — only that they may ask again, which the server will answer for itself.
          errEl.textContent = 'You can try again now.';
          if (btn) btn.disabled = false;
          return;
        }
        errEl.textContent = tmpl.replace('%WAIT%', String(left));
        if (btn) btn.disabled = true;
      };
      render();
      PHASE3_WAIT_TIMER = setInterval(render, 1000);
    }

    // newRequestID returns a CANONICAL RFC-4122 UUID — 36 characters, dashed — because that is the only
    // shape the server accepts.
    //
    // THE FALLBACK USED TO RETURN 32 UNDASHED HEX CHARACTERS, and the server rejects anything that is not a
    // canonical UUID with malformed_request_id. That looks like a corner case and is not: crypto.randomUUID
    // exists only in a SECURE CONTEXT, and a captive portal is served over plain HTTP by definition, so the
    // fallback is the path every real guest takes. Room sign-in therefore failed for everyone, on every
    // browser, before any room number or name was ever looked at — and because the failure is folded into the
    // uniform message, it was indistinguishable from a wrong surname.
    //
    // crypto.getRandomValues IS available in an insecure context, so the bytes were never the problem; only
    // the formatting was. The version and variant bits are set so the value is a real v4 UUID rather than
    // dashed random hex, and Math.random is the last resort for a browser offering neither API.
    function newRequestID() {
      if (window.crypto && window.crypto.randomUUID) { return window.crypto.randomUUID(); }
      const b = new Uint8Array(16);
      if (window.crypto && window.crypto.getRandomValues) {
        window.crypto.getRandomValues(b);
      } else {
        for (let i = 0; i < 16; i++) { b[i] = Math.floor(Math.random() * 256); }
      }
      b[6] = (b[6] & 0x0f) | 0x40; // version 4
      b[8] = (b[8] & 0x3f) | 0x80; // RFC-4122 variant
      const h = Array.from(b, function(x){ return ('0' + x.toString(16)).slice(-2); }).join('');
      return h.slice(0,8) + '-' + h.slice(8,12) + '-' + h.slice(12,16) + '-' + h.slice(16,20) + '-' + h.slice(20);
    }

    // ONE DELIBERATE SUBMISSION, ONE RESOLUTION REQUEST ID. The id is minted where the guest's tap is
    // handled — see the form's submit listener — and never reused by a later tap.
    //
    // IT USED TO BE DERIVED FROM THE DETAILS, on the reasoning that "the same details" means "the same
    // attempt", and that derivation is what froze room sign-in. The key it built read last_name, first_name
    // and reservation_number; room_any — the combined mode, and the one a property actually runs so that a
    // guest is not asked which KIND of identifier they hold — puts what the guest typed in 'verification',
    // which the key never looked at. Every submission from one page therefore carried ONE id, the server
    // correctly replayed the first resolution it had recorded for that id, and a guest who mistyped and then
    // corrected their surname was answered by their own typo until they reloaded the page. The uniform
    // failure message made that indistinguishable from a name that was genuinely wrong.
    //
    // The replacement does not try to be cleverer about which fields the key should read. It removes the
    // question: any field a derivation forgets is another way to freeze the page, and there is no version of
    // that function whose correctness does not depend on remembering to update it.
    //
    // WHAT MINTING FRESHLY DOES NOT COST. The id is still stable for the whole round trip, which is the only
    // idempotency the transport needs: the submit button is disabled while the request is in flight and
    // there is no automatic client-side retry, so one tap makes exactly one call. And duplicate ACCESS was
    // never what this id prevented — a Stay may hold exactly one live Entitlement (the grant refuses a
    // second and a unique index backs it), a retry from the same device against a context it already
    // consumed returns the session that consumption produced, and a resolution that SUCCEEDED is still
    // replayed verbatim for its own id. What a second id can now produce is a second recorded resolution for
    // a second deliberate attempt, which is what an attempt is.

    // POST-STAY. The body is {pin} and nothing else. The server refuses unknown fields outright, so a page
    // that tried to "helpfully" include a room or a stay would break loudly instead of being quietly ignored
    // -- which is the entire point of that strictness.
    document.addEventListener('DOMContentLoaded', function () {
      const f = document.getElementById('form-poststay');
      if (!f) return;
      f.addEventListener('submit', function (e) {
        e.preventDefault();
        const errEl = document.getElementById('ps-err');
        errEl.textContent = '';
        submitPostStay(document.getElementById('ps-pin').value, errEl);
      });
    });

    async function submitPostStay(pin, errEl) {
      let j = {};
      try {
        const r = await fetch('/auth/post-stay-pin', {
          method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({ pin: pin })
        });
        j = await r.json().catch(function(){ return {}; });
      } catch (e) { j = {}; }
      if (j.ok && j.session_id) {
        window.location = (j.redirect_to || '/success') + '?s=' + encodeURIComponent(j.session_id);
        return;
      }
      if (j.ok && j.auth_context_id) {
        // Verified. The conversion is a second call carrying the context the server just issued -- never a
        // subject the page chose.
        let k = {};
        try {
          const r2 = await fetch('/auth/post-stay-pin', {
            method:'POST', headers:{'Content-Type':'application/json'},
            body: JSON.stringify({ auth_context_id: j.auth_context_id })
          });
          k = await r2.json().catch(function(){ return {}; });
        } catch (e) { k = {}; }
        if (k.ok && k.session_id) {
          window.location = (k.redirect_to || '/success') + '?s=' + encodeURIComponent(k.session_id);
          return;
        }
      }
      // Every other answer is the same message -- wrong PIN, expired, revoked, locked out, the room re-let,
      // or post-stay not being offered here at all.
      errEl.textContent = PHASE3_FAIL;
    }

    async function submitPhase3(body, errEl) {
      let j = {};
      try {
        const r = await fetch('/auth/pms/phase3', {
          method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)
        });
        j = await r.json().catch(function(){ return {}; });
      } catch (e) { j = {}; }
      if (j.ok && j.session_id) {
        window.location = (j.redirect_to || '/success') + '?s=' + encodeURIComponent(j.session_id);
        return true;
      }
      if (j.ok && j.needs_choice) {
        PMS_AUTH_CONTEXT = j.auth_context_id || '';
        renderPhase3Choices(j.choices || [], errEl);
        return true;
      }
      // EVERY other answer — including a transport failure — is the same message. No branch here reads the
      // server's outcome, the site configuration, or anything else: one assignment, one string.
      //
      // NOTHING ABOUT THE FAILED ATTEMPT IS CARRIED FORWARD. This function cannot tell a wrong surname from a
      // reply that was lost after the server had already recorded a resolution — that is the uniform envelope
      // working as designed — so it must not make the next submission depend on which of those it was. It used
      // to keep the request id on the reasoning that a lost reply should be retried rather than duplicated,
      // and the cost of that was the guest who mistyped: the next tap carried the spent id, the server
      // replayed the refusal recorded under it, and the corrected surname was never compared against anything.
      // The next tap now mints its own id and is evaluated on its own evidence, and the server no longer
      // treats a recorded refusal as an answer binding on a later request.
      //
      // The boolean is for the CALLER, not the guest: the package-choice handler needs to know whether the
      // offer set it is displaying is still worth showing. It carries no more information than "this did not
      // succeed", which the guest can already see.
      //
      // retry_after_seconds is present on exactly one answer — the restricted one — and it is the SERVER's
      // remaining time. phase3Countdown renders it shrinking; it decides nothing.
      phase3Countdown(errEl, phase3Message(j), j.retry_after_seconds || 0);
      return false;
    }

    // RETURNING TO SIGN-IN AFTER A FAILED SELECTION.
    //
    // A failed grant used to re-enable the same buttons and leave them on screen, so the guest was looking at
    // three package choices that had just been refused. They read as still valid, so the natural response is
    // to press one again -- which is exactly what happened on 2026-09-07, three times in fourteen seconds,
    // each one failing identically.
    //
    // WHY SIGN-IN RATHER THAN A RETRY BUTTON. The guest-facing answer is uniform by design: the portal is not
    // told whether the grant failed because the Auth Context was already spent, because the offer expired, or
    // for an internal reason. So it cannot know whether pressing the same button again is a safe retry or an
    // attempt to spend a consumed context. Going back to sign-in is safe under every one of those readings,
    // and what makes it safe is a SERVER invariant rather than anything this page remembers: signing in again
    // proves identity again and may well produce a second Auth Context, but a Stay holds exactly one live
    // Entitlement, so the second context cannot become a second grant. The page deliberately keeps no state
    // from the failed attempt — a portal that had to remember the right thing to stay safe would be one
    // forgotten assignment away from not being.
    function resetPhase3ToSignIn(errEl) {
      const box = document.getElementById('pms-choices');
      const form = document.getElementById('form-pms');
      box.innerHTML = '';
      box.style.display = 'none';
      form.style.display = '';
      // The uniform message. A failed GRANT is not a failed identity check, so there is no server sentence
      // to prefer here and nothing about which stage failed.
      errEl.textContent = PHASE3_FAIL;
      const btn = form.querySelector('button[type=submit]');
      if (btn) btn.disabled = false;
    }

    function renderPhase3Choices(choices, errEl) {
      const box = document.getElementById('pms-choices');
      const form = document.getElementById('form-pms');
      box.innerHTML = '';
      if (!choices.length) { errEl.textContent = PHASE3_FAIL; return; }
      const h = document.createElement('p');
      h.className = 'small';
      h.textContent = 'Choose your internet package';
      box.appendChild(h);
      choices.forEach(function(c) {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'choice';
        b.dataset.packageRevisionId = c.package_revision_id;
        b.textContent = c.code + ' — ' + Math.round((c.down_kbps||0)/1000) + ' Mbps down';
        b.addEventListener('click', async function() {
          box.querySelectorAll('button').forEach(function(x){ x.disabled = true; });
          errEl.textContent = '';
          const ok = await submitPhase3({ auth_context_id: PMS_AUTH_CONTEXT, package_revision_id: c.package_revision_id }, errEl);
          // On success submitPhase3 has already navigated away. On failure the offer set can no longer be
          // trusted, so it is taken down rather than re-enabled.
          if (!ok) resetPhase3ToSignIn(errEl);
        });
        box.appendChild(b);
      });
      form.style.display = 'none';
      box.style.display = 'block';
    }

    // PMS — single-step form: room + secondary field. Mode decides which
    // server-side field we fill from the secondary input.
    document.getElementById('form-pms').addEventListener('submit', async (e) => {
      e.preventDefault();
      const btn = e.target.querySelector('button[type=submit]');
      const errEl = document.getElementById('pms-err');
      errEl.textContent = ''; btn.disabled = true;
      const room = document.getElementById('pms-room').value.trim();
      const sec  = document.getElementById('pms-secondary');
      const val  = sec.value.trim();
      const mode = sec.dataset.mode || 'either';
      const body = { room };
      if (mode === 'room_firstname')        body.first_name = val;
      else if (mode === 'room_reservation') body.reservation_number = val;
      else if (mode === 'room_lastname')    body.last_name = val;
      // room_any sends the value AS TYPED in one field and lets the server compare it against first name,
      // last name and reservation number together. The browser deliberately does not look at the value: the
      // legacy branch below is what happens when it does, and it is why a surname with a digit in it was
      // submitted as a reservation number and failed.
      else if (mode === 'room_any')         body.verification = val;
      else { // legacy "either" — kept exactly as it was for sites still configured with it.
        if (/^[A-Z0-9\-]+$/i.test(val) && /\d/.test(val)) body.reservation_number = val;
        else body.last_name = val;
      }
      try {
        if (!PHASE3_PMS) {
          const r = await fetch('/auth/pms/verify', {
            method:'POST', headers:{'Content-Type':'application/json'},
            body: JSON.stringify(body)
          });
          const j = await r.json().catch(() => ({}));
          if (!r.ok) { errEl.textContent = j.error || 'Verification failed'; return; }
          window.location = '/success?s=' + encodeURIComponent(j.session_id || '') +
                            '&t=' + encodeURIComponent(j.duration_seconds || 0);
          return;
        }
        // PHASE 3: the Stay-resolution flow. THIS tap is one resolution request, so it gets an id of its
        // own, minted here where the deliberate submission begins. It bounds the round trip — a reply lost
        // in transit could be retried against the same id and resolve once — and it deliberately does not
        // outlive the tap: the next tap is a new submission of whatever the guest has now typed, and has to
        // be evaluated on that evidence rather than answered from what the last one recorded.
        body.request_id = newRequestID();
        await submitPhase3(body, errEl);
      } finally {
        // A submission that ended in a restriction leaves the button disabled until the countdown clears it.
        // This is courtesy, not enforcement: the appliance refuses an early submission whatever this page
        // does with its own button.
        if (!PHASE3_WAIT_UNTIL || Date.now() >= PHASE3_WAIT_UNTIL) btn.disabled = false;
      }
    });
  </script>
  </main>

</body></html>`

const successHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connected</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; max-width: 460px; margin: 8vh auto; padding: 24px; text-align:center; }
  .ok { font-size: 3rem; color: #1a9e4a; }
  h1 { margin: 8px 0; }
  p { color: #666; }
  a.btn { display:inline-block; margin-top:16px; padding:10px 16px; border:1px solid #ccc; border-radius:8px; color:#333; text-decoration:none; }
  #commerce { margin-top:28px; text-align:left; border-top:1px solid #eee; padding-top:18px; }
  #commerce h2 { font-size:1.05rem; }
  .pkg { border:1px solid #ddd; border-radius:10px; padding:12px 14px; margin:10px 0; }
  .pkg h3 { margin:0 0 6px; font-size:1rem; }
  .pkg .meta { color:#666; font-size:.85rem; line-height:1.5; }
  .pkg button, #cx-confirm { padding:8px 14px; border:1px solid #0a6cff; background:#0a6cff; color:#fff; border-radius:8px; cursor:pointer; }
  .pkg button[disabled], #cx-confirm[disabled] { opacity:.5; cursor:default; }
  #cx-note { color:#666; font-size:.85rem; margin-top:8px; }
  .cx-err { color:#b00020; }
  #timeleft { margin-top:20px; padding:12px 14px; border:1px solid #ddd; border-radius:10px; text-align:left; }
  #timeleft .tl-main { font-size:1.05rem; font-weight:600; }
  #timeleft .tl-note { color:#666; font-size:.85rem; margin-top:4px; }
  #devices { margin-top:28px; text-align:left; border-top:1px solid #eee; padding-top:18px; }
  #devices h2 { font-size:1.05rem; }
  #devices p.lead { margin:.2rem 0 .9rem; }
  .dev { border:1px solid #ddd; border-radius:10px; padding:12px 14px; margin:10px 0; }
  .dev .name { font-weight:600; }
  .dev .meta { color:#666; font-size:.85rem; line-height:1.5; margin-top:2px; }
  .dev button { margin-top:8px; padding:8px 14px; border:1px solid #0a6cff; background:#fff; color:#0a6cff; border-radius:8px; cursor:pointer; }
  .dev button[disabled] { opacity:.5; cursor:default; }
  .dev .inuse { color:#666; font-size:.85rem; margin-top:8px; }
  #dv-note { font-size:.9rem; margin-top:10px; }
  .dv-done { color:#1a9e4a; }
  .dv-err { color:#b00020; }
</style>
</head><body>
  <div class="ok">✓</div>
  <h1>You're online</h1>
  <p>Session: {{.SessionID}}<br>
     {{if .DurationSeconds}}Time remaining: {{.HumanRemaining}}{{else}}No time limit{{end}}</p>
  <a class="btn" href="/status">Status</a>
  <form method="POST" action="/logout" style="display:inline"><button type="submit" style="margin-left:8px">Disconnect</button></form>

  {{if .CommerceEnabled}}
  <div id="commerce" data-commerce="on">
    <h2>Available packages</h2>
    <div id="cx-list">Loading…</div>
    <div id="cx-quote" hidden></div>
    <div id="cx-note"></div>
  </div>
  <script>
  (function(){
    var list = document.getElementById('cx-list');
    var quoteBox = document.getElementById('cx-quote');
    var note = document.getElementById('cx-note');
    var busy = false;
    function fmtBytes(n){ if(!n) return '∞'; var u=['B','KB','MB','GB','TB']; var i=0; while(n>=1024&&i<u.length-1){n/=1024;i++;} return n.toFixed(n<10&&i>0?1:0)+u[i]; }
    function fmtDur(s){ if(!s) return '∞'; var h=Math.floor(s/3600),m=Math.floor((s%3600)/60); return h>0?(h+'h'+(m?(' '+m+'m'):'')):(m+'m'); }
    function unavailable(msg){ note.className='cx-err'; note.textContent = msg||'This option is unavailable right now.'; }
    function clearNote(){ note.className=''; note.textContent=''; }
    function loadPackages(){
      clearNote();
      fetch('/api/commerce/packages', {headers:{'Accept':'application/json'}}).then(function(r){
        if(!r.ok){ list.textContent=''; unavailable(); return null; }
        return r.json();
      }).then(function(data){
        if(!data){ return; }
        var pkgs = (data.packages||[]);
        if(pkgs.length===0){ list.textContent='No packages are available for you right now.'; return; }
        list.innerHTML='';
        pkgs.forEach(function(p){
          var d = p.display||{};
          var el = document.createElement('div'); el.className='pkg';
          var speed = (d.down_kbps? (Math.round(d.down_kbps/1000)+' Mbps down'):'')+(d.up_kbps? (' / '+Math.round(d.up_kbps/1000)+' up'):'');
          el.innerHTML = '<h3></h3><div class="meta"></div>';
          el.querySelector('h3').textContent = d.name || 'Package';
          el.querySelector('.meta').textContent =
            (speed? (speed+' · '):'') +
            'Data: '+fmtBytes(d.data_quota_bytes)+' · Time: '+fmtDur(d.time_quota_seconds)+
            ' · Devices: '+(d.max_concurrent_devices||1)+' · Ends: '+(d.end_mode||'MANUAL_END');
          var btn = document.createElement('button'); btn.textContent='Select';
          btn.addEventListener('click', function(){ requestQuote(p.package_id, btn); });
          el.appendChild(btn);
          list.appendChild(el);
        });
      }).catch(function(){ unavailable(); });
    }
    function requestQuote(pkgId, btn){
      if(busy) return; busy=true; if(btn) btn.disabled=true; clearNote();
      fetch('/api/commerce/quote', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({package_id: pkgId})})
        .then(function(r){ return r.ok? r.json() : null; })
        .then(function(q){
          busy=false; if(btn) btn.disabled=false;
          if(!q || !q.quote_id){ unavailable(); return; }
          showQuote(q);
        }).catch(function(){ busy=false; if(btn) btn.disabled=false; unavailable(); });
    }
    function showQuote(q){
      var d = q.display||{};
      quoteBox.hidden=false;
      quoteBox.innerHTML =
        '<div class="pkg"><h3>Confirm your package</h3>'+
        '<div class="meta">'+(d.name||'Package')+' — free · Devices: '+(d.max_concurrent_devices||1)+
        ' · Ends: '+(d.end_mode||'MANUAL_END')+'</div>'+
        '<div class="meta">Offer expires: '+ (q.expires_at||'') +'</div>'+
        '<button id="cx-confirm">Confirm</button></div>';
      var cbtn = document.getElementById('cx-confirm');
      cbtn.addEventListener('click', function(){ confirmQuote(q.quote_id, cbtn); });
      list.hidden = true;
    }
    function confirmQuote(quoteId, cbtn){
      if(busy) return; busy=true; cbtn.disabled=true; clearNote();
      fetch('/api/commerce/confirm', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({quote_id: quoteId})})
        .then(function(r){ return r.ok? r.json() : null; })
        .then(function(res){
          busy=false;
          if(!res || !res.entitlement_id){ cbtn.disabled=false; unavailable('That offer expired or is no longer available.'); return; }
          quoteBox.innerHTML = '<div class="pkg"><h3>Package active</h3><div class="meta">Your package is now active. Enjoy your connection.</div></div>';
        }).catch(function(){ busy=false; cbtn.disabled=false; unavailable(); });
    }
    loadPackages();
  })();
  </script>
  {{end}}


  <!-- YOUR TIME (Phase 6, DARK).
       Hidden until the appliance answers with an aggregate package, so on every other package -- which is
       all of them today -- the page is unchanged.

       TWO CLOCKS, BOTH SHOWN. Remaining online time counts down only while the guest is connected; the hard
       expiry is a calendar instant that arrives whether they used the minutes or not. Showing only the
       minutes would be the comfortable half-truth: a guest with ninety minutes left and a window closing in
       ten would plan their evening around a number that is about to stop mattering. -->
  <div id="timeleft" hidden>
    <div class="tl-main"><span id="tl-remaining"></span> of internet time left</div>
    <div class="tl-note">This counts down only while you are connected.</div>
    <div class="tl-note" id="tl-expiry" hidden></div>
  </div>
  <script>
  (function(){
    var box = document.getElementById('timeleft');
    var main = document.getElementById('tl-remaining');
    var exp = document.getElementById('tl-expiry');
    function human(s){
      if(s <= 0) return 'no time';
      var h = Math.floor(s/3600), m = Math.round((s%3600)/60);
      if(h > 0) return h + ' hour' + (h===1?'':'s') + (m ? ' ' + m + ' min' : '');
      if(m > 0) return m + ' minute' + (m===1?'':'s');
      return 'less than a minute';
    }
    function day(iso){
      var t = Date.parse(iso);
      if(isNaN(t)) return '';
      var d = new Date(t);
      return d.toLocaleString();
    }
    fetch('/status', {headers:{'Accept':'application/json'}})
      .then(function(r){ return r.ok ? r.json() : null; })
      .then(function(st){
        if(!st || st.time_mode !== 'AGGREGATE_ONLINE_TIME') return;  // every other package: nothing changes
        main.textContent = human(st.remaining_online_seconds);
        if(st.hard_expiry){
          var when = day(st.hard_expiry);
          if(when){
            exp.textContent = 'Your access ends on ' + when + ', whether or not the time is used.';
            exp.hidden = false;
          }
        }
        box.hidden = false;
      })
      .catch(function(){ /* the ordinary page is the fallback */ });
  })();
  </script>

  <!-- YOUR DEVICES (Phase 6, DARK).
       The panel starts HIDDEN and is only ever shown after the appliance answers with a list. On an
       appliance where the capability is not deployed, or where the hotel has turned the setting off, the
       answer is the uniform non-success and this panel simply never appears — the guest sees the ordinary
       success page and learns nothing about whether device management exists here.

       What the guest sees about a device is when it was last used and whether it is online. There is no MAC
       address and no internal identifier anywhere in the text: a MAC would hand every guest on a shared
       network a stable identifier for somebody's phone, and the internal ids are not theirs to see. The
       opaque id travels in a data attribute because the release call needs a target, and it is never
       rendered. -->
  <div id="devices" hidden>
    <h2>Your devices</h2>
    <p class="lead">These are the devices using your internet access. Removing one frees its place for
       another device — nothing about your access changes, and the removed device can connect again at
       any time.</p>
    <div id="dv-list"></div>
    <div id="dv-note"></div>
  </div>
  <script>
  (function(){
    var panel = document.getElementById('devices');
    var list  = document.getElementById('dv-list');
    var note  = document.getElementById('dv-note');
    var busy  = false;

    function ago(iso){
      if(!iso) return 'Last used: unknown';
      var t = Date.parse(iso);
      if(isNaN(t)) return 'Last used: unknown';
      var mins = Math.floor((Date.now()-t)/60000);
      if(mins < 1)  return 'Last used: just now';
      if(mins < 60) return 'Last used: ' + mins + ' minute' + (mins===1?'':'s') + ' ago';
      var hrs = Math.floor(mins/60);
      if(hrs < 24)  return 'Last used: ' + hrs + ' hour' + (hrs===1?'':'s') + ' ago';
      var days = Math.floor(hrs/24);
      return 'Last used: ' + days + ' day' + (days===1?'':'s') + ' ago';
    }
    // Every refusal is the same sentence. The appliance does not tell the guest whether a removal failed
    // because the device came back online, because it was already removed, or because they have tried too
    // many times — and neither does this page.
    function refused(){ note.className='dv-err'; note.textContent='That didn’t work. Please try again in a moment.'; }
    function clearNote(){ note.className=''; note.textContent=''; }

    function render(devices){
      list.innerHTML='';
      devices.forEach(function(d, i){
        var el = document.createElement('div'); el.className='dev';
        var name = document.createElement('div'); name.className='name';
        name.textContent = 'Device ' + (i+1);
        var meta = document.createElement('div'); meta.className='meta';
        meta.textContent = (d.online ? 'Connected now' : 'Not connected') + ' · ' + ago(d.last_seen);
        el.appendChild(name); el.appendChild(meta);

        if(d.removable){
          var btn = document.createElement('button');
          btn.type='button';
          btn.textContent='Remove this device';
          btn.setAttribute('aria-label','Remove device ' + (i+1));
          btn.addEventListener('click', function(){ release(d.id, btn, i+1); });
          el.appendChild(btn);
        } else {
          // An online device is never removable, and saying so plainly is better than offering a button
          // that will refuse: the guest is told what to do instead.
          var why = document.createElement('div'); why.className='inuse';
          why.textContent = d.online
            ? 'In use right now, so it can’t be removed. Disconnect it from the Wi‑Fi first.'
            : 'This device can’t be removed right now.';
          el.appendChild(why);
        }
        list.appendChild(el);
      });
    }

    function load(showPanel){
      fetch('/devices/list', {method:'POST', headers:{'Content-Type':'application/json'}, body:'{}'})
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(res){
          if(!res || !res.ok || !res.devices || res.devices.length===0){
            // Not deployed, switched off, nothing to show, or a failure — one behaviour for all of them.
            if(showPanel) panel.hidden = true;
            return;
          }
          render(res.devices);
          panel.hidden = false;
        })
        .catch(function(){ if(showPanel) panel.hidden = true; });
    }

    function release(id, btn, n){
      if(busy) return;
      if(!window.confirm('Remove device ' + n + '? It will lose its place, and can connect again at any time.')) return;
      busy = true; btn.disabled = true; clearNote();
      fetch('/devices/release', {method:'POST', headers:{'Content-Type':'application/json'},
                                 body: JSON.stringify({device_id: id})})
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(res){
          busy = false;
          if(!res || !res.ok){ btn.disabled=false; refused(); return; }
          note.className='dv-done';
          note.textContent = res.message || 'That device has been removed and its place is free.';
          load(false);
        })
        .catch(function(){ busy=false; btn.disabled=false; refused(); });
    }

    load(true);
  })();
  </script>
</body></html>`
