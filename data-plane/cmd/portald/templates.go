package main

// THE GUEST PORTAL'S PAGES.
//
// Four pages, one look: the sign-in page (landingHTML), the package choice (packagesHTML), "You're online"
// (successHTML) and the friendly failure page (errorHTML). All four are drawn from the same token sheet and
// the same six layouts (portal_css.go), carry the hotel's name, logo and colours from the published design,
// and are rendered in the guest's language with the direction that language reads in.
//
// Nothing here decides anything. Every form posts where it always did, with the fields it always had; every
// fetch goes to the route and carries the body it always carried; every refusal is the sentence the server
// chose, shown in the guest's language. What changed is how it looks and which language it is read in.

// ---- icons: inline, drawn with currentColor, never meaning on their own --------------------------------------

const (
	svgOpen      = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"`
	iconGlobe    = svgOpen + ` class="i-globe"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.4 2.6 3.7 5.6 3.7 9s-1.3 6.4-3.7 9c-2.4-2.6-3.7-5.6-3.7-9S9.6 5.6 12 3z"/></svg>`
	iconChevDown = svgOpen + ` class="i-chev"><path d="m6 9 6 6 6-6"/></svg>`
	iconChevNext = svgOpen + ` class="c-chev"><path d="m9 6 6 6-6 6"/></svg>`
	iconInfo     = svgOpen + `><circle cx="12" cy="12" r="9"/><path d="M12 11v5.5M12 7.6v.1"/></svg>`
	iconWifi     = svgOpen + ` stroke-width="2"><path d="M5 12.6a10.5 10.5 0 0 1 14 0M1.9 9.1a15 15 0 0 1 20.2 0M8.5 16.1a5.5 5.5 0 0 1 7 0"/><path d="M12 20h.01"/></svg>`
	iconCheck    = svgOpen + ` stroke-width="2.4"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg>`
	iconAlert    = svgOpen + ` stroke-width="2.2"><path d="M12 7.5v6M12 17v.1"/></svg>`
	iconBulb     = svgOpen + `><path d="M9.5 18h5M10.5 21h3"/><path d="M12 3a6 6 0 0 0-3.7 10.7c.8.7 1.2 1.4 1.2 2.3v.5h5V16c0-.9.4-1.6 1.2-2.3A6 6 0 0 0 12 3z"/></svg>`
	iconClose    = svgOpen + ` stroke-width="2"><path d="M6 6l12 12M18 6 6 18"/></svg>`
)

// ---- the help sheet and the product attribution, on every guest page ---------------------------------------

// helpOpen opens the lightbulb and its sheet. It is a <details>: with no script the lightbulb is a native
// disclosure that shows the tips in place, keyboard-operable with nothing added. guestHelpScript upgrades it
// to a modal sheet (role="dialog", focus kept inside, Esc and the backdrop close it, focus returns to the
// lightbulb). The tips explain; they never carry an error, a countdown or a notice -- those stay on the page.
const helpOpen = `
<details class="sc-help" id="sc-help"><summary class="help-btn" data-i18n-aria="help.button" aria-label="{{index .T "help.button"}}" title="{{index .T "help.button"}}">` + iconBulb + `</summary><div class="help-sheet" id="sc-help-sheet"><div class="help-card">
<div class="help-head"><h2 class="help-title" id="sc-help-title" data-i18n="help.title" data-i18n-en="Help with signing in">{{index .T "help.title"}}</h2><button type="button" class="help-close" data-help-close data-i18n-aria="help.close" aria-label="{{index .T "help.close"}}" title="{{index .T "help.close"}}">` + iconClose + `</button></div>`

// helpClose ends the sheet: what to do when something does not work, and the hotel's own help line.
const helpClose = `
<p class="help-fail" data-i18n="help.fail" data-i18n-en="Something not working? Please contact reception — they are happy to help.">{{index .T "help.fail"}}</p>
<p class="help-hotel" id="help-hotel" dir="auto"{{if not .Brand.Help}} hidden{{end}}>{{.Brand.Help}}</p>
</div></div></details>`

// ogAttribution is the product's one line on a guest page: small, at the foot, after everything the hotel
// says. "OneGate" is a name and is never translated; it sits on a light chip so the black half stays black on
// any surface a hotel chooses, and it is isolated left-to-right so an Arabic page keeps it whole.
const ogAttribution = `
<p class="sc-by"><span data-i18n="brand.by" data-i18n-en="Wi-Fi by">{{index .T "brand.by"}}</span> <bdi class="og-mark" dir="ltr" lang="en"><b class="og-one">One</b><b class="og-gate">Gate</b></bdi></p>`

// guestFoot is the foot of every page after sign-in: the lightbulb (general help and the hotel's line) and the
// attribution.
const guestFoot = `
<div class="sc-foot sc-foot--page">` + helpOpen + helpClose + ogAttribution + `
</div>`

// guestHelpScript turns the <details> into a modal sheet. Presentation only: it opens and closes a panel that
// is already on the page.
const guestHelpScript = `
<script nonce="{{.Nonce}}">
(function () {
  var d = document.getElementById('sc-help');
  var sheet = document.getElementById('sc-help-sheet');
  if (!d || !sheet) return;
  var sum = d.querySelector('summary');
  // The sheet moves to <body>: a card with a backdrop filter or a transform would otherwise trap a fixed
  // overlay inside itself. Every id, and every data-i18n the language pass reaches, travels with it.
  d.open = false;
  d.setAttribute('data-modal', '');
  sheet.hidden = true;
  sheet.className += ' is-modal';
  sheet.setAttribute('role', 'dialog');
  sheet.setAttribute('aria-modal', 'true');
  sheet.setAttribute('aria-labelledby', 'sc-help-title');
  document.body.appendChild(sheet);
  sum.setAttribute('aria-haspopup', 'dialog');
  sum.setAttribute('aria-expanded', 'false');
  function focusables() {
    var all = sheet.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])');
    var out = [];
    for (var i = 0; i < all.length; i++) { if (all[i].offsetWidth || all[i].offsetHeight) out.push(all[i]); }
    return out;
  }
  function show() {
    sheet.hidden = false;
    sum.setAttribute('aria-expanded', 'true');
    document.documentElement.className += ' sc-help-open';
    var c = sheet.querySelector('[data-help-close]');
    if (c) c.focus();
  }
  function hide() {
    sheet.hidden = true;
    sum.setAttribute('aria-expanded', 'false');
    document.documentElement.className = document.documentElement.className.replace(/(^|\s)sc-help-open(?=\s|$)/g, '');
    sum.focus();
  }
  sum.addEventListener('click', function (e) { e.preventDefault(); if (sheet.hidden) show(); else hide(); });
  sheet.addEventListener('click', function (e) {
    // The backdrop, or the close button (or its icon), closes the sheet; a tap on the tips does not.
    var el = e.target;
    if (el === sheet) { hide(); return; }
    while (el && el !== sheet) {
      if (el.hasAttribute && el.hasAttribute('data-help-close')) { e.preventDefault(); hide(); return; }
      el = el.parentNode;
    }
  });
  document.addEventListener('keydown', function (e) {
    if (sheet.hidden) return;
    if (e.key === 'Escape' || e.key === 'Esc') { e.preventDefault(); hide(); return; }
    if (e.key !== 'Tab') return;
    var f = focusables();
    if (!f.length) { e.preventDefault(); return; }
    var first = f[0], last = f[f.length - 1], at = document.activeElement;
    if (e.shiftKey && (at === first || !sheet.contains(at))) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && (at === last || !sheet.contains(at))) { e.preventDefault(); first.focus(); }
  });
})();
</script>`

// guestHead opens every page after sign-in: language, direction, the hotel's layout attributes and colours
// on <html>, and the stylesheets in the SAME cascade as the sign-in page -- the portal's styling and the
// template in layers, the hotel's custom CSS in `@layer hotel` above them, and an unlayered guard sheet over the
// controls these pages need (portalPageGuardCSS). The hotel's sheet is rendered by the server here (HotelSheet,
// built by hotelSheet in portal_page.go from the design ForGuests already sanitised), so the hotel's look
// carries on after sign-in and its CSS still cannot hide Disconnect, Back or a package button.
const guestHead = `<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}" data-template="{{.Brand.Template}}" data-density="{{.Brand.Density}}" data-panel="{{.Brand.Panel}}" data-hero="{{.Brand.HeroHeight}}" data-surface="{{.Brand.Surface}}"{{with .Brand.Style}} style="{{.}}"{{end}}><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<style id="sc-base">
  @layer sc-base, sc-template, hotel;
  @layer sc-base {` + portalBaseCSS + `}
</style>
<style id="sc-templates">
@layer sc-template {` + portalTemplateCSS + `
  [data-template="split"] .card { display: flex; }
  [data-template="headerbar"] .card { display: grid; }
  /* The pages after sign-in have no help column, so the business layout's wide card closes up around them. */
  [data-template="headerbar"] .card--page { max-width: 600px; }
  [data-template="headerbar"] .card--page .sc-body { max-width: none; }
  .choice-list { margin-top: 20px; }
  .choice-list form { margin: 0; }
  .fact + .tl-note { margin-top: 10px; }
}
</style>
{{with .HotelSheet}}{{.}}{{end}}
<style id="sc-guard">` + portalPageGuardCSS + `</style>
<script nonce="{{.Nonce}}">
  // A browser without cascade layers drops a layered block whole; there the wrappers come off before anything
  // paints and plain source order -- base, template, hotel, guard -- keeps the same outcome.
  (function () {
    if (window.CSSLayerBlockRule) return;
    ['sc-base', 'sc-templates', 'sc-hotel'].forEach(function (id) {
      var el = document.getElementById(id);
      if (!el) return;
      var css = el.textContent.replace(/@layer[^;{]*;/g, '').replace(/@layer\s+[\w-]+\s*\{/, '');
      var end = css.lastIndexOf('}');
      el.textContent = end >= 0 ? css.slice(0, end) : css;
    });
  })();
</script>`

// guestChrome is the hero (decoration, for the photographic and bar layouts) and the language pill.
const guestChrome = `
<div class="sc-hero" aria-hidden="true"><div class="sc-hero-inner">{{with .Brand.Logo}}<img class="sc-hero-logo" src="{{.}}" alt="" data-logo>{{end}}<div class="sc-hero-name" dir="auto">{{if .Brand.Name}}{{.Brand.Name}}{{else}}{{index .T "brand.fallback"}}{{end}}</div>{{with .Brand.Welcome}}<p class="sc-hero-welcome" dir="auto">{{.}}</p>{{end}}</div></div>
{{if gt (len .Languages) 1}}<div class="langbar"><label class="lang">` + iconGlobe + `<select id="lang" aria-label="{{index .T "lang.label"}}">{{range .Languages}}<option value="{{.Code}}"{{if eq .Code $.Lang}} selected{{end}}>{{.Label}}</option>{{end}}</select>` + iconChevDown + `</label></div>{{end}}`

