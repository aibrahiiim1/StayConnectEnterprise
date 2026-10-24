"use client";

// Fetch available actions from the backend so operators cannot choose stale or unsupported decisions.
// CREATE_REVERSAL records a ledger row; Front Office must correct the PMS folio manually.

import { useCallback, useEffect, useId, useState } from "react";
import { Inbox } from "lucide-react";
import {
  api,
  ReviewActionDoc,
  ReviewPostingDetail,
  ReviewQueueRow,
  surfaceUnavailableMessage,
} from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { HistoryList } from "@/components/ui/history";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Field, FieldGrid, Input, Select, Textarea } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { KeyValueGrid } from "@/components/ui/data";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";
import { humanize, money } from "./format";

// NOT_SENT means pmsd proved no bytes reached the PMS; it is distinct from an unknown outcome.
const OUTCOME_TONE = (o: string) =>
  o === "UNKNOWN" ? "err" : o === "NOT_SENT" ? "neutral" : o === "ACKED" ? "ok" : "info";

const NOT_SENT_REASON_WORDS: Record<string, string> = {
  ROOM_CHANGED: "The guest had moved room before it was sent",
  ROOM_CHANGED_ON_LINK: "The guest moved room just before it was sent",
  RESERVATION_DEPARTED_ON_LINK: "The guest checked out just before it was sent",
  STAY_NOT_POSTABLE: "The stay could no longer be charged",
  RESERVATION_MISMATCH: "The reservation no longer matched",
  INTERFACE_NOT_FRESH: "The PMS connection was not up to date",
  LINK_NOT_CONNECTED: "The PMS connection was down",
  LINK_RESYNCING: "The PMS connection was resynchronising",
  LINK_BUSY: "Another charge was being sent",
};

const OUTCOME_WORDS: Record<string, string> = {
  SENDING: "Sending",
  ACKED: "Answered by the PMS",
  UNKNOWN: "Unknown",
  NOT_SENT: "Not sent (nothing reached the PMS)",
};

