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
//
// Read-only for every role that can open it: there is nothing to change here, only evidence to read.

import { useCallback, useEffect, useState } from "react";
import { Activity, ArrowLeft, ChevronRight, FileSearch, Search, Smartphone } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Meter, SkeletonRows } from "@/components/ui/misc";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { MetricStrip } from "@/components/ui/data";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { refreshingClass } from "@/components/ui/patterns";
import { formatBytes, quotaPercent, endReasonWords } from "@/lib/bytes";
import { cn, formatDate } from "@/lib/utils";

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

// The stay lifecycle in the words Stays uses.
const STAY_WORDS: Record<string, string> = {
  IN_HOUSE: "In house",
  RESERVED: "Arriving",
  CHECKED_OUT: "Checked out",
  POST_STAY_ACTIVE: "Post-stay access",
  CANCELLED: "Cancelled",
  NO_SHOW: "No show",
};
const stayWords = (s: string) => STAY_WORDS[s] ?? s.replace(/_/g, " ").toLowerCase();

function AllowanceCell({ consumed, quota }: { consumed?: number | null; quota?: number | null }) {
  const pct = quotaPercent(consumed, quota);
  if (pct === null) return <span className="text-sm text-muted-foreground">No limit</span>;
  return (
    <Meter
      className="min-w-32"
      value={Math.min(pct, 100)}
      max={100}
      label={pct >= 100 ? "Allowance used up" : undefined}
      caption={`${pct}% of ${formatBytes(quota)}`}
    />
  );
}

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
    <PageShell>
      <PageHeader
        eyebrow="Guests"
        title="Usage explorer"
        icon={<Activity />}
        description="Settle a data-usage question: drill from a room or a device down to its sessions and the accounting samples behind them. Every total is the sum of recorded sessions."
      />

      <ErrorBanner err={err} />

      <Tabs
        value={tab}
        onValueChange={(v) => { setTab(v as Tab); setStay(null); setDevice(null); setErr(null); }}
      >
        <TabsList aria-label="What to investigate">
          <TabsTrigger value="stays"><Activity className="size-4" aria-hidden /> By room or stay</TabsTrigger>
          <TabsTrigger value="devices"><Smartphone className="size-4" aria-hidden /> By device</TabsTrigger>
        </TabsList>

        {/* ---------------------------------------------------------------- BY ROOM OR STAY ---------------- */}
        <TabsContent value="stays" className="mt-5 space-y-5">
          {!stay && (
            <Card className="overflow-hidden">
              <CardBody className="border-b border-border py-3">
                <form className="flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); search(); }}>
                  <div className="relative min-w-0 flex-1">
                    <Search className="pointer-events-none absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
                    <Input value={q} onChange={(e) => setQ(e.target.value)} className="ps-8"
                      aria-label="Room number or reservation"
                      placeholder="Room number or reservation — or leave empty for the heaviest users" />
                  </div>
                  <Button type="submit" disabled={busy}>{busy ? "Searching…" : "Search"}</Button>
                </form>
              </CardBody>

              {rows === null ? <SkeletonRows rows={4} cols={5} /> : rows.length === 0 ? (
                <EmptyState icon={<Activity />} title={q.trim() ? "No stay matched" : "No usage recorded yet"}
                  hint="Only stays that were given internet access appear here. A stay with no access has nothing to measure." />
              ) : (
                <div className={cn(busy && refreshingClass)}>
                  <Table>
                    <THead>
                      <TR>
                        <TH>Room</TH>
                        <TH className="hidden md:table-cell">Stay</TH>
                        <TH className="hidden text-end sm:table-cell">Downloaded</TH>
                        <TH className="hidden text-end sm:table-cell">Uploaded</TH>
                        <TH className="text-end">Total</TH>
                        <TH className="hidden lg:table-cell">Allowance</TH>
                        <TH><span className="sr-only">Open</span></TH>
                      </TR>
                    </THead>
                    <TBody>
                      {rows.map((r) => (
                        <TR key={r.stay_id}>
                          <TD>
                            <div className="font-medium">{r.room ? `Room ${r.room}` : "—"}</div>
                            {/* The connection is not decoration: a room number only means something inside one. */}
                            <div className="text-caption text-muted-foreground">{r.pms_interface}</div>
                          </TD>
                          <TD className="hidden text-sm md:table-cell">
                            <div>{r.reservation ? `Reservation ${r.reservation}` : "—"}</div>
                            <div className="text-caption text-muted-foreground">
                              {stayWords(r.stay_status)}
                              {r.arrival ? ` · ${formatDate(r.arrival)}` : ""}
                            </div>
                          </TD>
                          <TD className="hidden text-end tabular sm:table-cell">{formatBytes(r.totals.bytes_down)}</TD>
                          <TD className="hidden text-end tabular sm:table-cell">{formatBytes(r.totals.bytes_up)}</TD>
                          <TD className="text-end font-medium tabular">{formatBytes(r.totals.bytes_total)}</TD>
                          <TD className="hidden lg:table-cell">
                            <AllowanceCell consumed={r.consumed_bytes} quota={r.quota_bytes} />
                          </TD>
                          <TD className="text-end">
                            <Button size="sm" variant="ghost" onClick={() => openStay(r.stay_id)}>
                              Details <ChevronRight className="rtl:rotate-180" />
                            </Button>
                          </TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                </div>
              )}
            </Card>
          )}

          {/* ---------------------------------------------------------------- ONE STAY ----------------------- */}
          {stay && (
            <div className="space-y-5">
              <Button variant="ghost" size="sm" onClick={() => setStay(null)}>
                <ArrowLeft className="rtl:rotate-180" /> Back to stays
              </Button>

              <Card>
                <CardHeader>
                  <div className="min-w-0 space-y-1">
                    <CardTitle>{stay.stay.room ? `Room ${stay.stay.room}` : "Stay"}</CardTitle>
                    <CardDescription>
                      {stay.stay.pms_interface}
                      {stay.stay.reservation ? ` · reservation ${stay.stay.reservation}` : ""}
                      {stay.stay.arrival ? ` · ${formatDate(stay.stay.arrival)}` : ""}
                      {stay.stay.departure ? ` → ${formatDate(stay.stay.departure)}` : ""}
                    </CardDescription>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    <Badge tone="neutral">{stayWords(stay.stay.stay_status)}</Badge>
                    {endReasonWords(stay.stay.end_reason) && (
                      <Badge tone="warn">{endReasonWords(stay.stay.end_reason)}</Badge>
                    )}
                  </div>
                </CardHeader>
                <CardBody className="space-y-4">
                  <MetricStrip
                    items={[
                      { label: "Downloaded", value: formatBytes(stay.stay.totals.bytes_down) },
                      { label: "Uploaded", value: formatBytes(stay.stay.totals.bytes_up) },
                      { label: "Total used", value: formatBytes(stay.stay.totals.bytes_total) },
                      {
                        label: "Allowance",
                        value: stay.stay.quota_bytes
                          ? `${formatBytes(stay.stay.quota_bytes)}${quotaPercent(stay.stay.consumed_bytes, stay.stay.quota_bytes) !== null ? ` · ${quotaPercent(stay.stay.consumed_bytes, stay.stay.quota_bytes)}% used` : ""}`
                          : "No limit",
                        tone: (quotaPercent(stay.stay.consumed_bytes, stay.stay.quota_bytes) ?? 0) >= 100 ? "warn" : undefined,
                      },
                    ]}
                  />
                  <div className="flex flex-wrap gap-2">
                    {stay.service_plan && <Badge tone="neutral">Service plan: {stay.service_plan}</Badge>}
                    <Badge tone="default">{stay.stay.totals.sessions} sessions</Badge>
                    <Badge tone="default">{stay.stay.totals.devices} devices</Badge>
                  </div>
                </CardBody>
              </Card>

              <Card className="overflow-hidden">
                <CardHeader><CardTitle>Devices used during this stay</CardTitle></CardHeader>
                {stay.devices.length === 0 ? (
                  <EmptyState icon={<Smartphone />} title="No device recorded"
                    hint="Nothing connected under this stay's access." />
                ) : (
                  <Table>
                    <THead>
                      <TR>
                        <TH>Device</TH>
                        <TH className="hidden text-end sm:table-cell">Downloaded</TH>
                        <TH className="hidden text-end sm:table-cell">Uploaded</TH>
                        <TH className="text-end">Total</TH>
                        <TH className="hidden sm:table-cell">Sessions</TH>
                        <TH><span className="sr-only">Open</span></TH>
                      </TR>
                    </THead>
                    <TBody>
                      {stay.devices.map((d) => (
                        <TR key={d.mac || "unknown"}>
                          <TD className="font-mono text-xs">{d.mac || "unknown"}</TD>
                          <TD className="hidden text-end tabular sm:table-cell">{formatBytes(d.bytes_down)}</TD>
                          <TD className="hidden text-end tabular sm:table-cell">{formatBytes(d.bytes_up)}</TD>
                          <TD className="text-end font-medium tabular">{formatBytes(d.bytes_total)}</TD>
                          <TD className="hidden sm:table-cell">{d.sessions}</TD>
                          <TD className="text-end">
                            {d.mac && (
                              <Button size="sm" variant="ghost" onClick={() => lookUpDevice(d.mac)}>
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

              <SessionsCard sessions={stay.sessions} samples={samples} onOpenSamples={openSamples} />
            </div>
          )}
        </TabsContent>

        {/* ---------------------------------------------------------------- BY DEVICE ---------------------- */}
        <TabsContent value="devices" className="mt-5 space-y-5">
          <Card>
            <CardHeader><CardTitle>Look up a device</CardTitle></CardHeader>
            <CardBody className="space-y-3">
              <form className="flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); lookUpDevice(q); }}>
                <div className="relative min-w-0 flex-1">
                  <Search className="pointer-events-none absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
                  <Input value={q} onChange={(e) => setQ(e.target.value)} className="ps-8 font-mono"
                    aria-label="Device MAC address" placeholder="aa:bb:cc:dd:ee:ff" />
                </div>
                <Button type="submit" disabled={busy || !q.trim()}>{busy ? "Looking…" : "Look up"}</Button>
              </form>
              <Callout tone="neutral" title="A device is not a person">
                A device address identifies a piece of equipment. This shows what the device used and which stays it
                was connected under — it does not tell you who was holding it.
              </Callout>
            </CardBody>
          </Card>

          {!device && !busy && (
            <EmptyState icon={<FileSearch />} title="Look up a device to see its usage"
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
                        onClick={() => { setTab("stays"); openStay(s.stay_id); }}>
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
    <Card className="overflow-hidden">
      <CardHeader>
        <div className="space-y-1">
          <CardTitle>Sessions</CardTitle>
          <CardDescription>
            Each period of connected access. Show the evidence to see the recorded samples a total was measured from.
          </CardDescription>
        </div>
      </CardHeader>
      {sessions.length === 0 ? (
        <EmptyState icon={<Activity />} title="No sessions recorded"
          hint="Nothing was measured in this period. That is not the same as zero usage — it means no session exists." />
      ) : (
        <Table>
          <THead>
            <TR>
              <TH>Started</TH>
              <TH className="hidden sm:table-cell">Ended</TH>
              <TH className="hidden md:table-cell">Device</TH>
              <TH className="text-end">Total</TH>
              <TH className="hidden lg:table-cell">How it ended</TH>
              <TH><span className="sr-only">Evidence</span></TH>
            </TR>
          </THead>
          <TBody>
            {sessions.map((s) => {
              const sm = samples[s.session_id];
              return (
                <TR key={s.session_id}>
                  <TD className="whitespace-nowrap text-sm">{s.started ? formatDate(s.started) : "—"}</TD>
                  <TD className="hidden whitespace-nowrap text-sm sm:table-cell">
                    {s.ended ? formatDate(s.ended) : <Badge tone="ok" dot>Still connected</Badge>}
                  </TD>
                  <TD className="hidden font-mono text-xs md:table-cell">{s.mac || "—"}</TD>
                  <TD className="text-end tabular">
                    {formatBytes(s.bytes_total)}
                    {sm && sm !== "loading" && (
                      <div className="text-caption text-muted-foreground">
                        {sm.sample_count.toLocaleString()} samples · {formatBytes(sm.bytes_total)}
                      </div>
                    )}
                  </TD>
                  <TD className="hidden text-sm lg:table-cell">{endReasonWords(s.end_reason) ?? "—"}</TD>
                  <TD className="text-end">
                    {!sm && (
                      <Button size="sm" variant="ghost" onClick={() => onOpenSamples(s.session_id)}>
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
