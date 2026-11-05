"use client";

// Email, phone and social sign-ins are not listed as sources: their identifiers are not readable by this console's service.
// Nothing is estimated; a source with no measured usage says so rather than showing zero.
// Never show secrets: vouchers by card reference (not code), client accounts by username (not display name).
// Room words appear only on a Hotel room/stay source, always with its PMS connection.

import { useCallback, useEffect, useRef, useState } from "react";
import { Activity, AlertTriangle, ArrowLeft, ChevronRight, FileSearch, KeyRound, RefreshCw, Smartphone } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader, PageShell } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { PERIODS, periodQuery, type Period } from "@/lib/usage-period";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Meter, MonoId, SkeletonRows } from "@/components/ui/misc";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { FilterChips, MetricStrip, Pagination, SearchInput } from "@/components/ui/data";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { refreshingClass } from "@/components/ui/patterns";
import { formatBytes, quotaPercent, endReasonWords } from "@/lib/bytes";
import { cn, formatDate } from "@/lib/utils";
import { moduleHasHistory, useCapabilities } from "@/lib/capabilities";

type Totals = { bytes_down: number; bytes_up: number; bytes_total: number; sessions: number; devices: number };
type StayRow = {
  stay_id: string; room: string; pms_interface: string; reservation?: string; stay_status: string;
  arrival?: string; departure?: string; effective_checkout_at?: string;
  totals: Totals; quota_bytes?: number | null; consumed_bytes?: number | null; end_reason?: string;
};
type DeviceRow = {
  mac: string; bytes_down: number; bytes_up: number; bytes_total: number; sessions: number;
  first_seen?: string; last_seen?: string;
};
// A session carried across packages (a package change, or check-out) lists each stretch; bytes only when the split is exact.
type SessionPackage = {
  entitlement_id: string; package_name: string; package_type?: string; from: string; until?: string;
  bytes_down?: number; bytes_up?: number; bytes_total?: number;
};
type SessionRow = {
  session_id: string; mac?: string; ip?: string; credential_method?: string; state: string;
  started?: string; ended?: string; end_reason?: string;
  bytes_down: number; bytes_up: number; bytes_total: number;
  packages?: SessionPackage[]; package_split_exact?: boolean;
};
type PackageRow = {
  entitlement_id: string; name: string; package_type?: string; service_plan?: string; status: string;
  activated_at?: string; terminated_at?: string; end_reason?: string; quota_bytes?: number | null; used_bytes: number;
};
type LatestPackage = { latest_package_name?: string; latest_package_type?: string; latest_package_status?: string };
type DeviceDetail = {
  mac: string; from: string; to: string; first_seen?: string; last_seen?: string;
  totals: Totals; sessions: SessionRow[]; stays: StayRow[];
};

type SourceType = "account" | "voucher" | "stay" | "open";
type SourceRow = {
  source_type: SourceType; source_id: string;
  account_username?: string;
  // Hotel room/stay only; a room always travels with its PMS connection.
  room?: string; pms_interface?: string; reservation?: string; stay_status?: string;
  arrival?: string; departure?: string; effective_checkout_at?: string;
  totals: Totals; quota_bytes?: number | null; consumed_bytes?: number | null; end_reason?: string;
  last_activity?: string;
} & LatestPackage;
type SourceDetail = {
  source: SourceRow; service_plan?: string; access_status?: string;
  packages?: PackageRow[]; older_packages_omitted?: boolean; devices: DeviceRow[]; sessions: SessionRow[];
  from?: string; to?: string;
};

type Tab = "sources" | "devices";

const USAGE_PAGE = 50;
type TypeFilter = "all" | SourceType;

const SOURCE_LABEL: Record<SourceType, string> = {
  account: "Client account",
  voucher: "Voucher",
  stay: "Hotel room/stay",
  open: "Without sign-in",
};

const SEARCH_HINT: Record<TypeFilter, { label: string; placeholder: string }> = {
  all: {
    label: "Username, card reference, room number or reservation",
    placeholder: "Username, card reference, room or reservation — or leave empty for the heaviest users",
  },
  account: { label: "Client account username", placeholder: "Username, e.g. alex.morgan" },
  voucher: { label: "Voucher card reference", placeholder: "Card reference — the first characters are enough" },
  stay: { label: "Room number or reservation", placeholder: "Room number or reservation" },
  open: { label: "Access reference", placeholder: "Access reference — the first characters are enough" },
};

