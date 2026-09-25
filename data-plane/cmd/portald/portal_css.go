package main

// THE PORTAL'S LOOK, AS ONE TOKEN SET.
//
// Every guest page -- sign-in, package choice, "You're online", the failure page -- is drawn from these two
// sheets. The first is the default look: the OneGate palette, radius scale and type roles as CSS custom
// properties (design-system/README.md, "Guest portal"), which the hotel's Portal settings override one
// property at a time. The second arranges the SAME elements into the six layouts. A layout never introduces a
// colour or a radius of its own; it reads the tokens, which is why each one stays coherent under any brand
// colour and any photograph.
//
// The page is served to captive-portal mini-browsers and old phones, with no internet behind them. So: no
// fonts to download (the platform's own, with Arabic-capable faces named for Arabic), no framework, and every
// newer CSS feature has a plain fallback declared before it or degrades to something that still works.
//
// Token map (hotel setting -> property):
//   brand colour -> --sc-brand          darker shade -> --sc-brand-dark     text colour -> --sc-ink
//   corner radius -> --sc-radius (cards; controls are 0.6 of it)            typeface -> font-family on <html>
//   background photo -> --sc-bg         hero photo -> --sc-hero             photo darkening -> --sc-overlay
//   heading typeface -> --sc-heading-font
// Layout options arrive as attributes on <html>: data-template, data-density, data-panel, data-hero,
// data-surface.

