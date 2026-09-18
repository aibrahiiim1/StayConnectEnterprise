"use client";

// GUEST PORTAL SETTINGS.
//
// WHAT THIS REPLACES, AND WHY
// ---------------------------
// The screen before it was a revision-management tool wearing a settings page's name. It asked a hotel
// receptionist to hold in their head: a draft, a published design, a version number, which version was live,
// and a rollback. None of those are things a hotel has. A hotel has ONE guest portal, and one current
// configuration for it.
//
// So the vocabulary is now: change something, look at the preview, save. The immutable revision history is
// still written on every save — it is how a bad change is recovered and how the audit says who changed the
// page guests type their room number into — but it is not a concept the operator is asked to operate.
// Nothing was deleted to achieve that.
//
// The four sections are the four questions an operator actually arrives with: what is this hotel called, what
// does the page look like, what language does it speak, and the escape hatch for the one property in fifty
// that has a designer.

import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { api, ApiError, ListResp, PortalAsset, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";
import {
  Paintbrush, Building2, Languages as LanguagesIcon, Code2, Upload, Trash2, Check, AlertTriangle,
} from "lucide-react";
import { PortalPreview } from "./preview";
import {
  Design, PORTAL_STRINGS, STRING_GROUPS, SHIPPED_LANGUAGES,
  advancedChanged, assetSrc, knownLanguages, languageStatus, offeredLanguages,
} from "./strings";

type BrandingState = { design: Design; draft: Design };

/** What an unbranded appliance shows. The portal carries the same values; these mirror them so a colour well
 *  that has never been set shows what a guest is actually looking at rather than black. */
const DEFAULTS = {
  brand_color: "#0f6b63",
  brand_color_dark: "#0b544e",
  text_color: "#14302e",
  corner_radius: "20px",
} as const;

type Tab = "general" | "branding" | "languages" | "advanced";
const TABS: { id: Tab; label: string; icon: typeof Building2 }[] = [
  { id: "general", label: "General", icon: Building2 },
  { id: "branding", label: "Branding", icon: Paintbrush },
  { id: "languages", label: "Languages", icon: LanguagesIcon },
  { id: "advanced", label: "Advanced", icon: Code2 },
];

export default function PortalSettingsPage() {
  const [saved, setSaved] = useState<Design | null>(null);
  const [d, setD] = useState<Design>({});
  const [roles, setRoles] = useState<string[]>([]);
  const [tab, setTab] = useState<Tab>("general");
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [uploading, setUploading] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [assets, setAssets] = useState<PortalAsset[]>([]);
  const [editingLang, setEditingLang] = useState("en");
  const [newLangCode, setNewLangCode] = useState("");
  const [newLangLabel, setNewLangLabel] = useState("");

  const writable = canWrite("portal-branding", roles);
  const set = <K extends keyof Design>(k: K, v: Design[K]) => setD((p) => ({ ...p, [k]: v }));

  const load = useCallback(async () => {
    try {
      const s = await api.get<BrandingState>("/portal-branding");
      const live = s.design ?? {};
      setSaved(live);
      // An operator who was interrupted mid-edit finds their work, without ever being told the word "draft".
      setD(Object.keys(s.draft ?? {}).length ? s.draft : live);
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
    () => saved !== null && JSON.stringify(d) !== JSON.stringify(saved),
    [d, saved],
  );
  const needsPassword = useMemo(
    () => saved !== null && advancedChanged(d, saved),
    [d, saved],
  );

  // UNSAVED WORK SURVIVES A CLOSED TAB. Written quietly and never named: an operator should not have to learn
  // a second save verb to get the protection every document editor gives them for free.
  const settle = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    if (!dirty || !writable) return;
    if (settle.current) clearTimeout(settle.current);
    settle.current = setTimeout(() => {
      api.put("/portal-branding/draft", { design: d }).catch(() => { /* a lost keystroke cache is not an error to report */ });
    }, 1500);
    return () => { if (settle.current) clearTimeout(settle.current); };
  }, [d, dirty, writable]);

  // Leaving with work in progress is caught by the browser rather than by a modal of our own.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  async function save() {
    setBusy(true); setErr(null); setMsg(null);
    try {
      await api.post("/portal-branding/settings", { design: d, password: password || undefined });
      setPassword("");
      setMsg("Saved. Guests see these settings now.");
      await load();
    } catch (e) {
      // The step-up refusal is not a failure to explain away — it is the one case where the operator has
      // something specific to do, so it is said plainly rather than as a red line of API text.
      if (e instanceof ApiError && e.code === "reauth_required") setErr(e.message);
      else setErr(errMsg(e));
    } finally { setBusy(false); }
  }

  async function discard() {
    if (!saved) return;
    setD(saved); setPassword(""); setErr(null); setMsg(null);
    try { await api.put("/portal-branding/draft", { design: saved }); } catch { /* best effort */ }
  }

  async function upload(field: "logo_url" | "background_url", file: File) {
    setUploading(field); setErr(null); setMsg(null);
    try {
      // Through the API CLIENT, which knows where edged is. The hand-written URL that used to be here is the
      // whole reason both uploads failed — see api.upload.
      const a = await api.upload<PortalAsset>("/portal-assets", file);
      set(field, a.url);
      const list = await api.get<ListResp<PortalAsset>>("/portal-assets");
      setAssets(list.data ?? []);
      setMsg(`${field === "logo_url" ? "Logo" : "Background"} uploaded. Save changes to show it to guests.`);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "the image could not be uploaded");
    } finally { setUploading(null); }
  }

  async function removeUnusedAsset(name: string) {
    setErr(null);
    try {
      await api.del(`/portal-assets/${encodeURIComponent(name)}`);
      const list = await api.get<ListResp<PortalAsset>>("/portal-assets");
      setAssets(list.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }

  // ---- languages ----------------------------------------------------------------------------------------
  const known = useMemo(() => knownLanguages(d), [d]);
  const offeredCodes = useMemo(() => offeredLanguages(d).map((l) => l.code), [d]);
  const editing = known.find((l) => l.code === editingLang) ?? known[0];
  const editingState = languageStatus(d, editing?.code ?? "en");

  function setOffered(code: string, on: boolean) {
    setD((p) => {
      const list = offeredLanguages(p);
      const label = known.find((l) => l.code === code)?.label ?? code.toUpperCase();
      const next = on
        ? [...list.filter((l) => l.code !== code), { code, label }]
        : list.filter((l) => l.code !== code || code === "en"); // English is the fallback and stays
      const order = known.map((l) => l.code);
      next.sort((a, b) => order.indexOf(a.code) - order.indexOf(b.code));
      return { ...p, languages: next };
    });
  }

  function setTranslation(code: string, key: string, value: string) {
    setD((p) => ({
      ...p,
      translations: { ...(p.translations ?? {}), [code]: { ...((p.translations ?? {})[code] ?? {}), [key]: value } },
    }));
  }

  const preview = { ...DEFAULTS, ...d };
  const currentLogo = assetSrc(d.logo_url);
  const currentBackground = assetSrc(d.background_url);
  const unused = assets.filter((a) => !a.in_use && a.url !== d.logo_url && a.url !== d.background_url);

  if (!saved) {
    return <p className="p-6 text-sm text-muted-foreground">{err ?? "Loading portal settings…"}</p>;
  }

  return (
    <div className="mx-auto w-full max-w-7xl">
      {/* ---- header: what this is, and the only two actions there are --------------------------------- */}
      <header className="mb-5 flex flex-wrap items-end justify-between gap-3">
        <div>
          <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">Guest portal</div>
          <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight sm:text-2xl">
            <Paintbrush className="h-5 w-5" /> Portal settings
          </h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            How the Wi-Fi sign-in page looks and reads. Changes take effect for guests as soon as you save.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {dirty
            ? <span className="text-xs text-warning-subtle-foreground">Unsaved changes</span>
            : <span className="inline-flex items-center gap-1 text-xs text-muted-foreground"><Check className="h-3.5 w-3.5" /> All changes saved</span>}
          <Button variant="secondary" disabled={!dirty || busy} onClick={discard}>Discard</Button>
          <Button disabled={!dirty || busy || !writable} onClick={save}>
            {busy ? "Saving…" : "Save changes"}
          </Button>
        </div>
      </header>

      {/* The step-up is asked for ONLY when the change needs it, in the place the change is made, rather than
          as a password prompt on every save. */}
      {needsPassword && (
        <div className="mb-4 flex flex-wrap items-end gap-3 rounded-lg border border-warning/30 bg-warning-subtle p-3">
          <AlertTriangle className="h-4 w-4 text-warning-subtle-foreground" />
          <label className="block text-sm text-warning-subtle-foreground">
            You changed the portal&apos;s custom CSS or HTML. Confirm your password to save.
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)}
              aria-label="Confirm your password" className="mt-1 w-56" autoComplete="current-password" />
          </label>
        </div>
      )}

      {err && <div role="alert" className="mb-4 rounded-lg border border-destructive/25 bg-destructive-subtle p-3 text-sm text-destructive-subtle-foreground">{err}</div>}
      {msg && <div role="status" className="mb-4 rounded-lg border border-success/25 bg-success-subtle p-3 text-sm text-success-subtle-foreground">{msg}</div>}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,520px)]">
        <div className="min-w-0">
          {/* ---- section tabs ------------------------------------------------------------------------ */}
          <div className="mb-4 flex flex-wrap gap-1 border-b" role="tablist" aria-label="Settings sections">
            {TABS.map((t) => (
              <button
                key={t.id}
                role="tab"
                type="button"
                aria-selected={tab === t.id}
                onClick={() => setTab(t.id)}
                className={`-mb-px inline-flex items-center gap-2 border-b-2 px-4 py-2.5 text-sm ${
                  tab === t.id ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground"
                }`}
              >
                <t.icon className="h-4 w-4" /> {t.label}
              </button>
            ))}
          </div>

          {/* ---- GENERAL ----------------------------------------------------------------------------- */}
          {tab === "general" && (
            <Card>
              <CardHeader><CardTitle>Hotel identity</CardTitle></CardHeader>
              <CardBody className="space-y-5">
                <Field label="Hotel name" help="Shown at the top of the sign-in page and in the browser tab.">
                  {(a) => <Input {...a} value={d.hotel_name ?? ""} disabled={!writable}
                    onChange={(e) => set("hotel_name", e.target.value)} placeholder="Coral Sea Holiday Resort" />}
                </Field>
                <Field label="Welcome line" help="One short sentence under the hotel name. Leave empty to show nothing.">
                  {(a) => <Input {...a} value={d.welcome_text ?? ""} disabled={!writable}
                    onChange={(e) => set("welcome_text", e.target.value)}
                    placeholder="Welcome — connect to our Wi-Fi" />}
                </Field>
                <Field label="Help line" help="Shown at the foot of the card, for guests who cannot get on.">
                  {(a) => <Input {...a} value={d.help_text ?? ""} disabled={!writable}
                    onChange={(e) => set("help_text", e.target.value)}
                    placeholder="Ask reception if you need a code" />}
                </Field>
                <Field
                  label="Terms of use link"
                  help="Opens in a new tab. A guest reaching the portal has no internet yet, so link to something on this appliance or accept that an external page will not load until they are online."
                >
                  {(a) => <Input {...a} value={d.terms_url ?? ""} disabled={!writable}
                    onChange={(e) => set("terms_url", e.target.value)} placeholder="https://…/terms" />}
                </Field>
              </CardBody>
            </Card>
          )}

          {/* ---- BRANDING ---------------------------------------------------------------------------- */}
          {tab === "branding" && (
            <div className="space-y-5">
              <Card>
                <CardHeader><CardTitle>Images</CardTitle></CardHeader>
                <CardBody className="space-y-6">
                  <ImageField
                    label="Logo"
                    help="Shown at the top of the sign-in card, up to 60px tall."
                    src={currentLogo}
                    busy={uploading === "logo_url"}
                    writable={writable}
                    frame="bg-surface"
                    onPick={(f) => upload("logo_url", f)}
                    onClear={() => set("logo_url", "")}
                  />
                  <ImageField
                    label="Background photograph"
                    help="Fills the screen behind the sign-in card. A wide, uncluttered photograph works best — the card sits over the middle of it."
                    src={currentBackground}
                    busy={uploading === "background_url"}
                    writable={writable}
                    frame="bg-surface"
                    wide
                    onPick={(f) => upload("background_url", f)}
                    onClear={() => set("background_url", "")}
                  />
                  <p className="text-xs text-muted-foreground">
                    PNG, JPEG, WebP or GIF, up to 8&nbsp;MB. Images are stored on this appliance and served from
                    it, so they load for a guest who has no internet yet. SVG is refused: it can carry script,
                    and this page collects room numbers and voucher codes.
                  </p>

                  {unused.length > 0 && (
                    <div className="border-t pt-4">
                      <h3 className="text-sm font-medium">Previously uploaded</h3>
                      <p className="mb-2 text-xs text-muted-foreground">Not used by the current settings.</p>
                      <ul className="flex flex-wrap gap-3">
                        {unused.map((a) => (
                          <li key={a.name} className="flex items-center gap-2 rounded border p-2">
                            {/* eslint-disable-next-line @next/next/no-img-element */}
                            <img src={assetSrc(a.url)} alt="" className="h-10 w-16 rounded object-contain" />
                            <span className="text-xs text-muted-foreground">{Math.round(a.size_bytes / 1024)} KB</span>
                            <Button size="sm" variant="secondary" disabled={!writable}
                              onClick={() => removeUnusedAsset(a.name)} aria-label={`Delete ${a.name}`}>
                              <Trash2 className="h-3.5 w-3.5" />
                            </Button>
                          </li>
                        ))}
                      </ul>
                    </div>
                  )}
                </CardBody>
              </Card>

              <Card>
                <CardHeader><CardTitle>Colours and type</CardTitle></CardHeader>
                <CardBody className="grid gap-5 sm:grid-cols-2">
                  <ColorField label="Brand colour" help="Buttons, the selected tab and links."
                    value={d.brand_color} fallback={preview.brand_color} disabled={!writable}
                    onChange={(v) => set("brand_color", v)} />
                  <ColorField label="Button shade" help="The darker end of the button gradient."
                    value={d.brand_color_dark} fallback={preview.brand_color_dark} disabled={!writable}
                    onChange={(v) => set("brand_color_dark", v)} />
                  <ColorField label="Text colour" help="Headings and field labels."
                    value={d.text_color} fallback={preview.text_color} disabled={!writable}
                    onChange={(v) => set("text_color", v)} />
                  <Field label="Corner radius" help="How rounded the card and fields are, e.g. 20px.">
                    {(a) => <Input {...a} value={d.corner_radius ?? ""} disabled={!writable}
                      onChange={(e) => set("corner_radius", e.target.value)} placeholder={DEFAULTS.corner_radius} />}
                  </Field>
                  <div className="sm:col-span-2">
                    <Field label="Typeface" help="A font stack. Only fonts already on the guest's device will be used — the portal loads nothing from the internet.">
                      {(a) => <Input {...a} value={d.font_family ?? ""} disabled={!writable}
                        onChange={(e) => set("font_family", e.target.value)}
                        placeholder="Inter, system-ui, sans-serif" />}
                    </Field>
                  </div>
                </CardBody>
              </Card>
            </div>
          )}

          {/* ---- LANGUAGES --------------------------------------------------------------------------- */}
          {tab === "languages" && (
            <div className="space-y-5">
              <Card>
                <CardHeader><CardTitle>Shown to guests</CardTitle></CardHeader>
                <CardBody className="space-y-3">
                  <p className="text-sm text-muted-foreground">
                    The portal ships complete wording for {SHIPPED_LANGUAGES.length} languages — nothing to
                    translate, just tick the ones your guests should be offered. English is always available
                    and is what any missing wording falls back to.
                  </p>
                  <div className="flex flex-wrap gap-2">
                    {known.map((l) => {
                      const on = offeredCodes.includes(l.code);
                      const fixed = l.code === "en";
                      return (
                        <label key={l.code}
                          className={`inline-flex cursor-pointer items-center gap-2 rounded-full border px-3.5 py-2 text-sm ${
                            on ? "border-primary bg-primary/5 text-primary" : "text-muted-foreground"
                          } ${fixed ? "cursor-default" : ""}`}>
                          <input type="checkbox" className="h-4 w-4" checked={on}
                            disabled={fixed || !writable}
                            aria-label={`Offer ${l.label} to guests`}
                            onChange={(e) => setOffered(l.code, e.target.checked)} />
                          {l.label}
                        </label>
                      );
                    })}
                  </div>
                </CardBody>
              </Card>

              <Card>
                <CardHeader><CardTitle>Wording</CardTitle></CardHeader>
                <CardBody className="space-y-4">
                  <p className="text-sm text-muted-foreground">
                    Only change these if your property words something differently. Anything you leave empty
                    keeps the wording the portal ships.
                  </p>

                  {/* ONE LANGUAGE AT A TIME. Six of these stacked was roughly three hundred inputs down a
                      single column, with no way to see the state of any one of them. */}
                  <div className="flex flex-wrap gap-2" role="tablist" aria-label="Language being edited">
                    {known.map((l) => {
                      const st = languageStatus(d, l.code);
                      return (
                        <button key={l.code} type="button" role="tab"
                          aria-selected={editing?.code === l.code}
                          onClick={() => setEditingLang(l.code)}
                          className={`inline-flex items-center gap-2 rounded-md border px-3 py-2 text-sm ${
                            editing?.code === l.code ? "border-primary bg-primary/5 text-primary" : "text-muted-foreground"
                          }`}>
                          {l.label}
                          {st.missing > 0
                            ? <Badge tone="warn">{st.missing} in English</Badge>
                            : st.custom > 0
                              ? <Badge tone="neutral">{st.custom} changed</Badge>
                              : null}
                        </button>
                      );
                    })}
                  </div>

                  {editing && (
                    <div className="space-y-3 rounded-lg border p-4">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <strong className="text-sm">{editing.label}</strong>
                        <span className="text-xs text-muted-foreground">
                          {editingState.shipped
                            ? `Ships complete. ${editingState.custom} of ${editingState.total} changed by this hotel.`
                            : `Added by this hotel. ${editingState.missing} of ${editingState.total} strings will show English.`}
                        </span>
                      </div>

                      {STRING_GROUPS.map((g) => (
                        // A language the hotel ADDED has every string to fill in, so its groups start open.
                        // A shipped one needs almost nothing changed, so it opens on the first group only and
                        // does not present fifty fields to somebody who came to change one.
                        <details key={g} open={!editingState.shipped || g === STRING_GROUPS[0]}
                          className="rounded-md border">
                          <summary className="cursor-pointer px-3 py-2 text-sm font-medium">{g}</summary>
                          <div className="grid gap-3 p-3 pt-0 sm:grid-cols-2">
                            {PORTAL_STRINGS.filter((s) => s.group === g).map((st) => {
                              const v = d.translations?.[editing.code]?.[st.key] ?? "";
                              const fallsBack = !v && !editingState.shipped;
                              return (
                                <label key={st.key} className="block text-xs">
                                  <span className="flex items-center gap-1.5 text-muted-foreground">
                                    {st.english}
                                    {fallsBack && <Badge tone="warn">English</Badge>}
                                  </span>
                                  <Input className="mt-1" value={v} placeholder={st.english} disabled={!writable}
                                    dir={editing.code === "ar" ? "rtl" : undefined}
                                    aria-label={`${st.english} in ${editing.label}`}
                                    onChange={(e) => setTranslation(editing.code, st.key, e.target.value)} />
                                </label>
                              );
                            })}
                          </div>
                        </details>
                      ))}
                    </div>
                  )}

                  <div className="flex flex-wrap items-end gap-2 border-t pt-4">
                    <Field label="Add another language" help="A language the portal does not ship. It starts empty, so every string you do not fill in shows English.">
                      {(a) => <span className="flex gap-2">
                        <Input {...a} value={newLangCode} className="w-20" placeholder="es" disabled={!writable}
                          aria-label="Language code"
                          onChange={(e) => setNewLangCode(e.target.value.toLowerCase().slice(0, 5))} />
                        <Input value={newLangLabel} placeholder="Español" disabled={!writable}
                          aria-label="Shown as"
                          onChange={(e) => setNewLangLabel(e.target.value)} />
                        <Button variant="secondary" disabled={!newLangCode.trim() || !writable}
                          onClick={() => {
                            const code = newLangCode.trim();
                            setD((p) => ({
                              ...p,
                              translations: { ...(p.translations ?? {}), [code]: (p.translations ?? {})[code] ?? {} },
                              languages: [
                                ...offeredLanguages(p).filter((l) => l.code !== code),
                                { code, label: newLangLabel.trim() || code.toUpperCase() },
                              ],
                            }));
                            setEditingLang(code); setNewLangCode(""); setNewLangLabel("");
                          }}>
                          Add
                        </Button>
                      </span>}
                    </Field>
                  </div>
                </CardBody>
              </Card>
            </div>
          )}

          {/* ---- ADVANCED ---------------------------------------------------------------------------- */}
          {tab === "advanced" && (
            <Card>
              <CardHeader><CardTitle>Custom CSS and HTML</CardTitle></CardHeader>
              <CardBody className="space-y-4">
                {/* SAID BEFORE THEY TYPE, not after it is rejected. */}
                <p className="rounded-lg border border-warning/30 bg-warning-subtle p-3 text-sm text-warning-subtle-foreground">
                  This is the page guests type their room number, surname and voucher codes into. Styling and
                  markup are accepted; <strong>scripts, inline event handlers, frames, extra forms and
                  @import are refused</strong> — anything executable here could collect a guest&apos;s
                  credentials. A design containing them is rejected rather than quietly cleaned up, and saving
                  a change here asks for your password.
                </p>
                <Field label="Custom CSS" help="Added after the portal's own stylesheet, so it wins.">
                  {(a) => <textarea {...a} rows={10} value={d.custom_css ?? ""} disabled={!writable}
                    onChange={(e) => set("custom_css", e.target.value)}
                    className="w-full rounded-md border bg-card p-3 font-mono text-xs"
                    placeholder=".card { box-shadow: none; }" />}
                </Field>
                <Field label="Custom HTML" help="Inserted at the foot of the sign-in card.">
                  {(a) => <textarea {...a} rows={8} value={d.custom_html ?? ""} disabled={!writable}
                    onChange={(e) => set("custom_html", e.target.value)}
                    className="w-full rounded-md border bg-card p-3 font-mono text-xs"
                    placeholder="<p>Room service: dial 9</p>" />}
                </Field>
              </CardBody>
            </Card>
          )}
        </div>

        {/* ---- preview ------------------------------------------------------------------------------- */}
        <div className="lg:sticky lg:top-6 lg:self-start">
          <PortalPreview design={d} />
        </div>
      </div>
    </div>
  );
}

