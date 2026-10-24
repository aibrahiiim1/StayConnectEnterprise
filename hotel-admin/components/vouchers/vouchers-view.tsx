"use client";

// Voucher codes can be recovered only through an audited reveal or export after their one-time issue response.
// Expiry is computed from the validity window at sign-in; filters and actions use the same effective state.

import * as React from "react";
import { Ban, CalendarClock, CheckCircle2, Plus, Settings2, Ticket, TicketCheck } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardFooter } from "@/components/ui/card";
import { FilterBar } from "@/components/ui/toolbar";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { FilterChips, Pagination, SearchInput } from "@/components/ui/data";
import { Explain } from "@/components/ui/tooltip";
import { Select } from "@/components/ui/input";
import { SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  batchLabel,
  effectiveStatus,
  maskedCode,
  validityWords,
  type EffectiveStatus,
  type Grantable,
  type VoucherBatch,
  type VoucherBatchesResp,
  type VoucherListResp,
  type VoucherReveal,
  type VoucherRow,
  type VoucherSummaryFull,
} from "@/lib/api/vouchers";
import { AccessLogTab } from "./access-log-tab";
import { BatchesTab } from "./batches-tab";
import { CodeSecurityTab } from "./code-security-tab";
import { IssueDialog } from "./issue-dialog";
import { StatusBadge, When } from "./shared";
import { VoucherSheet } from "./voucher-sheet";

const PAGE = 50;
type StatusFilter = "all" | EffectiveStatus;
type Tab = "vouchers" | "batches" | "access" | "security";

/** Fall back to stored-state counts for older servers without summary counts. */
function chipCounts(s: VoucherSummaryFull | null): Record<StatusFilter, number | undefined> {
  if (!s) return { all: undefined, available: undefined, not_yet_valid: undefined, expired: undefined, redeemed: undefined, cancelled: undefined };
  const total = s.total ?? s.unused + s.redeemed + s.revoked + s.redemption_expired;
  return {
    all: total,
    available: s.available ?? s.unused,
    not_yet_valid: s.not_yet_valid ?? 0,
    expired: s.expired_unused ?? s.redemption_expired,
    redeemed: s.redeemed,
    cancelled: s.revoked,
  };
}

