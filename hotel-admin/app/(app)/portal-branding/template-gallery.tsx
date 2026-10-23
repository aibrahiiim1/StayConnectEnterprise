"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { OptionCard } from "@/components/ui/data";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/misc";
import { PORTAL_TEMPLATES, type TemplateId } from "@/lib/api/portal-design";
import { Design } from "./strings";
import { PortalFrame, buildSrcDoc, usePortalHTML, useInlinedDesign, usePreviewMethods, useSettled, type AuthMethodsDoc } from "./preview";

const THUMB = { w: 1180, h: 820 };

export function TemplateGallery({ design, value, onChange, disabled }: {
  design: Design;
  value: TemplateId;
  onChange: (id: TemplateId) => void;
  disabled?: boolean;
}) {
  const { html } = usePortalHTML();
  // Same methods as the main preview, so a thumbnail never shows a method the site cannot offer.
  const { methods } = usePreviewMethods();
  const inlined = useInlinedDesign(useSettled(design, 700));
  // Advanced custom CSS/HTML is left out: thumbnails compare layouts only.
  const perTemplate = useMemo(() => {
    const base: Design = { ...inlined, custom_css: undefined, custom_html: undefined };
    return Object.fromEntries(PORTAL_TEMPLATES.map((t) => [t.id, { ...base, template_id: t.id }])) as Record<TemplateId, Design>;
  }, [inlined]);

  return (
    <div role="radiogroup" aria-label="Page template" className="grid gap-3 sm:grid-cols-2">
      {PORTAL_TEMPLATES.map((t) => (
        <OptionCard
          key={t.id}
          name="portal-template"
          value={t.id}
          checked={value === t.id}
          onChange={(v) => onChange(v as TemplateId)}
          disabled={disabled}
          title={t.name}
          description={t.description}
          badge={t.id === "classic" ? <Badge tone="neutral">Default</Badge> : value === t.id ? <Badge tone="accent">In use</Badge> : undefined}
        >
          <Thumbnail html={html} methods={methods} design={perTemplate[t.id]} name={t.name} />
        </OptionCard>
      ))}
    </div>
  );
}

function Thumbnail({ html, methods, design, name }: {
  html: string | null; methods: AuthMethodsDoc | null; design: Design; name: string;
}) {
  const srcDoc = useMemo(() => (html && methods ? buildSrcDoc(html, design, methods) : null), [html, design, methods]);
  // The whole desktop page is scaled to the card width so the layout is never clipped.
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = box.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);
  const scale = width > 0 ? width / THUMB.w : 0.2;
  return (
    <div ref={box} className="overflow-hidden rounded-md border border-border bg-surface" style={{ height: THUMB.h * scale }} aria-hidden>
      {srcDoc
        ? <PortalFrame srcDoc={srcDoc} width={THUMB.w} height={THUMB.h} scale={scale}
            title={`Template thumbnail: ${name}`} interactive={false} />
        : <Skeleton className="h-full w-full" />}
    </div>
  );
}
