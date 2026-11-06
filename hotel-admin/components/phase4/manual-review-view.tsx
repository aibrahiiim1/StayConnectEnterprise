"use client";

import { useCallback, useEffect, useId, useState } from "react";
import { AlertTriangle, Inbox, RefreshCw } from "lucide-react";
import {
  api,
  ApiError,
  ReviewActionDoc,
  ReviewPostingDetail,
  ReviewQueueRow,
} from "@/lib/api";
import { LIFECYCLE_WORDS } from "@/lib/api/pms-connections";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { HistoryList } from "@/components/ui/history";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";
import { Field, FieldGrid, Input, Select } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { KeyValueGrid } from "@/components/ui/data";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { cn, formatDate } from "@/lib/utils";
import { money } from "./format";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const MAX_REASON = 500;
const MAX_REFERENCE = 120;
const MAX_NOTE = 500;

const OUTCOME_TONE = (o: string) =>
  o === "UNKNOWN" ? "err" : o === "NOT_SENT" || o === "FAILED" ? "neutral" : o === "ACKED" ? "ok" : "info";

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
  FAILED: "Not sent (nothing reached the PMS)",
};

const STATE_WORDS: Record<string, string> = {
  NOT_ATTEMPTED: "Not sent yet",
  IN_FLIGHT: "Being sent",
  UNKNOWN: "Outcome unknown",
  NOT_SENT: "Not sent",
  POSTED: "Posted",
  REJECTED: "Refused by the PMS",
};

const SETTLEMENT_WORDS: Record<string, string> = {
  NOT_REQUIRED: "Not needed",
  REQUIRED: "Waiting to be charged",
  IN_PROGRESS: "Being charged",
  SETTLED: "Charged",
  FAILED: "Failed",
  MANUAL_REVIEW: "Waiting for a decision",
  PARTIALLY_REVERSED: "Partly reversed",
  REVERSED: "Reversed",
};

const PURCHASE_WORDS: Record<string, string> = {
  PENDING: "Started",
  AWAITING_SETTLEMENT: "Waiting for the charge",
  MANUAL_REVIEW: "Waiting for a decision",
  GRANTED: "Access given",
  FAILED: "Failed",
  CANCELLED: "Cancelled",
};

const CONNECTOR_WORDS: Record<string, string> = {
  "protel-fias": "Protel FIAS",
  "opera-fias": "Opera FIAS",
  "fidelio-fias": "Fidelio FIAS",
  mews: "Mews",
  apaleo: "Apaleo",
  stub: "Test connector",
};

const TARGET_WORDS: Record<string, string> = {
  RESERVATION: "Room and reservation number",
  UNSET: "Not recorded",
};

const ACTION_WORDS: Record<string, { label: string; explain: string }> = {
  CONFIRM_POSTED: {
    label: "It is on the guest's bill",
    explain: "You checked the PMS and the charge is on the guest's reservation. The charge is treated as paid.",
  },
  CONFIRM_NOT_POSTED_RETRY: {
    label: "It is not on the bill: send it once more",
    explain:
      "You checked the PMS and the charge is not on the reservation. The appliance sends it exactly one more time, as the same charge, so it cannot be posted twice.",
  },
  CONFIRM_NOT_POSTED_ABANDON: {
    label: "It is not on the bill: do not send it again",
    explain: "You checked the PMS and the charge is not on the reservation. It will not be sent again and is closed as failed.",
  },
  CREATE_REVERSAL: {
    label: "Record a reversal",
    explain:
      "Records in the appliance's ledger that this charge is reversed. Nothing is sent to the PMS: Front Office must correct the guest's bill in the PMS by hand.",
  },
  ESCALATE: {
    label: "Ask for a second opinion",
    explain: "Decides nothing. It can be asked more than once.",
  },
};

const SOURCE_WORDS: Record<string, string> = {
  PMS_FOLIO_INSPECTION: "I looked at the guest's bill in the PMS",
  PMS_REPORT: "A PMS report or export",
  FRONT_OFFICE_CONFIRMATION: "Front Office confirmed it",
  NIGHT_AUDIT: "The night audit",
  RECONCILIATION_EXPORT: "A reconciliation export",
  PROVIDER_DASHBOARD: "The payment provider's own dashboard",
  OTHER_DOCUMENTED: "Another source (describe it in the note)",
};

