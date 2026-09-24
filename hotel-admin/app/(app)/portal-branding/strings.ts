// WHAT THE GUEST PORTAL SAYS, AND IN WHICH LANGUAGES.
//
// This file is one half of a contract. The other half is BUILTIN in data-plane/cmd/portald/templates.go, and
// a test in cmd/portald reads both and fails if they disagree in either direction — a key offered here that
// the portal no longer renders is a field an operator fills in for nothing, and a key the portal renders that
// is missing here is a string nobody can change.

/** The settings a hotel can change. One flat document; the operator never sees its shape. */
export type Design = {
  hotel_name?: string;
  welcome_text?: string;
  help_text?: string;
  terms_url?: string;
  logo_url?: string;
  background_url?: string;
  brand_color?: string;
  brand_color_dark?: string;
  text_color?: string;
  font_family?: string;
  corner_radius?: string;
  /** code -> { key: text }. The hotel's own wording, merged over the shipped translations. */
  translations?: Record<string, Record<string, string>>;
  /** Which languages a guest is offered. Absent means "all the shipped ones". */
  languages?: { code: string; label: string }[];
  /** The page layout. Absent means classic, the original page. */
  template_id?: string;
  /** Closed vocabularies each template translates into its own CSS. None of them carries markup. */
  template_options?: TemplateOptions;
  /** The photograph for the hero panel of the split/immersive/resort layouts. Falls back to background_url. */
  hero_image_url?: string;
  custom_css?: string;
  custom_html?: string;
};

export type TemplateOptions = {
  /** Darkening over the hero photograph, 0–90 (%). */
  overlay?: number;
  panel_position?: "start" | "center" | "end";
  density?: "compact" | "comfortable" | "spacious";
  hero_height?: "short" | "medium" | "tall";
  surface?: "solid" | "glass";
  heading_font?: string;
};

/** The six languages the portal ships complete wording for. Codes, order and native names match LANGS in
 *  data-plane/cmd/portald/templates.go. A hotel may add a seventh of its own; that one it translates. */
export const SHIPPED_LANGUAGES: { code: string; label: string; rtl?: boolean }[] = [
  { code: "en", label: "English" },
  { code: "ar", label: "العربية", rtl: true },
  { code: "de", label: "Deutsch" },
  { code: "fr", label: "Français" },
  { code: "it", label: "Italiano" },
  { code: "ru", label: "Русский" },
];

export const isShippedLanguage = (code: string) => SHIPPED_LANGUAGES.some((l) => l.code === code);

/** The guest-facing strings, grouped the way they appear on the page rather than as one long list. */
export const PORTAL_STRINGS: { group: string; key: string; english: string }[] = [
  { group: "Navigation", key: "tab.guest", english: "Guest Login" },
  { group: "Navigation", key: "tab.account", english: "Account Login" },
  { group: "Navigation", key: "alt.title", english: "Or sign in with" },
  { group: "Navigation", key: "method.pms", english: "Room" },
  { group: "Navigation", key: "method.poststay", english: "Post-stay" },
  { group: "Navigation", key: "method.voucher", english: "Voucher" },
  { group: "Navigation", key: "method.account", english: "Personal account" },
  { group: "Navigation", key: "method.email", english: "Email" },
  { group: "Navigation", key: "method.sms", english: "Phone" },
  { group: "Navigation", key: "method.social", english: "Social" },

  { group: "Guest Login", key: "pms.room", english: "Room Number" },
  { group: "Guest Login", key: "pms.secondary", english: "Password" },
  { group: "Guest Login", key: "pms.prompt.lastname", english: "Last name on the reservation" },
  { group: "Guest Login", key: "pms.prompt.firstname", english: "First name on the reservation" },
  { group: "Guest Login", key: "pms.prompt.reservation", english: "Reservation / confirmation number" },
  { group: "Guest Login", key: "pms.prompt.any", english: "First name, last name, or reservation number" },
  { group: "Guest Login", key: "pms.prompt.either", english: "Last name OR reservation number" },
  { group: "Guest Login", key: "pms.choose", english: "Choose your internet package" },

  { group: "Account Login", key: "account.personal", english: "Use Personal Account" },
  { group: "Account Login", key: "voucher.label", english: "Voucher Code" },
  { group: "Account Login", key: "account.user", english: "Username" },
  { group: "Account Login", key: "account.pass", english: "Password" },

  { group: "Email and SMS", key: "email.dest", english: "Email address" },
  { group: "Email and SMS", key: "sms.dest", english: "Phone number" },
  { group: "Email and SMS", key: "sms.hint", english: "Include the country code, for example +44 20 7946 0958" },
  { group: "Email and SMS", key: "otp.code", english: "Verification code" },
  { group: "Email and SMS", key: "otp.sent.email", english: "We sent a 6-digit code to" },
  { group: "Email and SMS", key: "otp.sent.sms", english: "We texted a 6-digit code to" },
  { group: "Email and SMS", key: "otp.retry.email", english: "Try a different email" },
  { group: "Email and SMS", key: "otp.retry.sms", english: "Use a different number" },

  { group: "Post-stay", key: "poststay.pin", english: "Post-stay PIN" },
  { group: "Post-stay", key: "poststay.hint", english: "The PIN you were given at checkout" },

  { group: "Buttons", key: "btn.login", english: "Login" },
  { group: "Buttons", key: "btn.submit", english: "Submit" },
  { group: "Buttons", key: "btn.sendcode", english: "Send code" },
  { group: "Buttons", key: "btn.verify", english: "Verify" },
  { group: "Buttons", key: "btn.reconnect", english: "Reconnect" },

  { group: "Social sign-in", key: "social.note", english: "You will be redirected to the provider, then back here." },
  { group: "Social sign-in", key: "social.google", english: "Continue with Google" },
  { group: "Social sign-in", key: "social.apple", english: "Continue with Apple" },
  { group: "Social sign-in", key: "social.facebook", english: "Continue with Facebook" },

  { group: "Device info", key: "info.device", english: "Your device" },
  { group: "Device info", key: "info.ip", english: "IP address" },
  { group: "Device info", key: "info.mac", english: "MAC address" },
  { group: "Device info", key: "info.help", english: "Reception may ask for these if you need help connecting." },

  { group: "Messages", key: "notice.nomethods", english: "There is no way to sign in on this network yet. Please contact reception." },
  { group: "Messages", key: "notice.nopackages", english: "Internet access is not available here at the moment. You can still sign in, but there is nothing to connect you to yet — please let reception know." },
  { group: "Messages", key: "err.generic", english: "We could not verify your stay. Please check your details or contact reception." },
  { group: "Messages", key: "err.retry", english: "You can try again now." },
  { group: "Messages", key: "lang.label", english: "Language" },
  { group: "Messages", key: "terms.link", english: "Terms of use" },
];

