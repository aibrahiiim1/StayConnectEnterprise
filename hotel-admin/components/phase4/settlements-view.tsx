"use client";

// PACKAGE PAYMENTS (formerly "Settlements").
//
// The question this screen answers is the one a front-office manager actually asks: "who got which package,
// when, how was it paid for, and does anything need my attention?" Every Internet package a client receives
// creates one payment record here -- a free package, a voucher, a card payment or a room charge -- and this
// screen says, in words, whether that payment is complete.
//
// The backend codes (NOT_REQUIRED, PREPAID, ONLINE_PAYMENT, PMS_POSTING; REQUIRED, IN_PROGRESS, SETTLED,
// MANUAL_REVIEW, FAILED, REVERSED ...) are never shown as the answer; they are turned into one sentence per row.
//
// WHAT IT DOES NOT OFFER, deliberately. There is no refund button. The backend can record a refund raised by the
// provider, but no operator-initiated refund flow is authorized, so a button here would imply money can be moved
// from this screen. The detail renders its affordances from the API's own available_actions list. A payment that
// needs a decision is decided on Manual review, and the detail links there.

import { useState } from "react";
import Link from "next/link";
import { AlertTriangle, ArrowUpRight, BedDouble, CreditCard, Gift, Receipt, Ticket } from "lucide-react";
import { api, FinancialPayment, FinancialSettlement, surfaceUnavailableMessage } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { FilterChips, KeyValueGrid, Pagination } from "@/components/ui/data";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { humanize, money } from "./format";

type Tone = "ok" | "warn" | "err" | "info" | "default";

/** How the package was paid for, in words. */
export const PAID_BY: Record<string, string> = {
  NOT_REQUIRED: "Free",
  PREPAID: "Voucher",
  ONLINE_PAYMENT: "Card payment",
  PMS_POSTING: "Room charge",
  MANUAL_APPROVAL: "Manual approval",
};

/** Where the client's access came from. */
function sourceWords(r: FinancialSettlement): string {
  switch (r.source) {
    case "ROOM": return r.room ? `Room ${r.room}` : "Room sign-in";
    case "VOUCHER": return "Voucher";
    case "ACCOUNT": return "Client account";
    case "OPEN": return "Chose a package without signing in";
    default: return "—";
  }
}

/** One plain-language state per payment: what an operator needs to know, and its tone. */
export function paymentState(method: string, status: string): { label: string; tone: Tone; explain: string } {
  if (method === "NOT_REQUIRED" || status === "NOT_REQUIRED") {
    return { label: "Free — nothing to pay", tone: "default", explain: "The package is free, so there was nothing to collect." };
  }
  switch (status) {
    case "SETTLED":
      return method === "PREPAID"
        ? { label: "Paid by voucher", tone: "ok", explain: "A voucher paid for this package. The voucher is now used up." }
        : method === "PMS_POSTING"
          ? { label: "Charged to the room", tone: "ok", explain: "The PMS confirmed the charge on the guest's reservation." }
          : { label: "Paid", tone: "ok", explain: "The payment provider confirmed the payment." };
    case "REQUIRED":
      return { label: "Waiting for payment", tone: "info", explain: "The client chose the package but has not paid yet. No access is given until they do." };
    case "IN_PROGRESS":
      return method === "PMS_POSTING"
        ? { label: "Being sent to the PMS", tone: "info", explain: "The room charge is on its way to the PMS. Access is given once the PMS confirms it." }
        : { label: "Payment in progress", tone: "info", explain: "The client is paying on the provider's page. Access is given once the provider confirms it." };
    case "MANUAL_REVIEW":
      return { label: "Needs review", tone: "err", explain: "It is not certain whether the client was charged. A person must check and decide on Manual review." };
    case "FAILED":
      return { label: "Not paid", tone: "err", explain: "The payment did not go through. The client was not charged and received no access from it." };
    case "PARTIALLY_REVERSED":
      return { label: "Partly refunded", tone: "warn", explain: "Part of the payment was given back by the provider." };
    case "REVERSED":
      return { label: "Refunded", tone: "warn", explain: "The payment was given back by the provider." };
    default:
      return { label: humanize(status), tone: "default", explain: "" };
  }
}

const ACCESS_WORDS: Record<string, string> = {
  GRANTED: "Access given",
  AWAITING_SETTLEMENT: "Waiting for payment",
  PENDING: "Waiting",
  FAILED: "No access given",
  CANCELLED: "Cancelled",
};
// The views are applied by edged (?view=), with the definitions this screen used when it filtered in the
// browser: attention = waiting, in progress, failed or to review (and not free); paid = card or room charge;
// voucher = prepaid; free = nothing to pay.
type View = "" | "attention" | "paid" | "voucher" | "free";

type SettlementsResp = PagedFields & {
  settlements?: FinancialSettlement[];
  /** Counts and collected totals over every payment -- not the page, and not narrowed by the view. */
  summary?: {
    all?: number; attention?: number; paid?: number; voucher?: number; free?: number;
    collected?: { currency: string; currency_exponent: number; amount_minor: number }[];
  };
};

