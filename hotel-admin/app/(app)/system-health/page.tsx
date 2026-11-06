"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { StatusBadge, type Tone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Callout } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { BarList, ColumnChart } from "@/components/ui/chart";
import { HelpList, HelpSection } from "@/components/help";
import { AlertTriangle, CheckCircle2, Radar, RefreshCw } from "lucide-react";
import { usePoll } from "@/lib/use-poll";

type Status = "PASS" | "WARN" | "CRITICAL" | "NOT_VERIFIED";
type Check = { key: string; area: string; status: Status; text: string };
type Incident = Check & { first: string; last?: string; count: number };
type Resolved = { key: string; text?: string; first: string; cleared: string; status?: Status };
type Row = (string | number)[];
type DeviceFinding = {
  severity: string; status: string; device: string; mac: string; network: string; issue: string; cause: string;
  confidence: string; first: string; last: string; count: number; seen_since?: string; action: string;
};
type Deep = {
  at: string;
  analytics: { measures: string[]; windows: { label: string; now: number[]; previous: number[] }[] } | null;
  breakdowns: {
    ends_24h: Row[]; methods_24h: Row[]; results_24h: Row[]; networks_24h: Row[]; hourly: Row[]; daily: Row[]; online: Row;
  } | null;
  devices: DeviceFinding[] | null;
  deadlocks_daily: [string, number][];
  lock_waits: Record<string, Record<string, number>>;
};
type Snapshot = {
  at: string; mode: "quick" | "deep"; overall: Status; counts: Record<Status, number>;
  metrics: Record<string, string | number>; checks: Check[]; incidents: Incident[]; resolved: Resolved[]; deep: Deep | null;
};
type Answer = { available: boolean; reason?: string; stale?: boolean; age_seconds?: number; snapshot?: Snapshot };

const POLL_SECONDS = 30;
const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const SPIN = "animate-spin motion-reduce:animate-none";
const LINK = "font-medium underline underline-offset-2";

const STATUS: Record<Status, { word: string; tone: Tone }> = {
  PASS: { word: "Passing", tone: "ok" },
  WARN: { word: "Warning", tone: "warn" },
  CRITICAL: { word: "Critical", tone: "err" },
  NOT_VERIFIED: { word: "Not verified", tone: "default" },
};
const statusOf = (s: string) => STATUS[s as Status] ?? { word: "Not recognised", tone: "warn" as Tone };
const RANK: Record<string, number> = { CRITICAL: 0, WARN: 1, NOT_VERIFIED: 2, PASS: 3 };

const UNAVAILABLE: Record<string, string> = {
  not_installed: "The appliance monitor has not reported yet. It runs every five minutes once installed; an upgrade installs it.",
  unreadable: "The monitor's report exists but this service cannot read it. Re-run the appliance upgrade to restore its permissions.",
  invalid: "The monitor's last report could not be read. The next run replaces it.",
};