export const STRING_GROUPS = Array.from(new Set(PORTAL_STRINGS.map((s) => s.group)));

/** The settings that are "advanced": the only ones that can put markup or a stylesheet on the page, and
 *  therefore the only ones whose change -- including clearing them -- is confirmed with a password (Product
 *  Owner decision). This mirrors portaldesign.AdvancedFields on the server, which advancedChanged in
 *  resources_branding.go reads. A NEW field that can carry markup or CSS must be added to BOTH lists or the
 *  step-up is silently bypassed for it. The template choice and its options are closed vocabularies, not
 *  markup, and deliberately save without a password. */
export const ADVANCED_KEYS: (keyof Design)[] = ["custom_css", "custom_html"];

export function advancedChanged(next: Design, current: Design) {
  return ADVANCED_KEYS.some((k) => (next[k] ?? "") !== (current[k] ?? ""));
}

/** Which languages a guest is offered. An unset list means all six, which is what the portal does. */
export function offeredLanguages(d: Design): { code: string; label: string }[] {
  return d.languages?.length ? d.languages : SHIPPED_LANGUAGES.map((l) => ({ code: l.code, label: l.label }));
}

/** Every language this appliance knows about: the six that ship, plus anything the hotel added. */
export function knownLanguages(d: Design): { code: string; label: string }[] {
  const seen = new Map<string, { code: string; label: string }>();
  SHIPPED_LANGUAGES.forEach((l) => seen.set(l.code, { code: l.code, label: l.label }));
  (d.languages ?? []).forEach((l) => { if (!seen.has(l.code)) seen.set(l.code, l); });
  Object.keys(d.translations ?? {}).forEach((c) => {
    if (!seen.has(c)) seen.set(c, { code: c, label: c.toUpperCase() });
  });
  return Array.from(seen.values());
}

/** How complete one language is. A shipped language is never incomplete — the portal has a word for every
 *  key — so only a hotel-added language can fall back to English. */
export function languageStatus(d: Design, code: string) {
  const tr = d.translations?.[code] ?? {};
  const custom = PORTAL_STRINGS.filter((s) => (tr[s.key] ?? "").trim() !== "").length;
  const shipped = isShippedLanguage(code);
  return { shipped, custom, total: PORTAL_STRINGS.length, missing: shipped ? 0 : PORTAL_STRINGS.length - custom };
}

/** WCAG contrast between two CSS colours in #rgb, #rrggbb(aa) or rgb() form; null when either cannot be read
 *  (the designer then says nothing rather than guessing). */
export function contrastRatio(a?: string, b?: string): number | null {
  const la = luminanceOf(a);
  const lb = luminanceOf(b);
  if (la === null || lb === null) return null;
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

function luminanceOf(c?: string): number | null {
  const rgb = parseColour(c);
  if (!rgb) return null;
  const lin = rgb.map((v) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2];
}

function parseColour(c?: string): [number, number, number] | null {
  if (!c) return null;
  const v = c.trim();
  const rgb = v.match(/^rgb\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*\)$/i);
  if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3])];
  const hex = v.match(/^#([0-9a-f]{3,8})$/i);
  if (!hex) return null;
  let h = hex[1];
  if (h.length === 3 || h.length === 4) h = h.slice(0, 3).split("").map((x) => x + x).join("");
  if (h.length !== 6 && h.length !== 8) return null;
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
}

/** An appliance path is a GUEST-network path: /assets/<name> is served by portald on the guest side and
 *  resolves to nothing from the admin origin, which is why the old screen's own logo thumbnail was a broken
 *  image. The same file is read back here through the authenticated operator API. */
export function assetSrc(url?: string) {
  if (!url) return undefined;
  if (!url.startsWith("/assets/")) return url;
  return `/api/edge/v1/portal-assets/${encodeURIComponent(url.slice("/assets/".length))}/raw`;
}
