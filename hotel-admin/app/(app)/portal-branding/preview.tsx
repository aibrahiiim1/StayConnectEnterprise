"use client";

// THE PREVIEW IS THE REAL PORTAL.
//
// The obvious way to build this is to draw an approximation of the sign-in page in the admin. That is a second
// implementation of a page whose entire purpose is to be exactly what the guest sees: it would agree on the
// day it was written and drift from then on, and a preview that is subtly wrong is worse than no preview,
// because it is believed.
//
// So edged hands the admin portald's OWN landing page and this renders it in a sandboxed frame with the
// settings currently being edited injected into it. The template, the stylesheet and the script are the real
// ones -- including all six layouts, which is why the template gallery's thumbnails are this same page at a
// small scale rather than pictures of it. What is stubbed is only the appliance the page would otherwise talk to.
//
// SANDBOXING. The frame is `sandbox="allow-scripts"` — an opaque origin with no form submission, no access to
// the admin page around it, and no cookies. It needs no network either: window.fetch is replaced before the
// portal's own script runs, so every call it makes is answered locally from the settings under edit. Images
// are injected as data: URIs for the same reason — /assets/<name> is a GUEST-network path that resolves to
// nothing from the admin origin.
//
// WHAT THE FRAME SHOWS FOR THE ADVANCED FIELDS. When the server has answered /validate, the preview renders
// the SANITISED fragment and stylesheet -- what a guest would actually receive -- rather than the raw text in
// the editor. An operator who pastes an <img onerror> sees the image without the handler, which is the truth.

import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Monitor, Smartphone, Tablet, RefreshCw } from "lucide-react";
import { Segmented } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/misc";
import { Design } from "./strings";

type Device = "desktop" | "tablet" | "mobile";

/** The shapes a captive portal is actually met on.
 *
 *  1180 rather than 1440 for the desktop frame. Both are desktop as far as the portal is concerned — its last
 *  breakpoint is 1024 — but a 1440 frame scaled into this column spends a third of its pixels on empty
 *  background and leaves the card too small to read. */
export const DEVICES: Record<Device, { w: number; h: number; label: string }> = {
  desktop: { w: 1180, h: 820, label: "Desktop" },
  tablet: { w: 820, h: 1100, label: "Tablet" },
  mobile: { w: 390, h: 844, label: "Mobile" },
};

/** Sign-in methods the preview assumes. The real page asks the appliance; the preview shows the two groups so
 *  an operator can see both tabs and the strings inside them without changing what is enabled for guests. */
const PREVIEW_METHODS = {
  pms: { enabled: true, mode: "room_any" },
  voucher: { enabled: true },
  guest_account: { enabled: true },
  internet_packages_available: true,
};

/** buildSrcDoc injects the stub and the settings ahead of the portal's own script. */
export function buildSrcDoc(html: string, design: Design) {
  // "</" is escaped so no value in the design can close the shim's <script> element early.
  const payload = JSON.stringify({ design }).replace(/<\//g, "<\\/");
  const methods = JSON.stringify(PREVIEW_METHODS);
  const shim = `<script>
(function () {
  var BRANDING = ${payload};
  // The portal's stylesheet names a default background photograph. In a preview frame that URL resolves
  // against the ADMIN origin, where it is not an image and answers a login redirect — a request this frame
  // has no business making. With no background set, the gradient underneath is what a guest would see on an
  // appliance that has none either, so pointing at nothing is also the honest preview.
  if (!BRANDING.design || !BRANDING.design.background_url) {
    document.documentElement.style.setProperty("--sc-bg", "none");
  }
  var METHODS = ${methods};
  function ok(body) {
    return Promise.resolve({ ok: true, status: 200, json: function () { return Promise.resolve(body); } });
  }
  // Every call the sign-in page makes, answered from the settings under edit. Anything unrecognised resolves
  // as a failure rather than hanging, because the page's own catch branches are part of what is being
  // previewed.
  window.fetch = function (input) {
    var u = String((input && input.url) || input || "");
    if (u.indexOf("/api/branding") >= 0) return ok(BRANDING);
    if (u.indexOf("/api/auth-methods") >= 0) return ok(METHODS);
    if (u.indexOf("/access/status") >= 0) return ok({});
    return Promise.resolve({ ok: false, status: 404, json: function () { return Promise.resolve({}); } });
  };
  // A preview must not navigate. The forms cannot submit (no allow-forms) but the script also assigns
  // window.location on success, and in a preview there is no success to reach.
  try {
    Object.defineProperty(window, "location", { value: window.location, writable: false });
  } catch (e) { /* older engines: the sandbox is still the real guard */ }
})();
</script>`;
  // Before </head>, so the stub is installed before the page's own script at the end of <body> runs.
  return html.includes("</head>") ? html.replace("</head>", shim + "</head>") : shim + html;
}

// ONE FETCH OF THE PORTAL PAGE, SHARED. The main preview and six gallery thumbnails all render the same
// document; it changes when portald is redeployed, not when a colour is picked.
let portalPage: Promise<string> | null = null;

export function usePortalHTML() {
  const [html, setHtml] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    if (!portalPage) {
      portalPage = api.get<{ html: string }>("/portal-branding/preview").then((r) => r.html);
      portalPage.catch(() => { portalPage = null; });
    }
    portalPage
      .then((h) => { if (live) { setHtml(h); setErr(null); } })
      .catch((e) => { if (live) setErr(e instanceof ApiError ? e.message : "the preview could not be loaded"); });
    return () => { live = false; };
  }, []);
  return { html, err };
}