function ago(ts?: string | null): string {
  if (!ts) return "Not recorded";
  const s = Math.max(0, Math.floor((Date.now() - new Date(ts).getTime()) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}
function when(ts?: string | null): string {
  if (!ts) return "Not recorded";
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
const ACRONYMS: Record<string, string> = { dhcp: "DHCP", pms: "PMS", http: "HTTP", https: "HTTPS", cpu: "CPU", ip: "IP", os: "OS" };
// "automatic reconnection" -> "Automatic reconnection"; "ADDRESS_NO_LONGER_OWNED" -> "Address no longer owned".
function words(s: string): string {
  const w = s.replace(/_/g, " ").toLowerCase().split(" ").map((x) => ACRONYMS[x] ?? x).join(" ");
  return w.charAt(0).toUpperCase() + w.slice(1);
}
const num = (v: unknown) => (typeof v === "number" ? v : Number(v));
const shown = (v: unknown, unit = "") => (v === undefined || v === null || v === "?" || v === "" ? "Not reported" : `${v}${unit}`);
const pairs = (rows?: Row[]) => (rows ?? []).map((r) => ({ name: words(String(r[0])), value: num(r[1]) || 0, key: String(r[0]) }));
const stuckParts = (v: unknown) => /^(\d+)\((\d+) system\)$/.exec(String(v ?? ""));

function StatusPill({ status }: { status: string }) {
  const s = statusOf(status);
  return <StatusBadge tone={s.tone}>{s.word}</StatusBadge>;
}

export default function SystemHealthPage() {
  const [ans, setAns] = useState<Answer | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [tab, setTab] = useState("overview");
  const [filter, setFilter] = useState<"issues" | "all">("issues");

  const load = useCallback(async () => {
    setRefreshing(true);
    try {
      setAns(await api.get<Answer>("/diagnostics/monitor"));
      setLoadFailed(false);
      setUpdatedAt(Date.now());
    } catch {
      setLoadFailed(true);
    } finally {
      setRefreshing(false);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);
  usePoll(() => load(), POLL_SECONDS * 1000);

  const snap = ans?.available ? ans.snapshot ?? null : null;
  const deep = snap?.deep ?? null;
  const checks = useMemo(
    () => [...(snap?.checks ?? [])].sort((a, b) => (RANK[a.status] ?? 9) - (RANK[b.status] ?? 9) || a.area.localeCompare(b.area)),
    [snap],
  );
  const areas = useMemo(() => {
    const by = new Map<string, Check[]>();
    for (const c of checks) by.set(c.area, [...(by.get(c.area) ?? []), c]);
    return [...by.entries()]
      .map(([area, cs]) => ({ area, cs, worst: cs.reduce((w, c) => ((RANK[c.status] ?? 9) < (RANK[w] ?? 9) ? c.status : w), "PASS" as string) }))
      .sort((a, b) => (RANK[a.worst] ?? 9) - (RANK[b.worst] ?? 9) || a.area.localeCompare(b.area));
  }, [checks]);
  const m = snap?.metrics ?? {};
  const loading = ans === null && !loadFailed;

  return (
    <PageShell width="wide">
      <PageHeader
        title="System health"
        description="What the appliance monitor found on its last run, with client connectivity and history."
        help={
          <>
            <HelpSection title="Where this comes from">
              <p>
                The appliance monitor checks services, resources, the database, enforcement, DHCP, the PMS link, the licence and
                certificates every five minutes, and every 30 minutes adds client activity and device findings. It only reads; it
                never changes anything. This page shows its last report as recorded.
              </p>
            </HelpSection>
            <HelpSection title="Results">
              <HelpList
                items={[
                  <><strong>Passing</strong>: the check read its evidence and found nothing wrong.</>,
                  <><strong>Warning</strong> or <strong>Critical</strong>: something needs attention; an issue is raised only after it persists.</>,
                  <><strong>Not verified</strong>: the evidence could not be read. It is never counted as passing.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Privacy">
              <p>No client addresses are shown here, and hardware addresses are masked to their last two parts.</p>
            </HelpSection>
          </>
        }
        meta={snap && <StatusBadge tone={statusOf(snap.overall).tone}>Appliance: {statusOf(snap.overall).word}</StatusBadge>}
        actions={
          ans ? (
            <LiveStatus updatedAt={updatedAt} refreshing={refreshing} intervalSeconds={POLL_SECONDS} error={loadFailed} onRefresh={() => void load()} />
          ) : undefined
        }
      />

      {ans === null && loadFailed && (
        <div role="alert" className={FADE}>
          <EmptyState
            icon={<AlertTriangle />}
            title="System health could not be loaded"
            hint="Check the appliance is reachable, then try again. Nothing has been changed."
            action={<Button size="sm" variant="secondary" onClick={() => void load()} disabled={refreshing}><RefreshCw className={cn(refreshing && SPIN)} /> Try again</Button>}
          />
        </div>
      )}

      {ans && !ans.available && (
        <EmptyState icon={<Radar />} title="No monitor report yet" hint={UNAVAILABLE[ans.reason ?? ""] ?? UNAVAILABLE.invalid} />
      )}

      {ans?.available && ans.stale && snap && (
        <Callout tone="warning" title="This report is out of date">
          The monitor last reported {ago(snap.at)}. It normally reports every five minutes, so what follows may no longer be true.
        </Callout>
      )}

      {loading && (
        <StatGrid cols={5}>{Array.from({ length: 5 }, (_, i) => <Skeleton key={i} className="h-[4.75rem] rounded-lg" />)}</StatGrid>
      )}

      {snap && (
        <div className={cn("space-y-5", refreshing && refreshingClass)}>
          <StatGrid cols={5}>
            <StatCard label="Active issues" value={snap.incidents.length.toLocaleString()} tone={snap.incidents.some((i) => i.status === "CRITICAL") ? "err" : snap.incidents.length ? "warn" : "ok"}
              hint={`${(snap.counts.NOT_VERIFIED ?? 0).toLocaleString()} not verified`} />
            <StatCard label="Clients online" value={shown(m.online)} hint={`${shown(m.netd_sessions)} enforced by the firewall`} />
            <StatCard label="CPU" value={shown(m.cpu, "%")} hint={`Memory free ${shown(m.mem, " MB")}`} />
            <StatCard label="Disk used" value={shown(m.disk, "%")} />
            <StatCard label="Database deadlocks" value={shown(m.deadlocks_1h)} hint="in the last hour" tone={num(m.deadlocks_1h) > 0 ? "warn" : "default"} />
          </StatGrid>
          <p className="text-caption text-muted-foreground">
            Last {snap.mode === "deep" ? "full" : "quick"} run {ago(snap.at)} ({when(snap.at)}).{" "}
            {deep ? <>Client activity and device findings from {ago(deep.at)}.</> : <>Client activity appears after the first full run.</>}{" "}
            Service controls are on <Link className={LINK} href="/health">Diagnostics</Link>.
          </p>

          <Tabs value={tab} onValueChange={setTab}>
            <TabsList aria-label="System health sections">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="checks">Checks</TabsTrigger>
              <TabsTrigger value="clients">Client connectivity</TabsTrigger>
              <TabsTrigger value="devices">Devices</TabsTrigger>
              <TabsTrigger value="history">History</TabsTrigger>
            </TabsList>

            <TabsContent value="overview" className="mt-5 space-y-5">
              <SectionCard flush title="Active issues">
                {snap.incidents.length === 0 ? (
                  <EmptyState size="sm" icon={<CheckCircle2 />} title="No active issues" hint="Nothing has failed long enough to be raised." />
                ) : (
                  <IncidentTable rows={snap.incidents} />
                )}
              </SectionCard>
              <SectionCard flush title="By area">
                <Table label="Checks by area">
                  <THead><TR><TH>Area</TH><TH>Result</TH><TH numeric>Checks</TH><TH className="hidden md:table-cell">Needs attention</TH></TR></THead>
                  <TBody>
                    {areas.map((a) => {
                      const bad = a.cs.filter((c) => c.status !== "PASS");
                      return (
                        <TR key={a.area}>
                          <TD nowrap className="font-medium">{words(a.area)}</TD>
                          <TD nowrap><StatusPill status={a.worst} /></TD>
                          <TD numeric>{a.cs.length}</TD>
                          <TD className="hidden max-w-[32rem] text-xs text-muted-foreground md:table-cell">
                            <span className="line-clamp-2">{bad.length ? bad.map((c) => c.text).join(" · ") : "None"}</span>
                          </TD>
                        </TR>
                      );
                    })}
                  </TBody>
                </Table>
              </SectionCard>
            </TabsContent>

            <TabsContent value="checks" className="mt-5">
              <SectionCard
                flush
                title="Checks"
                actions={
                  <div className="flex gap-1" role="group" aria-label="Show">
                    <Button size="xs" variant={filter === "issues" ? "secondary" : "ghost"} aria-pressed={filter === "issues"} onClick={() => setFilter("issues")}>Needs attention</Button>
                    <Button size="xs" variant={filter === "all" ? "secondary" : "ghost"} aria-pressed={filter === "all"} onClick={() => setFilter("all")}>All {checks.length}</Button>
                  </div>
                }
              >
                {(() => {
                  const rows = filter === "all" ? checks : checks.filter((c) => c.status !== "PASS");
                  if (!rows.length) return <EmptyState size="sm" icon={<CheckCircle2 />} title="Every check is passing" />;
                  return (
                    <Table label="Checks">
                      <THead><TR><TH>Area</TH><TH>Result</TH><TH>Finding</TH></TR></THead>
                      <TBody>
                        {rows.map((c) => (
                          <TR key={c.key}>
                            <TD nowrap>{words(c.area)}</TD>
                            <TD nowrap><StatusPill status={c.status} /></TD>
                            <TD className="text-xs">{c.text}</TD>
                          </TR>
                        ))}
                      </TBody>
                    </Table>
                  );
                })()}
              </SectionCard>
            </TabsContent>

            <TabsContent value="clients" className="mt-5 space-y-5">
              <StatGrid cols={4}>
                <StatCard label="Sign-ins, last 15 min" value={shown(m.signin)} hint="verified / service unavailable / all" />
                <StatCard label="Automatic reconnections" value={shown(m.rejoined)} hint="in the last 15 min" />
                <StatCard label="Cut off twice or more" value={shown(m.recut)} hint="devices, last 6 h" tone={num(m.recut) > 0 ? "warn" : "default"} />
                <StatCard label="Stuck devices" value={stuckParts(m.stuck)?.[1] ?? shown(m.stuck)} hint={stuckParts(m.stuck) ? `refused 5+ times in 60 min; ${stuckParts(m.stuck)![2]} for a system reason` : "refused 5+ times in 60 min"} />
              </StatGrid>
              {!deep?.breakdowns ? (
                <EmptyState size="sm" icon={<Radar />} title="No client activity yet" hint="It is added by the monitor's full run every 30 minutes." />
              ) : (
                <>
                  <SectionCard title="Last 24 hours, by hour" description="Manual room sign-ins and automatic reconnections, by UTC hour.">
                    <ColumnChart
                      data={deep.breakdowns.hourly.map((r) => ({ label: String(r[0]).slice(-5, -3), values: [num(r[2]) || 0, num(r[3]) || 0] }))}
                      series={[{ name: "Manual sign-ins", token: "chart-1" }, { name: "Automatic reconnections", token: "chart-2" }]}
                    />
                  </SectionCard>
                  <div className="grid gap-5 lg:grid-cols-2">
                    <SectionCard title="Sessions ended, 24 h" description="By reason.">
                      <BarList items={pairs(deep.breakdowns.ends_24h)} />
                    </SectionCard>
                    <SectionCard title="Sign-in results, 24 h" description="Room sign-in attempts by result.">
                      <BarList items={pairs(deep.breakdowns.results_24h)} />
                    </SectionCard>
                  </div>
                  <SectionCard flush title="Per network, 24 h">
                    <RowsTable label="Per network" headers={["Network", "Devices", "Sessions", "Manual", "Automatic", "Address-loss ends", "Online now"]} rows={deep.breakdowns.networks_24h} />
                  </SectionCard>
                </>
              )}
              {deep?.analytics && (
                <SectionCard flush title="Compared with the previous period" description="Each window against the one before it.">
                  <Table label="Client activity by window">
                    <THead>
                      <TR><TH>Measure</TH>{deep.analytics.windows.map((w) => <TH key={w.label} numeric>{w.label}</TH>)}</TR>
                    </THead>
                    <TBody>
                      {deep.analytics.measures.map((name, i) => (
                        <TR key={name}>
                          <TD className={cn("text-xs", name.startsWith("    ") ? "pl-10" : name.startsWith("  ") ? "pl-6" : "font-medium")}>{name.trim()}</TD>
                          {deep.analytics!.windows.map((w) => (
                            <TD key={w.label} numeric className="text-xs">
                              {w.now[i]?.toLocaleString()} <span className="text-muted-foreground">/ {w.previous[i]?.toLocaleString()}</span>
                            </TD>
                          ))}
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                </SectionCard>
              )}
            </TabsContent>

            <TabsContent value="devices" className="mt-5">
              <SectionCard flush title="Device findings, last 24 hours" description="Hardware addresses masked; client addresses are not shown.">
                {!deep?.devices ? (
                  <EmptyState size="sm" icon={<Radar />} title="No device findings yet" hint="They are added by the monitor's full run every 30 minutes." />
                ) : deep.devices.length === 0 ? (
                  <EmptyState size="sm" icon={<CheckCircle2 />} title="No device needs attention" />
                ) : (
                  <Table label="Device findings">
                    <THead>
                      <TR>
                        <TH>Severity</TH><TH>Device</TH><TH>Issue</TH><TH className="hidden lg:table-cell">Likely cause</TH>
                        <TH className="hidden md:table-cell">Network</TH><TH numeric>Count</TH><TH className="hidden sm:table-cell">Last</TH><TH>Now</TH>
                      </TR>
                    </THead>
                    <TBody>
                      {deep.devices.map((d) => (
                        <TR key={`${d.device}|${d.issue}`}>
                          <TD nowrap><StatusBadge tone={d.severity === "HIGH" ? "err" : d.severity === "MEDIUM" ? "warn" : "default"}>{d.severity.toLowerCase()}</StatusBadge></TD>
                          <TD nowrap className="font-mono text-xs">{d.mac}<div className="text-caption text-muted-foreground">{d.device}</div></TD>
                          <TD className="text-xs">{d.issue}<div className="text-caption text-muted-foreground lg:hidden">{d.cause}</div></TD>
                          <TD className="hidden max-w-[22rem] text-xs text-muted-foreground lg:table-cell"><span className="line-clamp-2" title={d.cause}>{d.cause}</span><div className="line-clamp-2 text-caption" title={d.action}>Next: {d.action}</div></TD>
                          <TD nowrap className="hidden text-xs md:table-cell">{d.network || "Not recorded"}</TD>
                          <TD numeric className="text-xs">{d.count}</TD>
                          <TD nowrap muted className="hidden text-xs sm:table-cell">{d.last}</TD>
                          <TD nowrap><StatusBadge tone={d.status === "ONGOING" ? "warn" : "ok"}>{d.status === "ONGOING" ? "Ongoing" : "Back online"}</StatusBadge></TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                )}
              </SectionCard>
            </TabsContent>

            <TabsContent value="history" className="mt-5 space-y-5">
              <SectionCard flush title="Resolved issues" description="The most recent first.">
                {snap.resolved.length === 0 ? (
                  <EmptyState size="sm" title="Nothing resolved yet" />
                ) : (
                  <Table label="Resolved issues">
                    <THead><TR><TH>Issue</TH><TH>Was</TH><TH className="hidden sm:table-cell">From</TH><TH>Cleared</TH></TR></THead>
                    <TBody>
                      {snap.resolved.map((h) => (
                        <TR key={`${h.key}|${h.cleared}`}>
                          <TD className="text-xs">{h.text ?? h.key}</TD>
                          <TD nowrap>{h.status ? <StatusPill status={h.status} /> : "Not recorded"}</TD>
                          <TD nowrap muted className="hidden text-xs sm:table-cell">{when(h.first)}</TD>
                          <TD nowrap muted className="text-xs">{when(h.cleared)}</TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                )}
              </SectionCard>
              {deep?.breakdowns && (
                <SectionCard flush title="Daily, last 14 days" description="Compare a day with the same weekday a week before.">
                  <RowsTable label="Daily" headers={["Day (UTC)", "Devices", "Manual sign-ins", "Automatic", "Address-loss ends", "Refused attempts"]} rows={deep.breakdowns.daily} />
                </SectionCard>
              )}
              {deep && (
                <div className="grid gap-5 lg:grid-cols-2">
                  <SectionCard title="Database deadlocks per day">
                    <BarList items={deep.deadlocks_daily.map(([d, c]) => ({ name: d, value: c, key: d, tone: c > 0 ? "warn" as const : undefined }))} emptyLabel="None recorded" />
                  </SectionCard>
                  <SectionCard title="Lock waits over 1 s" description="By table, per day.">
                    {Object.keys(deep.lock_waits).length === 0 ? (
                      <p className="text-sm text-muted-foreground">None recorded</p>
                    ) : (
                      <ul className="space-y-1 text-xs">
                        {Object.entries(deep.lock_waits).reverse().map(([d, t]) => (
                          <li key={d}><span className="font-medium">{d}</span>: {Object.entries(t).map(([k, v]) => `${k} ${v}`).join(", ") || "none"}</li>
                        ))}
                      </ul>
                    )}
                  </SectionCard>
                </div>
              )}
            </TabsContent>
          </Tabs>
        </div>
      )}
      {loading && <SkeletonRows rows={6} cols={4} />}
    </PageShell>
  );
}

function IncidentTable({ rows }: { rows: Incident[] }) {
  return (
    <Table label="Active issues">
      <THead><TR><TH>Result</TH><TH>Area</TH><TH>Issue</TH><TH className="hidden sm:table-cell">Since</TH><TH numeric className="hidden md:table-cell">Runs</TH></TR></THead>
      <TBody>
        {rows.map((i) => (
          <TR key={i.key}>
            <TD nowrap><StatusPill status={i.status} /></TD>
            <TD nowrap>{words(i.area)}</TD>
            <TD className="text-xs">{i.text}</TD>
            <TD nowrap muted className="hidden text-xs sm:table-cell">{ago(i.first)}</TD>
            <TD numeric className="hidden text-xs md:table-cell">{i.count}</TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}

function RowsTable({ label, headers, rows }: { label: string; headers: string[]; rows: Row[] }) {
  if (!rows.length) return <EmptyState size="sm" title="Nothing recorded" />;
  return (
    <Table label={label}>
      <THead><TR>{headers.map((h, i) => <TH key={h} numeric={i > 0}>{h}</TH>)}</TR></THead>
      <TBody>
        {rows.map((r) => (
          <TR key={String(r[0])}>
            {r.map((c, i) => <TD key={i} numeric={i > 0} nowrap={i === 0} className="text-xs">{i > 0 ? (num(c) || 0).toLocaleString() : String(c)}</TD>)}
          </TR>
        ))}
      </TBody>
    </Table>
  );
}
