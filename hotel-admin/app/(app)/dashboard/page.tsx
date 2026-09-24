"use client";

// THE OVERVIEW — WHAT IS HAPPENING AT THIS PROPERTY, AND IS ANYTHING WRONG.
//
// Rebuilt on GET /reports/overview, which answers over a chosen range (24 hours, 7 days, 30 days) instead of
// only "today". The order is the order a shift asks in:
//
//   1. DOES ANYTHING NEED ME?      the attention list, which appears only when something does; otherwise one
//                                  calm line.
//   2. HOW BUSY, HOW MUCH?          guests online, sign-ins, data used, room sign-in readiness — each with its
//                                  trend over the range.
//   3. THE SHAPE OF THE RANGE.      traffic (measured from the usage records) and devices connected at once.
//   4. IS ANYONE FAILING TO GET IN? sign-in outcomes, and when guests sign in.
//   5. WHAT ARE THEY ON, AND IS THE HOTEL CONNECTED?  packages and the PMS.
//   6. THE NETWORKS.                per guest network: pool use from live DHCP leases, devices, traffic.
//   7. THE APPLIANCE.               services, DHCP/DNS, configuration in force, licence and resources.
//
// THE ONE RULE EVERY BLOCK FOLLOWS: a figure that could not be measured is not drawn. Each block of the response
// carries its own availability, and an unavailable block renders its reason in its own place. Nothing on this
// page substitutes a zero for "unknown", and what is not recorded anywhere (failed voucher, account and one-time
// code sign-ins; DNS query statistics) is named as not recorded.

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { api, EdgeHealth, SetupStatus } from "@/lib/api";
import {
  type OverviewRange, type OverviewSnapshot, OVERVIEW_RANGES, fetchOverview, fmtInt, normalizeOverview, reasonText,
} from "@/lib/api/dashboard";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Explain } from "@/components/ui/tooltip";
import { Separator, Skeleton } from "@/components/ui/misc";
import { Sparkline } from "@/components/ui/chart";
import { Segmented } from "@/components/ui/tabs";
import { useCapabilities, surfaceAvailable } from "@/lib/capabilities";
import { cn, formatBytes, formatRelative } from "@/lib/utils";
import { describeOutbox, describeDatabase, describeSessionController, describeLicense } from "@/lib/health-words";
import { ArrowDownUp, CheckCircle2, Hotel, LayoutDashboard, LogIn, RefreshCw, Users } from "lucide-react";
import {
  ApplianceCard, ConcurrencyCard, DhcpDnsCard, NetworksCard, PackagesCard, PmsCard, ServicesGrid, SignInOutcomesCard,
  SignInPatternCard, TrafficCard,
} from "./overview-blocks";

type AttentionRow = { text: string; detail?: string; href?: string; action?: string; tone: "warn" | "err" };