function actionLabel(code: string): string {
  return ACTION_WORDS[code]?.label ?? "Decision not recognised";
}

function answerWords(status: string | null | undefined): string {
  if (!status) return "No answer";
  if (status === "OK") return "Accepted";
  return `Refused (code ${status})`;
}

function actorWords(actor: string | null | undefined): string {
  if (!actor) return "Not recorded";
  if (actor.includes("@")) return actor;
  return `Operator ID ${actor.slice(0, 8)}`;
}

function attemptsTitle(total: number, unknown: number): string {
  const head = `${total} ${total === 1 ? "attempt" : "attempts"}`;
  return unknown > 0 ? `${head}, ${unknown} with an unknown outcome` : head;
}

type Evidence = { source_type?: string; reference?: string; note?: string } | null | undefined;

function evidenceWords(ev: Evidence): string | null {
  if (!ev || !ev.source_type) return null;
  const where = SOURCE_WORDS[ev.source_type] ?? "Source not recognised";
  return ev.reference ? `Evidence: ${where}, ${ev.reference}` : `Evidence: ${where}`;
}

function sourceNames(contract: unknown): string[] {
  if (Array.isArray(contract)) return contract.filter((s): s is string => typeof s === "string");
  if (contract && typeof contract === "object") return Object.keys(contract as Record<string, unknown>);
  return [];
}

type ActionsResp = { actions?: ReviewActionDoc[]; evidence_contract?: { source_types?: unknown } };

function decisionError(e: unknown): { message: string; reload: boolean } {
  const code = e instanceof ApiError ? e.code : "";
  const status = e instanceof ApiError ? e.status : 0;
  if (code === "reauth_required" || status === 401)
    return { message: "Your password was not accepted. Nothing was recorded.", reload: false };
  if (code === "reauth_unavailable")
    return { message: "Your password could not be checked right now. Nothing was recorded; try again.", reload: false };
  if (code === "forbidden" || status === 403)
    return { message: "Your role cannot record decisions here. Nothing was recorded.", reload: false };
  if (code === "stale_version")
    return {
      message: "Someone else recorded a decision on this charge while it was open. It has been reloaded; check it and decide again.",
      reload: true,
    };
  if (code === "already_decided")
    return { message: "This charge already has a final decision. It has been reloaded.", reload: true };
  if (code === "not_applicable")
    return { message: "That decision no longer applies to this charge. It has been reloaded; choose again.", reload: true };
  if (code === "reason_invalid")
    return {
      message: `The reason was not accepted. Keep it to one line of at most ${MAX_REASON} characters, without passwords, card numbers or copied PMS messages. Nothing was recorded.`,
      reload: false,
    };
  if (code === "evidence_invalid" || code === "evidence_required")
    return {
      message: `The evidence was not accepted. Choose where you checked and give a one-line reference of at most ${MAX_REFERENCE} characters, without passwords, card numbers or copied PMS messages. Nothing was recorded.`,
      reload: false,
    };
  if (code === "not_found" || status === 404)
    return { message: "This charge is no longer in this site's records. Nothing was recorded.", reload: true };
  return { message: "The decision could not be recorded. Nothing was recorded; try again.", reload: false };
}

