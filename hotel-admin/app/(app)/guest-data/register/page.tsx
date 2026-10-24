"use client";

// Requests are answered within one month (two more if complex) and a refusal must say why. Who asked is kept only
// while the request is open.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { api } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Select, Field, FieldGrid } from "@/components/ui/input";
import { StatusBadge } from "@/components/ui/badge";
import { DialogForm } from "@/components/ui/dialog";
import { RowActions } from "@/components/ui/row-actions";
import { HelpSection } from "@/components/help";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg } from "@/lib/utils";
import { Plus } from "lucide-react";
import { METHOD_OPTIONS } from "@/lib/guest-data-assisted-words";
import {
  OUTCOME_OPTIONS, REGISTER_KIND_WORDS, REGISTER_STATUS_LABEL, REGISTER_STATUS_OPTIONS, REGISTER_STATUS_TONE, dueWords,
} from "@/lib/guest-data-register-words";

type Row = {
  id: string; ref: string; kind: "ACCESS" | "ERASURE" | "OTHER"; received_at: string; due_at: string; extended: boolean;
  extension_reason: string | null; requester: string; verification_method: string | null;
  status: "OPEN" | "ANSWERED" | "PARTLY_REFUSED" | "REFUSED" | "WITHDRAWN"; outcome: string | null;
  erasure_ref: string | null; assisted_ref: string | null; days_left: number | null; overdue: boolean;
};

const today = () => new Date().toISOString().slice(0, 10);

