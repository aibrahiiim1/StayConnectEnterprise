"use client";

// One current configuration and one verb, "Save"; history is shown with Restore, never as drafts or versions.
// Ordinary settings need no password, but adding, changing or clearing custom CSS/HTML does (ADVANCED_KEYS /
// advancedChanged, mirrored on the server). Welcome and help lines are single fields, not per-language.
// Safety is the server's: every change goes to /portal-branding/validate and the preview renders the sanitised fields.

import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { api, ApiError, ListResp, PortalAsset, Whoami } from "@/lib/api";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Hint, Input, Label, Textarea } from "@/components/ui/input";
import { Segmented } from "@/components/ui/tabs";
import { MetricStrip } from "@/components/ui/data";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { buttonVariants } from "@/components/ui/button";
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
import { Design, TemplateOptions, advancedChanged, assetSrc, collectedDetails, contrastRatio, offeredLanguages } from "./strings";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { LanguagesSection } from "./languages";
import { PRIVACY_TEMPLATE } from "./privacy-template";

// Room numbers are mentioned only where hospitality is licensed.
const pageHelp = (hospitality: boolean) => (
  <>
    <HelpSection title="How this page works">
      <p>
        Design the Wi-Fi sign-in page: choose a layout, brand it, and check it in the live preview. The preview is
        the real sign-in page with your unsaved changes. Clients see your changes as soon as you save.
      </p>
      <p>
        The preview shows the sign-in methods your clients actually see. Which methods those are is decided in
        Sign-in methods, not on this page.
      </p>
    </HelpSection>
    <HelpSection title="The sections">
      <HelpList
        items={[
          <><strong>Template</strong> — each card is your own sign-in page in that layout. Every sign-in method works the same in all of them; only the arrangement changes. Only the options the chosen layout uses are shown.</>,
          <><strong>Brand</strong> — logo, background photograph, colours and type. Images are stored on this appliance and served from it, so they load for a client who has no internet yet.</>,
          <><strong>Content</strong> — your own words, shown to every client in every language.</>,
          <><strong>Sign-in page text</strong> and <strong>Languages</strong> — the wording clients read, and which languages they are offered.</>,
          <><strong>Advanced HTML &amp; CSS</strong> — your own styling and markup, checked before it is accepted.</>,
          <><strong>History</strong> — the most recent saves of this page; restoring one makes it what clients see again.</>,
        ]}
      />
    </HelpSection>
    <HelpSection title="Fonts and links">
      <p>
        Only fonts already on the client&apos;s device are used. A client reaching the portal has no internet yet,
        so an external page, image or font will not load until they are online, and each one tells its host the
        client&apos;s address; a file uploaded here works straight away. The checks name every outside host the
        page would load from.
      </p>
    </HelpSection>
    <HelpSection title="Languages">
      <p>
        The portal ships complete wording for its built-in languages — nothing to translate, just choose which
        your clients are offered. A client&apos;s device language is detected automatically and matched against
        that list; English is always available and is what anything else falls back to.
      </p>
    </HelpSection>
    <HelpSection title="Custom CSS and HTML">
      <HelpList
        items={[
          <>Custom CSS is wrapped in its own cascade layer: your rules beat the portal&apos;s styling and template, but cannot hide the sign-in forms. !important is removed — it is not needed.</>,
          <>Custom HTML is shown below the sign-in (as content blocks in the Editorial layout): text, headings, lists, tables, links and images from your uploads or https.</>,
          <>Saving a change to either asks for your password, because this page collects {collectedDetails(hospitality)}.</>,
        ]}
      />
    </HelpSection>
  </>
);