export function VouchersView(props: {
  canIssue: boolean;
  canRevealCodes: boolean;
  canReadFormat: boolean;
  canEditFormat: boolean;
  canReadBranding?: boolean;
  /** Show read-only roles that they cannot print, cancel, or reveal codes. */
  readOnly?: boolean;
}) {
  const { canIssue, canRevealCodes, canReadFormat, canEditFormat, canReadBranding = false, readOnly = false } = props;

  const [tab, setTab] = React.useState<Tab>("vouchers");
  const [reloadKey, setReloadKey] = React.useState(0);
  const reload = React.useCallback(() => setReloadKey((k) => k + 1), []);

  const [summary, setSummary] = React.useState<VoucherSummaryFull | null>(null);
  const [summaryErr, setSummaryErr] = React.useState<unknown>(null);
  React.useEffect(() => {
    api
      .get<VoucherSummaryFull>("/vouchers/summary")
      .then((s) => {
        setSummary(s);
        setSummaryErr(null);
      })
      .catch(setSummaryErr);
  }, [reloadKey]);

  const [status, setStatus] = React.useState<StatusFilter>("all");
  const [search, setSearch] = React.useState("");
  const [pkg, setPkg] = React.useState("");
  const [batch, setBatch] = React.useState("");
  const [offset, setOffset] = React.useState(0);
  const last4 = search.trim().toUpperCase();
  const searchProblem = last4 && !/^[A-Z0-9]{1,4}$/.test(last4) ? "Search uses up to the last 4 letters or digits of a code." : null;
  const narrowed = !!(last4 && !searchProblem) || !!pkg || !!batch;

  React.useEffect(() => setOffset(0), [status, last4, pkg, batch]);

  const filterQuery = React.useMemo(() => {
    const p = new URLSearchParams();
    if (last4 && !searchProblem) p.set("last4", last4);
    if (pkg) p.set("package_revision_id", pkg);
    if (batch) p.set("batch_id", batch);
    return p;
  }, [last4, searchProblem, pkg, batch]);

  // Chip counts follow the other active filters.
  const [filteredSummary, setFilteredSummary] = React.useState<VoucherSummaryFull | null>(null);
  React.useEffect(() => {
    if (!narrowed) {
      setFilteredSummary(null);
      return;
    }
    api
      .get<VoucherSummaryFull>(`/vouchers/summary?${filterQuery.toString()}`)
      .then(setFilteredSummary)
      .catch(() => setFilteredSummary(null));
  }, [narrowed, filterQuery, reloadKey]);
  const counts = chipCounts(narrowed ? filteredSummary : summary);

  const [list, setList] = React.useState<VoucherListResp | null>(null);
  const [listErr, setListErr] = React.useState<unknown>(null);
  React.useEffect(() => {
    if (searchProblem) return;
    const p = new URLSearchParams(filterQuery);
    p.set("limit", String(PAGE));
    p.set("offset", String(offset));
    if (status !== "all") p.set("effective", status);
    setList(null);
    api
      .get<VoucherListResp>(`/vouchers/?${p.toString()}`)
      .then((m) => {
        setList(m);
        setListErr(null);
      })
      .catch((e) => {
        setListErr(e);
        setList({ vouchers: [] });
      });
  }, [filterQuery, status, offset, reloadKey, searchProblem]);

  const [grantable, setGrantable] = React.useState<Grantable[]>([]);
  const [batches, setBatches] = React.useState<VoucherBatch[]>([]);
  React.useEffect(() => {
    api
      .get<{ revisions?: Grantable[] }>("/vouchers/grantable")
      .then((m) => setGrantable(m.revisions ?? []))
      .catch(() => setGrantable([]));
  }, []);
  React.useEffect(() => {
    api
      .get<VoucherBatchesResp>("/vouchers/batches?limit=200")
      .then((m) => setBatches(m.batches ?? []))
      .catch(() => setBatches([]));
  }, [reloadKey]);

  const packageOptions = React.useMemo(() => {
    const m = new Map<string, string>();
    for (const g of grantable) m.set(g.id, `${g.name} (version ${g.revision_no})`);
    for (const b of batches)
      if (!m.has(b.package_revision_id) && b.package_name)
        m.set(b.package_revision_id, `${b.package_name}${b.package_revision_no ? ` (version ${b.package_revision_no})` : ""}`);
    return [...m.entries()].sort((a, b) => a[1].localeCompare(b[1]));
  }, [grantable, batches]);

  const [reveals, setReveals] = React.useState<VoucherReveal[] | null>(null);
  const [revealsErr, setRevealsErr] = React.useState<unknown>(null);
  React.useEffect(() => {
    if (!canRevealCodes) return;
    api
      .get<{ reveals?: VoucherReveal[] }>("/voucher-codes/reveals")
      .then((m) => {
        setReveals(m.reveals ?? []);
        setRevealsErr(null);
      })
      .catch(setRevealsErr);
  }, [canRevealCodes, reloadKey]);

  const [hotelName, setHotelName] = React.useState<string | undefined>(undefined);
  React.useEffect(() => {
    if (!canReadBranding) return;
    api
      .get<{ design?: { hotel_name?: string } }>("/portal-branding")
      .then((m) => setHotelName(m.design?.hotel_name || undefined))
      .catch(() => setHotelName(undefined));
  }, [canReadBranding]);

  const [issueOpen, setIssueOpen] = React.useState(false);
  const [selected, setSelected] = React.useState<VoucherRow | null>(null);
  const [focusBatch, setFocusBatch] = React.useState<{ id: string } | null>(null);

  const rows = list?.vouchers ?? null;
  const batchKnown = !batch || batches.some((b) => b.batch_id === batch);

  function viewBatchCards(id: string) {
    setBatch(id);
    setStatus("all");
    setSearch("");
    setTab("vouchers");
  }

  return (
    <PageShell width="wide">
      <PageHeader
        title="Vouchers"
        description="Printed cards a client redeems for internet access."
        help={
          <>
            <HelpSection title="Four jobs">
              <HelpList items={[
                <><strong>Issue</strong> prints a batch. The codes appear once, in the issue dialog.</>,
                <><strong>Show code</strong> recovers one code for a card already in circulation.</>,
                <><strong>Export</strong> recovers a whole batch, with one record naming the size of the selection.</>,
                <><strong>Cancel</strong> ends an unused card that has not expired. A used card cannot be cancelled: that client already has access.</>,
              ]} />
            </HelpSection>
            <HelpSection title="Showing and exporting codes">
              <p>
                A voucher code is encrypted, not hashed, so it can be read again. Showing or exporting a code needs
                your password and a reason, and is recorded permanently with your name. The Access log lists every
                reveal and export.
              </p>
            </HelpSection>
            <HelpSection title="Card status">
              <p>
                Expiry is enforced when a client signs in: a card past its valid-until is refused there, while its
                record still says unused. The statuses here show what each card is right now.
              </p>
            </HelpSection>
            <HelpSection title="Code format and keys">
              <p>
                The code format applies to the next batch you issue; cards already printed keep working. Every code
                is indexed under a key, and each card stays tied to the key that made it. Retiring a key means new
                batches use a fresh one.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <>
            {canReadFormat && (
              <Button variant="secondary" onClick={() => setTab("security")}>
                <Settings2 /> Code format
              </Button>
            )}
            {canIssue && (
              <Button onClick={() => setIssueOpen(true)}>
                <Plus /> Issue vouchers
              </Button>
            )}
          </>
        }
      />

      {readOnly && <ReadOnlyNotice>Your role can see the voucher cards but not issue, cancel or reveal them.</ReadOnlyNotice>}
      <ErrorBanner err={summaryErr} />
      <StatGrid cols={5}>
        <StatCard
          label="Available now"
          value={summary ? (summary.available ?? summary.unused).toLocaleString() : "—"}
          icon={<CheckCircle2 />}
          hint="Unused, still valid"
        />
        <StatCard
          label="Used"
          value={summary ? summary.redeemed.toLocaleString() : "—"}
          icon={<TicketCheck />}
          hint="Redeemed by clients"
        />
        <StatCard
          label="Expired unused"
          value={summary ? (summary.expired_unused ?? summary.redemption_expired).toLocaleString() : "—"}
          icon={<CalendarClock />}
          tone={summary && (summary.expired_unused ?? 0) > 0 ? "warn" : "default"}
          hint="Refused at sign-in"
        />
        <StatCard
          label="Cancelled"
          value={summary ? summary.revoked.toLocaleString() : "—"}
          icon={<Ban />}
          hint="Before use"
        />
        <StatCard
          label="Issued this week"
          value={summary?.issued_last_7_days != null ? summary.issued_last_7_days.toLocaleString() : "—"}
          icon={<Ticket />}
          hint="Last 7 days"
          className="col-span-2 sm:col-span-1"
        />
      </StatGrid>

      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList className="overflow-x-auto">
          <TabsTrigger value="vouchers">Vouchers</TabsTrigger>
          <TabsTrigger value="batches">Batches</TabsTrigger>
          {canRevealCodes && <TabsTrigger value="access">Access log</TabsTrigger>}
          {canReadFormat && <TabsTrigger value="security">Code security</TabsTrigger>}
        </TabsList>

        <TabsContent value="vouchers" className="space-y-4 pt-5">
          <Card className="overflow-hidden">
            <FilterBar
              inset
              search={
                <SearchInput
                  value={search}
                  onChange={setSearch}
                  delay={300}
                  placeholder="Last 4 characters"
                  label="Search by the last 4 characters of a code"
                />
              }
              actions={narrowed ? (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setSearch("");
                    setPkg("");
                    setBatch("");
                  }}
                >
                  Clear filters
                </Button>
              ) : undefined}
            >
              <Select aria-label="Package" className="w-full sm:w-56" value={pkg} onChange={(e) => setPkg(e.target.value)}>
                <option value="">All packages</option>
                {packageOptions.map(([id, label]) => (
                  <option key={id} value={id}>
                    {label}
                  </option>
                ))}
              </Select>
              <Select aria-label="Batch" className="w-full sm:w-64" value={batch} onChange={(e) => setBatch(e.target.value)}>
                <option value="">All batches</option>
                {!batchKnown && <option value={batch}>Batch {batch.slice(0, 8)}</option>}
                {batches.map((b) => (
                  <option key={b.batch_id} value={b.batch_id}>
                    {batchLabel(b)} · {b.count} cards
                  </option>
                ))}
              </Select>
            </FilterBar>
            {searchProblem && <p className="border-b border-border px-4 py-2 text-xs text-destructive">{searchProblem}</p>}
            <FilterBar
              inset
              filters={
                <>
                  <FilterChips
                    label="Status"
                    value={status}
                    onChange={setStatus}
                    options={[
                      { value: "all", label: "All", count: counts.all },
                      { value: "available", label: "Available", count: counts.available, tone: "ok" },
                      { value: "not_yet_valid", label: "Not yet valid", count: counts.not_yet_valid, tone: "info" },
                      { value: "expired", label: "Expired (never used)", count: counts.expired, tone: "warn" },
                      { value: "redeemed", label: "Used", count: counts.redeemed },
                      { value: "cancelled", label: "Cancelled", count: counts.cancelled, tone: "err" },
                    ]}
                  />
                  <Explain>
                    Expiry is enforced when a client signs in: a card past its valid-until is refused there, while its record
                    still says unused. These statuses show what each card is right now.
                  </Explain>
                </>
              }
            />

            <ErrorBanner err={listErr} className="mx-4 mt-3" />
            {rows === null ? (
              <SkeletonRows rows={6} cols={5} />
            ) : rows.length === 0 ? (
              <EmptyState
                size="sm"
                icon={<Ticket />}
                title={narrowed || status !== "all" ? "No cards match these filters" : "No vouchers yet"}
                hint={
                  narrowed || status !== "all"
                    ? "Try another status, or clear the filters."
                    : canIssue
                      ? "Issue a batch to print cards clients can use to sign in."
                      : "Cards appear here once someone issues a batch."
                }
                action={
                  !narrowed && status === "all" && canIssue ? (
                    <Button onClick={() => setIssueOpen(true)}>
                      <Plus /> Issue vouchers
                    </Button>
                  ) : undefined
                }
              />
            ) : (
              <Table label="Voucher cards">
                  <THead>
                    <TR>
                      <TH>Card</TH>
                      <TH>Package</TH>
                      <TH>Status</TH>
                      <TH className="hidden sm:table-cell">Validity</TH>
                      <TH className="hidden md:table-cell">Batch</TH>
                      <TH className="hidden lg:table-cell">Issued</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {rows.map((r) => (
                      <TR key={r.id} aria-label={`Card ${maskedCode(r.code_last4)}`} onClick={() => setSelected(r)}>
                        <TD nowrap className="font-mono tracking-widest">{maskedCode(r.code_last4)}</TD>
                        <TD truncate title={r.package_name ?? undefined} className="max-w-[14rem]">
                          {r.package_name ?? <span className="text-muted-foreground">Earlier package version</span>}
                        </TD>
                        <TD nowrap>
                          <StatusBadge status={effectiveStatus(r)} />
                        </TD>
                        <TD nowrap className="hidden sm:table-cell">
                          {validityWords(r.redemption_valid_from, r.redemption_valid_until)}
                        </TD>
                        <TD className="hidden md:table-cell">
                          {r.batch_id ? (
                            <span className="font-mono text-xs text-muted-foreground">{r.batch_id.slice(0, 8)}</span>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </TD>
                        <TD nowrap className="hidden lg:table-cell">
                          <When iso={r.created_at} />
                          {r.issued_by_label && <div className="text-xs text-muted-foreground">{r.issued_by_label}</div>}
                        </TD>
                      </TR>
                    ))}
                  </TBody>
              </Table>
            )}
            {rows && rows.length > 0 && (
              <CardFooter>
                <Pagination offset={offset} limit={PAGE} shown={rows.length} total={list?.total ?? null} onChange={setOffset} />
              </CardFooter>
            )}
          </Card>
        </TabsContent>

        <TabsContent value="batches" className="pt-5">
          <BatchesTab
            canRevealCodes={canRevealCodes}
            canCancel={canIssue}
            reloadKey={reloadKey}
            onViewCards={viewBatchCards}
            hotelName={hotelName}
            focusBatch={focusBatch}
          />
        </TabsContent>

        {canRevealCodes && (
          <TabsContent value="access" className="pt-5">
            <AccessLogTab reveals={reveals} error={revealsErr} />
          </TabsContent>
        )}

        {canReadFormat && (
          <TabsContent value="security" className="pt-5">
            <CodeSecurityTab canEditFormat={canEditFormat} />
          </TabsContent>
        )}
      </Tabs>

      <VoucherSheet
        row={selected}
        onOpenChange={(v) => !v && setSelected(null)}
        canIssue={canIssue}
        canRevealCodes={canRevealCodes}
        reveals={reveals}
        onChanged={reload}
        onShowBatch={(id) => {
          setSelected(null);
          setFocusBatch({ id });
          setTab("batches");
        }}
      />

      <IssueDialog open={issueOpen} onOpenChange={setIssueOpen} onIssued={reload} hotelName={hotelName} />
    </PageShell>
  );
}