const assetCache = new Map<string, string>();

/** Images referenced as /assets/<name> are read back through the operator API and inlined. Fetched per asset,
 *  once, and kept -- switching device or template should not re-download a background photograph. */
export function useInlinedDesign(design: Design): Design {
  const [loaded, bump] = useState(0);
  const urls = [design.logo_url, design.background_url, design.hero_image_url];
  const key = urls.join("|");
  useEffect(() => {
    const wanted = urls
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
        } catch { /* an asset that will not load simply does not appear in the preview */ }
      }
    })();
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
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

/** The value, once it has stopped changing for `ms`. Frames reload when their document changes, so they follow
 *  the operator's edits a moment after the typing stops rather than on every keystroke. */
export function useSettled<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return settled;
}

/** A frame rendered at the device's REAL pixel width and scaled to fit, so what is on screen is the true layout
 *  rather than a narrow viewport pretending to be a phone. */
export function PortalFrame({
  srcDoc, width, height, scale, title, interactive = true,
}: {
  srcDoc: string; width: number; height: number; scale: number; title: string; interactive?: boolean;
}) {
  return (
    <div style={{ height: height * scale, width: width * scale, overflow: "hidden" }}>
      <iframe
        title={title}
        srcDoc={srcDoc}
        sandbox="allow-scripts"
        tabIndex={interactive ? undefined : -1}
        aria-hidden={interactive ? undefined : true}
        loading={interactive ? undefined : "lazy"}
        style={{
          width, height, border: 0, transform: `scale(${scale})`, transformOrigin: "top left",
          pointerEvents: interactive ? undefined : "none",
        }}
      />
    </div>
  );
}

export function PortalPreview({ design, sanitized }: {
  design: Design;
  /** The server's sanitised Advanced fields, when it has answered for the current text. */
  sanitized?: { custom_css: string; custom_html: string } | null;
}) {
  const [device, setDevice] = useState<Device>("desktop");
  const { html, err } = usePortalHTML();
  const box = useRef<HTMLDivElement>(null);
  const [boxWidth, setBoxWidth] = useState(0);

  // A light re-render: the frame reloads a quarter-second after the operator stops changing things, not on
  // every keystroke of a hex value.
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
  const srcDoc = useMemo(() => (html ? buildSrcDoc(html, shown) : null), [html, shown]);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setBoxWidth(el.clientWidth));
    ro.observe(el);
    setBoxWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const spec = DEVICES[device];
  // Tall devices are also bounded by height, so a tablet does not become a scroll of its own.
  const scale = boxWidth > 0 ? Math.min(1, boxWidth / spec.w, device === "desktop" ? 1 : 760 / spec.h) : 0.3;

  return (
    <section className="space-y-3" aria-label="Guest portal preview">
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
          <p className="p-4 text-sm text-muted-foreground">
            {err} <RefreshCw className="inline h-3.5 w-3.5" aria-hidden />
          </p>
        ) : !srcDoc ? (
          <div className="w-full space-y-3 p-4" aria-label="Loading the sign-in page">
            <Skeleton className="h-6 w-1/3" />
            <Skeleton className="h-64 w-full" />
          </div>
        ) : (
          <PortalFrame srcDoc={srcDoc} width={spec.w} height={spec.h} scale={scale}
            title={`Guest portal, ${spec.label.toLowerCase()}`} />
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        Room sign-in, vouchers and personal accounts are all shown here so you can check every tab — which of
        them guests actually see is decided in Sign-in methods, not on this page.
      </p>
    </section>
  );
}