const portalBaseCSS = `
  :root {
    --sc-brand: #1773bd;
    --sc-brand-dark: #125c97;
    --sc-on-brand: #ffffff;
    --sc-ink: #14161a;
    --sc-muted: #5a6270;
    --sc-canvas: #f1f2f4;
    --sc-card: #ffffff;
    --sc-recess: #f6f7f9;
    --sc-fill: #eceef1;
    --sc-line: #dcdfe4;
    --sc-edge: #7f8795;
    --sc-radius: 12px;
    --sc-r-ctl: calc(var(--sc-radius) * 0.6);
    --sc-ctl: 48px;
    --sc-gap: 16px;
    --sc-pad: 28px;
    --sc-bg: none;
    --sc-hero: var(--sc-bg);
    --sc-overlay: 0.35;
    --sc-shadow-card: 0 1px 2px rgba(18, 22, 28, 0.06), 0 8px 24px rgba(18, 22, 28, 0.08);
    --sc-shadow-float: 0 1px 3px rgba(18, 22, 28, 0.08), 0 18px 48px rgba(18, 22, 28, 0.16);
    --sc-font: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Noto Sans",
      "Noto Sans Arabic", "Geeza Pro", Tahoma, sans-serif;
    font-family: var(--sc-font);
    line-height: 1.45;
    -webkit-text-size-adjust: 100%;
    text-size-adjust: 100%;
  }
  /* Arabic: faces drawn for the script first, a little more leading, and no tracking -- letter-spacing breaks
     the joins between Arabic letters. A hotel typeface, set on <html>, still wins. */
  html[lang|="ar"] {
    --sc-font: "Segoe UI", "SF Arabic", "Geeza Pro", "Noto Naskh Arabic", "Noto Sans Arabic", Tahoma,
      -apple-system, BlinkMacSystemFont, Roboto, Arial, sans-serif;
    line-height: 1.6;
  }
  html[lang|="ar"] * { letter-spacing: 0; }
  [data-density="compact"] { --sc-ctl: 44px; --sc-gap: 12px; --sc-pad: 22px; }
  [data-density="spacious"] { --sc-ctl: 56px; --sc-gap: 22px; --sc-pad: 36px; }

  *, *::before, *::after { box-sizing: border-box; }
  html { height: 100%; }
  body {
    margin: 0;
    min-height: 100%;
    color: var(--sc-ink);
    font-size: 1rem;
    /* Classic: the hotel's photograph over everything when there is one; otherwise the canvas with a band of
       the brand colour across the top, which the card overlaps -- a finished look with nothing uploaded. */
    background-color: var(--sc-canvas);
    background-image: var(--sc-bg), linear-gradient(160deg, var(--sc-brand), var(--sc-brand-dark));
    background-size: cover, 100% 240px;
    background-position: center, 0 0;
    background-repeat: no-repeat;
  }
  @media (min-width: 640px) { body { background-size: cover, 100% 38vh; } }
  img { border: 0; }
  a { color: var(--sc-brand-dark); }
  h1, h2, h3, p { margin: 0; }
  [hidden] { display: none; }
  .sc-vh {
    position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; border: 0;
    overflow: hidden; clip: rect(0 0 0 0); clip-path: inset(50%); white-space: nowrap;
  }

  /* THE PAGE IS A COLUMN. The card is centred in it with auto margins, so a card taller than the screen
     simply scrolls instead of being clipped at the top. */
  .page {
    position: relative;
    min-height: 100vh;
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 72px 16px 32px;
  }
  @media (min-width: 640px) { .page { padding: 88px 24px 48px; } }

  /* ---- language pill: top corner, top-left in Arabic ----------------------------------------------------- */
  .langbar { position: absolute; top: 14px; right: 16px; z-index: 5; }
  [dir="rtl"] .langbar { right: auto; left: 16px; }
  .lang { position: relative; display: inline-block; }
  .lang select {
    -webkit-appearance: none; appearance: none;
    height: 44px; margin: 0; padding: 0 34px 0 38px;
    border: 1px solid rgba(20, 22, 26, 0.12); border-radius: 999px;
    background: rgba(255, 255, 255, 0.95); color: var(--sc-ink);
    font: inherit; font-size: 0.875rem; font-weight: 600; cursor: pointer;
    box-shadow: 0 1px 2px rgba(18, 22, 28, 0.08), 0 4px 14px rgba(18, 22, 28, 0.10);
  }
  [dir="rtl"] .lang select { padding: 0 38px 0 34px; }
  .lang svg {
    position: absolute; top: 50%; width: 18px; height: 18px; margin-top: -9px;
    color: var(--sc-muted); pointer-events: none;
  }
  .lang .i-globe { left: 13px; }
  .lang .i-chev { right: 12px; width: 16px; height: 16px; margin-top: -8px; }
  [dir="rtl"] .lang .i-globe { left: auto; right: 13px; }
  [dir="rtl"] .lang .i-chev { right: auto; left: 12px; }

  /* ---- the card ------------------------------------------------------------------------------------------- */
  .card {
    position: relative;
    width: 100%;
    max-width: 460px;
    margin-top: auto;
    margin-bottom: auto;
    background: var(--sc-card);
    border-radius: var(--sc-radius);
    box-shadow: var(--sc-shadow-float);
    padding: var(--sc-pad) 20px 12px;
  }
  @media (min-width: 640px) { .card { padding: calc(var(--sc-pad) + 8px) calc(var(--sc-pad) + 8px) 16px; } }

  .sc-brandblock { text-align: center; margin: 0 0 24px; }
  .brand { display: flex; flex-direction: column; align-items: center; }
  .brand img { display: block; max-height: 56px; max-width: 220px; margin: 0 0 12px; object-fit: contain; }
  .brand-mark {
    display: flex; align-items: center; justify-content: center;
    width: 48px; height: 48px; margin: 0 0 12px;
    border-radius: calc(var(--sc-radius) * 0.9);
    background: var(--sc-brand); color: var(--sc-on-brand);
  }
  .brand-mark svg { width: 26px; height: 26px; }
  .brand.has-logo .brand-mark { display: none; }
  .brand .name {
    font-family: var(--sc-heading-font, inherit);
    font-size: 1.375rem; font-weight: 700; line-height: 1.25; letter-spacing: -0.01em;
    overflow-wrap: anywhere;
  }
  .welcome { margin: 6px auto 0; max-width: 42ch; color: var(--sc-muted); font-size: 0.9375rem; }

  /* ---- notices: amber advisory, red refusal. Each carries an icon AND words -- never colour alone. --------- */
  .notice {
    display: none;
    margin: 0 0 16px; padding: 12px 14px;
    border: 1px solid #efd2a0; border-radius: var(--sc-r-ctl);
    background: #fbf0dd; color: #7d4a06;
    font-size: 0.875rem; line-height: 1.45;
  }
  .notice.show { display: flex; align-items: flex-start; }
  .notice--error { border-color: #f4c0bb; background: #fdeceb; color: #8f1c13; }
  .notice::before, .err::before {
    content: "!";
    flex: 0 0 auto;
    width: 18px; height: 18px; margin: 1px 10px 0 0;
    border-radius: 50%;
    background: #9a5b0a; color: #fff;
    font: 700 12px/18px Arial, sans-serif; text-align: center;
  }
  .notice--error::before, .err::before { background: #b42318; }
  [dir="rtl"] .notice::before, [dir="rtl"] .err::before { margin: 1px 0 0 10px; }

  .err {
    display: flex; align-items: flex-start;
    margin: 12px 0 0; padding: 10px 12px;
    border: 1px solid #f4c0bb; border-radius: var(--sc-r-ctl);
    background: #fdeceb; color: #8f1c13;
    font-size: 0.875rem; line-height: 1.45;
  }
  .err:empty { margin: 0; padding: 0; border: 0; background: none; }
  .err:empty::before { display: none; }
  /* "You can try again now." is not a refusal: information blue, an "i", the same words. */
  .err.err--ok { border-color: #bcd4e8; background: #eaf2f8; color: #1d4f7c; }
  .err.err--ok::before { content: "i"; background: #245d92; }

  /* ---- the two group tabs: a segmented control ------------------------------------------------------------ */
  .tabs {
    display: grid; grid-template-columns: 1fr 1fr; grid-gap: 4px; gap: 4px;
    margin: 0 0 20px; padding: 4px;
    border-radius: calc(var(--sc-r-ctl) + 4px);
    background: var(--sc-fill);
  }
  .tab {
    -webkit-appearance: none; appearance: none;
    display: inline-flex; align-items: center; justify-content: center;
    min-height: 44px; margin: 0; padding: 8px 10px;
    border: 0; border-radius: var(--sc-r-ctl);
    background: transparent; color: var(--sc-muted);
    font: inherit; font-size: 0.9375rem; font-weight: 600; line-height: 1.2; cursor: pointer;
  }
  .tab svg { flex: 0 0 auto; width: 18px; height: 18px; margin: 0 8px 0 0; }
  [dir="rtl"] .tab svg { margin: 0 0 0 8px; }
  .tab:hover { color: var(--sc-ink); }
  .tab[aria-selected="true"] {
    background: var(--sc-card); color: var(--sc-ink);
    box-shadow: 0 1px 2px rgba(18, 22, 28, 0.10), 0 1px 1px rgba(18, 22, 28, 0.04);
  }
  .tab[aria-selected="true"] svg { color: var(--sc-brand); }

  .panel { display: none; }
  .panel.active { display: block; }

  /* ---- fields ---------------------------------------------------------------------------------------------- */
  .field { margin: 0 0 var(--sc-gap); }
  .field label, .field .label { display: block; margin: 0 0 6px; font-size: 0.875rem; font-weight: 600; color: var(--sc-ink); }
  input[type=text], input[type=password], input[type=email], input[type=tel] {
    -webkit-appearance: none; appearance: none;
    display: block; width: 100%; min-height: var(--sc-ctl); margin: 0; padding: 0 14px;
    border: 1px solid var(--sc-edge); border-radius: var(--sc-r-ctl);
    background: #fff; color: var(--sc-ink);
    font: inherit; font-size: 1rem; /* 16px: iOS does not zoom a field this size */
    box-shadow: inset 0 1px 2px rgba(18, 22, 28, 0.04);
  }
  input::placeholder { color: #80868f; opacity: 1; }
  input[type=text]:hover, input[type=password]:hover, input[type=email]:hover, input[type=tel]:hover { border-color: var(--sc-muted); }
  #voucher { text-transform: uppercase; letter-spacing: 0.08em; }
  #voucher::placeholder { text-transform: none; letter-spacing: 0; }
  [data-stage="code"] input[name=code], #pms-room, #ps-pin { letter-spacing: 0.04em; font-variant-numeric: tabular-nums; }
  [data-stage="code"] input[name=code] { letter-spacing: 0.3em; font-size: 1.25rem; text-align: center; }
  .hint, .small { color: var(--sc-muted); font-size: 0.8125rem; line-height: 1.45; }
  .hint { margin: 6px 0 0; }
  .small { margin: 0 0 14px; }
  .dest { color: var(--sc-ink); font-weight: 600; overflow-wrap: anywhere; }

  /* ---- buttons: one primary per screen ------------------------------------------------------------------ */
  button.primary, .btn {
    display: flex; align-items: center; justify-content: center;
    width: 100%; min-height: var(--sc-ctl); margin: 0; padding: 0 20px;
    border: 0; border-radius: var(--sc-r-ctl);
    background: var(--sc-brand); color: var(--sc-on-brand);
    font: inherit; font-size: 1rem; font-weight: 600; line-height: 1.2; text-align: center;
    text-decoration: none; cursor: pointer;
    box-shadow: 0 1px 2px rgba(18, 22, 28, 0.10);
  }
  button.primary:hover, .btn:hover { background: var(--sc-brand-dark); }
  button.primary:active, .btn:active { box-shadow: inset 0 1px 2px rgba(18, 22, 28, 0.25); }
  button.primary:disabled { opacity: 0.55; cursor: default; }
  .btn--outline {
    border: 1px solid var(--sc-edge); background: var(--sc-card); color: var(--sc-ink);
    box-shadow: 0 1px 2px rgba(18, 22, 28, 0.06);
  }
  .btn--outline:hover { background: var(--sc-recess); }
  .btn--danger { border: 1px solid #e3a49c; background: var(--sc-card); color: #b42318; }
  .btn--danger:hover { background: #fdeceb; }
  .btn--solid-danger { background: #b42318; color: #fff; }
  .btn--solid-danger:hover { background: #8f1c13; }
  .btn--sm { width: auto; min-height: 44px; padding: 0 14px; font-size: 0.875rem; }
  button.link {
    display: inline-flex; align-items: center;
    min-height: 44px; margin: 6px 0 0; padding: 0 2px;
    border: 0; background: none; color: var(--sc-brand-dark);
    font: inherit; font-size: 0.9375rem; font-weight: 600; cursor: pointer;
    text-decoration: underline; text-underline-offset: 3px;
  }

  /* ---- "Use Personal Account": a switch, with its words ------------------------------------------------- */
  .pill {
    display: inline-flex; align-items: center; justify-content: space-between;
    width: 100%; min-height: 52px; margin: 0 0 18px; padding: 6px 14px;
    border: 1px solid var(--sc-line); border-radius: var(--sc-r-ctl);
    background: var(--sc-recess); color: var(--sc-ink);
    font-size: 0.9375rem; font-weight: 600; cursor: pointer;
    -webkit-user-select: none; user-select: none;
  }
  .pill input {
    -webkit-appearance: none; appearance: none;
    flex: 0 0 auto; width: 44px; height: 26px; min-height: 26px; margin: 0 0 0 12px;
    border-radius: 999px; cursor: pointer;
    background: var(--sc-edge) radial-gradient(circle closest-side, #fff 88%, rgba(255, 255, 255, 0) 100%) no-repeat;
    background-size: 20px 20px; background-position: 3px 50%;
  }
  .pill input:checked { background-color: var(--sc-brand); background-position: 21px 50%; }
  [dir="rtl"] .pill input { margin: 0 12px 0 0; background-position: 21px 50%; }
  [dir="rtl"] .pill input:checked { background-position: 3px 50%; }

  /* ---- "Or sign in with" ----------------------------------------------------------------------------- */
  .alt { margin-top: 24px; padding-top: 18px; border-top: 1px solid var(--sc-line); }
  .alt h3 {
    margin: 0 0 10px; color: var(--sc-muted);
    font-size: 0.75rem; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase;
  }
  .alt button.link {
    margin: 0 8px 8px 0; padding: 0 16px;
    border: 1px solid var(--sc-line); border-radius: 999px;
    background: var(--sc-card); color: var(--sc-ink); text-decoration: none;
  }
  .alt button.link:hover { border-color: var(--sc-edge); }
  /* The way in on screen: an ink outline twice as heavy -- marked by weight, not by colour. */
  .alt button.link[aria-pressed="true"] { border-color: var(--sc-ink); box-shadow: inset 0 0 0 1px var(--sc-ink); }
  [dir="rtl"] .alt button.link { margin: 0 0 8px 8px; }

  /* ---- social providers and package choices ----------------------------------------------------------- */
  .social-btn {
    display: flex; align-items: center; justify-content: center;
    min-height: var(--sc-ctl); margin: 0 0 10px; padding: 0 16px;
    border: 1px solid var(--sc-edge); border-radius: var(--sc-r-ctl);
    background: #fff; color: var(--sc-ink);
    font-weight: 600; text-decoration: none;
  }
  .social-btn:hover { background: var(--sc-recess); }
  .choices-title { margin: 0 0 12px; font-size: 1rem; font-weight: 700; }
  button.choice {
    display: flex; align-items: center;
    width: 100%; min-height: 64px; margin: 0 0 10px; padding: 12px 14px 12px 16px;
    border: 1px solid var(--sc-line); border-radius: var(--sc-r-ctl);
    background: var(--sc-card); color: var(--sc-ink);
    font: inherit; text-align: left; cursor: pointer;
    box-shadow: 0 1px 2px rgba(18, 22, 28, 0.05);
  }
  [dir="rtl"] button.choice { text-align: right; padding: 12px 16px 12px 14px; }
  button.choice:hover { border-color: var(--sc-brand); box-shadow: 0 5px 14px rgba(22, 28, 32, 0.08); }
  button.choice[disabled] { opacity: 0.55; cursor: default; }
  .c-text { flex: 1 1 auto; min-width: 0; }
  .c-name { display: block; font-size: 1rem; font-weight: 650; overflow-wrap: anywhere; }
  .c-detail { display: block; margin-top: 2px; color: var(--sc-muted); font-size: 0.875rem; }
  .c-chev { flex: 0 0 auto; width: 20px; height: 20px; margin: 0 0 0 12px; color: var(--sc-muted); }
  [dir="rtl"] .c-chev, [dir="rtl"] .i-dir { transform: scaleX(-1); }
  [dir="rtl"] .c-chev { margin: 0 12px 0 0; }

  /* ---- the hotel's own words, and the footer ---------------------------------------------------------- */
  .sc-extras { margin-top: 20px; }
  .sc-extras[data-empty] { display: none; }
  .help { color: var(--sc-muted); font-size: 0.875rem; line-height: 1.55; white-space: pre-line; }
  /* The hotel's own words are one string in whatever language it wrote them (dir="auto" keeps their
     punctuation in place); on an Arabic page they still sit on the Arabic side. */
  [dir="rtl"] .help, [dir="rtl"] .sc-hero-inner { text-align: right; }
  #custom-html { margin-top: 12px; font-size: 0.875rem; }
  #custom-html:empty { display: none; }
  .sc-foot {
    display: flex; flex-wrap: wrap; align-items: center;
    margin-top: 24px; padding-top: 6px; border-top: 1px solid var(--sc-line);
  }
  .terms { font-size: 0.875rem; }
  .terms a { display: inline-flex; align-items: center; min-height: 44px; font-weight: 600; text-underline-offset: 3px; }
  .info-btn {
    display: inline-flex; align-items: center; justify-content: center;
    width: 44px; height: 44px; margin: 0 -8px 0 auto; padding: 0;
    border: 0; border-radius: 999px;
    background: transparent; color: var(--sc-muted); cursor: pointer;
  }
  [dir="rtl"] .info-btn { margin: 0 auto 0 -8px; }
  .info-btn svg { width: 22px; height: 22px; }
  .info-btn:hover, .info-btn[aria-expanded="true"] { background: var(--sc-fill); color: var(--sc-ink); }
  .info-panel {
    flex: 1 1 100%; margin: 4px 0 12px; padding: 14px 16px;
    border: 1px solid var(--sc-line); border-radius: var(--sc-r-ctl);
    background: var(--sc-recess); color: var(--sc-muted); font-size: 0.875rem;
  }
  .info-panel strong { display: block; color: var(--sc-ink); }
  .info-panel dl { display: grid; grid-template-columns: auto 1fr; grid-gap: 6px 16px; gap: 6px 16px; margin: 10px 0 0; }
  .info-panel dt, .info-panel dd { margin: 0; }
  .info-panel dd {
    color: var(--sc-ink); font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 0.8125rem; overflow-wrap: anywhere;
  }
  .info-panel p { margin: 10px 0 0; }

  /* ---- the lightbulb and its sheet ---------------------------------------------------------------------
     A <details> first (the tips open in place with no script); guestHelpScript then moves the sheet to <body>
     as a modal (.is-modal). The lightbulb sits beside the information button, at the end of the footer row. */
  .sc-help { display: inline-block; margin: 0 0 0 auto; }
  [dir="rtl"] .sc-help { margin: 0 auto 0 0; }
  .sc-help + .info-btn { margin: 0 -8px 0 0; }
  [dir="rtl"] .sc-help + .info-btn { margin: 0 0 0 -8px; }
  .sc-foot--page .sc-help { margin: 0 -8px 0 auto; }
  [dir="rtl"] .sc-foot--page .sc-help { margin: 0 auto 0 -8px; }
  .sc-help > summary { list-style: none; }
  .sc-help > summary::-webkit-details-marker { display: none; }
  .sc-help > summary::marker { content: ""; }
  .help-btn {
    display: inline-flex; align-items: center; justify-content: center;
    width: 44px; height: 44px; padding: 0; border-radius: 999px;
    background: transparent; color: var(--sc-muted); cursor: pointer;
  }
  .help-btn svg { width: 22px; height: 22px; }
  .help-btn:hover, .sc-help[open] > .help-btn, .help-btn[aria-expanded="true"] { background: var(--sc-fill); color: var(--sc-ink); }
  .help-btn:focus-visible, .help-close:focus-visible { outline: 2px solid var(--sc-brand); outline-offset: 2px; }
  .sc-help[open]:not([data-modal]) { flex: 1 1 100%; }
  .help-card {
    margin: 4px 0 12px; padding: 18px 18px 16px;
    border: 1px solid var(--sc-line); border-radius: var(--sc-radius);
    background: var(--sc-card); color: var(--sc-ink); font-size: 0.9375rem; line-height: 1.5; text-align: left;
  }
  [dir="rtl"] .help-card { text-align: right; }
  .help-head { display: flex; align-items: center; }
  .help-title { flex: 1 1 auto; margin: 0; font-size: 1.125rem; font-weight: 700; line-height: 1.3; }
  .help-close {
    display: inline-flex; flex: 0 0 auto; align-items: center; justify-content: center;
    width: 40px; height: 40px; margin: -6px -8px -6px 8px; padding: 0;
    border: 0; border-radius: 999px; background: transparent; color: var(--sc-muted); cursor: pointer;
  }
  [dir="rtl"] .help-close { margin: -6px 8px -6px -8px; }
  .help-close svg { width: 20px; height: 20px; }
  .help-close:hover { background: var(--sc-fill); color: var(--sc-ink); }
  .help-sheet:not(.is-modal) .help-close { display: none; }
  .help-tips { list-style: none; margin: 12px 0 0; padding: 0; }
  .help-tips li { padding: 10px 0; border-top: 1px solid var(--sc-line); }
  .help-tips li:first-child { border-top: 0; padding-top: 2px; }
  .help-tips li[hidden] { display: none; }
  .help-tips strong { display: block; font-size: 0.875rem; font-weight: 650; }
  .help-tips span { display: block; margin-top: 2px; color: var(--sc-muted); font-size: 0.875rem; }
  .help-fail { margin: 12px 0 0; padding: 12px 14px; border-radius: var(--sc-r-ctl); background: var(--sc-recess); font-size: 0.875rem; }
  .help-device { margin: 12px 0 0; color: var(--sc-muted); font-size: 0.875rem; }
  .help-device strong { display: block; color: var(--sc-ink); }
  .help-device dl { display: grid; grid-template-columns: auto 1fr; grid-gap: 4px 16px; gap: 4px 16px; margin: 8px 0 0; }
  .help-device dt, .help-device dd { margin: 0; }
  .help-device dd {
    color: var(--sc-ink); font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 0.8125rem; overflow-wrap: anywhere;
  }
  .help-device p { margin: 8px 0 0; }
  .help-hotel { margin: 12px 0 0; color: var(--sc-muted); font-size: 0.875rem; white-space: pre-line; }
  .help-hotel[hidden] { display: none; }
  .help-sheet.is-modal {
    position: fixed; top: 0; right: 0; bottom: 0; left: 0; z-index: 50;
    display: flex; align-items: flex-end; justify-content: center;
    padding: 12px; background: rgba(12, 16, 20, 0.5);
  }
  .help-sheet.is-modal .help-card {
    width: 100%; max-width: 460px; max-height: 86vh; overflow: auto; margin: 0;
    box-shadow: var(--sc-shadow-float);
    -webkit-animation: sc-help-in 0.18s ease-out; animation: sc-help-in 0.18s ease-out;
  }
  @media (min-width: 560px) { .help-sheet.is-modal { align-items: center; } }
  @-webkit-keyframes sc-help-in { from { opacity: 0; -webkit-transform: translateY(12px); } to { opacity: 1; -webkit-transform: none; } }
  @keyframes sc-help-in { from { opacity: 0; transform: translateY(12px); } to { opacity: 1; transform: none; } }
  @media (prefers-reduced-motion: reduce) { .help-sheet.is-modal .help-card { -webkit-animation: none; animation: none; } }
  .help-sheet.is-modal[hidden] { display: none; }
  .sc-help-open body { overflow: hidden; }

  /* ---- the product's one line: "Wi-Fi by OneGate" -------------------------------------------------------
     Small and last, so the hotel's name stays the page's name. The wordmark is type, not an image: "One" in
     the green gradient, "Gate" in solid black, on a light chip so it reads the same on any hotel surface. */
  .sc-by {
    flex: 1 1 100%; margin: 8px 0 2px; text-align: center;
    color: var(--sc-muted); font-size: 0.75rem; line-height: 1.6;
  }
  .og-mark {
    display: inline-block; padding: 0 7px; border-radius: 999px;
    background: #ffffff; box-shadow: 0 0 0 1px rgba(10, 10, 10, 0.08);
    font-size: 0.8125rem; letter-spacing: 0; white-space: nowrap; unicode-bidi: isolate;
  }
  .og-one, .og-gate { font-weight: 700; }
  .og-one { color: #149c4a; }
  .og-gate { color: #0a0a0a; }
  @supports ((-webkit-background-clip: text) or (background-clip: text)) {
    .og-one {
      background-image: linear-gradient(95deg, #0b7a3b, #149c4a 38%, #3cc05a 70%, #8fdc5f);
      -webkit-background-clip: text; background-clip: text;
      -webkit-text-fill-color: transparent; color: transparent;
    }
  }

  .empty-state { padding: 8px 0 4px; text-align: center; }
  .empty-state svg { width: 40px; height: 40px; margin: 0 auto 10px; color: var(--sc-muted); display: block; }
  .empty-state p { color: var(--sc-ink); font-size: 0.9375rem; }

  /* ---- the pages after sign-in ------------------------------------------------------------------------- */
  .status-icon {
    display: flex; align-items: center; justify-content: center;
    width: 64px; height: 64px; margin: 0 auto 16px; border-radius: 50%;
    background: #e6f4ee; color: #157a52;
  }
  .status-icon svg { width: 32px; height: 32px; }
  .status-icon--err { background: #fdeceb; color: #b42318; }
  .page-title { font-size: 1.625rem; font-weight: 700; line-height: 1.3; letter-spacing: -0.02em; text-align: center; }
  .page-title--start { text-align: left; }
  [dir="rtl"] .page-title--start { text-align: right; }
  .page-lead { margin: 6px auto 0; max-width: 38ch; color: var(--sc-muted); font-size: 0.9375rem; text-align: center; }
  .page-lead--start { margin: 6px 0 20px; max-width: none; text-align: left; }
  [dir="rtl"] .page-lead--start { text-align: right; }
  .fact {
    display: flex; align-items: center; justify-content: space-between;
    margin: 20px 0 0; padding: 14px 16px;
    border: 1px solid var(--sc-line); border-radius: var(--sc-r-ctl); background: var(--sc-recess);
  }
  .fact-label { color: var(--sc-muted); font-size: 0.875rem; }
  .fact-value { font-size: 1.25rem; font-weight: 700; font-variant-numeric: tabular-nums; }
  .actions { display: flex; margin: 16px -5px 0; }
  .actions > * { flex: 1 1 0; margin: 0 5px; }
  .actions form { display: flex; }
  .section { margin-top: 24px; padding-top: 18px; border-top: 1px solid var(--sc-line); }
  .section h2 { font-size: 1.0625rem; font-weight: 700; }
  .section .lead { margin: 4px 0 8px; color: var(--sc-muted); font-size: 0.875rem; }
  .tl-main { font-size: 1rem; font-weight: 700; }
  .tl-note { margin-top: 4px; color: var(--sc-muted); font-size: 0.875rem; }
  .dev { display: flex; flex-wrap: wrap; align-items: center; padding: 12px 0; border-top: 1px solid var(--sc-line); }
  .dev:first-child { border-top: 0; }
  .dev-icon {
    display: flex; align-items: center; justify-content: center; flex: 0 0 auto;
    width: 40px; height: 40px; margin: 0 12px 0 0; border-radius: var(--sc-r-ctl);
    background: var(--sc-fill); color: var(--sc-muted);
  }
  [dir="rtl"] .dev-icon { margin: 0 0 0 12px; }
  .dev-icon svg { width: 20px; height: 20px; }
  .dev-text { flex: 1 1 140px; min-width: 0; }
  .dev .name { font-weight: 650; }
  .dev .meta { margin-top: 2px; color: var(--sc-muted); font-size: 0.8125rem; }
  .dev .inuse { flex: 1 1 100%; margin: 8px 0 0 52px; color: var(--sc-muted); font-size: 0.8125rem; }
  .dev > .btn { margin: 10px 0 0 52px; }
  .dev .confirm-row { margin-left: 52px; }
  [dir="rtl"] .dev .inuse { margin: 8px 52px 0 0; }
  [dir="rtl"] .dev > .btn { margin: 10px 52px 0 0; }
  [dir="rtl"] .dev .confirm-row { margin-left: 0; margin-right: 52px; }
  .dev-text { flex-basis: calc(100% - 52px); }
  .btn[hidden] { display: none; }
  .badge {
    display: inline-flex; align-items: center; padding: 1px 8px; margin: 0 6px 0 0;
    border-radius: 999px; font-size: 0.75rem; font-weight: 700;
    background: var(--sc-fill); color: var(--sc-muted);
  }
  [dir="rtl"] .badge { margin: 0 0 0 6px; }
  .badge::before { content: ""; width: 6px; height: 6px; margin: 0 6px 0 0; border-radius: 50%; background: currentColor; }
  [dir="rtl"] .badge::before { margin: 0 0 0 6px; }
  .badge--ok { background: #e6f4ee; color: #126a47; }
  .confirm-row {
    flex: 1 1 100%; margin: 10px 0 0; padding: 12px;
    border: 1px solid #f4c0bb; border-radius: var(--sc-r-ctl); background: #fdeceb; color: #8f1c13;
    font-size: 0.875rem;
  }
  .confirm-row .row { display: flex; margin: 10px -4px 0; }
  .confirm-row .row > * { flex: 1 1 0; margin: 0 4px; }
  #dv-note:empty { display: none; }
  #dv-note { margin-top: 10px; padding: 10px 12px; border-radius: var(--sc-r-ctl); font-size: 0.875rem; }
  .dv-done { border: 1px solid #b8dfcb; background: #e6f4ee; color: #126a47; }
  .dv-err { border: 1px solid #f4c0bb; background: #fdeceb; color: #8f1c13; }
  .pkg { margin: 10px 0; padding: 12px 14px; border: 1px solid var(--sc-line); border-radius: var(--sc-r-ctl); }
  .pkg h3 { margin: 0 0 4px; font-size: 1rem; }
  .pkg .meta, #cx-note { color: var(--sc-muted); font-size: 0.8125rem; line-height: 1.5; }
  .pkg button, #cx-confirm { margin-top: 8px; }
  .cx-err { color: #8f1c13; }

  /* ---- focus: a 2px ink ring with a white halo, visible on a card and on a photograph alike ------------- */
  a:focus, button:focus, select:focus, input:focus, summary:focus, [tabindex]:focus {
    outline: 2px solid var(--sc-ink); outline-offset: 2px; box-shadow: 0 0 0 2px #fff;
  }
  a:focus:not(:focus-visible), button:focus:not(:focus-visible), select:focus:not(:focus-visible),
  [tabindex]:focus:not(:focus-visible) { outline: 0; box-shadow: none; }
  .tab[aria-selected="true"]:focus:not(:focus-visible) {
    box-shadow: 0 1px 2px rgba(18, 22, 28, 0.10), 0 1px 1px rgba(18, 22, 28, 0.04);
  }
  input[type=text]:focus, input[type=password]:focus, input[type=email]:focus, input[type=tel]:focus {
    outline-offset: 1px; border-color: var(--sc-ink);
  }

  /* PHONE. The same card and hierarchy, given nearly the whole width. */
  @media (max-width: 680px) {
    .lang select { font-size: 0.8125rem; }
    .brand img { max-height: 48px; }
  }
  @media (prefers-reduced-motion: no-preference) {
    .tab, button.primary, .btn, button.choice, .social-btn, .pill input, input[type=text], input[type=password],
    input[type=email], input[type=tel] {
      transition: background-color 160ms cubic-bezier(0.2, 0, 0, 1), border-color 160ms cubic-bezier(0.2, 0, 0, 1),
        box-shadow 160ms cubic-bezier(0.2, 0, 0, 1), color 160ms cubic-bezier(0.2, 0, 0, 1),
        background-position 160ms cubic-bezier(0.2, 0, 0, 1);
    }
    .panel.active, .card { animation: sc-in 200ms cubic-bezier(0.2, 0, 0, 1); }
    @keyframes sc-in { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: none; } }
  }
`

