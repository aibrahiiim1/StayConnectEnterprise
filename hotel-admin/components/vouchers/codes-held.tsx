"use client";

// CODES THE OPERATOR HOLDS RIGHT NOW: the one-time issue response, or a batch export that was just recorded.
//
// Everything here works on the codes in memory. Copy puts them on the clipboard, CSV builds a file in the
// browser, Print lays out cards -- none of them sends a code anywhere or stores one. When the surrounding
// dialog or sheet closes, this component unmounts and the codes go with it.

import * as React from "react";
import { Copy, Download, Printer } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Callout } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { useToast } from "@/components/ui/toast";
import { codesCsv, downloadCsv } from "@/lib/api/vouchers";
import { PrintCards, VoucherCardFace, type PrintJob } from "./print-cards";

export function CodesHeld({
  codes,
  packageName,
  validity,
  batchId,
  defaultHeading,
  warning,
}: {
  codes: { code: string; id?: string }[];
  packageName: string;
  validity: string;
  batchId: string;
  defaultHeading?: string;
  warning: React.ReactNode;
}) {
  const toast = useToast();
  const [heading, setHeading] = React.useState(defaultHeading || "Guest Wi-Fi");
  const [job, setJob] = React.useState<PrintJob | null>(null);
  const headingId = React.useId();

  async function copyAll() {
    try {
      await navigator.clipboard.writeText(codes.map((c) => c.code).join("\n"));
      toast.success(`${codes.length} code${codes.length === 1 ? "" : "s"} copied`);
    } catch {
      toast.error("The browser did not allow copying", "Select the codes and copy them by hand, or download the CSV.");
    }
  }

  function csv() {
    downloadCsv(`vouchers-${batchId.slice(0, 8)}.csv`, codesCsv(codes, { package: packageName, validity }));
    toast.success("CSV downloaded", "The file is on this computer only. Treat it like the printed cards.");
  }

  return (
    <div className="space-y-4">
      <Callout tone="warning" title="Keep these codes now">
        {warning}
      </Callout>
      <div
        aria-label={`${codes.length} voucher codes`}
        className="grid max-h-64 grid-cols-2 gap-1.5 overflow-y-auto rounded-md border border-border bg-surface p-2 sm:grid-cols-3"
      >
        {codes.map((c, i) => (
          <div
            key={`${c.code}-${i}`}
            data-testid="held-code"
            className="rounded bg-card px-2 py-1 text-center font-mono text-sm tracking-widest"
          >
            {c.code}
          </div>
        ))}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="sm" onClick={copyAll}>
          <Copy /> Copy all
        </Button>
        <Button variant="secondary" size="sm" onClick={csv}>
          <Download /> Download CSV
        </Button>
        <Button
          size="sm"
          onClick={() => setJob({ heading, packageName, validity, codes: codes.map((c) => c.code) })}
          disabled={job !== null}
        >
          <Printer /> Print cards
        </Button>
      </div>
      <details className="rounded-md border border-border p-3">
        <summary className="cursor-pointer text-sm font-medium">Card layout</summary>
        <div className="mt-3 grid gap-4 sm:grid-cols-2">
          <Field label="Heading printed on each card" htmlFor={headingId}>
            <Input id={headingId} value={heading} maxLength={60} onChange={(e) => setHeading(e.target.value)} />
          </Field>
          <VoucherCardFace
            heading={heading}
            packageName={packageName}
            validity={validity}
            code={codes[0]?.code ?? ""}
          />
        </div>
      </details>
      <PrintCards job={job} onDone={() => setJob(null)} />
    </div>
  );
}
