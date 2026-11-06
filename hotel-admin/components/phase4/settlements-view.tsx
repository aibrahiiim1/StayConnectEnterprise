"use client";

import { usePmsConnections } from "@/lib/pms-scope";
import { useCallback, useMemo, useState } from "react";
import Link from "next/link";
import { AlertTriangle, ArrowUpRight, BedDouble, Receipt, RefreshCw } from "lucide-react";
import { api, ApiError, FinancialPayment, FinancialSettlement } from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";
import { Card, CardFooter } from "@/components/ui/card";
import { FilterBar } from "@/components/ui/toolbar";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { FilterChips, KeyValueGrid, Pagination, SearchInput } from "@/components/ui/data";
import { DateRangeFilter, PERIOD_PRESETS, periodBounds, periodWords, type Period } from "@/components/ui/date-range";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { money } from "./format";
import { currencyExponent } from "@/lib/commerce-form";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

type Tone = "ok" | "warn" | "err" | "info" | "default";

export const PAID_BY: Record<string, string> = {
  NOT_REQUIRED: "Free",
  PREPAID: "Voucher",
  ONLINE_PAYMENT: "Card payment",
  PMS_POSTING: "Room charge",
  MANUAL_APPROVAL: "Manual approval",
};

function paidBy(method: string): string {
  return PAID_BY[method] ?? "Not recognised";
}

function sourceWords(r: FinancialSettlement, multi = false): string {
  switch (r.source) {
    case "ROOM": return r.room ? `Room ${r.room}${multi && r.pms_interface_label ? ` · ${r.pms_interface_label}` : ""}` : "Room sign-in";
    case "VOUCHER": return "Voucher";
    case "ACCOUNT": return "Client account";
    case "OPEN": return "Chose a package without signing in";
    default: return "Not known";
  }
}

export function paymentState(method: string, status: string): { label: string; tone: Tone; explain: string } {
  if (method === "NOT_REQUIRED" || status === "NOT_REQUIRED") {
    return { label: "Nothing to pay", tone: "default", explain: "The package is free, so there was nothing to collect." };
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
      return { label: "State not recognised", tone: "default", explain: "This console does not recognise the state of this payment." };
  }
}

const ACCESS_WORDS: Record<string, string> = {
  GRANTED: "Access given",
  AWAITING_SETTLEMENT: "Waiting for payment",
  MANUAL_REVIEW: "Waiting for a decision",
  PENDING: "Started",
  FAILED: "No access given",
  CANCELLED: "Cancelled",
};

const STEP_WORDS: Record<string, string> = {
  CHARGE: "Payment",
  REFUND: "Refund",
  CHARGEBACK: "Chargeback",
};

function resultWords(p: FinancialPayment): { label: string; tone: "ok" | "err" | "info" | "neutral" } {
  switch (p.status) {
    case "CAPTURED":
      return {
        label: p.transaction_type === "REFUND" ? "Refunded" : p.transaction_type === "CHARGEBACK" ? "Returned to the cardholder" : "Paid",
        tone: "ok",
      };
    case "CREATED": return { label: "Started", tone: "info" };
    case "PENDING": return { label: "Waiting for the provider", tone: "info" };
    case "FAILED": return { label: "Failed", tone: "neutral" };
    case "EXPIRED": return { label: "Expired", tone: "neutral" };
    case "CANCELLED": return { label: "Cancelled", tone: "neutral" };
    case "UNKNOWN": return { label: "Not known whether it went through", tone: "err" };
    default: return { label: "Not recognised", tone: "neutral" };
  }
}

function settledAs(p: FinancialPayment): string | null {
  const r = p.settlement_report;
  if (!r) return null;
  const exp = currencyExponent(r.settlement_currency) ?? 2;
  const parts = [`Settled as ${money(r.settlement_amount_minor, r.settlement_currency, exp)}`];
  if (r.converted) parts[0] += r.exchange_rate ? `, converted at the provider's rate ${r.exchange_rate}` : ", converted by the provider";
  if (typeof r.fee_minor === "number") parts.push(`fee ${money(r.fee_minor, r.settlement_currency, exp)}`);
  if (typeof r.net_minor === "number") parts.push(`net ${money(r.net_minor, r.settlement_currency, exp)}`);
  return parts.join(" · ");
}