// portalTemplateCSS: the six layouts, as rules over the same elements. Classic is the base sheet above.
// Mobile first; each layout opens out at 900px.
const portalTemplateCSS = `
  .sc-hero { display: none; }
  .sc-hero-logo { display: block; max-height: 56px; max-width: 220px; margin: 0 0 16px; object-fit: contain; }
  .sc-hero-logo[hidden] { display: none; }
  .sc-hero-name {
    font-family: var(--sc-heading-font, inherit);
    font-weight: 700; line-height: 1.08; letter-spacing: -0.02em; overflow-wrap: anywhere;
  }
  .sc-hero-welcome { margin: 12px 0 0; max-width: 40ch; line-height: 1.45; }
  .sc-hero-welcome:empty { display: none; }
  [data-template="split"] .sc-brandblock, [data-template="immersive"] .sc-brandblock,
  [data-template="editorial"] .sc-brandblock, [data-template="headerbar"] .sc-brandblock .brand {
    position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; border: 0;
    overflow: hidden; clip: rect(0 0 0 0); clip-path: inset(50%); white-space: nowrap;
  }

  /* A photographic panel: the hero photograph under the hotel's darkening, over the brand colours when there
     is no photograph at all -- so nothing uploaded still reads as a deliberate panel. */
  [data-template="split"] .sc-hero, [data-template="immersive"] .sc-hero, [data-template="editorial"] .sc-hero {
    color: #fff;
    background-color: var(--sc-brand-dark);
    background-image:
      linear-gradient(rgba(0, 0, 0, var(--sc-overlay)), rgba(0, 0, 0, var(--sc-overlay))),
      var(--sc-hero),
      linear-gradient(150deg, var(--sc-brand), var(--sc-brand-dark));
    background-size: cover;
    background-position: center;
    background-repeat: no-repeat;
  }
  [data-template="split"] .sc-hero-name, [data-template="immersive"] .sc-hero-name,
  [data-template="editorial"] .sc-hero-name { text-shadow: 0 1px 18px rgba(0, 0, 0, 0.22); }
  /* A logo over a photograph sits on a light plate, so a dark logo stays legible on any picture. */
  [data-template="split"] .sc-hero-logo, [data-template="immersive"] .sc-hero-logo,
  [data-template="editorial"] .sc-hero-logo {
    max-height: 60px; padding: 6px 10px; border-radius: calc(var(--sc-radius) * 0.6);
    background: rgba(255, 255, 255, 0.92); box-shadow: 0 2px 10px rgba(0, 0, 0, 0.12);
  }

  /* ---- 2. SPLIT: photograph one side, sign-in the other; a banner over a sheet on a phone -------------- */
  [data-template="split"] body { background: var(--sc-card); }
  [data-template="split"] .page { padding: 0; align-items: stretch; }
  [data-template="split"] .sc-hero {
    display: flex; align-items: flex-end;
    min-height: 260px; min-height: 36vh; padding: 76px 20px 44px;
  }
  [data-template="split"] .sc-hero-name { font-size: 2rem; }
  [data-template="split"] .sc-hero-welcome { font-size: 1rem; opacity: 0.95; }
  [data-template="split"] .card {
    flex-direction: column; max-width: none; margin: -22px 0 0;
    border-radius: var(--sc-radius) var(--sc-radius) 0 0; box-shadow: none;
    padding: var(--sc-pad) 20px 12px;
  }

  /* ---- 3. IMMERSIVE: full-screen photograph, frosted panel, large type ---------------------------------- */
  [data-template="immersive"] body { background: #0d1416; }
  [data-template="immersive"] .sc-hero {
    display: block; position: fixed; top: 0; right: 0; bottom: 0; left: 0; z-index: 0;
    background-image:
      linear-gradient(rgba(0, 0, 0, var(--sc-overlay)), rgba(0, 0, 0, var(--sc-overlay))),
      var(--sc-hero),
      linear-gradient(150deg, var(--sc-brand-dark), #0d1416);
  }
  [data-template="immersive"] .sc-hero-inner { position: absolute; top: 76px; left: 20px; right: 20px; }
  [data-template="immersive"] .sc-hero-name { font-size: 2.25rem; }
  [data-template="immersive"] .sc-hero-welcome { font-size: 1.0625rem; opacity: 0.95; }
  [data-template="immersive"] .page {
    z-index: 1; justify-content: flex-end;
    padding: 300px 12px 16px; padding-top: max(300px, 42vh);
  }
  [data-template="immersive"] .card {
    max-width: 440px; margin: 0;
    background: rgba(255, 255, 255, 0.82);
    -webkit-backdrop-filter: blur(18px) saturate(1.4); backdrop-filter: blur(18px) saturate(1.4);
    border: 1px solid rgba(255, 255, 255, 0.6);
    box-shadow: 0 24px 64px rgba(0, 0, 0, 0.32);
  }
  @supports not ((-webkit-backdrop-filter: blur(1px)) or (backdrop-filter: blur(1px))) {
    [data-template="immersive"] .card { background: rgba(255, 255, 255, 0.96); }
  }
  [data-template="immersive"][data-surface="solid"] .card {
    background: var(--sc-card); -webkit-backdrop-filter: none; backdrop-filter: none; border-color: transparent;
  }
  [data-template="immersive"] .tabs { background: rgba(20, 22, 26, 0.08); }
  [data-template="immersive"] .sc-foot, [data-template="immersive"] .alt { border-color: rgba(20, 22, 26, 0.14); }

  /* ---- 4. HEADER BAR: business -- a brand bar, a wide card, a help column ------------------------------- */
  [data-template="headerbar"] body { background: var(--sc-canvas); }
  [data-template="headerbar"] .sc-hero {
    display: flex; align-items: center;
    position: absolute; top: 0; left: 0; right: 0; z-index: 4;
    height: 64px; padding: 0 172px 0 16px;
    background: var(--sc-brand); color: var(--sc-on-brand);
    box-shadow: 0 1px 0 rgba(0, 0, 0, 0.08), 0 6px 18px rgba(0, 0, 0, 0.08);
  }
  [dir="rtl"][data-template="headerbar"] .sc-hero { padding: 0 16px 0 172px; }
  [data-template="headerbar"] .sc-hero-inner { display: flex; align-items: center; min-width: 0; }
  [data-template="headerbar"] .sc-hero-logo {
    max-height: 40px; max-width: 128px; margin: 0 12px 0 0; padding: 4px 8px;
    border-radius: 6px; background: #fff;
  }
  [dir="rtl"][data-template="headerbar"] .sc-hero-logo { margin: 0 0 0 12px; }
  [data-template="headerbar"] .sc-hero-name {
    font-size: 1.0625rem; font-weight: 650; letter-spacing: 0;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  [data-template="headerbar"] .sc-hero-welcome { display: none; }
  [data-template="headerbar"] .langbar { top: 10px; }
  [data-template="headerbar"] .page { padding: 88px 12px 24px; }
  [data-template="headerbar"] .card {
    max-width: 1040px; margin-top: 0;
    grid-template-columns: minmax(0, 1fr); grid-template-areas: "brand" "body" "side" "foot";
    border: 1px solid var(--sc-line); box-shadow: var(--sc-shadow-card);
  }
  [data-template="headerbar"] .sc-brandblock { grid-area: brand; text-align: left; margin-bottom: 20px; }
  [dir="rtl"][data-template="headerbar"] .sc-brandblock { text-align: right; }
  [data-template="headerbar"] .welcome { margin: 0; max-width: none; color: var(--sc-ink); font-size: 1.25rem; font-weight: 700; }
  [data-template="headerbar"] .sc-body { grid-area: body; min-width: 0; }
  [data-template="headerbar"] .sc-extras { grid-area: side; }
  [data-template="headerbar"] .sc-foot { grid-area: foot; }

  /* ---- 5. RESORT: a tall banner, the card overlapping it, the hotel's content below --------------------- */
  [data-template="editorial"] body { background: #f5f2ed; }
  [data-template="editorial"] .page { padding: 0 0 40px; }
  [data-template="editorial"] .sc-hero {
    display: flex; align-items: center; justify-content: center; align-self: stretch; text-align: center;
    min-height: 42vh; padding: 84px 20px 110px;
  }
  [data-template="editorial"][data-hero="short"] .sc-hero { min-height: 32vh; }
  [data-template="editorial"][data-hero="tall"] .sc-hero { min-height: 58vh; }
  [data-template="editorial"] .sc-hero-inner { display: flex; flex-direction: column; align-items: center; max-width: 48rem; text-align: center; }
  [data-template="editorial"] .sc-hero-name {
    font-family: var(--sc-heading-font, Georgia, "Times New Roman", serif);
    font-size: 2.5rem; font-weight: 600; letter-spacing: -0.01em;
  }
  [data-template="editorial"] .sc-hero-welcome { margin-left: auto; margin-right: auto; font-size: 1.0625rem; opacity: 0.95; }
  [data-template="editorial"] .card {
    width: calc(100% - 24px); max-width: 540px; margin: -76px auto 0;
    background: transparent; box-shadow: none; padding: 0; border-radius: 0;
  }
  [data-template="editorial"] .sc-body {
    background: var(--sc-card); border-radius: var(--sc-radius); box-shadow: var(--sc-shadow-float);
    padding: var(--sc-pad) 20px 22px;
  }
  [data-template="editorial"] .help { text-align: center; }
  [data-template="editorial"] #custom-html > * {
    margin: 0 0 12px; padding: 16px 18px;
    border-radius: calc(var(--sc-radius) * 0.75); background: var(--sc-card); box-shadow: var(--sc-shadow-card);
  }
  [data-template="editorial"] .sc-foot { justify-content: center; border-top: 0; }
  [data-template="editorial"] .info-btn { margin: 0 4px; }
  [data-template="editorial"] .info-panel { background: var(--sc-card); }

  /* ---- 6. KIOSK: no imagery, large controls for a lobby tablet ------------------------------------------ */
  [data-template="kiosk"] { font-size: 112.5%; --sc-ctl: 60px; --sc-gap: 20px; }
  [data-template="kiosk"][data-density="compact"] { --sc-ctl: 52px; --sc-gap: 16px; }
  [data-template="kiosk"][data-density="spacious"] { --sc-ctl: 68px; --sc-gap: 26px; }
  [data-template="kiosk"] body { background: var(--sc-canvas); border-top: 6px solid var(--sc-brand); }
  [data-template="kiosk"] .card { max-width: 640px; box-shadow: var(--sc-shadow-card); }
  [data-template="kiosk"] .brand img { max-height: 76px; }
  [data-template="kiosk"] .brand-mark { width: 60px; height: 60px; }
  [data-template="kiosk"] .brand .name { font-size: 1.75rem; }
  [data-template="kiosk"] .tab { flex-direction: column; min-height: 72px; font-size: 1.0625rem; }
  [data-template="kiosk"] .tab svg, [dir="rtl"][data-template="kiosk"] .tab svg { width: 24px; height: 24px; margin: 0 0 4px; }
  [data-template="kiosk"] .lang select { height: 52px; font-size: 1rem; }
  [data-template="kiosk"] .info-btn { width: 52px; height: 52px; }
  [data-template="kiosk"] .pill { min-height: 60px; font-size: 1rem; }

  /* A phone's header bar has room for the logo or the name beside the language pill, not both. */
  @media (max-width: 680px) {
    [data-template="headerbar"] .sc-hero { padding: 0 148px 0 12px; }
    [dir="rtl"][data-template="headerbar"] .sc-hero { padding: 0 12px 0 148px; }
    [data-template="headerbar"] .sc-hero-logo:not([hidden]) + .sc-hero-name { display: none; }
  }

  /* ---- tablet and desktop ---------------------------------------------------------------------------- */
  @media (min-width: 900px) {
    [data-template="split"] .page { flex-direction: row; min-height: 100vh; }
    [data-template="split"] .sc-hero {
      flex: 1.15 1 0; position: -webkit-sticky; position: sticky; top: 0;
      height: 100vh; min-height: 0; padding: 56px;
    }
    [data-template="split"] .sc-hero-name { font-size: 3rem; }
    [data-template="split"] .sc-hero-welcome { font-size: 1.1875rem; }
    [data-template="split"] .card {
      flex: 1 1 0; justify-content: center; margin: 0; min-height: 100vh; border-radius: 0;
      padding: 96px 56px 24px;
    }
    [data-template="split"][data-panel="start"] .sc-hero { -webkit-box-ordinal-group: 3; order: 2; }
    [data-template="split"] .sc-brandblock, [data-template="split"] .sc-body,
    [data-template="split"] .sc-extras, [data-template="split"] .sc-foot {
      width: 100%; max-width: 420px; margin-left: auto; margin-right: auto;
    }

    [data-template="immersive"] .page {
      flex-direction: row; align-items: center; justify-content: flex-end; padding: 96px 6vw 48px;
    }
    [data-template="immersive"][data-panel="start"] .page { justify-content: flex-start; }
    [data-template="immersive"] .card { margin: auto 0; }
    [data-template="immersive"] .sc-hero-inner { top: auto; right: auto; left: 6vw; bottom: 11vh; max-width: 40vw; }
    [dir="rtl"][data-template="immersive"] .sc-hero-inner { left: auto; right: 6vw; }
    [data-template="immersive"][data-panel="start"] .sc-hero-inner { left: auto; right: 6vw; text-align: right; }
    [dir="rtl"][data-template="immersive"][data-panel="start"] .sc-hero-inner { right: auto; left: 6vw; text-align: left; }
    [data-template="immersive"][data-panel="start"] .sc-hero-welcome { margin-left: auto; }
    [dir="rtl"][data-template="immersive"][data-panel="start"] .sc-hero-welcome { margin-left: 0; margin-right: auto; }
    [data-template="immersive"] .sc-hero-name { font-size: 4rem; font-size: clamp(2.75rem, 5.4vw, 5rem); line-height: 1; }
    [data-template="immersive"] .sc-hero-welcome { font-size: 1.25rem; max-width: 32ch; }
    [data-template="immersive"][data-panel="center"] .page {
      flex-direction: column; justify-content: flex-start; padding-top: max(260px, 34vh);
    }
    [data-template="immersive"][data-panel="center"] .sc-hero-inner {
      top: 12vh; bottom: auto; left: 0; right: 0; max-width: 44rem; margin: 0 auto; text-align: center;
    }
    [data-template="immersive"][data-panel="center"] .sc-hero-welcome { margin-left: auto; margin-right: auto; }
    [data-template="immersive"][data-panel="center"] .sc-hero-name { font-size: 3.25rem; }

    [data-template="headerbar"] .sc-hero { padding: 0 200px 0 32px; }
    [dir="rtl"][data-template="headerbar"] .sc-hero { padding: 0 32px 0 200px; }
    [data-template="headerbar"] .page { padding: 104px 32px 40px; }
    [data-template="headerbar"] .card {
      grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: "brand brand" "body side" "foot foot";
      padding: 36px 40px 16px;
    }
    [data-template="headerbar"] .sc-body { max-width: 560px; }
    [data-template="headerbar"] .sc-extras {
      width: 320px; margin: 0 0 0 48px; padding: 0 0 0 32px; border-left: 1px solid var(--sc-line); align-self: start;
    }
    [dir="rtl"][data-template="headerbar"] .sc-extras { margin: 0 48px 0 0; padding: 0 32px 0 0; border-left: 0; border-right: 1px solid var(--sc-line); }

    [data-template="editorial"] .sc-hero { min-height: 50vh; padding: 96px 40px 150px; }
    [data-template="editorial"][data-hero="short"] .sc-hero { min-height: 38vh; }
    [data-template="editorial"][data-hero="tall"] .sc-hero { min-height: 68vh; }
    [data-template="editorial"] .sc-hero-name { font-size: 4rem; font-size: clamp(3rem, 5.2vw, 4.6rem); }
    [data-template="editorial"] .sc-hero-welcome { font-size: 1.25rem; }
    [data-template="editorial"] .card { max-width: 560px; margin-top: -110px; }
    [data-template="editorial"] .sc-body { padding: 40px 44px 32px; }
    [data-template="editorial"] #custom-html { display: -ms-grid; display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); grid-gap: 14px; gap: 14px; }
    [data-template="editorial"] #custom-html:empty { display: none; }
    [data-template="editorial"] #custom-html > * { margin: 0; }
  }
`

