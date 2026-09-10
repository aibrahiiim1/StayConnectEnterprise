"use client";

// THE DASHBOARD, REBUILT TO ANSWER "WHAT DOES TONIGHT LOOK LIKE?"
//
// What it used to be: four tiles (devices online, sessions today, bytes today, licence state) and a box with
// four raw facts in it, one of which read "Cloud sync outbox · 69572 pending · 8424 dead". A duty manager
// opening that page learned almost nothing they could act on, and one line of it was actively alarming without
// explaining itself.
//
// What it is now, in the order a shift actually asks:
//
//   1. IS ANYTHING BROKEN?        the attention strip, which appears only when something needs a person.
//   2. WHO IS ON, AND HOW BUSY?   devices and guests online, sign-ins today, data today, with the hour-by-hour
//                                 shape of the night.
//   3. IS THE HOTEL CONNECTED?    the PMS, per interface, stated as whether a guest can sign in with their room
//                                 number — not as four internal status words.
//   4. IS ANYONE FAILING TO GET IN? the sign-in check split for the last 24 hours.
//   5. WHAT ARE GUESTS ON?        the packages actually in use right now.
//   6. THE NETWORKS AND SERVICES. per-guest-network occupancy, and the services this appliance depends on.
//
// Every figure on this page is explained where it is not self-evident. That is the direct answer to a number
// like the outbox one: the figure stays, and a sentence next to it says what it is, whether it matters, and
// whether any guest is affected.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import {
  api, DashboardSnapshot, EdgeHealth,
} from "@/lib/api";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Explain, Tooltip } from "@/components/ui/tooltip";
import { Meter, Metric, Skeleton, Separator } from "@/components/ui/misc";
import { ColumnChart, SplitBar } from "@/components/ui/chart";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { formatBytes, formatRelative } from "@/lib/utils";
import {
  describeOutbox, describeDatabase, describeSessionController, describeLicense, describePmsReadiness,
} from "@/lib/health-words";
import {
  Users, Wifi, ArrowDownUp, BadgeCheck, Hotel, RefreshCw, LogIn, Network, Package, Wallet,
} from "lucide-react";

const fmtInt = (n?: number | null) => (typeof n === "number" ? n.toLocaleString() : "—");

/**
 * normalize fills in any section the payload did not carry.
 *
 * This is not defensive decoration. `snap?.pms.interfaces` reads safely only while `snap` is null: once a
 * response arrives with `pms` absent, the optional chain has already been satisfied and the next access throws
 * — which is a CLIENT-SIDE EXCEPTION that replaces the entire dashboard with "Application error", losing the
 * sections that did arrive. That is strictly worse than a missing number, and it is reachable from things
 * outside this page's control: an older edged that predates a section, a proxy returning an error document with
 * a 200, or any response shaped differently from the contract.
 *
 * So the payload is normalized ONCE, here, and the render reads a value it knows exists. A section that did not
 * arrive is marked unavailable, which is exactly how the UI already presents a capability this appliance does
 * not have — so an incomplete response degrades to the same honest surface rather than to a blank page.
 */
function normalize(d: DashboardSnapshot): DashboardSnapshot {
  const absent = { available: false, reason: "section_absent_from_response" };
  return {
    ...d,
    guests: d.guests ?? {
      devices_online: 0, guests_online: 0, sign_ins_today: 0, devices_today: 0, sign_ins_7d: 0,
    },
    data: d.data ?? {
      bytes_down_today: 0, bytes_up_today: 0, total_bytes_today: 0, bytes_down_7d: 0, bytes_up_7d: 0,
    },
    hourly: Array.isArray(d.hourly) ? d.hourly : [],
    occupancy: d.occupancy ?? {
      ...absent, in_house: 0, with_internet: 0, arrivals_today: 0, departures_today: 0, posting_allowed: 0,
    },
    sign_in_checks: d.sign_in_checks ?? { ...absent, window: "24h", total: 0, verified: 0, outcomes: [] },
    pms: d.pms
      ? { ...d.pms, interfaces: Array.isArray(d.pms.interfaces) ? d.pms.interfaces : [] }
      : { ...absent, interfaces: [], events_today: 0, events_applied_today: 0, events_needing_review: 0 },
    postings: d.postings ?? {
      ...absent, posted_today: 0, failed_today: 0, pending: 0, review_open: 0, unknown_open: 0,
    },
    networks: Array.isArray(d.networks) ? d.networks : [],
    packages: Array.isArray(d.packages) ? d.packages : [],
  };
}

