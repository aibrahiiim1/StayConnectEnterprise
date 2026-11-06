"use client";

import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, CircleCheck, RefreshCw, ShieldAlert } from "lucide-react";
import {
  api,
  ApiError,
  RECOVERY_RESOLUTIONS,
  RecoveryHold,
  RecoveryResolution,
  RecoveryStatus,
  ZeroAttemptQueue,
  ZeroAttemptRow,
} from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { MonoId, Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { cn, formatDate } from "@/lib/utils";
import { money } from "./format";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const MIN_NOTE = 10;
const MAX_REASON = 500;
const MAX_REFERENCE = 120;
const MAX_NOTE = 500;

const RESOLUTION_TEXT: Record<RecoveryResolution, string> = {
  CONFIRMED_COMPLETED: "It already went through (checked with the provider or on the guest's bill)",
  CONFIRMED_NOT_COMPLETED: "It never went through (checked that nothing was charged or posted)",
  ABANDONED: "No longer needed: nothing will be done about it",
  ESCALATED: "Someone else must decide before it can be concluded",
};

const RESOLVED_WORDS: Record<string, string> = {
  CONFIRMED_COMPLETED: "Reconciled: it went through",
  CONFIRMED_NOT_COMPLETED: "Reconciled: it never went through",
  ABANDONED: "Reconciled: no longer needed",
  ESCALATED: "Waiting for someone else",
};

const KIND_TEXT: Record<string, string> = {
  POSTING_OUTBOX: "Room charge",
  PAYMENT_TRANSACTION: "Payment",
  SETTLEMENT: "Package payment",
};

const HELD_WORDS: Record<string, string> = {
  QUEUED: "Waiting to be sent",
  IN_FLIGHT: "Being sent",
  CREATED: "Started",
  PENDING: "Waiting for the provider",
  UNKNOWN: "Not known whether it went through",
  REQUIRED: "Waiting for payment",
  IN_PROGRESS: "Being paid",
  MANUAL_REVIEW: "Waiting for a decision",
};

const REASON_WORDS: Record<string, string> = {
  RESTORE_DETECTED: "a database restore was detected",
  OPERATOR_DECLARED: "an operator started it",
  INITIAL: "the site was set up",
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

function exponentFor(currency: string): number {
  try {
    return new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
  } catch {
    return 2;
  }
}

function heldAmount(h: RecoveryHold): string {
  if (h.amount_minor === null || !h.currency) return "Amount not recorded";
  return money(h.amount_minor, h.currency, exponentFor(h.currency));
}

function noteBlocked(v: string | undefined): string | undefined {
  const n = (v ?? "").trim().length;
  if (n === 0) return "Write how you established it.";
  if (n < MIN_NOTE) return `Write at least ${MIN_NOTE} characters (${n} so far).`;
  return undefined;
}

type Msg = { tone: "success" | "danger"; text: string };

const RECOVERY_CODE_WORDS: [string, string][] = [
  ["RECOVERY_NOT_ACTIVE", "This site is not in financial recovery, so there is nothing to release."],
  ["RECOVERY_HOLD_UNKNOWN", "That held item no longer exists. Refresh the list."],
  ["RECOVERY_HOLD_ALREADY_RESOLVED", "Someone has already recorded a decision for that item. Refresh the list."],
  ["RECOVERY_NOTE_REQUIRED", "Write how you established this: the note is part of the permanent record."],
  ["RECOVERY_RESOLUTION_INVALID", "Choose what you established about this item."],
  ["REVIEW_REASON_REQUIRED", "Give the reason for authorising the retry (at least 10 characters)."],
  ["REVIEW_POSTING_UNKNOWN", "That room charge no longer exists. Refresh the list."],
  ["RECOVERY_HOLDS_UNRESOLVED", "Some held items have not been reconciled yet. Reconcile them first."],
  ["RECOVERY_STATE_UNSAFE", "Some charges or payments are still being sent or paid. Recovery can be released once they have finished."],
];

export function recoveryWords(e: any, fallback: string): string {
  const msg: string = e?.message ?? "";
  for (const [code, words] of RECOVERY_CODE_WORDS) if (msg.includes(code)) return words;
  return msg || fallback;
}

function recoveryError(e: unknown, fallback: string): string {
  const code = e instanceof ApiError ? e.code : "";
  const status = e instanceof ApiError ? e.status : 0;
  if (code === "reauth_required" || status === 401) return "Your password was not accepted. Nothing was recorded.";
  if (code === "reauth_unavailable") return "Your password could not be checked right now. Nothing was recorded; try again.";
  if (code === "forbidden" || status === 403) return "Your role cannot record recovery decisions. Nothing was recorded.";
  if (code === "unavailable" || status === 503) return "The payment service is not available right now. Nothing was recorded; try again.";
  if (code === "holds_unresolved") return "Some held items have not been reconciled yet. Reconcile them first. Nothing was released.";
  if (code === "already_resolved" || code === "already_decided" || code === "stale_version")
    return "Someone has already recorded a decision for this item. The list has been reloaded.";
  if (code === "not_applicable") return "This charge cannot be authorized now. The list has been reloaded.";
  if (code === "reason_invalid")
    return `The reason was not accepted. Keep it to one line of at most ${MAX_REASON} characters, without passwords, card numbers or copied PMS messages. Nothing was recorded.`;
  if (code === "evidence_invalid" || code === "evidence_required")
    return `The evidence was not accepted. Choose where you checked and give a one-line reference of at most ${MAX_REFERENCE} characters, without passwords, card numbers or copied PMS messages. Nothing was recorded.`;
  if (code === "not_found" || status === 404) return "This item is no longer in this site's records. The list has been reloaded.";
  const msg = (e as { message?: string })?.message ?? "";
  for (const [c, words] of RECOVERY_CODE_WORDS) if (msg.includes(c)) return words;
  return fallback;
}

function needsReload(e: unknown): boolean {
  if (!(e instanceof ApiError)) return false;
  return ["already_resolved", "already_decided", "stale_version", "not_applicable", "not_found"].includes(e.code)
    || e.message.includes("RECOVERY_HOLD_ALREADY_RESOLVED") || e.message.includes("RECOVERY_HOLD_UNKNOWN");
}

function Message({ msg }: { msg?: Msg }) {
  if (!msg) return null;
  return (
    <div className={FADE}>
      <Callout tone={msg.tone}>{msg.text}</Callout>
    </div>
  );
}

export function FinancialRecoveryView({ canAct = true, rolesKnown = true }: { canAct?: boolean; rolesKnown?: boolean }) {
  const [status, setStatus] = useState<RecoveryStatus | null>(null);
  const [holds, setHolds] = useState<RecoveryHold[] | null>(null);
  const [zero, setZero] = useState<ZeroAttemptQueue | null>(null);
  const [statusFailed, setStatusFailed] = useState(false);
  const [holdsFailed, setHoldsFailed] = useState(false);
  const [zeroFailed, setZeroFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [msgs, setMsgs] = useState<Record<string, Msg>>({});
  const [busy, setBusy] = useState<string | null>(null);

  const [choice, setChoice] = useState<Record<string, RecoveryResolution>>({});
  const [evidence, setEvidence] = useState<Record<string, string>>({});
  const [password, setPassword] = useState("");

  const [zaReason, setZaReason] = useState<Record<string, string>>({});
  const [zaSource, setZaSource] = useState<Record<string, string>>({});
  const [zaRef, setZaRef] = useState<Record<string, string>>({});
  const [zaNote, setZaNote] = useState<Record<string, string>>({});

  const load = useCallback(async () => {
    setLoading(true);
    const [s, h, z] = await Promise.allSettled([
      api.get<{ recovery: RecoveryStatus }>("/financial-ops/recovery"),
      api.get<{ holds: RecoveryHold[] }>("/financial-ops/recovery/holds"),
      api.get<ZeroAttemptQueue>("/financial-ops/recovery/zero-attempt"),
    ]);
    if (s.status === "fulfilled") {
      setStatus(s.value.recovery);
      setStatusFailed(false);
    } else {
      setStatusFailed(true);
    }
    if (h.status === "fulfilled") {
      setHolds(h.value.holds ?? []);
      setHoldsFailed(false);
    } else {
      setHoldsFailed(true);
    }
    if (z.status === "fulfilled") {
      setZero(z.value);
      setZeroFailed(false);
    } else {
      setZeroFailed(true);
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  function say(key: string, msg: Msg | null) {
    setMsgs((m) => {
      const next = { ...m };
      if (msg) next[key] = msg;
      else delete next[key];
      return next;
    });
  }

  async function resolve(hold: RecoveryHold) {
    const resolution = choice[hold.hold_id];
    if (!resolution || noteBlocked(evidence[hold.hold_id]) || !password) return;
    setBusy(hold.hold_id);
    say(hold.hold_id, null);
    say("__page", null);
    try {
      await api.post(`/financial-ops/recovery/holds/${hold.hold_id}/resolve`, {
        resolution,
        note: (evidence[hold.hold_id] ?? "").trim(),
        password,
      });
      say("__page", { tone: "success", text: `${KIND_TEXT[hold.work_kind] ?? "Item"} reconciled. Nothing was sent again.` });
      await load();
    } catch (e) {
      if (needsReload(e)) await load();
      say(hold.hold_id, { tone: "danger", text: recoveryError(e, "The decision could not be recorded. Nothing was recorded; try again.") });
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  async function release() {
    if (noteBlocked(evidence["__release"]) || !password) return;
    setBusy("release");
    say("__release", null);
    say("__page", null);
    try {
      await api.post("/financial-ops/recovery/release", {
        note: (evidence["__release"] ?? "").trim(),
        password,
      });
      say("__page", { tone: "success", text: "Financial recovery released. Charging and payments have resumed." });
      await load();
    } catch (e) {
      if (needsReload(e)) await load();
      say("__release", { tone: "danger", text: recoveryError(e, "Recovery could not be released. Nothing was changed; try again.") });
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  function zeroBlocked(row: ZeroAttemptRow): string | undefined {
    const id = row.posting_id;
    if (!(zaReason[id] ?? "").trim()) return "Write why this charge must still go out.";
    if (!zaSource[id]) return "Choose where you checked.";
    if (!(zaRef[id] ?? "").trim()) return "Write a reference to what you checked.";
    if (zaSource[id] === "OTHER_DOCUMENTED" && !(zaNote[id] ?? "").trim()) return "Describe the source in the note.";
    if (!password) return "Enter your password at the top of the page.";
    return undefined;
  }

  async function authorizeZeroAttempt(row: ZeroAttemptRow) {
    const id = row.posting_id;
    if (zeroBlocked(row)) return;
    setBusy(id);
    say(id, null);
    say("__page", null);
    const note = (zaNote[id] ?? "").trim();
    try {
      await api.post(`/financial-ops/recovery/zero-attempt/${id}/authorize`, {
        reason: (zaReason[id] ?? "").trim(),
        evidence: {
          source_type: zaSource[id] ?? "",
          reference: (zaRef[id] ?? "").trim(),
          ...(note ? { note } : {}),
        },
        password,
      });
      say(id, {
        tone: "success",
        text: "One attempt authorized. Nothing has been sent: the charge goes back to the ordinary queue and is sent once, when recovery is released.",
      });
      await load();
    } catch (e) {
      if (needsReload(e)) await load();
      say(id, { tone: "danger", text: recoveryError(e, "The attempt could not be authorized. Nothing was recorded; try again.") });
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  const spin = (on: boolean) => cn(on && "animate-spin motion-reduce:animate-none");
  const refresh = status ? (
    <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
      <RefreshCw className={spin(loading)} /> Refresh
    </Button>
  ) : undefined;

  const header = (
    <PageHeader
      title="Recovery"
      description="After a restore, settle in-flight payments before charging resumes. Nothing is re-sent."
      actions={refresh}
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
                <><strong>Never transmitted</strong> charges were held before anything was sent to the PMS, so there is no attempt to review on Manual review. Once one is reconciled as &ldquo;It never went through&rdquo;, exactly one further attempt can be authorized; authorizing sends nothing immediately.</>,
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
        {statusFailed ? (
          <div role="alert" className={FADE}>
            <EmptyState
              icon={<AlertTriangle />}
              title="Recovery could not be loaded"
              hint="Check the appliance is reachable, then try again. Nothing has been changed."
              action={
                <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
                  <RefreshCw className={spin(loading)} /> Try again
                </Button>
              }
            />
          </div>
        ) : (
          <div className="space-y-4" aria-busy="true">
            <span className="sr-only">Loading recovery state</span>
            <Skeleton className="h-28 w-full" />
            <Skeleton className="h-40 w-full" />
          </div>
        )}
      </PageShell>
    );
  }

  const stale = statusFailed;

  if (!status.Active) {
    return (
      <PageShell>
        {header}
        {stale && (
          <div className={FADE}>
            <Callout tone="warning">The recovery state could not be refreshed. What is shown may be out of date.</Callout>
          </div>
        )}
        <Message msg={msgs["__page"]} />
        <Card className={FADE}>
          <CardBody className="flex flex-col gap-3 sm:flex-row sm:items-center">
            <CircleCheck className="size-5 shrink-0 text-success" aria-hidden />
            <div className="min-w-0 space-y-1">
              <StatusBadge tone="ok">Not in recovery</StatusBadge>
              <p className="text-sm text-muted-foreground">
                Charging and payments are running normally.
              </p>
            </div>
          </CardBody>
        </Card>
      </PageShell>
    );
  }

  const open = holds ?? [];
  const zq = zero?.queue ?? [];
  const sources = zero?.evidence_contract?.source_types ?? [];
  const reasonWords = REASON_WORDS[status.Reason] ?? "the reason was not recognised";
  const releaseBlocked = noteBlocked(evidence["__release"]) ?? (!password ? "Enter your password at the top of the page." : undefined);

  return (
    <PageShell>
      {header}

      {rolesKnown && !canAct && (
        <ReadOnlyNotice>Your role can see the recovery state but not record decisions or release it.</ReadOnlyNotice>
      )}
      {stale && (
        <div className={FADE}>
          <Callout tone="warning">The recovery state could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}
      <Message msg={msgs["__page"]} />

      <section className="overflow-hidden rounded-lg border border-destructive/40 bg-card shadow-card">
        <div className="flex flex-col gap-3 bg-destructive-subtle px-5 py-4 sm:flex-row sm:items-start">
          <ShieldAlert className="size-5 shrink-0 text-destructive-subtle-foreground" aria-hidden />
          <div className="min-w-0 space-y-2 text-sm text-foreground">
            <StatusBadge tone="err">Financial recovery</StatusBadge>
            <p>
              Money movement is held for this site. Nothing has been replayed and nothing will be: after a
              restore, a correct retry is how a guest gets charged twice.
            </p>
            <p className="tabular">
              {status.EnteredAt ? `Started ${formatDate(status.EnteredAt)} because ${reasonWords}. ` : `Started because ${reasonWords}. `}
              <strong>{status.HeldOpen.toLocaleString()}</strong> of {status.HeldTotal.toLocaleString()} items still to reconcile.
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
              hint="Each decision is confirmed with your password. It is cleared after every decision."
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

      {zeroFailed && (
        <div className={FADE}>
          <Callout tone="warning">
            Room charges that were never sent could not be loaded. Refresh to try again.
          </Callout>
        </div>
      )}

      {zq.length > 0 ? (
        <Card>
          <CardHeader className="block space-y-0.5">
            <CardTitle>Never transmitted ({zq.length})</CardTitle>
            <CardDescription className="max-w-3xl">
              These room charges were held before anything was sent to the PMS, so there is no attempt to review
              and they do not appear on Manual review. Authorizing sends nothing now: it allows exactly one later
              attempt, once recovery is released.
            </CardDescription>
          </CardHeader>
          <ul className="divide-y divide-border">
            {zq.map((z) => {
              const blocked = zeroBlocked(z);
              const id = z.posting_id;
              return (
                <li key={id} className="space-y-3 px-5 py-4">
                  <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                    <span className="text-emphasis tabular">{money(z.amount_minor, z.currency, z.currency_exponent)}</span>
                    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                      Reference <MonoId value={id} title="Room charge reference" />
                    </span>
                    {z.retry_authorized_attempt_no !== null ? (
                      <StatusBadge tone="ok">Attempt {z.retry_authorized_attempt_no} authorized</StatusBadge>
                    ) : z.hold_resolution ? (
                      <Badge tone={z.eligible_for_retry_authorization ? "warn" : "default"}>
                        {RESOLVED_WORDS[z.hold_resolution] ?? "Not recognised"}
                      </Badge>
                    ) : (
                      <Badge tone="default">Not yet reconciled</Badge>
                    )}
                  </div>
                  <Message msg={msgs[id]} />
                  {z.eligible_for_retry_authorization ? (
                    canAct ? (
                      <div className="space-y-3">
                        <div className="grid gap-3 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)] md:items-end">
                          <Field label="Why this charge must still go out" htmlFor={`za-reason-${id}`} hint="One line.">
                            <Input
                              id={`za-reason-${id}`}
                              maxLength={MAX_REASON}
                              value={zaReason[id] ?? ""}
                              onChange={(e) => setZaReason({ ...zaReason, [id]: e.target.value })}
                            />
                          </Field>
                          <Field label="Evidence source for this posting" htmlFor={`za-source-${id}`}>
                            <Select
                              id={`za-source-${id}`}
                              value={zaSource[id] ?? ""}
                              onChange={(e) => setZaSource({ ...zaSource, [id]: e.target.value })}
                            >
                              <option value="">Choose…</option>
                              {sources.map((t) => (
                                <option key={t} value={t}>
                                  {SOURCE_WORDS[t] ?? "Source not recognised"}
                                </option>
                              ))}
                            </Select>
                          </Field>
                          <Field label="Reference to that evidence" htmlFor={`za-ref-${id}`}>
                            <Input
                              id={`za-ref-${id}`}
                              placeholder="For example: folio 4471"
                              maxLength={MAX_REFERENCE}
                              value={zaRef[id] ?? ""}
                              onChange={(e) => setZaRef({ ...zaRef, [id]: e.target.value })}
                            />
                          </Field>
                        </div>
                        {zaSource[id] === "OTHER_DOCUMENTED" && (
                          <div className={FADE}>
                            <Field label="Note" htmlFor={`za-note-${id}`} hint="Describe where you checked. One line.">
                              <Input
                                id={`za-note-${id}`}
                                maxLength={MAX_NOTE}
                                value={zaNote[id] ?? ""}
                                onChange={(e) => setZaNote({ ...zaNote, [id]: e.target.value })}
                              />
                            </Field>
                          </div>
                        )}
                        <div className="flex flex-wrap items-center justify-end gap-3">
                          {blocked && busy !== id && (
                            <p id={`za-blocked-${id}`} className="me-auto text-caption text-muted-foreground">{blocked}</p>
                          )}
                          <Button
                            loading={busy === id}
                            disabled={blocked !== undefined}
                            aria-describedby={blocked ? `za-blocked-${id}` : undefined}
                            onClick={() => void authorizeZeroAttempt(z)}
                          >
                            Authorize one attempt
                          </Button>
                        </div>
                      </div>
                    ) : null
                  ) : (
                    <p className="text-xs text-muted-foreground">
                      {z.retry_authorized_attempt_no !== null
                        ? "An attempt has already been authorized for this charge. Exactly one is allowed."
                        : "Reconcile this charge under Held work as “It never went through” first. The authorization rests on that finding."}
                    </p>
                  )}
                </li>
              );
            })}
          </ul>
          <CardFooter className="text-xs text-muted-foreground">
            A charge can be authorized only after it is reconciled as &ldquo;It never went through&rdquo;, and only once.
            Authorizing sends nothing now.
          </CardFooter>
        </Card>
      ) : null}

      {holdsFailed && !holds ? (
        <div role="alert" className={FADE}>
          <EmptyState
            icon={<AlertTriangle />}
            title="Held work could not be loaded"
            hint="Check the appliance is reachable, then try again. Nothing has been changed."
            action={
              <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
                <RefreshCw className={spin(loading)} /> Try again
              </Button>
            }
          />
        </div>
      ) : !holds ? (
        <Skeleton className="h-40 w-full" />
      ) : open.length === 0 ? (
        <Card>
          <CardBody className="space-y-4">
            {holdsFailed && (
              <Callout tone="warning">Held work could not be refreshed. What is shown may be out of date.</Callout>
            )}
            <EmptyState
              className="py-6"
              icon={<CircleCheck />}
              title="Everything has been reconciled"
              hint="No held items remain. Releasing recovery resumes charging and payments for this site."
            />
            {canAct && (
              <div className="space-y-3 border-t border-border pt-4">
                <Message msg={msgs["__release"]} />
                <Field
                  label="Why is it safe to resume?"
                  htmlFor="release-note"
                  hint={`Recorded in the audit log with your name. At least ${MIN_NOTE} characters. Recovery is released only when no charge or payment is still being sent or paid.`}
                >
                  <Textarea
                    id="release-note"
                    rows={3}
                    value={evidence["__release"] ?? ""}
                    onChange={(e) => setEvidence({ ...evidence, __release: e.target.value })}
                  />
                </Field>
                <div className="flex flex-wrap items-center gap-3">
                  <Button
                    loading={busy === "release"}
                    disabled={releaseBlocked !== undefined}
                    aria-describedby={releaseBlocked ? "release-blocked" : undefined}
                    onClick={() => void release()}
                  >
                    Release financial recovery
                  </Button>
                  {releaseBlocked && busy !== "release" && (
                    <p id="release-blocked" className="text-caption text-muted-foreground">{releaseBlocked}</p>
                  )}
                </div>
              </div>
            )}
          </CardBody>
        </Card>
      ) : (
        <Card>
          <CardHeader className="block space-y-0.5">
            <CardTitle>Held work ({open.length})</CardTitle>
            <CardDescription>
              For each item, record what you established and how. Recording a decision never re-sends anything.
            </CardDescription>
          </CardHeader>
          {holdsFailed && (
            <div className="px-5 pb-2">
              <Callout tone="warning">Held work could not be refreshed. What is shown may be out of date.</Callout>
            </div>
          )}
          <ul className="divide-y divide-border">
            {open.map((h) => {
              const kind = KIND_TEXT[h.work_kind] ?? "Item";
              const blocked = !choice[h.hold_id]
                ? "Choose what you established about this item."
                : noteBlocked(evidence[h.hold_id]) ?? (!password ? "Enter your password at the top of the page." : undefined);
              return (
                <li key={h.hold_id} className="space-y-3 px-5 py-4">
                  <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                    <span className="text-emphasis">{kind}</span>
                    <span className="tabular">{heldAmount(h)}</span>
                    <Badge tone="warn">When held: {HELD_WORDS[h.held_status] ?? "Not recognised"}</Badge>
                    <span className="text-xs text-muted-foreground">Held {formatDate(h.held_at)}</span>
                    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                      Reference <MonoId value={h.work_id} title={`${kind} reference`} />
                    </span>
                  </div>
                  <Message msg={msgs[h.hold_id]} />
                  {canAct && (
                    <div className="space-y-3">
                      <div className="grid gap-3 md:grid-cols-2 md:items-end">
                        <Field
                          label={`Conclusion for this ${kind.toLowerCase()}`}
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
                        <Field
                          label="Evidence for this decision"
                          htmlFor={`note-${h.hold_id}`}
                          hint={`How you established it, at least ${MIN_NOTE} characters.`}
                        >
                          <Input
                            id={`note-${h.hold_id}`}
                            placeholder="For example: provider dashboard shows no charge"
                            value={evidence[h.hold_id] ?? ""}
                            onChange={(e) => setEvidence({ ...evidence, [h.hold_id]: e.target.value })}
                          />
                        </Field>
                      </div>
                      <div className="flex flex-wrap items-center justify-end gap-3">
                        {blocked && busy !== h.hold_id && (
                          <p id={`res-blocked-${h.hold_id}`} className="me-auto text-caption text-muted-foreground">{blocked}</p>
                        )}
                        <Button
                          variant="secondary"
                          loading={busy === h.hold_id}
                          disabled={blocked !== undefined}
                          aria-describedby={blocked ? `res-blocked-${h.hold_id}` : undefined}
                          onClick={() => void resolve(h)}
                        >
                          Record
                        </Button>
                      </div>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
          <CardFooter className="text-xs text-muted-foreground">
            Recovery can only be released once every item above has been reconciled.
          </CardFooter>
        </Card>
      )}
    </PageShell>
  );
}