const SEARCH_HINT_NO_ROOMS = {
  label: "Username or card reference",
  placeholder: "Username or card reference — or leave empty for the heaviest users",
};

const STAY_WORDS: Record<string, string> = {
  IN_HOUSE: "In house",
  RESERVED: "Arriving",
  CHECKED_OUT: "Checked out",
  POST_STAY_ACTIVE: "After check-out access",
  CANCELLED: "Cancelled",
  NO_SHOW: "No show",
};
const stayWords = (s: string) => STAY_WORDS[s] ?? s.replace(/_/g, " ").toLowerCase();

const ACCESS_WORDS: Record<string, string> = {
  PENDING: "Not started",
  ACTIVE: "Active",
  SUSPENDED: "Suspended",
  TERMINATED: "Ended",
};
const accessWords = (s: string) => ACCESS_WORDS[s] ?? s.replace(/_/g, " ").toLowerCase();

const shortRef = (id: string) => (id.length > 12 ? `${id.slice(0, 8)}…` : id);

function sourceTitle(s: SourceRow): string {
  switch (s.source_type) {
    case "stay": return s.room ? `Room ${s.room}` : "Stay";
    case "account": return s.account_username || "Client account";
    case "voucher": return `Card ${shortRef(s.source_id)}`;
    case "open": return `Access ${shortRef(s.source_id)}`;
  }
}

/** A package by what an operator calls it: after-check-out and emergency access are system packages with internal names. */
function packageLabel(name?: string, type?: string): string {
  if (type === "CHECKOUT_GRACE") return "After check-out access";
  if (type === "EMERGENCY_GRACE") return "Emergency access";
  return name || "Package";
}

/** The latest package's own allowance and meter; never set against the source's totals, which span every package. */
function allowanceText(consumed?: number | null, quota?: number | null): string {
  if (!quota) return "No limit";
  return `${formatBytes(Math.min(consumed ?? 0, quota))} of ${formatBytes(quota)}`;
}

function LatestPackageCell({ row }: { row: SourceRow }) {
  if (!row.latest_package_name && !row.latest_package_type) return <span className="text-sm text-muted-foreground">—</span>;
  const pct = quotaPercent(row.consumed_bytes, row.quota_bytes);
  const label = packageLabel(row.latest_package_name, row.latest_package_type);
  const state = [row.latest_package_status ? accessWords(row.latest_package_status) : "", endReasonWords(row.end_reason) ?? ""]
    .filter(Boolean).join(" · ");
  return (
    <div className="min-w-44 space-y-1">
      <div className="truncate text-sm font-medium" title={label}>{label}</div>
      {pct === null ? (
        <div className="text-caption text-muted-foreground">No limit</div>
      ) : (
        <Meter className="min-w-40" value={pct} max={100} caption={allowanceText(row.consumed_bytes, row.quota_bytes)} />
      )}
      {state && <div className="truncate text-caption text-muted-foreground" title={state}>{state}</div>}
    </div>
  );
}

