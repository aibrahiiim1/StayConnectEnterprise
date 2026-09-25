"use client";

// THE TEMPLATE GALLERY.
//
// Each card shows the REAL portal page in that layout, rendered with this hotel's own name, colours and images
// at a small scale -- not a stock illustration of what a layout might look like. An operator choosing between
// "Split" and "Resort" is looking at their own sign-in page both ways.
//
// Choosing a template changes layout only. Every sign-in method, every form and every translation works the same
// under all of them, and the choice saves without a password: it is a closed list, not markup.

import { useEffect, useMemo, useRef, useState } from "react";
import { OptionCard } from "@/components/ui/data";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/misc";
import { PORTAL_TEMPLATES, type TemplateId } from "@/lib/api/portal-design";
import { Design } from "./strings";
import { PortalFrame, buildSrcDoc, usePortalHTML, useInlinedDesign, useSettled } from "./preview";

const THUMB = { w: 1180, h: 820 };

export function TemplateGallery({ design, value, onChange, disabled }: {
  design: Design;
  value: TemplateId;
  onChange: (id: TemplateId) => void;
  disabled?: boolean;
}) {
  const { html } = usePortalHTML();
  const inlined = useInlinedDesign(useSettled(design, 700));
  // One design per layout, rebuilt only when the hotel's design has settled. The Advanced fields are left out
  // of the thumbnails: they are the hotel's own additions, and the thumbnails are about the layouts.
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
          <Thumbnail html={html} design={perTemplate[t.id]} name={t.name} />
        </OptionCard>
      ))}
    </div>
  );
}

function Thumbnail({ html, design, name }: { html: string | null; design: Design; name: string }) {
  const srcDoc = useMemo(() => (html ? buildSrcDoc(html, design) : null), [html, design]);
  // The whole desktop page, scaled to the card's width: the card is narrower in a three-column designer than
  // on a phone, and a thumbnail that clips its own layout is not a preview of it.
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