export default function DashboardPage() {
  const [snap, setSnap] = useState<DashboardSnapshot | null>(null);
  const [health, setHealth] = useState<EdgeHealth | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [loadedAt, setLoadedAt] = useState<string | null>(null);

  const load = useCallback(async (manual = false) => {
    if (manual) setRefreshing(true);
    try {
      // Fetched together but settled independently: a health endpoint that hangs must not blank the operational
      // figures, and vice versa. Promise.all would make either failure take the whole page down.
      const [d, h] = await Promise.allSettled([
        api.get<DashboardSnapshot>("/reports/dashboard"),
        api.get<EdgeHealth>("/health"),
      ]);
      if (d.status === "fulfilled") { setSnap(normalize(d.value)); setErr(null); }
      else setErr(d.reason);
      if (h.status === "fulfilled") setHealth(h.value);
      setLoadedAt(new Date().toISOString());
    } finally {
      if (manual) setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), 30_000);
    return () => clearInterval(id);
  }, [load]);

  const outbox = describeOutbox(health?.sync_outbox);
  const license = describeLicense(health?.license_state, health?.license_installed);

  // THE ATTENTION STRIP. Only real, currently-true problems, each phrased as what it stops rather than as the
  // internal state that produced it. An always-present "system status" panel trains people to ignore it.
  const attention: { text: string; href?: string; tone: "warn" | "err" }[] = [];
  if (health && !health.db) attention.push({ text: describeDatabase(false).summary, href: "/health", tone: "err" });
  if (health && !health.scd) attention.push({ text: describeSessionController(false).summary, href: "/health", tone: "err" });
  if (health && !health.license_installed) {
    attention.push({ text: "This appliance has no signed licence installed yet — activate it before the property opens.", href: "/license", tone: "warn" });
  }
  for (const i of snap?.pms.interfaces ?? []) {
    if (i.lifecycle_state === "ACTIVE" && !i.room_auth_ready) {
      attention.push({
        text: `${i.display_label || "PMS connection"}: ${describePmsReadiness({
          transport: i.transport_status, sync: i.sync_status, roomAuthReady: i.room_auth_ready,
        }).summary}`,
        href: "/pms-interfaces",
        tone: i.transport_status === "CONNECTED" ? "warn" : "err",
      });
    }
  }
  if ((snap?.pms.events_needing_review ?? 0) > 0) {
    attention.push({
      text: `${fmtInt(snap?.pms.events_needing_review)} PMS messages could not be applied automatically and are waiting for a decision.`,
      href: "/stay-events", tone: "warn",
    });
  }
  if (snap?.postings.available && snap.postings.review_open > 0) {
    attention.push({
      text: `${fmtInt(snap.postings.review_open)} room charges are waiting for a manual decision.`,
      href: "/financial-review", tone: "warn",
    });
  }
  if (outbox.tone === "err") {
    attention.push({ text: outbox.summary, href: "/network/cloud", tone: "warn" });
  }

  const dayLabel = snap?.day_start
    ? new Date(snap.day_start).toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "short" })
    : null;

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Overview"
        title="Tonight at a glance"
        description={
          dayLabel
            ? `Everything marked "today" is counted from this appliance's local midnight — ${dayLabel}.`
            : "Live operational state for this property."
        }
        actions={
          <div className="flex items-center gap-2">
            {loadedAt && (
              <span className="hidden text-xs text-muted-foreground sm:inline">
                Updated {formatRelative(loadedAt)}
              </span>
            )}
            <Button variant="secondary" size="sm" onClick={() => void load(true)} disabled={refreshing}>
              <RefreshCw className={refreshing ? "animate-spin" : undefined} />
              Refresh
            </Button>
          </div>
        }
      />

      <ErrorBanner err={err} />

      {attention.length > 0 && (
        <Callout tone={attention.some((a) => a.tone === "err") ? "danger" : "warning"} title="Needs attention">
          <ul className="space-y-1">
            {attention.map((a, i) => (
              <li key={i}>
                {a.text}{" "}
                {a.href && (
                  <Link href={a.href} className="font-medium underline underline-offset-2">
                    Open
                  </Link>
                )}
              </li>
            ))}
          </ul>
        </Callout>
      )}

      {/* ---------------------------------------------------------------- who is on */}
      {!snap ? (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-28" />)}
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard
            label="Guests online"
            value={fmtInt(snap.guests.guests_online)}
            icon={<Users />}
            tone="primary"
            href="/sessions"
            hint={`${fmtInt(snap.guests.devices_online)} device${snap.guests.devices_online === 1 ? "" : "s"} connected`}
            explain={
              <Explain>
                A <strong>guest</strong> here is one room, account or voucher — whatever the internet was granted
                to. One guest with a phone, a laptop and a tablet counts as one guest and three devices.
              </Explain>
            }
          />
          <StatCard
            label="Sign-ins today"
            value={fmtInt(snap.guests.sign_ins_today)}
            icon={<LogIn />}
            tone="info"
            href="/sessions"
            hint={
              snap.guests.busiest_hour_today != null
                ? `Busiest hour ${String(snap.guests.busiest_hour_today).padStart(2, "0")}:00 · ${fmtInt(snap.guests.busiest_hour_count)}`
                : `${fmtInt(snap.guests.devices_today)} different devices`
            }
            explain={
              <Explain>
                Every time a device is put online it starts a session. This counts the sessions started since
                local midnight, so a guest reconnecting after losing signal is counted again.
              </Explain>
            }
          />
          <StatCard
            label="Data today"
            value={formatBytes(snap.data.total_bytes_today)}
            icon={<ArrowDownUp />}
            hint={`${formatBytes(snap.data.bytes_down_today)} down · ${formatBytes(snap.data.bytes_up_today)} up`}
            explain={
              <Explain>
                Traffic recorded against sessions that started today. Download is what guests pulled from the
                internet; upload is what they sent.
              </Explain>
            }
          />
          <StatCard
            label="Licence"
            value={
              <Badge
                tone={license.tone === "default" ? "default" : license.tone}
                className="text-sm"
                dot
              >
                {license.headline}
              </Badge>
            }
            icon={<BadgeCheck />}
            tone={license.tone === "ok" ? "ok" : license.tone === "err" ? "err" : "warn"}
            href="/license"
            hint={license.summary}
          />
        </div>
      )}

      {/* ---------------------------------------------------------------- the night's shape + occupancy */}
      <div className="grid gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <div>
              <CardTitle>Sign-ins by hour</CardTitle>
              <p className="mt-0.5 text-xs text-muted-foreground">
                How the day has filled up. Quiet hours are shown as zero rather than left out.
              </p>
            </div>
            <Tooltip
              content={
                <>
                  This chart counts sign-ins, not traffic. Hour-by-hour data volume is measured in the usage
                  ledger, which this admin service is deliberately not permitted to read — so rather than
                  attribute a guest&rsquo;s whole evening to the hour they happened to connect, the data figures
                  above are reported for the day as a whole.
                </>
              }
            >
              <span className="cursor-help text-xs text-muted-foreground underline decoration-dotted underline-offset-4">
                Why not data per hour?
              </span>
            </Tooltip>
          </CardHeader>
          <CardBody>
            {!snap ? (
              <Skeleton className="h-40" />
            ) : (
              <ColumnChart
                data={snap.hourly.map((h) => ({
                  label: `${String(h.hour).padStart(2, "0")}`,
                  values: [h.sign_ins],
                }))}
                series={[{ name: "Sign-ins" }]}
                tickEvery={3}
                formatValue={(n) => n.toLocaleString()}
                emptyLabel="No guest has signed in yet today."
              />
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Occupancy</CardTitle>
            {snap?.occupancy.available && (
              <Link href="/stays" className="text-xs text-muted-foreground hover:text-foreground">
                All stays →
              </Link>
            )}
          </CardHeader>
          <CardBody>
            {!snap ? (
              <Skeleton className="h-32" />
            ) : !snap.occupancy.available ? (
              <EmptyState
                icon={<Hotel />}
                title="No property management system"
                hint="Occupancy comes from the PMS. Without one, guests sign in with vouchers or accounts and the hotel's room list is not known here."
              />
            ) : (
              <div className="space-y-4">
                <div className="grid grid-cols-2 gap-4">
                  <Metric label="In house" value={fmtInt(snap.occupancy.in_house)} />
                  <Metric
                    label="With internet"
                    value={fmtInt(snap.occupancy.with_internet)}
                    tone={
                      snap.occupancy.in_house > 0 && snap.occupancy.with_internet === 0 ? "warn" : "default"
                    }
                  />
                  <Metric label="Arriving today" value={fmtInt(snap.occupancy.arrivals_today)} />
                  <Metric label="Departing today" value={fmtInt(snap.occupancy.departures_today)} />
                </div>
                <Meter
                  value={snap.occupancy.with_internet}
                  max={snap.occupancy.in_house}
                  label="In-house rooms with an internet package"
                  caption={`${fmtInt(snap.occupancy.with_internet)} / ${fmtInt(snap.occupancy.in_house)}`}
                  tone="info"
                />
                <p className="text-xs text-muted-foreground">
                  A room with no package has not been given internet yet — either nobody has signed in from it,
                  or no package applies to that stay.
                </p>
              </div>
            )}
          </CardBody>
        </Card>
      </div>

      {/* ---------------------------------------------------------------- the PMS */}
      <Card>
        <CardHeader>
          <div>
            <CardTitle>Property management system</CardTitle>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Whether a guest can sign in by typing their room number and name.
            </p>
          </div>
          <Link href="/pms-interfaces" className="text-xs text-muted-foreground hover:text-foreground">
            Manage connection →
          </Link>
        </CardHeader>
        <CardBody className={snap?.pms.interfaces.length ? "space-y-3" : undefined}>
          {!snap ? (
            <Skeleton className="h-24" />
          ) : !snap.pms.available || snap.pms.interfaces.length === 0 ? (
            <EmptyState
              icon={<Hotel />}
              title="No PMS connection is configured"
              hint="Guests can still sign in with vouchers or username-and-password accounts. Room sign-in needs a PMS."
              action={
                <Link href="/pms-interfaces" className={buttonVariants({ variant: "secondary", size: "sm" })}>
                  Set one up
                </Link>
              }
            />
          ) : (
            <>
              {snap.pms.interfaces.map((i) => {
                const words = describePmsReadiness({
                  transport: i.transport_status,
                  sync: i.sync_status,
                  roomAuthReady: i.room_auth_ready,
                  inHouse: i.in_house_stays,
                });
                const syncing = ["REQUESTING_FULL_SYNC", "WAITING_FOR_PMS", "RECEIVING", "PUBLISHING", "APPLYING"]
                  .includes(i.sync_stage ?? "") || !i.materialization_ready && i.sync_stage === "COMPLETE";
                return (
                  <div key={i.pms_interface_id} className="rounded-md border border-border bg-surface/40 p-3.5">
                    <div className="flex flex-wrap items-start justify-between gap-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <StatusDot tone={words.tone === "default" ? "default" : words.tone} />
                          <span className="truncate text-sm font-medium">
                            {i.display_label || "PMS connection"}
                          </span>
                          {i.lifecycle_state !== "ACTIVE" && (
                            <Badge tone="default">Not in use</Badge>
                          )}
                          {syncing && (
                            <Badge tone="info" dot>
                              Loading guest list
                            </Badge>
                          )}
                        </div>
                        <p className="mt-1 max-w-2xl text-xs text-muted-foreground">{words.summary}</p>
                      </div>
                      <Link
                        href="/pms-interfaces"
                        className="shrink-0 text-xs text-muted-foreground hover:text-foreground"
                      >
                        Details →
                      </Link>
                    </div>
                    <div className="mt-3 grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-4">
                      <Metric label="Guests in house" value={fmtInt(i.in_house_stays)} />
                      <Metric
                        label="Waiting to apply"
                        value={fmtInt(i.pending_events)}
                        tone={i.pending_events > 0 ? "warn" : "default"}
                      />
                      <Metric
                        label="Needs review"
                        value={fmtInt(i.review_events)}
                        tone={i.review_events > 0 ? "warn" : "default"}
                      />
                      <Metric
                        label="Last message"
                        value={<span className="text-sm">{formatRelative(i.last_stay_event_at)}</span>}
                      />
                    </div>
                  </div>
                );
              })}
              <Separator />
              <div className="grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-3">
                <Metric
                  label="Messages today"
                  value={fmtInt(snap.pms.events_today)}
                  sub="Arrivals, departures and changes received"
                />
                <Metric label="Applied automatically" value={fmtInt(snap.pms.events_applied_today)} />
                <Metric
                  label="Waiting for a decision"
                  value={fmtInt(snap.pms.events_needing_review)}
                  tone={snap.pms.events_needing_review > 0 ? "warn" : "default"}
                />
              </div>
            </>
          )}
        </CardBody>
      </Card>

      {/* ---------------------------------------------------------------- sign-in checks + packages */}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <div>
              <CardTitle>Room sign-in checks</CardTitle>
              <p className="mt-0.5 text-xs text-muted-foreground">Last 24 hours. No guest is named here.</p>
            </div>
            <Link href="/pms-resolutions" className="text-xs text-muted-foreground hover:text-foreground">
              Evidence →
            </Link>
          </CardHeader>
          <CardBody>
            {!snap ? (
              <Skeleton className="h-24" />
            ) : !snap.sign_in_checks.available || snap.sign_in_checks.total === 0 ? (
              <EmptyState
                icon={<LogIn />}
                title="No room sign-ins attempted"
                hint="Nobody has tried to sign in with a room number in the last 24 hours."
              />
            ) : (
              <div className="space-y-4">
                <div className="flex items-baseline gap-2">
                  <span className="text-2xl font-semibold tabular">
                    {Math.round((snap.sign_in_checks.verified / snap.sign_in_checks.total) * 100)}%
                  </span>
                  <span className="text-sm text-muted-foreground">
                    verified — {fmtInt(snap.sign_in_checks.verified)} of {fmtInt(snap.sign_in_checks.total)} attempts
                  </span>
                </div>
                <SplitBar
                  total={snap.sign_in_checks.total}
                  parts={[
                    { name: "Verified", value: snap.sign_in_checks.verified, tone: "ok" },
                    {
                      name: "Not verified",
                      value: snap.sign_in_checks.total - snap.sign_in_checks.verified,
                      tone: snap.sign_in_checks.verified === snap.sign_in_checks.total ? "neutral" : "err",
                    },
                  ]}
                />
                {snap.sign_in_checks.total > snap.sign_in_checks.verified && (
                  <div className="space-y-1.5">
                    <div className="text-xs font-medium text-muted-foreground">Why the rest failed</div>
                    <ul className="flex flex-wrap gap-1.5">
                      {snap.sign_in_checks.outcomes
                        .filter((o) => o.outcome_code !== "VERIFIED")
                        .map((o) => (
                          <li key={o.outcome_code}>
                            <Badge tone="warn">
                              {o.outcome_code.replace(/_/g, " ").toLowerCase()} · {fmtInt(o.count)}
                            </Badge>
                          </li>
                        ))}
                    </ul>
                  </div>
                )}
              </div>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <div>
              <CardTitle>Packages in use</CardTitle>
              <p className="mt-0.5 text-xs text-muted-foreground">
                What guests are actually on right now, not what is on offer.
              </p>
            </div>
            <Link href="/internet-packages" className="text-xs text-muted-foreground hover:text-foreground">
              Packages →
            </Link>
          </CardHeader>
          <CardBody className="p-0">
            {!snap ? (
              <Skeleton className="m-5 h-24" />
            ) : snap.packages.length === 0 ? (
              <EmptyState
                icon={<Package />}
                title="No internet packages"
                hint="Until a package exists, a verified guest has nothing to be given and cannot get online."
              />
            ) : (
              <Table>
                <THead>
                  <TR>
                    <TH>Package</TH>
                    <TH className="text-right">Online now</TH>
                    <TH className="text-right">Given out (7 days)</TH>
                  </TR>
                </THead>
                <tbody>
                  {snap.packages.map((p) => (
                    <TR key={p.package_id}>
                      <TD>
                        <div className="flex items-center gap-2">
                          <span className="font-medium">{p.name || p.code}</span>
                          {!p.active && <Badge tone="default">Disabled</Badge>}
                        </div>
                        {p.name && p.name !== p.code && (
                          <div className="text-xs text-muted-foreground">{p.code}</div>
                        )}
                      </TD>
                      <TD className="text-right tabular">{fmtInt(p.online_now)}</TD>
                      <TD className="text-right tabular text-muted-foreground">{fmtInt(p.grants_7d)}</TD>
                    </TR>
                  ))}
                </tbody>
              </Table>
            )}
          </CardBody>
        </Card>
      </div>

      {/* ---------------------------------------------------------------- networks */}
      <Card>
        <CardHeader>
          <div>
            <CardTitle>Guest networks</CardTitle>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Where the connected devices actually are, and how much room each network's address pool has left.
            </p>
          </div>
          <Link href="/network" className="text-xs text-muted-foreground hover:text-foreground">
            Networking →
          </Link>
        </CardHeader>
        <CardBody className={snap?.networks.length ? "grid gap-4 sm:grid-cols-2 lg:grid-cols-3" : undefined}>
          {!snap ? (
            <Skeleton className="h-24" />
          ) : snap.networks.length === 0 ? (
            <EmptyState
              icon={<Network />}
              title="No guest network is configured"
              hint="No device can be put online until at least one guest network exists."
              action={
                <Link href="/network/new" className={buttonVariants({ variant: "secondary", size: "sm" })}>
                  Create one
                </Link>
              }
            />
          ) : (
            snap.networks.map((n) => (
              <div key={n.bridge_name} className="rounded-md border border-border p-3.5">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{n.name}</div>
                    <div className="mt-0.5 font-mono text-2xs text-muted-foreground">
                      {n.subnet_cidr}
                      {n.vlan_id != null && ` · VLAN ${n.vlan_id}`}
                    </div>
                  </div>
                  <Badge tone={n.enabled ? "ok" : "default"} dot>
                    {n.enabled ? "On" : "Off"}
                  </Badge>
                </div>
                <div className="mt-3">
                  <Metric label="Devices online" value={fmtInt(n.devices_online)} />
                </div>
                {n.pool_addresses > 0 ? (
                  <Meter
                    className="mt-3"
                    value={n.devices_online}
                    max={n.pool_addresses}
                    label="Address pool in use"
                    caption={`${fmtInt(n.devices_online)} / ${fmtInt(n.pool_addresses)}`}
                  />
                ) : (
                  <p className="mt-3 text-xs text-muted-foreground">
                    {n.dhcp_mode === "local"
                      ? "No address range is configured, so this appliance cannot hand out addresses here."
                      : `Addresses are handed out ${n.dhcp_mode === "relay" ? "by a relayed server" : "elsewhere"}.`}
                  </p>
                )}
              </div>
            ))
          )}
        </CardBody>
      </Card>

      {/* ---------------------------------------------------------------- services + charges */}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <div>
              <CardTitle>Services this appliance depends on</CardTitle>
              <p className="mt-0.5 text-xs text-muted-foreground">
                Each row says what stops working if it is down.
              </p>
            </div>
            <Link href="/health" className="text-xs text-muted-foreground hover:text-foreground">
              Diagnostics →
            </Link>
          </CardHeader>
          <CardBody className="space-y-3">
            {!health ? (
              <Skeleton className="h-24" />
            ) : (
              <>
                <ServiceRow title="Site database" info={describeDatabase(!!health.db)} />
                <ServiceRow title="Session controller" info={describeSessionController(!!health.scd)} />
                {/*
                  THE LINE THE OPERATOR ASKED ABOUT. It used to read "Cloud sync outbox · 69572 pending · 8424
                  dead", with nothing to say what an outbox is or whether guests were affected. The figures are
                  still here — they are real and occasionally matter — but they now come with the sentence that
                  makes them actionable, and the reassurance that no guest depends on this queue.
                */}
                <ServiceRow
                  title="Reporting to the StayConnect cloud"
                  info={outbox}
                  href="/network/cloud"
                />
                <Separator />
                <div className="flex items-center justify-between gap-4 text-xs text-muted-foreground">
                  <span>Admin service {health.version}</span>
                  <span className="font-mono">site {health.site_id?.slice(0, 8)}…</span>
                </div>
              </>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <div>
              <CardTitle>Charges posted to the PMS</CardTitle>
              <p className="mt-0.5 text-xs text-muted-foreground">
                Paid internet billed to a guest&rsquo;s room account.
              </p>
            </div>
            {snap?.postings.available && (
              <Link href="/financial-health" className="text-xs text-muted-foreground hover:text-foreground">
                Charge health →
              </Link>
            )}
          </CardHeader>
          <CardBody>
            {!snap ? (
              <Skeleton className="h-24" />
            ) : !snap.postings.available ? (
              <EmptyState
                icon={<Wallet />}
                title="Room charging is not in use here"
                hint="This appliance is not configured to post charges to the PMS, so there is nothing to report. Free packages and pre-paid vouchers do not produce charges."
              />
            ) : (
              <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
                <Metric label="Posted today" value={fmtInt(snap.postings.posted_today)} tone="ok" />
                <Metric
                  label="Rejected today"
                  value={fmtInt(snap.postings.failed_today)}
                  tone={snap.postings.failed_today > 0 ? "err" : "default"}
                />
                <Metric label="In the queue" value={fmtInt(snap.postings.pending)} />
                <Metric
                  label="Awaiting review"
                  value={fmtInt(snap.postings.review_open)}
                  tone={snap.postings.review_open > 0 ? "warn" : "default"}
                  sub="Needs a person to decide"
                />
                <Metric
                  label="Outcome unknown"
                  value={fmtInt(snap.postings.unknown_open)}
                  tone={snap.postings.unknown_open > 0 ? "warn" : "default"}
                  sub="Sent, no answer from the PMS"
                />
              </div>
            )}
          </CardBody>
        </Card>
      </div>
    </PageShell>
  );
}

function ServiceRow({
  title, info, href,
}: {
  title: string;
  info: { headline: string; summary: string; tone: "ok" | "warn" | "err" | "default" };
  href?: string;
}) {
  const body = (
    <div className="flex items-start justify-between gap-4 rounded-md px-1 py-0.5">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <StatusDot tone={info.tone} />
          <span className="text-sm font-medium">{title}</span>
        </div>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{info.summary}</p>
      </div>
      <Badge tone={info.tone === "default" ? "default" : info.tone} className="shrink-0">
        {info.headline}
      </Badge>
    </div>
  );
  return href ? (
    <Link href={href} className="block rounded-md transition-colors hover:bg-surface/60">
      {body}
    </Link>
  ) : (
    body
  );
}
