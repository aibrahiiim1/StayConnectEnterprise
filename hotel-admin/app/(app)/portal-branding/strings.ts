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
  custom_css?: string;
  custom_html?: string;
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
  { group: "Tabs and navigation", key: "tab.guest", english: "Guest Login" },
  { group: "Tabs and navigation", key: "tab.account", english: "Account Login" },
  { group: "Tabs and navigation", key: "alt.title", english: "Or sign in with" },
  { group: "Tabs and navigation", key: "method.pms", english: "Room" },
  { group: "Tabs and navigation", key: "method.poststay", english: "Post-stay" },
  { group: "Tabs and navigation", key: "method.voucher", english: "Voucher" },
  { group: "Tabs and navigation", key: "method.account", english: "Personal account" },
  { group: "Tabs and navigation", key: "method.email", english: "Email" },
  { group: "Tabs and navigation", key: "method.sms", english: "Phone" },
  { group: "Tabs and navigation", key: "method.social", english: "Social" },

  { group: "Room sign-in", key: "pms.room", english: "Room Number" },
  { group: "Room sign-in", key: "pms.secondary", english: "Password" },
  { group: "Room sign-in", key: "pms.prompt.lastname", english: "Last name on the reservation" },
  { group: "Room sign-in", key: "pms.prompt.firstname", english: "First name on the reservation" },
  { group: "Room sign-in", key: "pms.prompt.reservation", english: "Reservation / confirmation number" },
  { group: "Room sign-in", key: "pms.prompt.any", english: "First name, last name, or reservation number" },
  { group: "Room sign-in", key: "pms.prompt.either", english: "Last name OR reservation number" },
  { group: "Room sign-in", key: "pms.choose", english: "Choose your internet package" },

  { group: "Voucher and account", key: "account.personal", english: "Use Personal Account" },
  { group: "Voucher and account", key: "voucher.label", english: "Voucher Code" },
  { group: "Voucher and account", key: "account.user", english: "Username" },
  { group: "Voucher and account", key: "account.pass", english: "Password" },

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

  { group: "Device information", key: "info.device", english: "Your device" },
  { group: "Device information", key: "info.ip", english: "IP address" },
  { group: "Device information", key: "info.mac", english: "MAC address" },
  { group: "Device information", key: "info.help", english: "Reception may ask for these if you need help connecting." },

  { group: "Notices and footer", key: "notice.nomethods", english: "There is no way to sign in on this network yet. Please contact reception." },
  { group: "Notices and footer", key: "notice.nopackages", english: "Internet access is not available here at the moment. You can still sign in, but there is nothing to connect you to yet — please let reception know." },
  { group: "Notices and footer", key: "err.generic", english: "We could not verify your stay. Please check your details or contact reception." },
  { group: "Notices and footer", key: "err.retry", english: "You can try again now." },
  { group: "Notices and footer", key: "lang.label", english: "Language" },
  { group: "Notices and footer", key: "terms.link", english: "Terms of use" },
];

export const STRING_GROUPS = Array.from(new Set(PORTAL_STRINGS.map((s) => s.group)));

/** The settings that are "advanced": the only ones that can put executable-shaped content on the page, and
 *  therefore the only ones whose change is confirmed with a password. Kept here so the screen and the
 *  server's own rule (advancedChanged, in resources_branding.go) are described by one list. */
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

/** An appliance path is a GUEST-network path: /assets/<name> is served by portald on the guest side and
 *  resolves to nothing from the admin origin, which is why the old screen's own logo thumbnail was a broken
 *  image. The same file is read back here through the authenticated operator API. */
export function assetSrc(url?: string) {
  if (!url) return undefined;
  if (!url.startsWith("/assets/")) return url;
  return `/api/edge/v1/portal-assets/${encodeURIComponent(url.slice("/assets/".length))}/raw`;
}