/** A labelled control with its explanation underneath rather than in a tooltip nobody opens.
 *
 *  THE HELP IS DESCRIBED, NOT LABELLED. Wrapping both in one <label> was the obvious spelling and it made the
 *  whole paragraph part of the control's accessible NAME: "Welcome line, one short sentence under the hotel
 *  name" is what a screen reader announced, and any two fields whose help mentioned the same words became
 *  indistinguishable to anyone navigating by label. The label names the control and aria-describedby carries
 *  the explanation, which is the division those two attributes exist for. */
function Field({ label, help, children }: {
  label: string;
  help?: string;
  children: (a: { id: string; "aria-describedby"?: string }) => React.ReactNode;
}) {
  const id = useId();
  const helpId = help ? id + "-help" : undefined;
  return (
    <div>
      <label htmlFor={id} className="block text-sm font-medium">{label}</label>
      {help && <p id={helpId} className="mb-1.5 text-xs text-muted-foreground">{help}</p>}
      {children({ id, "aria-describedby": helpId })}
    </div>
  );
}

function ColorField({ label, help, value, fallback, disabled, onChange }: {
  label: string; help: string; value?: string; fallback: string; disabled?: boolean;
  onChange: (v: string) => void;
}) {
  return (
    <Field label={label} help={help}>
      {(a) => (
        <span className="mt-1 flex items-center gap-2">
          <input {...a} type="color" value={value || fallback} disabled={disabled}
            onChange={(e) => onChange(e.target.value)} className="h-9 w-12 rounded border" />
          <Input value={value ?? ""} placeholder={fallback} disabled={disabled}
            aria-label={label + " as a hex value"}
            onChange={(e) => onChange(e.target.value)} />
        </span>
      )}
    </Field>
  );
}

