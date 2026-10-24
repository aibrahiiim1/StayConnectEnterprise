"use client";

// Email, phone and social sign-ins are not listed as sources: their identifiers are not readable by this console's service.
// Nothing is estimated; a source with no measured usage says so rather than showing zero.
// Never show secrets: vouchers by card reference (not code), client accounts by username (not display name).
// Room words appear only on a Hotel room/stay source, always with its PMS connection.

import { useCallback, useEffect, useRef, useState } from "react";
import { Activity, ArrowLeft, ChevronRight, FileSearch, KeyRound, Smartphone } from "lucide-react";
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
type SessionRow = {
  session_id: string; mac?: string; ip?: string; credential_method?: string; state: string;
  started?: string; ended?: string; end_reason?: string;
  bytes_down: number; bytes_up: number; bytes_total: number;
};
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
};
type SourceDetail = {
  source: SourceRow; service_plan?: string; access_status?: string;
  devices: DeviceRow[]; sessions: SessionRow[];
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
  POST_STAY_ACTIVE: "Post-stay access",
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

function AllowanceCell({ consumed, quota }: { consumed?: number | null; quota?: number | null }) {
  const pct = quotaPercent(consumed, quota);
  if (pct === null) return <span className="text-sm text-muted-foreground">No limit</span>;
  return (
    <Meter
      className="min-w-56"
      value={Math.min(pct, 100)}
      max={100}
      label={pct >= 100 ? "Allowance used up" : undefined}
      caption={`${pct}% of ${formatBytes(quota)}`}
    />
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
  }, [q, typeFilter, range.qs, range.error]);

  useEffect(() => { if (tab === "sources" && rows === null) search(); }, [tab, rows, search]);

  async function openSource(type: SourceType, id: string) {
    setBusy(true); setErr(null);
    try { setSource(await api.get<SourceDetail>(`/usage/sources/${type}/${encodeURIComponent(id)}?${range.qs}`)); }
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
    try { setDevice(await api.get<DeviceDetail>(`/usage/devices/${encodeURIComponent(mac)}?${range.qs}`)); setTab("devices"); }
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
          onChange={(e) => { setPeriod(e.target.value as Period); setRows(null); setSource(null); setDevice(null); }}>
          {PERIODS.map((p) => <option key={p.value} value={p.value}>{p.label}</option>)}
        </Select>
        {period === "custom" && (
          <>
            <Input type="date" aria-label="From" className="w-full sm:w-44" value={fromDay} onChange={(e) => setFromDay(e.target.value)} />
            <Input type="date" aria-label="To" className="w-full sm:w-44" value={toDay} onChange={(e) => setToDay(e.target.value)} />
            <Button variant="secondary" disabled={!!range.error} onClick={() => { setRows(null); setSource(null); setDevice(null); }}>Apply</Button>
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

              {rows === null ? <SkeletonRows rows={4} cols={5} /> : rows.length === 0 ? (
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
                        <TH numeric className="hidden sm:table-cell">Uploaded</TH>
                        <TH numeric>Total</TH>
                        <TH className="hidden lg:table-cell">Allowance</TH>
                        <TH><span className="sr-only">Open</span></TH>
                      </TR>
                    </THead>
                    <TBody>
                      {rows.map((r) => (
                        <TR key={`${r.source_type}:${r.source_id}`} onClick={() => void openSource(r.source_type, r.source_id)}>
                          <TD nowrap>
                            <div className={cn("font-medium", r.source_type === "voucher" && "font-mono text-sm")}>
                              {sourceTitle(r)}
                            </div>
                            <div className="text-caption text-muted-foreground">
                              {r.source_type === "stay" ? `${SOURCE_LABEL.stay} · ${r.pms_interface ?? ""}` : SOURCE_LABEL[r.source_type]}
                            </div>
                          </TD>
                          <TD className="hidden text-sm md:table-cell">
                            {r.source_type === "stay" ? (
                              <>
                                <div>{r.reservation ? `Reservation ${r.reservation}` : "—"}</div>
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
                          <TD numeric className="hidden sm:table-cell">{formatBytes(r.totals.bytes_up)}</TD>
                          <TD numeric className="font-medium">{formatBytes(r.totals.bytes_total)}</TD>
                          <TD className="hidden lg:table-cell">
                            <AllowanceCell consumed={r.consumed_bytes} quota={r.quota_bytes} />
                          </TD>
                          <TD className="w-0 text-end">
                            <Button size="xs" variant="ghost" className="text-foreground"
                              onClick={(e) => { e.stopPropagation(); void openSource(r.source_type, r.source_id); }}
                              aria-label={`Details for ${sourceTitle(r)}`}>
                              Details <ChevronRight className="rtl:rotate-180" />
                            </Button>
                          </TD>
                        </TR>
                      ))}
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
                      { label: "Total used", value: formatBytes(src.totals.bytes_total) },
                      {
                        label: "Allowance",
                        value: src.quota_bytes
                          ? `${formatBytes(src.quota_bytes)}${quotaPercent(src.consumed_bytes, src.quota_bytes) !== null ? ` · ${quotaPercent(src.consumed_bytes, src.quota_bytes)}% used` : ""}`
                          : "No limit",
                        tone: (quotaPercent(src.consumed_bytes, src.quota_bytes) ?? 0) >= 100 ? "warn" : undefined,
                      },
                    ]}
                  />
                  {source.from && source.to && (
                    <p className="text-caption text-muted-foreground">
                      Totals for {formatDate(source.from)} – {formatDate(source.to)}, the period chosen above. The
                      allowance is the whole package&apos;s.
                    </p>
                  )}
                  <div className="flex flex-wrap gap-2">
                    {source.service_plan && <Badge tone="neutral">Service plan: {source.service_plan}</Badge>}
                    <Badge tone="default">{src.totals.sessions} sessions</Badge>
                    <Badge tone="default">{src.totals.devices} devices</Badge>
                  </div>
                </CardBody>
              </Card>

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
                        <TH><span className="sr-only">Open</span></TH>
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
                          <TD className="w-0 text-end">
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
          <CardDescription>Each period of connected access.</CardDescription>
        </div>
      </CardHeader>
      {sessions.length === 0 ? (
        <EmptyState size="sm" icon={<Activity />} title="No sessions recorded"
          hint="Nothing was measured in this period. That is not the same as zero usage — it means no session exists." />
      ) : (
        <Table label="Sessions">
          <THead>
            <TR>
              <TH>Started</TH>
              <TH className="hidden sm:table-cell">Ended</TH>
              <TH className="hidden md:table-cell">Device</TH>
              <TH numeric>Total</TH>
              <TH className="hidden lg:table-cell">How it ended</TH>
              <TH><span className="sr-only">Evidence</span></TH>
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
                  </TD>
                  <TD className="hidden text-sm lg:table-cell">{endReasonWords(s.end_reason) ?? "—"}</TD>
                  <TD className="w-0 text-end">
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
