"use client";

// Phase 4 (DARK) — Financial recovery.
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
// Manual Review decision does: this is an assertion about real money and it should carry a name.

import { useCallback, useEffect, useState } from "react";
import {
  api,
  RECOVERY_RESOLUTIONS,
  RecoveryHold,
  RecoveryResolution,
  RecoveryStatus,
  ZeroAttemptQueue,
  ZeroAttemptRow,
} from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";

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

function money(minor: number | null, currency: string, exponent = 2): string {
  if (minor === null) return "—";
  return `${(minor / Math.pow(10, exponent)).toFixed(exponent)} ${currency}`;
}

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
      setErr(e?.message ?? "Could not load recovery state");
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

  if (!status) return <p role="status">Loading recovery state…</p>;

  if (!status.Active) {
    return (
      <Card>
        <CardBody>
          <div className="flex items-center gap-3">
            <Badge tone="ok">NOT IN RECOVERY</Badge>
            <p className="text-sm text-slate-600">
              Financial execution is running normally. Epoch {status.Epoch}.
            </p>
          </div>
        </CardBody>
      </Card>
    );
  }

  const open = holds ?? [];

  return (
    <div className="space-y-4">
      {err ? <p role="alert" className="text-sm text-red-700">{err}</p> : null}
      {note ? <p role="status" className="text-sm text-emerald-700">{note}</p> : null}

      <Card>
        <CardBody>
          <div className="flex items-start gap-3">
            <Badge tone="err">FINANCIAL RECOVERY</Badge>
            <div className="text-sm text-slate-700">
              <p>
                Money movement is held for this site. Nothing has been replayed and nothing will be: after a
                restore, a correct retry is how a guest gets charged twice.
              </p>
              <p className="mt-2">
                Epoch {status.Epoch} · {status.Reason.replace(/_/g, " ").toLowerCase()} ·{" "}
                <strong>{status.HeldOpen}</strong> of {status.HeldTotal} items still to reconcile.
              </p>
              <p className="mt-2 text-slate-600">Guest internet access is unaffected and continues to work.</p>
            </div>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardBody>
          <label className="block text-sm font-medium" htmlFor="recovery-password">
            Your password
          </label>
          <input
            id="recovery-password"
            type="password"
            autoComplete="current-password"
            className="mt-1 w-full max-w-sm rounded-md border border-slate-300 px-3 py-2"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            aria-describedby="recovery-password-help"
          />
          <p id="recovery-password-help" className="mt-1 text-xs text-slate-500">
            Every reconciliation decision is an audited statement about real money, so each one is confirmed
            with your password.
          </p>
        </CardBody>
      </Card>

      {(zero?.queue.length ?? 0) > 0 ? (
        <Card>
          <CardBody>
            <h3 className="mb-1 text-sm font-medium text-slate-500">
              Never transmitted ({zero!.queue.length})
            </h3>
            <p className="mb-3 max-w-3xl text-sm text-slate-700">
              These charges were held before anything was sent to the PMS, so there is no attempt to review
              and they do not appear on the Manual Review screen. {zero!.note}
            </p>
            <Table>
              <THead>
                <TR>
                  <TH>Posting</TH>
                  <TH>Amount</TH>
                  <TH>What was established</TH>
                  <TH>Authorize one attempt</TH>
                  <TH> </TH>
                </TR>
              </THead>
              <tbody>
                {zero!.queue.map((z) => (
                  <TR key={z.posting_id}>
                    <TD>
                      <code className="text-xs">{z.posting_id.slice(0, 8)}</code>
                    </TD>
                    <TD>{money(z.amount_minor, z.currency, z.currency_exponent)}</TD>
                    <TD>
                      {z.retry_authorized_attempt_no !== null ? (
                        <Badge tone="ok">ATTEMPT {z.retry_authorized_attempt_no} AUTHORIZED</Badge>
                      ) : z.hold_resolution ? (
                        <Badge tone={z.eligible_for_retry_authorization ? "warn" : "default"}>
                          {z.hold_resolution.replace(/_/g, " ")}
                        </Badge>
                      ) : (
                        <Badge tone="default">NOT YET RECONCILED</Badge>
                      )}
                    </TD>
                    <TD>
                      {z.eligible_for_retry_authorization ? (
                        <div className="space-y-2">
                          <div>
                            <label className="sr-only" htmlFor={`za-reason-${z.posting_id}`}>
                              Why this charge must still go out
                            </label>
                            <textarea
                              id={`za-reason-${z.posting_id}`}
                              rows={2}
                              className="w-72 rounded-md border border-slate-300 px-2 py-1"
                              placeholder="Why this charge must still go out"
                              value={zaReason[z.posting_id] ?? ""}
                              onChange={(e) =>
                                setZaReason({ ...zaReason, [z.posting_id]: e.target.value })
                              }
                            />
                          </div>
                          <div className="flex gap-2">
                            <div>
                              <label className="sr-only" htmlFor={`za-source-${z.posting_id}`}>
                                Evidence source for this posting
                              </label>
                              <select
                                id={`za-source-${z.posting_id}`}
                                className="rounded-md border border-slate-300 px-2 py-1"
                                value={zaSource[z.posting_id] ?? ""}
                                onChange={(e) =>
                                  setZaSource({ ...zaSource, [z.posting_id]: e.target.value })
                                }
                              >
                                <option value="">Choose…</option>
                                {(zero!.evidence_contract?.source_types ?? []).map((t) => (
                                  <option key={t} value={t}>
                                    {t}
                                  </option>
                                ))}
                              </select>
                            </div>
                            <div>
                              <label className="sr-only" htmlFor={`za-ref-${z.posting_id}`}>
                                Reference to that evidence
                              </label>
                              <input
                                id={`za-ref-${z.posting_id}`}
                                className="w-40 rounded-md border border-slate-300 px-2 py-1"
                                placeholder="e.g. folio 4471"
                                value={zaRef[z.posting_id] ?? ""}
                                onChange={(e) => setZaRef({ ...zaRef, [z.posting_id]: e.target.value })}
                              />
                            </div>
                          </div>
                        </div>
                      ) : (
                        <p className="max-w-xs text-xs text-slate-500">
                          {z.retry_authorized_attempt_no !== null
                            ? "An attempt has already been authorized for this posting. Exactly one is allowed."
                            : "Reconcile this item above as “It never completed” first — the authorization rests on that finding."}
                        </p>
                      )}
                    </TD>
                    <TD>
                      {z.eligible_for_retry_authorization ? (
                        <Button
                          disabled={!canAct || busy === z.posting_id}
                          onClick={() => void authorizeZeroAttempt(z)}
                        >
                          {busy === z.posting_id ? "Authorizing…" : "Authorize one attempt"}
                        </Button>
                      ) : null}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
            <p className="mt-3 text-xs text-slate-500">
              {zero!.eligibility} Authorizing sends nothing now, and it can be done once per posting.
            </p>
          </CardBody>
        </Card>
      ) : null}

      {open.length === 0 ? (
        <Card>
          <CardBody>
            <EmptyState
              title="Everything has been reconciled"
              hint="No held items remain. Releasing recovery resumes money movement for this site."
            />
            <div>
              <label className="mt-3 block text-sm font-medium" htmlFor="release-note">
                Why is it safe to resume?
              </label>
              <textarea
                id="release-note"
                className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2"
                rows={3}
                value={evidence["__release"] ?? ""}
                onChange={(e) => setEvidence({ ...evidence, __release: e.target.value })}
              />
              <Button
                className="mt-3"
                disabled={!canAct || busy === "release"}
                onClick={() => void release()}
              >
                {busy === "release" ? "Releasing…" : "Release financial recovery"}
              </Button>
            </div>
          </CardBody>
        </Card>
      ) : (
        <Card>
          <CardBody>
            <h3 className="mb-3 text-sm font-medium text-slate-500">Held work ({open.length})</h3>
            <Table>
              <THead>
                <TR>
                  <TH>What</TH>
                  <TH>State when held</TH>
                  <TH>Amount</TH>
                  <TH>What did you establish?</TH>
                  <TH>How did you establish it?</TH>
                  <TH> </TH>
                </TR>
              </THead>
              <tbody>
                {open.map((h) => (
                  <TR key={h.hold_id}>
                    <TD>{KIND_TEXT[h.work_kind]}</TD>
                    <TD>
                      <Badge tone="warn">{h.held_status}</Badge>
                    </TD>
                    <TD>{money(h.amount_minor, h.currency)}</TD>
                    <TD>
                      <label className="sr-only" htmlFor={`res-${h.hold_id}`}>
                        Conclusion for this {KIND_TEXT[h.work_kind].toLowerCase()}
                      </label>
                      <select
                        id={`res-${h.hold_id}`}
                        className="rounded-md border border-slate-300 px-2 py-1"
                        value={choice[h.hold_id] ?? ""}
                        onChange={(e) =>
                          setChoice({ ...choice, [h.hold_id]: e.target.value as RecoveryResolution })
                        }
                      >
                        <option value="">Choose…</option>
                        {RECOVERY_RESOLUTIONS.map((r) => (
                          <option key={r} value={r}>
                            {RESOLUTION_TEXT[r]}
                          </option>
                        ))}
                      </select>
                    </TD>
                    <TD>
                      <label className="sr-only" htmlFor={`note-${h.hold_id}`}>
                        Evidence for this decision
                      </label>
                      <input
                        id={`note-${h.hold_id}`}
                        className="w-64 rounded-md border border-slate-300 px-2 py-1"
                        placeholder="e.g. provider dashboard shows no charge"
                        value={evidence[h.hold_id] ?? ""}
                        onChange={(e) => setEvidence({ ...evidence, [h.hold_id]: e.target.value })}
                      />
                    </TD>
                    <TD>
                      <Button disabled={!canAct || busy === h.hold_id} onClick={() => void resolve(h)}>
                        {busy === h.hold_id ? "Recording…" : "Record"}
                      </Button>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
            <p className="mt-3 text-xs text-slate-500">
              Recording a decision never re-sends anything. Recovery can only be released once every item
              above has been reconciled.
            </p>
          </CardBody>
        </Card>
      )}
    </div>
  );
}
