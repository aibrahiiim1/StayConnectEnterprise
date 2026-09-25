"use client";

// Phase 4 (DARK) — Manual review.
//
// This is where an operator decides what happened to money the system could not determine for itself. The
// screen is built around one idea: it must show the EVIDENCE the decision has to rest on, and it must offer
// only the actions the backend will actually accept.
//
// So the action list is fetched, never hard-coded. §15's catalog lives in the database and is served by
// /financial-review/actions together with the evidence contract; a second copy in the frontend would drift,
// and the first symptom of that drift would be an operator confidently choosing something the backend
// refuses -- or worse, one that is missing an action they needed.
//
// There is deliberately no generic "approve". Programmatic PMS reversal is capability=false in v1, so
// CREATE_REVERSAL records an audited ledger row and the folio correction stays a manual Front Office job;
// the screen says so rather than implying the button fixes the folio.
//
// LAYOUT. The queue is the page; one charge opens in a side sheet with what it was attached to, every
// attempt, the decisions already recorded and the decision form, so the queue stays in view behind it.
// A role that may not decide sees the evidence and no form at all.

import { useCallback, useEffect, useId, useState } from "react";
import { ClipboardCheck, Inbox } from "lucide-react";
import {
  api,
  ReviewActionDoc,
  ReviewPostingDetail,
  ReviewQueueRow,
  surfaceUnavailableMessage,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { KeyValueGrid, Timeline } from "@/components/ui/data";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";
import { humanize, money } from "./format";

const OUTCOME_TONE = (o: string) =>
  o === "UNKNOWN" ? "err" : o === "FAILED" ? "warn" : o === "ACKED" ? "ok" : "info";

export function ManualReviewView({ canAct = true }: { canAct?: boolean }) {
  const toast = useToast();
  const formId = useId();
  const [rows, setRows] = useState<ReviewQueueRow[] | null>(null);
  const [actions, setActions] = useState<ReviewActionDoc[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<ReviewPostingDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [sheetErr, setSheetErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // The decision form. Reason and evidence are separate because the audit record keeps them separate: the
  // reason is why, the evidence is how it was established.
  const [action, setAction] = useState("");
  const [reason, setReason] = useState("");
  const [evSource, setEvSource] = useState("");
  const [evRef, setEvRef] = useState("");
  const [password, setPassword] = useState("");

  const loadQueue = useCallback(async () => {
    try {
      const [q, a] = await Promise.all([
        api.get<{ queue: ReviewQueueRow[] }>("/financial-review/queue"),
        api.get<{ actions: ReviewActionDoc[] }>("/financial-review/actions"),
      ]);
      setRows(q.queue ?? []);
      setActions(a.actions ?? []);
      setErr(null);
    } catch (e: any) {
      setErr(surfaceUnavailableMessage(e, "Manual financial review"));
    }
  }, []);

  useEffect(() => {
    void loadQueue();
  }, [loadQueue]);

  const open = useCallback(async (postingID: string) => {
    setSelected(postingID);
    setDetail(null);
    setAction("");
    setReason("");
    setEvSource("");
    setEvRef("");
    setSheetErr(null);
    try {
      setDetail(await api.get<ReviewPostingDetail>(`/financial-review/postings/${postingID}`));
    } catch (e: any) {
      setSheetErr(e?.message ?? "Could not load that posting");
    }
  }, []);

  function close() {
    setSelected(null);
    setDetail(null);
    setNote(null);
    setSheetErr(null);
    // A password must not outlive the sheet that collected it.
    setPassword("");
  }

  async function decide() {
    if (!selected || !detail) return;
    setBusy(true);
    setSheetErr(null);
    setNote(null);
    try {
      await api.post(`/financial-review/postings/${selected}/actions`, {
        action,
        reason,
        expected_version: detail.review.version,
        evidence: evSource ? { source_type: evSource, reference: evRef } : undefined,
        password,
      });
      setNote("Decision recorded.");
      toast.success("Decision recorded");
      await loadQueue();
      await open(selected);
    } catch (e: any) {
      setSheetErr(e?.message ?? "Could not record that decision");
    } finally {
      setBusy(false);
    }
  }

  const spec = actions.find((a) => a.action === action);
  const allowed = detail?.available_actions ?? [];
  const canDecide = canAct && !!detail && allowed.length > 0;

  return (
    <PageShell>
      <PageHeader
        icon={<ClipboardCheck />}
        eyebrow="Charges"
        title="Manual review"
        description="Decide what happened to a room charge whose outcome is unknown. Every decision is an audited statement about real money."
      />

      {!canAct && rows && <ReadOnlyNotice>Your role can see the review queue but not record decisions.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      <Card>
        <CardHeader>
          <CardTitle>Awaiting a decision{rows ? ` (${rows.length})` : ""}</CardTitle>
        </CardHeader>
        <CardBody className="p-0">
          {!rows ? (
            err ? null : <SkeletonRows rows={3} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Inbox />}
              title="Nothing is waiting on you"
              hint="Postings appear here when their outcome could not be determined, or when someone escalated them."
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Amount</TH>
                  <TH>State</TH>
                  <TH className="hidden sm:table-cell">Last attempt</TH>
                  <TH className="hidden md:table-cell">PMS answer</TH>
                  <TH>
                    <span className="sr-only">Actions</span>
                  </TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((r) => (
                  <TR key={r.posting_id}>
                    <TD className="font-medium tabular">{money(r.amount_minor, r.currency, r.currency_exponent)}</TD>
                    <TD>
                      <Badge tone={r.awaiting_manual_review ? "err" : "warn"} dot>
                        {humanize(r.execution_state)}
                      </Badge>
                    </TD>
                    <TD className="hidden tabular sm:table-cell">
                      {r.latest_attempt_no != null ? `#${r.latest_attempt_no}` : "—"}
                    </TD>
                    <TD className="hidden text-muted-foreground md:table-cell">{r.latest_pa_as_status ?? "no answer"}</TD>
                    <TD className="text-right">
                      <Button size="sm" variant="secondary" onClick={() => void open(r.posting_id)}>
                        Review
                      </Button>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Sheet open={selected !== null} onOpenChange={(v) => !v && !busy && close()}>
        <SheetContent width="lg">
          <SheetHeader
            icon={<ClipboardCheck />}
            eyebrow="Room charge"
            title={detail ? money(detail.posting.amount_minor, detail.posting.currency, detail.posting.currency_exponent) : "Room charge"}
            description="What it was attached to, every attempt to post it, and what has been decided."
            badges={
              detail ? (
                <>
                  <Badge tone={detail.posting.awaiting_manual_review ? "err" : "warn"} dot>
                    {humanize(detail.posting.execution_state)}
                  </Badge>
                  {detail.review.terminal_action && (
                    <Badge tone="neutral">Decided: {humanize(detail.review.terminal_action)}</Badge>
                  )}
                </>
              ) : undefined
            }
          />
          <SheetBody>
            <ErrorBanner err={sheetErr} className="mb-0" />
            {note && <Callout tone="success">{note}</Callout>}

            {!detail ? (
              sheetErr ? null : (
                <div className="space-y-3" aria-busy="true">
                  <span className="sr-only">Loading the posting</span>
                  <Skeleton className="h-24 w-full" />
                  <Skeleton className="h-32 w-full" />
                </div>
              )
            ) : (
              <>
                <SheetSection title="What this charge was attached to">
                  <KeyValueGrid
                    columns={3}
                    items={[
                      {
                        label: "Amount",
                        value: money(detail.posting.amount_minor, detail.posting.currency, detail.posting.currency_exponent),
                      },
                      { label: "Settlement", value: detail.pinned_evidence.settlement_status },
                      { label: "Purchase", value: detail.pinned_evidence.purchase_state },
                      {
                        label: "Interface",
                        value: `${detail.pinned_evidence.connector_kind} (${detail.pinned_evidence.interface_lifecycle_state})`,
                      },
                      { label: "Folio identity", value: detail.pinned_evidence.folio_identity_strategy },
                      { label: "Interface freshness", value: detail.diagnostics.interface_freshness_block ?? "OK" },
                    ]}
                  />
                </SheetSection>

                <SheetSection
                  title={`Attempts (${detail.diagnostics.attempt_count}, of which unknown: ${detail.diagnostics.unknown_attempt_count})`}
                >
                  {detail.attempts.length === 0 ? (
                    <p className="text-sm text-muted-foreground">This posting has never been transmitted.</p>
                  ) : (
                    <div className="overflow-hidden rounded-md border border-border">
                      <Table>
                        <THead>
                          <TR>
                            <TH>#</TH>
                            <TH>P#</TH>
                            <TH>Room / Guest</TH>
                            <TH>Outcome</TH>
                            <TH>PMS answer</TH>
                            <TH>Sent</TH>
                          </TR>
                        </THead>
                        <TBody>
                          {detail.attempts.map((a) => (
                            <TR key={a.attempt_no}>
                              <TD className="tabular">{a.attempt_no}</TD>
                              <TD className="tabular">{a.p_number}</TD>
                              <TD>
                                {a.rn}
                                {a.g_number ? ` / ${a.g_number}` : ""}
                              </TD>
                              <TD>
                                <Badge tone={OUTCOME_TONE(a.outcome)}>{a.outcome}</Badge>
                              </TD>
                              <TD>{a.pa_as_status ?? "—"}</TD>
                              <TD className="whitespace-nowrap text-muted-foreground">{formatDate(a.sent_at)}</TD>
                            </TR>
                          ))}
                        </TBody>
                      </Table>
                    </div>
                  )}
                  {detail.diagnostics.has_unknown_history ? (
                    <div className="rounded-md border border-warning/30 bg-warning-subtle px-3.5 py-2.5 text-sm text-warning-subtle-foreground">
                      An attempt ended UNKNOWN. Nobody knows whether the folio was charged, and nothing has been
                      retried automatically — that is what this decision is for.
                    </div>
                  ) : null}
                </SheetSection>

                {detail.review.history.length > 0 ? (
                  <SheetSection title="Decisions already recorded">
                    <Timeline
                      items={detail.review.history.map((h, i) => ({
                        key: String(i),
                        title: humanize(h.action),
                        when: formatDate(h.created_at),
                        body: (
                          <>
                            <span className="block text-foreground">{h.reason}</span>
                            {h.actor && <span className="text-xs">by {h.actor}</span>}
                          </>
                        ),
                      }))}
                    />
                  </SheetSection>
                ) : null}

                <SheetSection title="Record a decision">
                  {allowed.length === 0 ? (
                    <p className="text-sm text-muted-foreground">
                      This posting has a terminal decision already. Nothing further can be recorded against it.
                    </p>
                  ) : !canAct ? (
                    <ReadOnlyNotice>Your role can see this evidence but not record a decision.</ReadOnlyNotice>
                  ) : (
                    <form
                      id={formId}
                      className="space-y-4"
                      onSubmit={(e) => {
                        e.preventDefault();
                        void decide();
                      }}
                    >
                      <Field
                        label="What did you establish?"
                        htmlFor="review-action"
                        hint="Only the decisions this posting can still take are offered."
                      >
                        <Select id="review-action" value={action} onChange={(e) => setAction(e.target.value)}>
                          <option value="">Choose…</option>
                          {actions
                            .filter((a) => allowed.includes(a.action))
                            .map((a) => (
                              <option key={a.action} value={a.action}>
                                {a.action} — {a.summary}
                              </option>
                            ))}
                        </Select>
                      </Field>
                      {spec?.terminal ? (
                        <Callout tone="warning">
                          This is a terminal decision. It can be recorded once and never revised.
                        </Callout>
                      ) : null}

                      <Field label="Why" htmlFor="review-reason" hint="Recorded in the audit log.">
                        <Textarea id="review-reason" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} />
                      </Field>

                      {spec?.needs_evidence ? (
                        <div className="grid gap-4 sm:grid-cols-2">
                          <Field label="Evidence source" htmlFor="review-ev-source">
                            <Select id="review-ev-source" value={evSource} onChange={(e) => setEvSource(e.target.value)}>
                              <option value="">Choose…</option>
                              {(detail.evidence_contract?.source_types ?? []).map((s) => (
                                <option key={s} value={s}>
                                  {s}
                                </option>
                              ))}
                            </Select>
                          </Field>
                          <Field
                            label="Reference to it"
                            htmlFor="review-ev-ref"
                            hint="Record a REFERENCE to the artefact, never its contents. This goes into an immutable audit record."
                          >
                            <Input
                              id="review-ev-ref"
                              placeholder="e.g. PMS folio screen, 14:22"
                              value={evRef}
                              onChange={(e) => setEvRef(e.target.value)}
                            />
                          </Field>
                        </div>
                      ) : null}

                      <Field label="Your password" htmlFor="review-password" hint="Every decision is confirmed with your password.">
                        <Input
                          id="review-password"
                          type="password"
                          autoComplete="current-password"
                          className="max-w-sm"
                          value={password}
                          onChange={(e) => setPassword(e.target.value)}
                        />
                      </Field>

                      {(detail.limitations ?? []).length > 0 && (
                        <ul className="list-disc space-y-1 ps-5 text-xs text-muted-foreground">
                          {(detail.limitations ?? []).map((l, i) => (
                            <li key={i}>{l}</li>
                          ))}
                        </ul>
                      )}
                    </form>
                  )}
                </SheetSection>
              </>
            )}
          </SheetBody>
          <SheetFooter>
            <Button variant="ghost" disabled={busy} onClick={close}>
              Close
            </Button>
            {canDecide && (
              <Button type="submit" form={formId} disabled={busy || !action}>
                {busy ? "Recording…" : "Record decision"}
              </Button>
            )}
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </PageShell>
  );
}