// guestBrandblock is the hotel's logo, or the Wi-Fi mark when there is none, and its name.
const guestBrandblock = `
<div class="sc-brandblock"><div class="brand{{if .Brand.Logo}} has-logo{{end}}">{{with .Brand.Logo}}<img src="{{.}}" alt="" data-logo>{{end}}<span class="brand-mark" aria-hidden="true">` + iconWifi + `</span><p class="name" dir="auto">{{if .Brand.Name}}{{.Brand.Name}}{{else}}{{index .T "brand.fallback"}}{{end}}</p></div></div>`

// guestScripts: a logo that fails to load leaves nothing behind, and the language pill remembers the
// guest's choice exactly as the sign-in page does (cookie and localStorage) before redrawing the page in it.
const guestScripts = `
<script nonce="{{.Nonce}}">
(function () {
  var imgs = document.querySelectorAll('img[data-logo]');
  function gone(img) {
    img.style.display = 'none';
    var b = img.parentNode;
    if (b && b.classList) b.classList.remove('has-logo');
  }
  for (var i = 0; i < imgs.length; i++) {
    (function (img) {
      if (img.complete && !img.naturalWidth) gone(img);
      img.addEventListener('error', function () { gone(img); });
    })(imgs[i]);
  }
  var sel = document.getElementById('lang');
  if (sel) sel.addEventListener('change', function () {
    try { localStorage.setItem('sc-lang', sel.value); } catch (e) {}
    try { document.cookie = 'sc-lang=' + encodeURIComponent(sel.value) + '; path=/; max-age=31536000; SameSite=Lax'; } catch (e) {}
    location.reload();
  });
})();
</script>` + guestHelpScript + `
`

// ============================================================================================================
// THE SIGN-IN PAGE
// ============================================================================================================