// portalGuardCSS -- why the hotel's stylesheet can restyle every control and hide none of them.
//
// The cascade has three tiers on the sign-in page, weakest first:
//  1. @layer sc-base, sc-template   the portal's own look, and the template's
//  2. @layer hotel                  the hotel's custom CSS: beats (1) whatever the specificity
//  3. THIS SHEET, UNLAYERED         a normal declaration outside every layer beats every normal
//     declaration inside one, whatever the specificity
//
// and above all of them the inline style="" the sign-in script sets to show and hide its own forms, which is
// why nothing here uses !important: an important declaration here would beat the script too, and the
// voucher/personal-account switch would stop working. The one way a layered rule CAN beat an unlayered one is
// !important, so the portal removes !important from the hotel's stylesheet before serving it (see
// internal/portaldesign).
//
// What is guarded is what a guest needs to sign in: the path from the page down to the fields, the fields,
// their labels and buttons, the group tabs, and the messages the server or the sign-in script shows. Values
// here match the portal's own, so on an unstyled page this sheet changes nothing.
const portalGuardCSS = `
html, body { display: block; visibility: visible; opacity: 1; }
.page { display: flex; }
main.card { display: block; }
[data-template="split"] main.card { display: flex; }
[data-template="headerbar"] main.card { display: grid; }
.sc-signin, .panels, .panel.active, .panels form, .panels .field { display: block; }
.panel:not(.active) { display: none; }
#tabs { display: grid; }
#tabs .tab, .pill { display: inline-flex; }
.page, main.card, .sc-signin, .panels, .panel.active, .panels form, .panels .field, .panels label,
.panels input:not([type=hidden]), .panels button, #tabs, #tabs .tab, .pill, .langbar, #lang,
#server-error, .notice.show, .panels .err {
  visibility: visible; pointer-events: auto; filter: none; clip-path: none; content-visibility: visible;
  transform: none; translate: none; scale: none; rotate: none;
}
.page, main.card, .sc-signin, .panels, .panel.active, .panels form, .panels .field, .panels label,
.panels input:not([type=hidden]), #tabs, #tabs .tab, #server-error, .notice.show, .panels .err,
.panels button:not(:disabled) { opacity: 1; }
.sc-signin, .panels, .panels form, .panels .field, .panels input:not([type=hidden]), .panels button {
  height: auto; max-height: none; overflow: visible;
}
.page, main.card, .sc-signin, .panels, .panels form, .panels .field { position: relative; inset: auto; }
.panels input:not([type=hidden]):not([type=checkbox]), .panels button { position: static; }
#server-error, .notice.show, .panels .err { display: flex; position: static; }
/* Anything a stylesheet draws with ::before/::after can be looked at but never clicked, so a decorative
   overlay can never sit between a guest and the Login button. */
*::before, *::after { pointer-events: none; }
/* The hotel's own markup stays in its own box: paint containment clips it to the container, and makes the
   container the reference for position:fixed inside it -- so a fragment cannot lift itself over the forms. */
#custom-html { position: relative; inset: auto; z-index: 0; contain: layout paint; transform: none; translate: none; }
`

