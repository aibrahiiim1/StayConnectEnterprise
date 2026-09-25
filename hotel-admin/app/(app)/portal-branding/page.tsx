"use client";

// GUEST PORTAL SETTINGS — A DESIGNER FOR THE WI-FI SIGN-IN PAGE.
//
// WHAT AN OPERATOR DOES HERE
// --------------------------
// Pick a layout from six real templates, put the hotel's name, colours and photographs on it, write the words
// guests read, choose the languages they are offered, and -- for the property with a designer -- add their own
// CSS and HTML. A large live preview renders the REAL portal page with the unsaved changes, at desktop, tablet
// and phone sizes. Then one button: Save changes. Guests see it immediately.
//
// WHAT STAYS THE PRODUCT OWNER'S DECISION
// ---------------------------------------
//   - One current configuration and one verb, "Save". The immutable history every save appends is shown as
//     History, with Restore -- never as drafts, publishing or versions an operator has to operate.
//   - Ordinary settings save with no password. Custom CSS and HTML are confirmed with the operator's password,
//     in BOTH directions -- adding, changing or clearing them (ADVANCED_KEYS / advancedChanged, mirrored on the
//     server). Choosing a template is not markup and asks for nothing.
//   - The welcome and help lines are the hotel's own words, one field each, not per-language translations.
//
// SAFETY IS THE SERVER'S. Every change is sent to /portal-branding/validate as it is made; the answer names
// anything the portal would refuse or change, beside the field it belongs to, and the preview renders the
// sanitised Advanced fields so what an operator sees is what a guest would get.

import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { api, ApiError, ListResp, PortalAsset, Whoami } from "@/lib/api";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Hint, Input, Label } from "@/components/ui/input";
import { Segmented } from "@/components/ui/tabs";
import { MetricStrip } from "@/components/ui/data";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { canWrite } from "@/lib/roles";
import { cn, errMsg } from "@/lib/utils";
import {
  Palette, LayoutTemplate, Paintbrush, Type, TextCursorInput, Languages as LanguagesIcon, Code2, History as HistoryIcon,
  Upload, Trash2, Check, AlertTriangle,
} from "lucide-react";
import {
  LIMITS, templateById, validateDesign, type BrandingState, type DesignIssue, type RevisionSummary,
  type TemplateId, type ValidateResp,
} from "@/lib/api/portal-design";
import { PortalPreview } from "./preview";
import { TemplateGallery } from "./template-gallery";
import { AdvancedSection } from "./advanced";
import { HistorySection } from "./history";
import { Design, TemplateOptions, advancedChanged, assetSrc, contrastRatio, offeredLanguages } from "./strings";
import { LanguagesSection } from "./languages";

/** What an unbranded appliance shows. The portal carries the same values; these mirror them so a colour well
 *  that has never been set shows what a guest is actually looking at rather than black. */
const DEFAULTS = {
  brand_color: "#0f6b63",
  brand_color_dark: "#0b544e",
  text_color: "#14302e",
  corner_radius: "20px",
  /** The sign-in card and the text on buttons. */
  card: "#ffffff",
} as const;

type Section = "template" | "brand" | "content" | "wording" | "languages" | "advanced" | "history";
const SECTIONS: { id: Section; label: string; icon: typeof Palette }[] = [
  { id: "template", label: "Template", icon: LayoutTemplate },
  { id: "brand", label: "Brand", icon: Paintbrush },
  { id: "content", label: "Content", icon: Type },
  { id: "wording", label: "Sign-in page text", icon: TextCursorInput },
  { id: "languages", label: "Languages", icon: LanguagesIcon },
  { id: "advanced", label: "Advanced HTML & CSS", icon: Code2 },
  { id: "history", label: "History", icon: HistoryIcon },
];

type ImageKey = "logo_url" | "background_url" | "hero_image_url";
const IMAGE_LABEL: Record<ImageKey, string> = {
  logo_url: "Logo", background_url: "Background", hero_image_url: "Hero photograph",
};