function amountWords(r: FinancialSettlement): string {
  if (r.method === "NOT_REQUIRED") return "Free";
  if (r.amount_minor === 0) return "Nothing charged";
  return money(r.amount_minor, r.currency, r.currency_exponent);
}

type View = "" | "attention" | "paid" | "voucher" | "free";

type SettlementsResp = PagedFields & {
  settlements?: FinancialSettlement[];
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
  const { multi } = usePmsConnections();
  const [view, setView] = useState<View>("");
  const [period, setPeriod] = useState<Period>({ preset: "all" });
  const [text, setText] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [detailFailed, setDetailFailed] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);

  const path = useMemo(() => {
    const p = new URLSearchParams();
    if (view) p.set("view", view);
    const b = periodBounds(period);
    if (b) {
      p.set("from", b.from.toISOString());
      p.set("to", b.to.toISOString());
    }
    const qs = p.toString();
    return "/financial-ops/settlements" + (qs ? "?" + qs : "");
  }, [view, period]);
  const needle = text.trim();
  const headers = useMemo(() => (needle ? { "X-Payment-Search": encodeURIComponent(needle) } : undefined), [needle]);
  const narrowed = period.preset !== "all" || !!needle;
  const periodName = period.preset === "custom"
    ? periodWords(period)
    : PERIOD_PRESETS.find((o) => o.value === period.preset)?.label ?? "";
  function clearFilters() {
    setPeriod({ preset: "all" });
    setText("");
    setView("");
  }

  const list = useServerPage<SettlementsResp>({ path, headers, rowsOf: (r) => r.settlements });
  const { resp, current } = list;
  const rows = current && resp ? resp.settlements ?? [] : null;
  const notEnabled = list.err instanceof ApiError && list.err.status === 404;
  const loadFailed = !!list.err && !resp;
  const stale = !!list.err && !!resp;

  const fetchDetail = useCallback(async (id: string) => {
    setDetailLoading(true);
    try {
      setDetail(await api.get<Detail>(`/financial-ops/settlements/${id}`));
      setDetailFailed(false);
    } catch {
      setDetailFailed(true);
    } finally {
      setDetailLoading(false);
    }
  }, []);

  function open(id: string) {
    setSelected(id);
    setDetail(null);
    setDetailFailed(false);
    void fetchDetail(id);
  }

  function close() {
    setSelected(null);
    setDetail(null);
    setDetailFailed(false);
  }

  const sum = resp?.summary;
  const haveSummary = !!sum;
  const counts = {
    attention: sum?.attention ?? 0,
    paid: sum?.paid ?? 0,
    voucher: sum?.voucher ?? 0,
    free: sum?.free ?? 0,
    all: sum?.all ?? 0,
  };
  const collectedList = sum?.collected ?? [];
  const collected = collectedList.length
    ? collectedList.map((c) => money(c.amount_minor, c.currency, c.currency_exponent)).join(" · ")
    : "Nothing yet";

  const selection = [period.preset !== "all" ? periodName : "", needle ? `Matching “${needle}”` : ""]
    .filter(Boolean).join(" · ");

  function tile(value: string) {
    if (haveSummary) return <span className={FADE}>{value}</span>;
    if (list.err) return "Not known";
    return <Skeleton className="h-7 w-16" />;
  }

  const visible = rows ?? [];
  const s = detail?.settlement;
  const state = s ? paymentState(s.method, s.status) : null;
  const spin = (on: boolean) => cn(on && "animate-spin motion-reduce:animate-none");

  return (
    <PageShell>
      <PageHeader
        title="Package payments"
        description="Every Internet package given out, how it was paid for, and what needs your attention."
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

      {stale && (
        <div className={FADE}>
          <Callout tone="warning">The payments could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}

      <StatGrid cols={4}>
        <StatCard
          label="Needs your attention"
          value={tile(counts.attention.toLocaleString())}
          tone={!haveSummary ? "default" : counts.attention > 0 ? "warn" : "ok"}
          hint={
            !haveSummary
              ? list.err ? "Could not be loaded" : undefined
              : counts.attention > 0
                ? "Waiting for payment, in progress, not paid or to review"
                : narrowed ? "Nothing in this selection" : "Nothing to do"
          }
        />
        <StatCard
          label="Collected (card and room charge)"
          value={tile(collected)}
          hint={haveSummary
            ? `From ${counts.paid.toLocaleString()} card and room-charge package${counts.paid === 1 ? "" : "s"}${selection ? ` · ${selection}` : ""}`
            : undefined}
        />
        <StatCard label="Vouchers used" value={tile(counts.voucher.toLocaleString())} hint={haveSummary && selection ? selection : undefined} />
        <StatCard label="Free packages" value={tile(counts.free.toLocaleString())} hint={haveSummary && selection ? selection : undefined} />
      </StatGrid>

      <Card className="overflow-hidden">
        <FilterBar inset
          search={
            <SearchInput value={text} onChange={setText} label="Search package payments"
              placeholder="Package, room, paid by…" delay={300} />
          }
          filters={
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
        } actions={resp ? (
          <div className="flex items-center gap-3">
            <span className="text-xs text-muted-foreground">Newest first</span>
            <Button size="xs" variant="ghost" onClick={() => void list.reload()} disabled={list.loading}>
              <RefreshCw className={spin(list.loading)} /> Refresh
            </Button>
          </div>
        ) : undefined}>
          <DateRangeFilter value={period} onChange={setPeriod} />
        </FilterBar>
        <div>
          {loadFailed ? (
            <div role="alert" className={FADE}>
              <EmptyState
                size="sm"
                icon={<AlertTriangle />}
                title={notEnabled ? "Package payments is not enabled on this appliance" : "Package payments could not be loaded"}
                hint={notEnabled
                  ? "There is nothing to show here. Enabling it is a deployment decision. Nothing has been changed."
                  : "Check the appliance is reachable, then try again. Nothing has been changed."}
                action={
                  <Button size="sm" variant="secondary" onClick={() => void list.reload()} disabled={list.loading}>
                    <RefreshCw className={spin(list.loading)} /> Try again
                  </Button>
                }
              />
            </div>
          ) : !rows ? (
            <SkeletonRows rows={4} cols={6} />
          ) : visible.length === 0 && narrowed ? (
            <EmptyState
              size="sm"
              icon={<Receipt />}
              title="No payment matches"
              hint={view ? "Nothing in this view for the chosen period and search." : "Try another period or search."}
              action={<Button variant="secondary" size="sm" onClick={clearFilters}>Clear filters</Button>}
            />
          ) : visible.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<Receipt />}
              title={counts.all === 0 ? "No package has been given yet" : "Nothing here"}
              hint={counts.all === 0
                ? "A line appears here each time a client receives an Internet package."
                : view === "attention" ? "No payment needs your attention." : "Choose another view, or show all."}
              action={view ? <Button variant="secondary" size="sm" onClick={() => setView("")}>Show all</Button> : undefined}
            />
          ) : (
            <Table label="Package payments" aria-label="Package payments">
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>Package</TH>
                  <TH className="hidden md:table-cell">Client</TH>
                  <TH className="hidden sm:table-cell">Paid by</TH>
                  <TH numeric>Amount</TH>
                  <TH>Payment</TH>
                  <TH pinEnd><span className="sr-only">Details</span></TH>
                </TR>
              </THead>
              <TBody>
                {visible.map((r) => {
                  const st = paymentState(r.method, r.status);
                  return (
                    <TR key={r.settlement_id} onClick={() => open(r.settlement_id)}>
                      <TD nowrap muted className="text-xs">{r.at ? formatDate(r.at) : "Not known"}</TD>
                      <TD className="font-medium">{r.package_name || "Package not named"}</TD>
                      <TD className="hidden md:table-cell">
                        <span className="inline-flex items-center gap-1.5">
                          {r.source === "ROOM" && <BedDouble className="size-3.5 text-muted-foreground" aria-hidden />}
                          {sourceWords(r, multi)}
                        </span>
                      </TD>
                      <TD className="hidden sm:table-cell">{paidBy(r.method)}</TD>
                      <TD numeric>{amountWords(r)}</TD>
                      <TD nowrap><StatusBadge tone={st.tone}>{st.label}</StatusBadge></TD>
                      <TD pinEnd className="w-0 text-end">
                        <Button size="xs" variant="ghost"
                          onClick={(e) => { e.stopPropagation(); open(r.settlement_id); }}
                          onKeyDown={(e) => e.stopPropagation()}
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
        </div>
        {rows && rows.length > 0 && (list.offset > 0 || list.hasMore) && (
          <CardFooter>
            <Pagination className="w-full"
              offset={list.offset}
              limit={list.pageSize}
              shown={rows.length}
              total={list.total}
              hasMore={list.hasMore}
              onChange={list.setOffset}
            />
          </CardFooter>
        )}
      </Card>

      <Sheet open={selected !== null} onOpenChange={(v) => { if (!v) close(); }}>
        <SheetContent width="md">
          <SheetHeader
            eyebrow="Package payment"
            title={s ? (s.package_name || "Package not named") : "Package payment"}
            description={s ? `${paidBy(s.method)}${s.at ? ` · ${formatDate(s.at)}` : ""}` : undefined}
            badges={state ? <StatusBadge tone={state.tone}>{state.label}</StatusBadge> : undefined}
          />
          <SheetBody>
            {!detail || !s || !state ? (
              detailFailed ? (
                <div role="alert" className={FADE}>
                  <EmptyState
                    size="sm"
                    icon={<AlertTriangle />}
                    title="This payment could not be loaded"
                    hint="Check the appliance is reachable, then try again. Nothing has been changed."
                    action={
                      <Button size="sm" variant="secondary" disabled={detailLoading}
                        onClick={() => selected && void fetchDetail(selected)}>
                        <RefreshCw className={spin(detailLoading)} /> Try again
                      </Button>
                    }
                  />
                </div>
              ) : (
                <div className="space-y-3" aria-busy="true">
                  <span className="sr-only">Loading the payment</span>
                  <Skeleton className="h-20 w-full" />
                  <Skeleton className="h-28 w-full" />
                </div>
              )
            ) : (
              <div className={cn("space-y-6", FADE)}>
                <SheetSection title="What happened">
                  <p className="text-sm">{state.explain}</p>
                  {s.status === "MANUAL_REVIEW" && (
                    <Link href="/financial-review" className="mt-2 inline-flex items-center gap-1 text-sm text-primary underline-offset-4 hover:underline">
                      Decide it on Manual review <ArrowUpRight className="size-3.5" aria-hidden />
                    </Link>
                  )}
                  {s.status === "FAILED" && s.method === "PMS_POSTING" && (
                    <Link href="/financial-health" className="mt-2 inline-flex items-center gap-1 text-sm text-primary underline-offset-4 hover:underline">
                      See room charge problems on Charge health <ArrowUpRight className="size-3.5" aria-hidden />
                    </Link>
                  )}
                </SheetSection>
                <SheetSection title="Details">
                  <KeyValueGrid
                    items={[
                      { label: "Package", value: s.package_name || "Package not named" },
                      { label: "Client", value: sourceWords(s, multi) },
                      { label: "Paid by", value: paidBy(s.method) },
                      { label: "Amount", value: amountWords(s) },
                      { label: "Access", value: ACCESS_WORDS[s.purchase_state] ?? "Not recognised" },
                      { label: "When", value: s.at ? formatDate(s.at) : "Not known" },
                    ]}
                  />
                </SheetSection>
                {(s.method === "ONLINE_PAYMENT" || detail.payments.length > 0) && (
                  <SheetSection title="Card payment history">
                    {detail.payments.length === 0 ? (
                      <p className="text-sm text-muted-foreground">No card payment has been started for this package.</p>
                    ) : (
                      <div className="overflow-hidden rounded-md border border-border">
                        <Table label="Card payment history" aria-label="Card payment history">
                          <THead>
                            <TR><TH>Step</TH><TH>Amount</TH><TH>Result</TH></TR>
                          </THead>
                          <TBody>
                            {detail.payments.map((p) => {
                              const res = resultWords(p);
                              const report = settledAs(p);
                              return (
                                <TR key={p.payment_id}>
                                  <TD>{STEP_WORDS[p.transaction_type] ?? "Not recognised"}</TD>
                                  <TD className="tabular">
                                    {money(p.amount_minor, p.currency, p.currency_exponent)}
                                    {report && (
                                      <div className="text-2xs text-muted-foreground" data-testid="settlement-report">{report}</div>
                                    )}
                                  </TD>
                                  <TD><Badge tone={res.tone}>{res.label}</Badge></TD>
                                </TR>
                              );
                            })}
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
              </div>
            )}
          </SheetBody>
          <SheetFooter>
            <Button variant="ghost" onClick={close}>Close</Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </PageShell>
  );
}
