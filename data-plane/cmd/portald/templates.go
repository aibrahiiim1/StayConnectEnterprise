package main

const landingHTML = `<!doctype html>
<html lang="en" data-template="{{.Template}}" data-density="{{.Density}}" data-panel="{{.Panel}}" data-hero="{{.HeroHeight}}" data-surface="{{.Surface}}"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Wi-Fi Access</title>
<style id="sc-base">
  /* THE CASCADE ORDER, declared before anything else so nothing later can re-order it: the portal's own
     styling, then the chosen template, then the hotel's custom CSS. See the guard sheet below for why. */
  @layer sc-base, sc-template, hotel;
  @layer sc-base {
  /* THE GUEST PORTAL, to the Product Owner's reference design.
     One design across desktop, tablet and phone -- the same card, hierarchy and controls, laid out for the
     space available. Every colour and radius is a custom property so Branding restyles it without touching
     this file. */
  :root {
    --sc-brand:      #0f6b63;
    --sc-brand-dark: #0b544e;
    --sc-ink:        #14302e;
    --sc-muted:      #6b7b7a;
    --sc-card:       #ffffff;
    --sc-line:       #e3e8e8;
    --sc-radius:     20px;
    --sc-bg: none; /* no photo unless the hotel sets one; the old default named a file no appliance ships, so every guest page load requested it and got a 404 */
    font-family: "Inter", -apple-system, system-ui, "Segoe UI", Roboto, sans-serif;
  }
  * { box-sizing: border-box; }
  html { height: 100%; }
  body {
    margin: 0;
    min-height: 100%;
    color: var(--sc-ink);
    /* The photograph fills the viewport. A gradient sits under it so an appliance with no image uploaded
       still looks deliberate rather than broken. */
    background: var(--sc-bg) center/cover no-repeat fixed, linear-gradient(160deg, #cfe3e6, #eef3f2);
  }

  /* THE PAGE IS A COLUMN, NOT A ROW.
     This was a flex ROW on the body, with the language bar as a sibling of the card. On a phone the bar
     became a flex ITEM beside the card and align-items:stretch gave it the full viewport height -- a tall
     white column down the left with the form squeezed into what was left. A column layout cannot put
     anything beside the card. */
  .page {
    min-height: 100vh;
    min-height: 100dvh;
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: clamp(12px, 5vh, 56px) clamp(12px, 4vw, 48px) clamp(24px, 6vh, 56px);
  }

  .langbar { position: fixed; top: 14px; right: 16px; z-index: 5; }
  .lang {
    display: inline-flex; align-items: center; gap: 8px;
    background: #fff; border: 1px solid var(--sc-line); border-radius: 999px;
    padding: 9px 16px; font-size: .95rem; color: var(--sc-ink); cursor: pointer;
    box-shadow: 0 2px 12px rgb(0 0 0 / .10);
  }
  .lang select { border: 0; background: none; font: inherit; color: inherit; cursor: pointer; outline: none; }

  .card {
    position: relative;
    width: 100%;
    max-width: 960px;
    /* Centred vertically, so the photograph is present ABOVE and BELOW the card rather than only beneath it.
       AUTO MARGINS, not justify-content: a centred flex item taller than the viewport is clipped at the top
       with no way to scroll to it, and the card grows every time a guest opens the information panel or a
       site notice appears. Auto margins simply stop absorbing space once there is none. */
    margin-block: auto;
    background: var(--sc-card);
    border-radius: var(--sc-radius);
    box-shadow: 0 24px 64px rgb(0 0 0 / .18);
    padding: clamp(22px, 3vw, 48px) clamp(20px, 3.5vw, 56px) clamp(56px, 6vw, 72px);
  }

  /* A real logo when the hotel has uploaded one, its name when it has not. The row holds its height either
     way so the card does not jump as branding loads. */
  .brand { display: flex; align-items: center; gap: 16px; min-height: 60px; }
  .brand img { max-height: 60px; max-width: 280px; object-fit: contain; }
  .brand .name { font-size: clamp(1.05rem, 1.6vw, 1.35rem); font-weight: 600; letter-spacing: .01em; }
  .welcome { margin: 10px 0 0; color: var(--sc-muted); font-size: clamp(.95rem, 1.1vw, 1.05rem); max-width: 60ch; }
  .rule { border: 0; border-top: 1px solid var(--sc-line); margin: clamp(14px, 2vw, 22px) 0 0; }
  .help { margin: 22px 0 0; color: var(--sc-muted); font-size: .92rem; max-width: 60ch; }
  .terms { margin: 10px 0 0; font-size: .88rem; }
  .terms a { color: var(--sc-brand); }
  #custom-html:empty { display: none; }
  #custom-html { margin-top: 18px; }

  .tabs { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); }
  .tab {
    appearance: none; background: none; border: 0; border-bottom: 2px solid transparent;
    padding: clamp(14px, 1.8vw, 22px) 8px; font: inherit; font-size: clamp(1rem, 1.25vw, 1.2rem);
    color: var(--sc-muted); cursor: pointer;
    display: inline-flex; align-items: center; justify-content: center; gap: 10px;
  }
  .tab[aria-selected="true"] { color: var(--sc-brand); border-bottom-color: var(--sc-brand); font-weight: 600; }
  .tab svg { width: 1.15em; height: 1.15em; flex: 0 0 auto; }

  .panels { border-top: 1px solid var(--sc-line); padding-top: clamp(20px, 3vw, 34px); }
  .panel { display: none; }
  .panel.active { display: block; }

  /* One field width across every panel. The account panel used to opt out of it, so a guest who moved from
     Room Number to Voucher Code watched the input jump from half the card to all of it. */
  .field { margin-bottom: clamp(16px, 2vw, 22px); max-width: 560px; }
  label { display: block; font-size: clamp(.95rem, 1.05vw, 1.05rem); margin-bottom: 8px; }
  input[type=text], input[type=password], input[type=email], input[type=tel] {
    width: 100%; padding: 14px 16px; font-size: 1rem; color: var(--sc-ink);
    border: 1px solid var(--sc-line); border-radius: 12px; background: #fff;
  }
  input:focus-visible, .tab:focus-visible, button:focus-visible, select:focus-visible {
    outline: 2px solid var(--sc-brand); outline-offset: 2px;
  }
  .hint { color: var(--sc-muted); font-size: .92rem; margin: 8px 0 0; }
  button.primary {
    background: linear-gradient(180deg, var(--sc-brand), var(--sc-brand-dark));
    color: #fff; border: 0; border-radius: 12px; padding: 14px 36px;
    font: inherit; font-weight: 600; cursor: pointer;
  }
  button.primary:disabled { opacity: .55; cursor: wait; }

  .pill {
    display: inline-flex; align-items: center; gap: 10px;
    border: 1px solid var(--sc-line); border-radius: 999px; padding: 12px 22px;
    color: var(--sc-muted); cursor: pointer; user-select: none; margin-bottom: clamp(18px, 2.5vw, 26px);
  }
  .pill input { width: 18px; height: 18px; accent-color: var(--sc-brand); }
  .pill:has(input:checked) { color: var(--sc-brand); border-color: var(--sc-brand); }

  .info-btn {
    position: absolute; right: clamp(16px, 2vw, 28px); bottom: clamp(16px, 2vw, 28px);
    width: 30px; height: 30px; border-radius: 999px; border: 1px solid var(--sc-line);
    background: #f4f7f7; color: var(--sc-muted); cursor: pointer; font-weight: 700; line-height: 1;
  }
  .info-panel {
    margin-top: 22px; border: 1px solid var(--sc-line); border-radius: 12px;
    padding: 14px 16px; font-size: .92rem; color: var(--sc-muted); background: #f8fafa;
  }
  .info-panel dl { display: grid; grid-template-columns: auto 1fr; gap: 4px 16px; margin: 8px 0 0; }
  .info-panel dd { margin: 0; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--sc-ink); }

  .err { color: #b00020; margin-top: 12px; min-height: 1.2em; font-size: .92rem; }
  .notice { display:none; margin:0 0 16px; padding:12px 16px; border-radius:10px; font-size:.92rem;
            background:#fff8e1; border:1px solid #f0d38a; color:#6b4e00; }
  .notice.show { display:block; }
  /* A refusal is not a hint. It reads as an error rather than as the amber advisory the other notices use. */
  .notice--error { background:#fdecef; border-color:#f3b8c2; color:#8c0f2a; }
  .small { font-size:.85rem; color: var(--sc-muted); }
  .alt { margin-top: 22px; padding-top: 18px; border-top: 1px dashed var(--sc-line); }
  .alt h3 { font-size: .9rem; font-weight: 600; color: var(--sc-muted); margin: 0 0 12px; }
  #pms-choices button.choice { display:block; width:100%; text-align:left; margin:8px 0; padding:14px 16px;
    border:1px solid var(--sc-line); border-radius:12px; background:#fff; color:var(--sc-ink); cursor:pointer;
    font-size:1rem; }
  #pms-choices button.choice[disabled] { opacity:.5; cursor:default; }
  button.link { background:none; border:0; color:var(--sc-brand); font:inherit; padding:6px; cursor:pointer; }

  /* ARABIC READS RIGHT TO LEFT. A portal that renders it left-aligned is transliterated, not translated.
     dir follows the chosen language; only what would look wrong flips. */
  [dir="rtl"] .langbar { right: auto; left: 16px; }
  [dir="rtl"] .info-btn { right: auto; left: clamp(16px, 2vw, 28px); }

  /* PHONE. The SAME design, not a second one: the same card, the same hierarchy, the same controls, given
     nearly the whole width of the screen.
     What is deliberately NOT here: a full-bleed card. It was tried, and a card with min-height:100dvh holding
     four fields is a sheet of white with the bottom half empty and the information button pushed off the
     fold -- the "large blank decorative area" this design is not allowed to have. The card is as tall as its
     contents, a 12px gutter keeps the photograph visible around it, and the page ends where the card does. */
  @media (max-width: 680px) {
    body { background-attachment: scroll; }
    .page { padding: 0 12px 20px; }
    .langbar { position: static; width: 100%; display: flex; justify-content: flex-end; padding: 10px 0 8px; }
    .card {
      max-width: none;
      padding: 18px 16px 52px;
      box-shadow: 0 12px 32px rgb(0 0 0 / .16);
    }
    .brand { min-height: 48px; }
    .brand img { max-height: 48px; }
    .tabs { grid-template-columns: 1fr 1fr; }
    .tab { font-size: 1rem; padding: 14px 6px; gap: 8px; }
    .field { max-width: none; }
    button.primary { width: 100%; }
  }
  @media (min-width: 681px) and (max-width: 1024px) {
    .card { max-width: 680px; }
  }
  @media (prefers-reduced-motion: no-preference) {
    .panel.active { animation: fade .18s ease-out; }
    @keyframes fade { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: none; } }
  }
  }
</style>
<style id="sc-templates">
/* THE TEMPLATES -- six layouts over ONE page.
   Every template arranges the same elements: the same forms, the same ids, the same script. What changes is
   where the slots sit (.sc-hero, .sc-brandblock, .sc-signin, .sc-extras, .sc-foot) and how they look, so a
   guest signs in identically whichever layout the hotel chose. Classic is the original page and is not
   touched here at all: every rule below is scoped to a named template.
   The hero (.sc-hero) is decoration. It repeats the hotel's name and welcome in large type for the layouts
   that have a picture, and is aria-hidden: the sign-in card still carries the real, readable copies, which
   those layouts hide visually rather than remove. */
@layer sc-template {
  :root { --sc-overlay: .35; --sc-hero: var(--sc-bg); }
  .sc-hero { display: none; }
  .sc-hero-logo { display: block; max-height: 64px; max-width: 260px; object-fit: contain; margin: 0 0 18px; }
  .sc-hero-logo[hidden] { display: none; }
  .sc-hero-name { font-weight: 650; line-height: 1.08; letter-spacing: -.01em; }
  .sc-hero-welcome { margin: 14px 0 0; line-height: 1.45; max-width: 46ch; }
  .sc-hero-welcome:empty { display: none; }
  .brand .name, .sc-hero-name { font-family: var(--sc-heading-font, inherit); }
  .sc-visually-hidden, [data-template="split"] .sc-brandblock, [data-template="immersive"] .sc-brandblock,
  [data-template="editorial"] .sc-brandblock, [data-template="headerbar"] .sc-brandblock .brand {
    position: absolute; width: 1px; height: 1px; margin: -1px; overflow: hidden;
    clip-path: inset(50%); white-space: nowrap; border: 0; padding: 0;
  }

  /* DENSITY, for every template. */
  [data-density="compact"] .field { margin-bottom: 12px; }
  [data-density="compact"] input[type=text], [data-density="compact"] input[type=password],
  [data-density="compact"] input[type=email], [data-density="compact"] input[type=tel] { padding: 10px 12px; }
  [data-density="compact"] button.primary { padding: 11px 28px; }
  [data-density="spacious"] .field { margin-bottom: 30px; }
  [data-density="spacious"] input[type=text], [data-density="spacious"] input[type=password],
  [data-density="spacious"] input[type=email], [data-density="spacious"] input[type=tel] { padding: 18px 18px; font-size: 1.06rem; }
  [data-density="spacious"] button.primary { padding: 17px 44px; }

  /* A photographic panel: the hero image under a darkening overlay, over the brand colours when there is no
     image at all -- so an appliance with nothing uploaded still gets a deliberate panel, not a grey box. */
  [data-template="split"] .sc-hero, [data-template="immersive"] .sc-hero, [data-template="editorial"] .sc-hero {
    color: #fff;
    background:
      linear-gradient(rgb(0 0 0 / var(--sc-overlay)), rgb(0 0 0 / var(--sc-overlay))),
      var(--sc-hero) center/cover no-repeat,
      linear-gradient(150deg, var(--sc-brand), var(--sc-brand-dark));
  }

  /* ---- 2. SPLIT ------------------------------------------------------------------------------------- */
  [data-template="split"] body { background: var(--sc-card); }
  [data-template="split"] .page {
    grid-template-columns: minmax(0, 1.15fr) minmax(360px, 1fr);
    align-items: stretch; padding: 0; min-height: 100vh; min-height: 100dvh;
  }
  [data-template="split"] .sc-hero {
    display: flex; align-items: flex-end; grid-column: 1; grid-row: 1;
    position: sticky; top: 0; height: 100vh; height: 100dvh;
    padding: clamp(28px, 5vw, 72px);
  }
  [data-template="split"] .sc-hero-name { font-size: clamp(2rem, 3.6vw, 3.4rem); }
  [data-template="split"] .sc-hero-welcome { font-size: clamp(1rem, 1.3vw, 1.2rem); opacity: .94; }
  [data-template="split"] .card {
    grid-column: 2; grid-row: 1; flex-direction: column; justify-content: center;
    margin: 0; max-width: none; border-radius: 0; box-shadow: none;
    min-height: 100vh; min-height: 100dvh;
    padding: clamp(72px, 9vh, 96px) clamp(24px, 4.5vw, 64px) clamp(64px, 8vh, 88px);
  }
  [data-template="split"][data-panel="start"] .sc-hero { grid-column: 2; }
  [data-template="split"][data-panel="start"] .card { grid-column: 1; }
  [data-template="split"] .sc-signin, [data-template="split"] .sc-extras, [data-template="split"] .sc-foot {
    width: 100%; max-width: 560px; margin-inline: auto;
  }
  [data-template="split"] .tabs { grid-template-columns: 1fr 1fr; }

  /* ---- 3. IMMERSIVE --------------------------------------------------------------------------------- */
  [data-template="immersive"] body { background: #0d1416; }
  [data-template="immersive"] .sc-hero {
    display: block; position: fixed; inset: 0; z-index: 0;
    background:
      linear-gradient(180deg, rgb(0 0 0 / calc(var(--sc-overlay) * .5)) 0%, rgb(0 0 0 / var(--sc-overlay)) 70%),
      var(--sc-hero) center/cover no-repeat,
      linear-gradient(150deg, var(--sc-brand-dark), #0d1416);
  }
  /* The display type and the glass panel share the screen, never the same corner: the headline sits at the
     bottom of the side the panel is NOT on (panel at the end by default, so the words read first), and a
     centred panel moves the headline to the top. */
  [data-template="immersive"] .sc-hero-inner {
    position: absolute; inset-inline-start: clamp(24px, 6vw, 96px); bottom: clamp(40px, 10vh, 120px);
    max-width: min(18ch, 44vw);
  }
  [data-template="immersive"][data-panel="start"] .sc-hero-inner {
    inset-inline-start: auto; inset-inline-end: clamp(24px, 6vw, 96px); text-align: end;
  }
  [data-template="immersive"][data-panel="start"] .sc-hero-welcome { margin-inline-start: auto; }
  [data-template="immersive"][data-panel="center"] .sc-hero-inner {
    bottom: auto; top: clamp(64px, 11vh, 120px); inset-inline: 0; margin-inline: auto; max-width: min(24ch, 90vw);
    text-align: center;
  }
  [data-template="immersive"][data-panel="center"] .sc-hero-welcome { margin-inline: auto; }
  [data-template="immersive"][data-panel="center"] .sc-hero-inner { max-width: min(46rem, 90vw); }
  [data-template="immersive"][data-panel="center"] .sc-hero-name { font-size: clamp(2.2rem, 4.4vw, 3.8rem); }
  [data-template="immersive"] .sc-hero-name {
    font-size: clamp(2.6rem, 6.4vw, 5.6rem); font-weight: 700; letter-spacing: -.025em; line-height: .98;
    text-shadow: 0 2px 24px rgb(0 0 0 / .25);
  }
  [data-template="immersive"] .sc-hero-welcome { font-size: clamp(1.05rem, 1.6vw, 1.4rem); max-width: 34ch; opacity: .93; }
  [data-template="immersive"] .page {
    position: relative; z-index: 1; align-items: flex-end; justify-content: flex-end;
    padding: clamp(72px, 9vh, 96px) clamp(16px, 6vw, 96px) clamp(16px, 5vh, 56px);
  }
  [data-template="immersive"][data-panel="start"] .page { align-items: flex-start; }
  [data-template="immersive"][data-panel="center"] .page { align-items: center; padding-top: clamp(220px, 34vh, 360px); }
  [data-template="immersive"] .card {
    margin-block: auto 0; max-width: 540px;
    background: rgb(255 255 255 / .74);
    -webkit-backdrop-filter: blur(20px) saturate(1.5); backdrop-filter: blur(20px) saturate(1.5);
    border: 1px solid rgb(255 255 255 / .55);
    box-shadow: 0 30px 80px rgb(0 0 0 / .35);
  }
  [data-template="immersive"][data-surface="solid"] .card {
    background: var(--sc-card); -webkit-backdrop-filter: none; backdrop-filter: none;
  }
  [data-template="immersive"] .tabs { grid-template-columns: 1fr 1fr; }
  [data-template="immersive"] input[type=text], [data-template="immersive"] input[type=password],
  [data-template="immersive"] input[type=email], [data-template="immersive"] input[type=tel] { background: rgb(255 255 255 / .9); }
  [data-template="immersive"] .field { max-width: none; }

  /* ---- 4. HEADER BAR -------------------------------------------------------------------------------- */
  [data-template="headerbar"] body { background: #f2f4f5; }
  [data-template="headerbar"] .sc-hero {
    display: flex; align-items: center; position: fixed; top: 0; inset-inline: 0; z-index: 4;
    height: 64px; padding-inline: clamp(16px, 4vw, 40px); padding-inline-end: 190px;
    background: var(--sc-brand); color: #fff; box-shadow: 0 1px 0 rgb(0 0 0 / .08), 0 6px 18px rgb(0 0 0 / .08);
  }
  [data-template="headerbar"] .sc-hero-inner { display: flex; align-items: center; gap: 14px; min-width: 0; }
  [data-template="headerbar"] .sc-hero-logo { max-height: 38px; max-width: 160px; margin: 0; }
  [data-template="headerbar"] .sc-hero-name {
    font-size: 1.12rem; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  [data-template="headerbar"] .sc-hero-welcome { display: none; }
  [data-template="headerbar"] .langbar { top: 12px; z-index: 6; }
  [data-template="headerbar"] .lang { padding: 7px 14px; box-shadow: none; }
  [data-template="headerbar"] .page { padding: calc(64px + clamp(20px, 4vh, 44px)) clamp(12px, 4vw, 40px) clamp(20px, 4vh, 40px); }
  [data-template="headerbar"] .card {
    max-width: 1120px; margin-block: 0 auto;
    grid-template-columns: minmax(0, 1.7fr) minmax(240px, 1fr);
    grid-template-areas: "brand brand" "signin side" "foot foot";
    column-gap: clamp(24px, 4vw, 56px);
    border-radius: calc(var(--sc-radius) * .6);
    box-shadow: 0 1px 3px rgb(0 0 0 / .08), 0 10px 30px rgb(0 0 0 / .06);
    padding-bottom: clamp(20px, 3vw, 32px);
  }
  [data-template="headerbar"] .card:has(.sc-extras[data-empty]) { grid-template-columns: minmax(0, 1fr); grid-template-areas: "brand" "signin" "foot"; }
  [data-template="headerbar"] .sc-brandblock { grid-area: brand; }
  [data-template="headerbar"] .welcome { margin: 0 0 4px; font-size: clamp(1.1rem, 1.5vw, 1.35rem); color: var(--sc-ink); font-weight: 600; }
  [data-template="headerbar"] .sc-signin { grid-area: signin; min-width: 0; }
  [data-template="headerbar"] .sc-extras {
    grid-area: side; align-self: start; margin-top: clamp(20px, 3vw, 34px);
    border-inline-start: 1px solid var(--sc-line); padding-inline-start: clamp(18px, 2.5vw, 32px);
  }
  [data-template="headerbar"] .sc-extras[data-empty] { display: none; }
  [data-template="headerbar"] .sc-extras .help { margin-top: 0; }
  [data-template="headerbar"] .sc-foot {
    grid-area: foot; display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px;
    margin-top: 26px; padding-top: 16px; border-top: 1px solid var(--sc-line);
  }
  [data-template="headerbar"] .sc-foot .terms { margin: 0; }
  [data-template="headerbar"] .info-btn { position: static; }
  [data-template="headerbar"] .info-panel { flex-basis: 100%; margin-top: 0; }

  /* ---- 5. RESORT (editorial) ------------------------------------------------------------------------ */
  [data-template="editorial"] body { background: #f6f2ec; }
  [data-template="editorial"] .page { padding: 0 0 clamp(28px, 6vh, 64px); }
  [data-template="editorial"] .sc-hero {
    display: flex; align-items: center; justify-content: center; text-align: center;
    align-self: stretch; min-height: 48vh;
    padding: clamp(72px, 11vh, 120px) clamp(20px, 6vw, 80px) calc(clamp(56px, 8vh, 96px) + 80px);
  }
  [data-template="editorial"][data-hero="short"] .sc-hero { min-height: 34vh; }
  [data-template="editorial"][data-hero="tall"] .sc-hero { min-height: 66vh; }
  [data-template="editorial"] .sc-hero-inner { max-width: 60rem; display: flex; flex-direction: column; align-items: center; }
  [data-template="editorial"] .sc-hero-name {
    font-size: clamp(2.2rem, 5.4vw, 4.4rem); font-weight: 600;
    font-family: var(--sc-heading-font, Georgia, "Times New Roman", serif);
  }
  [data-template="editorial"] .sc-hero-welcome { font-size: clamp(1.05rem, 1.8vw, 1.35rem); margin-inline: auto; opacity: .95; }
  [data-template="editorial"] .card {
    margin-block: -96px 0; max-width: 880px; width: calc(100% - 2 * clamp(12px, 4vw, 48px));
    background: transparent; box-shadow: none; padding: 0;
  }
  [data-template="editorial"] .sc-signin {
    background: var(--sc-card); border-radius: var(--sc-radius); box-shadow: 0 24px 60px rgb(0 0 0 / .16);
    padding: clamp(22px, 3vw, 44px) clamp(20px, 3.5vw, 52px) clamp(26px, 3vw, 44px);
  }
  [data-template="editorial"] .sc-extras { padding: 0 4px; }
  [data-template="editorial"] .help { text-align: center; margin-inline: auto; }
  [data-template="editorial"] #custom-html {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 16px; padding: 8px;
  }
  [data-template="editorial"] #custom-html:empty { display: none; }
  [data-template="editorial"] #custom-html > * {
    margin: 0; background: var(--sc-card); border-radius: calc(var(--sc-radius) * .7);
    padding: 18px 20px; box-shadow: 0 6px 20px rgb(0 0 0 / .07);
  }
  [data-template="editorial"] .sc-foot {
    display: flex; flex-wrap: wrap; align-items: center; justify-content: center; gap: 14px; margin-top: 18px;
  }
  [data-template="editorial"] .sc-foot .terms { margin: 0; }
  [data-template="editorial"] .info-btn { position: static; }
  [data-template="editorial"] .info-panel { flex-basis: 100%; margin-top: 0; background: var(--sc-card); }

  /* ---- 6. KIOSK -------------------------------------------------------------------------------------- */
  [data-template="kiosk"] { font-size: 112.5%; }
  [data-template="kiosk"] body { background: #edf1f1; }
  [data-template="kiosk"] .card { max-width: 780px; }
  [data-template="kiosk"] .brand { justify-content: center; }
  [data-template="kiosk"] .brand img { max-height: 76px; }
  [data-template="kiosk"] .brand .name { font-size: clamp(1.4rem, 2.4vw, 1.9rem); }
  [data-template="kiosk"] .welcome { text-align: center; margin-inline: auto; font-size: 1.15rem; }
  [data-template="kiosk"] .tab { min-height: 76px; font-size: 1.3rem; }
  [data-template="kiosk"] .field { max-width: none; }
  [data-template="kiosk"] label { font-size: 1.15rem; }
  [data-template="kiosk"] input[type=text], [data-template="kiosk"] input[type=password],
  [data-template="kiosk"] input[type=email], [data-template="kiosk"] input[type=tel] {
    min-height: 64px; font-size: 1.3rem; border-width: 2px;
  }
  [data-template="kiosk"] button.primary { width: 100%; min-height: 64px; font-size: 1.25rem; }
  [data-template="kiosk"] .pill { font-size: 1.1rem; padding: 16px 26px; }
  [data-template="kiosk"] .lang { font-size: 1.1rem; padding: 12px 20px; }
  [data-template="kiosk"] .info-btn { width: 44px; height: 44px; font-size: 1.1rem; }

  /* ---- shared by every non-classic template ---------------------------------------------------------- */
  [data-template]:not([data-template="classic"]) .card { position: relative; }

  /* TABLET AND PHONE. Two-column layouts become one column; the photograph becomes a banner above the card. */
  @media (max-width: 860px) {
    [data-template="split"] .page { grid-template-columns: minmax(0, 1fr); }
    [data-template="split"] .sc-hero, [data-template="split"][data-panel="start"] .sc-hero {
      grid-column: 1; grid-row: 1; position: relative; height: auto; min-height: 34vh;
      padding: 72px 20px 44px;
    }
    [data-template="split"] .card, [data-template="split"][data-panel="start"] .card {
      grid-column: 1; grid-row: 2; min-height: 0; margin-top: -22px;
      border-radius: var(--sc-radius) var(--sc-radius) 0 0; padding: 26px 18px 64px;
    }
    [data-template="headerbar"] .card { grid-template-columns: minmax(0, 1fr); grid-template-areas: "brand" "signin" "side" "foot"; }
    [data-template="headerbar"] .sc-extras {
      border-inline-start: 0; padding-inline-start: 0; border-top: 1px solid var(--sc-line); padding-top: 18px;
    }
  }
  @media (max-width: 680px) {
    /* The base layout puts the language selector in the flow on a phone. The photographic and bar layouts
       keep it over their own header instead, where it does not become a row of the page grid. */
    [data-template="split"] .langbar, [data-template="immersive"] .langbar, [data-template="editorial"] .langbar {
      position: absolute; top: 12px; right: auto; left: auto; inset-inline-end: 12px; width: auto; padding: 0; z-index: 5;
    }
    [data-template="headerbar"] .langbar { position: fixed; top: 12px; right: auto; left: auto; inset-inline-end: 12px; width: auto; padding: 0; }
    [data-template="headerbar"] .sc-hero { padding-inline-end: 150px; }
    [data-template="headerbar"] .sc-hero-logo { max-height: 30px; max-width: 96px; }
    [data-template="headerbar"] .page { padding: 80px 12px 20px; }
    [data-template="immersive"] .page, [data-template="immersive"][data-panel] .page {
      padding: 42vh 12px 16px; align-items: stretch;
    }
    [data-template="immersive"] .sc-hero-inner, [data-template="immersive"][data-panel] .sc-hero-inner {
      top: 72px; bottom: auto; inset-inline: 20px; margin: 0; max-width: none; text-align: start;
    }
    [data-template="immersive"][data-panel] .sc-hero-welcome { margin-inline: 0; }
    [data-template="immersive"] .sc-hero-name { font-size: clamp(2.2rem, 11vw, 3rem); }
    [data-template="immersive"] .card { max-width: none; }
    [data-template="editorial"] .sc-hero { min-height: 40vh; padding: 76px 18px 120px; }
    [data-template="editorial"] .card { width: calc(100% - 24px); margin-top: -84px; }
    [data-template="editorial"] .sc-signin { padding: 20px 16px 26px; }
    [data-template="kiosk"] { font-size: 106%; }
  }
}
</style>
<style id="sc-guard">
/* THE GUARD -- why the hotel's stylesheet can restyle every control and hide none of them.
   The cascade has three tiers here, weakest first:
     1. @layer sc-base, sc-template   the portal's own look, and the template's
     2. @layer hotel                  the hotel's custom CSS: beats (1) whatever the specificity
     3. THIS SHEET, UNLAYERED         a normal declaration outside every layer beats every normal
                                      declaration inside one, whatever the specificity
   and above all of them the inline style="" the sign-in script sets to show and hide its own forms, which
   is why nothing below uses !important: an important declaration here would beat the script too, and the
   voucher/personal-account switch would stop working.
   The one way a layered rule CAN beat an unlayered one is !important -- for important declarations the layer
   order is reversed. So the portal removes !important from the hotel's stylesheet before serving it (see
   internal/portaldesign), and the tier above is what is left.
   What is guarded is what a guest needs to sign in: the path from the page down to the fields, the fields,
   their labels and buttons, the group tabs, and the messages the server or the sign-in script shows. What is
   guarded against is the ways a stylesheet hides a thing -- display, visibility, opacity, pointer-events,
   clipping, transforms, filters and zero heights -- not every visual trick an authenticated administrator
   could write; that is what the password step-up on the stylesheet itself is for. Values here match the
   portal's own, so on an unstyled page this sheet changes nothing. */
html, body { display: block; visibility: visible; opacity: 1; }
.page { display: flex; }
[data-template="split"] .page { display: grid; }
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
#server-error, .notice.show, .panels .err { display: block; position: static; }
/* Anything a stylesheet draws with ::before/::after can be looked at but never clicked, so a decorative
   overlay can never sit between a guest and the Login button. */
*::before, *::after { pointer-events: none; }
/* The hotel's own markup stays in its own box: paint containment clips it to the container, and makes the
   container the reference for position:fixed inside it -- so a fragment cannot lift itself over the forms. */
#custom-html { position: relative; inset: auto; z-index: 0; contain: layout paint; transform: none; translate: none; }
</style>
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
  <!-- THE HERO: decoration for the photographic and bar templates, hidden in Classic and Kiosk. It repeats the
       hotel's name and welcome in display type and is aria-hidden -- the card below keeps the real, readable
       copies, which those templates hide visually rather than remove. -->
  <div class="sc-hero" aria-hidden="true">
    <div class="sc-hero-inner">
      <img class="sc-hero-logo" alt="" hidden>
      <div class="sc-hero-name"></div>
      <p class="sc-hero-welcome"></p>
    </div>
  </div>
  <div class="langbar">
    <label class="lang" for="lang">
      <span id="lang-flag" aria-hidden="true">🌐</span>
      <select id="lang" aria-label="Language"><option value="en" selected>English</option></select>
    </label>
  </div>

  <main class="card">
    <div class="sc-brandblock">
    <!-- BRANDING. Filled from /api/branding when the hotel has published a design; the defaults below are
         what an unbranded appliance shows, which must still look deliberate rather than broken. -->
    <div class="brand">
      <img id="brand-logo" alt="" style="display:none">
      <span class="name" id="brand-name"></span>
    </div>
    <!-- THE HOTEL'S OWN WORDS. Welcome, help and terms were settable in Hotel Admin and rendered by NOTHING:
         three fields an operator could fill in that changed nothing a guest saw, which is the same write-only
         configuration the branding screen was rebuilt to stop being. Each is hidden until it has content, so
         an appliance that has set none of them looks deliberate rather than gappy. -->
    <p class="welcome" id="brand-welcome" hidden></p>
    <hr class="rule">
    </div>

    <div class="sc-signin">

    <!-- WHY THE INTERNET STOPPED. Shown only when this device's most recent access ended because it ran out
         of data or time; the sign-in below is unchanged and the guest carries straight on into it. -->
    <div class="notice" id="access-ended" role="status" aria-live="polite"></div>
    <div class="notice" id="site-notice" role="status" aria-live="polite"></div>
    {{if .Error}}
    <!-- WHAT THE SERVER SAID, WHERE THE GUEST CAN READ IT.
         landing() has always composed a message for every refusal it handles -- an empty voucher box, a
         device it cannot place on the guest network, packages that are unavailable -- and passed it in as
         .Error. Nothing here rendered it, so all of those arrived as a page that looked exactly like the one
         the guest had just submitted. The voucher and personal-account forms are plain HTML POSTs, so this
         response IS the page they land on; the PMS and OTP flows fetch and fill their own .err boxes, which
         is why their messages always showed and these never did.
         role="alert" rather than "status": this is the answer to something the guest just did. -->
    <div class="notice notice--error show" id="server-error" role="alert" aria-live="assertive">{{.Error}}</div>
    {{end}}

    <div class="tabs" id="tabs" role="tablist"></div>
    <div class="panels">

  <!-- ACCOUNT LOGIN — one panel, two ways in.
       The reference presents Voucher and Personal Account as a single choice behind a checkbox rather than as
       two separate sign-in methods, because from a guest's side they are the same question: "I have something
       that lets me on". Both forms below are unchanged; only which one is visible moves. -->
  <div class="panel panel--wide" id="panel-accountlogin">
    <label class="pill" for="use-personal">
      <input type="checkbox" id="use-personal">
      <span data-i18n="account.personal" data-i18n-en="Use Personal Account">Use Personal Account</span>
    </label>

    <form method="POST" action="/auth/voucher" id="form-voucher">
      <div class="field">
        <label for="voucher"><span data-i18n="voucher.label" data-i18n-en="Voucher Code">Voucher Code</span></label>
        <input id="voucher" name="code" type="text" autocomplete="off" required maxlength="32">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.login" data-i18n-en="Login">Login</span></button>
      <div class="err"></div>
    </form>

    <form method="POST" action="/auth/credentials" id="form-credentials" autocomplete="off" style="display:none">
      <div class="field">
        <label for="ga-username"><span data-i18n="account.user" data-i18n-en="Username">Username</span></label>
        <input id="ga-username" name="username" type="text" autocomplete="username" required maxlength="64">
      </div>
      <div class="field">
        <label for="ga-password"><span data-i18n="account.pass" data-i18n-en="Password">Password</span></label>
        <input id="ga-password" name="password" type="password" autocomplete="current-password" required maxlength="128">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.login" data-i18n-en="Login">Login</span></button>
      <div class="err"></div>
    </form>
  </div>

  <div class="panel" id="panel-email">
    <form data-otp="email" data-stage="dest" autocomplete="off">
      <div class="field">
        <label for="email"><span data-i18n="email.dest" data-i18n-en="Email address">Email address</span></label>
        <input id="email" name="dest" type="email" required placeholder="you@example.com" autocomplete="email">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.sendcode" data-i18n-en="Send code">Send code</span></button>
      <div class="err"></div>
    </form>
    <form data-otp="email" data-stage="code" autocomplete="off" style="display:none">
      <p class="small"><span data-i18n="otp.sent.email" data-i18n-en="We sent a 6-digit code to">We sent a 6-digit code to</span> <span class="dest"></span>.</p>
      <div class="field">
        <label><span data-i18n="otp.code" data-i18n-en="Verification code">Verification code</span></label>
        <input name="code" type="text" inputmode="numeric" pattern="[0-9]*" required maxlength="6" placeholder="------">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.verify" data-i18n-en="Verify">Verify</span></button>
      <button type="button" class="link" data-resend data-i18n="otp.retry.email" data-i18n-en="Try a different email">Try a different email</button>
      <div class="err"></div>
    </form>
  </div>

  <!-- PMS / Room panel — guest enters room number plus one verification field -->
  <div class="panel" id="panel-pms">
    <form id="form-pms" autocomplete="off">
      <div class="field">
        <label for="pms-room"><span data-i18n="pms.room" data-i18n-en="Room Number">Room Number</span></label>
        <input id="pms-room" name="room" type="text" inputmode="numeric" required>
      </div>
      <div class="field">
        <label for="pms-secondary"><span data-i18n="pms.secondary" data-i18n-en="Password">Password</span></label>
        <input id="pms-secondary" name="secondary" type="text" required>
        <!-- The hint sits UNDER the field, as in the reference. Its text is set from the site's configured
             room-sign-in mode, so a hotel that asks for a surname and one that asks for a reservation number
             each say so -- it is a real prompt, not decoration. -->
        <p class="hint" id="pms-prompt"></p>
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.submit" data-i18n-en="Submit">Submit</span></button>
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
      <div class="field">
        <label for="ps-pin"><span data-i18n="poststay.pin" data-i18n-en="Post-stay PIN">Post-stay PIN</span></label>
        <input id="ps-pin" name="pin" type="text" inputmode="text" autocapitalize="characters" required
               data-i18n-ph="poststay.hint" data-i18n-ph-en="The PIN you were given at checkout"
               placeholder="The PIN you were given at checkout">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.reconnect" data-i18n-en="Reconnect">Reconnect</span></button>
    </form>
    <div class="err" id="ps-err" role="alert" aria-live="polite"></div>
  </div>

  <!-- Social panel -->
  <div class="panel" id="panel-social">
    <div id="social-providers"></div>
    <p class="small" style="margin-top:12px" data-i18n="social.note"
       data-i18n-en="You'll be redirected to the provider, then back here.">You'll be redirected to the provider, then back here.</p>
  </div>

  <!-- SMS panel -->
  <div class="panel" id="panel-sms">
    <form data-otp="sms" data-stage="dest" autocomplete="off">
      <div class="field">
        <label for="phone"><span data-i18n="sms.dest" data-i18n-en="Phone number">Phone number</span></label>
        <input id="phone" name="dest" type="tel" required placeholder="+1 555 123 4567" autocomplete="tel">
        <p class="hint" data-i18n="sms.hint" data-i18n-en="Include the country code, for example +44 20 7946 0958">Include the country code, for example +44 20 7946 0958</p>
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.sendcode" data-i18n-en="Send code">Send code</span></button>
      <div class="err"></div>
    </form>
    <form data-otp="sms" data-stage="code" autocomplete="off" style="display:none">
      <p class="small"><span data-i18n="otp.sent.sms" data-i18n-en="We texted a 6-digit code to">We texted a 6-digit code to</span> <span class="dest"></span>.</p>
      <div class="field">
        <label><span data-i18n="otp.code" data-i18n-en="Verification code">Verification code</span></label>
        <input name="code" type="text" inputmode="numeric" pattern="[0-9]*" required maxlength="6" placeholder="------">
      </div>
      <button class="primary" type="submit"><span data-i18n="btn.verify" data-i18n-en="Verify">Verify</span></button>
      <button type="button" class="link" data-resend data-i18n="otp.retry.sms" data-i18n-en="Use a different number">Use a different number</button>
      <div class="err"></div>
    </form>
  </div>

      <div class="alt" id="alt-methods" style="display:none"></div>
    </div>
    </div>

    <!-- The hotel's own footer: a help line, an optional terms link, and the Advanced fragment. The fragment
         is inserted as MARKUP. It is safe to do so for three independent reasons: edged refuses a design whose
         fragment is not already clean (internal/portaldesign's allowlist, not a denylist); /api/branding
         rebuilds it through the same allowlist before a guest receives it; and this page's Content-Security-
         Policy runs only scripts carrying its per-response nonce, so an inline handler that somehow survived
         would still not execute. -->
    <div class="sc-extras">
    <p class="help" id="brand-help" hidden></p>
    <div id="custom-html"></div>
    </div>
    <div class="sc-foot">
    <p class="terms" id="brand-terms-wrap" hidden>
      <a id="brand-terms" target="_blank" rel="noopener noreferrer"
         data-i18n="terms.link" data-i18n-en="Terms of use">Terms of use</a>
    </p>

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
    </div>
  </main>
  </div>

  <script nonce="{{.Nonce}}">
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
      // Both point at the merged panel: which FORM shows is the pill's business, not the tab's.
      voucher: { id:'voucher', label:'Voucher', panel:'panel-accountlogin' },
      account: { id:'account', label:'Personal account', panel:'panel-accountlogin' },
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
    // The same five prompts as translation keys. The English above stays as the fallback the element carries,
    // so a mode nobody has translated still says something.
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
      // ALTERNATIVES MUST LEAD SOMEWHERE ELSE.
      //
      // Voucher and personal account share ONE panel, chosen between by the pill at the top of it. Listing
      // them here as well gave a guest looking at the voucher form an "Or sign in with: Personal account"
      // link that selected the panel they were already on -- two controls for one choice, and the one that
      // looks like navigation does nothing visible. An alternative is offered only when it opens a DIFFERENT
      // panel from the one already showing.
      const shownPanel = Tabs[members[0]] ? Tabs[members[0]].panel : '';
      const others = members.slice(1).filter(m => Tabs[m] && Tabs[m].panel !== shownPanel);
      if (others.length) {
        const h = document.createElement('h3');
        // Generated text carries its key so the language pass reaches it too. Setting textContent here and
        // translating there would leave whichever ran last on the screen.
        h.dataset.i18n = 'alt.title';
        h.dataset.i18nEn = BUILTIN.en['alt.title'];
        h.textContent = t('alt.title');
        alt.appendChild(h);
        others.forEach(m => {
          const b = document.createElement('button');
          b.type = 'button'; b.className = 'link';
          b.dataset.i18n = 'method.' + m;
          b.dataset.i18nEn = Tabs[m].label;
          b.textContent = t('method.' + m);
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
    // SIX LANGUAGES SHIP WITH THE PORTAL. An operator does not have to type a single word to offer a guest
    // Arabic, Italian, French, Russian or German -- the words below are part of the build. The hotel's own
    // translations, published with its design, are merged OVER these, so a property that calls the field
    // something else says so without losing the other forty strings it never wanted to think about.
    //
    // Resolution order for every string, most specific first:
    //   1. the hotel's published translation for the chosen language
    //   2. the built-in translation for the chosen language
    //   3. the built-in English, which is also the text already in the markup
    // A half-translated portal therefore still reads. A portal that shows raw keys is not a language anybody
    // speaks, and this cannot produce one.
    // THE WORDS COME FROM THE BINARY, not from a literal maintained here. See languages.go: the same map
    // is served at /api/languages so Hotel Admin can show an operator what the portal actually says.
    var LANGS = {{.Languages}};
    var BUILTIN = {{.Strings}};

    var I18N = {};        // the HOTEL's published overrides: code -> { key: text }
    var LANG = 'en';
    var DICT = BUILTIN.en;

    function langMeta(code) {
      for (var i = 0; i < LANGS.length; i++) { if (LANGS[i].code === code) return LANGS[i]; }
      return null;
    }

    // words merges the hotel's overrides over the built-in dictionary for one language. An override that is
    // blank is not an override -- an operator who clears a field is asking for the shipped wording back, not
    // for an empty label.
    function words(code) {
      var out = {};
      var base = BUILTIN[code] || {};
      var over = I18N[code] || {};
      Object.keys(base).forEach(function (k) { out[k] = base[k]; });
      Object.keys(over).forEach(function (k) { if (over[k]) out[k] = over[k]; });
      return out;
    }

    // t is for text this script GENERATES. Text that is already in the markup carries data-i18n instead and is
    // translated by the pass below.
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
      var sel = document.getElementById('lang');
      if (sel) sel.setAttribute('aria-label', t('lang.label'));
    }

    // REMEMBERING A CHOICE ON A CAPTIVE PORTAL IS NOT localStorage ALONE.
    //
    // iOS opens the portal in the Captive Network Assistant, a throwaway WebView. Its storage is not the
    // Safari profile's and does not reliably outlive the sheet, and on a locked-down device localStorage can
    // throw on the first write. A guest who picks Arabic and is bounced back to the portal a moment later
    // must not be reading English again. So the choice is written to both a cookie and localStorage, and read
    // back from whichever survived.
    //
    // It is written ONLY when the guest chooses. Automatic detection deliberately leaves no trace: a stored
    // value must mean "this person decided", or the first thing detection does is overwrite the evidence of
    // the decision it is supposed to defer to.
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

    // THE DEVICE'S OWN LANGUAGE PREFERENCE, IN THE ORDER THE DEVICE GIVES IT.
    //
    // navigator.languages is an ORDERED list -- a guest whose phone is set to Italian first and French second
    // should get Italian at a hotel offering both, and French at one offering only French. Reading
    // navigator.language alone, as this did, throws that away and answers with the first entry regardless of
    // what the hotel enabled.
    //
    // Each entry contributes twice: the tag as given and its primary subtag, so ar-EG matches a hotel that
    // enabled 'ar', and an exact 'pt-BR' would still be preferred over plain 'pt' if a hotel offered both.
    // Order is preserved and duplicates dropped, so the result reads as the device's own ranking.
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

    // chooseLanguage answers the only question that matters: which of the languages THIS HOTEL offers should
    // this guest see? Returns '' when the device asked for nothing the hotel has.
    function chooseLanguage(offeredCodes) {
      var have = {};
      offeredCodes.forEach(function (c) { have[String(c).toLowerCase()] = c; });
      var prefs = devicePreferredLanguages();
      for (var i = 0; i < prefs.length; i++) {
        if (have[prefs[i]]) return have[prefs[i]];
        // A hotel offering 'pt-br' should also answer a device asking for plain 'pt'.
        for (var j = 0; j < offeredCodes.length; j++) {
          if (String(offeredCodes[j]).toLowerCase().split('-')[0] === prefs[i]) return offeredCodes[j];
        }
      }
      return '';
    }

    // Remember the guest's choice for this device: a guest who picked their language once should not have to
    // do it again on the next captive-portal redirect.
    document.getElementById('lang').addEventListener('change', function (e) {
      rememberLanguage(e.target.value);
      applyLanguage(e.target.value);
    });

    // THE TEMPLATE. The server already rendered the published template's attributes on <html>, so a guest's
    // first paint is the right layout; this re-applies them from the design, which is what makes the admin
    // preview follow an operator's unsaved choice. Every value is a closed vocabulary (edged refuses anything
    // else and /api/branding drops it), and each is written as an attribute or a custom property -- never as
    // markup or a stylesheet.
    const TEMPLATES = ['classic', 'split', 'immersive', 'headerbar', 'editorial', 'kiosk'];
    function applyTemplate(d) {
      const el = document.documentElement;
      const o = (d && d.template_options && typeof d.template_options === 'object') ? d.template_options : {};
      el.dataset.template = TEMPLATES.indexOf(d && d.template_id) >= 0 ? d.template_id : 'classic';
      el.dataset.density = ['compact', 'comfortable', 'spacious'].indexOf(o.density) >= 0 ? o.density : '';
      el.dataset.panel = ['start', 'center', 'end'].indexOf(o.panel_position) >= 0 ? o.panel_position : '';
      el.dataset.hero = ['short', 'medium', 'tall'].indexOf(o.hero_height) >= 0 ? o.hero_height : '';
      el.dataset.surface = ['solid', 'glass'].indexOf(o.surface) >= 0 ? o.surface : '';
      const overlay = Number(o.overlay);
      if (o.overlay !== undefined && o.overlay !== null && overlay >= 0 && overlay <= 90) {
        el.style.setProperty('--sc-overlay', String(overlay / 100));
      }
      if (o.heading_font) el.style.setProperty('--sc-heading-font', o.heading_font);
      const hero = (d && (d.hero_image_url || d.background_url)) || '';
      if (hero) el.style.setProperty('--sc-hero', 'url("' + encodeURI(hero) + '")');
    }

    // BRANDING. Applied before anything else paints so the guest never sees the default teal flash to the
    // hotel's colour. Every value is optional and every default is a deliberate, finished-looking fallback.
    fetch('/api/branding').then(r => r.ok ? r.json() : {}).then(b => {
      const d = (b && b.design) || b || {};
      const root = document.documentElement.style;
      applyTemplate(d);
      if (d.brand_color)      root.setProperty('--sc-brand', d.brand_color);
      if (d.brand_color_dark) root.setProperty('--sc-brand-dark', d.brand_color_dark);
      if (d.text_color)       root.setProperty('--sc-ink', d.text_color);
      if (d.font_family)      root.setProperty('font-family', d.font_family);
      if (d.corner_radius)    root.setProperty('--sc-radius', d.corner_radius);
      if (d.background_url)   root.setProperty('--sc-bg', 'url("' + encodeURI(d.background_url) + '")');
      if (d.hotel_name) {
        document.getElementById('brand-name').textContent = d.hotel_name;
        document.querySelector('.sc-hero-name').textContent = d.hotel_name;
        document.title = d.hotel_name + ' — Wi-Fi';
      }
      if (d.logo_url) {
        const img = document.getElementById('brand-logo');
        // AN IMAGE THAT DOES NOT LOAD MUST LEAVE NOTHING BEHIND. A logo whose file has been deleted, or whose
        // https host is unreachable -- which on a captive portal is EVERY external host -- otherwise renders
        // as a broken-image glyph at the top of the sign-in page on every guest device.
        img.onerror = function () { img.style.display = 'none'; };
        img.onload = function () { img.style.display = ''; };
        img.alt = d.hotel_name || 'Hotel';
        img.src = d.logo_url;
        const heroLogo = document.querySelector('.sc-hero-logo');
        heroLogo.onerror = function () { heroLogo.hidden = true; };
        heroLogo.onload = function () { heroLogo.hidden = false; };
        heroLogo.src = d.logo_url;
      }
      // The hotel's own words. Each is shown only when it has something to say.
      if (d.welcome_text) {
        const el = document.getElementById('brand-welcome');
        el.textContent = d.welcome_text; el.hidden = false;
        document.querySelector('.sc-hero-welcome').textContent = d.welcome_text;
      }
      if (d.help_text) {
        const el = document.getElementById('brand-help');
        el.textContent = d.help_text; el.hidden = false;
      }
      if (d.terms_url) {
        document.getElementById('brand-terms').href = d.terms_url;
        document.getElementById('brand-terms-wrap').hidden = false;
      }
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
      if (d.custom_html) {
        // innerHTML, deliberately: the fragment IS markup. What arrives here has been through the allowlist
        // twice (on save in edged, on serve in /api/branding) and the page's CSP refuses any script without
        // this response's nonce -- see the note beside #custom-html.
        document.getElementById('custom-html').innerHTML = d.custom_html;
      }
      // A layout with a help column hides the column when the hotel has nothing to put in it.
      document.querySelector('.sc-extras').toggleAttribute('data-empty', !d.help_text && !d.custom_html);
      I18N = (d.translations && typeof d.translations === 'object') ? d.translations : {};
      // ONLY THE CONFIGURED LANGUAGES ARE OFFERED. A hotel that has chosen which languages its guests see gets
      // exactly that list; one that has never been near the screen gets the six the portal ships words for.
      // What is never offered is a language with nothing behind it.
      renderLanguages(Array.isArray(d.languages) && d.languages.length ? d.languages : null);
    }).catch(() => {});

    // renderLanguages fills the selector and selects the language this guest should see.
    //
    // It runs ONCE before branding is fetched and again with the hotel's answer. That is deliberate: a
    // captive portal is reached by a device with no internet, the branding call can fail, and a selector that
    // is only populated on success is a selector that is empty exactly when the network is worst.
    function renderLanguages(configured) {
      var sel = document.getElementById('lang');
      var offered = [];
      if (configured) {
        configured.forEach(function (l) {
          var code = (l && l.code) || l;
          var meta = langMeta(code);
          // A code the portal has no words for is still offered IF the hotel published a translation for it --
          // that is a hotel adding a seventh language, not an empty promise.
          if (!meta && !(I18N[code] && Object.keys(I18N[code]).length)) return;
          offered.push({ code: code, label: (l && l.label) || (meta && meta.label) || code.toUpperCase() });
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

      // THE ORDER OF PREFERENCE, and each step's reason:
      //
      //   1. what this guest CHOSE, if they chose. A decision outranks a detection, always.
      //   2. what their device asks for, best match against what the hotel enabled.
      //   3. English, which the hotel cannot switch off and every other language falls back to.
      //
      // A remembered choice the hotel no longer offers falls THROUGH to detection rather than pinning the
      // guest to a language that is not on the page any more.
      var codes = Array.prototype.map.call(sel.options, function (o) { return o.value; });
      var want = rememberedLanguage();
      if (!want || codes.indexOf(want) < 0) want = chooseLanguage(codes);
      if (!want || codes.indexOf(want) < 0) want = codes.indexOf('en') >= 0 ? 'en' : (codes[0] || 'en');
      sel.value = want;
      applyLanguage(want);
    }
    renderLanguages(null);

    // The "Use Personal Account" toggle. Both forms exist in the DOM at all times so neither loses what the
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
        // ONE PROMPT, UNDER THE FIELD. It used to be set as the hint AND as the field's placeholder, so the
        // guest read the same sentence twice with the second copy sitting where their answer goes.
        const prompt = document.getElementById('pms-prompt');
        const key = PMSPromptKeys[cfg.pms.mode] || PMSPromptKeys.either;
        prompt.dataset.i18n = key;
        prompt.dataset.i18nEn = PMSPrompts[cfg.pms.mode] || PMSPrompts.either;
        prompt.textContent = t(key);
        document.getElementById('pms-secondary').dataset.mode = cfg.pms.mode || 'either';
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
            a.dataset.i18n = 'social.' + p;
            a.dataset.i18nEn = ProviderLabels[p] || ('Continue with ' + p);
            a.textContent = t('social.' + p) === ('social.' + p) ? a.dataset.i18nEn : t('social.' + p);
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
        n.dataset.i18n = 'notice.nopackages';
        n.dataset.i18nEn = BUILTIN.en['notice.nopackages'];
        n.textContent = t('notice.nopackages');
        n.classList.add('show');
      }
      if (enabled.length === 0) {
        const none = document.createElement('div');
        none.className = 'small';
        none.dataset.i18n = 'notice.nomethods';
        none.dataset.i18nEn = BUILTIN.en['notice.nomethods'];
        none.textContent = t('notice.nomethods');
        tabsEl.innerHTML = '';
        tabsEl.appendChild(none);
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
          el.innerHTML = Groups[gid].icon + '<span data-i18n="tab.' + gid + '"></span>';
          const span = el.querySelector('span');
          span.dataset.i18nEn = Groups[gid].label;
          span.textContent = t('tab.' + gid);
          el.addEventListener('click', () => setGroup(gid, groupMembers));
          tabsEl.appendChild(el);
        });
      } else {
        tabsEl.style.display = 'none';
      }
      // The pill only makes sense when the site actually offers BOTH ways in. With one of them enabled it is
      // a choice with a single option, so it is hidden and its form shown directly.
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
    // It is a FUNCTION rather than a constant because the guest chooses their language after this script
    // loads, and a string captured at load time is a string in whatever language the page started in.
    function PHASE3_FAIL() { return t('err.generic'); }

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
      return (j && typeof j.message === 'string' && j.message) ? j.message : PHASE3_FAIL();
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
          errEl.textContent = t('err.retry');
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
      h.className = 'small';
      h.dataset.i18n = 'pms.choose';
      h.dataset.i18nEn = BUILTIN.en['pms.choose'];
      h.textContent = t('pms.choose');
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
  <script nonce="{{.Nonce}}">
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
  <script nonce="{{.Nonce}}">
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
  <script nonce="{{.Nonce}}">
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
