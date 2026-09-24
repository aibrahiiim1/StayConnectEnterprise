"use client";

// ISSUE VOUCHERS — a guided print run.
//
// Package → quantity and validity → review → issue → the codes, shown once. Issuing is not a step-up action
// (it creates secrets nobody holds yet rather than revealing existing ones), so there is no password here;
// the operator is recorded from the session by the server.
//
// The one-time nature is real: the issue response is the only moment the codes exist outside the server's
// encryption. They live in this component's state and nowhere else, and closing the dialog drops them.
// Recovering them later is the audited export on the Batches tab.

import * as React from "react";
import { Package, RefreshCw, Ticket } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { Field, Input, Textarea } from "@/components/ui/input";
import { KeyValueGrid, OptionCard, Stepper } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { formatPrice, validityWords, type Grantable, type IssuedBatch } from "@/lib/api/vouchers";
import { CodesHeld } from "./codes-held";

const STEPS = ["Package", "Quantity & validity", "Review"];

/** `datetime-local` value → RFC 3339 in UTC, or undefined when empty. */
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
  const toast = useToast();
  const [grantable, setGrantable] = React.useState<Grantable[] | null>(null);
  const [loadErr, setLoadErr] = React.useState<unknown>(null);
  const [step, setStep] = React.useState(0);
  const [revisionId, setRevisionId] = React.useState("");
  const [count, setCount] = React.useState("10");
  const [validFrom, setValidFrom] = React.useState("");
  const [validUntil, setValidUntil] = React.useState("");
  const [note, setNote] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<unknown>(null);
  const [issued, setIssued] = React.useState<IssuedBatch | null>(null);

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

  // Everything -- above all the codes -- is dropped when the dialog closes.
  React.useEffect(() => {
    if (!open) {
      setStep(0);
      setRevisionId("");
      setCount("10");
      setValidFrom("");
      setValidUntil("");
      setNote("");
      setErr(null);
      setIssued(null);
      setBusy(false);
    }
  }, [open]);

  const chosen = grantable?.find((g) => g.id === revisionId) ?? null;
  const n = Number(count);
  const countProblem = !/^\d+$/.test(count) || n < 1 || n > 500 ? "Enter a whole number from 1 to 500." : null;
  const fromIso = toIso(validFrom);
  const untilIso = toIso(validUntil);
  const windowProblem =
    untilIso && Date.parse(untilIso) <= Date.now()
      ? "Valid until is already in the past. Cards printed with it could never be used."
      : fromIso && untilIso && Date.parse(untilIso) <= Date.parse(fromIso)
        ? "Valid until must be after valid from."
        : null;

  async function issue() {
    if (!chosen || countProblem || windowProblem) return;
    setBusy(true);
    setErr(null);
    try {
      const body: Record<string, unknown> = { package_revision_id: chosen.id, count: n };
      if (note.trim()) body.note = note.trim();
      if (fromIso) body.valid_from = fromIso;
      if (untilIso) body.valid_until = untilIso;
      const out = await api.post<IssuedBatch>("/vouchers/issue", body);
      setIssued(out);
      toast.success(`${out.count} voucher${out.count === 1 ? "" : "s"} issued`, "Print or save the codes before closing.");
      onIssued();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  const validity = validityWords(fromIso ?? null, untilIso ?? null);

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{issued ? `${issued.count.toLocaleString()} voucher${issued.count === 1 ? "" : "s"} issued` : "Issue vouchers"}</DialogTitle>
          <DialogDescription>
            {issued
              ? "These codes are shown once. Copy, download or print them now."
              : "Print a batch of cards a guest redeems for internet access."}
          </DialogDescription>
          {!issued && <Stepper steps={STEPS} current={step} onStep={(i) => !busy && setStep(i)} className="pt-2" />}
        </DialogHeader>
        <DialogBody className="space-y-4">
          <ErrorBanner err={err} />
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
                <p className="text-sm text-muted-foreground">
                  The list of packages a card can grant could not be loaded, so there is nothing to choose from yet.
                </p>
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
                hint="Create and publish an active package under Internet packages, then come back to issue vouchers for it."
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
              <Field label="How many cards" hint="1 to 500 per batch." error={count !== "" ? countProblem : undefined} required>
                <Input type="number" inputMode="numeric" min={1} max={500} value={count} onChange={(e) => setCount(e.target.value)} />
              </Field>
              <div className="hidden sm:block" />
              <Field label="Valid from (optional)" hint="Leave empty to make the cards usable straight away.">
                <Input type="datetime-local" value={validFrom} onChange={(e) => setValidFrom(e.target.value)} />
              </Field>
              <Field label="Valid until (optional)" hint="Leave empty for cards that never expire." error={windowProblem ?? undefined}>
                <Input type="datetime-local" value={validUntil} onChange={(e) => setValidUntil(e.target.value)} />
              </Field>
              <Field label="Note (optional)" hint="For your own reference, e.g. who the cards are for." className="sm:col-span-2">
                <Textarea value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} placeholder="Conference desk, week 12" />
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
                  { label: "Note", value: note.trim() || null, wide: true },
                ]}
              />
              <Callout tone="info">
                The codes appear once, on the next screen. The code format is the one set under Code security.
              </Callout>
            </div>
          )}
        </DialogBody>
        <DialogFooter>
          {issued ? (
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
                <Button disabled={busy || !chosen || !!countProblem || !!windowProblem} onClick={issue}>
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
