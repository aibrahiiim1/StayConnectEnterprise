"use client";

// Phase 4 (DARK) — Settlement browser and detail.
//
// The question this screen answers is "was this guest actually charged, and what has been given back". So
// the list is filterable by status and the detail shows the charge together with every refund or chargeback
// that followed it.
//
// WHAT IT DOES NOT OFFER, deliberately. There is no refund button. The backend can record a refund and its
// tests exercise one, but no provider adapter exists and no operator-initiated refund flow is authorized --
// so a button here would imply a capability that is not there, and an operator who pressed it would be
// entitled to believe money had moved. The screen renders its affordances from the API's own
// available_actions list rather than deciding for itself, which is what keeps this true as the backend
// changes.

import { useCallback, useEffect, useState } from "react";
import { api, FinancialPayment, FinancialSettlement, surfaceUnavailableMessage } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";

const STATUS_TONE = (s: string) =>
  s === "SETTLED"
    ? "ok"
    : s === "FAILED" || s === "MANUAL_REVIEW"
      ? "err"
      : s === "REVERSED" || s === "PARTIALLY_REVERSED"
        ? "warn"
        : "info";

const STATUSES = [
  "REQUIRED",
  "IN_PROGRESS",
  "SETTLED",
  "MANUAL_REVIEW",
  "FAILED",
  "PARTIALLY_REVERSED",
  "REVERSED",
];

function money(minor: number, currency: string, exponent = 2): string {
  if (!currency) return String(minor);
  return `${(minor / Math.pow(10, exponent)).toFixed(exponent)} ${currency}`;
}

export function SettlementsView() {
  const [rows, setRows] = useState<FinancialSettlement[] | null>(null);
  const [status, setStatus] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<{
    settlement: FinancialSettlement;
    payments: FinancialPayment[];
    available_actions: string[];
    note: string;
  } | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async (st: string) => {
    try {
      const qs = st ? `?status=${encodeURIComponent(st)}` : "";
      const r = await api.get<{ settlements: FinancialSettlement[] }>(`/financial-ops/settlements${qs}`);
      setRows(r.settlements ?? []);
      setErr(null);
    } catch (e: any) {
      setErr(surfaceUnavailableMessage(e, "Settlements"));
    }
  }, []);

  useEffect(() => {
    void load(status);
  }, [load, status]);

  async function open(id: string) {
    setSelected(id);
    setDetail(null);
    try {
      setDetail(await api.get(`/financial-ops/settlements/${id}`));
    } catch (e: any) {
      setErr(e?.message ?? "Could not load that settlement");
    }
  }

  // The error is rendered BEFORE the loading guard. When the load fails the state variable is never set,
  // so a guard placed first returns "Loading…" forever and the alert further down is unreachable -- the
  // screen tells the operator it is still working when it has already given up.
  if (err) return <p role="alert" className="text-sm text-red-700">{err}</p>;
  if (!rows) return <p role="status">Loading settlements…</p>;

  return (
    <div className="space-y-4">
      {err ? <p role="alert" className="text-sm text-red-700">{err}</p> : null}

      <Card>
        <CardBody>
          <div className="mb-3 flex items-end gap-3">
            <div>
              <label className="block text-sm font-medium" htmlFor="settlement-status">
                Status
              </label>
              <select
                id="settlement-status"
                className="mt-1 rounded-md border border-slate-300 px-2 py-1"
                value={status}
                onChange={(e) => setStatus(e.target.value)}
              >
                <option value="">All</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {s.replace(/_/g, " ")}
                  </option>
                ))}
              </select>
            </div>
            <p className="pb-1 text-sm text-slate-500">{rows.length} shown (newest 200)</p>
          </div>

          {rows.length === 0 ? (
            <EmptyState title="No settlements match" hint="Try a different status, or clear the filter." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Amount</TH>
                  <TH>Method</TH>
                  <TH>Settlement</TH>
                  <TH>Purchase</TH>
                  <TH> </TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((r) => (
                  <TR key={r.settlement_id}>
                    <TD>{money(r.amount_minor, r.currency, r.currency_exponent)}</TD>
                    <TD>{r.method.replace(/_/g, " ")}</TD>
                    <TD>
                      <Badge tone={STATUS_TONE(r.status)}>{r.status.replace(/_/g, " ")}</Badge>
                    </TD>
                    <TD>{r.purchase_state}</TD>
                    <TD>
                      <Button onClick={() => void open(r.settlement_id)}>Open</Button>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {selected && detail ? (
        <Card>
          <CardBody>
            <h3 className="mb-3 text-sm font-medium text-slate-500">Payment history</h3>
            {detail.payments.length === 0 ? (
              <p className="text-sm text-slate-600">
                No payment has been attempted against this settlement.
              </p>
            ) : (
              <Table>
                <THead>
                  <TR>
                    <TH>Type</TH>
                    <TH>Amount</TH>
                    <TH>Status</TH>
                    <TH>Provider</TH>
                  </TR>
                </THead>
                <tbody>
                  {detail.payments.map((p) => (
                    <TR key={p.payment_id}>
                      <TD>{p.transaction_type}</TD>
                      <TD>{money(p.amount_minor, p.currency, p.currency_exponent)}</TD>
                      <TD>
                        <Badge tone={p.status === "CAPTURED" ? "ok" : p.status === "UNKNOWN" ? "err" : "info"}>
                          {p.status}
                        </Badge>
                      </TD>
                      <TD>{p.provider}</TD>
                    </TR>
                  ))}
                </tbody>
              </Table>
            )}

            {detail.available_actions.length === 0 ? (
              <p className="mt-3 text-sm text-slate-600">{detail.note}</p>
            ) : (
              <p className="mt-3 text-sm text-slate-600">
                Available actions: {detail.available_actions.join(", ")}
              </p>
            )}
          </CardBody>
        </Card>
      ) : null}
    </div>
  );
}
