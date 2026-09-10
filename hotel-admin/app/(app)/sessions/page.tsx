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

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ListResp, Session } from "@/lib/api";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Segmented } from "@/components/ui/tabs";
import { ConfirmDialog, DetailDialog } from "@/components/ui/dialog";
import { Explain } from "@/components/ui/tooltip";
import { DList, Meter, MonoId, Metric, SkeletonRows } from "@/components/ui/misc";
import { formatBytes, formatRelative, formatDate, errMsg } from "@/lib/utils";
import {
  identifySession, methodLabel, stateWords, endReasonWords, speedPair,
} from "@/lib/session-words";
import { Users, Monitor, ArrowDownUp, Search, Hotel, KeyRound, Ticket, UserCircle, Power } from "lucide-react";

type Tab = "active" | "recent";

const KIND_ICON = {
  room: Hotel,
  account: KeyRound,
  voucher: Ticket,
  guest: UserCircle,
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

export default function SessionsPage() {
  const [tab, setTab] = useState<Tab>("active");
  const [rows, setRows] = useState<Session[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState<string>("");
  const [detail, setDetail] = useState<Session | null>(null);
  const [confirm, setConfirm] = useState<Session | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    const q = new URLSearchParams();
    if (tab === "active") q.set("state", "active");
    try {
      const r = await api.get<ListResp<Session>>(`/sessions?${q.toString()}`);
      setRows(r.data ?? []);
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows([]);
    }
  }, [tab]);

  // The list is cleared on a tab CHANGE only, not on every poll. Blanking it each time the 10-second poll fired
  // made the table flash and lose the row the operator was reading.
  useEffect(() => { setRows(null); void load(); }, [load]);
  useEffect(() => {
    if (tab !== "active") return;
    const id = setInterval(() => void load(), 10_000);
    return () => clearInterval(id);
  }, [tab, load]);

  async function onDisconnect(s: Session) {
    setBusy(true); setActionErr(null);
    try {
      await api.post(`/sessions/${s.id}/disconnect`, { reason: "admin" });
      setConfirm(null);
      setDetail(null);
      // scd enforces asynchronously, so an immediate reload can still show the session as active. The short
      // delay is not cosmetic: without it the operator sees their own action appear not to have worked.
      setTimeout(() => void load(), 600);
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusy(false);
    }
  }

  const filtered = useMemo(() => {
    if (!rows) return null;
    const q = query.trim().toLowerCase();
    return rows.filter((s) => {
      if (kind && (s.subject_kind ?? "") !== kind) return false;
      if (!q) return true;
      const id = identifySession(s);
      return [
        id.title, id.subtitle, s.room, s.subject_label, s.subject_name, s.external_reservation_id,
        s.ip, s.mac, s.package_name, s.package_code, s.guest_network_name,
      ].some((v) => typeof v === "string" && v.toLowerCase().includes(q));
    });
  }, [rows, query, kind]);

  // Summary figures come from the rows on screen, which is the honest thing for them to describe: they are a
  // summary OF THIS LIST, and the dashboard is where the site-wide numbers live.
  const summary = useMemo(() => {
    const list = rows ?? [];
    const active = list.filter((s) => s.state === "active");
    return {
      devices: active.length,
      guests: new Set(active.map((s) => s.entitlement_id || s.mac)).size,
      rooms: new Set(active.filter((s) => s.subject_kind === "room").map((s) => s.subject_label)).size,
      bytes: list.reduce((a, s) => a + (s.bytes_down ?? 0) + (s.bytes_up ?? 0), 0),
    };
  }, [rows]);

  const kindCounts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const s of rows ?? []) c[s.subject_kind ?? ""] = (c[s.subject_kind ?? ""] ?? 0) + 1;
    return c;
  }, [rows]);

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Guests"
        title={tab === "active" ? "Active sessions" : "Recent sessions"}
        description="Every device currently online, and which guest it belongs to. A session is one device; a guest may have several."
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

      <ErrorBanner err={err} />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="Devices online"
          value={rows ? summary.devices.toLocaleString() : "—"}
          icon={<Monitor />}
          tone="primary"
        />
        <StatCard
          label="Guests online"
          value={rows ? summary.guests.toLocaleString() : "—"}
          icon={<Users />}
          explain={
            <Explain>
              Counted by what the internet was granted to — one room, account or voucher — so a family with four
              devices is one guest.
            </Explain>
          }
        />
        <StatCard
          label="Rooms online"
          value={rows ? summary.rooms.toLocaleString() : "—"}
          icon={<Hotel />}
          hint="Distinct rooms signed in with a room number"
        />
        <StatCard
          label="Data in this list"
          value={rows ? formatBytes(summary.bytes) : "—"}
          icon={<ArrowDownUp />}
          hint="Total recorded against the sessions shown"
        />
      </div>

      <Card>
        <CardBody className="border-b border-border py-3">
          <Toolbar>
            <div className="relative w-full max-w-xs">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Room, name, username, IP or MAC…"
                aria-label="Search sessions"
                className="pl-8"
              />
            </div>
            <div className="flex items-end gap-2">
              <Select
                value={kind}
                onChange={(e) => setKind(e.target.value)}
                aria-label="Filter by how the guest signed in"
                className="w-52"
              >
                <option value="">All sign-in types</option>
                <option value="room">Room ({kindCounts.room ?? 0})</option>
                <option value="account">Account ({kindCounts.account ?? 0})</option>
                <option value="voucher">Voucher ({kindCounts.voucher ?? 0})</option>
                <option value="guest">Email / social ({kindCounts.guest ?? 0})</option>
              </Select>
              {(query || kind) && (
                <Button variant="ghost" size="sm" onClick={() => { setQuery(""); setKind(""); }}>
                  Clear
                </Button>
              )}
            </div>
          </Toolbar>
        </CardBody>

        <CardBody className="p-0">
          {filtered === null ? (
            <SkeletonRows rows={6} cols={6} />
          ) : filtered.length === 0 ? (
            <EmptyState
              icon={<Monitor />}
              title={
                rows && rows.length > 0
                  ? "No session matches this filter"
                  : tab === "active"
                    ? "Nobody is online"
                    : "No recent sessions"
              }
              hint={
                rows && rows.length > 0
                  ? "Try a different search, or clear the filters."
                  : tab === "active"
                    ? "Connected guests appear here as soon as they sign in."
                    : undefined
              }
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Guest</TH>
                  <TH>Signed in with</TH>
                  <TH>Package</TH>
                  <TH>Allowance used</TH>
                  <TH>Network / device</TH>
                  <TH className="text-right">Data</TH>
                  <TH className="text-right">Status</TH>
                  <TH />
                </TR>
              </THead>
              <tbody>
                {filtered.map((s) => {
                  const id = identifySession(s);
                  const Icon = KIND_ICON[(s.subject_kind ?? "") as keyof typeof KIND_ICON] ?? Monitor;
                  const st = stateWords(s.state);
                  return (
                    <TR key={s.id}>
                      <TD>
                        <button
                          type="button"
                          onClick={() => { setDetail(s); setActionErr(null); }}
                          className="group flex items-start gap-2.5 text-left"
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
                                No guest record on this session
                              </span>
                            )}
                          </span>
                        </button>
                      </TD>
                      <TD className="text-muted-foreground">
                        <div className="text-xs">{methodLabel(s.credential_method)}</div>
                        <div className="text-2xs">{formatRelative(s.started_at)}</div>
                      </TD>
                      <TD>
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
                      <TD className="min-w-40">
                        <AllowanceCell s={s} />
                      </TD>
                      <TD>
                        <div className="text-xs">{s.guest_network_name ?? "—"}</div>
                        <div className="font-mono text-2xs text-muted-foreground">{s.ip}</div>
                      </TD>
                      <TD className="text-right">
                        <div className="tabular">{formatBytes(s.bytes_down)}</div>
                        <div className="text-2xs tabular text-muted-foreground">
                          {formatBytes(s.bytes_up)} up
                        </div>
                      </TD>
                      <TD className="text-right">
                        <Badge tone={st.tone} dot>{st.label}</Badge>
                        {s.end_reason && (
                          <div className="mt-0.5 text-2xs text-muted-foreground">
                            {endReasonWords(s.end_reason)}
                          </div>
                        )}
                      </TD>
                      <TD className="text-right">
                        {s.state === "active" && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => { setConfirm(s); setActionErr(null); }}
                          >
                            <Power /> Disconnect
                          </Button>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
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
          detail?.state === "active" ? (
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
        title="Disconnect this device?"
        description={
          confirm
            ? `${identifySession(confirm).title} will lose internet access on this device immediately. Their other devices stay online, and they can sign in again.`
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
                  label: "Other devices on this guest",
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
            {s.active_devices} device{s.active_devices === 1 ? "" : "s"} online for this guest
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
          { label: "Guest network", value: s.guest_network_name ?? s.ingress_interface ?? "—" },
          { label: "IP address", value: <span className="font-mono text-xs">{s.ip}</span> },
          { label: "Device (MAC)", value: <span className="font-mono text-xs">{s.mac}</span> },
          {
            label: "Internet package",
            value: s.package_name || s.package_code ? (
              <Link href="/internet-packages" className="text-primary hover:underline">
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
                    <Link href="/stays" className="text-primary hover:underline">Open in Stays</Link>
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
