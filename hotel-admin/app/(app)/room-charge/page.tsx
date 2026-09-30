"use client";

// HOTEL → ROOM CHARGE — which PMS interface a client's Internet package may be charged to, and whether room
// charge can work at all on this appliance.
//
// ROOM CHARGE IS A FINANCIAL ONBOARDING, NOT A SWITCH. Before OneGate posts a single charge, a site administrator
// records the posting target (always the RESERVATION: room number + reservation number, Phase-0 Amendment A1 —
// there is nothing to choose, and no folio is ever modelled or selected), the currency the PMS posts in, and the
// vendor's confirmation that reservation numbers are unique and never reused. That record is revisioned: the
// approval carries the revision it was made against (`expected_revision_id`), and a stale one is refused with
// 409 revision_conflict so two administrators cannot approve different facts about the same interface without
// one of them seeing it.
//
// ANSWER MEANINGS. Protel answers each posting with a status. Only a status the vendor has confirmed, per
// interface, as "definitely not posted" (NP, NG, NR, NA, RY) fails the purchase with its fixed stay effect;
// anything else — UR always, and any of those five until confirmed — is UNKNOWN and goes to manual review. The
// confirmations are append-only (CONFIRM / WITHDRAW with the vendor evidence) and a site administrator's call.
//
// ONLY FIAS, AND ONLY A SITE ADMINISTRATOR. Room charge posting is implemented for Protel FIAS only, so only a
// FIAS interface is offered the approval. The approval is a site administrator's decision — edged refuses it
// from any other role — so every other role sees the same table with no button and a sentence saying whose
// decision it is.
//
// POSTING MAY STILL BE WITHHELD. An appliance whose room-charge posting is not authorised (readiness
// PMS_POSTING_NOT_AUTHORISED on the room_charge module) can be configured here in full, but room charge is not
// offered to clients and nothing is posted to the PMS. The page shows a checklist at the top -- what is done, what
// is recommended and what is still needed -- because an operator who has just approved an interface will
// otherwise read "not offered" as a failed approval.
//
// The currency exponent is not asked: FIAS posts in minor units with two decimal places, and edged fixes it.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { api, ApiError, Whoami } from "@/lib/api";
import { canRead } from "@/lib/roles";
import {
  ANSWER_EFFECT_WORDS, ANSWER_NAMES, AnswerMeaning, OnboardingInterface, OnboardingResp, RESERVATION_TARGET, SiteModules,
  postingTargetLabel, saveErrorMessage,
} from "@/lib/payment-admin";
import { formatDate } from "@/lib/utils";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogForm } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input, Textarea } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { SkeletonRows } from "@/components/ui/misc";
import { NotAvailable, ReadOnlyNotice } from "@/components/ui/patterns";
import { Table, TBody, TD, TH, THead, TR, TableWrap } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import { ArrowUpRight, BedDouble, CheckCircle2, ShieldCheck, Undo2 } from "lucide-react";
import { CodeList, CodeText } from "../payment-methods/codes";

const FIAS = "protel-fias";
const ATTESTATION_MIN = 20;
const ATTESTATION_MAX = 2000;
const REASON_MIN = 4;
const REASON_MAX = 500;
const EVIDENCE_MIN = 20;
const EVIDENCE_MAX = 2000;

type AnswerChange = { iface: OnboardingInterface; meaning: AnswerMeaning; action: "CONFIRM" | "WITHDRAW" };

