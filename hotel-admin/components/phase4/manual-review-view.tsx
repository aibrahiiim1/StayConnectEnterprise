"use client";

// Phase 4 (DARK) — Financial Manual Review.
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

import { useCallback, useEffect, useState } from "react";
import {
  api,
  ReviewActionDoc,
  ReviewPostingDetail,
  ReviewQueueRow,
} from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";

function money(minor: number, currency: string, exponent = 2): string {
  return `${(minor / Math.pow(10, exponent)).toFixed(exponent)} ${currency}`;
}

const OUTCOME_TONE = (o: string) =>
  o === "UNKNOWN" ? "err" : o === "FAILED" ? "warn" : o === "ACKED" ? "ok" : "info";

export function ManualReviewView({ canAct = true }: { canAct?: boolean }) {
  const [rows, setRows] = useState<ReviewQueueRow[] | null>(null);
  const [actions, setActions] = useState<ReviewActionDoc[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<ReviewPostingDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
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
      setErr(e?.message ?? "Could not load the review queue");
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
    try {
      setDetail(await api.get<ReviewPostingDetail>(`/financial-review/postings/${postingID}`));
    } catch (e: any) {
      setErr(e?.message ?? "Could not load that posting");
    }
  }, []);

  async function decide() {
    if (!selected || !detail) return;
    setBusy(true);
    setErr(null);
    try {
      await api.post(`/financial-review/postings/${selected}/actions`, {
        action,
        reason,
        expected_version: detail.review.version,
        evidence: evSource ? { source_type: evSource, reference: evRef } : undefined,
        password,
      });
      setNote("Decision recorded.");
      await loadQueue();
      await open(selected);
    } catch (e: any) {
      setErr(e?.message ?? "Could not record that decision");
    } finally {
      setBusy(false);
    }
  }

  if (!rows) return <p role="status">Loading the review queue…</p>;

  const spec = actions.find((a) => a.action === action);
  const allowed = detail?.available_actions ?? [];

  return (
    <div className="space-y-4">
      {err ? <p role="alert" className="text-sm text-red-700">{err}</p> : null}
      {note ? <p role="status" className="text-sm text-emerald-700">{note}</p> : null}

      <Card>
        <CardBody>
          <h2 className="mb-3 text-sm font-medium text-slate-500">
            Awaiting a decision ({rows.length})
          </h2>
          {rows.length === 0 ? (
            <EmptyState
              title="Nothing is waiting on you"
              hint="Postings appear here when their outcome could not be determined, or when someone escalated them."
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Amount</TH>
                  <TH>State</TH>
                  <TH>Last attempt</TH>
                  <TH>PMS answer</TH>
                  <TH> </TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((r) => (
                  <TR key={r.posting_id}>
                    <TD>{money(r.amount_minor, r.currency, r.currency_exponent)}</TD>
                    <TD>
                      <Badge tone={r.awaiting_manual_review ? "err" : "warn"}>{r.execution_state}</Badge>
                    </TD>
                    <TD>{r.latest_attempt_no ?? "—"}</TD>
                    <TD>{r.latest_pa_as_status ?? "no answer"}</TD>
                    <TD>
                      <Button onClick={() => void open(r.posting_id)}>Review</Button>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {detail ? (
        <>
          <Card>
            <CardBody>
              <h3 className="mb-2 text-sm font-medium text-slate-500">What this charge was attached to</h3>
              <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-sm sm:grid-cols-3">
                <div>
                  <dt className="text-slate-500">Amount</dt>
                  <dd>{money(detail.posting.amount_minor, detail.posting.currency, detail.posting.currency_exponent)}</dd>
                </div>
                <div>
                  <dt className="text-slate-500">Settlement</dt>
                  <dd>{detail.pinned_evidence.settlement_status}</dd>
                </div>
                <div>
                  <dt className="text-slate-500">Purchase</dt>
                  <dd>{detail.pinned_evidence.purchase_state}</dd>
                </div>
                <div>
                  <dt className="text-slate-500">Interface</dt>
                  <dd>
                    {detail.pinned_evidence.connector_kind} ({detail.pinned_evidence.interface_lifecycle_state})
                  </dd>
                </div>
                <div>
                  <dt className="text-slate-500">Folio identity</dt>
                  <dd>{detail.pinned_evidence.folio_identity_strategy}</dd>
                </div>
                <div>
                  <dt className="text-slate-500">Interface freshness</dt>
                  <dd>{detail.diagnostics.interface_freshness_block ?? "OK"}</dd>
                </div>
              </dl>
            </CardBody>
          </Card>

          <Card>
            <CardBody>
              <h3 className="mb-3 text-sm font-medium text-slate-500">
                Attempts ({detail.diagnostics.attempt_count}, of which UNKNOWN:{" "}
                {detail.diagnostics.unknown_attempt_count})
              </h3>
              {detail.attempts.length === 0 ? (
                <p className="text-sm text-slate-600">This posting has never been transmitted.</p>
              ) : (
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
                  <tbody>
                    {detail.attempts.map((a) => (
                      <TR key={a.attempt_no}>
                        <TD>{a.attempt_no}</TD>
                        <TD>{a.p_number}</TD>
                        <TD>
                          {a.rn}
                          {a.g_number ? ` / ${a.g_number}` : ""}
                        </TD>
                        <TD>
                          <Badge tone={OUTCOME_TONE(a.outcome)}>{a.outcome}</Badge>
                        </TD>
                        <TD>{a.pa_as_status ?? "—"}</TD>
                        <TD>{a.sent_at}</TD>
                      </TR>
                    ))}
                  </tbody>
                </Table>
              )}
              {detail.diagnostics.has_unknown_history ? (
                <p className="mt-3 text-sm text-amber-800">
                  An attempt ended UNKNOWN. Nobody knows whether the folio was charged, and nothing has been
                  retried automatically — that is what this decision is for.
                </p>
              ) : null}
            </CardBody>
          </Card>

          {detail.review.history.length > 0 ? (
            <Card>
              <CardBody>
                <h3 className="mb-3 text-sm font-medium text-slate-500">Decisions already recorded</h3>
                <ul className="space-y-2 text-sm">
                  {detail.review.history.map((h, i) => (
                    <li key={i} className="rounded-md border border-slate-200 p-2">
                      <span className="font-medium">{h.action}</span> · {h.created_at}
                      <p className="mt-1 text-slate-700">{h.reason}</p>
                    </li>
                  ))}
                </ul>
              </CardBody>
            </Card>
          ) : null}

          <Card>
            <CardBody>
              <h3 className="mb-3 text-sm font-medium text-slate-500">Record a decision</h3>
              {allowed.length === 0 ? (
                <p className="text-sm text-slate-600">
                  This posting has a terminal decision already. Nothing further can be recorded against it.
                </p>
              ) : (
                <div className="space-y-3">
                  <div>
                    <label className="block text-sm font-medium" htmlFor="review-action">
                      What did you establish?
                    </label>
                    <select
                      id="review-action"
                      className="mt-1 w-full max-w-xl rounded-md border border-slate-300 px-2 py-1"
                      value={action}
                      onChange={(e) => setAction(e.target.value)}
                    >
                      <option value="">Choose…</option>
                      {actions
                        .filter((a) => allowed.includes(a.action))
                        .map((a) => (
                          <option key={a.action} value={a.action}>
                            {a.action} — {a.summary}
                          </option>
                        ))}
                    </select>
                    {spec?.terminal ? (
                      <p className="mt-1 text-xs text-amber-800">
                        This is a terminal decision. It can be recorded once and never revised.
                      </p>
                    ) : null}
                  </div>

                  <div>
                    <label className="block text-sm font-medium" htmlFor="review-reason">
                      Why
                    </label>
                    <textarea
                      id="review-reason"
                      rows={2}
                      className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2"
                      value={reason}
                      onChange={(e) => setReason(e.target.value)}
                    />
                  </div>

                  {spec?.needs_evidence ? (
                    <div className="grid gap-3 sm:grid-cols-2">
                      <div>
                        <label className="block text-sm font-medium" htmlFor="review-ev-source">
                          Evidence source
                        </label>
                        <select
                          id="review-ev-source"
                          className="mt-1 w-full rounded-md border border-slate-300 px-2 py-1"
                          value={evSource}
                          onChange={(e) => setEvSource(e.target.value)}
                        >
                          <option value="">Choose…</option>
                          {(detail.evidence_contract?.source_types ?? []).map((s) => (
                            <option key={s} value={s}>
                              {s}
                            </option>
                          ))}
                        </select>
                      </div>
                      <div>
                        <label className="block text-sm font-medium" htmlFor="review-ev-ref">
                          Reference to it
                        </label>
                        <input
                          id="review-ev-ref"
                          className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2"
                          placeholder="e.g. PMS folio screen, 14:22"
                          value={evRef}
                          onChange={(e) => setEvRef(e.target.value)}
                        />
                        <p className="mt-1 text-xs text-slate-500">
                          Record a REFERENCE to the artefact, never its contents. This goes into an immutable
                          audit record.
                        </p>
                      </div>
                    </div>
                  ) : null}

                  <div>
                    <label className="block text-sm font-medium" htmlFor="review-password">
                      Your password
                    </label>
                    <input
                      id="review-password"
                      type="password"
                      autoComplete="current-password"
                      className="mt-1 w-full max-w-sm rounded-md border border-slate-300 px-3 py-2"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  </div>

                  <Button disabled={!canAct || busy || !action} onClick={() => void decide()}>
                    {busy ? "Recording…" : "Record decision"}
                  </Button>

                  <ul className="mt-2 space-y-1 text-xs text-slate-500">
                    {(detail.limitations ?? []).map((l, i) => (
                      <li key={i}>{l}</li>
                    ))}
                  </ul>
                </div>
              )}
            </CardBody>
          </Card>
        </>
      ) : null}
    </div>
  );
}