export default function PortalSettingsPage() {
  const toast = useToast();
  const [saved, setSaved] = useState<Design | null>(null);
  const [d, setD] = useState<Design>({});
  const [revisions, setRevisions] = useState<RevisionSummary[]>([]);
  const [roles, setRoles] = useState<string[]>([]);
  const [section, setSection] = useState<Section>("template");
  const [wordingLang, setWordingLang] = useState<string | undefined>(undefined);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [saveErr, setSaveErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [uploading, setUploading] = useState<ImageKey | null>(null);
  const [assets, setAssets] = useState<PortalAsset[]>([]);
  const [stepUp, setStepUp] = useState(false);
  const [stepUpErr, setStepUpErr] = useState<unknown>(null);
  const [validation, setValidation] = useState<ValidateResp | null>(null);
  const [checking, setChecking] = useState(false);

  const writable = canWrite("portal-branding", roles);
  const set = useCallback(<K extends keyof Design>(k: K, v: Design[K]) => setD((p) => ({ ...p, [k]: v })), []);
  const setOpt = <K extends keyof TemplateOptions>(k: K, v: TemplateOptions[K]) =>
    setD((p) => ({ ...p, template_options: { ...(p.template_options ?? {}), [k]: v } }));

  const load = useCallback(async () => {
    try {
      const s = await api.get<BrandingState<Design>>("/portal-branding");
      const live = s.design ?? {};
      setSaved(live);
      setRevisions(s.revisions ?? []);
      // An operator who was interrupted mid-edit finds their work, without ever being told the word "draft".
      setD(Object.keys(s.draft ?? {}).length ? s.draft : live);
      setLoadErr(null);
      try {
        const a = await api.get<ListResp<PortalAsset>>("/portal-assets");
        setAssets(a.data ?? []);
      } catch { setAssets([]); }
    } catch (e) { setLoadErr(e); }
  }, []);

  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, [load]);

  const dirty = useMemo(() => saved !== null && JSON.stringify(d) !== JSON.stringify(saved), [d, saved]);
  const needsPassword = useMemo(() => saved !== null && advancedChanged(d, saved), [d, saved]);

  // UNSAVED WORK SURVIVES A CLOSED TAB. Written quietly and never named: an operator should not have to learn
  // a second save verb to get the protection every document editor gives them for free.
  //
  // ONLY A DRAFT THE SERVER WILL ACCEPT IS SENT. The server validates drafts with the same rules as a save, so
  // a draft carrying refused markup (a <base> tag, an inline handler) comes back 400. Found on PRE-LIVE: the
  // autosave kept firing on its own timer and failing silently while the screen already showed the problem.
  // It now waits for the verdict on THIS exact design and sends it only when that verdict is clean -- or when no
  // verdict could be obtained, in which case the server decides as it always did.
  const settle = useRef<ReturnType<typeof setTimeout> | null>(null);
  const designKey = useMemo(() => JSON.stringify(d), [d]);
  const [verdictFor, setVerdictFor] = useState<{ key: string; clean: boolean | null } | null>(null);
  const draftBlocked = verdictFor?.key === designKey && verdictFor.clean === false;
  useEffect(() => {
    if (!dirty || !writable) return;
    if (!verdictFor || verdictFor.key !== designKey || verdictFor.clean === false) return;
    if (settle.current) clearTimeout(settle.current);
    settle.current = setTimeout(() => {
      api.put("/portal-branding/draft", { design: d }).catch(() => { /* a lost keystroke cache is not an error to report */ });
    }, 1500);
    return () => { if (settle.current) clearTimeout(settle.current); };
  }, [d, designKey, dirty, writable, verdictFor]);

  // THE SERVER'S VERDICT, AS THE OPERATOR TYPES. Debounced, and only for someone who can save: it is the same
  // rule set that will accept or refuse the save, so there is no second copy of it in this screen.
  useEffect(() => {
    if (saved === null || !writable) return;
    let live = true;
    setChecking(true);
    const key = JSON.stringify(d);
    const t = setTimeout(() => {
      validateDesign(d as Record<string, unknown>)
        .then((r) => {
          if (!live) return;
          const v = r && Array.isArray(r.issues) ? r : null;
          setValidation(v);
          setVerdictFor({ key, clean: v ? !v.issues.some((i) => i.severity === "error") : null });
        })
        .catch(() => { if (live) { setValidation(null); setVerdictFor({ key, clean: null }); } })
        .finally(() => { if (live) setChecking(false); });
    }, 450);
    return () => { live = false; clearTimeout(t); };
  }, [d, saved, writable]);

  // Leaving with work in progress is caught by the browser rather than by a modal of our own.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const issues: DesignIssue[] = validation?.issues ?? [];
  const errorsFor = (field: string) => issues.filter((i) => i.field === field && i.severity === "error");
  const fieldError = (field: string) => errorsFor(field).map((i) => i.message).join("; ") || undefined;
  const blocking = issues.filter((i) => i.severity === "error");

  async function save(password?: string) {
    // The step-up is asked for ONLY when the change needs it, at the moment of saving, in a masked field.
    if (needsPassword && password === undefined) {
      setStepUpErr(null);
      setStepUp(true);
      return;
    }
    setBusy(true); setSaveErr(null);
    try {
      await api.post("/portal-branding/settings", { design: d, password: password || undefined });
      setStepUp(false);
      toast.success("Saved", "Guests see these settings now.");
      await load();
    } catch (e) {
      if (e instanceof ApiError && e.code === "reauth_required") {
        // The one refusal with something specific to do: confirm the password.
        setStepUpErr(password === undefined ? null : "That password was not accepted.");
        setStepUp(true);
      } else {
        setStepUp(false);
        setSaveErr(e);
        toast.error("Not saved", errMsg(e));
      }
    } finally { setBusy(false); }
  }

  async function discard() {
    if (!saved) return;
    setD(saved); setSaveErr(null);
    try { await api.put("/portal-branding/draft", { design: saved }); } catch { /* best effort */ }
  }

  async function upload(field: ImageKey, file: File) {
    setUploading(field); setSaveErr(null);
    try {
      // Through the API CLIENT, which knows where edged is. A hand-written URL here is what once made both
      // uploads fail -- see api.upload.
      const a = await api.upload<PortalAsset>("/portal-assets", file);
      set(field, a.url);
      const list = await api.get<ListResp<PortalAsset>>("/portal-assets");
      setAssets(list.data ?? []);
      toast.success(`${IMAGE_LABEL[field]} uploaded`, "Save changes to show it to guests.");
    } catch (e) {
      toast.error("Upload failed", e instanceof ApiError ? e.message : "the image could not be uploaded");
    } finally { setUploading(null); }
  }

  async function removeUnusedAsset(name: string) {
    try {
      await api.del(`/portal-assets/${encodeURIComponent(name)}`);
      const list = await api.get<ListResp<PortalAsset>>("/portal-assets");
      setAssets(list.data ?? []);
      toast.success("Image deleted");
    } catch (e) { toast.error("Could not delete the image", errMsg(e)); }
  }

  if (!saved) {
    return (
      <PageShell width="wide">
        <PageHeader icon={<Palette />} eyebrow="Guest portal" title="Portal settings"
          description="Design the Wi-Fi sign-in page guests see." />
        {loadErr ? <ErrorBanner err={loadErr} /> : (
          <div className="space-y-3" aria-label="Loading portal settings">
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-96 w-full" />
          </div>
        )}
      </PageShell>
    );
  }

  const tpl = templateById(d.template_id);
  const opts = d.template_options ?? {};
  const inUse = new Set([d.logo_url, d.background_url, d.hero_image_url].filter(Boolean));
  const unused = assets.filter((a) => !a.in_use && !inUse.has(a.url));
  const advancedCount = (d.custom_css?.trim() ? 1 : 0) + (d.custom_html?.trim() ? 1 : 0);
  const sectionHasIssue = (s: Section) => {
    const fields: Record<Section, string[]> = {
      template: ["template_id", "template_options", "hero_image_url"],
      brand: ["logo_url", "background_url", "brand_color", "brand_color_dark", "text_color", "corner_radius", "font_family"],
      content: ["hotel_name", "welcome_text", "help_text", "terms_url"],
      wording: ["translations"], languages: ["languages"], advanced: ["custom_css", "custom_html"], history: [],
    };
    return blocking.some((i) => fields[s].includes(i.field));
  };

  const buttonContrast = contrastRatio(d.brand_color || DEFAULTS.brand_color, DEFAULTS.card);
  const textContrast = contrastRatio(d.text_color || DEFAULTS.text_color, DEFAULTS.card);

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Palette />}
        eyebrow="Guest portal"
        title="Portal settings"
        description="Design the Wi-Fi sign-in page: choose a layout, brand it, and check it in the live preview. Guests see your changes as soon as you save."
        actions={
          <>
            {dirty
              ? <Badge tone="warn" dot>Unsaved changes</Badge>
              : <span className="inline-flex items-center gap-1 text-xs text-muted-foreground"><Check className="h-3.5 w-3.5" aria-hidden /> All changes saved</span>}
            <Button variant="secondary" disabled={!dirty || busy} onClick={discard}>Discard</Button>
            <Button disabled={!dirty || busy || !writable || blocking.length > 0} onClick={() => save()}>
              {busy ? "Saving…" : "Save changes"}
            </Button>
          </>
        }
      />

      <MetricStrip
        items={[
          { label: "Layout", value: tpl.name },
          { label: "Languages offered", value: offeredLanguages(d).length },
          { label: "Custom code", value: advancedCount ? (advancedCount === 2 ? "CSS and HTML" : d.custom_css?.trim() ? "CSS" : "HTML") : "None" },
          {
            label: "Checks",
            value: !writable ? "—" : checking ? "Checking…" : blocking.length ? `${blocking.length} to fix` : "All clear",
            tone: !writable || checking ? undefined : blocking.length ? "err" : "ok",
          },
        ]}
      />

      {needsPassword && (
        <Callout tone="info" title="Saving will ask for your password">
          You changed the portal&apos;s custom CSS or HTML. Those are confirmed separately, because this page
          collects room numbers and voucher codes.
        </Callout>
      )}
      {blocking.length > 0 && (
        <Callout tone="danger" title="Fix these before saving">
          <ul className="list-disc space-y-0.5 pl-4">
            {blocking.slice(0, 5).map((i, n) => <li key={n}>{fieldName(i.field)}: {i.message}</li>)}
            {blocking.length > 5 && <li>and {blocking.length - 5} more</li>}
          </ul>
          {draftBlocked && (
            <p className="mt-2 text-xs">Until these are fixed, your changes are not kept if you close this page.</p>
          )}
        </Callout>
      )}
      {saveErr ? <ErrorBanner err={saveErr} /> : null}

      <div className="grid gap-6 lg:grid-cols-[210px_minmax(0,1fr)] xl:grid-cols-[210px_minmax(0,1fr)_minmax(0,1.05fr)]">
        {/* ---- the rail --------------------------------------------------------------------------------- */}
        <div role="tablist" aria-label="Portal settings sections" aria-orientation="vertical"
          className="-mx-1 flex gap-1 overflow-x-auto px-1 pb-1 lg:mx-0 lg:flex-col lg:self-start lg:overflow-visible lg:px-0 xl:sticky xl:top-6">
          {SECTIONS.map((s) => (
            <button
              key={s.id}
              role="tab"
              type="button"
              aria-selected={section === s.id}
              aria-controls="portal-section"
              onClick={() => setSection(s.id)}
              className={cn(
                "inline-flex shrink-0 items-center gap-2 rounded-md px-3 py-2 text-left text-sm transition-colors",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                section === s.id
                  ? "bg-primary-subtle font-medium text-primary-subtle-foreground"
                  : "text-muted-foreground hover:bg-surface hover:text-foreground",
              )}
            >
              <s.icon className="h-4 w-4 shrink-0" aria-hidden />
              <span className="whitespace-nowrap">{s.label}</span>
              {sectionHasIssue(s.id) && <AlertTriangle className="ml-auto h-3.5 w-3.5 text-destructive" aria-label="has problems" />}
            </button>
          ))}
        </div>

        {/* ---- the section being edited ----------------------------------------------------------------- */}
        <div id="portal-section" role="tabpanel" className="min-w-0 space-y-5">
          {section === "template" && (
            <>
              <Card>
                <CardHeader>
                  <CardTitle>Layout</CardTitle>
                  <CardDescription>
                    Each card is your own sign-in page in that layout. Every sign-in method works the same in all
                    of them — only the arrangement changes.
                  </CardDescription>
                </CardHeader>
                <CardBody>
                  <TemplateGallery design={d} value={tpl.id} disabled={!writable}
                    onChange={(id: TemplateId) => set("template_id", id)} />
                </CardBody>
              </Card>
              <Card>
                <CardHeader>
                  <CardTitle>{tpl.name} options</CardTitle>
                  <CardDescription>Only the options this layout uses are shown.</CardDescription>
                </CardHeader>
                <CardBody className="space-y-5">
                  {tpl.options.includes("hero") && (
                    <ImageField label="Hero photograph"
                      help="The large photograph beside or behind the sign-in. Leave it empty to use the background photograph."
                      src={assetSrc(d.hero_image_url)} busy={uploading === "hero_image_url"} writable={writable} wide
                      onPick={(f) => upload("hero_image_url", f)} onClear={() => set("hero_image_url", "")} />
                  )}
                  {tpl.options.includes("overlay") && (
                    <Field label={`Photo darkening — ${opts.overlay ?? 35}%`}
                      hint="Darkens the photograph so the white headline over it stays readable.">
                      <input type="range" min={0} max={90} step={5} value={opts.overlay ?? 35} disabled={!writable}
                        onChange={(e) => setOpt("overlay", Number(e.target.value))}
                        className="w-full accent-primary" />
                    </Field>
                  )}
                  {tpl.options.includes("panel_position") && (
                    <OptionRow label="Sign-in panel" hint="Mirrored automatically for Arabic.">
                      <Segmented label="Sign-in panel position" size="sm"
                        value={opts.panel_position ?? "end"}
                        onChange={(v) => writable && setOpt("panel_position", v as TemplateOptions["panel_position"])}
                        options={[
                          { value: "start", label: "Left" },
                          ...(tpl.id === "immersive" ? [{ value: "center", label: "Centre" }] : []),
                          { value: "end", label: "Right" },
                        ]} />
                    </OptionRow>
                  )}
                  {tpl.options.includes("hero_height") && (
                    <OptionRow label="Banner height">
                      <Segmented label="Banner height" size="sm" value={opts.hero_height ?? "medium"}
                        onChange={(v) => writable && setOpt("hero_height", v as TemplateOptions["hero_height"])}
                        options={[{ value: "short", label: "Short" }, { value: "medium", label: "Medium" }, { value: "tall", label: "Tall" }]} />
                    </OptionRow>
                  )}
                  {tpl.options.includes("surface") && (
                    <OptionRow label="Panel surface">
                      <Segmented label="Panel surface" size="sm" value={opts.surface ?? "glass"}
                        onChange={(v) => writable && setOpt("surface", v as TemplateOptions["surface"])}
                        options={[{ value: "glass", label: "Frosted glass" }, { value: "solid", label: "Solid" }]} />
                    </OptionRow>
                  )}
                  {tpl.options.includes("density") && (
                    <OptionRow label="Spacing" hint="How much room the fields and buttons take.">
                      <Segmented label="Spacing" size="sm" value={opts.density ?? "comfortable"}
                        onChange={(v) => writable && setOpt("density", v as TemplateOptions["density"])}
                        options={[{ value: "compact", label: "Compact" }, { value: "comfortable", label: "Comfortable" }, { value: "spacious", label: "Spacious" }]} />
                    </OptionRow>
                  )}
                  {tpl.options.includes("heading_font") && (
                    <Field label="Heading typeface"
                      hint="A font stack for the hotel name and headline, e.g. Georgia, serif. Only fonts already on the guest's device are used."
                      error={fieldError("template_options")}>
                      <Input value={opts.heading_font ?? ""} disabled={!writable} placeholder="Georgia, serif"
                        onChange={(e) => setOpt("heading_font", e.target.value || undefined)} />
                    </Field>
                  )}
                </CardBody>
              </Card>
            </>
          )}

          {section === "brand" && (
            <>
              <Card>
                <CardHeader><CardTitle>Images</CardTitle></CardHeader>
                <CardBody className="space-y-6">
                  <ImageField label="Logo" help="Shown at the top of the sign-in page, up to 60px tall."
                    src={assetSrc(d.logo_url)} busy={uploading === "logo_url"} writable={writable}
                    onPick={(f) => upload("logo_url", f)} onClear={() => set("logo_url", "")} error={fieldError("logo_url")} />
                  <ImageField label="Background photograph"
                    help="Fills the screen behind the sign-in card. A wide, uncluttered photograph works best."
                    src={assetSrc(d.background_url)} busy={uploading === "background_url"} writable={writable} wide
                    onPick={(f) => upload("background_url", f)} onClear={() => set("background_url", "")} error={fieldError("background_url")} />
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
                <CardBody className="space-y-5">
                  <div className="grid gap-5 sm:grid-cols-2">
                    <ColorField label="Brand colour" help="Buttons, the selected tab and links."
                      value={d.brand_color} fallback={DEFAULTS.brand_color} disabled={!writable}
                      error={fieldError("brand_color")} onChange={(v) => set("brand_color", v)} />
                    <ColorField label="Button shade" help="The darker end of the button gradient."
                      value={d.brand_color_dark} fallback={DEFAULTS.brand_color_dark} disabled={!writable}
                      error={fieldError("brand_color_dark")} onChange={(v) => set("brand_color_dark", v)} />
                    <ColorField label="Text colour" help="Headings and field labels."
                      value={d.text_color} fallback={DEFAULTS.text_color} disabled={!writable}
                      error={fieldError("text_color")} onChange={(v) => set("text_color", v)} />
                    <Field label="Corner radius" hint="How rounded the card and fields are, e.g. 20px." error={fieldError("corner_radius")}>
                      <Input value={d.corner_radius ?? ""} disabled={!writable}
                        onChange={(e) => set("corner_radius", e.target.value)} placeholder={DEFAULTS.corner_radius} />
                    </Field>
                  </div>
                  <ContrastNote label="White button text on your brand colour" ratio={buttonContrast} />
                  <ContrastNote label="Your text colour on the white card" ratio={textContrast} />
                  <Field label="Typeface" error={fieldError("font_family")}
                    hint="A font stack. Only fonts already on the guest's device will be used — the portal loads nothing from the internet.">
                    <Input value={d.font_family ?? ""} disabled={!writable}
                      onChange={(e) => set("font_family", e.target.value)} placeholder="Inter, system-ui, sans-serif" />
                  </Field>
                </CardBody>
              </Card>
            </>
          )}

          {section === "content" && (
            <Card>
              <CardHeader>
                <CardTitle>Hotel identity and content</CardTitle>
                <CardDescription>Your own words, shown to every guest in every language.</CardDescription>
              </CardHeader>
              <CardBody className="space-y-5">
                <Field label="Hotel name" error={fieldError("hotel_name")}
                  hint={`Shown at the top of the sign-in page and in the browser tab. ${(d.hotel_name ?? "").length}/${LIMITS.hotelName}`}>
                  <Input value={d.hotel_name ?? ""} disabled={!writable} maxLength={LIMITS.hotelName}
                    onChange={(e) => set("hotel_name", e.target.value)} placeholder="Coral Sea Holiday Resort" />
                </Field>
                <Field label="Welcome line" error={fieldError("welcome_text")}
                  hint={`One short sentence under the hotel name — the headline in the photographic layouts. Leave empty to show nothing. ${(d.welcome_text ?? "").length}/${LIMITS.welcomeText}`}>
                  <Input value={d.welcome_text ?? ""} disabled={!writable} maxLength={LIMITS.welcomeText}
                    onChange={(e) => set("welcome_text", e.target.value)} placeholder="Welcome — connect to our Wi-Fi" />
                </Field>
                <Field label="Help line" error={fieldError("help_text")}
                  hint={`Shown below the sign-in, for guests who cannot get on. ${(d.help_text ?? "").length}/${LIMITS.helpText}`}>
                  <Input value={d.help_text ?? ""} disabled={!writable} maxLength={LIMITS.helpText}
                    onChange={(e) => set("help_text", e.target.value)} placeholder="Ask reception if you need a code" />
                </Field>
                <Field label="Terms of use link" error={fieldError("terms_url")}
                  hint="An https:// address, or a file you uploaded here (/assets/…). A guest reaching the portal has no internet yet, so an external page will not load until they are online.">
                  <Input value={d.terms_url ?? ""} disabled={!writable}
                    onChange={(e) => set("terms_url", e.target.value)} placeholder="https://…/terms" />
                </Field>
              </CardBody>
            </Card>
          )}

          {section === "wording" && (
            <LanguagesSection d={d} setD={setD} writable={writable} part="wording" initialEditing={wordingLang} />
          )}
          {section === "languages" && (
            <LanguagesSection d={d} setD={setD} writable={writable} part="offered"
              onEditWording={(code) => { setWordingLang(code); setSection("wording"); }} />
          )}

          {section === "advanced" && (
            <AdvancedSection d={d} set={set} writable={writable} checking={checking}
              issues={issues.filter((i) => i.field === "custom_css" || i.field === "custom_html")}
              sanitized={validation?.sanitized ?? null} needsPassword={needsPassword} />
          )}

          {section === "history" && (
            <HistorySection revisions={revisions} writable={writable} dirty={dirty} onRestored={load} />
          )}
        </div>

        {/* ---- the live preview --------------------------------------------------------------------------- */}
        <div className="min-w-0 lg:col-span-2 xl:col-span-1 xl:sticky xl:top-6 xl:self-start">
          <PortalPreview design={d} sanitized={validation?.sanitized ?? null} />
        </div>
      </div>

      <ConfirmDialog
        open={stepUp}
        onOpenChange={(o) => { if (!o) setStepUp(false); }}
        title="Save your custom CSS and HTML"
        description="You changed the portal's custom CSS or HTML. This page collects room numbers and voucher codes, so the styling and markup injected into it are confirmed separately."
        confirmLabel="Save changes"
        busy={busy}
        error={stepUpErr}
        requirePassword
        onConfirm={({ password }) => save(password)}
      />
    </PageShell>
  );
}