const landingHTML = `<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}" data-template="{{.Brand.Template}}" data-density="{{.Brand.Density}}" data-panel="{{.Brand.Panel}}" data-hero="{{.Brand.HeroHeight}}" data-surface="{{.Brand.Surface}}"{{with .Brand.Style}} style="{{.}}"{{end}}><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<title>{{if .Brand.Name}}{{.Brand.Name}} — Wi-Fi{{else}}{{index .T "brand.fallback"}}{{end}}</title>
<style id="sc-base">
  /* THE CASCADE ORDER, declared before anything else so nothing later can re-order it: the portal's own
     styling, then the chosen template, then the hotel's custom CSS. See the guard sheet below for why. */
  @layer sc-base, sc-template, hotel;
  @layer sc-base {` + portalBaseCSS + `}
</style>
<style id="sc-templates">
/* THE TEMPLATES -- six layouts over ONE page. Every template arranges the same elements: the same forms,
   the same ids, the same script. The hero is decoration and aria-hidden; the card keeps the readable copies. */
@layer sc-template {` + portalTemplateCSS + `}
</style>
<style id="sc-guard">` + portalGuardCSS + `</style>
<script nonce="{{.Nonce}}">
  // CASCADE LAYERS, AND THE BROWSERS THAT PREDATE THEM. A browser without cascade layers (iOS before 15.4,
  // for one) drops an entire layered block as an unknown rule -- which here would be the portal's whole
  // stylesheet. On those browsers the layer wrappers are removed before anything paints, and the page falls
  // back to plain source order: base, template, guard, with the hotel's sheet inserted before the guard.
  (function () {
    if (window.CSSLayerBlockRule) return;
    ['sc-base', 'sc-templates'].forEach(function (id) {
      var el = document.getElementById(id);
      if (!el) return;
      var css = el.textContent.replace(/@layer[^;{]*;/g, '').replace(/@layer\s+[\w-]+\s*\{/, '');
      var end = css.lastIndexOf('}');
      el.textContent = end >= 0 ? css.slice(0, end) : css;
    });
  })();
</script>
</head><body>
  <div class="page">
  <!-- THE HERO: decoration for the photographic and bar templates, hidden in Classic and Kiosk. -->
  <div class="sc-hero" aria-hidden="true">
    <div class="sc-hero-inner">
      <img class="sc-hero-logo" alt=""{{with .Brand.Logo}} src="{{.}}"{{else}} hidden{{end}}>
      <div class="sc-hero-name" dir="auto">{{.BrandName}}</div>
      <p class="sc-hero-welcome" dir="auto">{{.Brand.Welcome}}</p>
    </div>
  </div>
  <div class="langbar">
    <label class="lang" for="lang">` + iconGlobe + `
      <select id="lang" aria-label="{{index .T "lang.label"}}">{{range .Languages}}<option value="{{.Code}}"{{if eq .Code $.Lang}} selected{{end}}>{{.Label}}</option>{{end}}</select>` + iconChevDown + `
    </label>
  </div>

  <main class="card">
    <div class="sc-brandblock">
      <!-- BRANDING. Rendered from the published design so the first paint is already the hotel's, and applied
           again from /api/branding (which is how the admin preview shows unsaved changes). The defaults are
           what an unbranded appliance shows, which must still look deliberate rather than broken. -->
      <div class="brand{{if .Brand.Logo}} has-logo{{end}}">
        <img id="brand-logo" alt=""{{with .Brand.Logo}} src="{{.}}"{{else}} style="display:none"{{end}}>
        <span class="brand-mark" aria-hidden="true">` + iconWifi + `</span>
        <h1 class="name" id="brand-name" dir="auto"{{if not .Brand.Name}} data-i18n="brand.fallback" data-i18n-en="Guest Wi-Fi"{{end}}>{{.BrandName}}</h1>
      </div>
      <p class="welcome" id="brand-welcome" dir="auto"{{if not .Brand.Welcome}} hidden{{end}}>{{.Brand.Welcome}}</p>
    </div>

    <div class="sc-body sc-signin">
    <!-- WHY THE INTERNET STOPPED, and whether there is anything to connect to here. Amber advisories. -->
    <div class="notice" id="access-ended" role="status" aria-live="polite"></div>
    <div class="notice" id="site-notice" role="status" aria-live="polite"></div>
    {{if .Error}}
    <!-- WHAT THE SERVER SAID, WHERE THE GUEST CAN READ IT, in their language. The voucher and personal-account
         forms are plain HTML POSTs, so this response IS the page they land on. role="alert": it is the answer
         to something the guest just did. -->
    <div class="notice notice--error show" id="server-error" role="alert" aria-live="assertive"{{with .ErrorKey}} data-i18n="{{.}}" data-i18n-en="{{$.ErrorEn}}"{{end}}>{{.Error}}</div>
    {{end}}

    <div class="tabs" id="tabs" role="tablist"></div>
    <div class="panels" id="signin-panel">

  <!-- ACCOUNT LOGIN — one panel, two ways in: a voucher, or a personal account behind the switch. -->
  <div class="panel panel--wide" id="panel-accountlogin">
    <label class="pill" for="use-personal">
      <span data-i18n="account.personal" data-i18n-en="Use Personal Account">{{index .T "account.personal"}}</span>
      <input type="checkbox" id="use-personal" role="switch">
    </label>

    <form method="POST" action="/auth/voucher" id="form-voucher">
      <div class="field">
        <label for="voucher"><span data-i18n="voucher.label" data-i18n-en="Voucher Code">{{index .T "voucher.label"}}</span></label>
        <input id="voucher" name="code" type="text" autocomplete="off" autocapitalize="characters" autocorrect="off" spellcheck="false" required maxlength="32">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.login" data-i18n-en="Login">{{index .T "btn.login"}}</span></button>
      <div class="err" role="alert"></div>
    </form>

    <form method="POST" action="/auth/credentials" id="form-credentials" autocomplete="off" style="display:none">
      <div class="field">
        <label for="ga-username"><span data-i18n="account.user" data-i18n-en="Username">{{index .T "account.user"}}</span></label>
        <input id="ga-username" name="username" type="text" autocomplete="username" autocapitalize="none" autocorrect="off" spellcheck="false" required maxlength="64">
      </div>
      <div class="field">
        <label for="ga-password"><span data-i18n="account.pass" data-i18n-en="Password">{{index .T "account.pass"}}</span></label>
        <input id="ga-password" name="password" type="password" autocomplete="current-password" required maxlength="128">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.login" data-i18n-en="Login">{{index .T "btn.login"}}</span></button>
      <div class="err" role="alert"></div>
    </form>
  </div>

  <div class="panel" id="panel-email">
    <form data-otp="email" data-stage="dest" autocomplete="off">
      <div class="field">
        <label for="email"><span data-i18n="email.dest" data-i18n-en="Email address">{{index .T "email.dest"}}</span></label>
        <input id="email" name="dest" type="email" required placeholder="you@example.com" autocomplete="email" autocapitalize="none" spellcheck="false">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.sendcode" data-i18n-en="Send code">{{index .T "btn.sendcode"}}</span></button>
      <div class="err" role="alert"></div>
    </form>
    <form data-otp="email" data-stage="code" autocomplete="off" style="display:none">
      <p class="small"><span data-i18n="otp.sent.email" data-i18n-en="We sent a 6-digit code to">{{index .T "otp.sent.email"}}</span> <bdi class="dest"></bdi></p>
      <div class="field">
        <label for="email-code"><span data-i18n="otp.code" data-i18n-en="Verification code">{{index .T "otp.code"}}</span></label>
        <input id="email-code" name="code" type="text" inputmode="numeric" pattern="[0-9]*" autocomplete="one-time-code" required maxlength="6" placeholder="••••••" dir="ltr">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.verify" data-i18n-en="Verify">{{index .T "btn.verify"}}</span></button>
      <button type="button" class="link" data-resend data-i18n="otp.retry.email" data-i18n-en="Try a different email">{{index .T "otp.retry.email"}}</button>
      <div class="err" role="alert"></div>
    </form>
  </div>

  <!-- ROOM — the room number on a numeric keypad, and the one detail this hotel asks for. -->
  <div class="panel" id="panel-pms">
    <form id="form-pms" autocomplete="off">
      <div class="field">
        <label for="pms-room"><span data-i18n="pms.room" data-i18n-en="Room Number">{{index .T "pms.room"}}</span></label>
        <input id="pms-room" name="room" type="text" inputmode="numeric" autocomplete="off" autocorrect="off" spellcheck="false" required dir="ltr">
      </div>
      <div class="field">
        <label for="pms-secondary"><span data-i18n="pms.secondary" data-i18n-en="Password">{{index .T "pms.secondary"}}</span></label>
        <input id="pms-secondary" name="secondary" type="text" autocomplete="off" autocorrect="off" spellcheck="false" required aria-describedby="pms-prompt">
        <!-- The hint sits UNDER the field. Its text is set from the site's configured room-sign-in mode. -->
        <p class="hint" id="pms-prompt"></p>
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.submit" data-i18n-en="Submit">{{index .T "btn.submit"}}</span></button>
    </form>
    <!-- the error lives OUTSIDE the form: during package selection the form is hidden, and a failure message
         inside it would be invisible exactly when the guest most needs to see it. -->
    <div class="err" id="pms-err" role="alert" aria-live="polite"></div>
    <div id="pms-choices" role="group" aria-label="{{index .T "pms.choose"}}" style="display:none"></div>
  </div>

  <!-- POST-STAY — ONE field. The appliance already knows which stay this device belonged to. -->
  <div class="panel" id="panel-poststay">
    <form id="form-poststay" autocomplete="off">
      <div class="field">
        <label for="ps-pin"><span data-i18n="poststay.pin" data-i18n-en="Post-stay PIN">{{index .T "poststay.pin"}}</span></label>
        <input id="ps-pin" name="pin" type="text" inputmode="text" autocapitalize="characters" autocorrect="off" spellcheck="false" required dir="ltr"
               aria-describedby="ps-hint">
        <p class="hint" id="ps-hint" data-i18n="poststay.hint" data-i18n-en="The PIN you were given at checkout">{{index .T "poststay.hint"}}</p>
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.reconnect" data-i18n-en="Reconnect">{{index .T "btn.reconnect"}}</span></button>
    </form>
    <div class="err" id="ps-err" role="alert" aria-live="polite"></div>
  </div>

  <div class="panel" id="panel-social">
    <div id="social-providers"></div>
  </div>

  <div class="panel" id="panel-sms">
    <form data-otp="sms" data-stage="dest" autocomplete="off">
      <div class="field">
        <label for="phone"><span data-i18n="sms.dest" data-i18n-en="Phone number">{{index .T "sms.dest"}}</span></label>
        <input id="phone" name="dest" type="tel" required placeholder="+1 555 123 4567" autocomplete="tel" dir="ltr" aria-describedby="sms-hint">
        <p class="hint" id="sms-hint" data-i18n="sms.hint" data-i18n-en="Include the country code, for example +44 20 7946 0958">{{index .T "sms.hint"}}</p>
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.sendcode" data-i18n-en="Send code">{{index .T "btn.sendcode"}}</span></button>
      <div class="err" role="alert"></div>
    </form>
    <form data-otp="sms" data-stage="code" autocomplete="off" style="display:none">
      <p class="small"><span data-i18n="otp.sent.sms" data-i18n-en="We texted a 6-digit code to">{{index .T "otp.sent.sms"}}</span> <bdi class="dest"></bdi></p>
      <div class="field">
        <label for="sms-code"><span data-i18n="otp.code" data-i18n-en="Verification code">{{index .T "otp.code"}}</span></label>
        <input id="sms-code" name="code" type="text" inputmode="numeric" pattern="[0-9]*" autocomplete="one-time-code" required maxlength="6" placeholder="••••••" dir="ltr">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.verify" data-i18n-en="Verify">{{index .T "btn.verify"}}</span></button>
      <button type="button" class="link" data-resend data-i18n="otp.retry.sms" data-i18n-en="Use a different number">{{index .T "otp.retry.sms"}}</button>
      <div class="err" role="alert"></div>
    </form>
  </div>

      <div class="alt" id="alt-methods" style="display:none"></div>
    </div>
    </div>

    <!-- The hotel's own footer: a help line and the Advanced fragment. The fragment is inserted as MARKUP. It
         is safe to do so for three independent reasons: edged refuses a design whose fragment is not already
         clean (internal/portaldesign's allowlist); /api/branding rebuilds it through the same allowlist before
         a guest receives it; and this page's Content-Security-Policy runs only scripts carrying its
         per-response nonce, so an inline handler that somehow survived would still not execute. -->
    <div class="sc-extras"{{if not .HasExtras}} data-empty{{end}}>
      <p class="help" id="brand-help" dir="auto"{{if not .Help}} hidden{{end}}>{{.Help}}</p>
      <div id="custom-html"></div>
    </div>
    <div class="sc-foot">
      <p class="terms" id="brand-terms-wrap"{{if not .Terms}} hidden{{end}}>
        <a id="brand-terms"{{with .Terms}} href="{{.}}"{{end}} target="_blank" rel="noopener noreferrer"
           data-i18n="terms.link" data-i18n-en="Terms of use">{{index .T "terms.link"}}</a>
      </p>
      <!-- HELP. The ways in this hotel offers, explained; the script hides the tips for methods that are off. -->
      ` + helpOpen + `
      <ul class="help-tips" id="help-tips">
        <li data-help-method="pms"><strong data-i18n="method.pms" data-i18n-en="Room">{{index .T "method.pms"}}</strong><span data-i18n="help.pms" data-i18n-en="Enter your room number, then the detail asked for below it, exactly as it appears on your reservation.">{{index .T "help.pms"}}</span></li>
        <li data-help-method="poststay"><strong data-i18n="method.poststay" data-i18n-en="Post-stay">{{index .T "method.poststay"}}</strong><span data-i18n="help.poststay" data-i18n-en="Already checked out? Enter the PIN you were given at checkout to reconnect.">{{index .T "help.poststay"}}</span></li>
        <li data-help-method="voucher"><strong data-i18n="method.voucher" data-i18n-en="Voucher">{{index .T "method.voucher"}}</strong><span data-i18n="help.voucher" data-i18n-en="Type the code exactly as it is printed on your voucher, then tap Login.">{{index .T "help.voucher"}}</span></li>
        <li data-help-method="account"><strong data-i18n="method.account" data-i18n-en="Personal account">{{index .T "method.account"}}</strong><span data-i18n="help.account" data-i18n-en="Enter the username and password you were given. If a voucher field is showing, switch on “Use Personal Account” first.">{{index .T "help.account"}}</span></li>
        <li data-help-method="email"><strong data-i18n="method.email" data-i18n-en="Email">{{index .T "method.email"}}</strong><span data-i18n="help.email" data-i18n-en="Enter your email address and tap Send code, then type the 6-digit code from the email. Check your spam folder if it does not arrive.">{{index .T "help.email"}}</span></li>
        <li data-help-method="sms"><strong data-i18n="method.sms" data-i18n-en="Phone">{{index .T "method.sms"}}</strong><span data-i18n="help.sms" data-i18n-en="Enter your phone number with the country code and tap Send code, then type the 6-digit code from the text message.">{{index .T "help.sms"}}</span></li>
        <li data-help-method="social"><strong data-i18n="method.social" data-i18n-en="Social">{{index .T "method.social"}}</strong><span data-i18n="social.note" data-i18n-en="You will be redirected to the provider, then back here.">{{index .T "social.note"}}</span></li>
      </ul>
      <p class="help-fail" data-i18n="help.fail" data-i18n-en="Something not working? Please contact reception — they are happy to help.">{{index .T "help.fail"}}</p>
      <div class="help-device">
        <strong data-i18n="info.device" data-i18n-en="Your device">{{index .T "info.device"}}</strong>
        <dl>
          <dt><span data-i18n="info.ip" data-i18n-en="IP address">{{index .T "info.ip"}}</span></dt><dd dir="ltr">{{if .ClientIP}}{{.ClientIP}}{{else}}<span data-i18n="info.none" data-i18n-en="Not detected">{{index .T "info.none"}}</span>{{end}}</dd>
          <dt><span data-i18n="info.mac" data-i18n-en="MAC address">{{index .T "info.mac"}}</span></dt><dd dir="ltr">{{if .ClientMAC}}{{.ClientMAC}}{{else}}<span data-i18n="info.none" data-i18n-en="Not detected">{{index .T "info.none"}}</span>{{end}}</dd>
        </dl>
        <p data-i18n="info.help" data-i18n-en="Reception may ask for these if you need help connecting.">{{index .T "info.help"}}</p>
      </div>
      <p class="help-hotel" id="help-hotel" dir="auto"{{if not .Brand.Help}} hidden{{end}}>{{.Brand.Help}}</p>
      </div></div></details>
      <button class="info-btn" id="info-btn" type="button" aria-expanded="false" aria-controls="info-panel"
              data-i18n-aria="info.button" aria-label="{{index .T "info.button"}}" title="{{index .T "info.button"}}">` + iconInfo + `</button>
      <div class="info-panel" id="info-panel" hidden>
        <strong data-i18n="info.device" data-i18n-en="Your device">{{index .T "info.device"}}</strong>
        <dl>
          <dt><span data-i18n="info.ip" data-i18n-en="IP address">{{index .T "info.ip"}}</span></dt><dd dir="ltr">{{if .ClientIP}}{{.ClientIP}}{{else}}<span data-i18n="info.none" data-i18n-en="Not detected">{{index .T "info.none"}}</span>{{end}}</dd>
          <dt><span data-i18n="info.mac" data-i18n-en="MAC address">{{index .T "info.mac"}}</span></dt><dd dir="ltr">{{if .ClientMAC}}{{.ClientMAC}}{{else}}<span data-i18n="info.none" data-i18n-en="Not detected">{{index .T "info.none"}}</span>{{end}}</dd>
        </dl>
        <p data-i18n="info.help" data-i18n-en="Reception may ask for these if you need help connecting.">{{index .T "info.help"}}</p>
      </div>` + ogAttribution + `
    </div>
  </main>
  </div>

  <script nonce="{{.Nonce}}">
    // THE REFERENCE DESIGN PRESENTS TWO DOORS, NOT SIX. Every enabled method lands in one of two groups --
    // "am I staying here, or do I have a code?" -- and the group is only shown when it has something in it.
    // No method is removed: this is presentation, and each panel is the same form it always was.
    const ICON_DOOR = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M3.5 21h17M6.5 21V4.5A1.5 1.5 0 0 1 8 3h8a1.5 1.5 0 0 1 1.5 1.5V21"/><path d="M14 12.5v.1"/></svg>';
    const ICON_KEYS = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><circle cx="12" cy="8" r="4"/><path d="M4.5 20.5a7.5 7.5 0 0 1 15 0"/></svg>';
    const ICON_CHEV = '` + iconChevNext + `';
    const ICON_EMPTY = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M2 2l20 20M8.5 16.1a5.5 5.5 0 0 1 7 0M5 12.6a10.5 10.5 0 0 1 5.2-2.4M14.8 10.4a10.6 10.6 0 0 1 4.2 2.2M1.9 9.1A15 15 0 0 1 6.3 6.3M10.7 4.6a15 15 0 0 1 11.4 4.5M12 20h.01"/></svg>';

    const Groups = {
      guest:   { id:'guest',   label:'Guest Login',   icon: ICON_DOOR, members:['pms','poststay'] },
      account: { id:'account', label:'Account Login', icon: ICON_KEYS, members:['voucher','account','email','sms','social'] },
    };
    const Tabs = {
      // Both point at the merged panel: which FORM shows is the switch's business, not the tab's.
      voucher: { id:'voucher', label:'Voucher', panel:'panel-accountlogin' },
      account: { id:'account', label:'Personal account', panel:'panel-accountlogin' },
      email:   { id:'email',   label:'Email',   panel:'panel-email' },
      sms:     { id:'sms',     label:'Phone',   panel:'panel-sms' },
      pms:     { id:'pms',     label:'Room',    panel:'panel-pms' },
      social:  { id:'social',  label:'Social',  panel:'panel-social' },
      poststay:{ id:'poststay',label:'Post-stay',panel:'panel-poststay' },
    };
    const ProviderLabels = { google: 'Continue with Google', apple: 'Continue with Apple', facebook: 'Continue with Facebook', microsoft: 'Continue with Microsoft' };
    const PMSPrompts = {
      room_lastname:    "Last name on the reservation",
      room_firstname:   "First name on the reservation",
      room_reservation: "Reservation / confirmation number",
      // room_any accepts any of the three. The guest is told what they MAY type and is never asked to
      // classify it — the server compares one value against all three fields.
      room_any:         "First name, last name, or reservation number",
      either:           "Last name OR reservation number",
    };
    const PMSPromptKeys = {
      room_lastname:    'pms.prompt.lastname',
      room_firstname:   'pms.prompt.firstname',
      room_reservation: 'pms.prompt.reservation',
      room_any:         'pms.prompt.any',
      either:           'pms.prompt.either',
    };
    const challenges = {}; // channel -> challenge_id

    // Which METHOD is showing, within whichever group is selected.
    function setTab(id) {
      document.querySelectorAll('.panel').forEach(el => el.classList.remove('active'));
      const t = Tabs[id]; if (t) document.getElementById(t.panel).classList.add('active');
    }
    // Which GROUP is selected. Shows that group's first available method, and any others as alternatives
    // beneath it. The tabs are a real tablist: the selected tab is the one in the tab order, and the region
    // they control is labelled by it.
    function setGroup(gid, groupMembers) {
      document.querySelectorAll('.tab').forEach(el => {
        const on = el.dataset.group === gid;
        el.setAttribute('aria-selected', on ? 'true' : 'false');
        el.tabIndex = on ? 0 : -1;
        if (on) document.getElementById('signin-panel').setAttribute('aria-labelledby', el.id);
      });
      const members = groupMembers[gid] || [];
      setTab(members[0]);
      const alt = document.getElementById('alt-methods');
      alt.innerHTML = '';
      // THE GROUP'S WAYS IN. One chip per PANEL -- voucher and personal account share one, chosen by the switch
      // inside it, so they are one chip -- and only when there is more than one panel to choose between. The
      // chip for the panel on screen is marked as the current one (aria-pressed), so a guest who moved to
      // Post-stay can always get back to Room, even when the tabs are hidden because only one group exists.
      const seen = {};
      const others = members.filter(m => Tabs[m] && !seen[Tabs[m].panel] && (seen[Tabs[m].panel] = true));
      if (others.length > 1) {
        const h = document.createElement('h3');
        // Generated text carries its key so the language pass reaches it too.
        h.dataset.i18n = 'alt.title';
        h.dataset.i18nEn = BUILTIN.en['alt.title'];
        h.textContent = t('alt.title');
        alt.appendChild(h);
        others.forEach((m, i) => {
          const b = document.createElement('button');
          b.type = 'button'; b.className = 'link';
          b.dataset.i18n = 'method.' + m;
          b.dataset.i18nEn = Tabs[m].label;
          b.textContent = t('method.' + m);
          b.setAttribute('aria-pressed', i === 0 ? 'true' : 'false');
          // The alternatives ARE method selectors, so they carry the method they select.
          b.dataset.tab = m;
          b.onclick = () => {
            setTab(m);
            alt.querySelectorAll('button').forEach(x => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
          };
          alt.appendChild(b);
        });
        alt.style.display = '';
      } else {
        alt.style.display = 'none';
      }
    }

    // ARROW KEYS MOVE BETWEEN THE TABS, in the direction the page reads: in Arabic the next tab is to the left.
    function tabKeys(e) {
      const tabs = Array.prototype.slice.call(document.querySelectorAll('#tabs .tab'));
      const i = tabs.indexOf(document.activeElement);
      if (i < 0) return;
      const rtl = document.documentElement.dir === 'rtl';
      let j;
      if (e.key === 'ArrowRight' || e.key === 'Right') j = rtl ? i - 1 : i + 1;
      else if (e.key === 'ArrowLeft' || e.key === 'Left') j = rtl ? i + 1 : i - 1;
      else if (e.key === 'Home') j = 0;
      else if (e.key === 'End') j = tabs.length - 1;
      else return;
      e.preventDefault();
      j = (j + tabs.length) % tabs.length;
      tabs[j].focus();
      tabs[j].click();
    }

    // TRANSLATIONS. SIX LANGUAGES SHIP WITH THE PORTAL, and the hotel's own translations, published with its
    // design, are merged OVER them. Resolution order for every string, most specific first:
    //   1. the hotel's published translation for the chosen language
    //   2. the built-in translation for the chosen language
    //   3. the built-in English, which is also the text the markup carries in data-i18n-en
    // THE WORDS COME FROM THE BINARY (languages.go): the same map is served at /api/languages.
    var LANGS = {{.AllLanguages}};
    var BUILTIN = {{.Strings}};

    var I18N = {{.HotelWords}} || {};   // the HOTEL's published overrides: code -> { key: text }
    var LANG = 'en';
    var DICT = BUILTIN.en;

    function langMeta(code) {
      for (var i = 0; i < LANGS.length; i++) { if (LANGS[i].code === code) return LANGS[i]; }
      return null;
    }

    // words merges the hotel's overrides over the built-in dictionary for one language. A blank override is
    // not an override.
    function words(code) {
      var out = {};
      var base = BUILTIN[code] || {};
      var over = I18N[code] || {};
      Object.keys(base).forEach(function (k) { out[k] = base[k]; });
      Object.keys(over).forEach(function (k) { if (over[k]) out[k] = over[k]; });
      return out;
    }

    // t is for text this script GENERATES. Text already in the markup carries data-i18n instead.
    function t(key) { return DICT[key] || BUILTIN.en[key] || key; }

    function applyLanguage(code) {
      LANG = code;
      DICT = words(code);
      var meta = langMeta(code);
      document.documentElement.lang = code;
      // Arabic reads right to left. A portal that renders it left-aligned has transliterated the words and
      // left the page in the wrong language.
      document.documentElement.dir = (meta && meta.rtl) ? 'rtl' : 'ltr';
      document.querySelectorAll('[data-i18n]').forEach(function (el) {
        var k = el.dataset.i18n;
        if (DICT[k]) el.textContent = DICT[k];
        else if (el.dataset.i18nEn) el.textContent = el.dataset.i18nEn;
      });
      document.querySelectorAll('[data-i18n-ph]').forEach(function (el) {
        var k = el.dataset.i18nPh;
        if (DICT[k]) el.placeholder = DICT[k];
        else if (el.dataset.i18nPhEn) el.placeholder = el.dataset.i18nPhEn;
      });
      document.querySelectorAll('[data-i18n-aria]').forEach(function (el) {
        var v = t(el.dataset.i18nAria);
        el.setAttribute('aria-label', v);
        el.setAttribute('title', v);
      });
      var sel = document.getElementById('lang');
      if (sel) sel.setAttribute('aria-label', t('lang.label'));
      var choices = document.getElementById('pms-choices');
      if (choices) choices.setAttribute('aria-label', t('pms.choose'));
    }

    // REMEMBERING A CHOICE ON A CAPTIVE PORTAL IS NOT localStorage ALONE. iOS opens the portal in a throwaway
    // WebView whose storage does not reliably outlive the sheet, so the choice is written to both a cookie and
    // localStorage, and read back from whichever survived. It is written ONLY when the guest chooses:
    // automatic detection deliberately leaves no trace.
    var LANG_KEY = 'sc-lang';

    function rememberLanguage(code) {
      try { localStorage.setItem(LANG_KEY, code); } catch (e) {}
      try {
        // A year, path-wide. SameSite=Lax so the redirect back from a sign-in POST still carries it.
        document.cookie = LANG_KEY + '=' + encodeURIComponent(code) + '; path=/; max-age=31536000; SameSite=Lax';
      } catch (e) {}
    }

    function rememberedLanguage() {
      var v = '';
      try { v = localStorage.getItem(LANG_KEY) || ''; } catch (e) {}
      if (v) return v;
      try {
        var m = ('; ' + document.cookie).match(/; sc-lang=([^;]*)/);
        if (m) v = decodeURIComponent(m[1]);
      } catch (e) {}
      return v;
    }

    // THE DEVICE'S OWN LANGUAGE PREFERENCE, IN THE ORDER THE DEVICE GIVES IT. Each entry contributes twice:
    // the tag as given and its primary subtag, so ar-EG matches a hotel that enabled 'ar'.
    function devicePreferredLanguages() {
      var raw = [];
      if (navigator.languages && navigator.languages.length) {
        raw = Array.prototype.slice.call(navigator.languages);
      } else if (navigator.language) {
        raw = [navigator.language];
      }
      var out = [];
      function push(v) { if (v && out.indexOf(v) < 0) out.push(v); }
      raw.forEach(function (tag) {
        var t = String(tag || '').toLowerCase().replace(/_/g, '-');
        push(t);
        push(t.split('-')[0]);
      });
      return out;
    }

    // chooseLanguage: which of the languages THIS HOTEL offers should this guest see? '' when none matches.
    function chooseLanguage(offeredCodes) {
      var have = {};
      offeredCodes.forEach(function (c) { have[String(c).toLowerCase()] = c; });
      var prefs = devicePreferredLanguages();
      for (var i = 0; i < prefs.length; i++) {
        if (have[prefs[i]]) return have[prefs[i]];
        for (var j = 0; j < offeredCodes.length; j++) {
          if (String(offeredCodes[j]).toLowerCase().split('-')[0] === prefs[i]) return offeredCodes[j];
        }
      }
      return '';
    }

    document.getElementById('lang').addEventListener('change', function (e) {
      rememberLanguage(e.target.value);
      applyLanguage(e.target.value);
    });

    // THE TEMPLATE. The server already rendered the published template's attributes on <html>, so a guest's
    // first paint is the right layout; this re-applies them from the design, which is what makes the admin
    // preview follow an operator's unsaved choice. Every value is a closed vocabulary, written as an
    // attribute or a custom property -- never as markup or a stylesheet.
    const TEMPLATES = ['classic', 'split', 'immersive', 'headerbar', 'editorial', 'kiosk'];
    function cssProp(name, v) {
      const root = document.documentElement.style;
      if (v) root.setProperty(name, v); else root.removeProperty(name);
    }
    function cssURL(u) { return u ? 'url("' + encodeURI(u) + '")' : ''; }
    // White text on a pale brand colour is unreadable; such a hotel gets dark button text instead.
    function lightColour(c) {
      var m = /^#([0-9a-f]{3,8})$/i.exec(String(c || ''));
      if (!m) return false;
      var h = m[1];
      if (h.length < 6) h = h.charAt(0) + h.charAt(0) + h.charAt(1) + h.charAt(1) + h.charAt(2) + h.charAt(2);
      var l = [0, 2, 4].map(function (i) {
        var f = parseInt(h.substr(i, 2), 16) / 255;
        return f <= 0.03928 ? f / 12.92 : Math.pow((f + 0.055) / 1.055, 2.4);
      });
      var lum = 0.2126 * l[0] + 0.7152 * l[1] + 0.0722 * l[2];
      return 1.05 / (lum + 0.05) < 3;
    }
    function applyTemplate(d) {
      const el = document.documentElement;
      const o = (d && d.template_options && typeof d.template_options === 'object') ? d.template_options : {};
      el.dataset.template = TEMPLATES.indexOf(d && d.template_id) >= 0 ? d.template_id : 'classic';
      el.dataset.density = ['compact', 'comfortable', 'spacious'].indexOf(o.density) >= 0 ? o.density : '';
      el.dataset.panel = ['start', 'center', 'end'].indexOf(o.panel_position) >= 0 ? o.panel_position : '';
      el.dataset.hero = ['short', 'medium', 'tall'].indexOf(o.hero_height) >= 0 ? o.hero_height : '';
      el.dataset.surface = ['solid', 'glass'].indexOf(o.surface) >= 0 ? o.surface : '';
      const overlay = Number(o.overlay);
      const hasOverlay = o.overlay !== undefined && o.overlay !== null && overlay >= 0 && overlay <= 90;
      cssProp('--sc-overlay', hasOverlay ? String(overlay / 100) : '');
      cssProp('--sc-heading-font', o.heading_font || '');
      cssProp('--sc-hero', cssURL((d && (d.hero_image_url || d.background_url)) || ''));
    }

    // BRANDING, applied from /api/branding. Every value is set when the design has it and CLEARED when it does
    // not, so the admin preview -- which starts from the published page -- shows an unsaved removal too.
    fetch('/api/branding').then(r => r.ok ? r.json() : {}).then(b => {
      const d = (b && b.design) || b || {};
      applyTemplate(d);
      cssProp('--sc-brand', d.brand_color);
      cssProp('--sc-brand-dark', d.brand_color_dark);
      cssProp('--sc-ink', d.text_color);
      cssProp('--sc-on-brand', d.brand_color && lightColour(d.brand_color) ? '#14161a' : '');
      cssProp('font-family', d.font_family);
      cssProp('--sc-radius', d.corner_radius);
      cssProp('--sc-bg', cssURL(d.background_url));
      const nameEl = document.getElementById('brand-name');
      if (d.hotel_name) {
        delete nameEl.dataset.i18n;
        nameEl.textContent = d.hotel_name;
        document.title = d.hotel_name + ' — Wi-Fi';
      } else {
        nameEl.dataset.i18n = 'brand.fallback';
        nameEl.dataset.i18nEn = BUILTIN.en['brand.fallback'];
        nameEl.textContent = t('brand.fallback');
      }
      document.querySelector('.sc-hero-name').textContent = nameEl.textContent;
      // AN IMAGE THAT DOES NOT LOAD MUST LEAVE NOTHING BEHIND. A logo whose file has been deleted, or whose
      // https host is unreachable -- which on a captive portal is EVERY external host -- would otherwise
      // render as a broken-image glyph at the top of the sign-in page on every guest device.
      const img = document.getElementById('brand-logo');
      const brandRow = img.parentNode;
      const heroLogo = document.querySelector('.sc-hero-logo');
      if (d.logo_url) {
        img.onerror = function () { img.style.display = 'none'; brandRow.classList.remove('has-logo'); };
        img.onload = function () { img.style.display = ''; brandRow.classList.add('has-logo'); };
        heroLogo.onerror = function () { heroLogo.hidden = true; };
        heroLogo.onload = function () { heroLogo.hidden = false; };
        if (img.getAttribute('src') !== d.logo_url) { img.src = d.logo_url; heroLogo.src = d.logo_url; }
        else if (img.complete) { if (img.naturalWidth) { img.onload(); heroLogo.onload(); } else { img.onerror(); heroLogo.onerror(); } }
      } else {
        img.removeAttribute('src'); img.style.display = 'none'; brandRow.classList.remove('has-logo');
        heroLogo.removeAttribute('src'); heroLogo.hidden = true;
      }
      // The hotel's own words. Each is shown only when it has something to say.
      const welcome = document.getElementById('brand-welcome');
      welcome.textContent = d.welcome_text || ''; welcome.hidden = !d.welcome_text;
      document.querySelector('.sc-hero-welcome').textContent = d.welcome_text || '';
      const help = document.getElementById('brand-help');
      help.textContent = d.help_text || ''; help.hidden = !d.help_text;
      const helpSheet = document.getElementById('help-hotel');
      helpSheet.textContent = d.help_text || ''; helpSheet.hidden = !d.help_text;
      const terms = document.getElementById('brand-terms');
      if (d.terms_url) { terms.href = d.terms_url; } else { terms.removeAttribute('href'); }
      document.getElementById('brand-terms-wrap').hidden = !d.terms_url;
      const oldSheet = document.getElementById('sc-hotel');
      if (oldSheet) oldSheet.parentNode.removeChild(oldSheet);
      if (d.custom_css) {
        // THE HOTEL'S STYLESHEET GOES IN ITS OWN LAYER, above the portal's styling and below the guard sheet
        // that keeps the sign-in controls usable. The server has already removed !important (which would let
        // a layered rule outrank the guard); it is removed again here so the admin preview, which is handed
        // the operator's unsaved text, shows exactly what a guest would get.
        const st = document.createElement('style');
        st.id = 'sc-hotel';
        const css = String(d.custom_css).replace(/!\s*important/gi, '');
        if (window.CSSLayerBlockRule) {
          st.textContent = '@layer hotel {\n' + css + '\n}';
          document.head.appendChild(st);
        } else {
          // No cascade layers: source order is all there is, so the hotel's sheet goes BEFORE the guard.
          st.textContent = css;
          document.head.insertBefore(st, document.getElementById('sc-guard'));
        }
      }
      // innerHTML, deliberately: the fragment IS markup. What arrives here has been through the allowlist
      // twice (on save in edged, on serve in /api/branding) and the page's CSP refuses any script without
      // this response's nonce -- see the note beside #custom-html.
      document.getElementById('custom-html').innerHTML = d.custom_html || '';
      // A layout with a help column hides the column when the hotel has nothing to put in it.
      document.querySelector('.sc-extras').toggleAttribute('data-empty', !d.help_text && !d.custom_html);
      I18N = (d.translations && typeof d.translations === 'object') ? d.translations : {};
      // ONLY THE CONFIGURED LANGUAGES ARE OFFERED; one that has never been near the screen gets the six the
      // portal ships words for. What is never offered is a language with nothing behind it.
      renderLanguages(Array.isArray(d.languages) && d.languages.length ? d.languages : null);
    }).catch(() => {});

    // renderLanguages fills the selector and selects the language this guest should see. It runs once
    // before branding is fetched (from what the server rendered) and again with the hotel's answer.
    function renderLanguages(configured) {
      var sel = document.getElementById('lang');
      var offered = [];
      if (configured) {
        configured.forEach(function (l) {
          var code = (l && l.code) || l;
          var meta = langMeta(code);
          // A code the portal has no words for is still offered IF the hotel published a translation for it.
          if (!meta && !(I18N[code] && Object.keys(I18N[code]).length)) return;
          offered.push({ code: code, label: (l && l.label) || (meta && meta.label) || code.toUpperCase(), rtl: meta && meta.rtl });
        });
      }
      if (!offered.length) offered = LANGS.map(function (l) { return { code: l.code, label: l.label }; });

      sel.innerHTML = '';
      offered.forEach(function (l) {
        var o = document.createElement('option');
        o.value = l.code;
        o.textContent = l.label;
        sel.appendChild(o);
      });
      // One language is not a choice: the pill is hidden, and the page is simply in that language.
      document.querySelector('.langbar').style.display = offered.length > 1 ? '' : 'none';

      // THE ORDER OF PREFERENCE: what this guest CHOSE; then what their device asks for, against what the
      // hotel enabled; then English. A remembered choice the hotel no longer offers falls through.
      var codes = Array.prototype.map.call(sel.options, function (o) { return o.value; });
      var want = rememberedLanguage();
      if (!want || codes.indexOf(want) < 0) want = chooseLanguage(codes);
      if (!want || codes.indexOf(want) < 0) want = codes.indexOf('en') >= 0 ? 'en' : (codes[0] || 'en');
      sel.value = want;
      applyLanguage(want);
    }
    renderLanguages({{.Configured}});

    // The "Use Personal Account" switch. Both forms exist in the DOM at all times so neither loses what the
    // guest typed if they flip back and forth; only visibility moves.
    (function () {
      const cb = document.getElementById('use-personal');
      const voucher = document.getElementById('form-voucher');
      const creds = document.getElementById('form-credentials');
      if (!cb || !voucher || !creds) return;
      function sync() {
        const personal = cb.checked;
        voucher.style.display = personal ? 'none' : '';
        creds.style.display = personal ? '' : 'none';
        // The required attributes follow visibility, or the browser refuses to submit the visible form
        // because a hidden field in the other one is empty and required.
        voucher.querySelectorAll('[required]').forEach(el => { el.disabled = personal; });
        creds.querySelectorAll('[required]').forEach(el => { el.disabled = !personal; });
      }
      cb.addEventListener('change', sync);
      sync();
    })();

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

    // A plain-HTML sign-in form says it is working the moment it is sent, and cannot be sent twice.
    ['form-voucher', 'form-credentials'].forEach(function (id) {
      const f = document.getElementById(id);
      f.addEventListener('submit', function () {
        const b = f.querySelector('button[type=submit]');
        setTimeout(function () { b.disabled = true; }, 0);
      });
    });

    // THE SERVER'S SENTENCES, IN THE GUEST'S LANGUAGE. Each map is exact: a sentence the server sends is
    // looked up by its English and shown in translation, and one it does not know is shown as sent. Nothing
    // here chooses a sentence; the server did.
    const ENDED = {};
    ENDED[BUILTIN.en['notice.ended.data']] = 'notice.ended.data';
    ENDED[BUILTIN.en['notice.ended.time']] = 'notice.ended.time';

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
        // ONE PROMPT, UNDER THE FIELD -- not repeated as the field's placeholder.
        const prompt = document.getElementById('pms-prompt');
        const key = PMSPromptKeys[cfg.pms.mode] || PMSPromptKeys.either;
        prompt.dataset.i18n = key;
        prompt.dataset.i18nEn = PMSPrompts[cfg.pms.mode] || PMSPrompts.either;
        prompt.textContent = t(key);
        document.getElementById('pms-secondary').dataset.mode = cfg.pms.mode || 'either';
      }
      // POST-STAY HAS ITS OWN GATE, and it is not the PMS one. Pushed AFTER pms so a guest arriving to
      // authenticate for the first time lands on Room, not on a PIN they do not have yet.
      if (cfg.phase5_poststay) {
        enabled.push('poststay');
      }
      if (cfg.social) {
        const providers = Object.keys(cfg.social).filter(k => cfg.social[k] && cfg.social[k].enabled);
        if (providers.length > 0) {
          enabled.push('social');
          const host = document.getElementById('social-providers');
          providers.forEach(p => {
            const a = document.createElement('a');
            a.href = '/auth/social/start?provider=' + encodeURIComponent(p);
            a.className = 'social-btn';
            a.dataset.i18n = 'social.' + p;
            a.dataset.i18nEn = ProviderLabels[p] || ('Continue with ' + p);
            a.textContent = t('social.' + p) === ('social.' + p) ? a.dataset.i18nEn : t('social.' + p);
            host.appendChild(a);
          });
        }
      }
      // THE EXPIRY NOTICE. Asked once, on load, and only ever renders a message the SERVER chose from its
      // two-sentence vocabulary. Any failure is silent: the ordinary sign-in page is the correct fallback.
      fetch('/access/status', {method:'POST', headers:{'Content-Type':'application/json'}, body:'{}'})
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(res){
          if (!res || !res.message) return;
          const n = document.getElementById('access-ended');
          const k = ENDED[res.message];
          if (k) { n.dataset.i18n = k; n.dataset.i18nEn = res.message; n.textContent = t(k); }
          else n.textContent = res.message;
          n.classList.add('show');
        })
        .catch(function(){ /* no notice; the sign-in form below is unaffected */ });

      // NO INTERNET PACKAGE EXISTS AT THIS SITE — a site availability notice, read from configuration BEFORE
      // any identity is submitted and identical for every guest, so it carries nothing about any room or stay.
      if (cfg.internet_packages_available === false) {
        const n = document.getElementById('site-notice');
        n.dataset.i18n = 'notice.nopackages';
        n.dataset.i18nEn = BUILTIN.en['notice.nopackages'];
        n.textContent = t('notice.nopackages');
        n.classList.add('show');
      }
      // THE HELP SHEET explains only the ways in this hotel offers.
      document.querySelectorAll('[data-help-method]').forEach(function (el) {
        el.hidden = enabled.indexOf(el.dataset.helpMethod) < 0;
      });
      if (enabled.length === 0) {
        // NO WAY IN AT ALL: said plainly, with nothing on the page that looks like it might work.
        const none = document.createElement('p');
        none.dataset.i18n = 'notice.nomethods';
        none.dataset.i18nEn = BUILTIN.en['notice.nomethods'];
        none.textContent = t('notice.nomethods');
        const box = document.createElement('div');
        box.className = 'empty-state';
        box.setAttribute('role', 'status');
        box.innerHTML = ICON_EMPTY;
        box.appendChild(none);
        tabsEl.style.display = 'none';
        tabsEl.parentNode.insertBefore(box, tabsEl);
        return;
      }
      // Sort the enabled methods into the two groups, preserving the order enabled[] established.
      const groupMembers = {};
      Object.values(Groups).forEach(g => {
        const members = enabled.filter(id => g.members.includes(id));
        if (members.length) groupMembers[g.id] = members;
      });
      const shown = Object.keys(groupMembers);

      // A single group is not a choice, so it is not rendered as one: the guest sees the form.
      if (shown.length > 1) {
        document.getElementById('signin-panel').setAttribute('role', 'tabpanel');
        shown.forEach(gid => {
          const el = document.createElement('button');
          el.type = 'button'; el.className = 'tab'; el.dataset.group = gid;
          el.id = 'tab-' + gid;
          el.setAttribute('role', 'tab');
          el.setAttribute('aria-controls', 'signin-panel');
          el.innerHTML = Groups[gid].icon + '<span data-i18n="tab.' + gid + '"></span>';
          const span = el.querySelector('span');
          span.dataset.i18nEn = Groups[gid].label;
          span.textContent = t('tab.' + gid);
          el.addEventListener('click', () => setGroup(gid, groupMembers));
          tabsEl.appendChild(el);
        });
        tabsEl.addEventListener('keydown', tabKeys);
      } else {
        tabsEl.style.display = 'none';
      }
      // The switch only makes sense when the site offers BOTH ways in.
      (function () {
        const hasVoucher = enabled.includes('voucher');
        const hasAccount = enabled.includes('account');
        const pill = document.querySelector('.pill');
        const cb = document.getElementById('use-personal');
        if (pill && cb && !(hasVoucher && hasAccount)) {
          pill.style.display = 'none';
          cb.checked = hasAccount;
          cb.dispatchEvent(new Event('change'));
        }
      })();
      setGroup(shown[0], groupMembers);
    }).catch(() => { setTab('voucher'); });

    function panel(channel) { return document.getElementById('panel-' + channel); }
    function form(channel, stage) { return panel(channel).querySelector('form[data-stage="' + stage + '"]'); }

    // THE ONE-TIME-CODE REFUSALS, IN WORDS. The server's answers are short machine phrases ("incorrect
    // code", "code expired"); each becomes the sentence a guest can act on, in their language.
    function otpMessage(stage, j) {
      const e = String((j && j.error) || '').toLowerCase();
      if (e === 'too_many_attempts' || e.indexOf('too many requests') === 0) return t('err.attempts');
      if (e === 'device not on guest network') return t('err.device.network');
      if (stage === 'dest') {
        if (e === 'invalid email' || e.indexOf('invalid phone') === 0) return t('err.otp.dest');
        if (e === 'wait before requesting another code') return t('err.otp.wait');
        return t('err.otp.send');
      }
      if (e === 'incorrect code') return t('err.otp.code');
      if (e === 'code expired' || e === 'challenge not found' || e === 'code already used' || e === 'too many wrong attempts') {
        return t('err.otp.expired');
      }
      return t('err.service');
    }

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
          if (!r.ok) { errEl.textContent = otpMessage('dest', j); return; }
          challenges[channel] = j.challenge_id;
          codeForm.querySelector('.dest').textContent = dest;
          destForm.style.display = 'none';
          codeForm.style.display = 'block';
          codeForm.querySelector('input[name=code]').focus();
        } catch (err) {
          errEl.textContent = t('err.service');
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
          if (!r.ok) { errEl.textContent = otpMessage('code', j); return; }
          window.location = '/success?s=' + encodeURIComponent(j.session_id || '') +
                            '&t=' + encodeURIComponent(j.duration_seconds || 0);
        } catch (err) {
          errEl.textContent = t('err.service');
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
    // The guest sees exactly two possible outcomes: they are in, or the server's sentence. There is
    // deliberately no branch here that composes a reason of its own — a page that could say "that room exists
    // but the name is wrong" is an occupancy oracle for anyone sitting in the lobby.
    let PHASE3_PMS = false;
    let PMS_AUTH_CONTEXT = '';
    // THE ONE MESSAGE FOR A NON-SUCCESS NO SERVER SENTENCE EXPLAINS (a transport failure, an empty body, a
    // failed grant). A FUNCTION so it is read in the language chosen after this script loaded.
    function PHASE3_FAIL() { return t('err.generic'); }

    // THE SERVER'S SENTENCE IS THE ONE THE GUEST READS -- translated. The server chooses from a closed set
    // (internal/signinattempt, pms_phase3.go); this maps each member of that set to its translation, one to
    // one, so the translated page distinguishes exactly what the English one does and nothing more. A wrong
    // room and a wrong name are still the same sentence in every language.
    const ROOM_MESSAGES = {};
    ROOM_MESSAGES[BUILTIN.en['err.room.credential']] = 'err.room.credential';
    ROOM_MESSAGES[BUILTIN.en['err.room.technical']] = 'err.room.technical';
    ROOM_MESSAGES[BUILTIN.en['err.wait']] = 'err.wait';
    ROOM_MESSAGES[BUILTIN.en['err.generic']] = 'err.generic';
    function phase3Message(j) {
      if (!(j && typeof j.message === 'string' && j.message)) return PHASE3_FAIL();
      const m = j.message;
      if (ROOM_MESSAGES[m]) return t(ROOM_MESSAGES[m]);
      const wait = m.match(/^Too many attempts\. Please wait ([0-9]+) seconds and try again\.$/);
      if (wait) return t('err.wait.seconds').split('{n}').join(wait[1]);
      return m;
    }

    // PHASE3_WAIT_UNTIL is the moment the SERVER said it would consider another submission. It is a local
    // convenience for the countdown and for keeping the button quiet — never what decides whether a
    // submission is accepted.
    let PHASE3_WAIT_UNTIL = 0;
    let PHASE3_WAIT_TIMER = 0;

    // phase3Countdown renders the wait shrinking, second by second. THE WORDING COMES FROM THE SERVER (in
    // translation), THE NUMBER TICKS LOCALLY: the number in the sentence becomes a placeholder, so this page
    // never carries a second copy of the text. A message with no number is shown once and not counted down.
    function phase3Countdown(errEl, message, seconds) {
      if (PHASE3_WAIT_TIMER) { clearInterval(PHASE3_WAIT_TIMER); PHASE3_WAIT_TIMER = 0; }
      errEl.classList.remove('err--ok');
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
          errEl.textContent = t('err.retry');
          errEl.classList.add('err--ok');
          if (btn) btn.disabled = false;
          return;
        }
        errEl.textContent = tmpl.replace('%WAIT%', String(left));
        if (btn) btn.disabled = true;
      };
      render();
      PHASE3_WAIT_TIMER = setInterval(render, 1000);
    }

    // newRequestID returns a CANONICAL RFC-4122 UUID — 36 characters, dashed — because that is the only shape
    // the server accepts. crypto.randomUUID exists only in a SECURE CONTEXT and a captive portal is plain HTTP,
    // so the fallback is the path every real guest takes; crypto.getRandomValues works in an insecure context.
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

    // ONE DELIBERATE SUBMISSION, ONE RESOLUTION REQUEST ID. The id is minted where the guest's tap is handled
    // and never reused by a later tap: a derived id froze room sign-in on the guest's own typo.

    // POST-STAY. The body is {pin} and nothing else.
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
        // Verified. The conversion is a second call carrying the context the server just issued.
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
      errEl.textContent = PHASE3_FAIL();
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
      // EVERY other answer — including a transport failure — is the server's sentence or the uniform one.
      // NOTHING ABOUT THE FAILED ATTEMPT IS CARRIED FORWARD: the next tap mints its own id and is evaluated on
      // its own evidence. retry_after_seconds is present on exactly one answer — the restricted one — and it is
      // the SERVER's remaining time; phase3Countdown renders it shrinking and decides nothing.
      phase3Countdown(errEl, phase3Message(j), j.retry_after_seconds || 0);
      return false;
    }

    // RETURNING TO SIGN-IN AFTER A FAILED SELECTION. The refused choices are taken down rather than left on
    // screen to be pressed again; signing in again is safe under every reading of the uniform answer.
    function resetPhase3ToSignIn(errEl) {
      const box = document.getElementById('pms-choices');
      const form = document.getElementById('form-pms');
      box.innerHTML = '';
      box.style.display = 'none';
      form.style.display = '';
      // The uniform message. A failed GRANT is not a failed identity check, so there is nothing to add.
      errEl.textContent = PHASE3_FAIL();
      const btn = form.querySelector('button[type=submit]');
      if (btn) btn.disabled = false;
    }

    function renderPhase3Choices(choices, errEl) {
      const box = document.getElementById('pms-choices');
      const form = document.getElementById('form-pms');
      box.innerHTML = '';
      if (!choices.length) { errEl.textContent = PHASE3_FAIL(); return; }
      const h = document.createElement('p');
      h.className = 'choices-title';
      h.dataset.i18n = 'pms.choose';
      h.dataset.i18nEn = BUILTIN.en['pms.choose'];
      h.textContent = t('pms.choose');
      box.appendChild(h);
      choices.forEach(function(c) {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'choice';
        b.dataset.packageRevisionId = c.package_revision_id;
        const text = document.createElement('span');
        text.className = 'c-text';
        const name = document.createElement('span');
        name.className = 'c-name';
        name.dir = 'auto';
        name.textContent = c.code;
        text.appendChild(name);
        const down = Math.round((c.down_kbps || 0) / 1000);
        if (down > 0) {
          const detail = document.createElement('span');
          detail.className = 'c-detail';
          detail.textContent = t('unit.mbps').split('{n}').join(String(down));
          text.appendChild(detail);
        }
        b.appendChild(text);
        b.insertAdjacentHTML('beforeend', ICON_CHEV);
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
      const first = box.querySelector('button.choice');
      if (first) first.focus();
    }

    // PMS — single-step form: room + secondary field. Mode decides which server-side field the secondary
    // input fills.
    document.getElementById('form-pms').addEventListener('submit', async (e) => {
      e.preventDefault();
      const btn = e.target.querySelector('button[type=submit]');
      const errEl = document.getElementById('pms-err');
      errEl.textContent = ''; errEl.classList.remove('err--ok'); btn.disabled = true;
      const room = document.getElementById('pms-room').value.trim();
      const sec  = document.getElementById('pms-secondary');
      const val  = sec.value.trim();
      const mode = sec.dataset.mode || 'either';
      const body = { room };
      if (mode === 'room_firstname')        body.first_name = val;
      else if (mode === 'room_reservation') body.reservation_number = val;
      else if (mode === 'room_lastname')    body.last_name = val;
      // room_any sends the value AS TYPED in one field and lets the server compare it against first name,
      // last name and reservation number together.
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
          // The legacy hop's own words are not shown: whatever it said, the guest reads the uniform sentence.
          if (!r.ok) { errEl.textContent = PHASE3_FAIL(); return; }
          window.location = '/success?s=' + encodeURIComponent(j.session_id || '') +
                            '&t=' + encodeURIComponent(j.duration_seconds || 0);
          return;
        }
        // PHASE 3: THIS tap is one resolution request, so it gets an id of its own, minted here.
        body.request_id = newRequestID();
        await submitPhase3(body, errEl);
      } catch (err) {
        errEl.textContent = PHASE3_FAIL();
      } finally {
        // A submission that ended in a restriction leaves the button disabled until the countdown clears it.
        // This is courtesy, not enforcement.
        if (!PHASE3_WAIT_UNTIL || Date.now() >= PHASE3_WAIT_UNTIL) btn.disabled = false;
      }
    });
  </script>
` + guestHelpScript + `

</body></html>`

