package main

const landingHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Wi-Fi Access</title>
<style>
  :root { color-scheme: light dark; font-family: -apple-system, system-ui, sans-serif; }
  body { max-width: 440px; margin: 8vh auto; padding: 24px; }
  h1 { font-size: 1.4rem; margin: 0 0 8px; }
  p  { color: #666; margin: 0 0 20px; }
  .tabs { display:flex; gap:0; border-bottom:1px solid #ddd; margin-bottom:18px; }
  .tab { flex:1; padding:10px 12px; text-align:center; cursor:pointer;
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
  #pms-choices button.choice { display:block; width:100%; text-align:left; margin:8px 0; padding:12px 14px;
    border:1px solid #ccc; border-radius:10px; background:#fff; cursor:pointer; font-size:1rem; }
  #pms-choices button.choice[disabled] { opacity:.5; cursor:default; }
  .small { font-size:.8rem; color:#777; }
</style>
</head><body>
  <h1>Welcome</h1>
  <p>Choose how you'd like to connect.</p>

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
      if (enabled.length === 0) { tabsEl.innerHTML = '<div class="small">No auth methods configured.</div>'; return; }
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
    let PMS_AUTH_CONTEXT = '';
    const PHASE3_FAIL = 'We could not verify your stay. Please check your details or contact reception.';

    function newRequestID() {
      if (window.crypto && window.crypto.randomUUID) { return window.crypto.randomUUID(); }
      const b = new Uint8Array(16); (window.crypto || {}).getRandomValues && window.crypto.getRandomValues(b);
      return Array.from(b, function(x){ return ('0'+x.toString(16)).slice(-2); }).join('');
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
        PMS_REQUEST_ID = '';
        window.location = (j.redirect_to || '/success') + '?s=' + encodeURIComponent(j.session_id);
        return;
      }
      if (j.ok && j.needs_choice) {
        PMS_AUTH_CONTEXT = j.auth_context_id || '';
        renderPhase3Choices(j.choices || [], errEl);
        return;
      }
      // EVERY other answer — including a transport failure — is the same message.
      PMS_REQUEST_ID = '';
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
      if (mode === 'room_firstname')      body.first_name = val;
      else if (mode === 'room_reservation') body.reservation_number = val;
      else if (mode === 'room_lastname')    body.last_name = val;
      else { // either — guess: if all-digits-or-hyphens and starts with letters, treat as reservation
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
        if (!PMS_REQUEST_ID) { PMS_REQUEST_ID = newRequestID(); }
        body.request_id = PMS_REQUEST_ID;
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
</body></html>`