type Detail = {
  settlement: FinancialSettlement;
  payments: FinancialPayment[];
  available_actions: string[];
  note: string;
};

export function SettlementsView() {
  const [view, setView] = useState<View>("");
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [detailErr, setDetailErr] = useState<string | null>(null);

  // PAGED ON THE SERVER. The list used to stop at the newest 200 payments, and its counts and the collected
  // total counted those 200. A view change starts again at the first page.
  const list = useServerPage<SettlementsResp>({
    path: view ? `/financial-ops/settlements?view=${view}` : "/financial-ops/settlements",
    rowsOf: (r) => r.settlements,
  });
  const { resp, current } = list;
  const err = list.err ? surfaceUnavailableMessage(list.err as any, "Package payments") : null;
  const rows = current && resp ? resp.settlements ?? [] : null;

  async function open(id: string) {
    setSelected(id);
    setDetail(null);
    setDetailErr(null);
    try {
      setDetail(await api.get(`/financial-ops/settlements/${id}`));
    } catch (e: any) {
      setDetailErr(e?.message ?? "Could not load that payment");
    }
  }

  const sum = resp?.summary;
  const counts = {
    attention: sum?.attention ?? 0,
    paid: sum?.paid ?? 0,
    voucher: sum?.voucher ?? 0,
    free: sum?.free ?? 0,
    all: sum?.all ?? 0,
  };
  const collected = (sum?.collected ?? [])
    .map((c) => money(c.amount_minor, c.currency, c.currency_exponent)).join(" · ") || "0.00";
  const haveSummary = !!sum;
  const visible = rows ?? [];
  const s = detail?.settlement;
  const state = s ? paymentState(s.method, s.status) : null;

  return (
    <PageShell>
      <PageHeader
        icon={<Receipt />}
        eyebrow="Hotel"
        title="Package payments"
        description="Every Internet package a client received, how it was paid for, and whether anything needs your attention."
        help={
          <>
            <HelpSection title="What this page shows">
              <p>
                Each time a client gets an Internet package, one line appears here. It says which package, when, where
                the client came from (a room, a voucher, a client account) and how it was paid for: free, voucher, card
                payment or room charge.
              </p>
            </HelpSection>
            <HelpSection title="What you may need to do">
              <HelpList
                items={[
                  <><strong>Needs review</strong> — it is not certain whether the client was charged. Decide it on Manual review.</>,
                  <><strong>Not paid</strong> — the payment failed; the client was not charged and got no access from it. Nothing to do unless the client asks.</>,
                  <>Everything else is for information. Free and voucher lines never need action.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Refunds">
              <p>There is no refund button. A refund made by the card provider is recorded here automatically.</p>
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} className="mb-0" />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="Needs your attention"
          value={haveSummary ? counts.attention.toLocaleString() : "—"}
          icon={<AlertTriangle />}
          tone={counts.attention > 0 ? "err" : "ok"}
          hint={counts.attention > 0 ? "Payments waiting, in progress, failed or to review" : "Nothing to do"}
        />
        <StatCard
          label="Collected (card and room charge)"
          value={haveSummary ? collected : "—"}
          icon={<CreditCard />}
          hint={`${counts.paid.toLocaleString()} paid package${counts.paid === 1 ? "" : "s"}`}
        />
        <StatCard label="Vouchers used" value={haveSummary ? counts.voucher.toLocaleString() : "—"} icon={<Ticket />} />
        <StatCard label="Free packages" value={haveSummary ? counts.free.toLocaleString() : "—"} icon={<Gift />} />
      </div>

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
          <FilterChips<View>
            label="Show"
            value={view}
            onChange={setView}
            options={[
              { value: "", label: "All", count: counts.all },
              { value: "attention", label: "Needs attention", count: counts.attention, tone: counts.attention ? "err" : undefined },
              { value: "paid", label: "Card and room charge", count: counts.paid },
              { value: "voucher", label: "Voucher", count: counts.voucher },
              { value: "free", label: "Free", count: counts.free },
            ]}
          />
          {rows && <p className="text-xs text-muted-foreground tabular">Newest first</p>}
        </div>
        <CardBody className="p-0">
          {!rows ? (
            err ? null : <SkeletonRows rows={4} cols={6} />
          ) : visible.length === 0 ? (
            <EmptyState
              icon={<Receipt />}
              title={counts.all === 0 ? "No package has been given yet" : "Nothing here"}
              hint={counts.all === 0
                ? "A line appears here each time a client receives an Internet package."
                : view === "attention" ? "No payment needs your attention." : "Choose another view, or show all."}
              action={view ? <Button variant="secondary" size="sm" onClick={() => setView("")}>Show all</Button> : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>Package</TH>
                  <TH className="hidden md:table-cell">Client</TH>
                  <TH className="hidden sm:table-cell">Paid by</TH>
                  <TH className="text-right">Amount</TH>
                  <TH>Payment</TH>
                  <TH><span className="sr-only">Details</span></TH>
                </TR>
              </THead>
              <TBody>
                {visible.map((r) => {
                  const st = paymentState(r.method, r.status);
                  return (
                    <TR key={r.settlement_id}>
                      <TD className="whitespace-nowrap text-xs text-muted-foreground">{r.at ? formatDate(r.at) : "—"}</TD>
                      <TD className="font-medium">{r.package_name || "—"}</TD>
                      <TD className="hidden md:table-cell">
                        <span className="inline-flex items-center gap-1.5">
                          {r.source === "ROOM" && <BedDouble className="size-3.5 text-muted-foreground" aria-hidden />}
                          {sourceWords(r)}
                        </span>
                      </TD>
                      <TD className="hidden sm:table-cell">{PAID_BY[r.method] ?? humanize(r.method)}</TD>
                      <TD className="whitespace-nowrap text-right tabular">
                        {r.amount_minor === 0 ? "—" : money(r.amount_minor, r.currency, r.currency_exponent)}
                      </TD>
                      <TD><Badge tone={st.tone} dot>{st.label}</Badge></TD>
                      <TD className="text-right">
                        <Button size="sm" variant="ghost" onClick={() => void open(r.settlement_id)}
                          aria-label={`Details of ${r.package_name || "this payment"}`}>
                          Details
                        </Button>
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
        </CardBody>
        {rows && rows.length > 0 && (list.offset > 0 || list.hasMore) && (
          <CardBody className="border-t border-border py-3">
            <Pagination
              offset={list.offset}
              limit={list.pageSize}
              shown={rows.length}
              total={list.total}
              hasMore={list.hasMore}
              onChange={list.setOffset}
            />
          </CardBody>
        )}
      </Card>

      <Sheet open={selected !== null} onOpenChange={(v) => { if (!v) { setSelected(null); setDetail(null); } }}>
        <SheetContent width="md">
          <SheetHeader
            icon={<Receipt />}
            eyebrow="Package payment"
            title={s ? (s.package_name || "Package") : "Package payment"}
            description={s ? `${PAID_BY[s.method] ?? humanize(s.method)}${s.at ? ` · ${formatDate(s.at)}` : ""}` : undefined}
            badges={state ? <Badge tone={state.tone} dot>{state.label}</Badge> : undefined}
          />
          <SheetBody>
            <ErrorBanner err={detailErr} className="mb-0" />
            {!detail || !s || !state ? (
              detailErr ? null : (
                <div className="space-y-3" aria-busy="true">
                  <span className="sr-only">Loading the payment</span>
                  <Skeleton className="h-20 w-full" />
                  <Skeleton className="h-28 w-full" />
                </div>
              )
            ) : (
              <>
                <SheetSection title="What happened">
                  <p className="text-sm">{state.explain}</p>
                  {s.status === "MANUAL_REVIEW" && (
                    <Link href="/financial-review" className="mt-2 inline-flex items-center gap-1 text-sm text-primary underline-offset-4 hover:underline">
                      Decide it on Manual review <ArrowUpRight className="size-3.5" aria-hidden />
                    </Link>
                  )}
                </SheetSection>
                <SheetSection title="Details">
                  <KeyValueGrid
                    items={[
                      { label: "Package", value: s.package_name || "—" },
                      { label: "Client", value: sourceWords(s) },
                      { label: "Paid by", value: PAID_BY[s.method] ?? humanize(s.method) },
                      { label: "Amount", value: s.method === "NOT_REQUIRED" ? "Free" : money(s.amount_minor, s.currency, s.currency_exponent) },
                      { label: "Access", value: ACCESS_WORDS[s.purchase_state] ?? humanize(s.purchase_state) },
                      { label: "When", value: s.at ? formatDate(s.at) : "—" },
                    ]}
                  />
                </SheetSection>
                {(s.method === "ONLINE_PAYMENT" || detail.payments.length > 0) && (
                  <SheetSection title="Card payment history">
                    {detail.payments.length === 0 ? (
                      <p className="text-sm text-muted-foreground">No card payment has been started for this package.</p>
                    ) : (
                      <div className="overflow-hidden rounded-md border border-border">
                        <Table>
                          <THead>
                            <TR><TH>Step</TH><TH>Amount</TH><TH>Result</TH></TR>
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
                              </TR>
                            ))}
                          </TBody>
                        </Table>
                      </div>
                    )}
                  </SheetSection>
                )}
                {detail.available_actions.length === 0 && (
                  <p className="text-xs text-muted-foreground">
                    Nothing can be changed from here. Refunds are not made from OneGate; a refund made by the card
                    provider appears here automatically.
                  </p>
                )}
              </>
            )}
          </SheetBody>
          <SheetFooter>
            <Button variant="ghost" onClick={() => { setSelected(null); setDetail(null); }}>Close</Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </PageShell>
  );
}
