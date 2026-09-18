"use client";

// PORTAL BRANDING — a designer, not a textarea full of JSON.
//
// What was here asked the operator to hand-edit a raw JSON document with no schema, no validation, no
// history and no rollback. It also had no effect: the captive portal never fetched branding, so whatever was
// typed changed nothing a guest saw. It was write-only configuration behind a syntax check.
//
// This screen edits a DESIGN. It shows what the design will look like while it is being edited, publishes it
// under a password step-up, keeps versioned revisions and rolls back to one. The JSON underneath is an
// implementation detail the operator never meets.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ListResp, PortalAsset, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { canWrite } from "@/lib/roles";
import { errMsg, formatDate } from "@/lib/utils";
import { Paintbrush, Eye, History, Code2, Upload, Trash2, Languages } from "lucide-react";

type Design = {
  hotel_name?: string;
  logo_url?: string;
  background_url?: string;
  brand_color?: string;
  brand_color_dark?: string;
  text_color?: string;
  font_family?: string;
  corner_radius?: string;
  welcome_text?: string;
  help_text?: string;
  terms_url?: string;
  /** code -> { key: text }. What makes the language selector do something. */
  translations?: Record<string, Record<string, string>>;
  languages?: { code: string; label: string }[];
  custom_css?: string;
  custom_html?: string;
};

type Revision = { version: number; published_at: string; published_by?: string; note?: string };
type BrandingState = { design: Design; draft: Design; revisions: Revision[]; published: boolean };

/** What an unbranded appliance shows. The portal carries the same defaults; these mirror them so the preview
 *  is honest about what a guest would actually see before anything is published. */
/** The six languages the portal ships words for. Codes, order and native names match LANGS in
 *  data-plane/cmd/portald/templates.go. A hotel may still add a seventh of its own. */
const SHIPPED_LANGUAGES: { code: string; label: string }[] = [
  { code: "en", label: "English" },
  { code: "ar", label: "العربية" },
  { code: "de", label: "Deutsch" },
  { code: "fr", label: "Français" },
  { code: "it", label: "Italiano" },
  { code: "ru", label: "Русский" },
];

/** The guest-facing strings the portal tags for translation, grouped the way they appear on the page rather
 *  than as one undifferentiated list. Keys must match data-i18n / BUILTIN in the portal template: if they
 *  drift, the operator types words that never appear. A test in cmd/portald holds the two lists together. */
const PORTAL_STRINGS: { group: string; key: string; english: string }[] = [
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

  { group: "Notices", key: "notice.nomethods", english: "There is no way to sign in on this network yet. Please contact reception." },
  { group: "Notices", key: "notice.nopackages", english: "Internet access is not available here at the moment. You can still sign in, but there is nothing to connect you to yet — please let reception know." },
  { group: "Notices", key: "err.generic", english: "We could not verify your stay. Please check your details or contact reception." },
  { group: "Notices", key: "err.retry", english: "You can try again now." },
  { group: "Notices", key: "lang.label", english: "Language" },
];

const STRING_GROUPS = Array.from(new Set(PORTAL_STRINGS.map((s) => s.group)));

const DEFAULTS: Required<Pick<Design, "brand_color" | "brand_color_dark" | "text_color" | "corner_radius">> = {
  brand_color: "#0f6b63",
  brand_color_dark: "#0b544e",
  text_color: "#1c2b2a",
  corner_radius: "18px",
};

