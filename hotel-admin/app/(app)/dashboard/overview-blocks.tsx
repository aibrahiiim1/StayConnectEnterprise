"use client";

// THE OVERVIEW'S BLOCKS.
//
// Each block renders one section of GET /reports/overview and follows one rule: when its section is not
// available, it says why, in the block's own place, and draws NO figure. A zero that stands in for "could not be
// read" is the single most misleading thing an operator screen can show, so an unavailable block never reaches
// the code that formats numbers.

import Link from "next/link";
import { Cpu, Gauge, HardDrive, Hotel, Info, MemoryStick, Network, Package, Router, Wifi } from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
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
  type Block, type OverviewSnapshot, type OverviewRange, bucketLabel, fmtInt, formatDuration, methodLabel,
  notRecordedLabel, reasonText, resultLabel, serviceState, terminationLabel,
} from "@/lib/api/dashboard";

// ---------------------------------------------------------------------------------------------------------
// shared pieces
// ---------------------------------------------------------------------------------------------------------

/** The one way a block says it has nothing truthful to show. */
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
        <div className="min-w-0">
          <CardTitle>{title}</CardTitle>
          {description && <CardDescription className="mt-0.5 text-xs">{description}</CardDescription>}
        </div>
        {href && (
          <Link href={href} className="shrink-0 text-xs text-muted-foreground hover:text-foreground">
            {linkLabel ?? "Open"} →
          </Link>
        )}
      </CardHeader>
      <CardBody className={bodyClassName}>{children}</CardBody>
    </Card>
  );
}

const rangeWords: Record<OverviewRange, string> = { "24h": "last 24 hours", "7d": "last 7 days", "30d": "last 30 days" };

function points(snap: OverviewSnapshot, ...series: number[][]) {
  return snap.buckets.map((b, i) => ({ label: bucketLabel(b, snap.range), values: series.map((s) => s[i] ?? 0) }));
}

// ---------------------------------------------------------------------------------------------------------
// row 3: traffic and concurrency
// ---------------------------------------------------------------------------------------------------------

