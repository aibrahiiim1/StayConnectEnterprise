"use client";

// USAGE EXPLORER — the screen you open when a guest is arguing about their data.
//
// It is built around the two sentences that actually arrive at a front desk:
//
//     "Room 4202 says they never got the internet they paid for."
//     "Something on our network downloaded 40 GB last night."
//
// The first is a STAY question and the second is a DEVICE question, so those are the two tabs, and each one
// starts with the search box the operator already has an answer for — a room number, or a MAC off a handset.
//
// EVERY FIGURE IS TRACEABLE. The stay total is the sum of its sessions; each session can be opened to show
// the durable accounting samples it was summed from, with their own total printed next to the session's so
// the two can be seen to agree. Nothing here is estimated, and a stay with no measured usage says so rather
// than showing a confident zero.
//
// ROOM IS NOT IDENTITY and MAC IS NOT A PERSON. Every room is shown with the PMS connection that gives it
// meaning, and the device view says "device" throughout — it lists the stays a device was associated with,
// which is what the data supports, rather than naming a guest, which it does not.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { SkeletonRows } from "@/components/ui/misc";
import { EmptyState } from "@/components/ui/empty-state";
import { formatBytes, quotaPercent, endReasonWords } from "@/lib/bytes";
import { formatDate } from "@/lib/utils";
import { Search, Activity, Smartphone, ChevronRight, ArrowLeft } from "lucide-react";

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
type StayDetail = { stay: StayRow; service_plan?: string; devices: DeviceRow[]; sessions: SessionRow[] };
type DeviceDetail = {
  mac: string; from: string; to: string; first_seen?: string; last_seen?: string;
  totals: Totals; sessions: SessionRow[]; stays: StayRow[];
};

type Tab = "stays" | "devices";

