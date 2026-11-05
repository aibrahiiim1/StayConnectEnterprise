"use client";

// A block whose section is unavailable says why and draws no figure, never a zero for "could not be read".

import Link from "next/link";
import { ChevronRight, Cpu, Gauge, HardDrive, Hotel, Info, MemoryStick, Network, Package, Router, Wifi } from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { QuietDescription } from "@/components/ui/quiet";
import { Tooltip } from "@/components/ui/tooltip";
import { Badge, StatusDot } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Meter, Metric, Skeleton } from "@/components/ui/misc";
import { AreaChart, BarList, ColumnChart, Heatmap, SplitBar } from "@/components/ui/chart";
import { KeyValueGrid, MetricStrip } from "@/components/ui/data";
import { Table, THead, TBody, TR, TH, TD, TableWrap } from "@/components/ui/table";
import { cn, formatBytes, formatRelative } from "@/lib/utils";
import { describeLicense, describePmsReadiness } from "@/lib/health-words";
import {
  type Block, type OverviewSnapshot, type OverviewRange, bucketHour, bucketLabel, fmtInt, formatDuration, methodLabel,
  notRecordedLabel, reasonText, resultLabel, serviceState, terminationLabel,
} from "@/lib/api/dashboard";

export function Unavailable({ block, className }: { block: Block; className?: string }) {
  return (
    <div
      role="note"
      data-testid="block-unavailable"
      className={cn("flex items-start gap-2 rounded-md border border-dashed border-border bg-surface/40 px-3 py-2.5 text-sm text-muted-foreground", className)}
    >
      <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span>
        <span className="font-medium text-foreground">Not available.</span> {reasonText(block.reason)}
      </span>
    </div>
  );
}

export function BlockLink({ href, label }: { href: string; label: string }) {
  return (
    <Link
      href={href}
      className="inline-flex h-7 shrink-0 items-center gap-1 rounded-md px-2 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring pointer-coarse:min-h-11"
    >
      {label}
      <ChevronRight className="size-3.5 rtl:rotate-180" aria-hidden />
    </Link>
  );
}

function BlockCard({
  title, description, href, linkLabel, children, className, bodyClassName,
}: {
  title: string;
  description?: React.ReactNode;
  href?: string;
  linkLabel?: string;
  children: React.ReactNode;
  className?: string;
  bodyClassName?: string;
}) {
  return (
    <Card className={cn("min-w-0", className)}>
      <CardHeader>
        <div className="flex min-w-0 items-center gap-1">
          <CardTitle>{title}</CardTitle>
          <QuietDescription>{description}</QuietDescription>
        </div>
        {href && <BlockLink href={href} label={linkLabel ?? "Open"} />}
      </CardHeader>
      <CardBody className={bodyClassName}>{children}</CardBody>
    </Card>
  );
}

const rangeWords: Record<OverviewRange, string> = { "24h": "last 24 hours", "7d": "last 7 days", "30d": "last 30 days" };

function points(snap: OverviewSnapshot, ...series: number[][]) {
  return snap.buckets.map((b, i) => ({ label: bucketLabel(b, snap.range, snap.timezone), values: series.map((s) => s[i] ?? 0) }));
}

export function TrafficCard({ snap }: { snap: OverviewSnapshot | null }) {
  const t = snap?.traffic;
  return (
    <BlockCard
      title="Internet traffic"
      description={snap ? `Download and upload, ${rangeWords[snap.range]}.` : undefined}
      href="/usage"
      linkLabel="Usage"
    >
      {!snap || !t ? (
        <Skeleton className="h-48" />
      ) : !t.available ? (
        <Unavailable block={t} />
      ) : (
        <div className="space-y-4">
          <MetricStrip
            className="sm:grid-cols-3"
            items={[
              { label: "Total", value: formatBytes(t.bytes_down + t.bytes_up) },
              { label: "Download", value: formatBytes(t.bytes_down) },
              { label: "Upload", value: formatBytes(t.bytes_up) },
            ]}
          />
          <AreaChart
            data={points(snap, t.series_down, t.series_up)}
            series={[{ name: "Download" }, { name: "Upload" }]}
            formatValue={(n) => formatBytes(n)}
            emptyLabel="No traffic was recorded in this period."
          />
        </div>
      )}
    </BlockCard>
  );
}