export default function UsageExplorerPage() {
  const [tab, setTab] = useState<Tab>("sources");
  const [q, setQ] = useState("");
  const [typeFilter, setTypeFilter] = useState<TypeFilter>("all");
  const [rows, setRows] = useState<SourceRow[] | null>(null);
  const [source, setSource] = useState<SourceDetail | null>(null);
  const [device, setDevice] = useState<DeviceDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [samples, setSamples] = useState<Record<string, { bytes_total: number; sample_count: number; truncated?: boolean } | "loading">>({});
  const [period, setPeriod] = useState<Period>("30");
  const [fromDay, setFromDay] = useState("");
  const [toDay, setToDay] = useState("");
  const range = periodQuery(period, fromDay, toDay);

  // Only the newest request's answer is applied.
  const [offset, setOffset] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const latest = useRef(0);
  const search = useCallback(async (type: TypeFilter = typeFilter, at = 0) => {
    const range = periodQuery(period, fromDay, toDay);
    if (range.error) { setErr(range.error); return; }
    const mine = ++latest.current;
    setBusy(true); setErr(null);
    try {
      const t = type === "all" ? "" : type;
      const page = Math.floor(at / USAGE_PAGE) + 1;
      // Header, not URL: edged logs request lines.
      const needle = q.trim();
      const r = await api.get<{ data: SourceRow[]; meta?: { has_more?: boolean } }>(
        `/usage/sources?type=${encodeURIComponent(t)}&page=${page}&page_size=${USAGE_PAGE}&${range.qs}`,
        needle ? { "X-Usage-Search": encodeURIComponent(needle) } : undefined,
      );
      if (mine !== latest.current) return; // superseded
      setRows(r.data ?? []);
      setOffset(at);
      setHasMore(!!r.meta?.has_more);
    } catch (e) {
      if (mine !== latest.current) return;
      setErr(e instanceof ApiError ? e.message : "the usage records could not be read");
    } finally { if (mine === latest.current) setBusy(false); }
  }, [q, typeFilter, period, fromDay, toDay]);

  useEffect(() => { if (tab === "sources" && rows === null && err === null) search(); }, [tab, rows, err, search]);

  async function openSource(type: SourceType, id: string) {
    setBusy(true); setErr(null);
    try { setSource(await api.get<SourceDetail>(`/usage/sources/${type}/${encodeURIComponent(id)}?${periodQuery(period, fromDay, toDay).qs}`)); }
    catch (e) { setErr(e instanceof ApiError ? e.message : "that access source could not be read"); }
    finally { setBusy(false); }
  }

  // Deep link /usage?stay=<id>; read from location, not useSearchParams, which needs a Suspense boundary for the static build.
  useEffect(() => {
    const id = typeof window === "undefined" ? null : new URLSearchParams(window.location.search).get("stay");
    if (id) void openSource("stay", id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function lookUpDevice(mac: string) {
    setBusy(true); setErr(null); setSource(null);
    try { setDevice(await api.get<DeviceDetail>(`/usage/devices/${encodeURIComponent(mac)}?${periodQuery(period, fromDay, toDay).qs}`)); setTab("devices"); }
    catch (e) { setErr(e instanceof ApiError ? e.message : "that device could not be read"); setDevice(null); }
    finally { setBusy(false); }
  }

  async function openSamples(sessionID: string) {
    setSamples((p) => ({ ...p, [sessionID]: "loading" }));
    try {
      const r = await api.get<{ bytes_total: number; sample_count: number; truncated?: boolean }>(`/usage/sessions/${sessionID}/samples`);
      setSamples((p) => ({ ...p, [sessionID]: r }));
    } catch {
      setSamples((p) => { const n = { ...p }; delete n[sessionID]; return n; });
    }
  }

  const src = source?.source;
  const isStay = src?.source_type === "stay";
  // Room/stay options show only where hospitality is licensed or has history (usage stays reachable after a lapse).
  const caps = useCapabilities();
  const stays = moduleHasHistory(caps, "hospitality");
  const hint = typeFilter === "all" && !stays ? SEARCH_HINT_NO_ROOMS : SEARCH_HINT[typeFilter];

  return (
    <PageShell>
      <PageHeader
        title="Usage explorer"
        description="Trace what an access source or a device used, down to the recorded samples."
        help={
          <>
            <HelpSection title="Two ways in">
              <HelpList items={[
                <><strong>By access source</strong> &mdash; for &ldquo;My access stopped and I never got what I was given.&rdquo; An access source is whatever granted the access: {stays
                  ? <>a <strong>client account</strong>, a <strong>voucher</strong>, or a <strong>Hotel room/stay</strong>. Filter by type and search by username, card reference, room number or reservation</>
                  : <>a <strong>client account</strong> or a <strong>voucher</strong>. Filter by type and search by username or card reference</>} &mdash; or leave the search empty for the heaviest users.</>,
                <><strong>By device</strong> &mdash; for &ldquo;Something on our network downloaded 40 GB last night.&rdquo; Paste the device&rsquo;s MAC address.</>,
              ]} />
            </HelpSection>
            <HelpSection title="What each source shows">
              <HelpList items={[
                <><strong>Client account</strong> &mdash; its username, as on Client accounts.</>,
                <><strong>Voucher</strong> &mdash; its card reference, as on the voucher&rsquo;s card details. The code itself is never shown here.</>,
                ...(stays
                  ? [<><strong>Hotel room/stay</strong> &mdash; the room with its PMS connection, the reservation, and arrival and departure.</>]
                  : []),
              ]} />
              <p>
                Email, phone and social sign-ins are not listed as sources. The devices that used them still appear
                under By device.
              </p>
            </HelpSection>
            <HelpSection title="Every figure is traceable">
              <p>
                Every total is the sum of recorded sessions. A session is one period of connected access; show its
                evidence to see the recorded samples it was measured from, with their own total next to the
                session&rsquo;s. Nothing here is estimated.
              </p>
              <p>
                The totals cover every package a source was given. The latest package column shows that package&rsquo;s
                own allowance and use, which is what its limit is enforced against, so a total larger than the latest
                allowance is not an overrun.
              </p>
              <p>Only access sources that were given internet access appear. A source with no access has nothing to measure.</p>
            </HelpSection>
            <HelpSection title="A device is not a person">
              <p>
                A device address identifies a piece of equipment. The device view shows what it used and which
                {stays ? " stays" : " access"} it was connected under &mdash; it does not tell you who was holding it.
              </p>
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />

      <FilterBar>
        <span className="text-label text-muted-foreground" aria-hidden>Period</span>
        <Select aria-label="Period" className="w-full sm:w-48" value={period}
          onChange={(e) => { setPeriod(e.target.value as Period); setErr(null); setRows(null); setSource(null); setDevice(null); }}>
          {PERIODS.map((p) => <option key={p.value} value={p.value}>{p.label}</option>)}
        </Select>
        {period === "custom" && (
          <>
            <Input type="date" aria-label="From" className="w-full sm:w-44" value={fromDay} onChange={(e) => setFromDay(e.target.value)} />
            <Input type="date" aria-label="To" className="w-full sm:w-44" value={toDay} onChange={(e) => setToDay(e.target.value)} />
            <Button variant="secondary" disabled={!!range.error} onClick={() => { setErr(null); setRows(null); setSource(null); setDevice(null); }}>Apply</Button>
          </>
        )}
        <span className="text-xs text-muted-foreground">Every total on this page covers this period.</span>
      </FilterBar>

      <Tabs
        value={tab}
        onValueChange={(v) => { setTab(v as Tab); setSource(null); setDevice(null); setErr(null); }}
      >
        <TabsList aria-label="What to investigate">
          <TabsTrigger value="sources"><KeyRound className="size-4" aria-hidden /> By access source</TabsTrigger>
          <TabsTrigger value="devices"><Smartphone className="size-4" aria-hidden /> By device</TabsTrigger>
        </TabsList>

        <TabsContent value="sources" className="mt-5 space-y-5">
          {!source && (
            <Card className="overflow-hidden">
              <div className="space-y-3 border-b border-border px-4 py-3">
                <FilterChips<TypeFilter>
                  label="Access source type"
                  value={typeFilter}
                  onChange={(v) => { setTypeFilter(v); void search(v); }}
                  options={[
                    { value: "all", label: "All" },
                    { value: "account", label: SOURCE_LABEL.account },
                    { value: "voucher", label: SOURCE_LABEL.voucher },
                    ...(stays ? [{ value: "stay" as const, label: SOURCE_LABEL.stay }] : []),
                    { value: "open", label: SOURCE_LABEL.open },
                  ]}
                />
                {/* A search region, not a <form>: a lone text box labelled "Username…" inside a form is what makes a
                    browser offer saved sign-in credentials. SearchInput also opts out of every password manager. */}
                <div role="search" className="flex flex-wrap gap-2">
                  <SearchInput value={q} onChange={setQ} onSearch={() => void search()} name="usage-source-filter"
                    id="usage-source-filter" className="min-w-0 flex-1 sm:w-auto"
                    label={hint.label} placeholder={hint.placeholder} />
                  <Button variant="secondary" disabled={busy} onClick={() => void search()}>{busy ? "Searching…" : "Search"}</Button>
                </div>
              </div>

              {rows === null && err !== null ? (
                <EmptyState size="sm" icon={<AlertTriangle />} title="The usage records could not be loaded"
                  action={<Button size="sm" variant="secondary" disabled={busy} onClick={() => void search()}><RefreshCw /> Try again</Button>} />
              ) : rows === null ? <SkeletonRows rows={4} cols={5} /> : rows.length === 0 ? (
                <EmptyState size="sm" icon={<Activity />} title={q.trim() ? "No access source matched" : "No usage recorded yet"}
                  hint="Only access sources that were given internet access appear here." />
              ) : (
                <div className={cn(busy && refreshingClass)}>
                  <Table label="Access sources">
                    <THead>
                      <TR>
                        <TH>Access source</TH>
                        <TH className="hidden md:table-cell">Details</TH>
                        <TH numeric className="hidden sm:table-cell">Downloaded</TH>
                        <TH numeric className="hidden 2xl:table-cell">Uploaded</TH>
                        <TH numeric>Total, all packages</TH>
                        <TH className="hidden lg:table-cell">Latest package</TH>
                        <TH pinEnd><span className="sr-only">Open</span></TH>
                      </TR>
                    </THead>
                    <TBody>
                      {rows.map((r) => {
                        const title = sourceTitle(r);
                        const kind = r.source_type === "stay" ? `${SOURCE_LABEL.stay} · ${r.pms_interface ?? ""}` : SOURCE_LABEL[r.source_type];
                        const reservation = r.reservation ? `Reservation ${r.reservation}` : "—";
                        return (
                        <TR key={`${r.source_type}:${r.source_id}`} onClick={() => void openSource(r.source_type, r.source_id)}>
                          <TD nowrap className="max-w-[16rem]">
                            <div className={cn("truncate font-medium", r.source_type === "voucher" && "font-mono text-sm")} title={title}>
                              {title}
                            </div>
                            <div className="truncate text-caption text-muted-foreground" title={kind}>{kind}</div>
                          </TD>
                          <TD className="hidden max-w-[12rem] text-sm md:table-cell">
                            {r.source_type === "stay" ? (
                              <>
                                <div className="truncate" title={reservation}>{reservation}</div>
                                <div className="text-caption text-muted-foreground">
                                  {r.stay_status ? stayWords(r.stay_status) : ""}
                                  {r.arrival ? ` · ${formatDate(r.arrival)}` : ""}
                                </div>
                              </>
                            ) : (
                              r.last_activity ? (
                                <>
                                  <div className="text-muted-foreground">Last connected</div>
                                  <div className="whitespace-nowrap text-caption text-muted-foreground">{formatDate(r.last_activity)}</div>
                                </>
                              ) : (
                                <div className="text-muted-foreground">No session in this period</div>
                              )
                            )}
                          </TD>
                          <TD numeric className="hidden sm:table-cell">{formatBytes(r.totals.bytes_down)}</TD>
                          <TD numeric className="hidden 2xl:table-cell">{formatBytes(r.totals.bytes_up)}</TD>
                          <TD numeric className="font-medium">{formatBytes(r.totals.bytes_total)}</TD>
                          <TD className="hidden lg:table-cell">
                            <LatestPackageCell row={r} />
                          </TD>
                          <TD pinEnd className="w-0 text-end">
                            <Button size="xs" variant="ghost" className="text-foreground"
                              onClick={(e) => { e.stopPropagation(); void openSource(r.source_type, r.source_id); }}
                              aria-label={`Details for ${title}`}>
                              Details <ChevronRight className="rtl:rotate-180" />
                            </Button>
                          </TD>
                        </TR>
                        );
                      })}
                    </TBody>
                  </Table>
                </div>
              )}
              {rows && rows.length > 0 && (offset > 0 || hasMore) && (
                <CardFooter>
                  <Pagination
                    offset={offset}
                    limit={USAGE_PAGE}
                    shown={rows.length}
                    hasMore={hasMore}
                    onChange={(o) => void search(typeFilter, o)}
                  />
                </CardFooter>
              )}
            </Card>
          )}

          {source && src && (
            <div className="space-y-5">
              <Button variant="ghost" size="sm" onClick={() => setSource(null)}>
                <ArrowLeft className="rtl:rotate-180" /> Back to access sources
              </Button>

              <Card>
                <CardHeader>
                  <div className="min-w-0 space-y-1">
                    <CardTitle className={cn(src.source_type === "voucher" && "font-mono")}>{sourceTitle(src)}</CardTitle>
                    {isStay ? (
                      <CardDescription>
                        {src.pms_interface}
                        {src.reservation ? ` · reservation ${src.reservation}` : ""}
                        {src.arrival ? ` · ${formatDate(src.arrival)}` : ""}
                        {src.departure ? ` → ${formatDate(src.departure)}` : ""}
                      </CardDescription>
                    ) : (
                      <CardDescription>
                        {SOURCE_LABEL[src.source_type]}
                        {src.last_activity ? ` · last connected ${formatDate(src.last_activity)}` : ""}
                      </CardDescription>
                    )}
                    {src.source_type === "voucher" && (
                      <div className="flex items-center gap-1.5 text-caption text-muted-foreground">
                        Card reference <MonoId value={src.source_id} title="Card reference" />
                      </div>
                    )}
                    {src.source_type === "open" && (
                      <div className="flex items-center gap-1.5 text-caption text-muted-foreground">
                        Access reference <MonoId value={src.source_id} title="Access reference" />
                      </div>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {isStay && src.stay_status && <Badge tone="neutral">{stayWords(src.stay_status)}</Badge>}
                    {!isStay && source.access_status && <Badge tone="neutral">{accessWords(source.access_status)}</Badge>}
                    {endReasonWords(src.end_reason) && (
                      <Badge tone="warn">{endReasonWords(src.end_reason)}</Badge>
                    )}
                  </div>
                </CardHeader>
                <CardBody className="space-y-4">
                  <MetricStrip
                    items={[
                      { label: "Downloaded", value: formatBytes(src.totals.bytes_down) },
                      { label: "Uploaded", value: formatBytes(src.totals.bytes_up) },
                      { label: "Total, all packages", value: formatBytes(src.totals.bytes_total) },
                      {
                        label: src.latest_package_name || src.latest_package_type
                          ? `Latest package: ${packageLabel(src.latest_package_name, src.latest_package_type)}`
                          : "Latest package",
                        value: src.latest_package_name || src.latest_package_type ? allowanceText(src.consumed_bytes, src.quota_bytes) : "—",
                      },
                    ]}
                  />
                  {source.from && source.to && (
                    <p className="text-caption text-muted-foreground">
                      Downloaded, uploaded and total cover every session from {formatDate(source.from)} to{" "}
                      {formatDate(source.to)}, under every package. The latest package shows its own allowance and
                      its own use only.
                    </p>
                  )}
                  <div className="flex flex-wrap gap-2">
                    {source.service_plan && <Badge tone="neutral">Service plan: {source.service_plan}</Badge>}
                    <Badge tone="default">{src.totals.sessions} sessions</Badge>
                    <Badge tone="default">{src.totals.devices} devices</Badge>
                  </div>
                </CardBody>
              </Card>

              {source.packages && source.packages.length > 0 && (
                <PackagesCard packages={source.packages} isStay={isStay} olderOmitted={!!source.older_packages_omitted} />
              )}

              <Card className="overflow-hidden">
                <CardHeader>
                  <CardTitle>{isStay ? "Devices used during this stay" : "Devices used under this access"}</CardTitle>
                </CardHeader>
                {source.devices.length === 0 ? (
                  <EmptyState size="sm" icon={<Smartphone />} title="No device recorded"
                    hint={isStay ? "Nothing connected under this stay's access." : "Nothing connected under this access."} />
                ) : (
                  <Table label="Devices">
                    <THead>
                      <TR>
                        <TH>Device</TH>
                        <TH numeric className="hidden sm:table-cell">Downloaded</TH>
                        <TH numeric className="hidden sm:table-cell">Uploaded</TH>
                        <TH numeric>Total</TH>
                        <TH numeric className="hidden sm:table-cell">Sessions</TH>
                        <TH pinEnd><span className="sr-only">Open</span></TH>
                      </TR>
                    </THead>
                    <TBody>
                      {source.devices.map((d) => (
                        <TR key={d.mac || "unknown"}>
                          <TD nowrap className="font-mono text-xs">{d.mac || "unknown"}</TD>
                          <TD numeric className="hidden sm:table-cell">{formatBytes(d.bytes_down)}</TD>
                          <TD numeric className="hidden sm:table-cell">{formatBytes(d.bytes_up)}</TD>
                          <TD numeric className="font-medium">{formatBytes(d.bytes_total)}</TD>
                          <TD numeric className="hidden sm:table-cell">{d.sessions}</TD>
                          <TD pinEnd className="w-0 text-end">
                            {d.mac && (
                              <Button size="xs" variant="ghost" className="text-foreground" onClick={() => lookUpDevice(d.mac)}>
                                This device <ChevronRight className="rtl:rotate-180" />
                              </Button>
                            )}
                          </TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                )}
              </Card>

              <SessionsCard sessions={source.sessions} samples={samples} onOpenSamples={openSamples} />
            </div>
          )}
        </TabsContent>

        <TabsContent value="devices" className="mt-5 space-y-5">
          <Card>
            <CardHeader><CardTitle>Look up a device</CardTitle></CardHeader>
            <CardBody className="space-y-3">
              <div role="search" className="flex flex-wrap gap-2">
                <SearchInput value={q} onChange={setQ} onSearch={(v) => { if (v.trim()) void lookUpDevice(v); }}
                  name="usage-device-lookup" id="usage-device-lookup" className="min-w-0 flex-1 font-mono sm:w-auto"
                  label="Device MAC address" placeholder="aa:bb:cc:dd:ee:ff" />
                <Button variant="secondary" disabled={busy || !q.trim()} onClick={() => void lookUpDevice(q)}>{busy ? "Looking…" : "Look up"}</Button>
              </div>
            </CardBody>
          </Card>

          {!device && !busy && (
            <EmptyState className="rounded-lg border border-dashed border-border" icon={<FileSearch />} title="Look up a device to see its usage"
              hint="Paste the device's MAC address from the handset or from Active sessions." />
          )}

          {device && (
            <>
              <Card>
                <CardHeader>
                  <div className="min-w-0 space-y-1">
                    <CardTitle className="font-mono">{device.mac}</CardTitle>
                    <CardDescription>
                      {formatDate(device.from)} → {formatDate(device.to)}
                      {device.first_seen ? ` · first seen ${formatDate(device.first_seen)}` : ""}
                    </CardDescription>
                  </div>
                </CardHeader>
                <CardBody>
                  <MetricStrip
                    items={[
                      { label: "Downloaded", value: formatBytes(device.totals.bytes_down) },
                      { label: "Uploaded", value: formatBytes(device.totals.bytes_up) },
                      { label: "Total", value: formatBytes(device.totals.bytes_total) },
                      { label: "Sessions", value: String(device.totals.sessions) },
                    ]}
                  />
                </CardBody>
              </Card>

              {device.stays.length > 0 && (
                <Card>
                  <CardHeader><CardTitle>Stays this device connected under</CardTitle></CardHeader>
                  <CardBody className="flex flex-wrap gap-2">
                    {device.stays.map((s) => (
                      <Button key={s.stay_id} size="sm" variant="secondary"
                        onClick={() => { setTab("sources"); openSource("stay", s.stay_id); }}>
                        Room {s.room || "—"} <span className="text-caption text-muted-foreground">{s.pms_interface}</span>
                      </Button>
                    ))}
                  </CardBody>
                </Card>
              )}

              <SessionsCard sessions={device.sessions} samples={samples} onOpenSamples={openSamples} />
            </>
          )}
        </TabsContent>
      </Tabs>
    </PageShell>
  );
}

/** Each package with its own meter, the one that enforced its allowance; oldest first, for the source's whole life. */
function PackagesCard({ packages, isStay, olderOmitted }: { packages: PackageRow[]; isStay: boolean; olderOmitted: boolean }) {
  return (
    <Card className="overflow-hidden">
      <CardHeader>
        <div className="space-y-1">
          <CardTitle>Packages</CardTitle>
          <CardDescription>
            {isStay ? "Every package this stay was given, oldest first." : "Every package this access was given, oldest first."}{" "}
            Used is the package&apos;s own meter, the figure its allowance is enforced against. It is not limited to the period above.
            {olderOmitted && ` Only the newest ${packages.length} are listed; older packages are not shown.`}
          </CardDescription>
        </div>
      </CardHeader>
      <Table label="Packages" aria-label="Packages">
        <THead>
          <TR>
            <TH>Package</TH>
            <TH className="hidden sm:table-cell">Started</TH>
            <TH className="hidden sm:table-cell">Ended</TH>
            <TH className="hidden md:table-cell">How it ended</TH>
            <TH className="hidden lg:table-cell">Allowance</TH>
            <TH numeric>Used</TH>
          </TR>
        </THead>
        <TBody>
          {packages.map((p) => {
            const pct = quotaPercent(p.used_bytes, p.quota_bytes);
            return (
              <TR key={p.entitlement_id}>
                <TD className="max-w-[16rem]">
                  <div className="truncate font-medium">{packageLabel(p.name, p.package_type)}</div>
                  <div className="text-caption text-muted-foreground">{accessWords(p.status)}</div>
                </TD>
                <TD nowrap className="hidden sm:table-cell">{p.activated_at ? formatDate(p.activated_at) : "—"}</TD>
                <TD nowrap className="hidden sm:table-cell">
                  {p.terminated_at ? formatDate(p.terminated_at) : p.status === "ACTIVE" ? <StatusBadge tone="ok">In use</StatusBadge> : "—"}
                </TD>
                <TD className="hidden text-sm md:table-cell">{endReasonWords(p.end_reason) ?? "—"}</TD>
                <TD className="hidden lg:table-cell">
                  {pct === null ? <span className="text-sm text-muted-foreground">No limit</span> : (
                    <Meter className="min-w-36" value={pct} max={100} caption={allowanceText(p.used_bytes, p.quota_bytes)} />
                  )}
                </TD>
                <TD numeric className="font-medium">{formatBytes(p.used_bytes)}</TD>
              </TR>
            );
          })}
        </TBody>
      </Table>
    </Card>
  );
}

/** A carried session, stretch by stretch; without exact samples it says so instead of dividing the total. */
function SessionPackages({ s }: { s: SessionRow }) {
  if (!s.packages || s.packages.length < 2) return null;
  return (
    <div className="mt-1 space-y-0.5 text-caption text-muted-foreground">
      {s.packages.map((p, i) => (
        <div key={`${p.entitlement_id}:${i}`}>
          {packageLabel(p.package_name, p.package_type)}
          {s.package_split_exact && p.bytes_total !== undefined ? `: ${formatBytes(p.bytes_total)}` : ""}
        </div>
      ))}
      {!s.package_split_exact && (
        <div>Ran under these packages. The detailed samples needed to split its total by package are no longer kept.</div>
      )}
    </div>
  );
}

/** Sample totals print next to the session total so the two can be seen to agree. */
function SessionsCard({ sessions, samples, onOpenSamples }: {
  sessions: SessionRow[];
  samples: Record<string, { bytes_total: number; sample_count: number; truncated?: boolean } | "loading">;
  onOpenSamples: (id: string) => void;
}) {
  return (
    <Card className="overflow-hidden">
      <CardHeader>
        <div className="space-y-1">
          <CardTitle>Sessions</CardTitle>
          <CardDescription>
            Each period of connected access, counted once. A device that remained connected through a package change
            keeps one session; its use is shown under each package it ran under.
          </CardDescription>
        </div>
      </CardHeader>
      {sessions.length === 0 ? (
        <EmptyState size="sm" icon={<Activity />} title="No sessions recorded"
          hint="Nothing was measured in this period. That is not the same as zero usage — it means no session exists." />
      ) : (
        <Table label="Sessions" aria-label="Sessions">
          <THead>
            <TR>
              <TH>Started</TH>
              <TH className="hidden sm:table-cell">Ended</TH>
              <TH className="hidden md:table-cell">Device</TH>
              <TH numeric>Total</TH>
              <TH className="hidden lg:table-cell">How it ended</TH>
              <TH pinEnd><span className="sr-only">Evidence</span></TH>
            </TR>
          </THead>
          <TBody>
            {sessions.map((s) => {
              const sm = samples[s.session_id];
              return (
                <TR key={s.session_id}>
                  <TD nowrap>{s.started ? formatDate(s.started) : "—"}</TD>
                  <TD nowrap className="hidden sm:table-cell">
                    {s.ended ? formatDate(s.ended) : <StatusBadge tone="ok">Still connected</StatusBadge>}
                  </TD>
                  <TD nowrap className="hidden font-mono text-xs md:table-cell">{s.mac || "—"}</TD>
                  <TD className="whitespace-nowrap text-end tabular">
                    {formatBytes(s.bytes_total)}
                    {sm && sm !== "loading" && (
                      <div className="text-caption text-muted-foreground">
                        {sm.truncated ? "The first " : ""}{sm.sample_count.toLocaleString()} samples · {formatBytes(sm.bytes_total)}
                        {sm.truncated ? " — the session has more; this is not its whole total" : ""}
                      </div>
                    )}
                    <SessionPackages s={s} />
                  </TD>
                  <TD className="hidden text-sm lg:table-cell">{endReasonWords(s.end_reason) ?? "—"}</TD>
                  <TD pinEnd className="w-0 text-end">
                    {!sm && (
                      <Button size="xs" variant="ghost" className="text-foreground" onClick={() => onOpenSamples(s.session_id)}>
                        Show evidence
                      </Button>
                    )}
                    {sm === "loading" && <span className="text-caption text-muted-foreground" aria-live="polite">Reading…</span>}
                  </TD>
                </TR>
              );
            })}
          </TBody>
        </Table>
      )}
    </Card>
  );
}
