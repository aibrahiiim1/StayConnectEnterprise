"use client";

// Phase 4 (DARK) — Recovery.
//
// This screen exists for the worst day: the database has been restored, and nobody yet knows which of the
// payments and postings that were in flight actually completed out in the world.
//
// The design follows from that. There is no "resume", no "retry all", and no way to release recovery with
// work still unaccounted for -- because the one thing that must not happen is charging a guest twice for
// the same night's internet. Every button here records what an operator ESTABLISHED; none of them re-sends
// anything.
//
// Each decision takes a password and a written account of how it was established, for the same reason a
// Manual review decision does: this is an assertion about real money and it should carry a name.
//
// A role that may not decide sees the same state and items, and no form, password field or button at all.

import { useCallback, useEffect, useState } from "react";
import { CircleCheck, LifeBuoy, ShieldAlert } from "lucide-react";
import {
  api,
  RECOVERY_RESOLUTIONS,
  RecoveryHold,
  RecoveryResolution,
  RecoveryStatus,
  ZeroAttemptQueue,
  ZeroAttemptRow,
  surfaceUnavailableMessage,
} from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { MonoId, Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { formatDate } from "@/lib/utils";
import { humanize, money } from "./format";

const RESOLUTION_TEXT: Record<RecoveryResolution, string> = {
  CONFIRMED_COMPLETED: "It already completed — confirmed against the provider or the folio",
  CONFIRMED_NOT_COMPLETED: "It never completed — confirmed nothing was posted or charged",
  ABANDONED: "Abandon it — no longer required, and nothing will be done about it",
  ESCALATED: "Escalate — this needs someone else before it can be concluded",
};

const KIND_TEXT: Record<RecoveryHold["work_kind"], string> = {
  POSTING_OUTBOX: "PMS posting",
  PAYMENT_TRANSACTION: "Payment",
  SETTLEMENT: "Settlement",
};

export function FinancialRecoveryView({ canAct = true }: { canAct?: boolean }) {
  const [status, setStatus] = useState<RecoveryStatus | null>(null);
  const [holds, setHolds] = useState<RecoveryHold[] | null>(null);
  const [zero, setZero] = useState<ZeroAttemptQueue | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  // Per-hold decision state. Kept keyed by hold so two half-filled decisions cannot bleed into each other.
  const [choice, setChoice] = useState<Record<string, RecoveryResolution>>({});
  const [evidence, setEvidence] = useState<Record<string, string>>({});
  const [password, setPassword] = useState("");

  // The zero-attempt authorization form, keyed per posting for the same reason: two half-written
  // authorizations about two different charges must not be able to merge into one.
  const [zaReason, setZaReason] = useState<Record<string, string>>({});
  const [zaSource, setZaSource] = useState<Record<string, string>>({});
  const [zaRef, setZaRef] = useState<Record<string, string>>({});

  const load = useCallback(async () => {
    try {
      const [s, h, z] = await Promise.all([
        api.get<{ recovery: RecoveryStatus }>("/financial-ops/recovery"),
        api.get<{ holds: RecoveryHold[] }>("/financial-ops/recovery/holds"),
        api.get<ZeroAttemptQueue>("/financial-ops/recovery/zero-attempt"),
      ]);
      setStatus(s.recovery);
      setHolds(h.holds ?? []);
      setZero(z);
      setErr(null);
    } catch (e: any) {
      setErr(surfaceUnavailableMessage(e, "Financial recovery"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function resolve(hold: RecoveryHold) {
    const resolution = choice[hold.hold_id];
    if (!resolution) {
      setErr("Choose what you established about this item first.");
      return;
    }
    setBusy(hold.hold_id);
    setErr(null);
    setNote(null);
    try {
      await api.post(`/financial-ops/recovery/holds/${hold.hold_id}/resolve`, {
        resolution,
        note: evidence[hold.hold_id] ?? "",
        password,
      });
      setNote("Recorded. Nothing was re-sent.");
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Could not record that decision");
    } finally {
      setBusy(null);
    }
  }

  async function release() {
    setBusy("release");
    setErr(null);
    setNote(null);
    try {
      await api.post("/financial-ops/recovery/release", {
        note: evidence["__release"] ?? "",
        password,
      });
      setNote("Financial recovery released. Money movement has resumed.");
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Could not release recovery");
    } finally {
      setBusy(null);
    }
  }

  // Authorizing ONE attempt for a posting that was never transmitted. This sends nothing: it records that an
  // operator established the folio was never charged, and permits exactly one future attempt by the ordinary
  // worker. There is deliberately no "send now" here, and no bulk action -- each charge is decided on its own
  // evidence or not at all.
  async function authorizeZeroAttempt(row: ZeroAttemptRow) {
    setBusy(row.posting_id);
    setErr(null);
    setNote(null);
    try {
      await api.post(`/financial-ops/recovery/zero-attempt/${row.posting_id}/authorize`, {
        reason: zaReason[row.posting_id] ?? "",
        evidence: {
          source_type: zaSource[row.posting_id] ?? "",
          reference: zaRef[row.posting_id] ?? "",
        },
        password,
      });
      setNote(
        "One attempt authorized. Nothing has been sent — the posting re-joins the ordinary queue and will " +
          "be transmitted once, when recovery is released.",
      );
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Could not authorize that retry");
    } finally {
      setBusy(null);
    }
  }

  const header = (
    <PageHeader
      icon={<LifeBuoy />}
      eyebrow="Charges"
      title="Recovery"
      description="After a database restore, reconcile the money that was in flight before charging resumes. Nothing here re-sends anything."
      help={
        <>
          <HelpSection title="What recovery is">
            <p>
              After a database restore, money movement for this site is held until the money that was in flight
              before the restore has been reconciled. Nothing is replayed: after a restore, a correct retry is how a
              guest gets charged twice. Guest internet access is unaffected throughout.
            </p>
          </HelpSection>
          <HelpSection title="How to work through it">
            <HelpList
              items={[
                <>For each held item, record what you established and how. Recording a decision never re-sends anything.</>,
                <>Every reconciliation decision is an audited statement about real money, so each one is confirmed with your password.</>,
                <><strong>Never transmitted</strong> charges were held before anything was sent to the PMS, so there is no attempt to review on Manual review. Once one is reconciled as &ldquo;It never completed&rdquo;, exactly one further attempt can be authorized; authorizing sends nothing immediately.</>,
                <>When no held items remain, releasing recovery resumes money movement for this site. The reason you give is recorded in the audit log with your name.</>,
              ]}
            />
          </HelpSection>
        </>
      }
    />
  );

  if (!status) {
    return (
      <PageShell>
        {header}
        <ErrorBanner err={err} className="mb-0" />
        {!err && (
          <div className="space-y-4" aria-busy="true">
            <span className="sr-only">Loading recovery state</span>
            <Skeleton className="h-28 w-full" />
            <Skeleton className="h-40 w-full" />
          </div>
        )}
      </PageShell>
    );
  }

  if (!status.Active) {
    return (
      <PageShell>
        {header}
        <ErrorBanner err={err} className="mb-0" />
        {note && <Callout tone="success">{note}</Callout>}
        <Card>
          <CardBody className="flex flex-col gap-3 p-5 sm:flex-row sm:items-center">
            <CircleCheck className="size-6 shrink-0 text-success" aria-hidden />
            <div className="min-w-0 space-y-1">
              <Badge tone="ok" dot>Not in recovery</Badge>
              <p className="text-sm text-muted-foreground">
                Financial execution is running normally. Epoch {status.Epoch}.
              </p>
            </div>
          </CardBody>
        </Card>
      </PageShell>
    );
  }

  const open = holds ?? [];
  const zq = zero?.queue ?? [];

  return (
    <PageShell>
      {header}

      {!canAct && <ReadOnlyNotice>Your role can see the recovery state but not record decisions or release it.</ReadOnlyNotice>}
      <ErrorBanner err={err} className="mb-0" />
      {note && <Callout tone="success">{note}</Callout>}

      {/* The recovery banner. Deliberately not an alert region: it is the page's standing state, not news. */}
      <section className="overflow-hidden rounded-lg border border-destructive/40 bg-card shadow-card">
        <div className="flex flex-col gap-3 bg-destructive-subtle px-5 py-4 sm:flex-row sm:items-start">
          <ShieldAlert className="size-6 shrink-0 text-destructive-subtle-foreground" aria-hidden />
          <div className="min-w-0 space-y-2 text-sm text-foreground">
            <Badge tone="err" dot>Financial recovery</Badge>
            <p>
              Money movement is held for this site. Nothing has been replayed and nothing will be: after a
              restore, a correct retry is how a guest gets charged twice.
            </p>
            <p className="tabular">
              Epoch {status.Epoch} · {status.Reason.replace(/_/g, " ").toLowerCase()} ·{" "}
              <strong>{status.HeldOpen}</strong> of {status.HeldTotal} items still to reconcile.
            </p>
            <p className="text-muted-foreground">Guest internet access is unaffected and continues to work.</p>
          </div>
        </div>
      </section>

      {canAct && (
        <Card>
          <CardBody>
            <Field
              label="Your password"
              htmlFor="recovery-password"
              hint="Each decision is confirmed with your password."
            >
              <Input
                id="recovery-password"
                type="password"
                autoComplete="current-password"
                className="max-w-sm"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </Field>
          </CardBody>
        </Card>
      )}

      {zq.length > 0 ? (
        <Card>
          <CardHeader className="block space-y-1">
            <CardTitle>Never transmitted ({zq.length})</CardTitle>
            <CardDescription className="max-w-3xl">
              These charges were held before anything was sent to the PMS, so there is no attempt to review
              and they do not appear on the Manual Review screen. {zero!.note}
            </CardDescription>
          </CardHeader>
          <ul className="divide-y divide-border">
            {zq.map((z) => (
              <li key={z.posting_id} className="space-y-3 px-5 py-4">
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                  <span className="text-emphasis tabular">{money(z.amount_minor, z.currency, z.currency_exponent)}</span>
                  <MonoId value={z.posting_id} title="Posting reference" />
                  {z.retry_authorized_attempt_no !== null ? (
                    <Badge tone="ok" dot>Attempt {z.retry_authorized_attempt_no} authorized</Badge>
                  ) : z.hold_resolution ? (
                    <Badge tone={z.eligible_for_retry_authorization ? "warn" : "default"}>
                      {humanize(z.hold_resolution)}
                    </Badge>
                  ) : (
                    <Badge tone="default">Not yet reconciled</Badge>
                  )}
                </div>
                {z.eligible_for_retry_authorization ? (
                  canAct ? (
                    <div className="grid gap-3 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end">
                      <Field label="Why this charge must still go out" htmlFor={`za-reason-${z.posting_id}`}>
                        <Textarea
                          id={`za-reason-${z.posting_id}`}
                          rows={2}
                          className="min-h-10"
                          value={zaReason[z.posting_id] ?? ""}
                          onChange={(e) => setZaReason({ ...zaReason, [z.posting_id]: e.target.value })}
                        />
                      </Field>
                      <Field label="Evidence source for this posting" htmlFor={`za-source-${z.posting_id}`}>
                        <Select
                          id={`za-source-${z.posting_id}`}
                          value={zaSource[z.posting_id] ?? ""}
                          onChange={(e) => setZaSource({ ...zaSource, [z.posting_id]: e.target.value })}
                        >
                          <option value="">Choose…</option>
                          {(zero!.evidence_contract?.source_types ?? []).map((t) => (
                            <option key={t} value={t}>
                              {t}
                            </option>
                          ))}
                        </Select>
                      </Field>
                      <Field label="Reference to that evidence" htmlFor={`za-ref-${z.posting_id}`}>
                        <Input
                          id={`za-ref-${z.posting_id}`}
                          placeholder="e.g. folio number"
                          value={zaRef[z.posting_id] ?? ""}
                          onChange={(e) => setZaRef({ ...zaRef, [z.posting_id]: e.target.value })}
                        />
                      </Field>
                      <Button
                        className="h-10"
                        disabled={busy === z.posting_id}
                        onClick={() => void authorizeZeroAttempt(z)}
                      >
                        {busy === z.posting_id ? "Authorizing…" : "Authorize one attempt"}
                      </Button>
                    </div>
                  ) : null
                ) : (
                  <p className="text-xs text-muted-foreground">
                    {z.retry_authorized_attempt_no !== null
                      ? "An attempt has already been authorized for this posting. Exactly one is allowed."
                      : "Reconcile this item above as “It never completed” first — the authorization rests on that finding."}
                  </p>
                )}
              </li>
            ))}
          </ul>
          <CardFooter className="text-xs text-muted-foreground">
            {zero!.eligibility} Authorizing sends nothing now, and it can be done once per posting.
          </CardFooter>
        </Card>
      ) : null}

      {open.length === 0 ? (
        <Card>
          <CardBody className="space-y-4">
            <EmptyState
              className="py-6"
              icon={<CircleCheck />}
              title="Everything has been reconciled"
              hint="No held items remain. Releasing recovery resumes money movement for this site."
            />
            {canAct && (
              <div className="space-y-3 border-t border-border pt-4">
                <Field label="Why is it safe to resume?" htmlFor="release-note" hint="Recorded in the audit log with your name.">
                  <Textarea
                    id="release-note"
                    rows={3}
                    value={evidence["__release"] ?? ""}
                    onChange={(e) => setEvidence({ ...evidence, __release: e.target.value })}
                  />
                </Field>
                <Button disabled={busy === "release"} onClick={() => void release()}>
                  {busy === "release" ? "Releasing…" : "Release financial recovery"}
                </Button>
              </div>
            )}
          </CardBody>
        </Card>
      ) : (
        <Card>
          <CardHeader className="block space-y-1">
            <CardTitle>Held work ({open.length})</CardTitle>
            <CardDescription>
              For each item, record what you established and how. Recording a decision never re-sends anything.
            </CardDescription>
          </CardHeader>
          <ul className="divide-y divide-border">
            {open.map((h) => (
              <li key={h.hold_id} className="space-y-3 px-5 py-4">
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                  <span className="text-emphasis">{KIND_TEXT[h.work_kind]}</span>
                  <span className="tabular">{money(h.amount_minor, h.currency)}</span>
                  <Badge tone="warn">State when held: {humanize(h.held_status)}</Badge>
                  <span className="text-xs text-muted-foreground">Held {formatDate(h.held_at)}</span>
                </div>
                {canAct && (
                  <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end">
                    <Field
                      label={`Conclusion for this ${KIND_TEXT[h.work_kind].toLowerCase()}`}
                      htmlFor={`res-${h.hold_id}`}
                    >
                      <Select
                        id={`res-${h.hold_id}`}
                        value={choice[h.hold_id] ?? ""}
                        onChange={(e) => setChoice({ ...choice, [h.hold_id]: e.target.value as RecoveryResolution })}
                      >
                        <option value="">Choose…</option>
                        {RECOVERY_RESOLUTIONS.map((r) => (
                          <option key={r} value={r}>
                            {RESOLUTION_TEXT[r]}
                          </option>
                        ))}
                      </Select>
                    </Field>
                    <Field label="Evidence for this decision" htmlFor={`note-${h.hold_id}`}>
                      <Input
                        id={`note-${h.hold_id}`}
                        placeholder="e.g. provider dashboard shows no charge"
                        value={evidence[h.hold_id] ?? ""}
                        onChange={(e) => setEvidence({ ...evidence, [h.hold_id]: e.target.value })}
                      />
                    </Field>
                    <Button className="h-10" disabled={busy === h.hold_id} onClick={() => void resolve(h)}>
                      {busy === h.hold_id ? "Recording…" : "Record"}
                    </Button>
                  </div>
                )}
              </li>
            ))}
          </ul>
          <CardFooter className="text-xs text-muted-foreground">
            Recovery can only be released once every item above has been reconciled.
          </CardFooter>
        </Card>
      )}
    </PageShell>
  );
}