function fieldName(field: string) {
  const names: Record<string, string> = {
    hotel_name: "Hotel name", welcome_text: "Welcome line", help_text: "Help line", terms_url: "Terms of use link",
    logo_url: "Logo", background_url: "Background", hero_image_url: "Hero photograph", brand_color: "Brand colour",
    brand_color_dark: "Button shade", text_color: "Text colour", corner_radius: "Corner radius", font_family: "Typeface",
    template_id: "Layout", template_options: "Layout options", languages: "Languages", translations: "Sign-in page text",
    custom_css: "Custom CSS", custom_html: "Custom HTML",
  };
  return names[field] ?? field;
}

function OptionRow({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="min-w-0">
        <div className="text-sm font-medium">{label}</div>
        {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
      </div>
      {children}
    </div>
  );
}

/** WCAG AA asks 4.5:1 for body text. The designer warns rather than refuses: the colours are the hotel's. */
function ContrastNote({ label, ratio }: { label: string; ratio: number | null }) {
  if (ratio === null) return null;
  const ok = ratio >= 4.5;
  return (
    <p className={cn("flex items-center gap-1.5 text-xs", ok ? "text-muted-foreground" : "text-warning-subtle-foreground")}>
      {ok ? <Check className="h-3.5 w-3.5 text-success" aria-hidden /> : <AlertTriangle className="h-3.5 w-3.5" aria-hidden />}
      {label}: {ratio.toFixed(1)}:1{ok ? "" : " — below the 4.5:1 many guests need to read it comfortably. Choose a darker colour."}
    </p>
  );
}

function ColorField({ label, help, value, fallback, disabled, error, onChange }: {
  label: string; help: string; value?: string; fallback: string; disabled?: boolean; error?: string;
  onChange: (v: string) => void;
}) {
  // Two controls for one value (a colour well and its hex text), so the label is bound to the well and the text
  // field names itself; Field binds exactly one control and would give the wrapper the id.
  const id = useId();
  return (
    <div className="min-w-0">
      <Label htmlFor={id}>{label}</Label>
      <span className="flex items-center gap-2">
        <input id={id} type="color" value={/^#[0-9a-fA-F]{6}$/.test(value ?? "") ? value : fallback} disabled={disabled}
          aria-describedby={id + "-help"} onChange={(e) => onChange(e.target.value)} className="h-9 w-12 shrink-0 rounded border" />
        <Input value={value ?? ""} placeholder={fallback} disabled={disabled} aria-invalid={error ? true : undefined}
          aria-label={label + " as a hex value"} onChange={(e) => onChange(e.target.value)} />
      </span>
      {error
        ? <p id={id + "-help"} className="mt-1.5 text-xs text-destructive">{error}</p>
        : <Hint id={id + "-help"}>{help}</Hint>}
    </div>
  );
}

/** Upload, replace, remove — and see what is actually set, read back through the operator API (an /assets/
 *  path is a guest-network path that resolves to nothing from the admin origin). */
function ImageField({ label, help, src, busy, writable, wide, error, onPick, onClear }: {
  label: string; help: string; src?: string; busy: boolean; writable: boolean; wide?: boolean; error?: string;
  onPick: (f: File) => void; onClear: () => void;
}) {
  return (
    <div>
      <span className="block text-sm font-medium">{label}</span>
      <span className="mb-2 block text-xs text-muted-foreground">{help}</span>
      <div className="flex flex-wrap items-center gap-3">
        <div className={`flex items-center justify-center overflow-hidden rounded-lg border bg-surface ${wide ? "h-24 w-44" : "h-20 w-32"}`}>
          {src
            // eslint-disable-next-line @next/next/no-img-element
            ? <img src={src} alt={`${label} currently set`} className="h-full w-full object-contain" />
            : <span className="px-2 text-center text-2xs text-muted-foreground">Nothing set — the portal uses its own default</span>}
        </div>
        <div className="flex flex-col gap-2">
          <label className={`inline-flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm ${!writable ? "opacity-50" : ""}`}>
            <Upload className="h-4 w-4" aria-hidden />
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
      {error && <p className="mt-1.5 text-xs text-destructive">{error}</p>}
    </div>
  );
}