export function ConcurrencyCard({ snap }: { snap: OverviewSnapshot | null }) {
  const c = snap?.guests.concurrency;
  const g = snap?.guests;
  return (
    <BlockCard
      title="Connected devices"
      description="How many devices were online at once."
      href="/sessions"
      linkLabel="Sessions"
    >
      {!snap || !g || !c ? (
        <Skeleton className="h-48" />
      ) : !g.available ? (
        <Unavailable block={g} />
      ) : !c.available ? (
        <Unavailable block={c} />
      ) : (
        <div className="space-y-4">
          <MetricStrip
            className="sm:grid-cols-3"
            items={[
              { label: "Online now", value: fmtInt(g.devices_online) },
              { label: "Peak in range", value: fmtInt(c.peak_in_range) },
              { label: "Different devices", value: fmtInt(g.unique_devices) },
            ]}
          />
          <AreaChart
            data={points(snap, c.peak, c.average)}
            series={[{ name: "Peak at once" }, { name: "Average" }]}
            formatValue={(n) => (Number.isInteger(n) ? n.toLocaleString() : n.toFixed(1))}
            emptyLabel="No device was online in this period."
          />
          <p className="text-xs text-muted-foreground">{c.definition}</p>
        </div>
      )}
    </BlockCard>
  );
}

export function SignInOutcomesCard({ snap, rooms = true }: { snap: OverviewSnapshot | null; rooms?: boolean }) {
  const s = snap?.sign_in_outcomes;
  const g = snap?.guests;
  const failed = s ? s.total - s.verified : 0;
  // Room checks are a hospitality figure; sites without Hospitality see methods only.
  return (
    <BlockCard
      title="Sign-in outcomes"
      description={snap
        ? rooms
          ? `By method, and room checks, ${rangeWords[snap.range]}.`
          : `By method, ${rangeWords[snap.range]}.`
        : undefined}
      href={rooms ? "/guest-signin-attempts" : "/sessions"}
      linkLabel={rooms ? "Attempts" : "Sessions"}
    >
      {!snap || !s || !g ? (
        <Skeleton className="h-48" />
      ) : (
        <div className="space-y-5">
          <section className="space-y-2">
            <h3 className="text-xs font-medium text-muted-foreground">Successful sign-ins by method</h3>
            {!g.available ? (
              <Unavailable block={g} />
            ) : (
              <BarList
                items={g.sign_ins_by_method.map((m) => ({ key: m.method, name: methodLabel(m.method), value: m.total }))}
                emptyLabel="Nobody signed in during this period."
              />
            )}
          </section>

          {rooms && (<section className="space-y-2">
            <h3 className="text-xs font-medium text-muted-foreground">Room sign-in checks</h3>
            {!s.available ? (
              <Unavailable block={s} />
            ) : s.total === 0 ? (
              <p className="text-sm text-muted-foreground">Nobody tried to sign in with a room number in this period.</p>
            ) : (
              <>
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <span className="text-2xl font-semibold tabular">{Math.round((s.verified / s.total) * 100)}%</span>
                  <span className="text-sm text-muted-foreground">
                    verified — {fmtInt(s.verified)} of {fmtInt(s.total)} attempts
                  </span>
                </div>
                <SplitBar
                  total={s.total}
                  parts={[
                    { name: "Verified", value: s.verified, tone: "ok" },
                    { name: "Not verified", value: failed, tone: failed > 0 ? "err" : "neutral" },
                  ]}
                />
                {failed > 0 && (
                  <BarList
                    items={s.by_result
                      .filter((r) => r.result !== "VERIFIED")
                      .map((r) => ({ key: r.result, name: resultLabel(r.result), value: r.count, tone: "warn" as const }))}
                  />
                )}
                {s.latency.samples > 0 && s.latency.p50_ms != null && (
                  <p className="text-xs text-muted-foreground">
                    Check time: median {Math.round(s.latency.p50_ms)} ms
                    {s.latency.p95_ms != null && <> · 95th percentile {Math.round(s.latency.p95_ms)} ms</>}
                    {" "}({fmtInt(s.latency.samples)} timed checks)
                  </p>
                )}
              </>
            )}
          </section>)}

          {s.not_recorded.length > 0 && (
            <p data-testid="not-recorded" className="rounded-md bg-surface/60 px-3 py-2 text-xs text-muted-foreground">
              <span className="font-medium text-foreground">Not recorded on this appliance:</span>{" "}
              {s.not_recorded.map(notRecordedLabel).join(", ")}. They cannot be counted, so they are not shown —
              an absence here is not evidence that none happened.
            </p>
          )}
        </div>
      )}
    </BlockCard>
  );
}

