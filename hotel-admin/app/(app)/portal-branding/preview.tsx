"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { api, ApiError } from "@/lib/api";
import { Monitor, Smartphone, Tablet, RefreshCw } from "lucide-react";
import { Segmented } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/misc";
import { moduleLicensed, useCapabilities, type Capabilities } from "@/lib/capabilities";
import { Design } from "./strings";

type Device = "desktop" | "tablet" | "mobile";

export const DEVICES: Record<Device, { w: number; h: number; label: string }> = {
  desktop: { w: 1180, h: 820, label: "Desktop" },
  tablet: { w: 820, h: 1100, label: "Tablet" },
  mobile: { w: 390, h: 844, label: "Mobile" },
};

type Toggle = { enabled?: boolean };
export type AuthMethodsDoc = {
  voucher?: Toggle;
  guest_account?: Toggle;
  open?: Toggle;
  email_account?: Toggle;
  sms?: Toggle;
  whatsapp?: Toggle;
  social?: Record<string, Toggle>;
  pms?: Toggle & { mode?: string };
  phase3_pms?: boolean;
  phase5_poststay?: boolean;
  internet_packages_available?: boolean;
};

const OPTIONAL: { key: "pms" | "email_account" | "sms" | "whatsapp" | "social"; module: string; label: string }[] = [
  { key: "pms", module: "hospitality", label: "Room" },
  { key: "email_account", module: "email_otp", label: "Email" },
  { key: "sms", module: "sms_otp", label: "Phone" },
  { key: "whatsapp", module: "whatsapp_otp", label: "WhatsApp" },
  { key: "social", module: "social_login", label: "Social" },
];

export function previewMethods(doc: AuthMethodsDoc | null, caps: Capabilities | null): AuthMethodsDoc {
  const on = (m?: Toggle) => !!m && m.enabled === true;
  const out: AuthMethodsDoc = {
    internet_packages_available:
      typeof doc?.internet_packages_available === "boolean" ? doc.internet_packages_available : true,
  };
  if (!doc) return out;
  if (on(doc.voucher)) out.voucher = { enabled: true };
  if (on(doc.guest_account)) out.guest_account = { enabled: true };
  if (on(doc.open)) out.open = { enabled: true };
  for (const o of OPTIONAL) {
    if (!moduleLicensed(caps, o.module)) continue;
    if (o.key === "social") {
      const providers = Object.entries(doc.social ?? {}).filter(([, v]) => on(v));
      if (providers.length) out.social = Object.fromEntries(providers.map(([k]) => [k, { enabled: true }]));
    } else if (o.key === "pms") {
      if (on(doc.pms)) out.pms = { enabled: true, mode: doc.pms?.mode };
      if (doc.phase3_pms) out.phase3_pms = true;
      if (doc.phase5_poststay) out.phase5_poststay = true;
    } else if (on(doc[o.key])) {
      out[o.key] = { enabled: true };
    }
  }
  return out;
}

export function optionalMethodsShown(m: AuthMethodsDoc): string[] {
  return OPTIONAL.filter((o) => (o.key === "social" ? Object.keys(m.social ?? {}).length > 0 : !!m[o.key]?.enabled))
    .map((o) => o.label);
}

const METHODS_TTL_MS = 30_000;
let methodsDoc: Promise<AuthMethodsDoc | null> | null = null;
let methodsAt = 0;

function loadMethods(): Promise<AuthMethodsDoc | null> {
  if (!methodsDoc || Date.now() - methodsAt > METHODS_TTL_MS) {
    methodsAt = Date.now();
    methodsDoc = api.get<AuthMethodsDoc>("/auth-methods").then((d) => d ?? {}).catch(() => null);
  }
  return methodsDoc;
}

export function usePreviewMethods(): { methods: AuthMethodsDoc | null; unreadable: boolean } {
  const caps = useCapabilities();
  const [doc, setDoc] = useState<AuthMethodsDoc | null | undefined>(undefined);
  useEffect(() => {
    let live = true;
    loadMethods().then((d) => { if (live) setDoc(d); });
    return () => { live = false; };
  }, []);
  const methods = useMemo(
    () => (doc === undefined || caps === null ? null : previewMethods(doc, caps)),
    [doc, caps],
  );
  return { methods, unreadable: doc === null };
}