// ============================================================================================================
// CHOOSE YOUR PACKAGE
// ============================================================================================================

// packagesHTML: one large button per package, name and a detail line. Each is its own form posting the one
// opaque id it always posted; the page adds no field and no script to the acquisition.
const packagesHTML = guestHead + `
<title>{{index .T "pkg.title"}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <h1 class="page-title">{{index .T "pkg.title"}}</h1>
  <p class="page-lead">{{index .T "pkg.subtitle"}}</p>
  <div class="choice-list">
  {{range .Packages}}<form method="post" action="/packages/acquire">
    <input type="hidden" name="package_id" value="{{.ID}}">
    <button class="choice" type="submit"><span class="c-text"><span class="c-name" dir="auto">{{.Name}}</span>{{if .Detail}}<span class="c-detail">{{.Detail}}</span>{{end}}</span>` + iconChevNext + `</button>
  </form>{{end}}
  </div>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
<script nonce="{{.Nonce}}">
  // One tap, one acquisition: once a package is sent, the others stand still until the next page arrives.
  (function () {
    var forms = document.querySelectorAll('.choice-list form');
    for (var i = 0; i < forms.length; i++) {
      forms[i].addEventListener('submit', function () {
        setTimeout(function () {
          var bs = document.querySelectorAll('.choice-list button');
          for (var k = 0; k < bs.length; k++) bs[k].disabled = true;
        }, 0);
      });
    }
  })();
</script>
</body></html>`

