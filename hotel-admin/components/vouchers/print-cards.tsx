"use client";

// THE PRINTABLE VOUCHER SHEET.
//
// "Print" used to call window.print() on the admin page itself: the sidebar, the header, the tables and a
// plain list of codes, with no card layout at all. This prints ONLY a sheet of cut-out cards.
//
// How: while a print job is active, the sheet is portalled to the end of <body> and a print-only stylesheet
// hides every other child of <body>. On screen the portal is hidden, so nothing flashes. The codes come
// from the caller's memory -- the one-time issue response or a step-up export that was just recorded -- and
// are never written to storage, a URL or the DOM outside this job. The job is cleared as soon as the browser
// reports the print dialog closed.
//
// COLOURS ON PAPER. The admin may be in dark mode, and its tokens would print light text for a dark card on
// white paper. The print stylesheet therefore switches the sheet to the light system palette (`Canvas` /
// `CanvasText` under `color-scheme: light`) instead of carrying colours of its own.

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
  @page { margin: 12mm; }
  body > *:not(.voucher-print-root) { display: none !important; }
  html, body { background: Canvas !important; }
  .voucher-print-root { display: block !important; color-scheme: light; color: CanvasText; background: Canvas; }
  .voucher-print-root .vp-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6mm; }
  .voucher-print-root .vp-card { break-inside: avoid; page-break-inside: avoid; border: 1px dashed GrayText !important;
    background: Canvas !important; color: CanvasText !important; }
  .voucher-print-root .vp-card * { color: CanvasText !important; border-color: GrayText !important; background: transparent !important; }
}
`;

/** One card face. Used both for the on-screen preview and on paper. */
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
    <div className={cn("vp-card space-y-2 rounded-lg border border-dashed border-border-strong bg-card p-4", className)}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold">{heading || "Guest Wi-Fi"}</div>
          <div className="text-2xs uppercase tracking-widest text-muted-foreground">Internet access voucher</div>
        </div>
        <Wifi className="size-5 shrink-0 text-muted-foreground" aria-hidden />
      </div>
      <div className="text-xs text-muted-foreground">{packageName}</div>
      <div className="rounded-md border border-border px-3 py-2 text-center font-mono text-xl font-semibold tracking-[0.25em]">
        {code}
      </div>
      <div className="text-xs text-muted-foreground">Valid: {validity}</div>
      <ol className="list-decimal space-y-0.5 pl-4 text-2xs leading-relaxed text-muted-foreground">
        <li>Connect to the hotel Wi-Fi.</li>
        <li>Open any web page; the sign-in page appears.</li>
        <li>Choose the voucher option and enter the code above.</li>
      </ol>
    </div>
  );
}

/**
 * Renders nothing on screen. While `job` is set it mounts the sheet for print, opens the browser's print
 * dialog, and calls `onDone` once that dialog closes -- which is the caller's cue to drop the job.
 */
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
    // Let the portal paint before the print snapshot is taken.
    const t = window.setTimeout(() => {
      try {
        window.print();
      } finally {
        // Browsers whose print() blocks have already printed by now; those that do not fire afterprint.
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