// portalPageGuardCSS is the same guarantee for the pages after sign-in -- package choice, "You're online", the
// status page and the failure pages -- which carry the hotel's custom CSS too, in the same `@layer hotel` under
// the same unlayered guard (guestHead in templates.go). What a guest needs on those pages is the path from the
// page to the words and the buttons: the heading and the message (a refusal is role="alert"), the facts, the
// actions (Disconnect, Back, Status), the package buttons, the device and package panels once they have
// something to show, and the language pill. Values match the portal's own, so an unstyled page is unchanged.
// Nothing here uses !important, for the same reason as above, and every rule that sets display respects the
// hidden attribute, which is how the pages' own scripts show and hide their panels and buttons.
const portalPageGuardCSS = `
html, body { display: block; visibility: visible; opacity: 1; }
.page { display: flex; }
main.card { display: block; }
[data-template="split"] main.card { display: flex; }
[data-template="headerbar"] main.card { display: grid; }
.sc-body, .page-title, .page-lead, .choice-list, .choice-list form, .section:not([hidden]) { display: block; }
.fact, .actions, .actions form, .actions .btn:not([hidden]), .choice-list button.choice { display: flex; }
.page, main.card, .sc-body, .page-title, .page-lead, [role=alert], .fact, .fact-label, .fact-value, .actions,
.actions form, .actions .btn, .choice-list, .choice-list form, .choice-list button.choice, .section:not([hidden]),
.section button, .section .confirm-row, .langbar, #lang {
  visibility: visible; pointer-events: auto; filter: none; clip-path: none; content-visibility: visible;
  transform: none; translate: none; scale: none; rotate: none;
}
.page, main.card, .sc-body, .page-title, .page-lead, [role=alert], .fact, .fact-label, .fact-value, .actions,
.actions form, .choice-list, .choice-list form, .section:not([hidden]), .section .confirm-row, .langbar, #lang,
.actions .btn:not(:disabled), .choice-list button.choice:not(:disabled), .section button:not(:disabled) { opacity: 1; }
.sc-body, .actions, .actions form, .actions .btn, .choice-list, .choice-list form, .choice-list button.choice,
.section:not([hidden]) { height: auto; max-height: none; overflow: visible; }
.page, main.card, .sc-body { position: relative; inset: auto; }
.actions, .actions form, .actions .btn, .choice-list, .choice-list form, .choice-list button.choice { position: static; }
*::before, *::after { pointer-events: none; }
`