export function SignInPatternCard({ snap }: { snap: OverviewSnapshot | null }) {
  const g = snap?.guests;
  const hourly = snap?.range === "24h";
  return (
    <BlockCard
      title="When clients sign in"
      description={hourly ? "Sign-ins per hour, by method." : "Sign-ins by day of week and hour, in the appliance's local time."}
    >
      {!snap || !g ? (
        <Skeleton className="h-48" />
      ) : !g.available ? (
        <Unavailable block={g} />
      ) : hourly ? (
        <ColumnChart
          data={snap.buckets.map((b, i) => ({
            label: bucketHour(b, snap.timezone),
            values: g.sign_ins_by_method.map((m) => m.series[i] ?? 0),
          }))}
          series={g.sign_ins_by_method.length ? g.sign_ins_by_method.map((m) => ({ name: methodLabel(m.method) })) : [{ name: "Sign-ins" }]}
          tickEvery={3}
          emptyLabel="Nobody signed in during this period."
        />
      ) : g.sign_ins === 0 ? (
        <p className="text-sm text-muted-foreground">Nobody signed in during this period.</p>
      ) : (
        <Heatmap rows={g.heatmap.rows} columns={g.heatmap.columns} values={g.heatmap.values} />
      )}
    </BlockCard>
  );
}

