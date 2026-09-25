"use client";

// Phase 4 (DARK) — Settlements: browser and detail.
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
import { Receipt } from "lucide-react";
import { api, FinancialPayment, FinancialSettlement, surfaceUnavailableMessage } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { FilterChips, KeyValueGrid } from "@/components/ui/data";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { humanize, money } from "./format";

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

type Detail = {
  settlement: FinancialSettlement;
  payments: FinancialPayment[];
  available_actions: string[];
  note: string;
};

export function SettlementsView() {
  const [rows, setRows] = useState<FinancialSettlement[] | null>(null);
  const [status, setStatus] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [detailErr, setDetailErr] = useState<string | null>(null);

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
    setDetailErr(null);
    try {
      setDetail(await api.get(`/financial-ops/settlements/${id}`));
    } catch (e: any) {
      setDetailErr(e?.message ?? "Could not load that settlement");
    }
  }

  const s = detail?.settlement;

  return (
    <PageShell>
      <PageHeader
        icon={<Receipt />}
        eyebrow="Charges"
        title="Settlements"
        description="Whether a guest was actually charged for internet, and what has been given back since."
      />

      <ErrorBanner err={err} className="mb-0" />

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
          <FilterChips
            label="Status"
            value={status}
            onChange={setStatus}
            options={[{ value: "", label: "All" }, ...STATUSES.map((v) => ({ value: v, label: humanize(v) }))]}
          />
          {rows && <p className="text-xs text-muted-foreground tabular">{rows.length} shown (newest 200)</p>}
        </div>
        <CardBody className="p-0">
          {!rows ? (
            err ? null : <SkeletonRows rows={4} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Receipt />}
              title="No settlements match"
              hint="Try a different status, or clear the filter."
              action={status ? <Button variant="secondary" size="sm" onClick={() => setStatus("")}>Show all</Button> : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Amount</TH>
                  <TH className="hidden sm:table-cell">Method</TH>
                  <TH>Settlement</TH>
                  <TH className="hidden md:table-cell">Purchase</TH>
                  <TH>
                    <span className="sr-only">Actions</span>
                  </TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((r) => (
                  <TR key={r.settlement_id}>
                    <TD className="font-medium tabular">{money(r.amount_minor, r.currency, r.currency_exponent)}</TD>
                    <TD className="hidden sm:table-cell">{humanize(r.method)}</TD>
                    <TD>
                      <Badge tone={STATUS_TONE(r.status)} dot>{humanize(r.status)}</Badge>
                    </TD>
                    <TD className="hidden text-muted-foreground md:table-cell">{humanize(r.purchase_state)}</TD>
                    <TD className="text-right">
                      <Button size="sm" variant="secondary" onClick={() => void open(r.settlement_id)}>
                        Open
                      </Button>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Sheet open={selected !== null} onOpenChange={(v) => { if (!v) { setSelected(null); setDetail(null); } }}>
        <SheetContent width="md">
          <SheetHeader
            icon={<Receipt />}
            eyebrow="Settlement"
            title={s ? money(s.amount_minor, s.currency, s.currency_exponent) : "Settlement"}
            description="The charge and everything that followed it."
            badges={s ? <Badge tone={STATUS_TONE(s.status)} dot>{humanize(s.status)}</Badge> : undefined}
          />
          <SheetBody>
            <ErrorBanner err={detailErr} className="mb-0" />
            {!detail ? (
              detailErr ? null : (
                <div className="space-y-3" aria-busy="true">
                  <span className="sr-only">Loading the settlement</span>
                  <Skeleton className="h-20 w-full" />
                  <Skeleton className="h-28 w-full" />
                </div>
              )
            ) : (
              <>
                <SheetSection title="Details">
                  <KeyValueGrid
                    items={[
                      { label: "Amount", value: money(detail.settlement.amount_minor, detail.settlement.currency, detail.settlement.currency_exponent) },
                      { label: "Method", value: humanize(detail.settlement.method) },
                      { label: "Settlement", value: humanize(detail.settlement.status) },
                      { label: "Purchase", value: humanize(detail.settlement.purchase_state) },
                    ]}
                  />
                </SheetSection>
                <SheetSection title="Payment history">
                  {detail.payments.length === 0 ? (
                    <p className="text-sm text-muted-foreground">No payment has been attempted against this settlement.</p>
                  ) : (
                    <div className="overflow-hidden rounded-md border border-border">
                      <Table>
                        <THead>
                          <TR>
                            <TH>Type</TH>
                            <TH>Amount</TH>
                            <TH>Status</TH>
                            <TH>Provider</TH>
                          </TR>
                        </THead>
                        <TBody>
                          {detail.payments.map((p) => (
                            <TR key={p.payment_id}>
                              <TD>{humanize(p.transaction_type)}</TD>
                              <TD className="tabular">{money(p.amount_minor, p.currency, p.currency_exponent)}</TD>
                              <TD>
                                <Badge tone={p.status === "CAPTURED" ? "ok" : p.status === "UNKNOWN" ? "err" : "info"}>
                                  {humanize(p.status)}
                                </Badge>
                              </TD>
                              <TD className="text-muted-foreground">{p.provider}</TD>
                            </TR>
                          ))}
                        </TBody>
                      </Table>
                    </div>
                  )}
                </SheetSection>
                <p className="text-sm text-muted-foreground">
                  {detail.available_actions.length === 0
                    ? detail.note
                    : `Available actions: ${detail.available_actions.join(", ")}`}
                </p>
              </>
            )}
          </SheetBody>
          <SheetFooter>
            <Button variant="ghost" onClick={() => { setSelected(null); setDetail(null); }}>
              Close
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </PageShell>
  );
}