// ============================================================================================================
// SIGN-IN DID NOT WORK (a flow that has left the sign-in page, e.g. the social provider's return)
// ============================================================================================================

const errorHTML = guestHead + `
<title>{{.Title}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <div class="status-icon status-icon--err" aria-hidden="true">` + iconAlert + `</div>
  <h1 class="page-title">{{.Title}}</h1>
  <p class="page-lead" role="alert">{{.Message}}</p>
  <div class="actions"><a class="btn" href="{{.BackHref}}">{{.BackLabel}}</a></div>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
</body></html>`

// ============================================================================================================
// CONNECTION STATUS (a guest who follows the "Status" link, or otherwise navigates to /status)
// ============================================================================================================

// statusHTML draws what scd's session status says, in words: the device is online, and -- only on a package
// that has an online-time allowance, the one case where scd reports it -- how much of that time is left and
// when the access ends regardless. A script-driven fetch of /status still gets scd's JSON (main.go); only a
// browser navigating here gets this page. The actions are the online page's own: back to it, and Disconnect.
const statusHTML = guestHead + `
<title>{{index .T "online.status"}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <div class="status-icon" aria-hidden="true">` + iconCheck + `</div>
  <h1 class="page-title">{{index .T "online.title"}}</h1>
  <p class="page-lead">{{index .T "online.lead"}}</p>
  {{if .HasTime}}
  <div class="fact"><span class="fact-label">{{index .T "online.remaining"}}</span><span class="fact-value">{{.TimeLeft}}</span></div>
  <p class="tl-note">{{index .T "tl.note"}}</p>
  {{with .HardExpiry}}<p class="tl-note" id="st-ends" data-at="{{.}}" hidden></p>{{end}}
  {{end}}
  <div class="actions">
    <a class="btn btn--outline" href="{{.BackHref}}">{{index .T "online.back"}}</a>
    <form method="POST" action="/logout"><button class="btn btn--outline" type="submit">{{index .T "online.disconnect"}}</button></form>
  </div>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
<script nonce="{{.Nonce}}">
  // The end of the access is a moment, shown in the device's own clock and the page's language.
  (function () {
    var el = document.getElementById('st-ends');
    if (!el) return;
    var ts = Date.parse(el.getAttribute('data-at'));
    if (isNaN(ts)) return;
    var d = new Date(ts), when;
    try { when = d.toLocaleString(document.documentElement.lang || undefined); } catch (e) { when = d.toLocaleString(); }
    el.textContent = String({{index .T "tl.ends"}}).split('{date}').join(when);
    el.hidden = false;
  })();
</script>
</body></html>`

