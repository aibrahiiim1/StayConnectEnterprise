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
/** The guest-facing strings the portal tags for translation. Keys must match data-i18n in the portal
 *  template: if they drift, the operator types words that never appear. */
const PORTAL_STRINGS: { key: string; english: string }[] = [
  { key: "tab.guest", english: "Guest Login" },
  { key: "tab.account", english: "Account Login" },
  { key: "pms.room", english: "Room number" },
  { key: "account.pass", english: "Password" },
  { key: "account.user", english: "Username" },
  { key: "voucher.label", english: "Voucher code" },
  { key: "email.dest", english: "Email address" },
  { key: "sms.dest", english: "Phone number" },
  { key: "otp.code", english: "Verification code" },
  { key: "btn.connect", english: "Connect" },
  { key: "btn.verify", english: "Verify" },
  { key: "info.device", english: "Your device" },
  { key: "info.ip", english: "IP address" },
  { key: "info.mac", english: "MAC address" },
  { key: "info.help", english: "Reception may ask for these if you need help connecting." },
];

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

  const langs = Object.keys(d.translations ?? {});

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
              {/* A SELECTOR THAT CHANGES NOTHING IS WORSE THAN NO SELECTOR: it tells a guest their language
                  is supported and then does not support it. The portal only offers a language once there are
                  words behind it, so this is where a language starts existing. */}
              <p className="text-sm text-muted">
                English is always offered and is the fallback for anything a translation leaves out — a
                half-translated portal still reads. Add a language and give it the words guests see.
              </p>

              <div className="flex flex-wrap items-end gap-2">
                <label className="block text-sm">
                  Language code
                  <Input value={langCode} onChange={(e) => setLangCode(e.target.value.toLowerCase().slice(0, 5))}
                    placeholder="ar" className="w-24" />
                </label>
                <label className="block text-sm">
                  Shown as
                  <Input value={langLabel} onChange={(e) => setLangLabel(e.target.value)} placeholder="العربية" />
                </label>
                <Button
                  type="button"
                  variant="secondary"
                  disabled={!langCode.trim()}
                  onClick={() => {
                    const code = langCode.trim();
                    setD((p) => ({
                      ...p,
                      translations: { ...(p.translations ?? {}), [code]: (p.translations ?? {})[code] ?? {} },
                      languages: [
                        ...(p.languages ?? [{ code: "en", label: "English" }]).filter((l) => l.code !== code),
                        { code, label: langLabel.trim() || code.toUpperCase() },
                      ],
                    }));
                    setLangCode(""); setLangLabel("");
                  }}
                >
                  Add language
                </Button>
              </div>

              {langs.length === 0 ? (
                <p className="text-sm text-muted">Only English is offered.</p>
              ) : (
                langs.map((code) => (
                  <div key={code} className="space-y-2 rounded border p-3">
                    <div className="flex items-center justify-between">
                      <strong className="text-sm">
                        {(d.languages ?? []).find((l) => l.code === code)?.label ?? code}{" "}
                        <span className="text-muted">({code})</span>
                      </strong>
                      <Button size="sm" variant="secondary" onClick={() => setD((p) => {
                        const t = { ...(p.translations ?? {}) }; delete t[code];
                        return { ...p, translations: t, languages: (p.languages ?? []).filter((l) => l.code !== code) };
                      })}>
                        Remove
                      </Button>
                    </div>
                    <div className="grid gap-2 sm:grid-cols-2">
                      {PORTAL_STRINGS.map((st) => (
                        <label key={st.key} className="block text-xs">
                          {st.english}
                          <Input
                            value={(d.translations?.[code]?.[st.key]) ?? ""}
                            placeholder={st.english}
                            onChange={(e) => setD((p) => ({
                              ...p,
                              translations: {
                                ...(p.translations ?? {}),
                                [code]: { ...((p.translations ?? {})[code] ?? {}), [st.key]: e.target.value },
                              },
                            }))}
                          />
                        </label>
                      ))}
                    </div>
                  </div>
                ))
              )}
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