export default function RoomChargePage() {
  const toast = useToast();
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const readable = roles === null ? false : canRead("pms-financial-onboarding", roles);
  const isSiteAdmin = roles?.includes("site_admin") ?? false;

  const [data, setData] = useState<OnboardingResp | null>(null);
  const [readiness, setReadiness] = useState<string[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [approving, setApproving] = useState<OnboardingInterface | null>(null);
  const [answerChange, setAnswerChange] = useState<AnswerChange | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<OnboardingResp>("/pms-financial-onboarding");
      setData({ interfaces: r?.interfaces ?? [], posting_target_models: r?.posting_target_models ?? [] });
    } catch (e) {
      setErr(e);
      setData({ interfaces: [], posting_target_models: [] });
    }
    // Module readiness is advisory on this page: the table is still correct without it.
    try {
      const m = await api.get<SiteModules>("/modules");
      setReadiness(m?.modules?.room_charge?.readiness ?? []);
    } catch { /* readiness unknown; the note is simply not shown */ }
  }, []);
  useEffect(() => { if (readable) load(); }, [readable, load]);

  const postingWithheld = readiness.includes("PMS_POSTING_NOT_AUTHORISED");
  const otherReadiness = readiness.filter((c) => c !== "PMS_POSTING_NOT_AUTHORISED");

  const header = (
    <PageHeader
      icon={<BedDouble />}
      eyebrow="Hotel"
      title="Room charge"
      description="Approve a PMS interface for room charge, so a verified guest can charge an Internet package to their room."
      help={
        <>
          <HelpSection title="What approval records">
            <HelpList
              items={[
                <><strong>Posting target</strong> — always the guest&rsquo;s reservation: the room number and the reservation number together. Protel decides which folio receives the charge.</>,
                <><strong>Base currency</strong> — the currency the PMS posts in. Packages charged to a room are priced in it.</>,
                <><strong>Vendor confirmation</strong> — the PMS vendor&rsquo;s confirmation that reservation numbers are never reused. It is kept with the approval.</>,
              ]}
            />
          </HelpSection>
          <HelpSection title="Answer meanings">
            <p>
              Protel answers every charge with a status. A status counts as &ldquo;nothing was posted&rdquo; only after the
              vendor has confirmed it for this interface. Until then, and always for UR, the outcome is unknown and the
              charge goes to Manual review.
            </p>
          </HelpSection>
          <HelpSection title="Who can approve">
            <p>
              Only a site administrator, and only for a FIAS interface. Everyone else sees the approval and its state.
            </p>
          </HelpSection>
          <HelpSection title="Where clients see it">
            <p>
              Room charge is one of the payment methods under Internet offering → Payment methods. Once an interface is
              approved and posting is authorised on this appliance, a guest who signs in with their room can choose it.
            </p>
          </HelpSection>
        </>
      }
    />
  );

  if (roles === null || (readable && data === null)) {
    return (
      <PageShell>
        {header}
        <Card><SkeletonRows rows={3} cols={5} /></Card>
      </PageShell>
    );
  }

  if (!readable) {
    return (
      <PageShell>
        {header}
        <NotAvailable title="Not available to your role" reason="Your role cannot see the room charge approval for this site's PMS interfaces." />
      </PageShell>
    );
  }

  const interfaces = data?.interfaces ?? [];

  return (
    <PageShell>
      {header}
      {!isSiteAdmin && (
        <ReadOnlyNotice>Approving a PMS interface for room charge is a site administrator decision.</ReadOnlyNotice>
      )}
      {postingWithheld && <SetupChecklist interfaces={data?.interfaces ?? []} />}
      {otherReadiness.length > 0 && (
        <Callout tone="info" title="Room charge is not ready">
          <CodeList codes={otherReadiness} />
        </Callout>
      )}
      <ErrorBanner err={err} className="mb-0" />

      <Card>
        <CardHeader className="items-start">
          <div className="min-w-0 space-y-1">
            <CardTitle>PMS interfaces</CardTitle>
            <CardDescription>
              Room charge is supported on FIAS interfaces.{" "}
              <Link href="/payment-methods" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
                Payment methods <ArrowUpRight className="size-3.5" aria-hidden />
              </Link>
            </CardDescription>
          </div>
        </CardHeader>
        <TableWrap>
          {interfaces.length === 0 ? (
            <p className="px-5 py-4 text-sm text-muted-foreground">No PMS interface exists yet. Add one under Hotel → PMS connection.</p>
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Interface</TH>
                  <TH>Room charge</TH>
                  <TH>Currency</TH>
                  <TH>Posting target</TH>
                  <TH>Approved</TH>
                  {isSiteAdmin && <TH className="text-right">Action</TH>}
                </TR>
              </THead>
              <TBody>
                {interfaces.map((i) => (
                  <TR key={i.pms_interface_id}>
                    <TD>
                      <div className="font-medium">{i.display_label || i.pms_interface_id}</div>
                      <div className="text-xs text-muted-foreground">
                        {i.connector_kind === FIAS ? "Protel FIAS" : i.connector_kind} · {i.lifecycle_state}
                      </div>
                    </TD>
                    <TD>
                      {i.ready ? (
                        <Badge tone="ok" dot>Ready</Badge>
                      ) : (
                        <div className="space-y-1">
                          <Badge tone={i.connector_kind === FIAS ? "warn" : "default"}>Not ready</Badge>
                          {i.reason && <div className="text-xs text-muted-foreground"><CodeText code={i.reason} /></div>}
                        </div>
                      )}
                    </TD>
                    <TD className="font-mono">{i.financial_base_currency ?? "—"}</TD>
                    <TD>{postingTargetLabel(i.posting_target_model)}</TD>
                    <TD className="text-xs">
                      {i.approved_at ? (
                        <>
                          <div className="whitespace-nowrap">{formatDate(i.approved_at)}</div>
                          {i.approved_by && <div className="break-all text-muted-foreground">{i.approved_by}</div>}
                        </>
                      ) : "—"}
                    </TD>
                    {isSiteAdmin && (
                      <TD className="text-right">
                        {i.connector_kind === FIAS ? (
                          <Button
                            size="xs"
                            variant={i.ready ? "secondary" : "primary"}
                            onClick={() => setApproving(i)}
                            aria-label={`${i.ready ? "Update room charge approval for" : "Approve for room charge:"} ${i.display_label || i.pms_interface_id}`}
                          >
                            <CheckCircle2 /> {i.ready ? "Update approval" : "Approve for room charge"}
                          </Button>
                        ) : (
                          <span className="text-xs text-muted-foreground">FIAS only</span>
                        )}
                      </TD>
                    )}
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </TableWrap>
      </Card>

      {interfaces.filter((i) => i.connector_kind === FIAS).map((i) => (
        <AnswerMeaningsCard
          key={i.pms_interface_id}
          iface={i}
          canChange={isSiteAdmin}
          onChange={(meaning, action) => setAnswerChange({ iface: i, meaning, action })}
        />
      ))}

      {isSiteAdmin && (
        <AnswerDialog
          change={answerChange}
          onClose={() => setAnswerChange(null)}
          onRecorded={async (action) => {
            setAnswerChange(null);
            toast.success(action === "CONFIRM" ? "Answer meaning confirmed" : "Confirmation withdrawn");
            await load();
          }}
        />
      )}

      {isSiteAdmin && (
        <ApproveDialog
          iface={approving}
          onClose={() => setApproving(null)}
          onApproved={async () => {
            setApproving(null);
            toast.success("Approved for room charge");
            await load();
          }}
          onConflict={async (message) => {
            setApproving(null);
            setErr(message);
            await load();
          }}
        />
      )}
    </PageShell>
  );
}

// WHAT ROOM CHARGE STILL NEEDS. An operator who has just approved an interface and still sees "not offered" needs
// to know that their part is done and what is missing -- not a sentence that reads as if the approval failed. The
// last step is the appliance's posting switch: a safety switch set when the appliance is installed, not a setting
// on any page, because switching it on makes real charges reach the PMS.
function SetupChecklist({ interfaces }: { interfaces: OnboardingInterface[] }) {
  const fias = interfaces.filter((i) => i.connector_kind === FIAS);
  const approved = fias.filter((i) => i.approved_at || i.posting_target_model === "RESERVATION");
  const meanings = approved.flatMap((i) => i.answer_meanings ?? []);
  const confirmed = meanings.filter((m) => m.confirmed).length;
  const steps: { done: boolean; title: string; detail: string; optional?: boolean }[] = [
    {
      done: approved.length > 0,
      title: "Approve a PMS interface for room charge",
      detail: approved.length > 0
        ? `Done: ${approved.map((i) => i.display_label || "FIAS interface").join(", ")}.`
        : "Use “Approve for room charge” on the FIAS interface below.",
    },
    {
      done: meanings.length > 0 && confirmed === meanings.length,
      optional: true,
      title: "Confirm Protel's answer meanings",
      detail: meanings.length === 0
        ? "Available once an interface is approved."
        : `${confirmed} of ${meanings.length} confirmed. Recommended: an unconfirmed answer is treated as unknown and the charge goes to Manual review instead of failing cleanly.`,
    },
    {
      done: false,
      title: "Switch on sending charges to the PMS on this appliance",
      detail: "Off. This is a safety switch set when the appliance is installed, not a setting on this page, because it lets real charges reach Protel. Switching it on is a Product Owner decision; ask your OneGate installer.",
    },
  ];
  return (
    <Card data-testid="room-charge-checklist">
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle>Room charge is not offered to clients yet</CardTitle>
          <CardDescription>
            Your configuration is saved. Until every required step below is done, clients do not see Room charge and
            nothing is sent to the PMS.
          </CardDescription>
        </div>
      </CardHeader>
      <ol className="space-y-3 px-5 pb-5">
        {steps.map((st, n) => (
          <li key={st.title} className="flex gap-3">
            <span className="mt-0.5 shrink-0">
              {st.done ? (
                <Badge tone="ok" dot>Done</Badge>
              ) : st.optional ? (
                <Badge tone="default">Recommended</Badge>
              ) : (
                <Badge tone="warn">Needed</Badge>
              )}
            </span>
            <div className="min-w-0">
              <div className="text-sm font-medium">{n + 1}. {st.title}</div>
              <div className="text-xs text-muted-foreground">{st.detail}</div>
            </div>
          </li>
        ))}
      </ol>
    </Card>
  );
}

function ApproveDialog({
  iface, onClose, onApproved, onConflict,
}: {
  iface: OnboardingInterface | null;
  onClose: () => void;
  onApproved: () => Promise<void>;
  onConflict: (message: string) => Promise<void>;
}) {
  const open = iface !== null;
  const [currency, setCurrency] = useState("");
  const [attestation, setAttestation] = useState("");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Prefill what is already recorded; clear everything, the password above all, on close.
  useEffect(() => {
    setCurrency(iface?.financial_base_currency ?? "");
    setAttestation("");
    setReason("");
    setPassword("");
    setError(null);
  }, [iface]);

  const attLen = attestation.trim().length;
  const ready =
    /^[A-Z]{3}$/.test(currency) &&
    attLen >= ATTESTATION_MIN && attLen <= ATTESTATION_MAX &&
    reason.trim().length >= REASON_MIN && reason.trim().length <= REASON_MAX &&
    password !== "";

  async function submit() {
    if (!iface || !ready) return;
    setBusy(true); setError(null);
    try {
      await api.post<{ revision_id: string }>(`/pms-financial-onboarding/${encodeURIComponent(iface.pms_interface_id)}`, {
        expected_revision_id: iface.current_revision_id,
        posting_target_model: "RESERVATION",
        currency,
        attestation: attestation.trim(),
        reason: reason.trim(),
        password,
      });
      await onApproved();
    } catch (e) {
      if (e instanceof ApiError && e.body?.error === "revision_conflict") {
        await onConflict(saveErrorMessage(e));
        return;
      }
      setError(saveErrorMessage(e));
      setPassword("");
    } finally {
      setBusy(false);
    }
  }

  return (
    <DialogForm
      open={open}
      onOpenChange={(v) => { if (!v) onClose(); }}
      title={`Approve ${iface?.display_label || "interface"} for room charge`}
      description="What you record here decides how charges are posted to guest reservations through this interface."
      size="lg"
      submitLabel="Approve for room charge"
      busyLabel="Approving…"
      busy={busy}
      error={error}
      disabled={!ready}
      onSubmit={submit}
    >
      <div className="space-y-1 rounded-md border border-border bg-surface/40 px-3.5 py-2.5">
        <div className="text-label">Posting target</div>
        <div className="text-sm font-medium">{RESERVATION_TARGET.label}</div>
        <p className="text-xs text-muted-foreground">{RESERVATION_TARGET.explanation}</p>
      </div>
      <Field label="Base currency" required hint="Three letters, the currency the PMS posts in. Two decimal places are fixed for FIAS.">
        <Input
          value={currency}
          maxLength={3}
          autoComplete="off"
          className="w-28 font-mono uppercase"
          onChange={(e) => setCurrency(e.target.value.replace(/[^A-Za-z]/g, "").toUpperCase())}
        />
      </Field>
      <Field
        label="Vendor confirmation"
        required
        hint={`The PMS vendor's confirmation that reservation numbers on this interface are unique and never reused: who confirmed it, when, and the document reference. ${attLen}/${ATTESTATION_MAX}, at least ${ATTESTATION_MIN}.`}
      >
        <Textarea
          value={attestation}
          maxLength={ATTESTATION_MAX}
          rows={4}
          placeholder="e.g. Protel support, 2026-09-20, ticket 48213: reservation numbers are unique in this database and never reused."
          onChange={(e) => setAttestation(e.target.value)}
        />
      </Field>
      <Field label="Reason" required hint={`Recorded in the activity log. ${reason.length}/${REASON_MAX}`}>
        <Input value={reason} maxLength={REASON_MAX} onChange={(e) => setReason(e.target.value)} />
      </Field>
      <Field label="Confirm your password" required>
        <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </Field>
    </DialogForm>
  );
}

/** The vendor-confirmed meaning of each PMS answer on one FIAS interface. */
function AnswerMeaningsCard({
  iface, canChange, onChange,
}: {
  iface: OnboardingInterface;
  canChange: boolean;
  onChange: (meaning: AnswerMeaning, action: "CONFIRM" | "WITHDRAW") => void;
}) {
  const meanings = iface.answer_meanings ?? [];
  const name = iface.display_label || iface.pms_interface_id;
  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle>Answer meanings (vendor-confirmed) — {name}</CardTitle>
          <CardDescription>
            Protel answers every room charge with a status. A status below counts as &ldquo;nothing was posted&rdquo; only
            once the vendor has confirmed it for this interface. An unconfirmed status, and UR always, is treated as
            unknown: the charge is held for Manual review, never counted as posted or failed. Recording these does not
            switch posting on — real posting to the PMS stays disabled on this appliance.
          </CardDescription>
        </div>
      </CardHeader>
      <TableWrap>
        {meanings.length === 0 ? (
          <p className="px-5 py-4 text-sm text-muted-foreground">The answer meanings could not be read for this interface.</p>
        ) : (
          <Table aria-label={`Answer meanings for ${name}`}>
            <THead>
              <TR>
                <TH>Answer</TH>
                <TH>When confirmed</TH>
                <TH>State</TH>
                {canChange && <TH className="text-right">Action</TH>}
              </TR>
            </THead>
            <TBody>
              {meanings.map((m) => (
                <TR key={m.as_status}>
                  <TD>
                    <div className="font-mono font-medium">{m.as_status}</div>
                    <div className="text-xs text-muted-foreground">{ANSWER_NAMES[m.as_status] ?? ""}</div>
                  </TD>
                  <TD className="max-w-md text-sm">{ANSWER_EFFECT_WORDS[m.effect] ?? m.effect}</TD>
                  <TD className="text-xs">
                    {m.confirmed ? (
                      <Badge tone="ok" dot>Confirmed</Badge>
                    ) : (
                      <Badge tone="default">Not confirmed — unknown</Badge>
                    )}
                    {m.recorded_at && (
                      <div className="mt-1 text-muted-foreground">
                        {m.confirmed ? "Confirmed" : "Withdrawn"} {formatDate(m.recorded_at)}
                        {m.recorded_by ? ` by ${m.recorded_by}` : ""}
                      </div>
                    )}
                    {m.evidence && <div className="mt-0.5 break-words text-muted-foreground">{m.evidence}</div>}
                  </TD>
                  {canChange && (
                    <TD className="text-right">
                      {m.confirmed ? (
                        <Button
                          size="xs"
                          variant="secondary"
                          onClick={() => onChange(m, "WITHDRAW")}
                          aria-label={`Withdraw confirmation of ${m.as_status} on ${name}`}
                        >
                          <Undo2 /> Withdraw
                        </Button>
                      ) : (
                        <Button
                          size="xs"
                          variant="secondary"
                          onClick={() => onChange(m, "CONFIRM")}
                          aria-label={`Confirm ${m.as_status} on ${name}`}
                        >
                          <ShieldCheck /> Confirm
                        </Button>
                      )}
                    </TD>
                  )}
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </TableWrap>
    </Card>
  );
}

function AnswerDialog({
  change, onClose, onRecorded,
}: {
  change: AnswerChange | null;
  onClose: () => void;
  onRecorded: (action: "CONFIRM" | "WITHDRAW") => Promise<void>;
}) {
  const open = change !== null;
  const [evidence, setEvidence] = useState("");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Clear everything, the password above all, whenever the dialog opens for another answer or closes.
  useEffect(() => {
    setEvidence("");
    setReason("");
    setPassword("");
    setError(null);
  }, [change]);

  const evLen = evidence.trim().length;
  const ready =
    evLen >= EVIDENCE_MIN && evLen <= EVIDENCE_MAX &&
    reason.trim().length >= REASON_MIN && reason.trim().length <= REASON_MAX &&
    password !== "";

  async function submit() {
    if (!change || !ready) return;
    setBusy(true); setError(null);
    try {
      await api.post<{ id: string; answer_meanings: AnswerMeaning[] }>(
        `/pms-financial-onboarding/${encodeURIComponent(change.iface.pms_interface_id)}/answer-confirmations`,
        {
          as_status: change.meaning.as_status,
          action: change.action,
          evidence: evidence.trim(),
          reason: reason.trim(),
          password,
        },
      );
      await onRecorded(change.action);
    } catch (e) {
      setError(saveErrorMessage(e));
      setPassword("");
    } finally {
      setBusy(false);
    }
  }

  const code = change?.meaning.as_status ?? "";
  const confirm = change?.action === "CONFIRM";
  const ifaceName = change?.iface.display_label || "this interface";

  return (
    <DialogForm
      open={open}
      onOpenChange={(v) => { if (!v) onClose(); }}
      title={confirm ? `Confirm what ${code} means on ${ifaceName}` : `Withdraw the confirmation of ${code} on ${ifaceName}`}
      description={confirm
        ? `Record that the PMS vendor confirmed ${code} (${ANSWER_NAMES[code] ?? code}) means nothing was posted.`
        : `From then on ${code} is treated as unknown again: a charge answered with it is held for Manual review.`}
      size="md"
      submitLabel={confirm ? `Confirm ${code}` : `Withdraw ${code}`}
      busyLabel="Recording…"
      busy={busy}
      error={error}
      disabled={!ready}
      onSubmit={submit}
    >
      {confirm && change && (
        <p className="rounded-md border border-border bg-surface/40 px-3.5 py-2.5 text-sm">
          <strong>Once confirmed:</strong> {ANSWER_EFFECT_WORDS[change.meaning.effect] ?? change.meaning.effect}
        </p>
      )}
      <Field
        label="Vendor evidence"
        required
        hint={`Who at the vendor confirmed it, when, and the document reference. ${evLen}/${EVIDENCE_MAX}, at least ${EVIDENCE_MIN}.`}
      >
        <Textarea
          value={evidence}
          maxLength={EVIDENCE_MAX}
          rows={3}
          placeholder="e.g. Protel support, 2026-09-20, FIAS specification v2.1 section 7.4"
          onChange={(e) => setEvidence(e.target.value)}
        />
      </Field>
      <Field label="Reason" required hint={`Recorded in the activity log. ${reason.length}/${REASON_MAX}`}>
        <Input value={reason} maxLength={REASON_MAX} onChange={(e) => setReason(e.target.value)} />
      </Field>
      <Field label="Confirm your password" required>
        <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </Field>
    </DialogForm>
  );
}
