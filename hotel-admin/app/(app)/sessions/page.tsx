"use client";

// ACTIVE SESSIONS — who is online, in the terms the front desk uses.
//
// The screen used to be six columns: IP/MAC, state, started, last activity, down/up, and a Disconnect button.
// Every column was true and the screen still could not do its job, because the first one answered "which
// network card" when the only question anyone brings here is "which guest".
//
// So: each row now leads with the room, account or voucher the session belongs to, carries the package and
// speed it was granted, shows what is left of the allowance, and says which guest network the device is on. The
// MAC and IP are still present — a network engineer needs them — as secondary detail rather than as the identity.
//
// It also gained the things a list of this kind needs to be usable at all: a search box (a full hotel produces
// more rows than anyone scrolls), summary tiles, a per-row detail panel, and a disconnect confirmation that
// names who is about to be cut off.

import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { api, Session } from "@/lib/api";
import { usePoll } from "@/lib/use-poll";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { useOperatorRoles } from "@/lib/whoami-context";
import { canWrite } from "@/lib/roles";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Segmented } from "@/components/ui/tabs";
import { ConfirmDialog, DetailDialog } from "@/components/ui/dialog";
import { Explain } from "@/components/ui/tooltip";
import { DList, Meter, MonoId, Metric, SkeletonRows } from "@/components/ui/misc";
import { Pagination, SearchInput } from "@/components/ui/data";
import { LiveStatus, ReadOnlyNotice, refreshingClass } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn } from "@/lib/utils";
import { formatBytes, formatRelative, formatDate, errMsg } from "@/lib/utils";
import {
  identifySession, methodLabel, stateWords, endReasonWords, speedPair,
} from "@/lib/session-words";
import { moduleHasHistory, moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { Users, Monitor, ArrowDownUp, Hotel, KeyRound, Ticket, UserCircle, Power, PackageOpen } from "lucide-react";

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
  if (typeof n !== "number" || n <= 0) return "—";
  const h = Math.floor(n / 3600);
  const m = Math.floor((n % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${n}s`;
};

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
  const [detail, setDetail] = useState<Session | null>(null);
  const [confirm, setConfirm] = useState<Session | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const toast = useToast();

  // DISCONNECT IS OFFERED ONLY TO A ROLE THAT CAN USE IT. site_viewer, payments_operator and voucher_operator
  // read this list; edged refuses them the disconnect. A button that 403s on click was the redesign handoff's
  // complaint about this screen. Fails closed while the roles load.
  const roles = useOperatorRoles();
  const mayDisconnect = roles === null ? false : canWrite("sessions", roles);

  // PAGED, FILTERED AND SEARCHED ON THE SERVER. edged used to return the newest 200 sessions and this screen
  // searched, filtered and counted those, so on a busy evening an earlier device was simply not findable and
  // the tiles counted what had loaded. The search goes in a header: it is a room, a username or a guest's name,
  // and edged logs request lines. A new tab, filter or search starts again at the first page.
  const path = useMemo(() => {
    const p = new URLSearchParams();
    if (tab === "active") p.set("state", "active");
    if (kind) p.set("kind", kind);
    const s = p.toString();
    return s ? `/sessions?${s}` : "/sessions";
  }, [tab, kind]);
  const needle = query.trim();
  const headers = useMemo(
    () => (needle ? { "X-Session-Search": encodeURIComponent(needle) } : undefined),
    [needle],
  );
  const list = useServerPage<SessionsResp>({ path, headers });
  const { resp, current, err, loading: refreshing } = list;

  // The rows of the page on screen. An unchanged session stays the same object across polls, so the memoised
  // rows below re-render only where a figure actually moved. A new question shows the skeleton, not the old
  // question's rows; a failed first load shows an empty list under the error.
  const prevRows = useRef<Session[] | null>(null);
  const rows = useMemo(() => {
    if (!current || !resp) return err && !resp ? [] : null;
    const next = keepUnchanged(prevRows.current, resp.data ?? []);
    prevRows.current = next;
    return next;
  }, [current, resp, err]);
  useEffect(() => { if (current && resp) setUpdatedAt(Date.now()); }, [current, resp]);

  // Paused while the tab is hidden; an operator switching back gets a fresh page at once.
  usePoll(() => void list.reload(), 10_000, { enabled: tab === "active" });

  async function onDisconnect(s: Session) {
    setBusy(true); setActionErr(null);
    try {
      await api.post(`/sessions/${s.id}/disconnect`, { reason: "admin" });
      toast.success(`${identifySession(s).title} — device disconnected.`);
      setConfirm(null);
      setDetail(null);
      // scd enforces asynchronously, so an immediate reload can still show the session as active. The short
      // delay is not cosmetic: without it the operator sees their own action appear not to have worked.
      setTimeout(() => void list.reload(), 600);
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusy(false);
    }
  }

  const openDetail = useCallback((s: Session) => { setDetail(s); setActionErr(null); }, []);
  const askDisconnect = useCallback((s: Session) => { setConfirm(s); setActionErr(null); }, []);

  // THE TILES DESCRIBE EVERY SESSION IN THE TAB, counted by the server -- not the page on screen, and not
  // narrowed by the search or the sign-in type, so they stay put while the operator narrows the table.
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

  // WHICH SIGN-IN TYPES THIS SITE CAN HAVE. Rooms are a Hotel idea: they are offered as a filter, a search
  // term and a word in the help only where hospitality is licensed or has left records -- or where a room
  // session is actually in the list, because a filter must never hide rows that exist. Email / social follows
  // the identity modules the same way. Unknown module state hides both (fail closed); the rows themselves are
  // data and always shown as they are.
  const caps = useCapabilities();
  const rooms = moduleHasHistory(caps, "hospitality") || (kindCounts.room ?? 0) > 0;
  const identity = ["email_otp", "sms_otp", "whatsapp_otp", "social_login"].some((m) => moduleLicensed(caps, m))
    || (kindCounts.guest ?? 0) > 0;
  const grantedTo = rooms ? "one room, account or voucher" : "one account or voucher";

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Clients"
        title="Active sessions"
        icon={<Monitor />}
        description="Which devices are online, and whose they are."
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
            onChange={(v) => { setTab(v); setQuery(""); }}
            options={[
              { value: "active", label: "Online now" },
              { value: "recent", label: "Recent" },
            ]}
          />
        }
      />

      <div className="flex flex-wrap items-center justify-between gap-2">
        <LiveStatus
          updatedAt={updatedAt}
          refreshing={refreshing && rows !== null}
          intervalSeconds={tab === "active" ? 10 : undefined}
          error={!!err && rows !== null && rows.length > 0}
          onRefresh={() => void list.reload()}
        />
        {roles !== null && !mayDisconnect && (
          <ReadOnlyNotice className="py-1.5">Your role can see who is online but not disconnect a device.</ReadOnlyNotice>
        )}
      </div>

      <ErrorBanner err={err} />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="Devices online"
          value={haveSummary ? summary.devices.toLocaleString() : "—"}
          icon={<Monitor />}
          tone="primary"
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
        {/* Rooms are a Hotel idea: the tile appears only when somebody is actually signed in with a room. */}
        {haveSummary && summary.rooms > 0 && (
          <StatCard
            label="Rooms online"
            value={summary.rooms.toLocaleString()}
            icon={<Hotel />}
            hint="Distinct rooms signed in with a room number"
          />
        )}
        <StatCard
          label="Data in this list"
          value={haveSummary ? formatBytes(summary.bytes) : "—"}
          icon={<ArrowDownUp />}
          hint={tab === "active" ? "Recorded against every session online now" : "Recorded against every session in this list"}
        />
      </div>

      <Card>
        <CardBody className="border-b border-border py-3">
          <Toolbar>
            <SearchInput
              value={query}
              onChange={setQuery}
              placeholder={rooms ? "Room, name, username, IP or MAC…" : "Name, username, IP or MAC…"}
              label="Search sessions"
              className="sm:max-w-xs"
              delay={300}
            />
            <div className="flex items-end gap-2">
              <Select
                value={kind}
                onChange={(e) => setKind(e.target.value)}
                aria-label="Filter by how the client signed in"
                className="w-full sm:w-52"
              >
                <option value="">All sign-in types</option>
                {rooms && <option value="room">Room ({kindCounts.room ?? 0})</option>}
                <option value="account">Account ({kindCounts.account ?? 0})</option>
                <option value="voucher">Voucher ({kindCounts.voucher ?? 0})</option>
                {identity && <option value="guest">Email / social ({kindCounts.guest ?? 0})</option>}
                <option value="open">Without sign-in ({kindCounts.open ?? 0})</option>
              </Select>
              {(query || kind) && (
                <Button variant="ghost" size="sm" onClick={() => { setQuery(""); setKind(""); }}>
                  Clear
                </Button>
              )}
            </div>
          </Toolbar>
        </CardBody>

        <CardBody className={cn("p-0", refreshing && rows !== null && refreshingClass)}>
          {rows === null ? (
            <SkeletonRows rows={6} cols={6} />
          ) : rows.length === 0 ? (
            <EmptyState
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
                    : undefined
              }
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Client</TH>
                  <TH className="hidden md:table-cell">Signed in with</TH>
                  <TH className="hidden lg:table-cell">Package</TH>
                  <TH className="hidden xl:table-cell">Allowance used</TH>
                  <TH className="hidden lg:table-cell">Network / device</TH>
                  <TH className="hidden text-end sm:table-cell">Data</TH>
                  <TH className="text-end">Status</TH>
                  {mayDisconnect && <TH><span className="sr-only">Actions</span></TH>}
                </TR>
              </THead>
              <TBody>
                {rows.map((s) => (
                  <SessionRow key={s.id} s={s} mayDisconnect={mayDisconnect} onOpen={openDetail} onDisconnect={askDisconnect} />
                ))}
              </TBody>
            </Table>
          )}
          {rows && rows.length > 0 && (list.offset > 0 || list.hasMore) && (
            <div className="border-t border-border px-4 py-3">
              <Pagination
                offset={list.offset}
                limit={list.pageSize}
                shown={rows.length}
                total={list.total}
                hasMore={list.hasMore}
                onChange={list.setOffset}
              />
            </div>
          )}
        </CardBody>
      </Card>

      {/* ------------------------------------------------------------------ detail */}
      <DetailDialog
        open={detail !== null}
        onOpenChange={(v) => !v && setDetail(null)}
        title={detail ? identifySession(detail).title : ""}
        description={detail ? identifySession(detail).subtitle : undefined}
        footer={
          detail?.state === "active" && mayDisconnect ? (
            <>
              <Button variant="ghost" onClick={() => setDetail(null)}>Close</Button>
              <Button variant="danger" onClick={() => { setConfirm(detail); }}>
                <Power /> Disconnect this device
              </Button>
            </>
          ) : undefined
        }
      >
        {detail && <SessionDetail s={detail} />}
      </DetailDialog>

      {/* ------------------------------------------------------------------ disconnect */}
      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(v) => !v && setConfirm(null)}
        title={confirm ? `Disconnect ${identifySession(confirm).title}'s device?` : "Disconnect this device?"}
        description={
          confirm
            ? `${identifySession(confirm).title} will lose internet access on this device immediately. ${
                typeof confirm.active_devices === "number" && confirm.active_devices > 1
                  ? `Their ${confirm.active_devices - 1} other device${confirm.active_devices - 1 === 1 ? "" : "s"} stay online`
                  : "They have no other device online"
              }, and they can sign in again.`
            : undefined
        }
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
                { label: "Network", value: confirm.guest_network_name ?? "—" },
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

/**
 * AllowanceCell — what is left, only when there is an allowance to have left.
 *
 * A plan with no data quota is unmetered on that axis; rendering an empty meter for it would imply a limit the
 * guest does not have, which is exactly the kind of invented fact that gets repeated to a guest at the desk.
 */
// One row of the list. MEMOISED, and `keepUnchanged` below keeps an unchanged session the same object across
// polls, so a 10-second refresh re-renders only the rows whose figures actually moved.
const SessionRow = memo(function SessionRow({
  s, mayDisconnect, onOpen, onDisconnect,
}: {
  s: Session;
  mayDisconnect: boolean;
  onOpen: (s: Session) => void;
  onDisconnect: (s: Session) => void;
}) {
  const id = identifySession(s);
  const Icon = KIND_ICON[(s.subject_kind ?? "") as keyof typeof KIND_ICON] ?? Monitor;
  const st = stateWords(s.state);
  return (
    <TR>
      <TD>
        <button
          type="button"
          onClick={() => onOpen(s)}
          className="group flex items-start gap-2.5 text-start"
        >
          <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-md bg-surface text-muted-foreground">
            <Icon className="size-3.5" />
          </span>
          <span className="min-w-0">
            <span
              className={
                id.anonymous
                  ? "block font-mono text-xs text-muted-foreground group-hover:text-foreground"
                  : "block font-medium group-hover:text-primary"
              }
            >
              {id.title}
            </span>
            {id.subtitle && (
              <span className="block truncate text-xs text-muted-foreground">{id.subtitle}</span>
            )}
            {id.anonymous && (
              <span className="block text-2xs text-muted-foreground">
                No client record on this session
              </span>
            )}
          </span>
        </button>
      </TD>
      <TD className="hidden text-muted-foreground md:table-cell">
        <div className="text-xs">{methodLabel(s.credential_method)}</div>
        <div className="text-2xs">{formatRelative(s.started_at)}</div>
      </TD>
      <TD className="hidden lg:table-cell">
        {s.package_name || s.package_code ? (
          <>
            <div className="text-sm">{s.package_name || s.package_code}</div>
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
      <TD className="hidden lg:table-cell">
        <div className="text-xs">{s.guest_network_name ?? "—"}</div>
        <div className="font-mono text-2xs text-muted-foreground">{s.ip}</div>
      </TD>
      <TD className="hidden text-end sm:table-cell">
        <div className="tabular">{formatBytes(s.bytes_down)}</div>
        <div className="text-2xs tabular text-muted-foreground">
          {formatBytes(s.bytes_up)} up
        </div>
      </TD>
      <TD className="text-end">
        <Badge tone={st.tone} dot>{st.label}</Badge>
        {s.end_reason && (
          <div className="mt-0.5 text-2xs text-muted-foreground">
            {endReasonWords(s.end_reason)}
          </div>
        )}
      </TD>
      {mayDisconnect && (
        <TD className="text-end">
          {s.state === "active" && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => onDisconnect(s)}
            >
              <Power /> Disconnect
            </Button>
          )}
        </TD>
      )}
    </TR>
  );
});

/** Reuse the previous object for every session whose content has not changed, so memoised rows can skip. */
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
  const id = identifySession(s);
  const st = stateWords(s.state);
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={st.tone} dot>{st.label}</Badge>
        <Badge tone="neutral">{id.kindLabel}</Badge>
        {s.entitlement_status && s.entitlement_status !== "ACTIVE" && (
          <Badge tone="warn">Access {s.entitlement_status.toLowerCase()}</Badge>
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
              <Link href="/internet-packages" className="text-primary underline underline-offset-2 hover:decoration-2">
                {s.package_name || s.package_code}
              </Link>
            ) : "—",
          },
          {
            label: "Service plan",
            value: s.service_plan_code
              ? `${s.service_plan_code}${speedPair(s.down_kbps, s.up_kbps) ? ` — ${speedPair(s.down_kbps, s.up_kbps)}` : ""}`
              : "—",
          },
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
                    <Link href="/stays" className="text-primary underline underline-offset-2 hover:decoration-2">Open in Stays</Link>
                  ) : "—",
                },
              ]
            : []),
          { label: "Started at", value: <span className="text-xs">{formatDate(s.started_at)}</span> },
          ...(s.end_reason ? [{ label: "Ended because", value: endReasonWords(s.end_reason) }] : []),
          { label: "Session id", value: <MonoId value={s.id} title="Session" /> },
        ]}
      />
    </>
  );
}