export function buildSrcDoc(html: string, design: Design, methodsAnswer: AuthMethodsDoc) {
  const payload = JSON.stringify({ design }).replace(/<\//g, "<\\/");
  const methods = JSON.stringify(methodsAnswer).replace(/<\//g, "<\\/");
  const shim = `<script>
(function () {
  var BRANDING = ${payload};
  if (!BRANDING.design || !BRANDING.design.background_url) {
    document.documentElement.style.setProperty("--sc-bg", "none");
  }
  var METHODS = ${methods};
  function ok(body) {
    return Promise.resolve({ ok: true, status: 200, json: function () { return Promise.resolve(body); } });
  }
  window.fetch = function (input) {
    var u = String((input && input.url) || input || "");
    if (u.indexOf("/api/branding") >= 0) return ok(BRANDING);
    if (u.indexOf("/api/auth-methods") >= 0) return ok(METHODS);
    if (u.indexOf("/access/status") >= 0) return ok({});
    return Promise.resolve({ ok: false, status: 404, json: function () { return Promise.resolve({}); } });
  };
  try {
    Object.defineProperty(window, "location", { value: window.location, writable: false });
  } catch (e) { void e; }
})();
</script>`;
  return html.includes("</head>") ? html.replace("</head>", shim + "</head>") : shim + html;
}

let portalPage: Promise<string> | null = null;

export function usePortalHTML() {
  const [html, setHtml] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const retry = useCallback(() => { portalPage = null; setErr(null); setAttempt((n) => n + 1); }, []);
  useEffect(() => {
    let live = true;
    if (!portalPage) {
      portalPage = api.get<{ html: string }>("/portal-branding/preview").then((r) => r.html);
      portalPage.catch(() => { portalPage = null; });
    }
    portalPage
      .then((h) => { if (live) { setHtml(h); setErr(null); } })
      .catch((e) => {
        if (!live) return;
        const m = e instanceof ApiError ? e.message.trim() : "";
        setErr(m ? m.charAt(0).toUpperCase() + m.slice(1) : "The preview could not be loaded.");
      });
    return () => { live = false; };
  }, [attempt]);
  return { html, err, retry };
}

const assetCache = new Map<string, string>();

export function useInlinedDesign(design: Design): Design {
  const [loaded, bump] = useState(0);
  const urls = [design.logo_url, design.background_url, design.hero_image_url];
  const key = urls.join("|");
  useEffect(() => {
    const wanted = key.split("|")
      .filter((u): u is string => !!u && u.startsWith("/assets/"))
      .map((u) => u.slice("/assets/".length))
      .filter((name) => !assetCache.has(name));
    if (!wanted.length) return;
    let live = true;
    (async () => {
      for (const name of wanted) {
        try {
          const res = await fetch(`/api/edge/v1/portal-assets/${encodeURIComponent(name)}/raw`, { cache: "force-cache" });
          if (!res.ok) continue;
          const blob = await res.blob();
          const data = await new Promise<string>((resolve, reject) => {
            const fr = new FileReader();
            fr.onload = () => resolve(String(fr.result));
            fr.onerror = () => reject(fr.error);
            fr.readAsDataURL(blob);
          });
          assetCache.set(name, data);
          if (live) bump((n) => n + 1);
        } catch { continue; }
      }
    })();
    return () => { live = false; };
  }, [key]);
  return useMemo(() => {
    const inline = (u?: string) => {
      if (!u || !u.startsWith("/assets/")) return u;
      return assetCache.get(u.slice("/assets/".length)) ?? undefined;
    };
    return {
      ...design,
      logo_url: inline(design.logo_url),
      background_url: inline(design.background_url),
      hero_image_url: inline(design.hero_image_url),
    };
  }, [design, loaded]);
}

export function useSettled<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return settled;
}

export function PortalFrame({
  srcDoc, width, height, scale, title, interactive = true,
}: {
  srcDoc: string; width: number; height: number; scale: number; title: string; interactive?: boolean;
}) {
  const [loaded, setLoaded] = useState(false);
  const boxRef = useRef<HTMLDivElement | null>(null);
  const [near, setNear] = useState(interactive);
  useEffect(() => {
    if (near) return;
    const el = boxRef.current;
    if (!el || typeof IntersectionObserver === "undefined") { setNear(true); return; }
    const io = new IntersectionObserver((es) => { if (es.some((e) => e.isIntersecting)) setNear(true); }, { rootMargin: "200px" });
    io.observe(el);
    return () => io.disconnect();
  }, [near]);
  return (
    <div ref={boxRef} className="relative" style={{ height: height * scale, width: width * scale, overflow: "hidden" }}>
      {!loaded && <Skeleton className="absolute inset-0 h-full w-full rounded-none" />}
      {near && <iframe
        title={title}
        srcDoc={srcDoc}
        sandbox="allow-scripts"
        tabIndex={interactive ? undefined : -1}
        aria-hidden={interactive ? undefined : true}
        loading={interactive ? undefined : "lazy"}
        onLoad={() => setLoaded(true)}
        style={{
          width, height, border: 0, transform: `scale(${scale})`, transformOrigin: "top left",
          pointerEvents: interactive ? undefined : "none",
        }}
      />}
    </div>
  );
}

export function PortalPreview({ design, sanitized }: {
  design: Design;
  sanitized?: { custom_css: string; custom_html: string } | null;
}) {
  const [device, setDevice] = useState<Device>("desktop");
  const { html, err, retry } = usePortalHTML();
  const { methods, unreadable } = usePreviewMethods();
  const box = useRef<HTMLDivElement>(null);
  const [boxWidth, setBoxWidth] = useState(0);

  const settled = useSettled(design, 250);

  const inlined = useInlinedDesign(settled);
  const shown = useMemo<Design>(() => {
    if (!sanitized) return inlined;
    return {
      ...inlined,
      custom_css: inlined.custom_css ? sanitized.custom_css : inlined.custom_css,
      custom_html: inlined.custom_html ? sanitized.custom_html : inlined.custom_html,
    };
  }, [inlined, sanitized]);
  const srcDoc = useMemo(() => (html && methods ? buildSrcDoc(html, shown, methods) : null), [html, shown, methods]);
  const optional = methods ? optionalMethodsShown(methods) : [];

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setBoxWidth(el.clientWidth));
    ro.observe(el);
    setBoxWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const spec = DEVICES[device];
  const scale = boxWidth > 0 ? Math.min(1, boxWidth / spec.w, device === "desktop" ? 1 : 760 / spec.h) : 0.3;

  return (
    <section className="space-y-3" aria-label="Client Portal preview">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold">Live preview</h2>
          <p className="text-xs text-muted-foreground">The real sign-in page, with your unsaved changes.</p>
        </div>
        <Segmented<Device>
          label="Preview device"
          size="sm"
          value={device}
          onChange={setDevice}
          options={(Object.keys(DEVICES) as Device[]).map((k) => ({
            value: k,
            label: (
              <span className="inline-flex items-center gap-1.5">
                {k === "desktop" ? <Monitor className="h-3.5 w-3.5" /> : k === "tablet" ? <Tablet className="h-3.5 w-3.5" /> : <Smartphone className="h-3.5 w-3.5" />}
                {DEVICES[k].label}
              </span>
            ),
          }))}
        />
      </div>

      <div ref={box} className="flex justify-center overflow-hidden rounded-lg border border-border bg-surface">
        {err ? (
          <div className="flex flex-col items-center gap-3 p-6 text-center">
            <p className="text-sm text-muted-foreground">{err}</p>
            <Button variant="secondary" size="sm" onClick={retry}>
              <RefreshCw aria-hidden /> Try again
            </Button>
          </div>
        ) : !srcDoc ? (
          <div className="w-full space-y-3 p-4" aria-label="Loading the sign-in page">
            <Skeleton className="h-6 w-1/3" />
            <Skeleton className="h-64 w-full" />
          </div>
        ) : (
          <div className="animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none">
            <PortalFrame srcDoc={srcDoc} width={spec.w} height={spec.h} scale={scale}
              title={`Client Portal, ${spec.label.toLowerCase()}`} />
          </div>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        {unreadable
          ? "The sign-in methods could not be read, so the preview shows none. Which methods clients see is set in Sign-in methods."
          : methods && optional.length === 0
            ? "The preview shows the sign-in methods your clients actually see, as set in Sign-in methods. No optional sign-in method is switched on here."
            : "The preview shows the sign-in methods your clients actually see, as set in Sign-in methods."}
      </p>
    </section>
  );
}