export default function PortalBrandingPage() {
  const [state, setState] = useState<BrandingState | null>(null);
  const [d, setD] = useState<Design>({});
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [password, setPassword] = useState("");
  const [note, setNote] = useState("");
  const [rollbackTo, setRollbackTo] = useState<number | null>(null);
  const [assets, setAssets] = useState<PortalAsset[]>([]);
  const [uploading, setUploading] = useState<string | null>(null);
  const [langCode, setLangCode] = useState("");
  const [langLabel, setLangLabel] = useState("");
  /** Which language the string editor is showing. One at a time — see the Languages card. */
  const [editing, setEditing] = useState("en");

  const writable = canWrite("portal-branding", roles);
  const set = <K extends keyof Design>(k: K, v: Design[K]) => setD((p) => ({ ...p, [k]: v }));

  const load = useCallback(async () => {
    try {
      const s = await api.get<BrandingState>("/portal-branding");
      setState(s);
      // An operator returning to an unfinished design should find it, not a blank form.
      setD(Object.keys(s.draft ?? {}).length ? s.draft : (s.design ?? {}));
      try {
        const a = await api.get<ListResp<PortalAsset>>("/portal-assets");
        setAssets(a.data ?? []);
      } catch { setAssets([]); }
    } catch (e) { setErr(errMsg(e)); }
  }, []);

  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, [load]);

  const dirty = useMemo(
    () => JSON.stringify(d) !== JSON.stringify(state?.design ?? {}),
    [d, state],
  );

  async function saveDraft() {
    setBusy(true); setErr(null); setMsg(null);
    try {
      await api.put("/portal-branding/draft", { design: d });
      setMsg("Draft saved. Guests still see the published design.");
      await load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function publish() {
    setBusy(true); setErr(null); setMsg(null);
    try {
      const r = await api.post<{ version: number }>("/portal-branding/publish", { design: d, note, password });
      setMsg(`Published as version ${r.version}. Guests see it now.`);
      setPassword(""); setNote(""); setPublishing(false);
      await load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function doRollback(v: number) {
    setBusy(true); setErr(null); setMsg(null);
    try {
      const r = await api.post<{ version: number; restored_from: number }>(
        `/portal-branding/rollback/${v}`, { password },
      );
      setMsg(`Rolled back to version ${r.restored_from}, published as version ${r.version}.`);
      setPassword(""); setRollbackTo(null);
      await load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  /** Upload goes straight to the appliance and the field is pointed at the result -- an operator should not
   *  have to copy a URL from one place to another. */
  async function upload(field: "logo_url" | "background_url", file: File) {
    setUploading(field); setErr(null); setMsg(null);
    try {
      const form = new FormData();
      form.append("file", file);
      const res = await fetch("/edge/v1/portal-assets", { method: "POST", body: form, credentials: "same-origin" });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body?.message || "the image was refused");
      set(field, body.url);
      const a = await api.get<ListResp<PortalAsset>>("/portal-assets");
      setAssets(a.data ?? []);
      setMsg("Image uploaded and applied to the design. Publish to show it to guests.");
    } catch (e: any) { setErr(e?.message ?? "the image could not be uploaded"); }
    finally { setUploading(null); }
  }

  async function removeAsset(name: string) {
    setErr(null);
    try {
      await api.del(`/portal-assets/${encodeURIComponent(name)}`);
      const a = await api.get<ListResp<PortalAsset>>("/portal-assets");
      setAssets(a.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }

  // ---- languages ---------------------------------------------------------------------------------------
  //
  // Two different lists that used to be one, which is what made the old selector offer languages nobody had
  // chosen: WHICH languages a guest is offered, and WHICH languages this hotel has written its own words for.
  // Writing an Italian greeting is not a decision to offer Italian.

  /** Every language this appliance knows about: the six that ship, plus anything the hotel added. */
  const offeredList = useMemo(() => {
    const seen = new Map<string, { code: string; label: string }>();
    SHIPPED_LANGUAGES.forEach((l) => seen.set(l.code, l));
    (d.languages ?? []).forEach((l) => { if (!seen.has(l.code)) seen.set(l.code, l); });
    Object.keys(d.translations ?? {}).forEach((c) => {
      if (!seen.has(c)) seen.set(c, { code: c, label: c.toUpperCase() });
    });
    return Array.from(seen.values());
  }, [d.languages, d.translations]);

  /** An unset list means all six — the same rule the portal applies, so the screen and the page agree. */
  const currentOffered = (p: Design) => (p.languages?.length ? p.languages : SHIPPED_LANGUAGES);
  const offered = useMemo(() => currentOffered(d).map((l) => l.code), [d]);

  function setOffered(code: string, on: boolean) {
    setD((p) => {
      const list = currentOffered(p);
      const label = offeredList.find((l) => l.code === code)?.label ?? code.toUpperCase();
      const next = on
        ? [...list.filter((l) => l.code !== code), { code, label }]
        : list.filter((l) => l.code !== code || code === "en"); // English is not removable
      // Keep the shipped order, so the guest's selector does not reshuffle as this screen is edited.
      const order = offeredList.map((l) => l.code);
      next.sort((a, b) => order.indexOf(a.code) - order.indexOf(b.code));
      return { ...p, languages: next };
    });
  }

  const isShipped = (code: string) => SHIPPED_LANGUAGES.some((l) => l.code === code);

  /** What the badge on each language tab counts. A shipped language is never "missing" a string — the portal
   *  has a word for it — so only a language this hotel added can fall back to English. */
  function statusOf(code: string) {
    const tr = d.translations?.[code] ?? {};
    const custom = PORTAL_STRINGS.filter((s) => (tr[s.key] ?? "").trim() !== "").length;
    const shipped = isShipped(code);
    return { shipped, custom, missing: shipped ? 0 : PORTAL_STRINGS.length - custom };
  }

  const editingMeta = offeredList.find((l) => l.code === editing) ?? offeredList[0] ?? null;
  const editingStatus = statusOf(editingMeta?.code ?? "en");

  const preview = { ...DEFAULTS, ...d };

  return (
    <div className="mx-auto w-full max-w-7xl space-y-5">
      <div className="mb-4">
        <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">Guest portal</div>
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight sm:text-2xl">
          <Paintbrush className="h-5 w-5" /> Branding
        </h1>
      </div>

      {err && <div role="alert" className="text-sm text-destructive">{err}</div>}
      {msg && <div role="status" className="text-sm text-success-subtle-foreground">{msg}</div>}

      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,420px)]">
        {/* ------------------------------------------------------------------ the design */}
        <div className="space-y-5">
          <Card>
            <CardHeader><CardTitle>Hotel</CardTitle></CardHeader>
            <CardBody className="space-y-4">
              <label className="block text-sm">
                Hotel name
                <Input value={d.hotel_name ?? ""} onChange={(e) => set("hotel_name", e.target.value)}
                  placeholder="Coral Sea Holiday Resort" />
              </label>
              {(["logo_url", "background_url"] as const).map((field) => (
                <div key={field} className="space-y-2 text-sm">
                  <span className="block">{field === "logo_url" ? "Logo" : "Background photograph"}</span>
                  {d[field] ? (
                    <div className="flex items-center gap-3">
                      {/* eslint-disable-next-line @next/next/no-img-element */}
                      <img src={d[field]} alt="" className="h-12 w-20 rounded border object-contain" />
                      <code className="text-xs text-muted">{d[field]}</code>
                      <Button size="sm" variant="secondary" onClick={() => set(field, "")}>Remove</Button>
                    </div>
                  ) : (
                    <p className="text-xs text-muted">Nothing set — the portal uses its own default.</p>
                  )}
                  <label className="inline-flex cursor-pointer items-center gap-2 rounded border px-3 py-1.5">
                    <Upload className="h-4 w-4" />
                    {uploading === field ? "Uploading…" : "Upload image"}
                    <input
                      type="file"
                      accept="image/png,image/jpeg,image/webp,image/gif"
                      className="hidden"
                      aria-label={field === "logo_url" ? "Upload logo" : "Upload background photograph"}
                      onChange={(e) => { const f = e.target.files?.[0]; if (f) upload(field, f); e.target.value = ""; }}
                    />
                  </label>
                  <Input value={d[field] ?? ""} onChange={(e) => set(field, e.target.value)}
                    placeholder="or paste an https address" />
                  {/* Stored on the appliance and served from the same origin as the sign-in page. A guest
                      reaches the portal precisely because they have no internet yet, so an image hosted
                      anywhere else is one that fails exactly when it matters. */}
                  <span className="block text-xs text-muted">
                    PNG, JPEG, WebP or GIF, up to 8 MB. SVG is refused: it can carry script, and this image is
                    served to every guest device before sign-in.
                  </span>
                </div>
              ))}
            </CardBody>
          </Card>

          <Card>
            <CardHeader><CardTitle>Appearance</CardTitle></CardHeader>
            <CardBody className="grid gap-4 sm:grid-cols-2">
              <label className="block text-sm">
                Brand colour
                <span className="mt-1 flex items-center gap-2">
                  <input type="color" aria-label="Brand colour"
                    value={preview.brand_color}
                    onChange={(e) => set("brand_color", e.target.value)}
                    className="h-9 w-12 rounded border" />
                  <Input value={d.brand_color ?? ""} onChange={(e) => set("brand_color", e.target.value)}
                    placeholder={DEFAULTS.brand_color} />
                </span>
              </label>
              <label className="block text-sm">
                Button shade
                <span className="mt-1 flex items-center gap-2">
                  <input type="color" aria-label="Button shade"
                    value={preview.brand_color_dark}
                    onChange={(e) => set("brand_color_dark", e.target.value)}
                    className="h-9 w-12 rounded border" />
                  <Input value={d.brand_color_dark ?? ""} onChange={(e) => set("brand_color_dark", e.target.value)}
                    placeholder={DEFAULTS.brand_color_dark} />
                </span>
              </label>
              <label className="block text-sm">
                Text colour
                <Input value={d.text_color ?? ""} onChange={(e) => set("text_color", e.target.value)}
                  placeholder={DEFAULTS.text_color} />
              </label>
              <label className="block text-sm">
                Corner radius
                <Input value={d.corner_radius ?? ""} onChange={(e) => set("corner_radius", e.target.value)}
                  placeholder={DEFAULTS.corner_radius} />
              </label>
              <label className="block text-sm sm:col-span-2">
                Typeface
                <Input value={d.font_family ?? ""} onChange={(e) => set("font_family", e.target.value)}
                  placeholder="Inter, system-ui, sans-serif" />
              </label>
            </CardBody>
          </Card>

          <Card>
            <CardHeader><CardTitle>Words</CardTitle></CardHeader>
            <CardBody className="space-y-4">
              <label className="block text-sm">
                Welcome line
                <Input value={d.welcome_text ?? ""} onChange={(e) => set("welcome_text", e.target.value)}
                  placeholder="Welcome — connect to our Wi-Fi" />
              </label>
              <label className="block text-sm">
                Help text
                <Input value={d.help_text ?? ""} onChange={(e) => set("help_text", e.target.value)}
                  placeholder="Ask reception if you need a code" />
              </label>
              <label className="block text-sm">
                Terms link
                <Input value={d.terms_url ?? ""} onChange={(e) => set("terms_url", e.target.value)}
                  placeholder="https://…/terms" />
              </label>
            </CardBody>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2"><Languages className="h-4 w-4" /> Languages</CardTitle>
            </CardHeader>
            <CardBody className="space-y-4">
              {/* SIX LANGUAGES AT ONCE IS A SCROLL, NOT A SCREEN.
                  This used to render every language as its own block, one under another, each with the full
                  string list inside it — six languages meant roughly three hundred inputs stacked vertically
                  and no way to see what any one language was missing. It is now one language at a time: pick
                  it, see its state, edit it.
                  It is also no longer a data-entry job. The portal SHIPS words for all six, so offering a
                  guest Arabic costs one tick; the fields below exist for a property that wants its own
                  wording, not for one that has to supply the basics. */}
              <p className="text-sm text-muted">
                The portal ships complete wording for {SHIPPED_LANGUAGES.length} languages. Tick the ones your
                guests should see, and edit any string you want to say differently. Anything you leave blank
                uses the shipped wording, and anything neither you nor the portal has a word for falls back to
                English — a half-translated portal still reads.
              </p>

              {/* WHICH LANGUAGES A GUEST IS OFFERED. Separate from which ones have overrides: an operator who
                  writes an Italian greeting has not decided that Italian is offered, and the two being the
                  same setting is what produced a selector full of languages nobody chose. */}
              <fieldset className="space-y-2">
                <legend className="text-sm font-medium">Offered to guests</legend>
                <div className="flex flex-wrap gap-2">
                  {offeredList.map((l) => {
                    const on = offered.includes(l.code);
                    const fixed = l.code === "en";
                    return (
                      <label
                        key={l.code}
                        className={`inline-flex cursor-pointer items-center gap-2 rounded-full border px-3 py-1.5 text-sm ${
                          on ? "border-primary text-primary" : "text-muted-foreground"
                        } ${fixed ? "cursor-default opacity-80" : ""}`}
                      >
                        <input
                          type="checkbox"
                          className="h-4 w-4"
                          aria-label={`Offer ${l.label} to guests`}
                          checked={on}
                          disabled={fixed || !writable}
                          onChange={(e) => setOffered(l.code, e.target.checked)}
                        />
                        {l.label} <span className="text-2xs text-muted">({l.code})</span>
                      </label>
                    );
                  })}
                </div>
                <p className="text-xs text-muted">
                  English is always offered; it is what the portal falls back to. A language with no words
                  behind it is never shown to a guest, whatever is ticked here.
                </p>
              </fieldset>

              {/* Adding a seventh language the portal does not ship. It arrives with nothing, so every string
                  it leaves blank shows English — which is exactly what the counter below says. */}
              <div className="flex flex-wrap items-end gap-2 border-t pt-4">
                <label className="block text-sm">
                  Language code
                  <Input value={langCode} onChange={(e) => setLangCode(e.target.value.toLowerCase().slice(0, 5))}
                    placeholder="es" className="w-24" />
                </label>
                <label className="block text-sm">
                  Shown as
                  <Input value={langLabel} onChange={(e) => setLangLabel(e.target.value)} placeholder="Español" />
                </label>
                <Button
                  type="button"
                  variant="secondary"
                  disabled={!langCode.trim() || !writable}
                  onClick={() => {
                    const code = langCode.trim();
                    setD((p) => ({
                      ...p,
                      translations: { ...(p.translations ?? {}), [code]: (p.translations ?? {})[code] ?? {} },
                      languages: [
                        ...currentOffered(p).filter((l) => l.code !== code),
                        { code, label: langLabel.trim() || code.toUpperCase() },
                      ],
                    }));
                    setEditing(code);
                    setLangCode(""); setLangLabel("");
                  }}
                >
                  Add language
                </Button>
              </div>

              {/* ---- the language being edited ------------------------------------------------ */}
              <div className="space-y-3 border-t pt-4">
                <div className="flex flex-wrap items-center gap-2" role="tablist" aria-label="Language being edited">
                  {offeredList.map((l) => {
                    const st = statusOf(l.code);
                    return (
                      <button
                        key={l.code}
                        type="button"
                        role="tab"
                        aria-selected={editing === l.code}
                        onClick={() => setEditing(l.code)}
                        className={`inline-flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm ${
                          editing === l.code ? "border-primary bg-primary/5 text-primary" : "text-muted-foreground"
                        }`}
                      >
                        {l.label}
                        {/* MISSING IS COUNTED, NOT GUESSED AT. A shipped language cannot be missing anything;
                            a language the hotel added itself shows English for every string it has not been
                            given, and the badge is that number. */}
                        {st.missing > 0 ? (
                          <Badge tone="warn">{st.missing} in English</Badge>
                        ) : st.custom > 0 ? (
                          <Badge tone="neutral">{st.custom} edited</Badge>
                        ) : null}
                      </button>
                    );
                  })}
                </div>

                {editingMeta && (
                  <div className="space-y-3 rounded border p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <strong className="text-sm">
                        {editingMeta.label} <span className="text-muted">({editingMeta.code})</span>
                      </strong>
                      <span className="flex items-center gap-2">
                        {editingStatus.shipped ? (
                          <span className="text-xs text-muted">
                            Ships with the portal. Leave a field empty to use its wording.
                          </span>
                        ) : (
                          <span className="text-xs text-muted">
                            Added by this hotel. Empty fields show English.
                          </span>
                        )}
                        {!editingStatus.shipped && (
                          <Button size="sm" variant="secondary" disabled={!writable} onClick={() => setD((p) => {
                            const tr = { ...(p.translations ?? {}) }; delete tr[editingMeta.code];
                            return {
                              ...p,
                              translations: tr,
                              languages: currentOffered(p).filter((l) => l.code !== editingMeta.code),
                            };
                          })}>
                            Remove
                          </Button>
                        )}
                      </span>
                    </div>

                    {editingMeta.code === "en" ? (
                      <p className="text-sm text-muted">
                        English is the portal&apos;s own wording and the fallback for every other language.
                        Change a string here and it changes for guests reading English and for every language
                        that has not been given its own word for it.
                      </p>
                    ) : null}

                    {STRING_GROUPS.map((g) => (
                      <details key={g} open className="rounded border">
                        <summary className="cursor-pointer px-3 py-2 text-sm font-medium">{g}</summary>
                        <div className="grid gap-2 p-3 pt-0 sm:grid-cols-2">
                          {PORTAL_STRINGS.filter((s) => s.group === g).map((st) => {
                            const v = d.translations?.[editingMeta.code]?.[st.key] ?? "";
                            const usesEnglish = !v && !editingStatus.shipped;
                            return (
                              <label key={st.key} className="block text-xs">
                                <span className="flex items-center gap-1.5">
                                  {st.english}
                                  {usesEnglish && <span className="text-2xs text-warning-subtle-foreground">English</span>}
                                </span>
                                <Input
                                  value={v}
                                  placeholder={st.english}
                                  disabled={!writable}
                                  aria-label={`${st.english} in ${editingMeta.label}`}
                                  onChange={(e) => setD((p) => ({
                                    ...p,
                                    translations: {
                                      ...(p.translations ?? {}),
                                      [editingMeta.code]: {
                                        ...((p.translations ?? {})[editingMeta.code] ?? {}),
                                        [st.key]: e.target.value,
                                      },
                                    },
                                  }))}
                                />
                              </label>
                            );
                          })}
                        </div>
                      </details>
                    ))}
                  </div>
                )}
              </div>
            </CardBody>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2"><Code2 className="h-4 w-4" /> Advanced</CardTitle>
            </CardHeader>
            <CardBody className="space-y-3">
              {!advanced ? (
                <Button variant="secondary" onClick={() => setAdvanced(true)}>Custom CSS and HTML</Button>
              ) : (
                <>
                  {/* SAID BEFORE THEY TYPE, not after it is rejected. */}
                  <p className="text-sm">
                    This is the page guests type their room number, surname and voucher codes into. Styling and
                    markup are accepted; <strong>scripts, inline event handlers, frames, extra forms and
                    @import are refused</strong> — anything executable here could collect a guest&apos;s
                    credentials. A design containing them is rejected rather than quietly cleaned up.
                  </p>
                  <label className="block text-sm">
                    Custom CSS
                    <textarea
                      className="mt-1 h-40 w-full rounded border bg-panel p-2 font-mono text-xs"
                      value={d.custom_css ?? ""} onChange={(e) => set("custom_css", e.target.value)}
                      placeholder=".card { box-shadow: 0 10px 40px rgba(0,0,0,.2); }" />
                  </label>
                  <label className="block text-sm">
                    Custom HTML
                    <textarea
                      className="mt-1 h-32 w-full rounded border bg-panel p-2 font-mono text-xs"
                      value={d.custom_html ?? ""} onChange={(e) => set("custom_html", e.target.value)}
                      placeholder="<p class=&quot;small&quot;>Ask reception for help on extension 9.</p>" />
                  </label>
                </>
              )}
            </CardBody>
          </Card>
        </div>

        {/* ------------------------------------------------------------------ preview + publish */}
        <div className="space-y-5">
          <Card>
            <CardHeader><CardTitle className="flex items-center gap-2"><Eye className="h-4 w-4" /> Preview</CardTitle></CardHeader>
            <CardBody>
              {/* A REPRESENTATION, and it says so. It shows the design decisions -- colours, logo, name,
                  shape -- against the real portal layout. It is not the portal itself, and claiming it were
                  would be the more expensive lie. */}
              <div
                aria-label="Portal preview"
                className="overflow-hidden rounded border"
                style={{
                  background: d.background_url
                    ? `center/cover no-repeat url("${d.background_url}")`
                    : "linear-gradient(160deg,#cfe3e6,#eef3f2)",
                  padding: 16,
                }}
              >
                <div
                  style={{
                    background: "#fff",
                    borderRadius: preview.corner_radius,
                    padding: 16,
                    color: preview.text_color,
                    fontFamily: d.font_family || "inherit",
                  }}
                >
                  <div className="flex items-center gap-2" style={{ minHeight: 28 }}>
                    {d.logo_url
                      // eslint-disable-next-line @next/next/no-img-element
                      ? <img src={d.logo_url} alt="" style={{ maxHeight: 28, maxWidth: 140, objectFit: "contain" }} />
                      : <span className="text-xs text-muted">{d.hotel_name || "Your hotel"}</span>}
                  </div>
                  <hr className="my-3" />
                  <div className="flex gap-4 text-xs">
                    <span style={{ color: preview.brand_color, borderBottom: `2px solid ${preview.brand_color}`, paddingBottom: 6 }}>
                      Guest Login
                    </span>
                    <span className="text-muted" style={{ paddingBottom: 6 }}>Account Login</span>
                  </div>
                  <div className="mt-3 space-y-2">
                    <div className="text-xs">Room Number</div>
                    <div className="h-7 rounded border" />
                    <div className="text-xs">Password</div>
                    <div className="h-7 rounded border" />
                    <button
                      type="button"
                      style={{
                        background: `linear-gradient(180deg, ${preview.brand_color}, ${preview.brand_color_dark})`,
                        color: "#fff", border: 0, borderRadius: 8, padding: "6px 18px", fontSize: 12,
                      }}
                    >
                      Submit
                    </button>
                  </div>
                </div>
              </div>
              <p className="mt-2 text-xs text-muted">
                A representation of the published design, not a live copy of the portal.
              </p>
            </CardBody>
          </Card>

          <Card>
            <CardHeader><CardTitle>Publish</CardTitle></CardHeader>
            <CardBody className="space-y-3">
              <p className="text-sm text-muted">
                {dirty
                  ? "This design differs from what guests currently see."
                  : "This design is what guests currently see."}
              </p>
              {!publishing ? (
                <div className="flex flex-wrap gap-2">
                  <Button disabled={!writable || busy || !dirty} onClick={() => setPublishing(true)}>
                    Publish to guests
                  </Button>
                  <Button variant="secondary" disabled={!writable || busy} onClick={saveDraft}>
                    Save draft
                  </Button>
                </div>
              ) : (
                <div className="space-y-3">
                  <label className="block text-sm">
                    What changed
                    <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="New summer photography" />
                  </label>
                  <label className="block text-sm">
                    Confirm your password
                    <Input type="password" autoComplete="current-password"
                      value={password} onChange={(e) => setPassword(e.target.value)} />
                  </label>
                  <div className="flex gap-2">
                    <Button disabled={busy} onClick={publish}>{busy ? "Publishing…" : "Confirm and publish"}</Button>
                    <Button variant="secondary" disabled={busy} onClick={() => { setPublishing(false); setPassword(""); }}>
                      Cancel
                    </Button>
                  </div>
                </div>
              )}
              {!writable && <p className="text-sm text-muted">Your role can view branding but not change it.</p>}
            </CardBody>
          </Card>

          <Card>
            <CardHeader><CardTitle className="flex items-center gap-2"><History className="h-4 w-4" /> Published versions</CardTitle></CardHeader>
            <CardBody className="p-0">
              {!state?.revisions?.length ? (
                <p className="p-4 text-sm text-muted">No design has been published yet.</p>
              ) : (
                <ul className="divide-y" aria-label="Published design versions">
                  {state.revisions.map((r, i) => (
                    <li key={r.version} className="space-y-2 px-4 py-3 text-sm">
                      <div className="flex flex-wrap items-baseline gap-2">
                        <span className="font-medium">v{r.version}</span>
                        {i === 0 && <Badge tone="ok">live</Badge>}
                        <span className="text-muted">{formatDate(r.published_at)}</span>
                        {r.published_by && <span className="text-muted">· {r.published_by}</span>}
                      </div>
                      {r.note && <div className="text-muted">{r.note}</div>}
                      {i !== 0 && writable && (
                        rollbackTo === r.version ? (
                          <div className="space-y-2">
                            <Input type="password" autoComplete="current-password" placeholder="Confirm your password"
                              value={password} onChange={(e) => setPassword(e.target.value)} />
                            <div className="flex gap-2">
                              <Button size="sm" disabled={busy} onClick={() => doRollback(r.version)}>
                                Confirm rollback
                              </Button>
                              <Button size="sm" variant="secondary" disabled={busy}
                                onClick={() => { setRollbackTo(null); setPassword(""); }}>
                                Cancel
                              </Button>
                            </div>
                          </div>
                        ) : (
                          <Button size="sm" variant="secondary" onClick={() => setRollbackTo(r.version)}>
                            Roll back to this
                          </Button>
                        )
                      )}
                    </li>
                  ))}
                </ul>
              )}
              {/* Rolling back publishes the old design as a NEW version rather than deleting the newer one,
                  so the history says what actually happened. */}
              <p className="border-t p-3 text-xs text-muted">
                Rolling back re-publishes an earlier design as a new version. Nothing is removed from this
                list — it records what the hotel actually showed, and when.
              </p>
            </CardBody>
          </Card>
        </div>
      </div>
    </div>
  );
}
