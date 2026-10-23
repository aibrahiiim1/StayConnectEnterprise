"use client";

// Print only the transient voucher sheet; the portal and its codes disappear after the print job.
// Force light colors because dark-mode tokens print unreadable text on white paper.

import * as React from "react";
import { createPortal } from "react-dom";
import { Wifi } from "lucide-react";
import { cn } from "@/lib/utils";

export type PrintJob = {
  heading: string;
  packageName: string;
  validity: string;
  codes: string[];
};

const PRINT_CSS = `
@media screen { .voucher-print-root { display: none !important; } }
@media print {
  @page { size: A4 portrait; margin: 12mm; }
  body > *:not(.voucher-print-root) { display: none !important; }
  html, body { background: Canvas !important; }
  .voucher-print-root { display: block !important; color-scheme: light; color: CanvasText; background: Canvas;
    -webkit-print-color-adjust: exact; print-color-adjust: exact; }
  .voucher-print-root .vp-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr));
    grid-auto-rows: 65mm; gap: 3mm; }
  .voucher-print-root .vp-card { height: 65mm; box-sizing: border-box; overflow: hidden; border-radius: 0 !important;
    break-inside: avoid; page-break-inside: avoid; box-shadow: none !important;
    border: 0.3mm dashed CanvasText !important; background: Canvas !important; color: CanvasText !important; }
  .voucher-print-root .vp-card * { color: CanvasText !important; border-color: CanvasText !important;
    background: transparent !important; box-shadow: none !important; }
  .voucher-print-root .vp-quiet, .voucher-print-root .vp-quiet * { color: GrayText !important; }
}
`;

function CutMarks() {
  const mark = "pointer-events-none absolute size-2.5 border-foreground/60";
  return (
    <span aria-hidden>
      <span className={cn(mark, "start-1 top-1 border-s border-t")} />
      <span className={cn(mark, "end-1 top-1 border-e border-t")} />
      <span className={cn(mark, "bottom-1 start-1 border-b border-s")} />
      <span className={cn(mark, "bottom-1 end-1 border-b border-e")} />
    </span>
  );
}

export function VoucherCardFace({
  heading,
  packageName,
  validity,
  code,
  className,
}: {
  heading: string;
  packageName: string;
  validity: string;
  code: string;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "vp-card relative flex flex-col gap-1.5 rounded-lg border border-dashed border-border-strong bg-card px-5 py-3.5 text-card-foreground",
        className,
      )}
    >
      <CutMarks />
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="break-words text-base font-bold leading-tight">{heading || "Wi-Fi Access"}</div>
          <div className="vp-quiet mt-0.5 text-[0.625rem] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            Internet access voucher
          </div>
        </div>
        <Wifi className="size-5 shrink-0" aria-hidden />
      </div>

      <div className="text-sm font-medium leading-snug">{packageName}</div>

      <div className="rounded-md border-[1.5px] border-foreground px-3 py-2 text-center">
        <div className="vp-quiet text-[0.5625rem] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
          Your code
        </div>
        <div className="font-mono text-2xl font-bold leading-tight tracking-[0.22em]">{code}</div>
      </div>

      <div className="text-xs">
        <span className="font-semibold">Valid:</span> {validity}
      </div>

      <div className="mt-auto">
        <div className="text-[0.625rem] font-bold uppercase tracking-[0.12em]">How to connect</div>
        <ol className="mt-0.5 list-decimal space-y-px ps-4 text-[0.6875rem] leading-snug">
          <li>Join the Wi-Fi network.</li>
          <li>Open any web page; the sign-in page appears.</li>
          <li>Choose the voucher option and enter the code above.</li>
        </ol>
      </div>

      <div className="vp-quiet absolute bottom-1.5 end-4 text-[0.5rem] tracking-[0.08em] text-muted-foreground">
        OneGate
      </div>
    </div>
  );
}

/** Drop the transient print job when the browser closes its print dialog. */
export function PrintCards({ job, onDone }: { job: PrintJob | null; onDone: () => void }) {
  const [host, setHost] = React.useState<HTMLElement | null>(null);
  const done = React.useRef(onDone);
  done.current = onDone;

  React.useEffect(() => {
    if (!job) return;
    const el = document.createElement("div");
    el.className = "voucher-print-root";
    document.body.appendChild(el);
    setHost(el);
    return () => {
      el.remove();
      setHost(null);
    };
  }, [job]);

  React.useEffect(() => {
    if (!job || !host) return;
    let finished = false;
    const finish = () => {
      if (finished) return;
      finished = true;
      window.removeEventListener("afterprint", finish);
      done.current();
    };
    window.addEventListener("afterprint", finish);
    // Let the portal paint before the browser snapshots the print sheet.
    const t = window.setTimeout(() => {
      try {
        window.print();
      } finally {
        // Some browsers block in print(); others signal completion with afterprint.
        window.setTimeout(finish, 1000);
      }
    }, 50);
    return () => {
      window.clearTimeout(t);
      window.removeEventListener("afterprint", finish);
    };
  }, [job, host]);

  if (!job || !host) return null;
  return createPortal(
    <>
      <style>{PRINT_CSS}</style>
      <div className="vp-grid" data-testid="voucher-print-sheet">
        {job.codes.map((c, i) => (
          <VoucherCardFace key={`${c}-${i}`} heading={job.heading} packageName={job.packageName} validity={job.validity} code={c} />
        ))}
      </div>
    </>,
    host,
  );
}