// ============================================================================================================
// YOU'RE ONLINE
// ============================================================================================================

const successHTML = guestHead + `
<title>{{index .T "online.title"}}</title>
<script nonce="{{.Nonce}}">
  // The words this page's own scripts write, in the guest's language. Everything else is rendered.
  var T = {{.JS}};
  function t(k) { return T[k] || k; }
  function fill(s, k, v) { return String(s).split('{' + k + '}').join(String(v)); }
</script>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <div class="status-icon" aria-hidden="true">` + iconCheck + `</div>
  <h1 class="page-title">{{index .T "online.title"}}</h1>
  <p class="page-lead">{{index .T "online.lead"}}</p>
  <div class="fact">
    {{if .DurationSeconds}}<span class="fact-label">{{index .T "online.remaining"}}</span><span class="fact-value">{{.HumanRemaining}}</span>
    {{else}}<span class="fact-label">{{index .T "online.remaining"}}</span><span class="fact-value">{{index .T "online.unlimited"}}</span>{{end}}
  </div>
  <div class="actions">
    <a class="btn btn--outline" href="/status?s={{.SessionID}}&amp;t={{.DurationSeconds}}">{{index .T "online.status"}}</a>
    <form method="POST" action="/logout"><button class="btn btn--outline" type="submit">{{index .T "online.disconnect"}}</button></form>
  </div>

  {{if .CommerceEnabled}}
  <div id="commerce" class="section" data-commerce="on">
    <h2 dir="auto">{{index .CX "cx.title"}}</h2>
    <div id="cx-list" aria-busy="true">{{index .CX "cx.loading"}}</div>
    <div id="cx-quote" hidden></div>
    <div id="cx-note" role="status" aria-live="polite"></div>
  </div>
  <script nonce="{{.Nonce}}">
  (function(){
    // The panel's words, in the guest's language with English underneath (commerce_strings.go). Portal-only:
    // this table is not part of the dictionary a hotel overrides.
    var CX = {{.CXJS}};
    function cx(k){ return CX[k] || k; }
    var list = document.getElementById('cx-list');
    var quoteBox = document.getElementById('cx-quote');
    var note = document.getElementById('cx-note');
    var busy = false;
    function num(n){ return String(n); }
    function fmtBytes(n){
      if(!n) return cx('cx.unlimited');
      var u=['cx.b','cx.kb','cx.mb','cx.gb','cx.tb']; var i=0;
      while(n>=1024&&i<u.length-1){n/=1024;i++;}
      return fill(cx(u[i]), 'n', n.toFixed(n<10&&i>0?1:0));
    }
    function fmtDur(s){
      if(!s) return cx('cx.unlimited');
      var h=Math.floor(s/3600), m=Math.floor((s%3600)/60), out=[];
      if(h>0) out.push(fill(t('unit.h'), 'n', h));
      if(m>0 || h===0) out.push(fill(t('unit.min'), 'n', m));
      return out.join(' ');
    }
    // An end-mode CODE is never shown: each known code has its words, and one this page does not know reads
    // as the hotel's own arrangement.
    function endWords(code){ var k = 'cx.end.' + String(code || 'MANUAL_END'); return CX[k] || cx('cx.end.other'); }
    function devices(d){ return fill(cx('cx.devices'), 'n', num(d.max_concurrent_devices||1)); }
    function unavailable(msg){ note.className='cx-err'; note.textContent = msg||cx('cx.unavailable'); }
    function clearNote(){ note.className=''; note.textContent=''; }
    // NOTHING TO OFFER IS NOT AN ERROR ON THIS PAGE. The guest reading it is already online: the sign-in that
    // got them here is spent, so the list is refused or empty. Showing "unavailable" or "none" under a
    // success message reads as if their connection failed, so the panel simply goes away.
    var panel = document.getElementById('commerce');
    function nothingToOffer(){ panel.hidden = true; }
    function loadPackages(){
      clearNote();
      fetch('/api/commerce/packages', {headers:{'Accept':'application/json'}}).then(function(r){
        if(!r.ok){ nothingToOffer(); return null; }
        return r.json();
      }).then(function(data){
        if(!data){ return; }
        list.removeAttribute('aria-busy');
        var pkgs = (data.packages||[]);
        if(pkgs.length===0){ nothingToOffer(); return; }
        list.innerHTML='';
        pkgs.forEach(function(p){
          var d = p.display||{};
          var el = document.createElement('div'); el.className='pkg';
          var speed = (d.down_kbps? fill(cx('cx.down'), 'n', Math.round(d.down_kbps/1000)):'')+
                      (d.up_kbps? (' / '+fill(cx('cx.up'), 'n', Math.round(d.up_kbps/1000))):'');
          // The name is the hotel's, in whatever script it was written: isolated, but aligned with the page.
          el.innerHTML = '<h3><bdi></bdi></h3><div class="meta"></div>';
          el.querySelector('bdi').textContent = d.name || cx('cx.default');
          el.querySelector('.meta').textContent = [
            speed,
            fill(cx('cx.data'), 'v', fmtBytes(d.data_quota_bytes)),
            fill(cx('cx.time'), 'v', fmtDur(d.time_quota_seconds)),
            devices(d),
            fill(cx('cx.ends'), 'v', endWords(d.end_mode)),
          ].filter(Boolean).join(' · ');
          var btn = document.createElement('button'); btn.type='button'; btn.textContent=cx('cx.select'); btn.className='btn btn--sm';
          btn.addEventListener('click', function(){ requestQuote(p.package_id, btn); });
          el.appendChild(btn);
          list.appendChild(el);
        });
      }).catch(function(){ nothingToOffer(); });
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
        '<div class="pkg"><h3></h3>'+
        '<div class="meta"></div>'+
        '<div class="meta"></div>'+
        '<button type="button" id="cx-confirm" class="btn btn--sm"></button></div>';
      quoteBox.querySelector('h3').textContent = cx('cx.confirm.title');
      var metas = quoteBox.querySelectorAll('.meta');
      metas[0].textContent = (d.name||cx('cx.default'))+' — '+cx('cx.free')+' · '+devices(d)+' · '+
        fill(cx('cx.ends'), 'v', endWords(d.end_mode));
      // The offer's expiry, in the device's own clock and the page's language; nothing if it is not a time.
      var ts = Date.parse(q.expires_at || ''), when = '';
      if(!isNaN(ts)){
        try { when = new Date(ts).toLocaleString(document.documentElement.lang || undefined); } catch (e) { when = new Date(ts).toLocaleString(); }
      }
      if(when) metas[1].textContent = fill(cx('cx.expires'), 'date', when); else metas[1].hidden = true;
      var cbtn = document.getElementById('cx-confirm');
      cbtn.textContent = cx('cx.confirm');
      cbtn.addEventListener('click', function(){ confirmQuote(q.quote_id, cbtn); });
      list.hidden = true;
    }
    function confirmQuote(quoteId, cbtn){
      if(busy) return; busy=true; cbtn.disabled=true; clearNote();
      fetch('/api/commerce/confirm', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({quote_id: quoteId})})
        .then(function(r){ return r.ok? r.json() : null; })
        .then(function(res){
          busy=false;
          if(!res || !res.entitlement_id){ cbtn.disabled=false; unavailable(cx('cx.expired')); return; }
          quoteBox.innerHTML = '<div class="pkg"><h3></h3><div class="meta"></div></div>';
          quoteBox.querySelector('h3').textContent = cx('cx.active.title');
          quoteBox.querySelector('.meta').textContent = cx('cx.active');
        }).catch(function(){ busy=false; cbtn.disabled=false; unavailable(); });
    }
    loadPackages();
  })();
  </script>
  {{end}}

  <!-- YOUR TIME (Phase 6, DARK). Hidden until the appliance answers with an aggregate package. TWO CLOCKS,
       BOTH SHOWN: remaining online time counts down only while connected; the hard expiry is a calendar
       instant that arrives whether the minutes were used or not. -->
  <div id="timeleft" class="section" hidden>
    <div class="tl-main" id="tl-remaining"></div>
    <div class="tl-note">{{index .T "tl.note"}}</div>
    <div class="tl-note" id="tl-expiry" hidden></div>
  </div>
  <script nonce="{{.Nonce}}">
  (function(){
    var box = document.getElementById('timeleft');
    var main = document.getElementById('tl-remaining');
    var exp = document.getElementById('tl-expiry');
    function human(s){
      if(s <= 0) return t('tl.none');
      var mins = Math.round(s/60), h = Math.floor(mins/60), m = mins % 60, out = [];
      if(mins < 1) return '< ' + fill(t('unit.min'), 'n', 1);
      if(h > 0) out.push(fill(t('unit.h'), 'n', h));
      if(m > 0) out.push(fill(t('unit.min'), 'n', m));
      return out.join(' ');
    }
    function day(iso){
      var ts = Date.parse(iso);
      if(isNaN(ts)) return '';
      var d = new Date(ts);
      try { return d.toLocaleString(document.documentElement.lang || undefined); } catch (e) { return d.toLocaleString(); }
    }
    fetch('/status', {headers:{'Accept':'application/json'}})
      .then(function(r){ return r.ok ? r.json() : null; })
      .then(function(st){
        if(!st || st.time_mode !== 'AGGREGATE_ONLINE_TIME') return;  // every other package: nothing changes
        main.textContent = fill(t('tl.left'), 't', human(st.remaining_online_seconds));
        if(st.hard_expiry){
          var when = day(st.hard_expiry);
          if(when){
            exp.textContent = fill(t('tl.ends'), 'date', when);
            exp.hidden = false;
          }
        }
        box.hidden = false;
      })
      .catch(function(){ /* the ordinary page is the fallback */ });
  })();
  </script>

  <!-- YOUR DEVICES (Phase 6, DARK). Hidden until the appliance answers with a list; on an appliance where the
       capability is not deployed or is switched off, the answer is the uniform non-success and this panel
       never appears. No hardware address and no internal identifier is ever shown: the opaque id travels in
       the release request, never in the text. -->
  <div id="devices" class="section" hidden>
    <h2>{{index .T "dev.title"}}</h2>
    <p class="lead">{{index .T "dev.lead"}}</p>
    <div id="dv-list"></div>
    <div id="dv-note" role="status" aria-live="polite"></div>
  </div>
  <script nonce="{{.Nonce}}">
  (function(){
    var panel = document.getElementById('devices');
    var list  = document.getElementById('dv-list');
    var note  = document.getElementById('dv-note');
    var busy  = false;
    var ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><rect x="6.5" y="2.5" width="11" height="19" rx="2.2"/><path d="M11 18.3h2"/></svg>';

    function ago(iso){
      if(!iso) return '';
      var ts = Date.parse(iso);
      if(isNaN(ts)) return '';
      var mins = Math.floor((Date.now()-ts)/60000), v;
      if(mins < 1) return fill(t('dev.lastused'), 't', t('dev.justnow'));
      if(mins < 60) v = fill(t('unit.min'), 'n', mins);
      else if(mins < 1440) v = fill(t('unit.h'), 'n', Math.floor(mins/60));
      else v = fill(t('unit.d'), 'n', Math.floor(mins/1440));
      return fill(t('dev.lastused'), 't', fill(t('dev.ago'), 't', v));
    }
    // Every refusal is the same sentence. The appliance does not tell the guest why a removal failed, and
    // neither does this page.
    function refused(){ note.className='dv-err'; note.textContent=t('dev.refused'); }
    function clearNote(){ note.className=''; note.textContent=''; }

    function render(devices){
      list.innerHTML='';
      devices.forEach(function(d, i){
        var el = document.createElement('div'); el.className='dev';
        var icon = document.createElement('span'); icon.className='dev-icon'; icon.innerHTML = ICON;
        var text = document.createElement('div'); text.className='dev-text';
        var name = document.createElement('div'); name.className='name';
        name.textContent = fill(t('dev.name'), 'n', i+1);
        var meta = document.createElement('div'); meta.className='meta';
        var badge = document.createElement('span');
        badge.className = d.online ? 'badge badge--ok' : 'badge';
        badge.textContent = d.online ? t('dev.online') : t('dev.offline');
        meta.appendChild(badge);
        var when = ago(d.last_seen);
        if(when) meta.appendChild(document.createTextNode(when));
        text.appendChild(name); text.appendChild(meta);
        el.appendChild(icon); el.appendChild(text);

        if(d.removable){
          var btn = document.createElement('button');
          btn.type='button';
          btn.className='btn btn--danger btn--sm';
          btn.textContent=t('dev.remove');
          btn.setAttribute('aria-label', fill(t('dev.name'), 'n', i+1) + ' — ' + t('dev.remove'));
          btn.addEventListener('click', function(){ ask(el, d.id, btn, i+1); });
          el.appendChild(btn);
        } else {
          // An online device is never removable, and saying so plainly is better than offering a button that
          // will refuse: the guest is told what to do instead.
          var why = document.createElement('div'); why.className='inuse';
          why.textContent = d.online ? t('dev.inuse') : t('dev.cannot');
          el.appendChild(why);
        }
        list.appendChild(el);
      });
    }

    // THE CONFIRMATION IS PART OF THE PAGE, not the browser's own dialog: it is in the guest's language, it
    // states the consequence, focus lands on the safe answer, and Escape keeps the device.
    function ask(el, id, btn, n){
      if(busy) return;
      clearNote();
      var row = document.createElement('div'); row.className='confirm-row';
      row.setAttribute('role','group');
      var q = document.createElement('p'); q.id = 'dv-q-' + n;
      q.textContent = fill(t('dev.confirm'), 'n', n);
      row.setAttribute('aria-labelledby', q.id);
      var bar = document.createElement('div'); bar.className='row';
      var yes = document.createElement('button'); yes.type='button'; yes.className='btn btn--solid-danger btn--sm';
      yes.textContent = t('dev.yes');
      var no = document.createElement('button'); no.type='button'; no.className='btn btn--outline btn--sm';
      no.textContent = t('dev.keep');
      function close(){ if(row.parentNode) row.parentNode.removeChild(row); btn.hidden = false; btn.focus(); }
      no.addEventListener('click', close);
      row.addEventListener('keydown', function(e){ if(e.key === 'Escape' || e.key === 'Esc'){ e.preventDefault(); close(); } });
      yes.addEventListener('click', function(){ yes.disabled = true; no.disabled = true; release(id, btn, row); });
      bar.appendChild(yes); bar.appendChild(no);
      row.appendChild(q); row.appendChild(bar);
      btn.hidden = true;
      el.appendChild(row);
      no.focus();
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

    function release(id, btn, row){
      if(busy) return;
      busy = true; clearNote();
      fetch('/devices/release', {method:'POST', headers:{'Content-Type':'application/json'},
                                 body: JSON.stringify({device_id: id})})
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(res){
          busy = false;
          if(!res || !res.ok){
            if(row.parentNode) row.parentNode.removeChild(row);
            btn.hidden = false; btn.disabled = false; refused(); return;
          }
          note.className='dv-done';
          note.textContent = t('dev.removed');
          load(false);
        })
        .catch(function(){
          busy = false;
          if(row.parentNode) row.parentNode.removeChild(row);
          btn.hidden = false; btn.disabled = false; refused();
        });
    }

    load(true);
  })();
  </script>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
</body></html>`