export default function UsageExplorerPage() {
  const [tab, setTab] = useState<Tab>("stays");
  const [q, setQ] = useState("");
  const [rows, setRows] = useState<StayRow[] | null>(null);
  const [stay, setStay] = useState<StayDetail | null>(null);
  const [device, setDevice] = useState<DeviceDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [samples, setSamples] = useState<Record<string, { bytes_total: number; sample_count: number } | "loading">>({});

  const search = useCallback(async () => {
    setBusy(true); setErr(null);
    try {
      const r = await api.get<{ data: StayRow[] }>(`/usage/stays?q=${encodeURIComponent(q)}&limit=100`);
      setRows(r.data ?? []);
    } catch (e) { setErr(e instanceof ApiError ? e.message : "the usage records could not be read"); }
    finally { setBusy(false); }
  }, [q]);

  useEffect(() => { if (tab === "stays" && rows === null) search(); }, [tab, rows, search]);

  async function openStay(id: string) {
    setBusy(true); setErr(null);
    try { setStay(await api.get<StayDetail>(`/usage/stays/${id}`)); }
    catch (e) { setErr(e instanceof ApiError ? e.message : "that stay could not be read"); }
    finally { setBusy(false); }
  }

  // DEEP LINK: /usage?stay=<id> opens that stay directly. Internet packages → Guest activity links here, so
  // "see what this stay used" lands on the stay rather than on an empty search. Read from the location once,
  // on mount, rather than through useSearchParams, which would need a Suspense boundary for the static build.
  useEffect(() => {
    const id = typeof window === "undefined" ? null : new URLSearchParams(window.location.search).get("stay");
    if (id) void openStay(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function lookUpDevice(mac: string) {
    setBusy(true); setErr(null); setStay(null);
    try { setDevice(await api.get<DeviceDetail>(`/usage/devices/${encodeURIComponent(mac)}`)); setTab("devices"); }
    catch (e) { setErr(e instanceof ApiError ? e.message : "that device could not be read"); setDevice(null); }
    finally { setBusy(false); }
  }

  /** THE SAMPLES BEHIND ONE SESSION. Fetched only when an operator asks, because this is the step that turns
   *  "the system says 209 MB" into something they can show a guest. */
  async function openSamples(sessionID: string) {
    setSamples((p) => ({ ...p, [sessionID]: "loading" }));
    try {
      const r = await api.get<{ bytes_total: number; sample_count: number }>(`/usage/sessions/${sessionID}/samples`);
      setSamples((p) => ({ ...p, [sessionID]: r }));
    } catch {
      setSamples((p) => { const n = { ...p }; delete n[sessionID]; return n; });
    }
  }

  return (
    <div className="mx-auto w-full max-w-7xl space-y-5">
      <header>
        <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">Guests</div>
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight sm:text-2xl">
          <Activity className="h-5 w-5" /> Usage explorer
        </h1>
        <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
          What a room or a device actually used, measured by this appliance. Every total here is the sum of
          recorded sessions, and every session can be opened to show the accounting samples behind it.
        </p>
      </header>

      {err && (
        <div role="alert" className="rounded-lg border border-destructive/25 bg-destructive-subtle p-3 text-sm text-destructive-subtle-foreground">
          {err}
        </div>
      )}

      <div className="flex flex-wrap gap-1 border-b" role="tablist" aria-label="What to investigate">
        {([["stays", "By room or stay", Activity], ["devices", "By device", Smartphone]] as const).map(([id, label, Icon]) => (
          <button key={id} role="tab" type="button" aria-selected={tab === id}
            onClick={() => { setTab(id); setStay(null); setDevice(null); setErr(null); }}
            className={`-mb-px inline-flex items-center gap-2 border-b-2 px-4 py-2.5 text-sm ${
              tab === id ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground"
            }`}>
            <Icon className="h-4 w-4" /> {label}
          </button>
        ))}
      </div>

      {/* ---------------------------------------------------------------- BY ROOM OR STAY ---------------- */}
      {tab === "stays" && !stay && (
        <Card>
          <CardHeader><CardTitle>Find a stay</CardTitle></CardHeader>
          <CardBody className="space-y-4">
            <form className="flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); search(); }}>
              <label className="relative min-w-0 flex-1">
                <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                <Input value={q} onChange={(e) => setQ(e.target.value)} className="pl-8"
                  aria-label="Room number or reservation"
                  placeholder="Room number or reservation — or leave empty for the heaviest users" />
              </label>
              <Button type="submit" disabled={busy}>{busy ? "Searching…" : "Search"}</Button>
            </form>

            {rows === null ? <SkeletonRows rows={4} cols={5} /> : rows.length === 0 ? (
              <EmptyState icon={<Activity />} title="No stay matched"
                hint="Only stays that were given internet access appear here. A stay with no access has nothing to measure." />
            ) : (
              <div className="overflow-x-auto">
                <Table>
                  <THead>
                    <TR>
                      <TH>Room</TH><TH>Stay</TH><TH className="text-right">Downloaded</TH>
                      <TH className="text-right">Uploaded</TH><TH className="text-right">Total</TH>
                      <TH>Allowance</TH><TH />
                    </TR>
                  </THead>
                  <tbody>
                    {rows.map((r) => {
                      const pct = quotaPercent(r.consumed_bytes, r.quota_bytes);
                      return (
                        <TR key={r.stay_id}>
                          <TD>
                            <div className="font-medium">{r.room || "—"}</div>
                            {/* The interface is not decoration: a room number only means something inside one. */}
                            <div className="text-2xs text-muted-foreground">{r.pms_interface}</div>
                          </TD>
                          <TD className="text-sm">
                            <div>{r.reservation || "—"}</div>
                            <div className="text-2xs text-muted-foreground">
                              {r.stay_status.replace(/_/g, " ").toLowerCase()}
                              {r.arrival ? ` · ${formatDate(r.arrival)}` : ""}
                            </div>
                          </TD>
                          <TD className="text-right tabular-nums">{formatBytes(r.totals.bytes_down)}</TD>
                          <TD className="text-right tabular-nums">{formatBytes(r.totals.bytes_up)}</TD>
                          <TD className="text-right font-medium tabular-nums">{formatBytes(r.totals.bytes_total)}</TD>
                          <TD className="text-sm">
                            {pct === null ? <span className="text-muted-foreground">No limit</span> : (
                              <span className={pct >= 100 ? "text-warning-subtle-foreground" : ""}>
                                {pct}% of {formatBytes(r.quota_bytes)}
                              </span>
                            )}
                          </TD>
                          <TD className="text-right">
                            <Button size="sm" variant="ghost" onClick={() => openStay(r.stay_id)}>
                              Details <ChevronRight className="ml-1 h-3.5 w-3.5" />
                            </Button>
                          </TD>
                        </TR>
                      );
                    })}
                  </tbody>
                </Table>
              </div>
            )}
          </CardBody>
        </Card>
      )}

      {/* ---------------------------------------------------------------- ONE STAY ----------------------- */}
      {tab === "stays" && stay && (
        <div className="space-y-5">
          <Button variant="ghost" size="sm" onClick={() => setStay(null)}>
            <ArrowLeft className="mr-1 h-4 w-4" /> Back to stays
          </Button>

          <Card>
            <CardHeader>
              <div>
                <CardTitle>Room {stay.stay.room || "—"}</CardTitle>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  {stay.stay.pms_interface}
                  {stay.stay.reservation ? ` · reservation ${stay.stay.reservation}` : ""}
                  {stay.stay.arrival ? ` · ${formatDate(stay.stay.arrival)}` : ""}
                  {stay.stay.departure ? ` → ${formatDate(stay.stay.departure)}` : ""}
                </p>
              </div>
            </CardHeader>
            <CardBody className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-4">
                <Figure label="Downloaded" value={formatBytes(stay.stay.totals.bytes_down)} />
                <Figure label="Uploaded" value={formatBytes(stay.stay.totals.bytes_up)} />
                <Figure label="Total used" value={formatBytes(stay.stay.totals.bytes_total)} strong />
                <Figure
                  label="Allowance"
                  value={stay.stay.quota_bytes ? formatBytes(stay.stay.quota_bytes) : "No limit"}
                  hint={quotaPercent(stay.stay.consumed_bytes, stay.stay.quota_bytes) !== null
                    ? `${quotaPercent(stay.stay.consumed_bytes, stay.stay.quota_bytes)}% used`
                    : undefined}
                />
              </div>
              <div className="flex flex-wrap gap-2 text-sm">
                {stay.service_plan && <Badge tone="neutral">Plan: {stay.service_plan}</Badge>}
                {endReasonWords(stay.stay.end_reason) && (
                  <Badge tone="warn">{endReasonWords(stay.stay.end_reason)}</Badge>
                )}
                <Badge tone="default">{stay.stay.totals.sessions} sessions</Badge>
                <Badge tone="default">{stay.stay.totals.devices} devices</Badge>
              </div>
            </CardBody>
          </Card>

          <Card>
            <CardHeader><CardTitle>Devices used during this stay</CardTitle></CardHeader>
            <CardBody>
              {stay.devices.length === 0 ? (
                <EmptyState icon={<Smartphone />} title="No device recorded"
                  hint="Nothing connected under this stay's access." />
              ) : (
                <div className="overflow-x-auto">
                  <Table>
                    <THead><TR><TH>Device</TH><TH className="text-right">Downloaded</TH><TH className="text-right">Uploaded</TH><TH className="text-right">Total</TH><TH>Sessions</TH><TH /></TR></THead>
                    <tbody>
                      {stay.devices.map((d) => (
                        <TR key={d.mac || "unknown"}>
                          <TD className="font-mono text-sm">{d.mac || "unknown"}</TD>
                          <TD className="text-right tabular-nums">{formatBytes(d.bytes_down)}</TD>
                          <TD className="text-right tabular-nums">{formatBytes(d.bytes_up)}</TD>
                          <TD className="text-right font-medium tabular-nums">{formatBytes(d.bytes_total)}</TD>
                          <TD>{d.sessions}</TD>
                          <TD className="text-right">
                            {d.mac && (
                              <Button size="sm" variant="ghost" onClick={() => lookUpDevice(d.mac)}>
                                This device
                              </Button>
                            )}
                          </TD>
                        </TR>
                      ))}
                    </tbody>
                  </Table>
                </div>
              )}
            </CardBody>
          </Card>

          <SessionsCard sessions={stay.sessions} samples={samples} onOpenSamples={openSamples} />
        </div>
      )}

      {/* ---------------------------------------------------------------- BY DEVICE ---------------------- */}
      {tab === "devices" && (
        <div className="space-y-5">
          <Card>
            <CardHeader><CardTitle>Look up a device</CardTitle></CardHeader>
            <CardBody className="space-y-3">
              <form className="flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); lookUpDevice(q); }}>
                <label className="relative min-w-0 flex-1">
                  <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                  <Input value={q} onChange={(e) => setQ(e.target.value)} className="pl-8 font-mono"
                    aria-label="Device MAC address" placeholder="aa:bb:cc:dd:ee:ff" />
                </label>
                <Button type="submit" disabled={busy || !q.trim()}>{busy ? "Looking…" : "Look up"}</Button>
              </form>
              <p className="text-xs text-muted-foreground">
                A device address identifies a piece of equipment, not a person. This shows what the device
                used and which stays it was connected under — it does not tell you who was holding it.
              </p>
            </CardBody>
          </Card>

          {device && (
            <>
              <Card>
                <CardHeader>
                  <div>
                    <CardTitle className="font-mono">{device.mac}</CardTitle>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      {formatDate(device.from)} → {formatDate(device.to)}
                      {device.first_seen ? ` · first seen ${formatDate(device.first_seen)}` : ""}
                    </p>
                  </div>
                </CardHeader>
                <CardBody>
                  <div className="grid gap-4 sm:grid-cols-4">
                    <Figure label="Downloaded" value={formatBytes(device.totals.bytes_down)} />
                    <Figure label="Uploaded" value={formatBytes(device.totals.bytes_up)} />
                    <Figure label="Total" value={formatBytes(device.totals.bytes_total)} strong />
                    <Figure label="Sessions" value={String(device.totals.sessions)} />
                  </div>
                </CardBody>
              </Card>

              {device.stays.length > 0 && (
                <Card>
                  <CardHeader><CardTitle>Stays this device connected under</CardTitle></CardHeader>
                  <CardBody className="flex flex-wrap gap-2">
                    {device.stays.map((s) => (
                      <Button key={s.stay_id} size="sm" variant="secondary"
                        onClick={() => { setTab("stays"); openStay(s.stay_id); }}>
                        Room {s.room || "—"} <span className="ml-1 text-2xs text-muted-foreground">{s.pms_interface}</span>
                      </Button>
                    ))}
                  </CardBody>
                </Card>
              )}

              <SessionsCard sessions={device.sessions} samples={samples} onOpenSamples={openSamples} />
            </>
          )}
        </div>
      )}
    </div>
  );
}

function Figure({ label, value, hint, strong }: { label: string; value: string; hint?: string; strong?: boolean }) {
  return (
    <div>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={`tabular-nums ${strong ? "text-2xl font-semibold" : "text-xl"}`}>{value}</div>
      {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
    </div>
  );
}

/** The sessions, with the accounting samples behind each one available on request.
 *
 *  This is the part that makes the screen usable in a real dispute: the operator can say "209 MB, across
 *  1,204 recorded samples between 14:02 and 23:51", and the sample total is printed next to the session
 *  total so the two can be seen to agree rather than taken on trust. */
function SessionsCard({ sessions, samples, onOpenSamples }: {
  sessions: SessionRow[];
  samples: Record<string, { bytes_total: number; sample_count: number } | "loading">;
  onOpenSamples: (id: string) => void;
}) {
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Sessions</CardTitle>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Each period of connected access. Open one to see the recorded samples its total was measured from.
          </p>
        </div>
      </CardHeader>
      <CardBody>
        {sessions.length === 0 ? (
          <EmptyState icon={<Activity />} title="No sessions recorded"
            hint="Nothing was measured in this period. That is not the same as zero usage — it means no session exists." />
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <THead>
                <TR>
                  <TH>Started</TH><TH>Ended</TH><TH>Device</TH>
                  <TH className="text-right">Total</TH><TH>How it ended</TH><TH />
                </TR>
              </THead>
              <tbody>
                {sessions.map((s) => {
                  const sm = samples[s.session_id];
                  return (
                    <TR key={s.session_id}>
                      <TD className="whitespace-nowrap text-sm">{s.started ? formatDate(s.started) : "—"}</TD>
                      <TD className="whitespace-nowrap text-sm">
                        {s.ended ? formatDate(s.ended) : <Badge tone="ok">Still connected</Badge>}
                      </TD>
                      <TD className="font-mono text-2xs">{s.mac || "—"}</TD>
                      <TD className="text-right tabular-nums">
                        {formatBytes(s.bytes_total)}
                        {sm && sm !== "loading" && (
                          <div className="text-2xs text-muted-foreground">
                            {sm.sample_count} samples · {formatBytes(sm.bytes_total)}
                          </div>
                        )}
                      </TD>
                      <TD className="text-sm">{endReasonWords(s.end_reason) ?? "—"}</TD>
                      <TD className="text-right">
                        {!sm && (
                          <Button size="sm" variant="ghost" onClick={() => onOpenSamples(s.session_id)}>
                            Show evidence
                          </Button>
                        )}
                        {sm === "loading" && <span className="text-2xs text-muted-foreground">Reading…</span>}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          </div>
        )}
      </CardBody>
    </Card>
  );
}