export function ManualReviewView({
  canAct = true,
  rolesKnown = true,
  onNotMounted,
}: {
  canAct?: boolean;
  rolesKnown?: boolean;
  onNotMounted?: () => void;
}) {
  const formId = useId();
  const [rows, setRows] = useState<ReviewQueueRow[] | null>(null);
  const [actions, setActions] = useState<ReviewActionDoc[]>([]);
  const [sources, setSources] = useState<string[]>([]);
  const [queueErr, setQueueErr] = useState(false);
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<ReviewPostingDetail | null>(null);
  const [detailFailed, setDetailFailed] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [sheetErr, setSheetErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState(false);

  const [action, setAction] = useState("");
  const [reason, setReason] = useState("");
  const [evSource, setEvSource] = useState("");
  const [evRef, setEvRef] = useState("");
  const [evNote, setEvNote] = useState("");
  const [password, setPassword] = useState("");

  const loadQueue = useCallback(async () => {
    setLoading(true);
    try {
      const [q, a] = await Promise.all([
        api.get<{ items?: ReviewQueueRow[] }>("/financial-review/queue"),
        api.get<ActionsResp>("/financial-review/actions"),
      ]);
      setRows(q.items ?? []);
      setActions(a.actions ?? []);
      setSources(sourceNames(a.evidence_contract?.source_types));
      setQueueErr(false);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404 && onNotMounted) {
        onNotMounted();
        return;
      }
      setQueueErr(true);
    } finally {
      setLoading(false);
    }
  }, [onNotMounted]);

  useEffect(() => {
    void loadQueue();
  }, [loadQueue]);

  function resetForm() {
    setAction("");
    setReason("");
    setEvSource("");
    setEvRef("");
    setEvNote("");
    setPassword("");
  }

  const fetchDetail = useCallback(async (postingID: string) => {
    setDetailLoading(true);
    try {
      setDetail(await api.get<ReviewPostingDetail>(`/financial-review/postings/${postingID}`));
      setDetailFailed(false);
    } catch {
      setDetailFailed(true);
    } finally {
      setDetailLoading(false);
    }
  }, []);

  function open(postingID: string) {
    setSelected(postingID);
    setDetail(null);
    setDetailFailed(false);
    setSheetErr(null);
    setNote(null);
    setAsking(false);
    resetForm();
    void fetchDetail(postingID);
  }

  function close() {
    setSelected(null);
    setDetail(null);
    setDetailFailed(false);
    setNote(null);
    setSheetErr(null);
    setAsking(false);
    resetForm();
  }

  const dirty = !!(action || reason || evSource || evRef || evNote || password);

  function requestClose() {
    if (busy) return;
    if (dirty) {
      setAsking(true);
      return;
    }
    close();
  }

  const spec = actions.find((a) => a.action === action);
  const allowed = detail?.available_actions ?? [];
  const offered = actions.filter((a) => allowed.includes(a.action));
  const onlyEscalate = allowed.length > 0 && allowed.every((a) => a === "ESCALATE");
  const canDecide = canAct && !!detail && allowed.length > 0;

  const blocked = !action
    ? "Choose what you established."
    : !reason.trim()
      ? "Write why."
      : spec?.needs_evidence && !evSource
        ? "Choose where you checked."
        : spec?.needs_evidence && !evRef.trim()
          ? "Write a reference to what you checked."
          : spec?.needs_evidence && evSource === "OTHER_DOCUMENTED" && !evNote.trim()
            ? "Describe the source in the note."
            : !password
              ? "Enter your password."
              : undefined;

  async function decide() {
    if (!selected || !detail || blocked) return;
    setBusy(true);
    setSheetErr(null);
    setNote(null);
    const evidence = spec?.needs_evidence
      ? { source_type: evSource, reference: evRef.trim(), ...(evNote.trim() ? { note: evNote.trim() } : {}) }
      : undefined;
    try {
      await api.post(`/financial-review/postings/${selected}/actions`, {
        action,
        reason: reason.trim(),
        expected_version: detail.review.version,
        evidence,
        password,
      });
      resetForm();
      setNote(action === "ESCALATE" ? "Second opinion requested." : "Decision recorded.");
      await loadQueue();
      await fetchDetail(selected);
    } catch (e) {
      const { message, reload } = decisionError(e);
      setPassword("");
      if (reload) {
        setAction("");
        await loadQueue();
        await fetchDetail(selected);
      }
      setSheetErr(message);
    } finally {
      setBusy(false);
    }
  }

  const lastAttempt = detail?.attempts[detail.attempts.length - 1];
  const onlyEscalateReason = !detail
    ? null
    : detail.review.terminal_action
      ? `Decided: ${actionLabel(detail.review.terminal_action)}. A final decision cannot be changed. You can still ask for a second opinion.`
      : detail.attempts.length === 0
        ? "Nothing has been sent to the PMS yet, so there is nothing to decide. You can still ask for a second opinion."
        : lastAttempt?.outcome === "SENDING"
          ? "The last attempt is still being sent, so its outcome is not known yet. You can still ask for a second opinion."
          : null;

  const queueFailed = queueErr && rows === null;

  const discardPrompt = (
    <>
      <p role="status" className="me-auto text-caption text-foreground">Discard your changes?</p>
      <Button variant="ghost" onClick={() => setAsking(false)}>Keep editing</Button>
      <Button variant="danger" onClick={close}>Discard</Button>
    </>
  );

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

      {rolesKnown && !canAct && rows && (
        <ReadOnlyNotice>Your role can see the review queue but not record decisions.</ReadOnlyNotice>
      )}

      {queueErr && rows && (
        <div className={FADE}>
          <Callout tone="warning">The queue could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}

      <SectionCard
        flush
        title="Awaiting a decision"
        actions={
          rows || queueFailed ? (
            <div className="flex items-center gap-2">
              {rows ? <Badge tone={rows.length ? "warn" : "neutral"}>{rows.length}</Badge> : null}
              <Button size="xs" variant="ghost" onClick={() => void loadQueue()} disabled={loading}>
                <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Refresh
              </Button>
            </div>
          ) : undefined
        }
      >
        {queueFailed ? (
          <div role="alert" className={FADE}>
            <EmptyState
              size="sm"
              icon={<AlertTriangle />}
              title="The review queue could not be loaded"
              hint="Check the appliance is reachable, then try again. Nothing has been changed."
              action={
                <Button size="sm" variant="secondary" onClick={() => void loadQueue()} disabled={loading}>
                  <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
                </Button>
              }
            />
          </div>
        ) : !rows ? (
          <SkeletonRows rows={3} cols={5} />
        ) : rows.length === 0 ? (
          <EmptyState
            size="sm"
            icon={<Inbox />}
            title="Nothing is waiting on you"
            hint="Room charges appear here when their outcome could not be determined, or when someone asked for a second opinion."
          />
        ) : (
          <Table label="Room charges awaiting a decision">
            <THead>
              <TR>
                <TH>Created</TH>
                <TH numeric>Amount</TH>
                <TH>State</TH>
                <TH className="hidden sm:table-cell">Last attempt</TH>
                <TH className="hidden md:table-cell">PMS answer</TH>
                <TH pinEnd>
                  <span className="sr-only">Actions</span>
                </TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r) => (
                <TR key={r.posting_id} onClick={() => open(r.posting_id)}>
                  <TD nowrap muted>{formatDate(r.created_at)}</TD>
                  <TD numeric className="font-medium">{money(r.amount_minor, r.currency, r.currency_exponent)}</TD>
                  <TD nowrap>
                    <StatusBadge tone={r.awaiting_manual_review ? "err" : "warn"}>
                      {STATE_WORDS[r.execution_state] ?? "State not recognised"}
                    </StatusBadge>
                  </TD>
                  <TD className="hidden sm:table-cell">
                    {r.latest_attempt_no != null ? `Attempt ${r.latest_attempt_no}` : "Not sent yet"}
                  </TD>
                  <TD muted className="hidden md:table-cell">{answerWords(r.latest_pa_as_status)}</TD>
                  <TD pinEnd className="w-0 text-end">
                    <Button
                      size="xs"
                      variant="secondary"
                      onClick={(e) => { e.stopPropagation(); open(r.posting_id); }}
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

      <Sheet open={selected !== null} onOpenChange={(v) => !v && requestClose()}>
        <SheetContent width="lg">
          <SheetHeader
            eyebrow="Room charge"
            title={detail ? money(detail.posting.amount_minor, detail.posting.currency, detail.posting.currency_exponent) : "Room charge"}
            description="What it was attached to, every attempt to post it, and what has been decided."
            badges={
              detail ? (
                <>
                  <StatusBadge tone={detail.posting.awaiting_manual_review ? "err" : "warn"}>
                    {STATE_WORDS[detail.posting.execution_state] ?? "State not recognised"}
                  </StatusBadge>
                  {detail.review.terminal_action && (
                    <Badge tone="neutral">Decided: {actionLabel(detail.review.terminal_action)}</Badge>
                  )}
                </>
              ) : undefined
            }
          />
          <SheetBody>
            {sheetErr && (
              <div className={FADE}>
                <Callout tone="danger">{sheetErr}</Callout>
              </div>
            )}
            {note && (
              <div className={FADE}>
                <Callout tone="success">{note}</Callout>
              </div>
            )}
            {detail && detailFailed && (
              <div className={FADE}>
                <Callout tone="warning">This charge could not be reloaded. What is shown may be out of date.</Callout>
              </div>
            )}

            {!detail ? (
              detailFailed ? (
                <div role="alert" className={FADE}>
                  <EmptyState
                    size="sm"
                    icon={<AlertTriangle />}
                    title="This charge could not be loaded"
                    hint="Check the appliance is reachable, then try again. Nothing has been changed."
                    action={
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={detailLoading}
                        onClick={() => selected && void fetchDetail(selected)}
                      >
                        <RefreshCw className={cn(detailLoading && "animate-spin motion-reduce:animate-none")} /> Try again
                      </Button>
                    }
                  />
                </div>
              ) : (
                <div className="space-y-3" aria-busy="true">
                  <span className="sr-only">Loading the charge</span>
                  <Skeleton className="h-24 w-full" />
                  <Skeleton className="h-32 w-full" />
                </div>
              )
            ) : (
              <div className={cn("space-y-6", FADE)}>
                <SheetSection title="What this charge was attached to">
                  <KeyValueGrid
                    columns={2}
                    items={[
                      {
                        label: "Amount",
                        value: money(detail.posting.amount_minor, detail.posting.currency, detail.posting.currency_exponent),
                      },
                      {
                        label: "Charge",
                        value: SETTLEMENT_WORDS[detail.pinned_evidence.settlement_status] ?? "State not recognised",
                      },
                      {
                        label: "Purchase",
                        value: PURCHASE_WORDS[detail.pinned_evidence.purchase_state] ?? "State not recognised",
                      },
                      {
                        label: "PMS connection",
                        value: `${CONNECTOR_WORDS[detail.pinned_evidence.connector_kind] ?? "Connector not recognised"} · ${
                          LIFECYCLE_WORDS[detail.pinned_evidence.interface_lifecycle_state] ?? "State not recognised"
                        }`,
                      },
                      { label: "Reservation number", value: detail.pinned_evidence.g_number || "Not recorded" },
                      {
                        label: "Sent to",
                        value: TARGET_WORDS[detail.pinned_evidence.posting_target_model] ?? "Not recognised",
                      },
                      {
                        label: "PMS connection up to date",
                        value: detail.diagnostics.interface_freshness_block ? "No" : "Yes",
                      },
                    ]}
                  />
                </SheetSection>

                <SheetSection
                  title={attemptsTitle(detail.diagnostics.attempt_count, detail.diagnostics.unknown_attempt_count)}
                >
                  {detail.attempts.length === 0 ? (
                    <p className="text-sm text-muted-foreground">This charge has never been sent to the PMS.</p>
                  ) : (
                    <div className="overflow-hidden rounded-md border border-border">
                      <Table density="compact" label="Attempts">
                        <THead>
                          <TR>
                            <TH numeric>Attempt</TH>
                            <TH numeric>PMS posting number</TH>
                            <TH>Room / reservation</TH>
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
                                <Badge tone={OUTCOME_TONE(a.outcome)}>{OUTCOME_WORDS[a.outcome] ?? "Not recognised"}</Badge>
                                {(a.outcome === "NOT_SENT" || a.outcome === "FAILED") && a.not_sent_reason && (
                                  <div className="mt-0.5 text-2xs text-muted-foreground" data-testid="not-sent-reason">
                                    {NOT_SENT_REASON_WORDS[a.not_sent_reason] ?? "Reason not recognised"}
                                  </div>
                                )}
                              </TD>
                              <TD>{answerWords(a.pa_as_status)}</TD>
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
                      &ldquo;Not sent&rdquo; means the appliance proved nothing reached the PMS. It is not a PMS failure.
                    </p>
                  )}
                  {detail.diagnostics.has_unknown_history ? (
                    <Callout tone="warning">
                      An attempt ended with an unknown outcome. Nobody knows whether the guest&apos;s bill was charged,
                      and nothing has been sent again automatically. That is what this decision is for.
                    </Callout>
                  ) : null}
                </SheetSection>

                {detail.review.history.length > 0 ? (
                  <SheetSection title="Decisions already recorded">
                    <div className="overflow-hidden rounded-md border border-border">
                      <HistoryList
                        label="Decisions already recorded"
                        items={detail.review.history.map((h, i) => {
                          const ev = evidenceWords(h.evidence as Evidence);
                          return {
                            id: String(i),
                            title: actionLabel(h.action),
                            actor: actorWords(h.actor),
                            at: formatDate(h.created_at),
                            summary: ev ? `${h.reason} · ${ev}` : h.reason,
                          };
                        })}
                      />
                    </div>
                  </SheetSection>
                ) : null}

                <SheetSection title="Record a decision">
                  {allowed.length === 0 ? (
                    <p className="text-sm text-muted-foreground">Nothing further can be recorded against this charge.</p>
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
                      {onlyEscalate && onlyEscalateReason ? (
                        <Callout tone="neutral">{onlyEscalateReason}</Callout>
                      ) : null}
                      <Field
                        label="What did you establish?"
                        htmlFor="review-action"
                        hint={spec ? ACTION_WORDS[spec.action]?.explain : "Only the decisions this charge can still take are offered."}
                      >
                        <Select id="review-action" value={action} onChange={(e) => setAction(e.target.value)}>
                          <option value="">Choose…</option>
                          {offered.map((a) => (
                            <option key={a.action} value={a.action}>
                              {actionLabel(a.action)}
                            </option>
                          ))}
                        </Select>
                      </Field>
                      {spec?.terminal ? (
                        <div className={FADE}>
                          <Callout tone="warning">
                            This is a final decision. It is recorded once and can never be changed.
                          </Callout>
                        </div>
                      ) : null}

                      <Field label="Why" htmlFor="review-reason" hint={`Recorded in the audit log. One line, at most ${MAX_REASON} characters.`}>
                        <Input
                          id="review-reason"
                          value={reason}
                          maxLength={MAX_REASON}
                          onChange={(e) => setReason(e.target.value)}
                        />
                      </Field>

                      {spec?.needs_evidence ? (
                        <div className={cn("space-y-4", FADE)}>
                          <FieldGrid cols={2}>
                            <Field label="Evidence source" htmlFor="review-ev-source">
                              <Select id="review-ev-source" value={evSource} onChange={(e) => setEvSource(e.target.value)}>
                                <option value="">Choose…</option>
                                {sources.map((s) => (
                                  <option key={s} value={s}>
                                    {SOURCE_WORDS[s] ?? "Source not recognised"}
                                  </option>
                                ))}
                              </Select>
                            </Field>
                            <Field
                              label="Reference to it"
                              htmlFor="review-ev-ref"
                              hint="A reference to what you checked, never its contents. It goes into a permanent audit record."
                            >
                              <Input
                                id="review-ev-ref"
                                placeholder="For example: PMS folio screen, 14:22"
                                maxLength={MAX_REFERENCE}
                                value={evRef}
                                onChange={(e) => setEvRef(e.target.value)}
                              />
                            </Field>
                          </FieldGrid>
                          <Field
                            label={evSource === "OTHER_DOCUMENTED" ? "Note" : "Note (optional)"}
                            htmlFor="review-ev-note"
                            hint={
                              evSource === "OTHER_DOCUMENTED"
                                ? "Describe where you checked. One line."
                                : "Anything else about what you checked. One line."
                            }
                          >
                            <Input
                              id="review-ev-note"
                              maxLength={MAX_NOTE}
                              value={evNote}
                              onChange={(e) => setEvNote(e.target.value)}
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
                    </form>
                  )}
                </SheetSection>
              </div>
            )}
          </SheetBody>
          <SheetFooter>
            {asking ? (
              discardPrompt
            ) : (
              <>
                {canDecide && blocked && !busy && (
                  <p id={`${formId}-blocked`} className="me-auto text-caption text-muted-foreground">{blocked}</p>
                )}
                <Button variant="ghost" disabled={busy} onClick={requestClose}>
                  Close
                </Button>
                {canDecide && (
                  <Button
                    type="submit"
                    form={formId}
                    loading={busy}
                    disabled={blocked !== undefined}
                    aria-describedby={blocked ? `${formId}-blocked` : undefined}
                  >
                    {action === "ESCALATE" ? "Ask for a second opinion" : "Record decision"}
                  </Button>
                )}
              </>
            )}
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </PageShell>
  );
}
