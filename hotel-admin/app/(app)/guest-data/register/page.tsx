"use client";

// REQUEST REGISTER (migration 0147).
//
// The law gives the hotel one month to answer a guest who asks for their data or for its erasure, and two more months
// for a complex request (told within the first month). A refusal must say why and that the person may complain. This
// page is the hotel's record of each request: when it came, when it is due, how the person was verified and how it was
// answered. It decides nothing and runs no export or erasure; those are done on the Guest data requests pages, and their
// reference is written here. Who asked is kept only while the request is open: answering shows it once, as the hotel's
// own file of the request, and removes it.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { api } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Select, Field } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg } from "@/lib/utils";
import { ClipboardList } from "lucide-react";
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

  async function open(e: React.FormEvent) {
    e.preventDefault();
    setFile(null);
    setBusy(true); setErr(null);
    try {
      await api.post("/guest-data-rights/register", { kind, received_at: received, requester: requester.trim(), verification_method: method });
      setRequester("");
      toast.success("Request recorded", "It is due one month after the day it was received.");
      await load();
    } catch (e2) { setErr(errMsg(e2)); } finally { setBusy(false); }
  }

  async function extend(r: Row) {
    const reason = window.prompt("Why does this request need two more months? (do not name the guest)");
    if (!reason) return;
    setBusy(true); setErr(null);
    try {
      await api.post(`/guest-data-rights/register/${r.id}/extend`, { reason });
      toast.success("Extended", "Tell the person within the first month, and why.");
      await load();
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  async function answer(e: React.FormEvent) {
    e.preventDefault();
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

  return (
    <PageShell>
      <PageHeader
        icon={<ClipboardList />}
        eyebrow="System"
        title="Request register"
        description="Each data request, when it is due and how it was answered."
      />

      <Callout tone="info">
        A request is answered within one month; a complex or numerous request may take two more months if the person is told
        within the first month. A refusal, or part of one, must name its ground and tell the person they may complain to the
        data-protection authority. Carry the request out on <Link className="underline" href="/guest-data">Guest data requests</Link>{" "}
        or <Link className="underline" href="/guest-data/assisted">Assisted requests</Link> and write its reference here.
      </Callout>

      <ErrorBanner err={err} />

      {file && (
        <Callout tone="success" title={`Request ${String(file.reference)} closed`}>
          The hotel&rsquo;s file of this request (shown once, then removed from the register): {REGISTER_KIND_WORDS[String(file.kind)] ?? String(file.kind)},
          from <strong>{String(file.requester)}</strong>, received {String(file.received_at)}, due {String(file.due_at)},
          {file.answered_in_time ? " answered in time" : " answered late"}.{" "}
          <Button size="sm" variant="outline" onClick={() => setFile(null)}>Dismiss</Button>
        </Callout>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Record a request</CardTitle>
          <CardDescription>Use the day it was received, not today, if it came earlier.</CardDescription>
        </CardHeader>
        <CardBody>
          <form className="grid gap-3 sm:grid-cols-2" onSubmit={open}>
            <Field label="What is asked">
              <Select value={kind} onChange={(e) => setKind(e.target.value)}>
                {Object.entries(REGISTER_KIND_WORDS).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
              </Select>
            </Field>
            <Field label="Received on"><Input type="date" value={received} max={today()} onChange={(e) => setReceived(e.target.value)} /></Field>
            <Field label="Who is asking" hint="A name or a reference. Kept only while the request is open.">
              <Input value={requester} maxLength={120} onChange={(e) => setRequester(e.target.value)} />
            </Field>
            <Field label="How you verified them">
              <Select value={method} onChange={(e) => setMethod(e.target.value)}>
                {METHOD_OPTIONS.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
              </Select>
            </Field>
            <div className="sm:col-span-2">
              <Button type="submit" variant="primary" disabled={busy || requester.trim().length < 2}>Record request</Button>
            </div>
          </form>
        </CardBody>
      </Card>

      {answering && (
        <Card>
          <CardHeader>
            <CardTitle>Close request {answering.ref}</CardTitle>
            <CardDescription>Do not put the guest&rsquo;s name, address, phone, room or reservation in the note.</CardDescription>
          </CardHeader>
          <CardBody>
            <form className="grid gap-3 sm:grid-cols-2" onSubmit={answer}>
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
              <Field label="Erasure reference (if any)"><Input value={erasureRef} placeholder="ER-…" onChange={(e) => setErasureRef(e.target.value)} /></Field>
              <Field label="Assisted request reference (if any)"><Input value={assistedRef} placeholder="AR-…" onChange={(e) => setAssistedRef(e.target.value)} /></Field>
              <Field label="Note" hint="Shown once when you close it, then removed." className="sm:col-span-2">
                <Input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} />
              </Field>
              <div className="flex gap-2 sm:col-span-2">
                <Button type="submit" variant="primary" disabled={busy || (refusal && outcome === "NONE")}>Close request</Button>
                <Button type="button" variant="outline" onClick={resetAnswer}>Cancel</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Requests</CardTitle>
          <CardDescription>Open ones first, soonest due on top.</CardDescription>
        </CardHeader>
        <CardBody>
          {rows !== null && rows.length === 0 && <EmptyState title="No request recorded" />}
          {rows !== null && rows.length > 0 && (
            <div className="overflow-x-auto">
              <Table>
                <THead>
                  <TR><TH>Reference</TH><TH>Request</TH><TH>Received</TH><TH>Due</TH><TH>Status</TH><TH><span className="sr-only">Actions</span></TH></TR>
                </THead>
                <TBody>
                  {rows.map((r) => (
                    <TR key={r.id}>
                      <TD className="font-mono">{r.ref}</TD>
                      <TD>
                        <div>{REGISTER_KIND_WORDS[r.kind]}</div>
                        {r.status === "OPEN" && <div className="text-caption text-muted-foreground">{r.requester}</div>}
                        {r.erasure_ref && <div className="font-mono text-caption">{r.erasure_ref}</div>}
                        {r.assisted_ref && <div className="font-mono text-caption">{r.assisted_ref}</div>}
                      </TD>
                      <TD>{r.received_at}</TD>
                      <TD>
                        {r.due_at}{r.extended ? " (extended)" : ""}
                        {r.status === "OPEN" && <div className={r.overdue ? "text-caption text-danger" : "text-caption text-muted-foreground"}>{dueWords(r.days_left, r.overdue)}</div>}
                      </TD>
                      <TD><Badge tone={REGISTER_STATUS_TONE[r.status]}>{REGISTER_STATUS_LABEL[r.status]}</Badge></TD>
                      <TD className="whitespace-nowrap text-right">
                        {r.status === "OPEN" && (
                          <>
                            {!r.extended && !r.overdue && (
                              <Button size="sm" variant="outline" disabled={busy} aria-label={`Extend request ${r.ref}`} onClick={() => void extend(r)}>Extend</Button>
                            )}{" "}
                            <Button size="sm" variant="outline" aria-label={`Close request ${r.ref}`} onClick={() => { setErr(null); setFile(null); resetAnswer(); setAnswering(r); }}>Close</Button>
                          </>
                        )}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </CardBody>
      </Card>
    </PageShell>
  );
}