const TARGET_WORDS: Record<string, string> = {
  RESERVATION: "Reservation (room + reservation number)",
  UNSET: "Not recorded",
};

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

  // Keep reason and evidence separate to preserve their distinct audit fields.
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
    // Drop the password when the sheet closes.
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
        title="Manual review"
        description="Decide what happened to a room charge whose outcome is unknown."
        help={
          <>
            <HelpSection title="What this queue is">
              <p>
                Room charges whose outcome could not be determined, or that someone escalated. Open one to see what
                it was attached to, every attempt to post it, and what has been decided.
              </p>
            </HelpSection>
            <HelpSection title="Recording a decision">
              <HelpList
                items={[
                  <>Every decision is an audited statement about real money, and is confirmed with your password.</>,
                  <>Only the decisions a posting can still take are offered. A terminal decision is recorded once and never revised.</>,
                  <>Where evidence is needed, record a <strong>reference</strong> to the artefact (for example &ldquo;PMS folio screen, 14:22&rdquo;), never its contents — it goes into an immutable audit record.</>,
                ]}
              />
            </HelpSection>
          </>
        }
      />

      {!canAct && rows && <ReadOnlyNotice>Your role can see the review queue but not record decisions.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      <SectionCard
        flush
        title="Awaiting a decision"
        actions={rows ? <Badge tone={rows.length ? "warn" : "neutral"}>{rows.length}</Badge> : undefined}
      >
          {!rows ? (
            err ? null : <SkeletonRows rows={3} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<Inbox />}
              title="Nothing is waiting on you"
              hint="Postings appear here when their outcome could not be determined, or when someone escalated them."
            />
          ) : (
            <Table label="Room charges awaiting a decision">
              <THead>
                <TR>
                  <TH numeric>Amount</TH>
                  <TH>State</TH>
                  <TH numeric className="hidden sm:table-cell">Last attempt</TH>
                  <TH className="hidden md:table-cell">PMS answer</TH>
                  <TH pinEnd>
                    <span className="sr-only">Actions</span>
                  </TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((r) => (
                  <TR key={r.posting_id} onClick={() => void open(r.posting_id)}>
                    <TD numeric className="font-medium">{money(r.amount_minor, r.currency, r.currency_exponent)}</TD>
                    <TD nowrap>
                      <StatusBadge tone={r.awaiting_manual_review ? "err" : "warn"}>
                        {humanize(r.execution_state)}
                      </StatusBadge>
                    </TD>
                    <TD numeric className="hidden sm:table-cell">
                      {r.latest_attempt_no != null ? `#${r.latest_attempt_no}` : "—"}
                    </TD>
                    <TD muted className="hidden md:table-cell">{r.latest_pa_as_status ?? "no answer"}</TD>
                    <TD pinEnd className="w-0 text-end">
                      <Button
                        size="xs"
                        variant="secondary"
                        onClick={(e) => { e.stopPropagation(); void open(r.posting_id); }}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        Review
                      </Button>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
      </SectionCard>

      <Sheet open={selected !== null} onOpenChange={(v) => !v && !busy && close()}>
        <SheetContent width="lg">
          <SheetHeader
            eyebrow="Room charge"
            title={detail ? money(detail.posting.amount_minor, detail.posting.currency, detail.posting.currency_exponent) : "Room charge"}
            description="What it was attached to, every attempt to post it, and what has been decided."
            badges={
              detail ? (
                <>
                  <StatusBadge tone={detail.posting.awaiting_manual_review ? "err" : "warn"}>
                    {humanize(detail.posting.execution_state)}
                  </StatusBadge>
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
                    columns={2}
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
                      { label: "Reservation (G#)", value: detail.pinned_evidence.g_number || "—" },
                      {
                        label: "Posting target",
                        value: TARGET_WORDS[detail.pinned_evidence.posting_target_model] ?? detail.pinned_evidence.posting_target_model,
                      },
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
                      <Table density="compact" label="Attempts">
                        <THead>
                          <TR>
                            <TH numeric>#</TH>
                            <TH numeric>P#</TH>
                            <TH>Room / Reservation</TH>
                            <TH>Outcome</TH>
                            <TH>PMS answer</TH>
                            <TH>Sent</TH>
                          </TR>
                        </THead>
                        <TBody>
                          {detail.attempts.map((a) => (
                            <TR key={a.attempt_no}>
                              <TD numeric>{a.attempt_no}</TD>
                              <TD numeric>{a.p_number}</TD>
                              <TD nowrap>
                                {a.rn}
                                {a.g_number ? ` / ${a.g_number}` : ""}
                              </TD>
                              <TD>
                                <Badge tone={OUTCOME_TONE(a.outcome)}>{OUTCOME_WORDS[a.outcome] ?? a.outcome}</Badge>
                                {a.outcome === "NOT_SENT" && a.not_sent_reason && (
                                  <div className="mt-0.5 text-2xs text-muted-foreground" data-testid="not-sent-reason">
                                    {NOT_SENT_REASON_WORDS[a.not_sent_reason] ?? a.not_sent_reason}
                                  </div>
                                )}
                              </TD>
                              <TD>{a.pa_as_status ?? "—"}</TD>
                              <TD className="whitespace-nowrap text-muted-foreground">{formatDate(a.sent_at)}</TD>
                            </TR>
                          ))}
                        </TBody>
                      </Table>
                    </div>
                  )}
                  {detail.attempts.length > 0 && (
                    <p className="text-xs text-muted-foreground">
                      Each attempt keeps the room it actually carried. The reservation number never changes; if the
                      guest moved rooms, a later attempt carries the new room and the earlier one still shows the old one.
                      &ldquo;Not sent&rdquo; means the appliance proved nothing reached the PMS — it is not a PMS failure.
                    </p>
                  )}
                  {detail.diagnostics.has_unknown_history ? (
                    <Callout tone="warning">
                      An attempt ended UNKNOWN. Nobody knows whether the folio was charged, and nothing has been
                      retried automatically — that is what this decision is for.
                    </Callout>
                  ) : null}
                </SheetSection>

                {detail.review.history.length > 0 ? (
                  <SheetSection title="Decisions already recorded">
                    <div className="overflow-hidden rounded-md border border-border">
                      <HistoryList
                        label="Decisions already recorded"
                        items={detail.review.history.map((h, i) => ({
                          id: String(i),
                          title: humanize(h.action),
                          actor: h.actor || undefined,
                          at: formatDate(h.created_at),
                          summary: h.reason,
                        }))}
                      />
                    </div>
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
                        <FieldGrid cols={2}>
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
                            hint="A reference to the artefact, never its contents. It goes into an immutable audit record."
                          >
                            <Input
                              id="review-ev-ref"
                              placeholder="e.g. PMS folio screen, 14:22"
                              value={evRef}
                              onChange={(e) => setEvRef(e.target.value)}
                            />
                          </Field>
                        </FieldGrid>
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
              <Button type="submit" form={formId} loading={busy} disabled={!action}>
                Record decision
              </Button>
            )}
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </PageShell>
  );
}
