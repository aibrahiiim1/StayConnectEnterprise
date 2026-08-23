package main

const landingHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Wi-Fi Access</title>
<style>
  :root { color-scheme: light dark; font-family: -apple-system, system-ui, sans-serif; }
  /* 8vh top margin is pleasant on a laptop and wasteful on a phone in landscape, where it pushed the first
     field below the fold. Clamped so it stays generous on a large screen and modest on a short one. */
  body { max-width: 440px; margin: clamp(16px, 8vh, 96px) auto; padding: 24px; }
  h1 { font-size: 1.4rem; margin: 0 0 8px; }
  p  { color: #666; margin: 0 0 20px; }
  /* WITH PMS ENABLED THERE CAN BE SIX TABS on a 360px phone. flex:1 divided the row evenly and the labels
     ran into each other; the sign-in method a guest needs was unreadable on the device almost every guest
     uses. They now size to their content and scroll horizontally when they do not fit, which keeps every
     label legible instead of shrinking all of them until none are. */
  .tabs { display:flex; gap:0; border-bottom:1px solid #ddd; margin-bottom:18px;
          overflow-x:auto; -webkit-overflow-scrolling:touch; scrollbar-width:none; }
  .tabs::-webkit-scrollbar { display:none; }
  .tab { flex:0 0 auto; padding:10px 14px; text-align:center; cursor:pointer; white-space:nowrap;
         color:#666; font-size:.92rem; border-bottom:2px solid transparent; user-select:none; }
  .tab.active { color:inherit; border-bottom-color:#0a6cff; font-weight:600; }
  .panel { display:none; }
  .panel.active { display:block; }
  label { display:block; font-size:.9rem; margin-bottom:6px; }
  input[type=text], input[type=email], input[type=tel] {
    width:100%; padding:12px 14px; font-size:1.1rem;
    box-sizing:border-box; border:1px solid #ccc; border-radius:8px;
  }
  input[name=code] { letter-spacing:8px; text-align:center; font-variant-numeric: tabular-nums; }
  input[name=voucher] { letter-spacing:2px; text-transform:uppercase; }
  button { width:100%; margin-top:16px; padding:12px; font-size:1rem; font-weight:600;
           border:0; border-radius:8px; background:#0a6cff; color:#fff; cursor:pointer; }
  button:hover:not(:disabled) { background:#0858d6; }
  button:disabled { opacity:.5; cursor:wait; }
  button.link { background:none; color:#0a6cff; font-weight:400; padding:6px; margin-top:8px; }
  .err { color:#b00020; margin-top:12px; min-height:1.2em; font-size:.9rem; }
  /* These hardcoded background:#fff while the page declares color-scheme: light dark, so on a phone in
     dark mode the label inherited light text onto a white button and the guest's package choices were
     invisible. Canvas/CanvasText follow the active scheme, so the choice is readable either way. */
  #pms-choices button.choice { display:block; width:100%; text-align:left; margin:8px 0; padding:12px 14px;
    border:1px solid #ccc; border-radius:10px; background:Canvas; color:CanvasText; cursor:pointer;
    font-size:1rem; }
  #pms-choices button.choice[disabled] { opacity:.5; cursor:default; }
  .small { font-size:.8rem; color:#777; }
  /* A site-level advisory, not an error the guest caused. Amber rather than red, and it sits above the
     sign-in choices because it changes what the guest should expect from all of them. */
  .notice { display:none; margin:0 0 14px; padding:12px 14px; border-radius:8px; font-size:.9rem;
            background:#fff8e1; border:1px solid #f0d38a; color:#6b4e00; }
  .notice.show { display:block; }
</style>
</head><body>
  <h1>Welcome</h1>
  <p>Choose how you'd like to connect.</p>

  <div class="notice" id="site-notice" role="status" aria-live="polite"></div>

  <div class="tabs" id="tabs"></div>

  <!-- Voucher panel -->
  <div class="panel" id="panel-voucher">
    <form method="POST" action="/auth/voucher">
      <label for="voucher">Voucher code</label>
      <input id="voucher" name="code" type="text" autocomplete="off" required maxlength="32" placeholder="XXXX-XXXX-XXXX">
      <button type="submit">Connect</button>
      <div class="err">{{.Error}}</div>
    </form>
  </div>

  <!-- Guest account (username + password) panel -->
  <div class="panel" id="panel-account">
    <form method="POST" action="/auth/credentials" autocomplete="off">
      <label for="ga-username">Username</label>
      <input id="ga-username" name="username" type="text" autocomplete="username" required maxlength="64" placeholder="username">
      <label for="ga-password" style="margin-top:10px">Password</label>
      <input id="ga-password" name="password" type="password" autocomplete="current-password" required maxlength="128" placeholder="password">
      <button type="submit" style="margin-top:10px">Connect</button>
      <div class="err">{{.Error}}</div>
    </form>
  </div>

  <!-- Email panel -->
  <div class="panel" id="panel-email">
    <form data-otp="email" data-stage="dest" autocomplete="off">
      <label for="email">Email address</label>
      <input id="email" name="dest" type="email" required placeholder="you@example.com" autocomplete="email">
      <button type="submit">Send code</button>
      <div class="err"></div>
    </form>
    <form data-otp="email" data-stage="code" autocomplete="off" style="display:none">
      <p class="small">We sent a 6-digit code to <span class="dest"></span>.</p>
      <label>Verification code</label>
      <input name="code" type="text" inputmode="numeric" pattern="[0-9]*" required maxlength="6" placeholder="------">
      <button type="submit">Verify</button>
      <button type="button" class="link" data-resend>Try a different email</button>
      <div class="err"></div>
    </form>
  </div>

  <!-- PMS / Room panel — guest enters room number plus one verification field -->
  <div class="panel" id="panel-pms">
    <form id="form-pms" autocomplete="off">
      <label for="pms-room">Room number</label>
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
      <label for="phone">Phone number</label>
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

  <script>
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
      either:           "Last name OR reservation number",
    };
    const challenges = {}; // channel -> challenge_id

    function setTab(id) {
      document.querySelectorAll('.tab').forEach(el => el.classList.toggle('active', el.dataset.tab === id));
      document.querySelectorAll('.panel').forEach(el => el.classList.remove('active'));
      const t = Tabs[id]; if (t) document.getElementById(t.panel).classList.add('active');
    }

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
      if (cfg.internet_packages_available === false) {
        const n = document.getElementById('site-notice');
        n.textContent = 'Internet access is not available here at the moment. You can still sign in, but there is nothing to connect you to yet — please let reception know.';
        n.classList.add('show');
      }
      if (enabled.length === 0) {
        tabsEl.innerHTML = '<div class="small">There is no way to sign in on this network yet. Please contact reception.</div>';
        return;
      }
      enabled.forEach(id => {
        const el = document.createElement('div');
        el.className = 'tab'; el.dataset.tab = id; el.textContent = Tabs[id].label;
        el.addEventListener('click', () => setTab(id));
        tabsEl.appendChild(el);
      });
      setTab(enabled[0]);
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
    let PMS_REQUEST_ID = '';
    // PMS_ATTEMPT_KEY is the details the current request id belongs to. The id must survive a retry of the
    // SAME attempt and must not survive a different one — see phase3RequestID below.
    let PMS_ATTEMPT_KEY = '';
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

    function newRequestID() {
      if (window.crypto && window.crypto.randomUUID) { return window.crypto.randomUUID(); }
      const b = new Uint8Array(16); (window.crypto || {}).getRandomValues && window.crypto.getRandomValues(b);
      return Array.from(b, function(x){ return ('0'+x.toString(16)).slice(-2); }).join('');
    }

    // phase3RequestID decides whether this submission is a RETRY of the attempt already in flight or a NEW
    // attempt, and returns the id accordingly.
    //
    // This distinction is the whole value of the request id, and getting it wrong fails in both directions.
    // Minting a fresh id every time means a guest on a flaky lobby connection — the normal case for a captive
    // portal — records a second resolution and a second Auth Context every time they tap again, which is
    // precisely the duplication the id exists to prevent. Never minting a new one means a guest who mistyped
    // their room is stuck replaying the failed attempt forever, because the server correctly returns the same
    // answer for the same id.
    //
    // The details themselves are the discriminator: same details, same attempt.
    function phase3RequestID(body) {
      const key = JSON.stringify([body.room||'', body.last_name||'', body.first_name||'', body.reservation_number||'']);
      if (key !== PMS_ATTEMPT_KEY || !PMS_REQUEST_ID) {
        PMS_ATTEMPT_KEY = key;
        PMS_REQUEST_ID = newRequestID();
      }
      return PMS_REQUEST_ID;
    }

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
        // A new attempt after this one must be a NEW resolution, not a replay of a spent request id.
        PMS_REQUEST_ID = ''; PMS_ATTEMPT_KEY = '';
        window.location = (j.redirect_to || '/success') + '?s=' + encodeURIComponent(j.session_id);
        return;
      }
      if (j.ok && j.needs_choice) {
        PMS_AUTH_CONTEXT = j.auth_context_id || '';
        renderPhase3Choices(j.choices || [], errEl);
        return;
      }
      // EVERY other answer — including a transport failure — is the same message. No branch here reads the
      // server's outcome, the site configuration, or anything else: one assignment, one string.
      //
      // The request id is deliberately KEPT. A non-success can mean the guest's details were wrong, but it can
      // equally mean the attempt was abandoned at the server's response-time budget or lost in transit with
      // the resolution already recorded. Discarding the id would turn the second of those into a duplicate
      // resolution; keeping it lets the retry return the same Auth Context. A guest who corrects their details
      // gets a new id automatically, because the details are what the id is keyed to.
      errEl.textContent = PHASE3_FAIL;
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
          await submitPhase3({ auth_context_id: PMS_AUTH_CONTEXT, package_revision_id: c.package_revision_id }, errEl);
          box.querySelectorAll('button').forEach(function(x){ x.disabled = false; });
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
        // PHASE 3: the Stay-resolution flow. The request id makes a double-tap or a retry on a bad
        // connection resolve ONCE — without it the guest's second attempt records a second resolution.
        body.request_id = phase3RequestID(body);
        await submitPhase3(body, errEl);
      } finally { btn.disabled = false; }
    });
  </script>
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