/** Upload, replace, remove — and see what is actually set, which the old screen could not do: it pointed an
 *  <img> at /assets/<name>, a guest-network path that resolves to nothing from the admin origin. */
function ImageField({ label, help, src, busy, writable, wide, frame, onPick, onClear }: {
  label: string; help: string; src?: string; busy: boolean; writable: boolean; wide?: boolean;
  frame: string; onPick: (f: File) => void; onClear: () => void;
}) {
  return (
    <div>
      <span className="block text-sm font-medium">{label}</span>
      <span className="mb-2 block text-xs text-muted-foreground">{help}</span>
      <div className="flex flex-wrap items-center gap-3">
        <div className={`flex items-center justify-center overflow-hidden rounded-lg border ${frame} ${wide ? "h-24 w-44" : "h-20 w-32"}`}>
          {src
            // eslint-disable-next-line @next/next/no-img-element
            ? <img src={src} alt={`${label} currently set`} className="h-full w-full object-contain" />
            : <span className="px-2 text-center text-2xs text-muted-foreground">Nothing set — the portal uses its own default</span>}
        </div>
        <div className="flex flex-col gap-2">
          <label className={`inline-flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm ${!writable ? "opacity-50" : ""}`}>
            <Upload className="h-4 w-4" />
            {busy ? "Uploading…" : src ? `Replace ${label.toLowerCase()}` : `Upload ${label.toLowerCase()}`}
            <input type="file" className="hidden" disabled={!writable || busy}
              accept="image/png,image/jpeg,image/webp,image/gif"
              aria-label={`Upload ${label.toLowerCase()}`}
              onChange={(e) => { const f = e.target.files?.[0]; if (f) onPick(f); e.target.value = ""; }} />
          </label>
          {src && (
            <Button size="sm" variant="secondary" disabled={!writable} onClick={onClear}>
              Remove {label.toLowerCase()}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
