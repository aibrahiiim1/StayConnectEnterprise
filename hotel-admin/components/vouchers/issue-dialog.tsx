"use client";

// Issuing bulk access requires password step-up; codes remain in component state until the dialog closes.
// Later recovery requires an audited export.

import * as React from "react";
import Link from "next/link";
import { Package, RefreshCw, Ticket } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { KeyValueGrid, OptionCard, Stepper } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { InfoHint } from "@/components/ui/quiet";
import { formatPrice, validityWords, type Grantable, type IssuedBatch } from "@/lib/api/vouchers";
import { CodesHeld } from "./codes-held";
import { stepUpError } from "./shared";

const STEPS = ["Package", "Quantity & validity", "Review"];

const browserTimeZone = (): string => {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || "this computer's time zone"; }
  catch { return "this computer's time zone"; }
};

function toIso(local: string): string | undefined {
  if (!local) return undefined;
  const t = new Date(local);
  return Number.isNaN(t.getTime()) ? undefined : t.toISOString();
}

export function IssueDialog({
  open,
  onOpenChange,
  onIssued,
  hotelName,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onIssued: () => void;
  hotelName?: string;
}) {
  const [grantable, setGrantable] = React.useState<Grantable[] | null>(null);
  const [loadErr, setLoadErr] = React.useState<unknown>(null);
  const [step, setStep] = React.useState(0);
  const [revisionId, setRevisionId] = React.useState("");
  const [count, setCount] = React.useState("10");
  const [validFrom, setValidFrom] = React.useState("");
  const [validUntil, setValidUntil] = React.useState("");
  const [note, setNote] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<unknown>(null);
  const [issued, setIssued] = React.useState<IssuedBatch | null>(null);
  const [askClose, setAskClose] = React.useState(false);
  const keepRef = React.useRef<HTMLButtonElement>(null);
  React.useEffect(() => {
    if (askClose) keepRef.current?.focus();
  }, [askClose]);
  const tz = browserTimeZone();

  const loadGrantable = React.useCallback(() => {
    setLoadErr(null);
    setGrantable(null);
    api
      .get<{ revisions?: Grantable[] }>("/vouchers/grantable")
      .then((m) => setGrantable(m.revisions ?? []))
      .catch((e) => setLoadErr(e));
  }, []);

  React.useEffect(() => {
    if (open) loadGrantable();
  }, [open, loadGrantable]);

  // Drop the issued codes when the dialog closes.
  React.useEffect(() => {
    if (!open) {
      setStep(0);
      setRevisionId("");
      setCount("10");
      setValidFrom("");
      setValidUntil("");
      setNote("");
      setPassword("");
      setErr(null);
      setIssued(null);
      setBusy(false);
      setAskClose(false);
    }
  }, [open]);

  const chosen = grantable?.find((g) => g.id === revisionId) ?? null;
  const n = Number(count);
  const countProblem = count.trim() === "" ? "Enter how many cards to print, from 1 to 500."
    : !/^\d+$/.test(count) || n < 1 || n > 500 ? "Enter a whole number from 1 to 500." : null;
  const fromIso = toIso(validFrom);
  const untilIso = toIso(validUntil);
  const windowProblem =
    untilIso && Date.parse(untilIso) <= Date.now()
      ? "Valid until is already in the past. Cards printed with it could never be used."
      : fromIso && untilIso && Date.parse(untilIso) <= Date.parse(fromIso)
        ? "Valid until must be after valid from."
        : null;
  const reachable = !chosen ? 0 : countProblem || windowProblem ? 1 : 2;

  async function issue() {
    if (!chosen || countProblem || windowProblem || !password) return;
    setBusy(true);
    setErr(null);
    try {
      const body: Record<string, unknown> = { package_revision_id: chosen.id, count: n, password };
      if (note.trim()) body.note = note.trim();
      if (fromIso) body.valid_from = fromIso;
      if (untilIso) body.valid_until = untilIso;
      const out = await api.post<IssuedBatch>("/vouchers/issue", body);
      setIssued(out);
      setPassword("");
      onIssued();
    } catch (e) {
      // Preserve the server's reason when the selected package cannot be redeemed by voucher.
      setErr(stepUpError(e, "No vouchers were issued."));
    } finally {
      setBusy(false);
    }
  }

  const validity = validityWords(fromIso ?? null, untilIso ?? null);

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (busy) return;
        if (!v && issued) { setAskClose(true); return; }
        onOpenChange(v);
      }}
    >
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{issued ? `${issued.count.toLocaleString()} voucher${issued.count === 1 ? "" : "s"} issued` : "Issue vouchers"}</DialogTitle>
          <DialogDescription className={issued ? undefined : "sr-only"}>
            {issued
              ? "These codes are shown once. Copy, download or print them now."
              : "Print a batch of cards a client redeems for internet access."}
          </DialogDescription>
          {!issued && <Stepper steps={STEPS} current={step} onStep={(i) => !busy && i <= reachable && setStep(i)} className="pt-2" />}
        </DialogHeader>
        <DialogBody className="space-y-4">
          <ErrorBanner err={err} />
          <div key={issued ? "issued" : step} className="space-y-4 animate-in fade-in-0 slide-in-from-right-2 duration-base ease-onegate rtl:slide-in-from-left-2 motion-reduce:animate-none">
          {issued ? (
            <CodesHeld
              codes={issued.codes.map((c) => ({ code: c }))}
              packageName={chosen?.name ?? ""}
              validity={validity}
              batchId={issued.batch_id}
              defaultHeading={hotelName}
              warning={
                <>
                  These codes will <strong>not be shown again</strong> here. Getting them back later is an
                  export on the Batches tab, which needs your password and a reason and is recorded against your
                  name.
                </>
              }
            />
          ) : step === 0 ? (
            loadErr ? (
              <div className="space-y-3">
                <ErrorBanner err={loadErr} />
                <p className="text-sm text-muted-foreground">The packages could not be loaded.</p>
                <Button variant="secondary" size="sm" onClick={loadGrantable}>
                  <RefreshCw /> Try again
                </Button>
              </div>
            ) : grantable === null ? (
              <div className="grid gap-3 sm:grid-cols-2" aria-busy="true">
                <Skeleton className="h-20" />
                <Skeleton className="h-20" />
              </div>
            ) : grantable.length === 0 ? (
              <EmptyState
                icon={<Package />}
                title="No internet package can be put on a card yet"
                hint={
                  <>
                    Vouchers are issued only for an active package that clients may get by Voucher. Under{" "}
                    <Link href="/internet-packages" className="underline">Internet packages</Link>, tick Voucher on the
                    package, then come back to issue cards for it. Where a card can be used is set by the package (some
                    packages are limited to certain networks or PMS connections) and by Sign-in methods.
                  </>
                }
              />
            ) : (
              <div role="radiogroup" aria-label="Package the cards grant" className="grid gap-3 sm:grid-cols-2">
                {grantable.map((g) => (
                  <OptionCard
                    key={g.id}
                    name="voucher-package"
                    value={g.id}
                    checked={revisionId === g.id}
                    onChange={setRevisionId}
                    icon={<Ticket />}
                    title={g.name}
                    badge={<span className="text-xs font-medium text-muted-foreground">{formatPrice(g.price_minor, g.currency, g.currency_exponent)}</span>}
                    description={`${g.package_code} · version ${g.revision_no}`}
                  />
                ))}
              </div>
            )
          ) : step === 1 ? (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="How many cards" hint="1 to 500 per batch." error={countProblem ?? undefined} required>
                <Input type="number" inputMode="numeric" min={1} max={500} value={count} onChange={(e) => setCount(e.target.value)} />
              </Field>
              <div className="hidden sm:block" />
              <Field label="Valid from (optional)" hint={`Leave empty to make the cards usable straight away. In ${tz}.`}>
                <Input type="datetime-local" value={validFrom} onChange={(e) => setValidFrom(e.target.value)} />
              </Field>
              <Field label="Valid until (optional)" hint={`Leave empty for cards that never expire. In ${tz}.`} error={windowProblem ?? undefined}>
                <Input type="datetime-local" value={validUntil} onChange={(e) => setValidUntil(e.target.value)} />
              </Field>
              <Field label="Batch name (optional)" className="sm:col-span-2">
                <Input value={note} maxLength={120} onChange={(e) => setNote(e.target.value)} placeholder="Visitor desk, week 12" />
              </Field>
            </div>
          ) : (
            <div className="space-y-4">
              <KeyValueGrid
                items={[
                  { label: "Package", value: chosen?.name },
                  { label: "Price per card", value: chosen ? formatPrice(chosen.price_minor, chosen.currency, chosen.currency_exponent) : null },
                  { label: "Cards", value: n.toLocaleString() },
                  { label: "Validity", value: validity },
                  { label: "Batch name", value: note.trim() || null, wide: true },
                ]}
              />
              <p className="flex items-center gap-1 text-sm text-muted-foreground">
                Codes are shown once, on the next screen.
                <InfoHint>The code format is the one set under Code security.</InfoHint>
              </p>
              <Field label="Confirm your password" required
                htmlFor="voucher-issue-password">
                <Input id="voucher-issue-password" type="password" autoComplete="current-password" value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      void issue();
                    }
                  }} />
              </Field>
            </div>
          )}
          </div>
        </DialogBody>
        <DialogFooter>
          {issued && askClose ? (
            <>
              <p role="status" className="me-auto text-caption text-foreground">
                Close without keeping these codes? They are not shown again here.
              </p>
              <Button ref={keepRef} variant="secondary" onClick={() => setAskClose(false)}>Keep them open</Button>
              <Button variant="danger" onClick={() => onOpenChange(false)}>Close anyway</Button>
            </>
          ) : issued ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              <Button variant="ghost" disabled={busy} onClick={() => (step === 0 ? onOpenChange(false) : setStep(step - 1))}>
                {step === 0 ? "Cancel" : "Back"}
              </Button>
              {step === 0 && (
                <Button disabled={!chosen} onClick={() => setStep(1)}>
                  Next
                </Button>
              )}
              {step === 1 && (
                <Button disabled={!!countProblem || !!windowProblem} onClick={() => setStep(2)}>
                  Review
                </Button>
              )}
              {step === 2 && (
                <Button disabled={busy || !chosen || !!countProblem || !!windowProblem || !password} onClick={issue}>
                  {busy ? "Issuing…" : `Issue ${n.toLocaleString()} voucher${n === 1 ? "" : "s"}`}
                </Button>
              )}
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