export function PackagesCard({ snap }: { snap: OverviewSnapshot | null }) {
  const p = snap?.packages;
  const t = snap?.traffic;
  return (
    <BlockCard title="Packages in use" description="Still valid now, and given out in this range." href="/internet-packages" linkLabel="Packages">
      {!snap || !p ? (
        <Skeleton className="h-48" />
      ) : !p.available ? (
        <Unavailable block={p} />
      ) : (
        <div className="space-y-5">
          <section className="space-y-2">
            <h3 className="text-xs font-medium text-muted-foreground">Still valid</h3>
            {p.active_now.length === 0 ? (
              <EmptyState className="py-6" icon={<Package />} title="No active packages" hint="No client currently holds an internet package." />
            ) : (
              <BarList items={p.active_now.map((r) => ({ key: r.package_id || "none", name: r.name || "Without a package", hint: r.code && r.code !== r.name ? r.code : undefined, value: r.count }))} />
            )}
          </section>
          <section className="space-y-2">
            <h3 className="text-xs font-medium text-muted-foreground">Given out, {rangeWords[snap.range]}</h3>
            <BarList
              items={p.grants.map((r) => ({ key: r.package_id || "none", name: r.name || "Without a package", value: r.count }))}
              emptyLabel="No package was given out in this period."
            />
          </section>
          {t?.available && t.top_packages.length > 0 && (
            <section className="space-y-2">
              <h3 className="text-xs font-medium text-muted-foreground">Most data used, by package</h3>
              <BarList
                items={t.top_packages.map((r) => ({ key: r.key || "none", name: r.name || "Without a package", value: r.bytes_down + r.bytes_up }))}
                formatValue={formatBytes}
              />
            </section>
          )}
          {p.terminations.length > 0 && (
            <section className="space-y-2">
              <h3 className="text-xs font-medium text-muted-foreground">Why access ended</h3>
              <ul className="flex flex-wrap gap-1.5">
                {p.terminations.map((r) => (
                  <li key={r.reason}>
                    <Badge tone="neutral">{terminationLabel(r.reason)} · {fmtInt(r.count)}</Badge>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </div>
      )}
    </BlockCard>
  );
}

export function PmsCard({ snap, canCharges }: { snap: OverviewSnapshot | null; canCharges: boolean }) {
  const pms = snap?.pms;
  // Only connections that can serve a guest are listed; a switched-off, never-configured one is counted but not shown.
  const inUse = (pms?.interfaces ?? []).filter((i) => i.lifecycle_state === "ACTIVE" || i.published);
  const neverConfigured = (pms?.interfaces.length ?? 0) - inUse.length;
  return (
    <BlockCard
      title="Property Management System"
      description="Whether a client can sign in with their room number and name."
      href="/pms-interfaces"
      linkLabel="PMS connection"
    >
      {!snap || !pms ? (
        <Skeleton className="h-48" />
      ) : !pms.available || inUse.length === 0 ? (
        <EmptyState
          className="py-8"
          icon={<Hotel />}
          title="No PMS connection is configured"
          hint="Clients can still sign in with vouchers or client accounts. Room sign-in needs a PMS."
          action={<Link href="/pms-interfaces" className={buttonVariants({ variant: "secondary", size: "sm" })}>Set one up</Link>}
        />
      ) : (
        <div className="space-y-4">
          {inUse.map((i) => {
            const words = describePmsReadiness({
              transport: i.transport_status, sync: i.sync_status, roomAuthReady: i.room_auth_ready, source: i.room_auth_source, inHouse: i.in_house_stays,
            });
            const last = i.last_heartbeat_at ?? i.last_stay_event_at;
            return (
              <div key={i.pms_interface_id} className="rounded-md border border-border p-3.5">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <StatusDot tone={words.tone === "default" ? "default" : words.tone} />
                    <span className="truncate text-sm font-medium">{i.display_label || "PMS connection"}</span>
                    {i.lifecycle_state !== "ACTIVE" && <Badge tone="default">Not in use</Badge>}
                  </div>
                  <Badge tone={words.tone === "default" ? "default" : words.tone} dot>{words.headline}</Badge>
                </div>
                <p className="mt-1 text-xs text-muted-foreground">{words.summary}</p>
                <div className="mt-3 grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-4">
                  <Metric label="In house" value={fmtInt(i.in_house_stays)} />
                  <Metric label="Waiting to apply" value={fmtInt(i.pending_events)} tone={i.pending_events > 0 ? "warn" : "default"} />
                  <Metric label="Needs review" value={fmtInt(i.review_events)} tone={i.review_events > 0 ? "warn" : "default"} />
                  <Metric
                    label="Last heard from"
                    value={<span className="text-sm" title={last ? new Date(last).toLocaleString() : undefined}>{formatRelative(last)}</span>}
                  />
                </div>
              </div>
            );
          })}
          {neverConfigured > 0 && (
            <p className="text-xs text-muted-foreground">
              {neverConfigured === 1
                ? "1 further connection is set up but has never been configured; it is not connected to any PMS."
                : `${neverConfigured} further connections are set up but have never been configured; they are not connected to any PMS.`}{" "}
              <Link href="/pms-interfaces" className="underline underline-offset-2 hover:text-foreground pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">Review</Link>
            </p>
          )}
          {pms.occupancy.available && (
            <MetricStrip
              items={[
                { label: "In house", value: fmtInt(pms.occupancy.in_house) },
                { label: "With internet", value: fmtInt(pms.occupancy.with_internet) },
                { label: "Arriving today", value: fmtInt(pms.occupancy.arrivals_today) },
                { label: "Departing today", value: fmtInt(pms.occupancy.departures_today) },
              ]}
            />
          )}
          <div className="flex items-center gap-1 text-xs text-muted-foreground">
            <span>{fmtInt(pms.events_today)} PMS messages today</span>
            <QuietDescription>
              {fmtInt(pms.events_applied_today)} applied automatically
              {pms.events_needing_review > 0 ? `, ${fmtInt(pms.events_needing_review)} waiting for a decision` : ""}.
            </QuietDescription>
          </div>
          {pms.postings.available && (
            <div className="space-y-2 border-t border-border pt-3">
              <div className="flex items-center justify-between gap-2">
                <h3 className="text-xs font-medium text-muted-foreground">Room charges posted to the PMS</h3>
                {canCharges && <BlockLink href="/financial-health" label="Charge health" />}
              </div>
              <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                <Metric label="Posted today" value={fmtInt(pms.postings.posted_today)} />
                <Metric label="Rejected today" value={fmtInt(pms.postings.failed_today)} tone={pms.postings.failed_today > 0 ? "err" : "default"} />
                <Metric label="Awaiting review" value={fmtInt(pms.postings.review_open)} tone={pms.postings.review_open > 0 ? "warn" : "default"} />
                <Metric label="Outcome unknown" value={fmtInt(pms.postings.unknown_open)} tone={pms.postings.unknown_open > 0 ? "warn" : "default"} />
              </div>
            </div>
          )}
        </div>
      )}
    </BlockCard>
  );
}

const dhcpWords: Record<string, string> = {
  local: "DHCP by this appliance", relay: "DHCP relayed", external: "DHCP elsewhere", disabled: "No DHCP",
};

export function NetworksCard({ snap }: { snap: OverviewSnapshot | null }) {
  const nb = snap?.networks;
  return (
    <Card className="min-w-0">
      <CardHeader>
        <div className="flex min-w-0 items-center gap-1">
          <CardTitle>Client networks</CardTitle>
          <QuietDescription>
            Address use comes from the DHCP server&rsquo;s live leases; devices online from active sessions. Subnets and
            VLANs are on the Networking page.
          </QuietDescription>
        </div>
        <BlockLink href="/network" label="Networking" />
      </CardHeader>
      {!snap || !nb ? (
        <CardBody><Skeleton className="h-32" /></CardBody>
      ) : !nb.available ? (
        <CardBody><Unavailable block={nb} /></CardBody>
      ) : nb.rows.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<Network />}
            title="No client network is configured"
            hint="No device can be put online until at least one client network exists."
            action={<Link href="/network/new" className={buttonVariants({ variant: "secondary", size: "sm" })}>Create one</Link>}
          />
        </CardBody>
      ) : (
        <TableWrap>
          {nb.leases_reason && (
            <p className="border-b border-border px-5 py-2 text-xs text-muted-foreground">
              Address pool use is not shown: {reasonText(nb.leases_reason)}
            </p>
          )}
          <Table>
            <THead>
              <TR>
                <TH>Network</TH>
                <TH>Room sign-in</TH>
                <TH className="min-w-44">Address use</TH>
                <TH numeric>Devices online</TH>
                <TH numeric>Traffic</TH>
              </TR>
            </THead>
            <TBody>
              {nb.rows.map((n) => (
                <TR key={n.id}>
                  <TD>
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{n.name}</span>
                      {!n.enabled && <Badge tone="default">Switched off</Badge>}
                    </div>
                  </TD>
                  <TD>
                    {n.room_sign_in_policy && n.room_sign_in_policy !== "NONE" && (n.room_sign_in_connections?.length ?? 0) > 0 ? (
                      <div className="flex flex-wrap gap-1">
                        {n.room_sign_in_connections!.map((c) => <Badge key={c} tone="info" className="px-1.5 text-2xs">{c}</Badge>)}
                      </div>
                    ) : (
                      <span className="text-xs text-muted-foreground">—</span>
                    )}
                  </TD>
                  <TD>
                    {n.dhcp_mode !== "local" ? (
                      <span className="text-xs text-muted-foreground">{dhcpWords[n.dhcp_mode] ?? n.dhcp_mode}</span>
                    ) : n.pool_size <= 0 ? (
                      <span className="text-xs text-muted-foreground">
                        {n.invalid_pools ? "The configured range is not valid." : "No address range is configured."}
                      </span>
                    ) : n.leases_known && n.leases_in_pool != null ? (
                      <Meter
                        value={n.leases_in_pool}
                        max={n.pool_size}
                        name={`${n.name} address use`}
                        caption={`${fmtInt(n.leases_in_pool)} / ${fmtInt(n.pool_size)}${n.utilisation_pct != null ? ` · ${n.utilisation_pct}%` : ""}`}
                        label={n.expiring_soon ? `${fmtInt(n.expiring_soon)} expiring soon` : "Leased"}
                      />
                    ) : (
                      <span className="text-xs text-muted-foreground">{fmtInt(n.pool_size)} addresses · use not known</span>
                    )}
                  </TD>
                  <TD numeric>{fmtInt(n.devices_online)}</TD>
                  <TD numeric>
                    {n.traffic_known ? (
                      <span title={`${formatBytes(n.bytes_down)} down · ${formatBytes(n.bytes_up)} up`}>{formatBytes(n.bytes_down + n.bytes_up)}</span>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </TableWrap>
      )}
    </Card>
  );
}

export function DhcpDnsCard({ snap }: { snap: OverviewSnapshot | null }) {
  const d = snap?.dhcp;
  const dns = snap?.dns;
  return (
    <BlockCard title="Addresses and names" description="The DHCP server that hands out addresses and the DNS resolver clients use." href="/network/dhcp" linkLabel="DHCP">
      {!snap || !d || !dns ? (
        <Skeleton className="h-24" />
      ) : (
        <ul className="divide-y divide-border">
          <li className="flex min-h-11 items-center justify-between gap-3 py-2">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <Router className="size-4 text-muted-foreground" aria-hidden /> DHCP
              {d.available && (
                <QuietDescription>
                  {d.captive_option_networks.length > 0
                    ? `Sign-in page advertised by DHCP on: ${d.captive_option_networks.join(", ")}.`
                    : d.local_dhcp_networks > 0
                      ? "No network advertises the sign-in page through DHCP."
                      : "No client network gets its addresses from this appliance."}
                  {d.leases_available && d.expiring_soon > 0 ? ` ${fmtInt(d.expiring_soon)} leases expire within ${Math.round(d.expiring_within_seconds / 60)} min.` : ""}
                  {d.server_healthy === false && d.server_detail ? ` ${d.server_detail}` : ""}
                </QuietDescription>
              )}
            </span>
            {!d.available ? (
              <span className="text-xs text-muted-foreground">{reasonText(d.reason)}</span>
            ) : (
              <span className="flex flex-wrap items-center justify-end gap-2">
                {d.leases_available && <span className="text-xs text-muted-foreground tabular">{fmtInt(d.active_leases)} leases</span>}
                {d.server_healthy == null ? (
                  <Badge tone="default">Unknown</Badge>
                ) : !d.server_configured ? (
                  <Badge tone="default">Not needed yet</Badge>
                ) : (
                  <Badge tone={d.server_healthy ? "ok" : "err"} dot>{d.server_healthy ? "Healthy" : "Not healthy"}</Badge>
                )}
              </span>
            )}
          </li>
          <li className="flex min-h-11 items-center justify-between gap-3 py-2">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <Wifi className="size-4 text-muted-foreground" aria-hidden /> DNS
              {dns.available && (
                <QuietDescription>
                  {dns.networks.length > 0
                    ? dns.networks.map((n) => `${n.network}: ${n.mode === "custom" ? `custom servers ${n.servers.join(", ") || "(none listed)"}` : "this appliance's resolver"}`).join("; ") + "."
                    : "Clients use this appliance's resolver."}
                  {dns.last_healthy_at ? ` Last answered ${formatRelative(dns.last_healthy_at)}.` : ""}
                  {!dns.statistics_collected ? " Query and cache statistics are not collected on this appliance." : ""}
                </QuietDescription>
              )}
            </span>
            {!dns.available ? (
              <span className="text-xs text-muted-foreground">{reasonText(dns.reason)}</span>
            ) : (
              <Badge tone={serviceState(dns.resolver_state).tone} dot>{serviceState(dns.resolver_state).label}</Badge>
            )}
          </li>
        </ul>
      )}
    </BlockCard>
  );
}

/** The appliance at a glance: one strip at the top of the Overview. */
export function ApplianceCard({ snap, licenseHeadline }: { snap: OverviewSnapshot | null; licenseHeadline?: string }) {
  const a = snap?.appliance;
  const res = a?.resources;
  const net = a?.network;
  const lic = a?.license;
  const licWords = lic?.available ? describeLicense(lic.state) : null;
  const disk = res?.available ? res.disks.find((d) => !d.same_filesystem_as) : undefined;
  return (
    <Card className="min-w-0" data-testid="appliance-strip">
      <CardHeader>
        <div className="flex min-w-0 items-center gap-2">
          <CardTitle>Appliance</CardTitle>
          {licWords ? (
            <Badge tone={licWords.tone === "default" ? "default" : licWords.tone} dot>{licWords.headline}</Badge>
          ) : (
            licenseHeadline && <Badge tone="default">{licenseHeadline}</Badge>
          )}
          {lic?.valid_until && (
            <span className="hidden text-xs text-muted-foreground sm:inline">until {new Date(lic.valid_until).toLocaleDateString()}</span>
          )}
        </div>
        <BlockLink href="/appliance" label="Appliance" />
      </CardHeader>
      <CardBody>
        {!snap || !a ? (
          <Skeleton className="h-16" />
        ) : (
          <div className="grid gap-x-8 gap-y-4 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
            <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
              <Fact label="Internet (WAN)">
                {!net?.available ? (
                  <span className="text-muted-foreground">{reasonText(net?.reason)}</span>
                ) : (
                  <span className="inline-flex items-center gap-1.5">
                    {net.internet_reachable != null && (
                      <Tooltip content={net.internet_reachable ? "Internet reachable" : "Internet not reachable"}>
                        <button type="button" aria-label={net.internet_reachable ? "Internet reachable" : "Internet not reachable"}
                          className="inline-flex rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                          <StatusDot tone={net.internet_reachable ? "ok" : "err"} />
                        </button>
                      </Tooltip>
                    )}
                    <span className="font-mono text-xs">{net.wan_address || "no address"}</span>
                  </span>
                )}
              </Fact>
              <Fact label="Local network (LAN)">
                <span className="font-mono text-xs">{net?.available ? net.lan_address || "no address" : "—"}</span>
              </Fact>
              <Fact label="Version"><span className="font-mono text-xs">{a.version || "—"}</span></Fact>
              <Fact label="Up for">{res?.available ? formatDuration(res.uptime_seconds) : "—"}</Fact>
            </dl>
            {!res || !res.available ? (
              <Unavailable block={res ?? { available: false }} />
            ) : (
              <div className="grid grid-cols-3 gap-4" data-testid="resources">
                {res.load && (
                  <Meter
                    value={res.load.one_per_cpu}
                    max={1}
                    name="CPU load"
                    label={<span className="inline-flex items-center gap-1"><Cpu className="size-3" aria-hidden /> CPU</span>}
                    caption={`load ${res.load.one.toFixed(2)}`}
                  />
                )}
                {res.memory && (
                  <Meter
                    value={res.memory.used_pct}
                    max={100}
                    name="Memory used"
                    label={<span className="inline-flex items-center gap-1"><MemoryStick className="size-3" aria-hidden /> Memory</span>}
                    caption={`${res.memory.used_pct}%`}
                  />
                )}
                {disk && (
                  <Meter
                    value={disk.used_pct}
                    max={100}
                    name="Disk used"
                    label={<span className="inline-flex items-center gap-1"><HardDrive className="size-3" aria-hidden /> Disk</span>}
                    caption={`${formatBytes(disk.available_bytes)} free`}
                  />
                )}
              </div>
            )}
          </div>
        )}
      </CardBody>
    </Card>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="truncate text-xs font-medium text-muted-foreground">{label}</dt>
      <dd className="mt-1 truncate text-sm">{children}</dd>
    </div>
  );
}

export function ServicesGrid({ snap }: { snap: OverviewSnapshot | null }) {
  const sv = snap?.services;
  if (!snap || !sv) return <Skeleton className="h-32" />;
  if (!sv.available) return <Unavailable block={sv} />;
  return (
    <ul className="grid grid-cols-1 gap-x-6 sm:grid-cols-2" data-testid="services-grid">
      {sv.services.map((s) => {
        const st = serviceState(s.state);
        return (
          <li key={s.name} className="flex min-h-9 min-w-0 items-center justify-between gap-2 border-b border-border/60 py-1.5">
            <div className="flex min-w-0 items-center gap-2">
              <StatusDot tone={st.tone} />
              <span className="min-w-0 text-sm leading-snug [overflow-wrap:anywhere]">{s.label}</span>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              {s.failures_in_range > 0 && (
                <span className="text-2xs text-muted-foreground" title="Failures detected by the health monitor in the selected range">
                  {fmtInt(s.failures_in_range)} failure{s.failures_in_range === 1 ? "" : "s"}
                </span>
              )}
              <Badge tone={st.tone} className="px-1.5 text-2xs">{st.label}</Badge>
            </div>
          </li>
        );
      })}
    </ul>
  );
}