export function TrafficCard({ snap }: { snap: OverviewSnapshot | null }) {
  const t = snap?.traffic;
  return (
    <BlockCard
      title="Internet traffic"
      description={`Measured from usage records, ${snap ? rangeWords[snap.range] : ""}. Download is what guests pulled from the internet.`}
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

// ---------------------------------------------------------------------------------------------------------
// row 4: sign-in outcomes and when guests sign in
// ---------------------------------------------------------------------------------------------------------

export function SignInOutcomesCard({ snap }: { snap: OverviewSnapshot | null }) {
  const s = snap?.sign_in_outcomes;
  const g = snap?.guests;
  const failed = s ? s.total - s.verified : 0;
  return (
    <BlockCard
      title="Sign-in outcomes"
      description={snap ? `Successful sign-ins by method, and room sign-in checks, ${rangeWords[snap.range]}. No guest is named.` : undefined}
      href="/guest-signin-attempts"
      linkLabel="Attempts"
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

          <section className="space-y-2">
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
          </section>

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
      title="When guests sign in"
      description={hourly ? "Sign-ins per hour, by method." : "Sign-ins by day of week and hour, in the appliance's local time."}
    >
      {!snap || !g ? (
        <Skeleton className="h-48" />
      ) : !g.available ? (
        <Unavailable block={g} />
      ) : hourly ? (
        <ColumnChart
          data={snap.buckets.map((b, i) => ({
            label: String(new Date(b).getHours()).padStart(2, "0"),
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

// ---------------------------------------------------------------------------------------------------------
// row 5: packages and PMS
// ---------------------------------------------------------------------------------------------------------

export function PackagesCard({ snap }: { snap: OverviewSnapshot | null }) {
  const p = snap?.packages;
  const t = snap?.traffic;
  return (
    <BlockCard title="Packages in use" description="What guests are on right now, and what was given out." href="/internet-packages" linkLabel="Packages">
      {!snap || !p ? (
        <Skeleton className="h-48" />
      ) : !p.available ? (
        <Unavailable block={p} />
      ) : (
        <div className="space-y-5">
          <section className="space-y-2">
            <h3 className="text-xs font-medium text-muted-foreground">Active now</h3>
            {p.active_now.length === 0 ? (
              <EmptyState className="py-6" icon={<Package />} title="No active packages" hint="No guest currently holds an internet package." />
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
  return (
    <BlockCard
      title="Property management system"
      description="Whether a guest can sign in with their room number and name."
      href="/pms-interfaces"
      linkLabel="PMS connection"
    >
      {!snap || !pms ? (
        <Skeleton className="h-48" />
      ) : !pms.available || pms.interfaces.length === 0 ? (
        <EmptyState
          className="py-8"
          icon={<Hotel />}
          title="No PMS connection is configured"
          hint="Guests can still sign in with vouchers or guest accounts. Room sign-in needs a PMS."
          action={<Link href="/pms-interfaces" className={buttonVariants({ variant: "secondary", size: "sm" })}>Set one up</Link>}
        />
      ) : (
        <div className="space-y-4">
          {pms.interfaces.map((i) => {
            const words = describePmsReadiness({
              transport: i.transport_status, sync: i.sync_status, roomAuthReady: i.room_auth_ready, inHouse: i.in_house_stays,
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
                <p className="mt-1.5 text-xs text-muted-foreground">{words.summary}</p>
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
          <p className="text-xs text-muted-foreground">
            Today: {fmtInt(pms.events_today)} PMS messages received, {fmtInt(pms.events_applied_today)} applied automatically
            {pms.events_needing_review > 0 && <>, {fmtInt(pms.events_needing_review)} waiting for a decision</>}.
          </p>
          {pms.postings.available && (
            <div className="space-y-2 border-t border-border pt-3">
              <div className="flex items-center justify-between gap-2">
                <h3 className="text-xs font-medium text-muted-foreground">Room charges posted to the PMS</h3>
                {canCharges && (
                  <Link href="/financial-health" className="text-xs text-muted-foreground hover:text-foreground">Charge health →</Link>
                )}
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

// ---------------------------------------------------------------------------------------------------------
// row 6: guest networks
// ---------------------------------------------------------------------------------------------------------

function Chip({ on, label }: { on: boolean; label: string }) {
  return (
    <Badge tone={on ? "info" : "default"} className="px-1.5 text-2xs">
      {label} {on ? "on" : "off"}
    </Badge>
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
        <div className="min-w-0">
          <CardTitle>Guest networks</CardTitle>
          <CardDescription className="mt-0.5 text-xs">
            Address pool use comes from the DHCP server's live leases; devices online from active sessions.
          </CardDescription>
        </div>
        <Link href="/network" className="shrink-0 text-xs text-muted-foreground hover:text-foreground">Networking →</Link>
      </CardHeader>
      {!snap || !nb ? (
        <CardBody><Skeleton className="h-32" /></CardBody>
      ) : !nb.available ? (
        <CardBody><Unavailable block={nb} /></CardBody>
      ) : nb.rows.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<Network />}
            title="No guest network is configured"
            hint="No device can be put online until at least one guest network exists."
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
                <TH className="min-w-44">Address pool</TH>
                <TH className="text-right">Devices online</TH>
                <TH className="text-right">Traffic</TH>
                <TH>Services</TH>
              </TR>
            </THead>
            <TBody>
              {nb.rows.map((n) => (
                <TR key={n.id}>
                  <TD>
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{n.name}</span>
                      {!n.enabled && <Badge tone="default">Off</Badge>}
                    </div>
                    <div className="mt-0.5 font-mono text-2xs text-muted-foreground">
                      {n.subnet_cidr}{n.vlan_id != null && ` · VLAN ${n.vlan_id}`}
                    </div>
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
                        caption={`${fmtInt(n.leases_in_pool)} / ${fmtInt(n.pool_size)}${n.utilisation_pct != null ? ` · ${n.utilisation_pct}%` : ""}`}
                        label={n.expiring_soon ? `${fmtInt(n.expiring_soon)} expiring soon` : "Leased"}
                      />
                    ) : (
                      <span className="text-xs text-muted-foreground">{fmtInt(n.pool_size)} addresses · use not known</span>
                    )}
                  </TD>
                  <TD className="text-right tabular">{fmtInt(n.devices_online)}</TD>
                  <TD className="text-right tabular">
                    {n.traffic_known ? (
                      <span title={`${formatBytes(n.bytes_down)} down · ${formatBytes(n.bytes_up)} up`}>{formatBytes(n.bytes_down + n.bytes_up)}</span>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TD>
                  <TD>
                    <div className="flex flex-wrap gap-1">
                      <Chip on={n.captive_portal_enabled} label="Portal" />
                      <Chip on={n.internet_access_enabled} label="Internet" />
                      <Badge tone="default" className="px-1.5 text-2xs">DNS {n.dns_mode === "custom" ? "custom" : "appliance"}</Badge>
                    </div>
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

// ---------------------------------------------------------------------------------------------------------
// row 7: DHCP/DNS and the appliance
// ---------------------------------------------------------------------------------------------------------

export function DhcpDnsCard({ snap }: { snap: OverviewSnapshot | null }) {
  const d = snap?.dhcp;
  const dns = snap?.dns;
  return (
    <BlockCard title="Addresses and names" description="The DHCP server and the DNS resolver guests use." href="/network/dhcp" linkLabel="DHCP">
      {!snap || !d || !dns ? (
        <Skeleton className="h-48" />
      ) : (
        <div className="space-y-5">
          <section className="space-y-2">
            <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><Router className="size-3.5" aria-hidden /> DHCP</h3>
            {!d.available ? (
              <Unavailable block={d} />
            ) : (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  {d.server_healthy == null ? (
                    <Badge tone="default">Server state unknown</Badge>
                  ) : !d.server_configured ? (
                    <Badge tone="default">Not needed yet</Badge>
                  ) : (
                    <Badge tone={d.server_healthy ? "ok" : "err"} dot>{d.server_healthy ? "Server healthy" : "Server not healthy"}</Badge>
                  )}
                  {d.leases_available && <Badge tone="neutral">{fmtInt(d.active_leases)} active leases</Badge>}
                  {d.leases_available && d.expiring_soon > 0 && (
                    <Badge tone="neutral">{fmtInt(d.expiring_soon)} expiring within {Math.round(d.expiring_within_seconds / 60)} min</Badge>
                  )}
                </div>
                {!d.leases_available && d.leases_reason && (
                  <p className="text-xs text-muted-foreground">Leases: {reasonText(d.leases_reason)}</p>
                )}
                {d.server_detail && d.server_healthy === false && <p className="text-xs text-muted-foreground">{d.server_detail}</p>}
                <p className="text-xs text-muted-foreground">
                  {d.captive_option_networks.length > 0
                    ? <>Sign-in page advertised to devices by DHCP (option 114) on: {d.captive_option_networks.join(", ")}.</>
                    : d.local_dhcp_networks > 0
                      ? "No network advertises the sign-in page through DHCP (option 114)."
                      : "No guest network gets its addresses from this appliance."}
                </p>
              </>
            )}
          </section>
          <section className="space-y-2">
            <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><Wifi className="size-3.5" aria-hidden /> DNS</h3>
            {!dns.available ? (
              <Unavailable block={dns} />
            ) : (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone={serviceState(dns.resolver_state).tone} dot>Resolver: {serviceState(dns.resolver_state).label}</Badge>
                  {dns.last_healthy_at && (
                    <span className="text-xs text-muted-foreground" title={new Date(dns.last_healthy_at).toLocaleString()}>
                      last answered {formatRelative(dns.last_healthy_at)}
                    </span>
                  )}
                </div>
                {dns.networks.length > 0 && (
                  <ul className="space-y-1 text-xs">
                    {dns.networks.map((n) => (
                      <li key={n.network} className="flex flex-wrap gap-x-2">
                        <span className="font-medium">{n.network}</span>
                        <span className="text-muted-foreground">
                          {n.mode === "custom" ? `custom servers ${n.servers.join(", ") || "(none listed)"}` : "this appliance's resolver"}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
                {!dns.statistics_collected && (
                  <p className="text-xs text-muted-foreground">Query and cache statistics are not collected on this appliance.</p>
                )}
              </>
            )}
          </section>
        </div>
      )}
    </BlockCard>
  );
}

export function ApplianceCard({ snap, licenseHeadline }: { snap: OverviewSnapshot | null; licenseHeadline?: string }) {
  const a = snap?.appliance;
  const res = a?.resources;
  const net = a?.network;
  const rev = a?.revisions;
  const lic = a?.license;
  const licWords = lic?.available ? describeLicense(lic.state, lic.installed) : null;
  return (
    <BlockCard title="Appliance" description="Its configuration, licence and resources." href="/appliance" linkLabel="Appliance">
      {!snap || !a ? (
        <Skeleton className="h-48" />
      ) : (
        <div className="space-y-5">
          <KeyValueGrid
            items={[
              {
                label: "Licence",
                value: licWords ? (
                  <Badge tone={licWords.tone === "default" ? "default" : licWords.tone} dot>{licWords.headline}</Badge>
                ) : (
                  <span className="text-muted-foreground">{licenseHeadline ?? reasonText(lic?.reason)}</span>
                ),
                hint: lic?.valid_until ? `Valid until ${new Date(lic.valid_until).toLocaleDateString()}` : undefined,
              },
              { label: "Admin service version", value: <span className="font-mono text-xs">{a.version || "—"}</span> },
              {
                label: "Internet (WAN)",
                value: !net?.available ? (
                  <span className="text-muted-foreground">{reasonText(net?.reason)}</span>
                ) : (
                  <span className="font-mono text-xs">{net.wan_address || "no address"}{net.wan_mode ? ` · ${net.wan_mode}` : ""}</span>
                ),
                hint: net?.available
                  ? net.internet_reachable == null ? undefined : net.internet_reachable ? "Internet reachable" : "Internet not reachable"
                  : undefined,
              },
              {
                label: "Local network (LAN)",
                value: !net?.available ? "—" : <span className="font-mono text-xs">{net.lan_address || "no address"}</span>,
              },
              {
                label: "Network configuration",
                value: !rev?.available ? (
                  <span className="text-muted-foreground">{reasonText(rev?.reason)}</span>
                ) : rev.latest_active ? (
                  <span>Change #{rev.latest_active.seq} in force</span>
                ) : (
                  <span className="text-muted-foreground">No change applied yet</span>
                ),
                hint: rev?.available && rev.latest_active?.confirmed_at
                  ? `Confirmed ${formatRelative(rev.latest_active.confirmed_at)}`
                  : rev?.pending ? `Change #${rev.pending.seq} waiting to be confirmed` : undefined,
              },
              { label: "Up for", value: res?.available ? formatDuration(res.uptime_seconds) : "—" },
            ]}
          />
          <section className="space-y-3">
            <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><Gauge className="size-3.5" aria-hidden /> Resources</h3>
            {!res || !res.available ? (
              <Unavailable block={res ?? { available: false }} />
            ) : (
              <div className="space-y-3" data-testid="resources">
                {res.load && (
                  <Meter
                    value={res.load.one_per_cpu}
                    max={1}
                    label={<span className="inline-flex items-center gap-1"><Cpu className="size-3" aria-hidden /> Load ({res.load.cpus} CPU{res.load.cpus === 1 ? "" : "s"})</span>}
                    caption={`${res.load.one.toFixed(2)} · ${res.load.five.toFixed(2)} · ${res.load.fifteen.toFixed(2)}`}
                  />
                )}
                {res.memory && (
                  <Meter
                    value={res.memory.used_pct}
                    max={100}
                    label={<span className="inline-flex items-center gap-1"><MemoryStick className="size-3" aria-hidden /> Memory</span>}
                    caption={`${formatBytes(res.memory.used_bytes)} of ${formatBytes(res.memory.total_bytes)} · ${res.memory.used_pct}%`}
                  />
                )}
                {res.disks.filter((d) => !d.same_filesystem_as).map((d) => (
                  <Meter
                    key={d.path}
                    value={d.used_pct}
                    max={100}
                    label={<span className="inline-flex items-center gap-1"><HardDrive className="size-3" aria-hidden /> Disk {d.path}{res.disks.some((x) => x.same_filesystem_as === d.path) ? " (includes the install directory)" : ""}</span>}
                    caption={`${formatBytes(d.available_bytes)} free · ${d.used_pct}%`}
                  />
                ))}
                {res.errors && res.errors.length > 0 && (
                  <p className="text-xs text-muted-foreground">Not read: {res.errors.join("; ")}.</p>
                )}
              </div>
            )}
          </section>
        </div>
      )}
    </BlockCard>
  );
}

// ---------------------------------------------------------------------------------------------------------
// services grid (the page adds the /health dependencies beneath it)
// ---------------------------------------------------------------------------------------------------------

export function ServicesGrid({ snap }: { snap: OverviewSnapshot | null }) {
  const sv = snap?.services;
  if (!snap || !sv) return <Skeleton className="h-32" />;
  if (!sv.available) return <Unavailable block={sv} />;
  return (
    <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2" data-testid="services-grid">
      {sv.services.map((s) => {
        const st = serviceState(s.state);
        return (
          <li key={s.name} className="flex min-w-0 items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
            <div className="flex min-w-0 items-center gap-2">
              <StatusDot tone={st.tone} />
              <span className="truncate text-sm">{s.label}</span>
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
