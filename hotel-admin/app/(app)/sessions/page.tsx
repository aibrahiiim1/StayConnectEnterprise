"use client";

import { usePmsConnections } from "@/lib/pms-scope";
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { api, Session } from "@/lib/api";
import { usePoll } from "@/lib/use-poll";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { useOperatorRoles } from "@/lib/whoami-context";
import { canWrite } from "@/lib/roles";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { RowActions } from "@/components/ui/row-actions";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardFooter } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Segmented } from "@/components/ui/tabs";
import { ConfirmDialog } from "@/components/ui/dialog";
import { DetailsDrawer } from "@/components/ui/details-drawer";
import { PackageControls } from "@/components/package-controls";
import { Explain } from "@/components/ui/tooltip";
import { DList, Meter, MonoId, Metric, SkeletonRows } from "@/components/ui/misc";
import { Pagination, ResultsCount, SearchInput, FilterChips } from "@/components/ui/data";
import { LiveStatus, ReadOnlyNotice, refreshingClass } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn } from "@/lib/utils";
import { formatBytes, formatRelative, formatDate } from "@/lib/utils";
import {
  identifySession, methodLabel, stateWords, endReasonWords, speedPair, acquisitionOrdinal,
} from "@/lib/session-words";
import { moduleHasHistory, moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { AlertTriangle, Users, Monitor, ArrowDownUp, Hotel, KeyRound, Ticket, UserCircle, Power, PackageOpen, RefreshCw } from "lucide-react";

type Tab = "active" | "recent";

const KIND_ICON = {
  room: Hotel,
  account: KeyRound,
  voucher: Ticket,
  guest: UserCircle,
  open: PackageOpen,
  "": Monitor,
} as const;

const secs = (n?: number | null) => {
  if (typeof n !== "number" || n < 0) return "—";
  if (n === 0) return "0m";
  const h = Math.floor(n / 3600);
  const m = Math.floor((n % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${n}s`;
};

const ACCESS_WORDS: Record<string, string> = {
  PENDING: "Access not started",
  SUSPENDED: "Access paused",
  TERMINATED: "Access ended",
  EXPIRED: "Access expired",
  REVOKED: "Access withdrawn",
};

function accessWords(status: string): string {
  const known = ACCESS_WORDS[status.toUpperCase()];
  if (known) return known;
  const w = status.toLowerCase().replace(/_/g, " ");
  return `Access ${w}`;
}

const STUCK_AFTER_MS = 15_000;

type SessionsResp = PagedFields & {
  data?: Session[];
  summary?: {
    devices_online?: number;
    clients_online?: number;
    rooms_online?: number;
    bytes_total?: number;
    kinds?: Record<string, number>;
  };
};

export default function SessionsPage() {
  const [tab, setTab] = useState<Tab>("active");
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState<string>("");
  // Ended sessions: the last 30 days by default, the whole history on request (audit AD-56).
  const [history, setHistory] = useState<"recent" | "all">("recent");
  const [detailSnap, setDetail] = useState<Session | null>(null);
  const [disconnecting, setDisconnecting] = useState<ReadonlyMap<string, string>>(() => new Map());
  const [roomsSeen, setRoomsSeen] = useState(false);
  const [confirm, setConfirm] = useState<Session | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const toast = useToast();
  const { multi } = usePmsConnections();

  // Disconnect is offered only to roles edged accepts it from; fails closed while roles load.
  const roles = useOperatorRoles();
  const mayDisconnect = roles === null ? false : canWrite("sessions", roles);

  // Paging, filtering and search are server-side. The search term goes in a header, not the URL, because it can be
  // a guest's name and edged logs request lines.
  const path = useMemo(() => {
    const p = new URLSearchParams();
    if (tab === "active") p.set("state", "active");
    if (kind) p.set("kind", kind);
    if (tab !== "active" && history === "all") p.set("window", "all");
    const s = p.toString();
    return s ? `/sessions?${s}` : "/sessions";
  }, [tab, kind, history]);
  const needle = query.trim();
  const headers = useMemo(
    () => (needle ? { "X-Session-Search": encodeURIComponent(needle) } : undefined),
    [needle],
  );
  const list = useServerPage<SessionsResp>({ path, headers });
  const { resp, current, err, loading: refreshing } = list;

  // Unchanged sessions keep object identity across polls so memoised rows re-render only on change.
  // null shows the skeleton for a new query; a failed first load yields an empty list.
  const prevRows = useRef<Session[] | null>(null);
  const failed = !!err && !current;
  const rows = useMemo(() => {
    if (!current || !resp) return null;
    const next = keepUnchanged(prevRows.current, resp.data ?? []);
    prevRows.current = next;
    return next;
  }, [current, resp]);
  const rowsRef = useRef<Session[] | null>(null);
  useEffect(() => { rowsRef.current = rows; }, [rows]);
  useEffect(() => { if (current && resp) setUpdatedAt(Date.now()); }, [current, resp]);

  usePoll(() => list.reload(), 10_000, { enabled: tab === "active" });

  const detailId = detailSnap?.id ?? null;
  const liveRow = detailId ? rows?.find((r) => r.id === detailId) : undefined;
  const detail = liveRow ?? detailSnap;
  useEffect(() => {
    if (!detailId || liveRow) return;
    let stale = false;
    api.get<Session>(`/sessions/${detailId}`)
      .then((s) => { if (!stale) setDetail((cur) => (cur && cur.id === s.id ? s : cur)); })
      .catch(() => {});
    return () => { stale = true; };
  }, [detailId, liveRow, updatedAt]);

  useEffect(() => {
    if (disconnecting.size === 0 || !rows) return;
    const still = new Map<string, string>();
    for (const [id, title] of disconnecting) {
      if (rows.some((r) => r.id === id && r.state === "active")) still.set(id, title);
      else toast.success(`${title} — device disconnected.`);
    }
    if (still.size !== disconnecting.size) setDisconnecting(still);
  }, [rows, disconnecting, toast]);

  const pendingRef = useRef(disconnecting);
  useEffect(() => { pendingRef.current = disconnecting; }, [disconnecting]);
  const giveUp = useCallback((id: string, title: string) => {
    if (!pendingRef.current.has(id)) return;
    setDisconnecting((cur) => {
      const next = new Map(cur);
      next.delete(id);
      return next;
    });
    if (rowsRef.current?.some((r) => r.id === id && r.state === "active")) {
      toast.error(`${title} is still online.`, "The disconnect did not take effect. Try again, or look in Client activity.");
    }
  }, [toast]);

  async function onDisconnect(s: Session) {
    setBusy(true); setActionErr(null);
    try {
      await api.post(`/sessions/${s.id}/disconnect`, { reason: "admin" });
      const title = identifySession(s, multi).title;
      toast.toast({ tone: "info", title: `Disconnecting ${title}'s device…` });
      setConfirm(null);
      setDetail(null);
      setDisconnecting((cur) => new Map(cur).set(s.id, title));
      setTimeout(() => giveUp(s.id, title), STUCK_AFTER_MS);
      // scd enforces asynchronously; reloading immediately would still show the session active.
      setTimeout(() => void list.reload(), 600);
      setTimeout(() => void list.reload(), 2500);
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusy(false);
    }
  }

  const openDetail = useCallback((s: Session) => { setDetail(s); setActionErr(null); }, []);
  const askDisconnect = useCallback((s: Session) => { setConfirm(s); setActionErr(null); }, []);

  // Tiles are server counts for the whole tab, unaffected by search or sign-in type.
  const tiles = resp?.summary;
  const summary = {
    devices: tiles?.devices_online ?? 0,
    guests: tiles?.clients_online ?? 0,
    rooms: tiles?.rooms_online ?? 0,
    bytes: tiles?.bytes_total ?? 0,
  };
  const kindCounts: Record<string, number> = tiles?.kinds ?? {};
  const haveSummary = !!tiles;
  const searching = needle !== "" || kind !== "";
  useEffect(() => { if (summary.rooms > 0) setRoomsSeen(true); }, [summary.rooms]);
  const clearFilters = () => { setQuery(""); setKind(""); };

  // Room / email-social options show only where the module is licensed or has history, or rows of that kind
  // exist (a filter must never hide existing rows). Unknown module state hides them.
  const caps = useCapabilities();
  const rooms = moduleHasHistory(caps, "hospitality") || (kindCounts.room ?? 0) > 0;
  const identity = ["email_otp", "sms_otp", "whatsapp_otp", "social_login"].some((m) => moduleLicensed(caps, m))
    || (kindCounts.guest ?? 0) > 0;
  const grantedTo = rooms ? "one room, account or voucher" : "one account or voucher";

  return (
    <PageShell width="wide">
      <PageHeader
        title="Active sessions"
        helpTitle="Sessions"
        description={tab === "active" ? "Which devices are online, and whose they are." : "Sessions that are online or ended recently, and whose they were."}
        help={
          <>
            <HelpSection title="Sessions, devices and clients">
              <p>
                A session is one device; a client may have several. A client is counted by what the internet was
                granted to &mdash; {grantedTo} &mdash; so a family with four devices is one client.
              </p>
            </HelpSection>
            <HelpSection title="On this page">
              <HelpList items={[
                <><strong>Online now</strong> refreshes every 10 seconds; <strong>Recent</strong> also includes sessions that have ended.</>,
                `Search by ${rooms ? "room, " : ""}name, username, IP or MAC, or filter by how the client signed in.`,
                "Open a session for its details, or disconnect a device. A disconnected client can sign in again.",
              ]} />
            </HelpSection>
          </>
        }
        actions={
          <Segmented
            label="Which sessions"
            value={tab}
            onChange={(v) => setTab(v)}
            options={[
              { value: "active", label: "Online now" },
              { value: "recent", label: "Recent" },
            ]}
          />
        }
      />

      <div className="flex flex-wrap items-center justify-between gap-2">
        {failed && updatedAt === null ? <span /> : (
          <LiveStatus
            updatedAt={updatedAt}
            refreshing={refreshing}
            intervalSeconds={tab === "active" ? 10 : undefined}
            error={!!err}
            onRefresh={() => void list.reload()}
          />
        )}
        {roles !== null && !mayDisconnect && (
          <ReadOnlyNotice className="py-1.5">Your role can see who is online but not disconnect a device.</ReadOnlyNotice>
        )}
      </div>

      <StatGrid cols={4}>
        <StatCard
          label="Devices online"
          value={haveSummary ? summary.devices.toLocaleString() : "—"}
          icon={<Monitor />}
        />
        <StatCard
          label="Clients online"
          value={haveSummary ? summary.guests.toLocaleString() : "—"}
          icon={<Users />}
          explain={
            <Explain>
              Counted by what the internet was granted to — {grantedTo} — so a family with four devices is one
              client.
            </Explain>
          }
        />
        {roomsSeen && (
          <div className="animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none">
            <StatCard
              label="Rooms online"
              value={haveSummary ? summary.rooms.toLocaleString() : "—"}
              icon={<Hotel />}
              hint="Signed in with a room number"
            />
          </div>
        )}
        <StatCard
          label={tab === "active" ? "Data, all sessions online now" : "Data, all recent sessions"}
          value={haveSummary ? formatBytes(summary.bytes) : "—"}
          icon={<ArrowDownUp />}
          hint={searching ? "Not narrowed by the search or filter" : undefined}
        />
      </StatGrid>

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={
            <SearchInput
              value={query}
              onChange={setQuery}
              placeholder={rooms ? "Room, name, username, IP or MAC…" : "Name, username, IP or MAC…"}
              label="Search sessions"
              delay={300}
            />
          }
          actions={<ResultsCount count={list.total ?? rows?.length} noun="session" more={list.total == null && list.hasMore} />}
        >
              <FilterChips<string>
                label="How the client signed in"
                value={kind || "all"}
                onChange={(v) => setKind(v === "all" ? "" : v)}
                options={[
                  { value: "all", label: "All" },
                  ...(rooms ? [{ value: "room", label: "Room", count: resp ? kindCounts.room ?? 0 : undefined }] : []),
                  { value: "account", label: "Account", count: resp ? kindCounts.account ?? 0 : undefined },
                  { value: "voucher", label: "Voucher", count: resp ? kindCounts.voucher ?? 0 : undefined },
                  ...(identity ? [{ value: "guest", label: "Email / social", count: resp ? kindCounts.guest ?? 0 : undefined }] : []),
                  { value: "open", label: "No sign-in", count: resp ? kindCounts.open ?? 0 : undefined },
                ]}
              />
              {tab !== "active" && (
                <Select
                  value={history}
                  onChange={(e) => setHistory(e.target.value === "all" ? "all" : "recent")}
                  aria-label="How far back to list ended sessions"
                  className="w-full sm:w-44"
                >
                  <option value="recent">Last 30 days</option>
                  <option value="all">All history</option>
                </Select>
              )}
              {(query || kind) && (
                <Button variant="ghost" size="sm" onClick={clearFilters}>
                  Clear
                </Button>
              )}
        </FilterBar>

        <div className={cn(refreshing && rows !== null && refreshingClass)}>
          {failed ? (
            <EmptyState
              icon={<AlertTriangle />}
              title="The sessions could not be loaded"
              hint={err instanceof Error ? err.message : undefined}
              action={<Button variant="secondary" disabled={refreshing} onClick={() => void list.reload()}><RefreshCw /> Try again</Button>}
            />
          ) : rows === null ? (
            <SkeletonRows rows={6} cols={6} />
          ) : rows.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<Monitor />}
              title={
                searching
                  ? "No session matches this filter"
                  : tab === "active"
                    ? "Nobody is online"
                    : "No recent sessions"
              }
              hint={
                searching
                  ? "Try a different search, or clear the filters."
                  : tab === "active"
                    ? "Connected clients appear here as soon as they sign in."
                    : history === "recent"
                      ? "Only the last 30 days are listed. Choose All history to look further back."
                      : undefined
              }
              action={searching ? <Button variant="secondary" size="sm" onClick={clearFilters}>Clear filters</Button> : undefined}
            />
          ) : (
            <Table label="Sessions">
              <THead>
                <TR>
                  <TH>Client</TH>
                  <TH className="hidden md:table-cell">Signed in with</TH>
                  <TH className="hidden lg:table-cell">Package</TH>
                  <TH className="hidden xl:table-cell">Allowance used</TH>
                  <TH className="hidden lg:table-cell">Network / device</TH>
                  <TH numeric className="hidden sm:table-cell">Data (down / up)</TH>
                  <TH>Status</TH>
                  {roles !== null && <TH><span className="sr-only">Actions</span></TH>}
                </TR>
              </THead>
              <TBody>
                {rows.map((s) => (
                  <SessionRow
                    key={s.id}
                    s={s}
                    pending={disconnecting.has(s.id)}
                    mayDisconnect={mayDisconnect}
                    showActions={roles !== null}
                    onOpen={openDetail}
                    onDisconnect={askDisconnect}
                  />
                ))}
              </TBody>
            </Table>
          )}
        </div>
        {rows && rows.length > 0 && (list.offset > 0 || list.hasMore) && (
          <CardFooter>
            <Pagination
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

      <DetailsDrawer
        open={detail !== null}
        onOpenChange={(v) => !v && setDetail(null)}
        eyebrow="Session"
        title={detail ? identifySession(detail, multi).title : ""}
        description={detail ? identifySession(detail, multi).subtitle : undefined}
        actions={
          detail?.state === "active" && mayDisconnect ? (
            <>
              <Button variant="secondary" onClick={() => setDetail(null)}>Close</Button>
              <Button variant="danger" onClick={() => { setConfirm(detail); }}>
                <Power /> Disconnect this device
              </Button>
            </>
          ) : undefined
        }
      >
        {detail && (
          <div className="space-y-5">
            <SessionDetail s={detail} />
            <section className="space-y-2">
              <h3 className="text-sm font-semibold">The package this device is on</h3>
              <PackageControls subject={{ kind: "session", id: detail.id }} />
            </section>
          </div>
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(v) => !v && setConfirm(null)}
        title={confirm ? `Disconnect ${identifySession(confirm, multi).title}'s device?` : "Disconnect this device?"}
        description="This device loses internet access now. The client can sign in again."
        confirmLabel="Disconnect"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        onConfirm={() => { if (confirm) void onDisconnect(confirm); }}
      >
        {confirm && (
          <div className="rounded-md border border-border bg-surface/60 p-3 text-xs">
            <DList
              columns={1}
              items={[
                { label: "Device", value: <span className="font-mono">{confirm.mac}</span> },
                { label: "Address", value: <span className="font-mono">{confirm.ip}</span> },
                { label: "Network", value: confirm.guest_network_name ?? "Not recorded" },
                {
                  label: "Other devices on this client",
                  value:
                    typeof confirm.active_devices === "number" && confirm.active_devices > 1
                      ? `${confirm.active_devices - 1} will stay online`
                      : "None",
                },
              ]}
            />
          </div>
        )}
      </ConfirmDialog>
    </PageShell>
  );
}

// Memoised: relies on `keepUnchanged` preserving object identity across polls.
const SessionRow = memo(function SessionRow({
  s, pending, mayDisconnect, showActions, onOpen, onDisconnect,
}: {
  s: Session;
  pending: boolean;
  mayDisconnect: boolean;
  showActions: boolean;
  onOpen: (s: Session) => void;
  onDisconnect: (s: Session) => void;
}) {
  const { multi } = usePmsConnections();
  const id = identifySession(s, multi);
  const Icon = KIND_ICON[(s.subject_kind ?? "") as keyof typeof KIND_ICON] ?? Monitor;
  const st = stateWords(s.state);
  return (
    <TR onClick={() => onOpen(s)}>
      <TD className="max-w-[18rem]">
        <div className="flex items-start gap-2.5">
          <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-md bg-surface text-muted-foreground" aria-hidden>
            <Icon className="size-3.5" />
          </span>
          <span className="min-w-0">
            <span
              className={
                id.anonymous
                  ? "block truncate font-mono text-xs text-muted-foreground"
                  : "block truncate font-medium"
              }
              title={id.anonymous ? `${id.title} — no client record on this session` : id.title}
            >
              {id.title}
            </span>
            {id.subtitle && (
              <span className="block truncate text-xs text-muted-foreground" title={id.subtitle}>{id.subtitle}</span>
            )}
            <span className="block truncate text-2xs text-muted-foreground lg:hidden">
              <span className="font-mono">{s.ip}</span>
              {s.guest_network_name ? ` · ${s.guest_network_name}` : ""}
            </span>
            {id.anonymous && <span className="sr-only">No client record on this session</span>}
          </span>
        </div>
      </TD>
      <TD nowrap className="hidden text-muted-foreground md:table-cell">
        <div className="text-xs">{methodLabel(s.credential_method)}</div>
        <div className="text-2xs">{formatRelative(s.started_at)}</div>
      </TD>
      <TD nowrap className="hidden max-w-[13rem] lg:table-cell">
        {s.package_name || s.package_code ? (
          <>
            <div className="flex items-center gap-1.5 text-sm">
              <span className="min-w-0 truncate" title={s.package_name || s.package_code || undefined}>{s.package_name || s.package_code}</span>
              {acquisitionOrdinal(s.package_acquisitions) && (
                <Badge tone="neutral">
                  {acquisitionOrdinal(s.package_acquisitions)}
                  <span className="sr-only"> this client has had this package</span>
                </Badge>
              )}
            </div>
            <div className="text-2xs text-muted-foreground">
              {speedPair(s.down_kbps, s.up_kbps) ?? s.service_plan_code ?? ""}
            </div>
          </>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        )}
      </TD>
      <TD className="hidden min-w-40 xl:table-cell">
        <AllowanceCell s={s} />
      </TD>
      <TD nowrap className="hidden max-w-[11rem] lg:table-cell">
        <div className="truncate text-xs" title={s.guest_network_name ?? undefined}>{s.guest_network_name ?? "—"}</div>
        <div className="font-mono text-2xs text-muted-foreground">{s.ip}</div>
      </TD>
      <TD numeric className="hidden sm:table-cell">
        {formatBytes(s.bytes_down)}
        <span className="text-muted-foreground"> / {formatBytes(s.bytes_up)}</span>
      </TD>
      <TD nowrap>
        {pending && s.state === "active" ? (
          <StatusBadge tone="warn" className="motion-safe:[&>span:first-child]:animate-pulse">Disconnecting…</StatusBadge>
        ) : (
          <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
        )}
        {s.end_reason && (
          <div className="mt-0.5 text-2xs text-muted-foreground">
            {endReasonWords(s.end_reason)}
          </div>
        )}
      </TD>
      {showActions && (
        <TD className="w-0">
          <RowActions
            subject={id.title}
            items={[
              { label: "Details", onSelect: () => onOpen(s) },
              ...(mayDisconnect && s.state === "active" && !pending
                ? [{ label: "Disconnect this device", icon: <Power />, danger: true, onSelect: () => onDisconnect(s) }]
                : []),
            ]}
          />
        </TD>
      )}
    </TR>
  );
});

function keepUnchanged(prev: Session[] | null, next: Session[]): Session[] {
  if (!prev || prev.length === 0) return next;
  const before = new Map(prev.map((p) => [p.id, p]));
  return next.map((n) => {
    const p = before.get(n.id);
    return p && JSON.stringify(p) === JSON.stringify(n) ? p : n;
  });
}

function AllowanceCell({ s }: { s: Session }) {
  const hasData = typeof s.data_quota_bytes === "number" && s.data_quota_bytes > 0;
  const hasTime = typeof s.time_quota_seconds === "number" && s.time_quota_seconds > 0;
  if (!hasData && !hasTime) {
    return <span className="text-xs text-muted-foreground">Unlimited</span>;
  }
  return (
    <div className="space-y-1.5">
      {hasData && (
        <Meter
          value={s.data_used_bytes ?? 0}
          max={s.data_quota_bytes!}
          label="Data"
          caption={`${formatBytes(s.data_used_bytes ?? 0)} / ${formatBytes(s.data_quota_bytes!)}`}
        />
      )}
      {hasTime && (
        <Meter
          value={s.time_used_seconds ?? 0}
          max={s.time_quota_seconds!}
          label="Time"
          caption={`${secs(s.time_used_seconds)} / ${secs(s.time_quota_seconds)}`}
        />
      )}
    </div>
  );
}

function SessionDetail({ s }: { s: Session }) {
  const { multi } = usePmsConnections();
  const id = identifySession(s, multi);
  const st = stateWords(s.state);
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
        <Badge tone="neutral">{id.kindLabel}</Badge>
        {s.entitlement_status && s.entitlement_status !== "ACTIVE" && (
          <Badge tone="warn">{accessWords(s.entitlement_status)}</Badge>
        )}
        {typeof s.active_devices === "number" && (
          <span className="text-xs text-muted-foreground">
            {s.active_devices} device{s.active_devices === 1 ? "" : "s"} online for this client
            {typeof s.max_devices === "number" && s.max_devices > 0 && ` of ${s.max_devices} allowed`}
          </span>
        )}
      </div>

      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <Metric label="Downloaded" value={formatBytes(s.bytes_down)} />
        <Metric label="Uploaded" value={formatBytes(s.bytes_up)} />
        <Metric label="Started" value={<span className="text-sm">{formatRelative(s.started_at)}</span>} />
        <Metric
          label={s.ended_at ? "Ended" : "Access ends"}
          value={
            <span className="text-sm">
              {s.ended_at ? formatRelative(s.ended_at) : s.expires_at ? formatRelative(s.expires_at) : "—"}
            </span>
          }
        />
      </div>

      <AllowanceCell s={s} />

      <DList
        items={[
          { label: "Signed in with", value: methodLabel(s.credential_method) },
          { label: "Client network", value: s.guest_network_name ?? s.ingress_interface ?? "—" },
          { label: "IP address", value: <span className="font-mono text-xs">{s.ip}</span> },
          { label: "Device (MAC)", value: <span className="font-mono text-xs">{s.mac}</span> },
          {
            label: "Internet package",
            value: s.package_name || s.package_code ? (
              <>
                {s.package_name || s.package_code}
                {" · "}
                <Link href="/internet-packages" className="text-primary underline underline-offset-2 hover:decoration-2">
                  All packages
                </Link>
              </>
            ) : "—",
          },
          {
            label: "Times this client had this package",
            value: typeof s.package_acquisitions === "number" && s.package_acquisitions > 0
              ? `${s.package_acquisitions}${s.package_acquisitions === 1 ? " (first time)" : ""}`
              : "—",
          },
          { label: "Speed", value: speedPair(s.down_kbps, s.up_kbps) ?? "—" },
          ...(s.subject_kind === "room"
            ? [
                { label: "Room", value: s.room ?? "—" },
                {
                  label: "Reservation",
                  value: s.external_reservation_id ? (
                    <span className="font-mono text-xs">{s.external_reservation_id}</span>
                  ) : "—",
                },
                {
                  label: "Stay",
                  value: s.stay_id ? (
                    <Link href={`/stays?stay=${encodeURIComponent(s.stay_id)}`} className="text-primary underline underline-offset-2 hover:decoration-2">Open this stay</Link>
                  ) : "—",
                },
              ]
            : []),
          { label: "Started at", value: <span className="text-xs">{formatDate(s.started_at)}</span> },
          ...(s.end_reason ? [{ label: "Ended because", value: endReasonWords(s.end_reason) }] : []),
        ]}
      />

      <section className="space-y-2">
        <h3 className="text-sm font-semibold">For support</h3>
        <DList
          items={[
            { label: "Service plan", value: s.service_plan_code ? <span className="font-mono text-xs">{s.service_plan_code}</span> : "—" },
            { label: "Session id", value: <MonoId value={s.id} title="Session" /> },
          ]}
        />
      </section>
    </>
  );
}