/** Mirrors the portal's unbranded values so an unset colour well shows what a guest sees, not black. */
const DEFAULTS = {
  brand_color: "#0f6b63",
  brand_color_dark: "#0b544e",
  text_color: "#14302e",
  corner_radius: "20px",
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
  // Picks the wording for what the sign-in page collects; unknown module state fails closed to the neutral wording.
  const hospitality = moduleLicensed(useCapabilities(), "hospitality");
  const [saved, setSaved] = useState<Design | null>(null);
  const [d, setD] = useState<Design>({});
  const [revisions, setRevisions] = useState<RevisionSummary[]>([]);
  const [roles, setRoles] = useState<string[] | null>(null);
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

  const writable = roles !== null && canWrite("portal-branding", roles);
  const set = useCallback(<K extends keyof Design>(k: K, v: Design[K]) => setD((p) => ({ ...p, [k]: v })), []);
  const setOpt = <K extends keyof TemplateOptions>(k: K, v: TemplateOptions[K]) =>
    setD((p) => ({ ...p, template_options: { ...(p.template_options ?? {}), [k]: v } }));

  const load = useCallback(async () => {
    try {
      const s = await api.get<BrandingState<Design>>("/portal-branding");
      const live = s.design ?? {};
      setSaved(live);
      setRevisions(s.revisions ?? []);
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
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, [load]);

  const dirty = useMemo(() => saved !== null && JSON.stringify(d) !== JSON.stringify(saved), [d, saved]);
  const needsPassword = useMemo(() => saved !== null && advancedChanged(d, saved), [d, saved]);

  // Unsaved work is autosaved quietly as a draft. The server validates drafts like a save and returns 400 for
  // refused markup, so send only once the verdict on this exact design is clean (or unobtainable).
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

  // The server's verdict as the operator types (debounced): the same rules that accept or refuse the save.
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
  // Warnings outside the Advanced section, which lists its own.
  const notes = issues.filter((i) => i.severity === "warning" && i.field !== "custom_css" && i.field !== "custom_html");
  const fieldNote = (field: string) => notes.filter((i) => i.field === field).map((i) => i.message).join("; ") || undefined;

  async function save(password?: string) {
    if (needsPassword && password === undefined) {
      setStepUpErr(null);
      setStepUp(true);
      return;
    }
    setBusy(true); setSaveErr(null);
    try {
      await api.post("/portal-branding/settings", { design: d, password: password || undefined });
      setStepUp(false);
      toast.success("Saved", "Clients see these settings now.");
      await load();
    } catch (e) {
      if (e instanceof ApiError && e.code === "reauth_required") {
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
      // Use the API client, which knows where edged is; a hand-written URL once broke uploads (see api.upload).
      const a = await api.upload<PortalAsset>("/portal-assets", file);
      set(field, a.url);
      const list = await api.get<ListResp<PortalAsset>>("/portal-assets");
      setAssets(list.data ?? []);
      toast.success(`${IMAGE_LABEL[field]} uploaded`, "Save changes to show it to clients.");
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
        <PageHeader icon={<Palette />} eyebrow="Client access" title="Portal settings"
          description="Design the Wi-Fi sign-in page clients see." help={pageHelp(hospitality)} />
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
      content: ["hotel_name", "welcome_text", "help_text", "terms_url", "privacy_text", "privacy_url"],
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
        eyebrow="Client access"
        title="Portal settings"
        description="Design the Wi-Fi sign-in page clients see. Clients see your changes as soon as you save."
        help={pageHelp(hospitality)}
        actions={
          writable ? (
            <>
              <span aria-live="polite">
                {dirty
                  ? <Badge tone="warn" dot>Unsaved changes</Badge>
                  : <span className="inline-flex items-center gap-1 text-xs text-muted-foreground"><Check className="size-3.5" aria-hidden /> All changes saved</span>}
              </span>
              <Button variant="secondary" disabled={!dirty || busy} onClick={discard}>Discard</Button>
              <Button disabled={!dirty || busy || blocking.length > 0} onClick={() => save()}>
                {busy ? "Saving…" : "Save changes"}
              </Button>
            </>
          ) : undefined
        }
      />

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see the portal design but not change it.</ReadOnlyNotice>
      )}

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
          collects {collectedDetails(hospitality)}.
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
      {notes.length > 0 && blocking.length === 0 && (
        <Callout tone="warning" title="Worth knowing before you save">
          <ul className="list-disc space-y-0.5 pl-4">
            {notes.map((i, n) => <li key={n}>{fieldName(i.field)}: {i.message}</li>)}
          </ul>
        </Callout>
      )}
      {saveErr ? <ErrorBanner err={saveErr} /> : null}

      <div className="grid gap-6 lg:grid-cols-[210px_minmax(0,1fr)] xl:grid-cols-[210px_minmax(0,1fr)_minmax(0,1.05fr)]">
        {/* The tablist is entered at its selected tab (roving tabIndex); jsx-a11y reads the container role as a control. */}
        {/* eslint-disable-next-line jsx-a11y/interactive-supports-focus */}
        <div role="tablist" aria-label="Portal settings sections" aria-orientation="vertical"
          onKeyDown={(e) => {
            const i = SECTIONS.findIndex((x) => x.id === section);
            const step = e.key === "ArrowDown" || e.key === "ArrowRight" ? 1 : e.key === "ArrowUp" || e.key === "ArrowLeft" ? -1 : 0;
            if (!step) return;
            e.preventDefault();
            const next = SECTIONS[(i + step + SECTIONS.length) % SECTIONS.length];
            setSection(next.id);
            document.getElementById(`portal-tab-${next.id}`)?.focus();
          }}
          className="-mx-1 flex gap-1 overflow-x-auto px-1 pb-1 lg:mx-0 lg:flex-col lg:self-start lg:overflow-visible lg:px-0 xl:sticky xl:top-6">
          {SECTIONS.map((s) => (
            <button
              key={s.id}
              id={`portal-tab-${s.id}`}
              role="tab"
              type="button"
              tabIndex={section === s.id ? 0 : -1}
              aria-selected={section === s.id}
              aria-controls="portal-section"
              onClick={() => setSection(s.id)}
              className={cn(
                "inline-flex shrink-0 items-center gap-2 rounded-md px-3 py-2 text-start text-sm transition-colors",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                section === s.id
                  ? "bg-primary-subtle font-medium text-primary-subtle-foreground"
                  : "text-muted-foreground hover:bg-surface hover:text-foreground",
              )}
            >
              <s.icon className="size-4 shrink-0" aria-hidden />
              <span className="whitespace-nowrap">{s.label}</span>
              {sectionHasIssue(s.id) && (
                <span className="ms-auto inline-flex items-center text-destructive">
                  <AlertTriangle className="size-3.5" aria-hidden />
                  <span className="sr-only">has problems</span>
                </span>
              )}
            </button>
          ))}
        </div>

        <div id="portal-section" role="tabpanel" className="min-w-0 space-y-5">
          {section === "template" && (
            <>
              <Card>
                <CardHeader>
                  <CardTitle>Layout</CardTitle>
                  <CardDescription>Each card is your own sign-in page in that layout.</CardDescription>
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
                      hint="A font stack for the site name and headline, e.g. Georgia, serif. Only fonts already on the client's device are used."
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
                    PNG, JPEG, WebP or GIF, up to 8&nbsp;MB. SVG is refused: it can carry script, and this page
                    collects {collectedDetails(hospitality)}.
                  </p>
                  {unused.length > 0 && (
                    <div className="border-t border-border pt-4">
                      <h3 className="text-label">Previously uploaded</h3>
                      <p className="mb-2 text-xs text-muted-foreground">Not used by the current settings.</p>
                      <ul className="flex flex-wrap gap-3">
                        {unused.map((a) => (
                          <li key={a.name} className="flex items-center gap-2 rounded-md border border-border p-2">
                            {/* eslint-disable-next-line @next/next/no-img-element */}
                            <img src={assetSrc(a.url)} alt="" className="h-10 w-16 rounded object-contain" />
                            <span className="text-xs text-muted-foreground">{Math.round(a.size_bytes / 1024)} KB</span>
                            {writable && (
                              <Button size="icon-sm" variant="ghost"
                                onClick={() => removeUnusedAsset(a.name)} aria-label={`Delete ${a.name}`}>
                                <Trash2 />
                              </Button>
                            )}
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
                    hint="A font stack. Only fonts already on the client's device are used.">
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
                <CardTitle>Site identity and content</CardTitle>
                <CardDescription>Your own words, shown to every client in every language.</CardDescription>
              </CardHeader>
              <CardBody className="space-y-5">
                <Field label="Site name" error={fieldError("hotel_name")}
                  hint={`Shown at the top of the sign-in page and in the browser tab. ${(d.hotel_name ?? "").length}/${LIMITS.hotelName}`}>
                  <Input value={d.hotel_name ?? ""} disabled={!writable} maxLength={LIMITS.hotelName}
                    onChange={(e) => set("hotel_name", e.target.value)} placeholder="Semantics Demo Site" />
                </Field>
                <Field label="Welcome line" error={fieldError("welcome_text")}
                  hint={`One short sentence under the site name — the headline in the photographic layouts. Leave empty to show nothing. ${(d.welcome_text ?? "").length}/${LIMITS.welcomeText}`}>
                  <Input value={d.welcome_text ?? ""} disabled={!writable} maxLength={LIMITS.welcomeText}
                    onChange={(e) => set("welcome_text", e.target.value)} placeholder="Welcome — connect to our Wi-Fi" />
                </Field>
                <Field label="Help line" error={fieldError("help_text")}
                  hint={`Shown below the sign-in, for clients who cannot get on. ${(d.help_text ?? "").length}/${LIMITS.helpText}`}>
                  <Input value={d.help_text ?? ""} disabled={!writable} maxLength={LIMITS.helpText}
                    onChange={(e) => set("help_text", e.target.value)} placeholder="Ask the site team if you need a code" />
                </Field>
                <Field label="Terms of use link" error={fieldError("terms_url")}
                  hint="An https:// address, or a file you uploaded here (/assets/…). An external page loads only once the client is online.">
                  <Input value={d.terms_url ?? ""} disabled={!writable}
                    onChange={(e) => set("terms_url", e.target.value)} placeholder="https://…/terms" />
                </Field>
              </CardBody>
            </Card>
          )}

          {section === "content" && (
            <Card>
              <CardHeader>
                <CardTitle>Privacy notice</CardTitle>
                <CardDescription>
                  What clients are told about their data before they sign in. Shown under every sign-in method as
                  &ldquo;How we use your data&rdquo;, opening in place, so it can be read before the client is online.
                </CardDescription>
              </CardHeader>
              <CardBody className="space-y-5">
                {!(d.privacy_text ?? "").trim() && !(d.privacy_url ?? "").trim() && (
                  <Callout tone="warning" title="Clients are not told how their data is used">
                    <p>Data protection law asks you to tell clients, before they sign in, who you are, what this
                      Wi-Fi records, why, for how long, and what their rights are.</p>
                    {writable && (
                      <Button className="mt-2" size="sm" variant="secondary"
                        onClick={() => set("privacy_text", PRIVACY_TEMPLATE)}>
                        Start from the template
                      </Button>
                    )}
                  </Callout>
                )}
                <Field label="Notice text" error={fieldError("privacy_text")}
                  hint={fieldNote("privacy_text") ??
                    `Plain text; line breaks are kept. Complete every part in [[double brackets]]. The retention periods are the data retention settings on the Backups page. ${(d.privacy_text ?? "").length}/${LIMITS.privacyText}`}>
                  <Textarea value={d.privacy_text ?? ""} disabled={!writable} maxLength={LIMITS.privacyText} rows={14}
                    onChange={(e) => set("privacy_text", e.target.value)} />
                </Field>
                <Field label="Full privacy notice link (optional)" error={fieldError("privacy_url")}
                  hint="An https:// address, or a file you uploaded here (/assets/…). An external page loads only once the client is online, so keep the text above complete.">
                  <Input value={d.privacy_url ?? ""} disabled={!writable}
                    onChange={(e) => set("privacy_url", e.target.value)} placeholder="https://…/privacy" />
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
              sanitized={validation?.sanitized ?? null} needsPassword={needsPassword} hospitality={hospitality} />
          )}

          {section === "history" && (
            <HistorySection revisions={revisions} writable={writable} dirty={dirty} onRestored={load} />
          )}
        </div>

        <div className="min-w-0 lg:col-span-2 xl:col-span-1 xl:sticky xl:top-6 xl:self-start">
          <PortalPreview design={d} sanitized={validation?.sanitized ?? null} />
        </div>
      </div>

      <ConfirmDialog
        open={stepUp}
        onOpenChange={(o) => { if (!o) setStepUp(false); }}
        title="Save your custom CSS and HTML"
        description={`You changed the portal's custom CSS or HTML. This page collects ${collectedDetails(hospitality)}, so the styling and markup injected into it are confirmed separately.`}
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
    hotel_name: "Site name", welcome_text: "Welcome line", help_text: "Help line", terms_url: "Terms of use link",
    privacy_text: "Privacy notice", privacy_url: "Privacy notice link",
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
        <div className="text-label">{label}</div>
        {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
      </div>
      {children}
    </div>
  );
}

/** WCAG AA asks 4.5:1 for body text; warns rather than refuses since the colours are the hotel's. */
function ContrastNote({ label, ratio }: { label: string; ratio: number | null }) {
  if (ratio === null) return null;
  const ok = ratio >= 4.5;
  return (
    <p className={cn(
      "flex items-start gap-1.5 text-xs",
      ok ? "text-muted-foreground" : "rounded-md border border-warning/30 bg-warning-subtle px-3 py-2 text-warning-subtle-foreground",
    )}>
      {ok ? <Check className="mt-px size-3.5 shrink-0 text-success" aria-hidden /> : <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden />}
      {label}: {ratio.toFixed(1)}:1{ok ? "" : " — below the 4.5:1 many clients need to read it comfortably. Choose a darker colour."}
    </p>
  );
}

function ColorField({ label, help, value, fallback, disabled, error, onChange }: {
  label: string; help: string; value?: string; fallback: string; disabled?: boolean; error?: string;
  onChange: (v: string) => void;
}) {
  // Two controls for one value, so the label binds to the well and the text field names itself (Field binds one control).
  const id = useId();
  return (
    <div className="min-w-0">
      <Label htmlFor={id}>{label}</Label>
      <span className="flex items-center gap-2">
        <input id={id} type="color" value={/^#[0-9a-fA-F]{6}$/.test(value ?? "") ? value : fallback} disabled={disabled}
          aria-describedby={id + "-help"} onChange={(e) => onChange(e.target.value)}
          className="h-10 w-12 shrink-0 cursor-pointer rounded-md border border-input bg-card p-1 disabled:cursor-not-allowed disabled:opacity-50" />
        <Input value={value ?? ""} placeholder={fallback} disabled={disabled} aria-invalid={error ? true : undefined}
          aria-label={label + " as a hex value"} onChange={(e) => onChange(e.target.value)} />
      </span>
      {error
        ? <p id={id + "-help"} className="mt-1.5 text-xs text-destructive">{error}</p>
        : <Hint id={id + "-help"}>{help}</Hint>}
    </div>
  );
}

/** Reads the image back through the operator API: an /assets/ path resolves to nothing from the admin origin. */
function ImageField({ label, help, src, busy, writable, wide, error, onPick, onClear }: {
  label: string; help: string; src?: string; busy: boolean; writable: boolean; wide?: boolean; error?: string;
  onPick: (f: File) => void; onClear: () => void;
}) {
  return (
    <div>
      <span className="block text-label">{label}</span>
      <span className="mb-2 block text-xs text-muted-foreground">{help}</span>
      <div className="flex flex-wrap items-center gap-3">
        <div className={cn("flex items-center justify-center overflow-hidden rounded-lg border border-border bg-surface", wide ? "h-24 w-44" : "h-20 w-32")}>
          {src
            // eslint-disable-next-line @next/next/no-img-element
            ? <img src={src} alt={`${label} currently set`} className="h-full w-full object-contain" />
            : <span className="px-2 text-center text-2xs text-muted-foreground">Nothing set — the portal uses its own default</span>}
        </div>
        {writable && (
          <div className="flex flex-col gap-2">
            {/* The file input is visually hidden, not display:none, so the upload stays keyboard-reachable. */}
            <label className={cn(
              buttonVariants({ variant: "secondary", size: "sm" }),
              "cursor-pointer has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring has-[:focus-visible]:ring-offset-2",
              busy && "pointer-events-none opacity-50",
            )}>
              <Upload aria-hidden />
              {busy ? "Uploading…" : src ? `Replace ${label.toLowerCase()}` : `Upload ${label.toLowerCase()}`}
              <input type="file" className="sr-only" disabled={busy}
                accept="image/png,image/jpeg,image/webp,image/gif"
                aria-label={`Upload ${label.toLowerCase()}`}
                onChange={(e) => { const f = e.target.files?.[0]; if (f) onPick(f); e.target.value = ""; }} />
            </label>
            {src && (
              <Button size="sm" variant="ghost" onClick={onClear}>
                Remove {label.toLowerCase()}
              </Button>
            )}
          </div>
        )}
      </div>
      {error && <p role="alert" className="mt-1.5 text-xs text-destructive">{error}</p>}
    </div>
  );
}