export default function RequestRegisterPage() {
  const toast = useToast();
  const [rows, setRows] = useState<Row[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [kind, setKind] = useState("ACCESS");
  const [received, setReceived] = useState(today());
  const [requester, setRequester] = useState("");
  const [method, setMethod] = useState("ID_CHECKED_IN_PERSON");
  const [recording, setRecording] = useState(false);

  const [extending, setExtending] = useState<Row | null>(null);
  const [extReason, setExtReason] = useState("");
  const [answering, setAnswering] = useState<Row | null>(null);
  const [status, setStatus] = useState("ANSWERED");
  const [outcome, setOutcome] = useState("NONE");
  const [note, setNote] = useState("");
  const [erasureRef, setErasureRef] = useState("");
  const [assistedRef, setAssistedRef] = useState("");
  const [file, setFile] = useState<Record<string, unknown> | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ requests: Row[] }>("/guest-data-rights/register");
      setRows(r.requests ?? []);
    } catch (e) { setErr(errMsg(e)); setRows([]); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  function resetAnswer() {
    setAnswering(null); setNote(""); setErasureRef(""); setAssistedRef(""); setStatus("ANSWERED"); setOutcome("NONE");
  }

  async function open() {
    setFile(null);
    setBusy(true); setErr(null);
    try {
      await api.post("/guest-data-rights/register", { kind, received_at: received, requester: requester.trim(), verification_method: method });
      setRequester("");
      setRecording(false);
      toast.success("Request recorded", "It is due one month after the day it was received.");
      await load();
    } catch (e2) { setErr(errMsg(e2)); } finally { setBusy(false); }
  }

  async function extend() {
    if (!extending) return;
    setBusy(true); setErr(null);
    try {
      await api.post(`/guest-data-rights/register/${extending.id}/extend`, { reason: extReason.trim() });
      toast.success("Extended", "Tell the person within the first month, and why.");
      setExtending(null); setExtReason("");
      await load();
    } catch (e2) { setErr(errMsg(e2)); } finally { setBusy(false); }
  }

  async function answer() {
    if (!answering) return;
    setBusy(true); setErr(null);
    try {
      const r = await api.post<Record<string, unknown>>(`/guest-data-rights/register/${answering.id}/answer`, {
        status, outcome, note: note.trim(), erasure_ref: erasureRef.trim(), assisted_ref: assistedRef.trim(),
      });
      setFile(r);
      resetAnswer();
      await load();
    } catch (e2) { setErr(errMsg(e2)); } finally { setBusy(false); }
  }

  const refusal = status === "PARTLY_REFUSED" || status === "REFUSED";

  const dialogOpen = recording || extending !== null || answering !== null;

  return (
    <PageShell>
      <PageHeader
        title="Request register"
        description="Each data request, when it is due and how it was answered."
        help={
          <>
            <HelpSection title="Deadlines">
              <p>
                A request is answered within one month; a complex or numerous request may take two more months if the
                person is told within the first month.
              </p>
            </HelpSection>
            <HelpSection title="Refusals">
              <p>
                A refusal, or part of one, must name its ground and tell the person they may complain to the
                data-protection authority.
              </p>
            </HelpSection>
            <HelpSection title="Carrying a request out">
              <p>
                Carry the request out on <strong>Guest data requests</strong> or <strong>Assisted requests</strong> and
                write its reference here when you close it. Who asked is kept only while the request is open.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <Button onClick={() => { setErr(null); setRecording(true); }}>
            <Plus /> New request
          </Button>
        }
      />

      <Callout tone="info">
        Answer within one month. A refusal must name its ground and say the person may complain to the data-protection
        authority. Carry it out on <Link className="underline" href="/guest-data">Guest data requests</Link>{" "}
        or <Link className="underline" href="/guest-data/assisted">Assisted requests</Link>.
      </Callout>

      {!dialogOpen && <ErrorBanner err={err} className="mb-0" />}

      {file && (
        <Callout tone="success" title={`Request ${String(file.reference)} closed`}>
          The hotel&rsquo;s file of this request (shown once, then removed from the register): {REGISTER_KIND_WORDS[String(file.kind)] ?? String(file.kind)},
          from <strong>{String(file.requester)}</strong>, received {String(file.received_at)}, due {String(file.due_at)},
          {file.answered_in_time ? " answered in time" : " answered late"}.{" "}
          <Button size="xs" variant="secondary" onClick={() => setFile(null)}>Dismiss</Button>
        </Callout>
      )}

      <SectionCard flush title="Requests" description="Open ones first, soonest due on top.">
        {rows === null ? null : rows.length === 0 ? (
          <EmptyState size="sm" title="No request recorded" />
        ) : (
          <Table label="Data requests">
            <THead>
              <TR>
                <TH>Reference</TH>
                <TH>Request</TH>
                <TH className="hidden sm:table-cell">Received</TH>
                <TH>Due</TH>
                <TH>Status</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r) => (
                <TR key={r.id}>
                  <TD nowrap className="font-mono text-xs">{r.ref}</TD>
                  <TD>
                    <div>{REGISTER_KIND_WORDS[r.kind]}</div>
                    {r.status === "OPEN" && <div className="text-caption text-muted-foreground">{r.requester}</div>}
                    {r.erasure_ref && <div className="font-mono text-caption">{r.erasure_ref}</div>}
                    {r.assisted_ref && <div className="font-mono text-caption">{r.assisted_ref}</div>}
                  </TD>
                  <TD nowrap muted className="hidden sm:table-cell">{r.received_at}</TD>
                  <TD nowrap>
                    {r.due_at}{r.extended ? " (extended)" : ""}
                    {r.status === "OPEN" && <div className={r.overdue ? "text-caption text-destructive" : "text-caption text-muted-foreground"}>{dueWords(r.days_left, r.overdue)}</div>}
                  </TD>
                  <TD nowrap><StatusBadge tone={REGISTER_STATUS_TONE[r.status] === "neutral" ? "default" : REGISTER_STATUS_TONE[r.status]}>{REGISTER_STATUS_LABEL[r.status]}</StatusBadge></TD>
                  <TD className="w-0">
                    <RowActions
                      subject={r.ref}
                      primary={{
                        label: "Close",
                        hidden: r.status !== "OPEN",
                        onSelect: () => { setErr(null); setFile(null); setExtending(null); resetAnswer(); setAnswering(r); },
                      }}
                      items={[{
                        label: "Extend…",
                        hidden: r.status !== "OPEN" || r.extended || r.overdue,
                        disabled: busy,
                        onSelect: () => { setErr(null); setAnswering(null); setExtending(r); },
                      }]}
                    />
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </SectionCard>

      <DialogForm
        open={recording}
        onOpenChange={(v) => { if (!v) setRecording(false); }}
        title="Record a request"
        description="Use the day it was received, not today, if it came earlier."
        submitLabel="Record request"
        busy={busy}
        error={recording ? err : null}
        disabled={requester.trim().length < 2}
        onSubmit={open}
      >
        <FieldGrid cols={2}>
          <Field label="What is asked">
            <Select value={kind} onChange={(e) => setKind(e.target.value)}>
              {Object.entries(REGISTER_KIND_WORDS).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </Select>
          </Field>
          <Field label="Received on"><Input type="date" value={received} max={today()} onChange={(e) => setReceived(e.target.value)} /></Field>
        </FieldGrid>
        <Field label="Who is asking" hint="A name or a reference. Kept only while the request is open.">
          <Input value={requester} maxLength={120} onChange={(e) => setRequester(e.target.value)} />
        </Field>
        <Field label="How you verified them">
          <Select value={method} onChange={(e) => setMethod(e.target.value)}>
            {METHOD_OPTIONS.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
          </Select>
        </Field>
      </DialogForm>

      <DialogForm
        open={extending !== null}
        onOpenChange={(v) => { if (!v) { setExtending(null); setExtReason(""); } }}
        title={`Extend request ${extending?.ref ?? ""}`}
        description="Two more months, once, for a complex or numerous request. Tell the person within the first month."
        submitLabel="Extend"
        busy={busy}
        error={extending ? err : null}
        disabled={extReason.trim().length < 4}
        onSubmit={extend}
      >
        <Field label="Why it needs longer" hint="Do not name the guest or give an address or number.">
          <Input value={extReason} maxLength={500} onChange={(e) => setExtReason(e.target.value)} />
        </Field>
      </DialogForm>

      <DialogForm
        open={answering !== null}
        onOpenChange={(v) => { if (!v) resetAnswer(); }}
        size="lg"
        title={`Close request ${answering?.ref ?? ""}`}
        description="Do not put the guest’s name, address, phone, room or reservation in the note."
        submitLabel="Close request"
        busy={busy}
        error={answering ? err : null}
        disabled={refusal && outcome === "NONE"}
        disabledReason="A refusal needs its ground."
        onSubmit={answer}
      >
        <FieldGrid cols={2}>
          <Field label="Result">
            <Select value={status} onChange={(e) => setStatus(e.target.value)}>
              {REGISTER_STATUS_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </Select>
          </Field>
          <Field label={refusal ? "Ground for refusing" : "Anything kept or left out"} hint={refusal ? "Required. The person is told it." : undefined}>
            <Select value={outcome} onChange={(e) => setOutcome(e.target.value)}>
              {OUTCOME_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </Select>
          </Field>
          <Field label="Erasure reference" optional><Input value={erasureRef} placeholder="ER-…" onChange={(e) => setErasureRef(e.target.value)} /></Field>
          <Field label="Assisted request reference" optional><Input value={assistedRef} placeholder="AR-…" onChange={(e) => setAssistedRef(e.target.value)} /></Field>
        </FieldGrid>
        <Field label="Note" hint="Shown once when you close it, then removed.">
          <Input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} />
        </Field>
      </DialogForm>
    </PageShell>
  );
}
