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
import { api, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { canWrite } from "@/lib/roles";
import { errMsg, formatDate } from "@/lib/utils";
import { Paintbrush, Eye, History, Code2 } from "lucide-react";

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
  custom_css?: string;
  custom_html?: string;
};

type Revision = { version: number; published_at: string; published_by?: string; note?: string };
type BrandingState = { design: Design; draft: Design; revisions: Revision[]; published: boolean };

/** What an unbranded appliance shows. The portal carries the same defaults; these mirror them so the preview
 *  is honest about what a guest would actually see before anything is published. */
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

  const writable = canWrite("portal-branding", roles);
  const set = <K extends keyof Design>(k: K, v: Design[K]) => setD((p) => ({ ...p, [k]: v }));

  const load = useCallback(async () => {
    try {
      const s = await api.get<BrandingState>("/portal-branding");
      setState(s);
      // An operator returning to an unfinished design should find it, not a blank form.
      setD(Object.keys(s.draft ?? {}).length ? s.draft : (s.design ?? {}));
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
              <label className="block text-sm">
                Logo
                <Input value={d.logo_url ?? ""} onChange={(e) => set("logo_url", e.target.value)}
                  placeholder="/assets/logo.png or https://…" />
                <span className="mt-1 block text-xs text-muted">
                  An appliance path or an https address. Plain http is refused — a logo fetched over http can
                  be replaced by anyone on the network.
                </span>
              </label>
              <label className="block text-sm">
                Background photograph
                <Input value={d.background_url ?? ""} onChange={(e) => set("background_url", e.target.value)}
                  placeholder="/assets/portal-background.jpg" />
              </label>
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
