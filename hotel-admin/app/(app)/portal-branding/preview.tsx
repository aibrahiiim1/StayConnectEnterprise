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
// ones. What is stubbed is only the appliance the page would otherwise talk to.
//
// SANDBOXING. The frame is `sandbox="allow-scripts"` — an opaque origin with no form submission, no access to
// the admin page around it, and no cookies. It needs no network either: window.fetch is replaced before the
// portal's own script runs, so every call it makes is answered locally from the settings under edit. Images
// are injected as data: URIs for the same reason — /assets/<name> is a GUEST-network path that resolves to
// nothing from the admin origin.

import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Monitor, Smartphone, RefreshCw } from "lucide-react";
import { Design } from "./strings";

type Device = "desktop" | "mobile";

/** The two shapes a captive portal is actually met on.
 *
 *  1100 rather than 1440 for the desktop frame. Both are desktop as far as the portal is concerned — its last
 *  breakpoint is 1024 — but the sign-in card is 960px wide either way, so a 1440 frame scaled into this column
 *  spends a third of its pixels on empty background and leaves the card too small to read. 1100 shows the same
 *  layout with the card filling it. */
const DEVICES: Record<Device, { w: number; h: number; label: string }> = {
  desktop: { w: 1100, h: 820, label: "Desktop" },
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
function buildSrcDoc(html: string, design: Design) {
  const payload = JSON.stringify({ design });
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

export function PortalPreview({ design }: { design: Design }) {
  const [device, setDevice] = useState<Device>("desktop");
  const [html, setHtml] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [assets, setAssets] = useState<Record<string, string>>({});
  const box = useRef<HTMLDivElement>(null);
  const [boxWidth, setBoxWidth] = useState(0);

  // The portal page itself is fetched once. It changes when portald is redeployed, not when a colour is
  // picked, so re-fetching it per keystroke would be a request per character for an unchanging document.
  useEffect(() => {
    let live = true;
    api.get<{ html: string }>("/portal-branding/preview")
      .then((r) => { if (live) { setHtml(r.html); setErr(null); } })
      .catch((e) => { if (live) setErr(e instanceof ApiError ? e.message : "the preview could not be loaded"); });
    return () => { live = false; };
  }, []);

  // Images referenced as /assets/<name> are read back through the operator API and inlined. Fetched per
  // asset, once, and kept — an operator switching between Desktop and Mobile should not re-download a
  // background photograph each time.
  useEffect(() => {
    const wanted = [design.logo_url, design.background_url]
      .filter((u): u is string => !!u && u.startsWith("/assets/"))
      .map((u) => u.slice("/assets/".length))
      .filter((name) => !assets[name]);
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
          if (live) setAssets((p) => ({ ...p, [name]: data }));
        } catch { /* an asset that will not load simply does not appear in the preview */ }
      }
    })();
    return () => { live = false; };
  }, [design.logo_url, design.background_url, assets]);

  const previewDesign = useMemo(() => {
    const inline = (u?: string) => {
      if (!u || !u.startsWith("/assets/")) return u;
      return assets[u.slice("/assets/".length)] ?? undefined;
    };
    return { ...design, logo_url: inline(design.logo_url), background_url: inline(design.background_url) };
  }, [design, assets]);

  const srcDoc = useMemo(() => (html ? buildSrcDoc(html, previewDesign) : null), [html, previewDesign]);

  // The frame renders at the device's REAL pixel width and is scaled down to fit the column, so what is on
  // screen is the true layout rather than a narrow viewport pretending to be a phone. A 390px-wide frame
  // shown at 390px would trigger the desktop rules of a responsive design and show the wrong thing.
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setBoxWidth(el.clientWidth));
    ro.observe(el);
    setBoxWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const spec = DEVICES[device];
  const scale = boxWidth > 0 ? Math.min(1, boxWidth / spec.w) : 0.3;

  return (
    <section className="space-y-3" aria-label="Guest portal preview">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">Preview</h2>
        <div className="inline-flex rounded-md border p-0.5" role="group" aria-label="Preview device">
          {(Object.keys(DEVICES) as Device[]).map((k) => (
            <button
              key={k}
              type="button"
              aria-pressed={device === k}
              onClick={() => setDevice(k)}
              className={`inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs ${
                device === k ? "bg-primary text-primary-foreground" : "text-muted-foreground"
              }`}
            >
              {k === "desktop" ? <Monitor className="h-3.5 w-3.5" /> : <Smartphone className="h-3.5 w-3.5" />}
              {DEVICES[k].label}
            </button>
          ))}
        </div>
      </div>

      <div ref={box} className="overflow-hidden rounded-lg border bg-surface">
        {err ? (
          <p className="p-4 text-sm text-muted-foreground">
            {err} <RefreshCw className="inline h-3.5 w-3.5" />
          </p>
        ) : !srcDoc ? (
          <p className="p-4 text-sm text-muted-foreground">Loading the sign-in page…</p>
        ) : (
          <div style={{ height: spec.h * scale, overflow: "hidden" }}>
            <iframe
              title={`Guest portal, ${spec.label.toLowerCase()}`}
              srcDoc={srcDoc}
              sandbox="allow-scripts"
              style={{
                width: spec.w,
                height: spec.h,
                border: 0,
                transform: `scale(${scale})`,
                transformOrigin: "top left",
              }}
            />
          </div>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        The real sign-in page, rendered with the settings above. Room sign-in, vouchers and personal accounts
        are all shown here so you can check every tab — which of them guests actually see is decided in
        Sign-in methods, not on this page.
      </p>
    </section>
  );
}