export default function DashboardPage() {
  const [range, setRange] = useState<OverviewRange>("24h");
  const [snap, setSnap] = useState<OverviewSnapshot | null>(null);
  const [health, setHealth] = useState<EdgeHealth | null>(null);
  const [context, setContext] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [loadedAt, setLoadedAt] = useState<string | null>(null);
  const rangeRef = useRef<OverviewRange>(range);
  const seq = useRef(0);

  const load = useCallback(async (r: OverviewRange, manual = false) => {
    // A range switch while a poll is in flight must not let the older answer land last and overwrite the newer
    // range, so every request is numbered and only the latest may write.
    const mine = ++seq.current;
    if (manual) setBusy(true);
    try {
      // Settled independently: a /health that hangs must not blank the overview, and vice versa.
      const [o, h] = await Promise.allSettled([fetchOverview(r), api.get<EdgeHealth>("/health")]);
      if (mine !== seq.current) return;
      if (o.status === "fulfilled") {
        setSnap(normalizeOverview(o.value));
        setErr(null);
      } else {
        setErr(o.reason);
      }
      if (h.status === "fulfilled") setHealth(h.value);
      setLoadedAt(new Date().toISOString());
    } finally {
      if (mine === seq.current) setBusy(false);
    }
  }, []);

  useEffect(() => {
    rangeRef.current = range;
    void load(range, true);
  }, [range, load]);

  useEffect(() => {
    const id = setInterval(() => void load(rangeRef.current), 30_000);
    return () => clearInterval(id);
  }, [load]);

  // Which property and which box this is, once. Purely a label: if it cannot be read the header just omits it.
  useEffect(() => {
    api.get<SetupStatus>("/setup/status")
      .then((s) => {
        const parts = [s.assignment?.site_name, s.hardware?.hostname].filter(Boolean) as string[];
        if (parts.length) setContext(parts.join(" · "));
      })
      .catch(() => {});
  }, []);

  const caps = useCapabilities();
  const outbox = describeOutbox(health?.sync_outbox);
  const stale = !!snap && snap.range !== range;
  const rangeLong = OVERVIEW_RANGES.find((r) => r.value === range)?.long ?? "";

  // NEEDS ATTENTION. The server derives the operational items from the blocks this operator may read; the two
  // runtime dependencies and the cloud queue come from /health, exactly as before.
  const attention: AttentionRow[] = (snap?.attention ?? []).map((a) => ({
    text: a.title, detail: a.detail, href: a.href, action: a.action, tone: a.severity,
  }));
  if (health && !health.db) attention.push({ text: describeDatabase(false).summary, href: "/health", tone: "err" });
  if (health && !health.scd) attention.push({ text: describeSessionController(false).summary, href: "/health", tone: "err" });
  // A licensing-only appliance never raises a cloud item: that state is a decision, and the obvious "repair"
  // is the one thing that must not happen.
  if (outbox.tone === "err" && health?.sync_outbox?.mode !== "LICENSING_ONLY") {
    attention.push({ text: outbox.summary, href: "/appliance?section=license", tone: "warn" });
  }
  // Worth knowing, not worth doing: kept out of the attention list on purpose.
  const notes: { text: string; href: string }[] = [];
  const historical = snap?.pms.historical_exceptions ?? 0;
  if (historical > 0) {
    notes.push({
      text: `${fmtInt(historical)} historical PMS exception${historical === 1 ? "" : "s"} — a departure recorded before this appliance had the full guest list. Guests are unaffected and nothing here needs doing.`,
      href: "/roster-reconciliation",
    });
  }

  const g = snap?.guests;
  const t = snap?.traffic;
  const pms = snap?.pms;
  const so = snap?.sign_in_outcomes;
  const license = describeLicense(health?.license_state, health?.license_installed);

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<LayoutDashboard />}
        eyebrow={context ?? "Overview"}
        title="Overview"
        description={`Guests, traffic, sign-ins and the health of this appliance over ${rangeLong}${snap?.timezone ? `, in the appliance's local time (${snap.timezone})` : ""}.`}
        actions={
          <>
            <Segmented
              label="Time range"
              value={range}
              onChange={setRange}
              options={OVERVIEW_RANGES.map((r) => ({ value: r.value, label: r.label }))}
            />
            {loadedAt && (
              <span className="text-xs text-muted-foreground" title={new Date(loadedAt).toLocaleString()} aria-live="polite">
                {busy ? "Updating…" : `Updated ${formatRelative(loadedAt)}`}
              </span>
            )}
            <Button variant="secondary" size="sm" onClick={() => void load(range, true)} disabled={busy}>
              <RefreshCw className={busy ? "animate-spin" : undefined} />
              Refresh
            </Button>
          </>
        }
      />

      <ErrorBanner err={err} />

      {/* ------------------------------------------------------------------ row 1: needs attention */}
      {attention.length > 0 ? (
        <Callout tone={attention.some((a) => a.tone === "err") ? "danger" : "warning"} title="Needs attention">
          <ul className="space-y-1.5" data-testid="attention">
            {attention.map((a, i) => (
              <li key={i} className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-medium">{a.text}</span>
                {a.detail && <span className="text-muted-foreground">{a.detail}</span>}
                {a.href && (
                  <Link href={a.href} className="font-medium underline underline-offset-2">
                    {a.action ?? "Open"}
                  </Link>
                )}
              </li>
            ))}
          </ul>
        </Callout>
      ) : snap && health ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground" data-testid="all-normal">
          <CheckCircle2 className="size-4 text-success" aria-hidden />
          All systems normal — nothing needs attention right now.
        </div>
      ) : null}

      {notes.length > 0 && (
        <Callout tone="info" title="For information">
          <ul className="space-y-1">
            {notes.map((n, i) => (
              <li key={i}>
                {n.text}{" "}
                <Link href={n.href} className="font-medium underline underline-offset-2">See details</Link>
              </li>
            ))}
          </ul>
        </Callout>
      )}

      <div className={cn("space-y-5 transition-opacity", stale && "opacity-60")} aria-busy={busy || undefined}>
        {/* ---------------------------------------------------------------- row 2: the headline figures */}
        {!snap || !g || !t || !pms || !so ? (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-32" />)}
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatCard
              label="Guests online"
              value={g.available ? fmtInt(g.guests_online) : "—"}
              icon={<Users />}
              tone="primary"
              href="/sessions"
              hint={g.available ? `${fmtInt(g.devices_online)} device${g.devices_online === 1 ? "" : "s"} connected now` : reasonText(g.reason)}
              explain={
                <Explain>
                  A <strong>guest</strong> is one room, account or voucher — whatever the internet was granted to.
                  One guest with a phone and a laptop is one guest and two devices. The trend line is the most
                  devices online at once in each interval.
                </Explain>
              }
              footer={g.available && g.concurrency.available ? <Sparkline values={g.concurrency.peak} width={120} /> : undefined}
            />
            <StatCard
              label="Sign-ins"
              value={g.available ? fmtInt(g.sign_ins) : "—"}
              icon={<LogIn />}
              tone="info"
              href="/sessions"
              hint={g.available ? `${fmtInt(g.unique_devices)} different devices over ${rangeLong}` : reasonText(g.reason)}
              explain={
                <Explain>
                  Every time a device is put online it starts a session, so a guest reconnecting after losing
                  signal is counted again.
                </Explain>
              }
              footer={g.available ? <Sparkline values={g.sign_ins_series} width={120} token="chart-2" /> : undefined}
            />
            <StatCard
              label="Data used"
              value={t.available ? formatBytes(t.bytes_down + t.bytes_up) : "—"}
              icon={<ArrowDownUp />}
              href="/usage"
              hint={t.available ? `${formatBytes(t.bytes_down)} down · ${formatBytes(t.bytes_up)} up` : reasonText(t.reason)}
              explain={
                <Explain>
                  Measured from the appliance&rsquo;s usage records and placed in the interval it was moved in,
                  not in the interval the session started.
                </Explain>
              }
              footer={t.available ? <Sparkline values={t.series_down.map((d, i) => d + (t.series_up[i] ?? 0))} width={120} token="chart-3" /> : undefined}
            />
            <StatCard
              label="Room sign-in"
              value={
                !pms.available || pms.active_interfaces === 0 ? (
                  <span className="text-base font-medium text-muted-foreground">Not in use</span>
                ) : (
                  <Badge tone={pms.ready_interfaces === pms.active_interfaces ? "ok" : "err"} className="text-sm" dot>
                    {pms.ready_interfaces === pms.active_interfaces
                      ? "Ready"
                      : `${fmtInt(pms.ready_interfaces)} of ${fmtInt(pms.active_interfaces)} ready`}
                  </Badge>
                )
              }
              icon={<Hotel />}
              tone={!pms.available || pms.active_interfaces === 0 ? "default" : pms.ready_interfaces === pms.active_interfaces ? "ok" : "err"}
              href="/pms-interfaces"
              hint={
                !pms.available || pms.active_interfaces === 0
                  ? "No PMS connection is in use. Guests sign in with vouchers or accounts."
                  : so.available && so.total > 0
                    ? `${Math.round((so.verified / so.total) * 100)}% of ${fmtInt(so.total)} room checks verified`
                    : so.available
                      ? "No room sign-in was attempted in this range."
                      : reasonText(so.reason)
              }
              footer={pms.available && so.available && so.total > 0 ? <Sparkline values={so.series_verified} width={120} token="chart-4" /> : undefined}
            />
          </div>
        )}

        {/* ---------------------------------------------------------------- row 3 */}
        <div className="grid gap-4 lg:grid-cols-2">
          <TrafficCard snap={snap} />
          <ConcurrencyCard snap={snap} />
        </div>

        {/* ---------------------------------------------------------------- row 4 */}
        <div className="grid gap-4 lg:grid-cols-2">
          <SignInOutcomesCard snap={snap} />
          <SignInPatternCard snap={snap} />
        </div>

        {/* ---------------------------------------------------------------- row 5 */}
        <div className="grid gap-4 lg:grid-cols-2">
          <PackagesCard snap={snap} />
          <PmsCard snap={snap} canCharges={surfaceAvailable(caps, "financial-review")} />
        </div>

        {/* ---------------------------------------------------------------- row 6 */}
        <NetworksCard snap={snap} />

        {/* ---------------------------------------------------------------- row 7 */}
        <div className="grid gap-4 lg:grid-cols-2 2xl:grid-cols-3">
          <Card className="min-w-0">
            <CardHeader>
              <div className="min-w-0">
                <CardTitle>Services</CardTitle>
                <CardDescription className="mt-0.5 text-xs">
                  What this appliance runs, as its health monitor last saw it.
                </CardDescription>
              </div>
              <Link href="/health" className="shrink-0 text-xs text-muted-foreground hover:text-foreground">
                Diagnostics →
              </Link>
            </CardHeader>
            <CardBody className="space-y-3">
              <ServicesGrid snap={snap} />
              <Separator />
              {!health ? (
                <Skeleton className="h-16" />
              ) : (
                <>
                  <ServiceRow title="Site database" info={describeDatabase(!!health.db)} />
                  <ServiceRow title="Session controller" info={describeSessionController(!!health.scd)} />
                  {/*
                    THE CLOUD IS NOT A RUNTIME DEPENDENCY OF THIS SITE WHILE IT IS LICENSING-ONLY, SO IT IS NOT
                    LISTED AS ONE. If the mode is ever something else, the cloud is a live dependency and comes
                    back as an ordinary row.
                  */}
                  {outbox.headline !== "Licensing only" && (
                    <ServiceRow title="Reporting to the StayConnect cloud" info={outbox} href="/appliance?section=license" />
                  )}
                </>
              )}
            </CardBody>
          </Card>
          <DhcpDnsCard snap={snap} />
          <ApplianceCard snap={snap} licenseHeadline={health ? license.headline : undefined} />
        </div>
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
